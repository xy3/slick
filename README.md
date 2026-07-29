# slick

An utterly minimal wrapper for **sending** Slack messages. Press nothing, type a
name, type a message, send. Dark, monotone, rounded, distraction-free.

It reuses your existing Slack session (an `xoxc-` token + the `d` cookie), so
there's no OAuth app to create — it acts as you.

## Run

```sh
go run .            # http://127.0.0.1:8383
go run . -addr :9000
```

First launch opens a **setup** page. From a Slack browser tab, DevTools (`F12`):

1. **Token** — Console:
   `JSON.parse(localStorage.localConfig_v2).teams` → expand your workspace →
   copy its `token` (starts `xoxc-`).
2. **d cookie** — Application ▸ Cookies ▸ `https://app.slack.com` → copy the
   value of the `d` row (starts `xoxd-`).

Credentials are stored at `~/.config/slick/config.json` (user-only, `0600`).
Nothing leaves your machine except calls to Slack. If Slack rotates the token
(occasional), just paste a fresh pair.

## Use

- Type to fuzzy-search a channel or person; `↑`/`↓` to move, `Enter` to pick.
- The last ~25 messages appear and refresh every few seconds while open.
- Write your message; `⌘/Ctrl+Enter` to send, `Esc` to change recipient.

### A near-global shortcut

Since slick runs as a page, the fastest "shortcut" is to open it as its own
window and bind an OS hotkey to it:

- Chrome ▸ ⋮ ▸ **Cast, save, and share ▸ Create shortcut…** ▸ *Open as window*.
- Bind that shortcut to a key (Windows: shortcut Properties ▸ Shortcut key, or
  AutoHotkey). It always opens focused on the recipient search — ready to type.

## Layout

- `main.go` — local HTTP server, paste-once auth, conversation cache.
- `slack/client.go` — `xoxc`+cookie Web API client (`auth.test`,
  `conversations.list`, `users.list`, `conversations.history`,
  `chat.postMessage`).
- `web/` — embedded UI (`index.html`, `setup.html`, `style.css`, `app.js`).

## Not yet

- **True realtime.** History currently refreshes by polling every 4s; a
  WebSocket (the browser client's flannel/RTM socket) would be instant.
- **Threads**, message editing, reactions, and multi-workspace.
