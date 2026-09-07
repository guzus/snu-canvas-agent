# lx-agent

Canvas LMS (Learning X) monitoring agent in Go, with Telegram bot controls and a Bun + TypeScript admin dashboard for ChatGPT/Codex account linking.

Built for [서울대 Learning X](https://myetl.snu.ac.kr), compatible with Canvas LMS APIs.

## Features

- **Archive sync**: mirror every course file to local disk on a schedule, so materials accumulate instead of being downloaded one at a time
- New file / assignment / announcement monitoring
- Deadline alerts (`D-3`, `D-1`, `D-Day`)
- Telegram bot commands for course info
- Interactive course selectors in Telegram for commands that need `course_id`
- Per-chat language setting (`ko` default, switchable to `en` via `/settings`)
- Canvas token ↔ Telegram chat binding in Postgres
- Admin dashboard (TypeScript + Bun): ChatGPT OAuth login + Codex model selection

## Quick Start (Agent)

1. Get a Canvas API token (`Account -> Settings -> New Access Token`).
2. Create config:

```bash
cp config.yaml.example config.yaml
```

3. Fill required values:

```yaml
canvas:
  url: "myetl.snu.ac.kr"
  token: "..."

notifier:
  provider: "telegram"
  telegram:
    bot_token: "..."
    chat_id: ""   # optional if bound via DB

database:
  url: "postgres://..."
```

4. Run:

```bash
go run ./cmd/lx-agent serve
```

## Archive Sync (scheduled download)

`sync` walks every active course and mirrors its files to `archive.dir`:

```bash
go run ./cmd/lx-agent sync --dry-run     # show what would be fetched
go run ./cmd/lx-agent sync               # download
go run ./cmd/lx-agent sync --notify      # download + Telegram summary
```

Layout on disk:

```
~/Documents/etl-archive/
├── archive-manifest.json          # file-ID → path index (do not delete)
├── 자료구조 (2026-1)/
│   ├── 1주차/…                    # mirrors the LMS folder tree
│   ├── _modules/Week 1/…          # files reachable only via Modules
│   └── _attachments/…             # files linked from assignments/announcements
└── …
```

Notes that matter in practice:

- **Enumeration is a union.** SNU courses often disable the Files tab, so
  `/courses/:id/files` returns 403 and the materials live only in module items.
  `sync` unions the files endpoint, module items, and file links embedded in
  assignment/announcement HTML, deduped by Canvas file ID.
- **Re-runs are free.** `archive-manifest.json` is keyed by file ID — not
  filename, because macOS stores Korean names in NFD while Canvas serves NFC,
  and a name-based check would re-download everything each run. A file is
  refetched only when its size or `updated_at` changes, or the local copy is
  gone.
- **Locked files are skipped**, not written as empty stubs.
- **Video/audio is opt-in** (`--include-videos` / `archive.include_videos`).
- **Expired credentials are loud.** The credential is the thing that rots; when
  Canvas returns 401 the run sends a Telegram alert and exits non-zero instead
  of quietly archiving nothing.

### Schedule it (macOS)

```bash
./deploy/launchd/install.sh                 # every 6 hours
INTERVAL=3600 ./deploy/launchd/install.sh   # hourly
./deploy/launchd/install.sh --uninstall
```

The installer builds a real binary to `~/.local/bin/lx-agent`, verifies auth
with a live `courses` call before scheduling anything, and loads
`xyz.guzus.lx-archive`. It uses `StartInterval` rather than a calendar time:
a sleeping laptop misses a calendar firing but catches an interval on wake.

```bash
launchctl kickstart -p gui/$(id -u)/xyz.guzus.lx-archive   # run now
tail -f ~/Library/Logs/lx-archive.log                      # watch it
```

## CLI Commands

- `courses`
- `assignments [course-id]`
- `files [course-id]`
- `announcements`
- `sync [--out DIR] [--course ID]... [--dry-run] [--include-videos] [--max-mb N] [--notify]`
- `bind-chat [chat-id]`
- `bot`
- `serve`
- `once`
- `run`
- `config`

## Telegram Commands

- `/menu` (quick action menu)
- `/status`
- `/settings` (change language: Korean/English)
- `/listening` (show subscribed courses for this chat)
- `/listen` (interactive selector to subscribe course alerts)
- `/unlisten` (interactive selector to unsubscribe)
- `/courses [keyword]`
- `/assignments` (interactive course selector)
- `/files` (interactive course selector)
- `/upcoming [days] [limit]`
- `/announcements [limit]`
- `/chat <message>` (Codex conversation)
- Plain text message (without `/`) also routes to Codex conversation
- `/bind`

## Admin Dashboard (TypeScript + Bun)

The admin stack is in `apps/admin-backend` and `apps/admin-frontend`.

### Run locally

```bash
bun install
bun run admin:backend
bun run admin:frontend
```

- Backend: `http://localhost:8787`
- Frontend: `http://localhost:5173`

### What it does

- `Login with ChatGPT` (OAuth flow for OpenAI Codex account)
- Persist linked account JSON in `apps/admin-backend/data/codex_account.json`
- Persist provider config in `apps/admin-backend/data/config.json`
- Default model is `openai-codex/gpt-5.3-codex-spark`

## Environment Variables

- `CANVAS_URL`
- `CANVAS_TOKEN`
- `CANVAS_SESSION_COOKIE`
- `ARCHIVE_DIR`
- `TELEGRAM_BOT_TOKEN`
- `TELEGRAM_CHAT_ID`
- `DATABASE_URL`
- `ADMIN_BACKEND_PORT` (optional; default `8787`)
- `ADMIN_BACKEND_URL` (optional; used by lx-agent bot to call admin Codex chat API)
- `ADMIN_BACKEND_BOT_TOKEN` (recommended with dashboard password; set same value in both `lx-agent` and `admin-dashboard` services)
- `ADMIN_DASHBOARD_PASSWORD` (optional; when set, enables password gateway for admin dashboard/API)
- `ADMIN_DASHBOARD_SESSION_SECONDS` (optional; session max age in seconds, default `1209600`)

## Architecture

- `cmd/lx-agent/main.go`: CLI entrypoint and wiring
- `internal/canvas/*`: Canvas API client
- `internal/archive/*`: course-file mirror + manifest for `sync`
- `internal/monitor/*`: monitor loop + state tracking
- `internal/notifier/*`: stdout + Telegram notifier/bot
- `internal/binding/*`: Postgres token/chat binding + language preferences
- `apps/admin-backend/*`: Bun API for ChatGPT OAuth + Codex config
- `apps/admin-frontend/*`: React UI for admin actions

## Notes

- Gemini integration has been removed from this repository.
- Course filtering can be fixed to a term or subset using `monitor.courses` in config.
- `serve` can run without Canvas config (Telegram bot only). Canvas commands will return a not-configured message.
- Chat course subscriptions are persisted in Postgres and used by monitor filtering when available.
- The archive manifest is deliberately separate from `monitor.State`: the
  monitor's `seen_files` map tracks what has been *announced*, and sharing it
  would make the archiver skip every file the notifier saw first.
- Sent alerts are persisted with metadata/dedupe keys in Postgres to prevent re-sending duplicates.
- If no explicit subscriptions exist for a chat, the bot/monitor defaults to current semester courses (e.g., `2026-1` in spring 2026 KST).

## License

MIT
