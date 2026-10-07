package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const testConfig = `
users:
  - name: jirka
    api-keys: ["key-j"]
    primary: ["jirka@"]
    access: ["matyas@"]
  - name: matyas
    api-keys: ["key-m"]
    primary: ["matyas@", "matyas-note"]
  - name: legacy
    api-keys: ["key-l"]
    auths: ["legacy@"]
`

func configureForTest(t *testing.T) {
	t.Helper()
	raw, _ := json.Marshal(lifecycleRequest{ConfigYAML: []byte(testConfig)})
	if err := configure(raw); err != nil {
		t.Fatalf("configure: %v", err)
	}
}

func pick(t *testing.T, opts pluginapi.SchedulerOptions, candidates ...pluginapi.SchedulerAuthCandidate) pluginapi.SchedulerPickResponse {
	t.Helper()
	raw, _ := json.Marshal(pluginapi.SchedulerPickRequest{Model: "m", Options: opts, Candidates: candidates})
	out, err := pickAuth(raw)
	if err != nil {
		t.Fatalf("pickAuth: %v", err)
	}
	var env envelope
	if err := json.Unmarshal(out, &env); err != nil || !env.OK {
		t.Fatalf("bad envelope %s: %v", out, err)
	}
	var resp pluginapi.SchedulerPickResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatalf("bad result: %v", err)
	}
	return resp
}

var (
	authJ     = pluginapi.SchedulerAuthCandidate{ID: "claude-jirka@example.com.json", Provider: "claude"}
	authM     = pluginapi.SchedulerAuthCandidate{ID: "claude-matyas@example.com.json", Provider: "claude"}
	authNote  = pluginapi.SchedulerAuthCandidate{ID: "codex-x.json", Provider: "codex", Attributes: map[string]string{"note": "Matyas-Note"}}
	authOther = pluginapi.SchedulerAuthCandidate{ID: "claude-stranger@example.com.json", Provider: "claude"}
)

func scope(key string) pluginapi.SchedulerOptions {
	return pluginapi.SchedulerOptions{Metadata: map[string]any{"caller_scope": callerScope(key)}}
}

func TestOwnCredentialPreferred(t *testing.T) {
	configureForTest(t)
	if got := pick(t, scope("key-j"), authM, authJ); !got.Handled || got.AuthID != authJ.ID {
		t.Fatalf("jirka: got %+v", got)
	}
	if got := pick(t, scope("key-m"), authJ, authM); !got.Handled || got.AuthID != authM.ID {
		t.Fatalf("matyas: got %+v", got)
	}
}

func TestAccessUsedWhenPrimaryUnavailable(t *testing.T) {
	configureForTest(t)
	if got := pick(t, scope("key-j"), authM, authOther); !got.Handled || got.Reject || got.AuthID != authM.ID {
		t.Fatalf("expected access account, got %+v", got)
	}
}

func TestRejectWhenNothingAllowed(t *testing.T) {
	configureForTest(t)
	// matyas has no access accounts: jirka's account must not be used.
	if got := pick(t, scope("key-m"), authJ, authOther); !got.Handled || !got.Reject || got.AuthID != "" {
		t.Fatalf("expected reject, got %+v", got)
	}
}

func TestPrimaryWinsOverAccess(t *testing.T) {
	raw, _ := json.Marshal(lifecycleRequest{ConfigYAML: []byte("users:\n  - {name: a, api-keys: [k], primary: [both], access: [both, other]}\n")})
	if err := configure(raw); err != nil {
		t.Fatal(err)
	}
	both := pluginapi.SchedulerAuthCandidate{ID: "z-both.json"}
	other := pluginapi.SchedulerAuthCandidate{ID: "a-other.json", Priority: 9}
	if got := pick(t, scope("k"), other, both); got.AuthID != both.ID {
		t.Fatalf("got %+v", got)
	}
}

func TestLegacyAuthsFallBackToBuiltin(t *testing.T) {
	configureForTest(t)
	legacy := pluginapi.SchedulerAuthCandidate{ID: "claude-legacy@example.com.json"}
	if got := pick(t, scope("key-l"), authJ, legacy); got.AuthID != legacy.ID {
		t.Fatalf("legacy primary: got %+v", got)
	}
	if got := pick(t, scope("key-l"), authJ); got.Handled {
		t.Fatalf("legacy fallback should be unhandled, got %+v", got)
	}
}

func TestUnknownKeyUnhandled(t *testing.T) {
	configureForTest(t)
	if got := pick(t, scope("other"), authJ, authM); got.Handled {
		t.Fatalf("expected unhandled, got %+v", got)
	}
}

