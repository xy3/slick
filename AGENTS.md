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
slack/client_test.go  Unit test for mrkdwn rendering (renderSegments).
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
| `/api/notifications` | GET    | Unread/mention conversations (joins `client.counts` with names) |
| `/api/history?channel=<id>[&latest=<ts>]` | GET | Last ~25 messages, oldest first (window ends at `latest` when given) |
| `/api/search?q=<text>` | GET | Full-text message search; hits resolved to picker names |
| `/api/replies?channel=<id>&thread=<ts>` | GET | A thread: parent + replies, oldest first |
| `/api/mark`          | POST   | `{channel, ts}` -> `conversations.mark` (clears unread) |
| `/api/file?u=<url>`  | GET    | Auth proxy for Slack images (Slack hosts + `image/*` only) |
| `/api/send`          | POST   | `{channel, text, thread?}` -> `chat.postMessage` (thread replies) |

API handlers return `401` when unconfigured and `502` (`friendly()` message) on
Slack errors.

## Slack API methods used

`auth.test`, `conversations.list` (types: public/private/mpim/im, paginated),
`users.list` (paginated, cached), `conversations.history`,
`conversations.replies` (thread parent + replies), `chat.postMessage` (with
optional `thread_ts` for thread replies), `conversations.mark` (clears unread
when you view a conversation), `client.counts` (unread/mention counts — the same
undocumented endpoint the web app uses; works because we hold a real browser
session), `search.messages` (full-text search from the search bar),
`conversations.info` (names an unread conversation that isn't in the cached
picker list — e.g. a group DM opened after the cache filled). Images are fetched with an authenticated GET (`FetchFile`) and proxied
via `/api/file`, since Slack file URLs (`url_private`, thumbnails) require the
same token+cookie and can't be loaded by the browser directly.

`slack/client.go` renders Slack **mrkdwn** into a flat list of **segments**
(`renderSegments`): `text` (with a `style` of `b`/`i`/`s` for `*bold*`,
`_italic_`, `~strike~`), `mention` (`<@U…>` users resolved via the users cache,
`<#C…|name>` channels, and `<!here>`/`<!channel>` broadcasts — a `self` flag
marks a mention of the current user), `link` (`<url|text>`), inline `code`, and
fenced `pre` code blocks. Underscore-italic is only applied on word boundaries so
`file_names_like_this` survive. HTML entities are unescaped. The client emits
structured segments rather than HTML so the browser can build DOM nodes safely
via `textContent` (message text is untrusted — never render it as HTML). See
`renderSegments` and its test.

Image files render as `image` segments; **GIFs** use Slack's animated thumbnail
(`thumb_480_gif`/`thumb_360_gif`, falling back to the original) so they play,
while other images use the static `thumb_720`/`thumb_360`.

## Frontend behavior

- Page loads focused on the recipient search. Fuzzy match ranks prefix >
  substring > subsequence; top 8 shown. `↑`/`↓` move, `Enter` selects. The same
  box also runs a debounced full-text **message search** (`/api/search`); hits
  render below the name matches (reusing `renderBody`) and clicking one opens the
  conversation anchored at that message (`&latest=<ts>`, polling stays pinned and
  does not mark-read) or opens its thread when the hit is a reply.
- **Notifications**: an unread/mention summary (`#notifs`) sits atop the card in
  browse mode, polled every 15s. Each row opens that conversation on click. It's
  hidden during compose to keep that view distraction-free, and restored on
  `Esc`/reset. An empty list hides the panel (`.notifs:empty`); fetch failures
  clear it silently (notifications are ambient — they must never nag).
- On select: a chip shows the target, recent history renders above the compose
  box, and history **polls every 4s** (and refreshes right after a send).
- Message bodies are built from server segments (`renderBody`): links become
  `<a target="_blank" rel="noopener noreferrer">` (only when the URL is
  `http(s)`), inline code and fenced blocks get monochrome `code`/`pre` styling,
  and `image` segments render a bounded thumbnail linking to the full image —
  both routed through `/api/file` (`fileProxy`).
- **Threads**: a parent message with replies shows an "N replies" pill; clicking
  it opens the thread (parent + replies) in place, with a back bar. While a
  thread is open, sending posts a reply into it (`thread` in the send body) and
  polling follows the thread. `Esc` steps back out of the thread first, then out
  to recipient selection.
- `Shift+Enter` sends (plain `Enter` inserts a newline); `Esc` clears the
  recipient and stops polling.
- History re-renders in place on each poll and only auto-scrolls to the bottom
  when the reader is already there, so scrolling up to read isn't interrupted.
- All message content is inserted via `textContent` / node creation (never
  `innerHTML`) — keep it that way; message text is untrusted.

## Build / test / run

```sh
go build ./...          # or: go build -o /tmp/slick .
go vet ./...
go test ./...           # renderSegments coverage
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
- Threads are read + reply (no thread-level unread badges yet). Images are
  read-only (viewing); slick does not upload files.
- Not yet: message editing, reactions, non-image file previews, multi-workspace.
- Single-user, single-workspace; no concurrent-session handling beyond the
  RWMutex around the live client + caches.
```
