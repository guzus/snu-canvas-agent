---
name: learningx-telegram-cli
description: Run and troubleshoot the lx-agent Telegram + LearningX stack through its CLI commands. Use when you need to inspect config, list courses/assignments/files/announcements, run bot/serve modes, or execute operational checks from the command line.
---

# LearningX Telegram CLI

Use this skill to operate `lx-agent` via CLI.

## Command Bridge

Run commands through the bundled bridge script:

```bash
skills/learningx-telegram-cli/scripts/run-lx-agent-cli.sh <command> [args...]
```

Set `LX_AGENT_ROOT` when running outside the repository root.

## Common Commands

```bash
skills/learningx-telegram-cli/scripts/run-lx-agent-cli.sh config
skills/learningx-telegram-cli/scripts/run-lx-agent-cli.sh courses
skills/learningx-telegram-cli/scripts/run-lx-agent-cli.sh assignments
skills/learningx-telegram-cli/scripts/run-lx-agent-cli.sh files
skills/learningx-telegram-cli/scripts/run-lx-agent-cli.sh announcements
skills/learningx-telegram-cli/scripts/run-lx-agent-cli.sh sync --dry-run
skills/learningx-telegram-cli/scripts/run-lx-agent-cli.sh sync --notify
skills/learningx-telegram-cli/scripts/run-lx-agent-cli.sh bot
skills/learningx-telegram-cli/scripts/run-lx-agent-cli.sh serve
```

## Archive Sync

`sync` mirrors every course file to `archive.dir` and is what the scheduled
launchd job runs.

- `sync --dry-run` lists what would be fetched without writing anything.
- `sync --course <id>` restricts the run to one course.
- `sync --include-videos` also pulls lecture video/audio.
- Downloads are keyed by Canvas file ID in `archive-manifest.json`, so re-runs
  transfer nothing new.
- Exit code 1 means files failed or the credential expired — check the log
  rather than assuming a quiet run succeeded.
- Scheduled on `gunux` as a systemd user timer. Operate it with
  `deploy/gunux/deploy.sh --status|--run|--uninstall`, or read the journal
  directly: `ssh gunux journalctl --user -u lx-archive.service -n 50`.

## Notes

- The bridge runs `go run ./cmd/lx-agent ...`.
- Keep outputs concise and include command results directly in your response.
- For bot/serve runs, surface startup errors and required env/config clearly.
