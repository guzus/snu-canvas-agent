#!/usr/bin/env bash
# Install the periodic LearningX archive job on macOS.
#
#   ./deploy/launchd/install.sh                 # every 6 hours
#   INTERVAL=3600 ./deploy/launchd/install.sh   # hourly
#   ./deploy/launchd/install.sh --uninstall
set -euo pipefail

LABEL="xyz.guzus.lx-archive"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PLIST="$HOME/Library/LaunchAgents/$LABEL.plist"
DOMAIN="gui/$(id -u)"

if [[ "${1:-}" == "--uninstall" ]]; then
  launchctl bootout "$DOMAIN/$LABEL" 2>/dev/null || true
  rm -f "$PLIST"
  echo "uninstalled $LABEL"
  exit 0
fi

BIN="${BIN:-$HOME/.local/bin/lx-agent}"
CONFIG="${CONFIG:-$REPO_ROOT/config.yaml}"
ARCHIVE_DIR="${ARCHIVE_DIR:-$HOME/Documents/etl-archive}"
INTERVAL="${INTERVAL:-21600}"   # 6 hours
LOG="${LOG:-$HOME/Library/Logs/lx-archive.log}"

if [[ ! -f "$CONFIG" ]]; then
  echo "error: $CONFIG not found. Copy config.yaml.example and fill in canvas.url + credentials." >&2
  exit 1
fi

echo "building $BIN"
mkdir -p "$(dirname "$BIN")" "$ARCHIVE_DIR" "$(dirname "$LOG")"
# A real binary, not `go run`: launchd should not shell out to a toolchain.
(cd "$REPO_ROOT" && go build -o "$BIN" ./cmd/lx-agent)

echo "verifying credentials"
if ! "$BIN" -config "$CONFIG" courses >/dev/null; then
  echo "error: 'lx-agent courses' failed — fix auth before scheduling the job." >&2
  exit 1
fi

mkdir -p "$(dirname "$PLIST")"
sed -e "s|__BIN__|$BIN|g" \
    -e "s|__CONFIG__|$CONFIG|g" \
    -e "s|__WORKDIR__|$REPO_ROOT|g" \
    -e "s|__ARCHIVE_DIR__|$ARCHIVE_DIR|g" \
    -e "s|__INTERVAL__|$INTERVAL|g" \
    -e "s|__LOG__|$LOG|g" \
    "$REPO_ROOT/deploy/launchd/$LABEL.plist.template" > "$PLIST"

launchctl bootout "$DOMAIN/$LABEL" 2>/dev/null || true
launchctl bootstrap "$DOMAIN" "$PLIST"
launchctl enable "$DOMAIN/$LABEL"

cat <<MSG

installed $LABEL
  binary   : $BIN
  config   : $CONFIG
  archive  : $ARCHIVE_DIR
  interval : ${INTERVAL}s
  log      : $LOG

  run now   : launchctl kickstart -p $DOMAIN/$LABEL
  status    : launchctl print $DOMAIN/$LABEL | head -20
  uninstall : $REPO_ROOT/deploy/launchd/install.sh --uninstall
MSG
