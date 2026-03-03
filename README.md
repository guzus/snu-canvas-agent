# lx-agent

Canvas LMS (Learning X) monitoring agent written in Go. Watches your courses for new files, assignments, announcements, and upcoming deadlines — then summarizes and notifies you.

Built for [서울대 Learning X](https://learningx.snu.ac.kr) but works with any Canvas LMS instance.

## Features

- 📄 **New file detection** — alerts when lecture materials are uploaded
- 📝 **New assignment tracking** — notifies on new assignments with due dates
- 📢 **Announcement monitoring** — catches new course announcements
- ⏰ **Deadline alerts** — configurable D-3, D-1, D-Day reminders
- 🤖 **Auto-summarization** — summarizes new content via Gemini AI
- 📱 **Telegram notifications** — push alerts to your phone
- 🔌 **Extensible** — pluggable notifier and summarizer interfaces

## Quick Start

### 1. Get your Canvas API token

Go to your Canvas LMS → Account → Settings → New Access Token

### 2. Install

```bash
go install github.com/mgnlia/lx-agent/cmd/lx-agent@latest
```

Or build from source:

```bash
git clone https://github.com/mgnlia/lx-agent.git
cd lx-agent
go build -o lx-agent ./cmd/lx-agent/
```

### 3. Configure

```bash
cp config.yaml my-config.yaml
# Edit my-config.yaml with your tokens
```

Or use environment variables:

```bash
export CANVAS_URL=learningx.snu.ac.kr
export CANVAS_TOKEN=your-canvas-api-token
export DATABASE_URL=your-postgres-url      # optional, for token->chat binding
export GEMINI_API_KEY=your-gemini-key        # optional, for summarization
export TELEGRAM_BOT_TOKEN=your-bot-token     # optional, for telegram alerts
export TELEGRAM_CHAT_ID=your-chat-id
```

### 4. Run

```bash
# List your courses
lx-agent courses

# List upcoming assignments
lx-agent assignments

# List recent files
lx-agent files

# Run a single check
lx-agent once

# Start monitoring loop (polls every 10 minutes)
lx-agent run

# Start Telegram command bot only
lx-agent bot

# Run monitor + Telegram command bot together
lx-agent serve
```

## Commands

| Command | Description |
|---------|-------------|
| `run` | Start monitoring loop |
| `once` | Run a single check and exit |
| `courses` | List enrolled courses |
| `assignments [course-id]` | List upcoming assignments |
| `files [course-id]` | List recent files |
| `announcements` | List recent announcements |
| `bind-chat [chat-id]` | Bind current `CANVAS_TOKEN` to Telegram `chat_id` in DB |
| `bot` | Run Telegram command bot (responds to chat commands) |
| `serve` | Run monitor loop + Telegram command bot |
| `config` | Show current configuration |

### Telegram Bot Commands

- `/status`
- `/courses [keyword]`
- `/assignments <course_id> [limit]`
- `/upcoming [days] [limit]`
- `/announcements [limit]`
- `/files <course_id> [limit]`
- `/bind`

## Configuration

```yaml
canvas:
  url: "learningx.snu.ac.kr"
  token: "your-api-token"

monitor:
  poll_interval: "10m"
  summarize_new: true
  deadline_alerts: [3, 1, 0]
  state_path: "lx-state.json"
  # courses: [12345]  # filter specific courses

summarizer:
  provider: "gemini"
  gemini_api_key: "your-key"

notifier:
  provider: "telegram"  # or "stdout"
  telegram:
    bot_token: "your-bot-token"
    chat_id: "your-chat-id"   # optional when database.url is set

database:
  url: "postgres://..."
```

## Architecture

```
lx-agent/
├── cmd/lx-agent/main.go         # CLI entrypoint + config loading
├── internal/
│   ├── canvas/                   # Canvas LMS API client
│   │   ├── client.go             # HTTP client, pagination, rate limiting
│   │   ├── courses.go            # GET /courses
│   │   ├── assignments.go        # GET /courses/:id/assignments
│   │   ├── files.go              # GET /courses/:id/files, modules
│   │   ├── announcements.go      # GET /announcements
│   │   └── types.go              # Shared types
│   ├── monitor/                  # Monitoring logic
│   │   ├── monitor.go            # Poll loop + diff detection
│   │   └── state.go              # Persistent state (seen IDs)
│   ├── summarizer/               # Content summarization
│   │   ├── summarizer.go         # Interface
│   │   └── gemini.go             # Gemini implementation
│   └── notifier/                 # Notification delivery
│       ├── notifier.go           # Interface
│       ├── telegram.go           # Telegram bot
│       └── stdout.go             # Terminal output
└── config.yaml
```

### Extending

**Add a new notifier** (e.g., Discord):

```go
type DiscordNotifier struct { webhookURL string }

func (d *DiscordNotifier) Send(ctx context.Context, message string) error {
    // POST to Discord webhook
}
```

**Add a new summarizer** (e.g., OpenAI):

```go
type OpenAISummarizer struct { apiKey string }

func (o *OpenAISummarizer) SummarizeText(ctx context.Context, title, text string) (string, error) {
    // Call OpenAI API
}
```

## Canvas API

lx-agent uses the [Canvas LMS REST API](https://canvas.instructure.com/doc/api/):

- Authentication: Bearer token
- Pagination: Link header (`rel="next"`)
- Rate limiting: auto-retry on 429

## License

MIT
