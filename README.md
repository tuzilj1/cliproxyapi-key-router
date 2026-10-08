# key-router – CLIProxyAPI plugin

A plugin for [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) (v8) that picks the upstream account (Claude, Codex, Antigravity, Gemini…) based on the **client API key**. It is meant for a proxy used by several people with their own client keys. Each user has a set of primary accounts that are used first and a set of fallback accounts that are used only when the primary ones are unavailable. The administrator assigns these sets per user in the configuration; accounts that are not assigned to a user are never used for that user.

It also ships a **configuration page** inside the CLIProxyAPI management panel.

## How it works

Each user has one or more client keys (from `access.api-keys`) and one of three roles for every logged-in account:

| Role | Behaviour |
|---|---|
| **Primary** | Used first. With several primary accounts, the higher `priority` wins, then the auth ID in alphabetical order. |
| **Access** (fallback) | Used only when no primary account is available (quota exhausted / cooldown, error, disabled account). The configuration key is still `access`. |
| **No access** | Never used for this user. |

When neither a primary nor an access account is available, the plugin rejects the request with
`auth_unavailable: key-router: no primary or access account of user "…" is available for model …`.

Requests with a key that is not assigned to any user go through the built-in CLIProxyAPI routing (`routing.strategy`).

The switch to an access account happens when CLIProxyAPI marks the primary account as unavailable. This is typically after the provider answers with a quota error (429) and the account enters cooldown. The plugin does not predict limits in advance.

### Identifying the user

CLIProxyAPI passes `caller_scope` metadata to scheduler plugins. It is a hash of the client key that passed authentication, so identification works for `Authorization: Bearer`, `x-api-key`, `x-goog-api-key` and `?key=`. Request headers are used as a fallback.

### Account rules

The configuration page stores a `provider:e-mail` rule for each account, e.g. `claude:jane@example.com`:

- the account provider must match,
- the auth file name must contain the e-mail at a token boundary (`a@x.com` does not match `ba@x.com`),
- the rule survives logging the account out and in again (CLIProxyAPI may then create a file with a different random prefix, e.g. `claude-8f425b23-…`),
- accounts without an e-mail are stored by their auth file name.

A rule without a colon is a substring of the auth file name or of the account `note` (the older format). The page converts such rules to per-account rules when it loads.

## Configuration page

The plugin adds a **Key router** menu item to the management panel. The page is served at `/v0/resource/plugins/key-router/config`, and its source, `ui.html`, is embedded in the `.so`. On the page you can:

- add, rename and remove users,
- assign an existing client key or generate a new one (on save, a new key is also added to `access.api-keys`),
- set the Primary / Access / No access role of every logged-in account,
- enable or disable the plugin and its debug log,
- see an overview of all accounts and every user's role.

Changes are saved through `PUT /v0/management/plugins/key-router/config`, and CLIProxyAPI reloads the plugin right away.

CLIProxyAPI serves plugin pages without authentication and does not pass the panel login to them. The page therefore contains no data itself. On first use it asks for the **management key**, remembers it in the browser and loads everything from the management API. Note: after 5 failed management key attempts, CLIProxyAPI blocks the IP for 30 minutes. The ban is kept in memory only, so a restart clears it.

A newly logged-in account has "No access" for every user until you assign it a role.

## Installation

1. **Build** (requires Docker; produces `out/key-router.so` for linux/amd64):

   ```sh
   CPA_VERSION=v8.0.17 ./build.sh test
   ```

   `build.sh` compiles against the CLIProxyAPI source in the sibling directory `../CLIProxyAPI` (see the `replace` directive in `go.mod`). If the directory is missing, it clones the `CPA_VERSION` tag there. The version must match the CLIProxyAPI you run. The build runs in `golang:1.26-bookworm`, so it links against the same glibc as the official image (`debian:bookworm`).

2. **Deploy:** copy `out/key-router.so` into the CLIProxyAPI plugin directory (`./plugins` → `/CLIProxyAPI/plugins` in the official docker-compose setup) and enable plugins in `config.yaml`:

   ```yaml
   plugins:
       enabled: true
       dir: "plugins"
       configs:
           key-router:
               enabled: true
               priority: 1
               debug: false          # true = log every routing decision to stderr (docker logs), keys are never logged
               users:
                   - name: jane
                     api-keys: ["sk-…"]
                     primary: ["claude:jane@example.com", "antigravity:jane@example.com"]
                     access: ["claude:john@example.com"]
                   - name: john
                     api-keys: ["sk-…"]
                     primary: ["claude:john@example.com"]
                     access: ["antigravity:jane@example.com"]
   ```

   The keys must also be listed in `access.api-keys`. A key cannot belong to two users; such a configuration is rejected.

3. Restart CLIProxyAPI and check the log for `pluginhost: plugin loaded plugin_id=key-router` without an `invalid metadata` warning. Users and roles can then be managed on the **Key router** page in the panel.

## Compatibility

- Built and tested against CLIProxyAPI **v8.0.17** (plugin ABI 1, RPC schema 6).
- The official image uses `pull_policy: always`, so it may update on restart. After an update, check that the plugin still loads. If `pluginabi.ABIVersion` or `SchemaVersion` changes, rebuild against the new version: delete `../CLIProxyAPI`, then run `CPA_VERSION=… ./build.sh`.
- Configuration in the v0.2 format (only `auths`, without `primary`/`access`) still works: `auths` act as primary accounts, otherwise the built-in routing applies.

## Development

- `main.go`: the C ABI plugin (scheduler and management resource), routing and rules.
- `ui.html`: the configuration page, no dependencies.
- `router_test.go`: tests for routing, rules and page serving (`./build.sh test`).

## License

[MIT](LICENSE)
