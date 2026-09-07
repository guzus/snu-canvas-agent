#!/usr/bin/env bash
# Deploy the LearningX archive sync to a Linux host as a systemd user timer.
#
#   ./deploy/gunux/deploy.sh                      # build, ship, install, verify
#   ./deploy/gunux/deploy.sh --seed               # also rsync the local archive up
#   ./deploy/gunux/deploy.sh --run                # trigger a run and tail the journal
#   ./deploy/gunux/deploy.sh --status
#   ./deploy/gunux/deploy.sh --uninstall
#
# Env overrides: HOST, ARCHIVE_DIR, ONCALENDAR, CONFIG
set -euo pipefail

HOST="${HOST:-gunux}"
ARCHIVE_DIR="${ARCHIVE_DIR:-/ssd1/etl-archive}"
ONCALENDAR="${ONCALENDAR:-*-*-* 00/6:00:00}"   # every 6 hours
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CONFIG="${CONFIG:-$REPO_ROOT/config.yaml}"

REMOTE_BIN='.local/bin/lx-agent'
REMOTE_CFG='.config/lx-agent/config.yaml'
UNIT_DIR='.config/systemd/user'

say()  { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
fail() { printf '\033[31merror: %s\033[0m\n' "$*" >&2; exit 1; }

# ssh_ runs a remote command with a login-ish environment. XDG_RUNTIME_DIR must
# be set explicitly or systemctl --user cannot reach the user bus over ssh.
ssh_() { ssh -o BatchMode=yes "$HOST" "export XDG_RUNTIME_DIR=/run/user/\$(id -u); $*"; }

case "${1:-}" in
--uninstall)
  say "removing timer and service from $HOST"
  ssh_ "systemctl --user disable --now lx-archive.timer 2>/dev/null || true;
        rm -f ~/$UNIT_DIR/lx-archive.timer ~/$UNIT_DIR/lx-archive.service;
        systemctl --user daemon-reload"
  echo "uninstalled (binary, config and archive left in place)"
  exit 0
  ;;
--status)
  ssh_ "systemctl --user list-timers lx-archive.timer --no-pager;
        echo; systemctl --user status lx-archive.service --no-pager -n 20 || true"
  exit 0
  ;;
--run)
  say "triggering a run on $HOST"
  ssh_ "systemctl --user start lx-archive.service"
  ssh_ "journalctl --user -u lx-archive.service -n 40 --no-pager"
  exit 0
  ;;
esac

SEED=0
for arg in "$@"; do
  case "$arg" in
    --seed) SEED=1 ;;
    --no-build) NO_BUILD=1 ;;
    *) fail "unknown flag: $arg" ;;
  esac
done

[[ -f "$CONFIG" ]] || fail "$CONFIG not found. Copy config.yaml.example and fill in credentials."
ssh -o BatchMode=yes -o ConnectTimeout=10 "$HOST" true 2>/dev/null || fail "cannot ssh to $HOST"

# --- build ------------------------------------------------------------------
# Cross-compiled here rather than built on the host: CGO is off, so this is a
# single static binary and the host needs no Go toolchain to stay current.
if [[ -z "${NO_BUILD:-}" ]]; then
  say "building linux/amd64 binary"
  mkdir -p "$REPO_ROOT/dist"
  ( cd "$REPO_ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
      go build -trimpath -o dist/lx-agent-linux-amd64 ./cmd/lx-agent )
  ls -lh "$REPO_ROOT/dist/lx-agent-linux-amd64" | awk '{print "  " $9, $5}'
fi

# --- ship -------------------------------------------------------------------
say "creating directories on $HOST"
ssh_ "mkdir -p ~/.local/bin ~/.config/lx-agent ~/$UNIT_DIR '$ARCHIVE_DIR'"
ssh_ "test -w '$ARCHIVE_DIR'" || fail "$ARCHIVE_DIR is not writable on $HOST"

say "uploading binary"
# Upload beside the target then rename: replacing a running binary in place
# would give ETXTBSY or corrupt an in-flight run.
scp -q "$REPO_ROOT/dist/lx-agent-linux-amd64" "$HOST:$REMOTE_BIN.new"
ssh_ "chmod +x ~/$REMOTE_BIN.new && mv ~/$REMOTE_BIN.new ~/$REMOTE_BIN"

say "uploading config (contains the session cookie)"
scp -q "$CONFIG" "$HOST:$REMOTE_CFG.new"
ssh_ "chmod 600 ~/$REMOTE_CFG.new && mv ~/$REMOTE_CFG.new ~/$REMOTE_CFG"

# --- seed -------------------------------------------------------------------
if [[ "$SEED" == "1" ]]; then
  LOCAL_ARCHIVE="${LOCAL_ARCHIVE:-$HOME/etl-archive}"
  [[ -d "$LOCAL_ARCHIVE" ]] || fail "$LOCAL_ARCHIVE not found; nothing to seed"
  say "seeding archive from $LOCAL_ARCHIVE (avoids re-downloading from SNU)"
  # macOS ships openrsync ("rsync 2.6.9 compatible"), which has neither
  # --info=progress2 nor --partial. Probe instead of assuming GNU rsync.
  rsync_flags=(-a)
  if rsync --info=progress2 --version >/dev/null 2>&1; then
    rsync_flags+=(--info=progress2 --partial)
  else
    rsync_flags+=(-v)
  fi

  # The manifest is keyed by Canvas file ID and stores relative paths, so the
  # whole tree transplants cleanly and the first remote run finds it complete.
  rsync "${rsync_flags[@]}" "$LOCAL_ARCHIVE/" "$HOST:$ARCHIVE_DIR/"
fi

# --- install units ----------------------------------------------------------
say "installing systemd user units"
sed "s|__ARCHIVE_DIR__|$ARCHIVE_DIR|g" \
  "$REPO_ROOT/deploy/gunux/lx-archive.service.template" \
  | ssh -o BatchMode=yes "$HOST" "cat > ~/$UNIT_DIR/lx-archive.service"
sed "s|__ONCALENDAR__|$ONCALENDAR|g" \
  "$REPO_ROOT/deploy/gunux/lx-archive.timer.template" \
  | ssh -o BatchMode=yes "$HOST" "cat > ~/$UNIT_DIR/lx-archive.timer"

# Without lingering, user units stop the moment the ssh session ends.
if ! ssh_ "loginctl show-user \$(whoami) 2>/dev/null | grep -q 'Linger=yes'"; then
  echo "  enabling linger so the timer runs without an active login"
  ssh_ "loginctl enable-linger \$(whoami)" || \
    fail "could not enable linger; run: sudo loginctl enable-linger \$(whoami)"
fi

ssh_ "systemctl --user daemon-reload && systemctl --user enable --now lx-archive.timer"

# --- verify -----------------------------------------------------------------
say "verifying credentials on $HOST"
ssh_ "~/$REMOTE_BIN -config ~/$REMOTE_CFG courses | head -3" \
  || fail "remote 'courses' failed — check the session cookie in $CONFIG"

say "deployed"
ssh_ "systemctl --user list-timers lx-archive.timer --no-pager | head -3"
cat <<MSG

  host     : $HOST
  binary   : ~/$REMOTE_BIN
  config   : ~/$REMOTE_CFG (0600)
  archive  : $ARCHIVE_DIR
  schedule : $ONCALENDAR (Persistent=true, catches up after downtime)

  run now  : $0 --run
  status   : $0 --status
  logs     : ssh $HOST journalctl --user -u lx-archive.service -f
  remove   : $0 --uninstall
MSG
