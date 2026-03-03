# lx-agent

Canvas LMS (Learning X) monitoring agent in Go, with Telegram bot controls and a Bun + TypeScript admin dashboard for ChatGPT/Codex account linking.

Built for [서울대 Learning X](https://learningx.snu.ac.kr), compatible with Canvas LMS APIs.

## Features

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
  url: "learningx.snu.ac.kr"
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

## CLI Commands

- `courses`
- `assignments [course-id]`
- `files [course-id]`
- `announcements`
- `bind-chat [chat-id]`
- `bot`
- `serve`
- `once`
- `run`
- `config`

## Telegram Commands

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
- `TELEGRAM_BOT_TOKEN`
- `TELEGRAM_CHAT_ID`
- `DATABASE_URL`
- `ADMIN_BACKEND_PORT` (optional; default `8787`)

## Architecture

- `cmd/lx-agent/main.go`: CLI entrypoint and wiring
- `internal/canvas/*`: Canvas API client
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
- Sent alerts are persisted with metadata/dedupe keys in Postgres to prevent re-sending duplicates.

## License

MIT
