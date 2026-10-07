Vytvořeno: 2026-10-07
Upraveno: 2026-10-07

# key-router – plugin pro CLIProxyAPI

Plugin pro [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) (v8), který vybírá upstream účet (Claude, Codex, Antigravity, Gemini…) podle **klientského API klíče**. Hodí se, když proxy sdílí víc lidí: každý jede primárně na svém předplatném a teprve po vyčerpání limitů na účtech, ke kterým mu ostatní dali přístup.

Obsahuje i **grafickou konfiguraci** přímo v management panelu CLIProxyAPI.

## Jak to funguje

Každý uživatel má jeden nebo víc klientských klíčů (`access.api-keys`) a u každého přihlášeného účtu jednu ze tří rolí:

| Role | Chování |
|---|---|
| **Primary** | Používá se jako první. Při více primárních účtech vyhraje vyšší `priority`, pak abecedně podle ID. |
| **Access** | Použije se, až když žádný primární účet není dostupný (vyčerpaný limit / cooldown, chyba, vypnutý účet). |
| **No access** | Pro daného uživatele se nepoužije nikdy. |

Když pro uživatele není dostupný žádný Primary ani Access účet, plugin dotaz odmítne chybou
`auth_unavailable: key-router: no primary or access account of user "…" is available for model …`.

Dotazy s klíčem, který není přiřazený žádnému uživateli, jdou přes výchozí routování CLIProxyAPI (`routing.strategy`).

Přepnutí na Access účet nastane ve chvíli, kdy CLIProxyAPI označí primární účet za nedostupný. Typicky po odpovědi poskytovatele o vyčerpaném limitu (429), kdy se účet dostane do cooldownu. Plugin limity dopředu nepředvídá.

### Identifikace uživatele

CLIProxyAPI předává scheduler pluginům metadata `caller_scope`, tj. hash klientského klíče, který prošel ověřením. Funguje tak pro `Authorization: Bearer`, `x-api-key`, `x-goog-api-key` i `?key=`. Jako záloha se čtou hlavičky požadavku.

### Pravidla účtů

Konfigurační stránka ukládá u každého účtu pravidlo `poskytovatel:e-mail`, např. `claude:jan@example.com`:

- poskytovatel účtu se musí shodovat,
- název auth souboru musí obsahovat e-mail na hranici tokenu (`a@x.cz` nesedí na `ba@x.cz`),
- pravidlo přežije odhlášení a nové přihlášení účtu (CLIProxyAPI pak může vytvořit soubor s jiným náhodným prefixem, např. `claude-8f425b23-…`),
- účty bez e-mailu se ukládají názvem auth souboru.

Pravidlo bez dvojtečky je podřetězec názvu auth souboru nebo poznámky (`note`) účtu (starší formát). Stránka ho při načtení převede na pravidla pro jednotlivé účty.

## Grafická konfigurace

Plugin přidává do management panelu položku menu **Key router** (stránka `/v0/resource/plugins/key-router/config`, zdroj `ui.html` je vložený přímo v `.so`). Stránka umí:

- přidat, přejmenovat a odebrat uživatele,
- přiřadit existující klientský klíč nebo vygenerovat nový (při uložení se přidá i do `access.api-keys`),
- nastavit u každého přihlášeného účtu roli Primary / Access / No access,
- zapnout nebo vypnout plugin a debug log,
- zobrazit přehled účtů a rolí všech uživatelů.

Ukládá přes `PUT /v0/management/plugins/key-router/config` a CLIProxyAPI plugin hned přenačte.

CLIProxyAPI servíruje stránky pluginů bez ověření a přihlášení z panelu jim nepředává. Stránka proto žádná data neobsahuje: při prvním otevření si řekne o **management klíč**, uloží ho v prohlížeči a vše načítá z management API. Pozor: CLIProxyAPI po 5 neúspěšných pokusech o management klíč zablokuje IP na 30 minut (ban se drží jen v paměti, zruší ho restart).

Nově přihlášený účet má u všech uživatelů „No access“, dokud mu roli nenastavíš.

## Instalace

1. **Sestavení** (potřebuje Docker; výstup `out/key-router.so` pro linux/amd64):

   ```sh
   CPA_VERSION=v8.0.17 ./build.sh test
   ```

   `build.sh` kompiluje proti zdrojákům CLIProxyAPI v sousední složce `../CLIProxyAPI` (viz `replace` v `go.mod`). Když chybí, naklonuje do ní tag `CPA_VERSION`. Verze musí odpovídat nasazenému CLIProxyAPI. Build probíhá v `golang:1.26-bookworm`, tedy se stejnou glibc jako oficiální image (`debian:bookworm`).

2. **Nasazení:** zkopíruj `out/key-router.so` do adresáře pluginů CLIProxyAPI (v oficiálním docker-compose `./plugins` → `/CLIProxyAPI/plugins`) a v `config.yaml` zapni pluginy:

   ```yaml
   plugins:
       enabled: true
       dir: "plugins"
       configs:
           key-router:
               enabled: true
               priority: 1
               debug: false          # true = logovat každé rozhodnutí do stderr (docker logs), bez klíčů
               users:
                   - name: jan
                     api-keys: ["sk-…"]
                     primary: ["claude:jan@example.com", "antigravity:jan@example.com"]
                     access: ["claude:petr@example.com"]
                   - name: petr
                     api-keys: ["sk-…"]
                     primary: ["claude:petr@example.com"]
                     access: ["antigravity:jan@example.com"]
   ```

   Klíče musí být zároveň v `access.api-keys`. Jeden klíč nesmí patřit dvěma uživatelům (konfigurace se odmítne).

3. Restartuj CLIProxyAPI a v logu zkontroluj `pluginhost: plugin loaded plugin_id=key-router` bez varování `invalid metadata`. Uživatele a role pak jde spravovat na stránce **Key router** v panelu.

## Kompatibilita

- Sestaveno a otestováno proti CLIProxyAPI **v8.0.17** (plugin ABI 1, RPC schema 6).
- Oficiální image má `pull_policy: always`, takže se při restartu může aktualizovat. Po aktualizaci zkontroluj načtení pluginu v logu. Při změně `pluginabi.ABIVersion` / `SchemaVersion` plugin sestav znovu proti nové verzi (`CPA_VERSION=… ./build.sh`, předtím smaž `../CLIProxyAPI`).
- Konfigurace ve formátu v0.2 (jen `auths`, bez `primary`/`access`) dál funguje: `auths` = primary, jinak výchozí routování.

## Vývoj

- `main.go`: C ABI plugin (scheduler + management resource), routování a pravidla.
- `ui.html`: konfigurační stránka, bez závislostí.
- `router_test.go`: testy routování, pravidel a servírování stránky (`./build.sh test`).
