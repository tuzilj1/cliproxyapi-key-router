// Package main implements a CLIProxyAPI scheduler plugin that routes each
// request by the caller's client API key. Every user has primary accounts and
// fallback accounts (configured as access) assigned to them. Primary accounts
// are used first; when none of them is available (quota cooldown, error,
// disabled), the fallback accounts are used. Other accounts are never used for
// that user: the pick is rejected instead.
package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	void* call;
	void* free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);
*/
import "C"

import (
	_ "embed"

	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

const pluginVersion = "0.4.0"

// configUIPath is the resource path shown as a menu page in the management panel,
// served at /v0/resource/plugins/key-router/config.
const configUIPath = "/config"

//go:embed ui.html
var configUI []byte

var currentConfig atomic.Value

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

// userConfig binds client API keys to the accounts one person may use.
// Account rules are case-insensitive and come in two forms:
//   - "provider:identity" (written by the config page): the auth provider must
//     equal provider and the auth ID (relative auth file name) must contain
//     identity, usually the account e-mail, at a token boundary. This survives
//     re-login, which may produce a new file name prefix.
//   - anything else: a substring of the auth ID or the auth "note" attribute.
type userConfig struct {
	Name    string   `yaml:"name"`
	APIKeys []string `yaml:"api-keys"`
	// Primary accounts are preferred.
	Primary []string `yaml:"primary"`
	// Access accounts are used once no primary account is available.
	Access []string `yaml:"access"`
	// Auths is the v0.2 spelling of Primary. A legacy user without Primary and
	// Access keeps the old behaviour: fall back to any available account.
	Auths []string `yaml:"auths"`
}

type accountTier int

const (
	tierNone accountTier = iota
	tierAccess
	tierPrimary
)

type userRules struct {
	primary []string
	access  []string
	// unrestricted users fall back to built-in routing instead of being rejected.
	unrestricted bool
}

type pluginConfig struct {
	Debug bool         `yaml:"debug"`
	Users []userConfig `yaml:"users"`
}

// routingTable is the compiled form of pluginConfig.
type routingTable struct {
	debug   bool
	byKey   map[string]*userConfig
	byScope map[string]*userConfig
	rules   map[*userConfig]userRules
}

type registration struct {
	SchemaVersion uint32                 `json:"schema_version"`
	Metadata      pluginapi.Metadata     `json:"metadata"`
	Capabilities  registrationCapability `json:"capabilities"`
}

type registrationCapability struct {
	Scheduler                 bool `json:"scheduler"`
	SchedulerAcrossPriorities bool `json:"scheduler_across_priorities"`
	ManagementAPI             bool `json:"management_api"`
}

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(_ *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required"))
		return 1
	}
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, errHandle := handleMethod(C.GoString(method), requestBytes)
	if errHandle != nil {
		writeResponse(response, errorEnvelope("plugin_error", errHandle.Error()))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, len C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		if errConfigure := configure(request); errConfigure != nil {
			return nil, errConfigure
		}
		return okEnvelope(pluginRegistration())
	case pluginabi.MethodSchedulerPick:
		return pickAuth(request)
	case pluginabi.MethodManagementRegister:
		return okEnvelope(pluginapi.ManagementRegistrationResponse{
			Resources: []pluginapi.ResourceRoute{{
				Path:        configUIPath,
				Menu:        "Key router",
				Description: "Assign client API keys and own accounts to users.",
			}},
		})
	case pluginabi.MethodManagementHandle:
		return handleManagement(request)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func configure(raw []byte) error {
	var req lifecycleRequest
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
			return errUnmarshal
		}
	}
	var cfg pluginConfig
	if len(req.ConfigYAML) > 0 {
		if errUnmarshal := yaml.Unmarshal(req.ConfigYAML, &cfg); errUnmarshal != nil {
			return errUnmarshal
		}
	}
	table, errCompile := compile(cfg)
	if errCompile != nil {
		return errCompile
	}
	currentConfig.Store(table)
	logf(table, "configured %d user(s)", len(cfg.Users))
	return nil
}

