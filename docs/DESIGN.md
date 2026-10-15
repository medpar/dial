# dial design notes

## Goals

1. Switch the account behind the Claude Code CLI, the Codex CLI, Claude Desktop and Codex Desktop quickly, without logging in again.
2. Run several accounts at the same time.
3. Keep remote machines on the same account as the laptop.
4. Show usage limits and resets for every account.
5. Stay cheap: no resident daemon, a tiny menu bar app, no dependencies.

## Architecture

```text
Dial.app (Swift, AppKit + SwiftUI popover)            ~27 MB footprint
   │  runs `dial … --json`, renders the JSON
   ▼
dial (Go, stdlib only)                                single static binary
   ├── vault       ~/.dial/profiles/<provider>/<id>
   ├── claude      keychain / .credentials.json, ~/.claude.json, OAuth refresh, /api/oauth/usage
   ├── codex       auth.json, OAuth refresh, wham/usage
   ├── desktop     Electron app control, Claude sign-in slices, Codex instance homes
   ├── remote      ssh → `dial remote-agent info|apply` (JSON over stdio)
   └── core        import, reconcile, switch, refresh policy, usage cache
```

All account logic lives in the CLI, so the menu bar app and the terminal always agree. The app shells out for every action. A `dial status --json` run from cache takes about 15 ms.

## Credentials and token families

Both providers use OAuth with rotating refresh tokens.

- A refresh returns a new refresh token and invalidates the old one (and, for OpenAI, can revoke the whole family on reuse).
- Therefore **a login must have exactly one active refresher**.

dial follows these rules:

- **Single source of truth.** The vault holds each account's credentials. The live CLI location holds a copy of the active account only.
- **Reconcile before switching.**
  - Before writing a new account into the live location, dial reads what is there.
  - If it matches a vault profile and is newer (Codex `last_refresh`, Claude `expiresAt`), it is saved back.
  - An unknown live login is imported.
- **Lineage.**
  - Every login has a random lineage id.
  - Copies with the same lineage are the same family and merge newest-wins.
  - A different lineage (a fresh login) replaces the old family.
  - A remote never overwrites its own family with a pushed copy unless asked to (`seed`).
- **Refresh only what is idle.**
  - dial refreshes an account only when it is not live in the CLI, not open in a window and not in a running `dial run`.
  - Exception: dial refreshes a live account itself only when no CLI process is running, and then writes the result back to the live location too.
  - Refreshes hold the global lock, compare-and-swap against the stored copy, and back off for 11 minutes after a failure.
- **Per-machine families for remotes.**
  - `dial remote seed` runs the provider's login in a throwaway home on the laptop and pushes the result to the remote as the remote's own family.
  - After that, a switch sends only the account key.

## Identity of a live Claude login

Claude Code keeps the login (keychain item or `.credentials.json`) apart from the account it belongs to (`oauthAccount` in `.claude.json`). Claude Desktop's built-in Claude Code rewrites `~/.claude.json` from every window with that window's account. Trusting it once led dial to:

- treat the CLI's real login as idle and refresh its stored copy, which revoked the keychain copy, after which Claude Code blanked the login; and
- attribute a login to the wrong profile.

Identity is now derived from the credential itself:

1. an exact token match with a stored profile; else
2. `GET /api/oauth/profile` with the access token, cached in `~/.dial/identity.json` until the token expires; else
3. unknown.

An unknown login is never synced back and never refreshed by dial. Liveness ("is this profile the CLI's login?") uses the same identity, so dial never rotates a token family the keychain also holds without writing the result back.

## Claude Code specifics

- **macOS keychain item:** service `Claude Code-credentials` for the default config dir, or `Claude Code-credentials-<sha256(CLAUDE_CONFIG_DIR)[:8]>` for a custom one. The account is `$USER`.
- **Linux:** `<config dir>/.credentials.json`.
- **Item contents:** the JSON contains `claudeAiOauth` plus other keys (`mcpOAuth`). dial replaces only `claudeAiOauth`.
- **Identity:** `oauthAccount` in `.claude.json`. dial swaps only that key.
- **Running sessions:** Claude Code ≥ 2.1.296 re-reads its credential store when it changes, so open sessions follow a switch. Older versions keep the previous login in memory until restarted.
- **Refresh:** `POST https://platform.claude.com/v1/oauth/token` with `grant_type=refresh_token`, `client_id` and `scope`.
- **Usage:** `GET https://api.anthropic.com/api/oauth/usage` with `anthropic-beta: oauth-2025-04-20`. Current responses list every limit in `limits[]` (session, weekly_all, weekly_scoped per model). Older ones use `five_hour` / `seven_day*`.

## Codex specifics