func TestHeaderFallbackAndNote(t *testing.T) {
	configureForTest(t)
	opts := pluginapi.SchedulerOptions{Headers: map[string][]string{"x-api-key": {"key-m"}}}
	if got := pick(t, opts, authJ, authNote); !got.Handled || got.AuthID != authNote.ID {
		t.Fatalf("got %+v", got)
	}
	opts = pluginapi.SchedulerOptions{Headers: map[string][]string{"Authorization": {"Bearer key-j"}}}
	if got := pick(t, opts, authM, authJ); got.AuthID != authJ.ID {
		t.Fatalf("got %+v", got)
	}
}

func TestPriorityWithinOwn(t *testing.T) {
	configureForTest(t)
	low := pluginapi.SchedulerAuthCandidate{ID: "a-jirka@low.json", Priority: 0}
	high := pluginapi.SchedulerAuthCandidate{ID: "z-jirka@high.json", Priority: 5}
	if got := pick(t, scope("key-j"), low, high); got.AuthID != high.ID {
		t.Fatalf("got %+v", got)
	}
}

func TestDuplicateKeyRejected(t *testing.T) {
	raw, _ := json.Marshal(lifecycleRequest{ConfigYAML: []byte("users:\n  - {name: a, api-keys: [k]}\n  - {name: b, api-keys: [k]}\n")})
	if err := configure(raw); err == nil {
		t.Fatal("expected duplicate key error")
	}
}

func TestConfigPageServed(t *testing.T) {
	handle := func(method, path string) pluginapi.ManagementResponse {
		raw, _ := json.Marshal(pluginapi.ManagementRequest{Method: method, Path: path})
		out, err := handleManagement(raw)
		if err != nil {
			t.Fatalf("handleManagement: %v", err)
		}
		var env envelope
		var resp pluginapi.ManagementResponse
		if err := json.Unmarshal(out, &env); err != nil || json.Unmarshal(env.Result, &resp) != nil {
			t.Fatalf("bad response %s", out)
		}
		return resp
	}
	resp := handle("GET", "/v0/resource/plugins/key-router/config")
	if resp.StatusCode != 200 || !strings.Contains(string(resp.Body), "Key router") {
		t.Fatalf("config page: status %d, %d bytes", resp.StatusCode, len(resp.Body))
	}
	if resp := handle("POST", "/v0/resource/plugins/key-router/config"); resp.StatusCode != 404 {
		t.Fatalf("POST should be 404, got %d", resp.StatusCode)
	}
}

func TestProviderScopedRules(t *testing.T) {
	raw, _ := json.Marshal(lifecycleRequest{ConfigYAML: []byte("users:\n  - {name: a, api-keys: [k], primary: [\"claude:a@x.cz\"], access: [\"antigravity:a@x.cz\"]}\n")})
	if err := configure(raw); err != nil {
		t.Fatal(err)
	}
	claudeA := pluginapi.SchedulerAuthCandidate{ID: "claude-1234abcd-a@x.cz.json", Provider: "claude"}
	claudeRelogin := pluginapi.SchedulerAuthCandidate{ID: "claude-99999999-a@x.cz.json", Provider: "claude"}
	claudeBA := pluginapi.SchedulerAuthCandidate{ID: "claude-1234abcd-ba@x.cz.json", Provider: "claude"}
	agA := pluginapi.SchedulerAuthCandidate{ID: "antigravity-a@x.cz.json", Provider: "antigravity"}
	codexA := pluginapi.SchedulerAuthCandidate{ID: "codex-a@x.cz-plus.json", Provider: "codex"}
	if got := pick(t, scope("k"), agA, claudeA); got.AuthID != claudeA.ID {
		t.Fatalf("primary claude: got %+v", got)
	}
	if got := pick(t, scope("k"), claudeRelogin); got.AuthID != claudeRelogin.ID {
		t.Fatalf("re-login file must still match: got %+v", got)
	}
	if got := pick(t, scope("k"), agA); got.AuthID != agA.ID {
		t.Fatalf("access antigravity: got %+v", got)
	}
	if got := pick(t, scope("k"), claudeBA, codexA); !got.Reject {
		t.Fatalf("other e-mail and other provider must be rejected: got %+v", got)
	}
}

func TestRegistrationMetadataComplete(t *testing.T) {
	meta := pluginRegistration().Metadata
	if meta.Name == "" || meta.Version == "" || meta.Author == "" || meta.GitHubRepository == "" {
		t.Fatalf("host rejects incomplete metadata: %+v", meta)
	}
}