func compile(cfg pluginConfig) (*routingTable, error) {
	table := &routingTable{
		debug:   cfg.Debug,
		byKey:   make(map[string]*userConfig),
		byScope: make(map[string]*userConfig),
		rules:   make(map[*userConfig]userRules),
	}
	for i := range cfg.Users {
		user := &cfg.Users[i]
		user.Name = strings.TrimSpace(user.Name)
		if user.Name == "" {
			user.Name = fmt.Sprintf("user-%d", i+1)
		}
		for _, key := range user.APIKeys {
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			if other, exists := table.byKey[key]; exists && other != user {
				return nil, fmt.Errorf("api key is assigned to both %q and %q", other.Name, user.Name)
			}
			table.byKey[key] = user
			table.byScope[callerScope(key)] = user
		}
		rules := userRules{primary: normalizeRules(user.Primary), access: normalizeRules(user.Access)}
		if len(user.Primary) == 0 && len(user.Access) == 0 && len(user.Auths) > 0 {
			rules.primary = normalizeRules(user.Auths)
			rules.unrestricted = true
		}
		table.rules[user] = rules
	}
	return table, nil
}

func normalizeRules(raw []string) []string {
	out := make([]string, 0, len(raw))
	for _, rule := range raw {
		if rule = strings.ToLower(strings.TrimSpace(rule)); rule != "" {
			out = append(out, rule)
		}
	}
	return out
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:    "key-router",
			Version: pluginVersion,
			Author:  "jirka",
			// Required by the host; the plugin is local, so point at the upstream project.
			GitHubRepository: "https://github.com/router-for-me/CLIProxyAPI",
			ConfigFields: []pluginapi.ConfigField{
				{
					Name:        "users",
					Type:        pluginapi.ConfigFieldTypeArray,
					Description: "List of {name, api-keys, primary, access}: requests with one of api-keys use primary accounts first, then access accounts, never others.",
				},
				{
					Name:        "debug",
					Type:        pluginapi.ConfigFieldTypeBoolean,
					Description: "Log every routing decision to stderr.",
				},
			},
		},
		Capabilities: registrationCapability{
			Scheduler:                 true,
			SchedulerAcrossPriorities: true,
			ManagementAPI:             true,
		},
	}
}

func pickAuth(raw []byte) ([]byte, error) {
	var req pluginapi.SchedulerPickRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	table := loadedConfig()
	if table == nil || len(table.byKey) == 0 {
		return unhandled()
	}

	user := table.userFor(req.Options)
	if user == nil {
		logf(table, "model=%s: unknown client key, built-in routing", req.Model)
		return unhandled()
	}

	var primary, access []pluginapi.SchedulerAuthCandidate
	for _, candidate := range req.Candidates {
		switch table.tier(user, candidate) {
		case tierPrimary:
			primary = append(primary, candidate)
		case tierAccess:
			access = append(access, candidate)
		}
	}
	if pick, ok := best(primary); ok {
		logf(table, "user=%s model=%s: primary %s", user.Name, req.Model, pick)
		return okEnvelope(pluginapi.SchedulerPickResponse{AuthID: pick, Handled: true})
	}
	if pick, ok := best(access); ok {
		logf(table, "user=%s model=%s: no primary available, access %s", user.Name, req.Model, pick)
		return okEnvelope(pluginapi.SchedulerPickResponse{AuthID: pick, Handled: true})
	}
	if table.rules[user].unrestricted {
		logf(table, "user=%s model=%s: no own account available, built-in routing over %d candidate(s)", user.Name, req.Model, len(req.Candidates))
		return unhandled()
	}
	logf(table, "user=%s model=%s: rejected, none of %d candidate(s) is allowed", user.Name, req.Model, len(req.Candidates))
	return okEnvelope(pluginapi.SchedulerPickResponse{
		Handled:      true,
		Reject:       true,
		RejectCode:   "auth_unavailable",
		RejectReason: fmt.Sprintf("key-router: no primary or access account of user %q is available for model %s (quota exhausted, cooling down or disabled)", user.Name, req.Model),
	})
}

// best picks fill-first: highest priority, then stable ID order.
func best(candidates []pluginapi.SchedulerAuthCandidate) (string, bool) {
	if len(candidates) == 0 {
		return "", false
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Priority != candidates[j].Priority {
			return candidates[i].Priority > candidates[j].Priority
		}
		return candidates[i].ID < candidates[j].ID
	})
	return candidates[0].ID, true
}

