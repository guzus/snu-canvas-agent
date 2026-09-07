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
~/etl-archive/
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
  Canvas returns 401 the run alerts through hooker at priority 5 and exits
  non-zero instead of quietly archiving nothing. Routine "N files archived"
  notices go out at priority 3, so the one that needs action is not skimmed
  past with the ones that do not.

### Schedule it (Linux host, recommended)

The archive belongs on an always-on machine, not a laptop that sleeps:

```bash
./deploy/gunux/deploy.sh              # build, ship, install the systemd timer
./deploy/gunux/deploy.sh --seed       # also rsync an existing local archive up
./deploy/gunux/deploy.sh --run        # trigger a run now, tail the journal
./deploy/gunux/deploy.sh --status
./deploy/gunux/deploy.sh --uninstall
```

Defaults to `HOST=gunux`, `ARCHIVE_DIR=/ssd1/etl-archive`, every 6 hours; all
three are env overrides. The script cross-compiles a static `linux/amd64`
binary here — CGO is off, so the host needs no Go toolchain to stay current —
uploads it beside the target and renames it into place (replacing a running
binary in-place gives `ETXTBSY`), installs the config at mode 600, and refuses
to finish unless a live `courses` call succeeds on the host.

Both units are systemd **user** units, so they need lingering enabled
(`loginctl enable-linger`); the script checks and enables it. The timer uses
`Persistent=true`, so a run missed while the box was off fires on boot instead
of being skipped. `TimeoutStartSec=3h` caps a wedged run so it cannot block
every later firing.

```bash
ssh gunux journalctl --user -u lx-archive.service -f
```

Alerts route through hooker to a Telegram forum thread. The deploy renders
`~/.config/lx-agent/env` **on the host** from the host's own
`~/.config/hooker/env`, so the credentials never transit the machine running
the deploy; it is rewritten rather than pointed at because that file uses
`export VAR=...`, which systemd's `EnvironmentFile` parses as a variable named
`export VAR`.

Verify the alert path on demand — it is otherwise only exercised when something
is already broken:

```bash
ssh gunux 'set -a; . ~/.config/lx-agent/env; set +a; ~/.local/bin/lx-agent -config ~/.config/lx-agent/config.yaml notify-test'
```

hooker's `delivered: 1` only reports that *some* route accepted the message, so
the notifier compares the echoed `destinations` against the chat and thread it
addressed and fails loudly on a misroute.

Seeding matters: the manifest is keyed by Canvas file ID and stores relative
paths, so an existing archive transplants cleanly with `rsync` and the first
remote run finds it already complete — no re-downloading gigabytes from SNU.

### Schedule it (macOS, laptop-local)

```bash
./deploy/launchd/install.sh                 # every 6 hours
INTERVAL=3600 ./deploy/launchd/install.sh   # hourly
./deploy/launchd/install.sh --uninstall
```

It uses `StartInterval` rather than a calendar time: a sleeping laptop misses a
calendar firing but catches an interval on wake.

**Keep the archive out of `~/Documents`, `~/Desktop` and `~/Downloads`.**
macOS TCC gates those folders, and a headless launchd job that touches one
blocks in `open()` forever, waiting on a consent dialog no one will ever see —
observed here as a job that ran 8 minutes using 0.03s of CPU with no network
activity. The installer refuses such a path unless you grant the binary Full
Disk Access and pass `ALLOW_TCC_DIR=1`.

## CLI Commands

- `courses`
- `assignments [course-id]`
- `files [course-id]`
- `announcements`
- `notify-test`
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
- `HOOKER_URL`, `HOOKER_API_KEY`, `HOOKER_TOPIC`, `HOOKER_CHAT_ID`, `HOOKER_MESSAGE_THREAD_ID`
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
