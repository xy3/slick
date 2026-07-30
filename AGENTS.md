# AGENTS.md

Guidance for AI agents (and humans) working on **slick**. Read this before
making changes.

## What slick is

An utterly minimal wrapper UI for Slack, focused on **quick compose** with a
lightweight **read** layer. A local Go server serves an embedded dark HTML/CSS/JS
page you open in a browser tab. You fuzzy-pick a channel or DM, see recent
history, and send a message. Design intent: dark, monotone, rounded, white
text, one card, no distractions.

It is a **personal tool** that reuses your existing Slack browser session — it
acts as *you*. There is no Slack OAuth app.

## Auth model (important)

slick authenticates exactly like the Slack web client:

- An **`xoxc-` token** (from `localStorage.localConfig_v2` in a Slack tab), sent
  as `Authorization: Bearer <token>`.
- The **`d` cookie** (`xoxd-…`, HttpOnly), sent as `Cookie: d=<value>`.

**Both are required together** — an `xoxc` token alone yields `invalid_auth`.

The user provides these once via the `/setup` page (**paste-once**). They are
stored at `~/.config/slick/config.json` with mode `0600`. On startup slick loads
them and calls `auth.test`; if that fails it falls back to `/setup`.

### Why paste-once (context that constrains this repo)

Auto-extraction from disk was ruled out on the target machine: the session lives
in **Windows Chrome 150**, which uses **App-Bound Encryption** for cookies, so
offline decryption from WSL2 is not feasible. A companion browser extension is
the viable "auto" path if revisited. **Do not** add code that tries to defeat
App-Bound Encryption or scrape encrypted cookie stores.

Slack may rotate the `xoxc` token occasionally; when it does, sends fail with a
friendly error and the user pastes a fresh pair.

## Architecture / layout

```
main.go            Local HTTP server: routing, paste-once auth, config load/save,
                   conversation cache (5 min), error mapping (friendly()).
slack/client.go    Slack Web API client. Auth via xoxc + d cookie. All calls go
                   through call() -> https://slack.com/api/<method> as urlencoded
                   POST. Caches the users directory (usersCache) for name lookup.
slack/client_test.go  Unit test for mrkdwn rendering (renderText).
web/               Embedded (go:embed) UI, served from /static/ and rendered as
                   html/template.
  index.html       Main compose UI.
  setup.html       Paste-once credential form + how-to.
  style.css        Design tokens + all styling (dark, monotone, rounded).
  app.js           Vanilla JS: fuzzy picker, keyboard nav, thread polling, send.
```

No third-party Go dependencies — standard library only. `go.mod` module is
`slick`, Go 1.26.

## HTTP endpoints

| Route                | Method | Purpose                                         |
|----------------------|--------|-------------------------------------------------|
| `/`                  | GET    | Compose UI; redirects to `/setup` if unconfigured |
| `/setup`             | GET/POST | Paste-once credentials; POST validates + saves |
| `/static/*`          | GET    | Embedded CSS/JS                                 |
| `/api/conversations` | GET    | JSON list of channels/DMs (cached 5 min)        |
| `/api/history?channel=<id>` | GET | Last ~25 messages, oldest first             |
| `/api/send`          | POST   | `{channel, text}` -> `chat.postMessage`         |

API handlers return `401` when unconfigured and `502` (`friendly()` message) on
Slack errors.

## Slack API methods used

`auth.test`, `conversations.list` (types: public/private/mpim/im, paginated),
`users.list` (paginated, cached), `conversations.history`, `chat.postMessage`.

`slack/client.go` also renders Slack **mrkdwn** to plain text: `<@U…>` mentions
(resolved via the users cache), `<#C…|name>` channels, `<url|text>` links, and
HTML entity unescaping. See `renderText` and its test.

## Frontend behavior

- Page loads focused on the recipient search. Fuzzy match ranks prefix >
  substring > subsequence; top 8 shown. `↑`/`↓` move, `Enter` selects.
- On select: a chip shows the target, recent history renders above the compose
  box, and history **polls every 4s** (and refreshes right after a send).
- `⌘/Ctrl+Enter` sends; `Esc` clears the recipient and stops polling.
- All message content is inserted via `textContent` (never `innerHTML`) — keep
  it that way; message text is untrusted.

## Build / test / run

```sh
go build ./...          # or: go build -o /tmp/slick .
go vet ./...
go test ./...           # renderText coverage
go run .                # http://127.0.0.1:8383
go run . -addr :9000    # custom listen address
```

To exercise the auth/redirect/guard paths without real credentials, run the
server and curl `/`, `/static/...`, `/api/history` (expect `401` unconfigured),
and POST bad creds to `/setup` (real `auth.test` round-trip -> friendly error).
**Real history and sending require a real token+cookie** and cannot be verified
otherwise.

## Conventions

- Standard library only; keep the dependency footprint at zero unless there's a
  strong reason.
- Match the existing terse, commented style. Keep the UI minimal — new features
  should not clutter the single-card compose experience.
- Never log or expose the token/cookie. Config file stays `0600`.

## Known limitations / next steps

- **Realtime is polling (4s)**, not a socket. True realtime would use the
  browser client's flannel/RTM WebSocket — but it **cannot be built reliably
  without a live session to test against**. Do this once real creds are
  connected; consider hiding it behind a flag until verified.
- The "shortcut" is in-tab (the page opens compose-ready). A true system-wide
  hotkey would require a native window (e.g. Wails) — a different build.
- Not yet: threads, message editing, reactions, multi-workspace.
- Single-user, single-workspace; no concurrent-session handling beyond the
  RWMutex around the live client + caches.
```