// tier classifies an account for a user; primary wins over access.
func (t *routingTable) tier(user *userConfig, candidate pluginapi.SchedulerAuthCandidate) accountTier {
	rules := t.rules[user]
	switch {
	case matchesAny(rules.primary, candidate):
		return tierPrimary
	case matchesAny(rules.access, candidate):
		return tierAccess
	default:
		return tierNone
	}
}

func matchesAny(rules []string, candidate pluginapi.SchedulerAuthCandidate) bool {
	id := strings.ToLower(candidate.ID)
	note := strings.ToLower(candidate.Attributes["note"])
	provider := strings.ToLower(candidate.Provider)
	for _, rule := range rules {
		if ruleProvider, identity, scoped := strings.Cut(rule, ":"); scoped {
			if ruleProvider == provider && containsToken(id, identity) {
				return true
			}
			continue
		}
		if strings.Contains(id, rule) || (note != "" && strings.Contains(note, rule)) {
			return true
		}
	}
	return false
}

// containsToken reports whether s contains token not preceded by a character
// that could belong to it, so "a@x.cz" does not match "ba@x.cz".
func containsToken(s, token string) bool {
	if token == "" {
		return false
	}
	for offset := 0; ; {
		idx := strings.Index(s[offset:], token)
		if idx < 0 {
			return false
		}
		at := offset + idx
		if at == 0 || !isIdentityChar(s[at-1]) {
			return true
		}
		offset = at + 1
	}
}

func isIdentityChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.IndexByte("._%+", c) >= 0
}

// userFor identifies the caller. The host passes caller_scope metadata, a hash
// of the client key that passed proxy authentication (any source, including
// ?key=). Request headers are a fallback for hosts without caller_scope.
func (t *routingTable) userFor(opts pluginapi.SchedulerOptions) *userConfig {
	if scope, _ := opts.Metadata["caller_scope"].(string); scope != "" {
		if user := t.byScope[scope]; user != nil {
			return user
		}
	}
	get := func(name string) string {
		for key, values := range opts.Headers {
			if strings.EqualFold(key, name) && len(values) > 0 {
				return strings.TrimSpace(values[0])
			}
		}
		return ""
	}
	bearer := get("Authorization")
	if len(bearer) > 7 && strings.EqualFold(bearer[:7], "bearer ") {
		bearer = strings.TrimSpace(bearer[7:])
	}
	for _, key := range []string{bearer, get("X-Goog-Api-Key"), get("X-Api-Key")} {
		if user := t.byKey[key]; key != "" && user != nil {
			return user
		}
	}
	return nil
}

// callerScope mirrors sdk/cliproxy/session.CallerScope.
func callerScope(key string) string {
	sum := sha256.Sum256([]byte("cli-proxy-api:caller-scope:v1\x00" + key))
	return hex.EncodeToString(sum[:])
}

// handleManagement serves the configuration page. The resource route is not
// management-authenticated, so the page holds no data: it asks for the
// management key and talks to the regular Management API from the browser.
func handleManagement(raw []byte) ([]byte, error) {
	var req pluginapi.ManagementRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	if req.Method != http.MethodGet || !strings.HasSuffix(strings.TrimRight(req.Path, "/"), configUIPath) {
		return okEnvelope(pluginapi.ManagementResponse{StatusCode: http.StatusNotFound})
	}
	return okEnvelope(pluginapi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers: http.Header{
			"Content-Type":  {"text/html; charset=utf-8"},
			"Cache-Control": {"no-store"},
		},
		Body: configUI,
	})
}

func unhandled() ([]byte, error) {
	return okEnvelope(pluginapi.SchedulerPickResponse{Handled: false})
}

func loadedConfig() *routingTable {
	table, _ := currentConfig.Load().(*routingTable)
	return table
}

func logf(table *routingTable, format string, args ...any) {
	if table == nil || !table.debug {
		return
	}
	fmt.Fprintf(os.Stderr, "[key-router] "+format+"\n", args...)
}

func okEnvelope(v any) ([]byte, error) {
	raw, errMarshal := json.Marshal(v)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return json.Marshal(envelope{OK: true, Result: raw})
}

func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	return raw
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}