- **Auth file:** `$CODEX_HOME/auth.json`, shared by the CLI and the desktop app.
- **Identity:** taken from the `id_token` claims. The key is `chatgpt_user_id` + `chatgpt_account_id`, because one user can have several workspaces.
- **Refresh:** `POST https://auth.openai.com/oauth/token` with JSON `{client_id, grant_type, refresh_token, scope}`.
- **Usage:** `GET https://chatgpt.com/backend-api/wham/usage` with `ChatGPT-Account-Id`. Windows are classified by `limit_window_seconds`, not by position.
- **App-server daemon:** Codex CLI ≥ 0.16x keeps a managed app-server daemon (`$CODEX_HOME/app-server-daemon/daemon.pid`) that holds the login in memory. After a switch, dial stops it if no interactive `codex` session is running; it restarts on demand.
- **Desktop app:** honours `CODEX_HOME`, `CODEX_ELECTRON_USER_DATA_PATH` and `--user-data-dir`, so a second instance is `open -n --env … -a ChatGPT.app --args --user-data-dir=…`.
- **Instance homes:** these symlink your shared setup from `~/.codex` (config, skills, rules, memories, plugins) but never SQLite files or `sessions/`. Concurrent app-servers on one WAL database stall, and shared rollout files would surface threads the instance's own database doesn't know.
- **Instance state:** a new home inherits only plain preferences and remote host definitions from `.codex-global-state.json`. The rest of that file is account-bound (remote-control enrollments and environment ids, push tokens, cached account ids) or keyed by the main home's projects and threads.

## Claude Desktop specifics

**The sign-in slice** consists of:

- `Cookies`, `Cookies-journal`, `DIPS`, `DIPS-wal`, `fcache`, `Local Storage`, `Session Storage`, `IndexedDB` and `Network Persistent State`
- the `config.json` keys `oauth:tokenCacheV2`, `oauth:tokenCache` and `lastKnownAccountUuid`

**Where slices live.** Every account's slice lives in exactly one place: the app's data dir (live), or `~/.dial/desktop/claude/<accountUuid>` (parked).

**Switching:**

1. Quit the app: AppleScript when it is the only instance, otherwise SIGTERM, which Chromium handles gracefully.
2. Move the current slice out.
3. Move the target slice in.
4. Relaunch.

Moves are renames on the same volume, so they are instant. A failure rolls back.

**Adding an account.** Claude Desktop completes Google and email sign-ins through `claude://…/login/…` links that carry a token only the instance that started the sign-in accepts ("Google sign-in code does not answer a sign-in this app started; ignoring"). macOS delivers the link to a single running instance, so with several windows open the sign-in is often lost. dial therefore:

1. Quits every Claude instance.
2. Launches one instance on a fresh `.signin-*` data dir.
3. Polls its `config.json` until a login is present and stable.
4. Quits it and adopts the dir as that account's home.
5. Relaunches the instances that were open, plus the new account's window.

dial never logs out, because logging out would revoke the saved session server-side.

**Launching.**

- Instances are always started with `open -n`. A plain `open -a` merely focuses whichever instance of the bundle already runs, which is how a relaunched main window used to come back as another account's window.
- An instance whose data dir already has a process is focused, never launched twice. Claude Desktop takes no single-instance lock, and two processes on one data dir fight over its cookie and LevelDB stores.
- The main window is started before other windows (`desktop_main_first`), because the Dock binds its pinned Claude icon to the first instance it sees.
- Focusing targets one exact process: Dial.app runs `NSRunningApplication.activate` (`Dial --activate-pid`), with System Events as the fallback.

**Dock launchers.** `dial dock` makes `~/Applications/Dial/<Provider> · <account>.app`, one per account. Each is a copy of a tiny template app (`Contents/Helpers/DialLauncher.app`) with its own Info.plist target and a generated icon, ad-hoc signed. On click it asks `dial desktop where`, activates that pid or runs `dial desktop show`, then quits. `--replace` swaps the generic Claude/ChatGPT Dock items for the launchers (via `CFPreferences` on `com.apple.dock`).

**Switch semantics.** A CLI switch never moves accounts between windows. `dial use --show` brings the account's own window forward; only `dial desktop use` swaps the main window's account.

**Detection is by content.** Each running instance's account is read from its own `config.json`, never inferred from the directory name. Idle homes whose content no longer matches their name, for example because someone signed a different account into a window, are renamed to match.

**Separate windows.** A parked directory is a valid user-data dir. `open -n -a Claude --args --user-data-dir=<dir>` runs it as a separate instance. Electron's single-instance lock is per data dir, and the "Claude Safe Storage" key is shared, so cookies decrypt in any instance.

**Remote sessions.** Claude Desktop's SSH remote sessions receive their token from the app (`CLAUDE_CODE_OAUTH_TOKEN`). Claude Code's own Remote Control on a remote host uses that host's login, which is what `dial remote` keeps in step.

## Usage cache

- **Location:** `~/.dial/usage.json`, keyed by `provider:id`.
- **Freshness:** reports younger than `usage_ttl_seconds` (default 120) are reused.
- **Fetching:** stale reports are fetched concurrently with 150 ms staggering.
- **Errors:**
  - A failed fetch returns the last good report, marked stale.
  - A 429 pauses that account for 5 minutes.
  - A 401 triggers one refresh and one retry.

## Security

- **Storage:**
  - Credentials stay on the machine: files are 0600 inside 0700 directories, and dial's keychain writes go through `/usr/bin/security` so the items keep an ACL Claude Code can read without prompts.
  - Nothing is sent anywhere except the providers' own endpoints and the ssh remotes you configure.
- **Remote agent:** reads JSON from stdin and never prints credentials. Error messages from HTTP bodies are filtered to avoid echoing tokens.
- **Logins:** `dial add` uses the official CLIs' login commands with a throwaway home. dial never sees passwords.
