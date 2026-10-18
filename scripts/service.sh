#!/usr/bin/env bash
# Manage the qoder2api service on macOS via launchd (label: com.qoder2api.service).
#
# Usage: scripts/service.sh {install|start|stop|restart|status|logs|off|on|update}
#
# Everything is overridable via environment variables:
#   QODER_PORT       listening port                       (default 10081)
#   QODER_BIN        installed binary path                (default ~/.local/bin/qoder2api)
#   QODER_DATA_DIR   runtime dir (data.json + logs)       (default ~/.local/share/qoder2api)
#   QODER_LABEL      launchd label                        (default com.qoder2api.service)
#
# macOS-only: this script drives `launchctl` and `codesign`. On other systems
# run the binary directly (or use Docker) — see README.
#
# Note: the binary and runtime data default to locations OUTSIDE ~/Documents,
# because macOS TCC can block launchd-launched processes from reading files in
# protected folders (Desktop/Documents/Downloads). Keep them elsewhere.
set -euo pipefail

LABEL="${QODER_LABEL:-com.qoder2api.service}"
PORT="${QODER_PORT:-10081}"
BIN="${QODER_BIN:-$HOME/.local/bin/qoder2api}"
RUN_DIR="${QODER_DATA_DIR:-$HOME/.local/share/qoder2api}"
DATA_FILE="$RUN_DIR/data.json"
PLIST="$HOME/Library/LaunchAgents/$LABEL.plist"
LOG="$RUN_DIR/service.log"
ERRLOG="$RUN_DIR/service.err.log"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd -P)"
DOMAIN="gui/$(id -u)"

require_macos() {
    if [[ "$(uname -s)" != "Darwin" ]] || ! command -v launchctl >/dev/null 2>&1; then
        echo "ERROR: this script manages a macOS launchd service and only works on macOS." >&2
        echo "       On Linux, run the binary directly or use Docker (see README)." >&2
        exit 1
    fi
}

is_loaded() { launchctl print "$DOMAIN/$LABEL" &>/dev/null; }

write_plist() {
    mkdir -p "$(dirname "$PLIST")" "$RUN_DIR" "$(dirname "$BIN")"
    cat >"$PLIST" <<PLIST_EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key><string>$LABEL</string>
    <key>ProgramArguments</key>
    <array><string>$BIN</string></array>
    <key>WorkingDirectory</key><string>$RUN_DIR</string>
    <key>EnvironmentVariables</key>
    <dict>
        <key>QODER_DATA_PATH</key><string>$DATA_FILE</string>
        <key>QODER_PORT</key><string>$PORT</string>
    </dict>
    <key>RunAtLoad</key><true/>
    <key>KeepAlive</key><true/>
    <key>ThrottleInterval</key><integer>10</integer>
    <key>StandardOutPath</key><string>$LOG</string>
    <key>StandardErrorPath</key><string>$ERRLOG</string>
</dict>
</plist>
PLIST_EOF
    echo "Wrote $PLIST"
}

cmd_install() {
    require_macos
    write_plist
    if [[ ! -x "$BIN" ]]; then
        echo "Binary not found at $BIN. Building from source..."
        cmd_update
    fi
    echo "Installed. Use '$0 start' to start it."
}

cmd_start() {
    require_macos
    if lsof -nP -iTCP:"$PORT" -sTCP:LISTEN &>/dev/null; then
        echo "ERROR: port $PORT is already in use:"
        lsof -nP -iTCP:"$PORT" -sTCP:LISTEN | tail -n +2
        echo "Stop that instance first (e.g. 'kill <pid>'), then run '$0 start'."
        return 1
    fi
    [[ -f "$PLIST" ]] || write_plist
    launchctl enable "$DOMAIN/$LABEL" 2>/dev/null || true
    if is_loaded; then
        echo "Already loaded. Use '$0 restart' to restart it."
    else
        launchctl bootstrap "$DOMAIN" "$PLIST"
        sleep 2
        echo "Started."
    fi
    cmd_status
}

cmd_stop() {
    require_macos
    if is_loaded; then
        launchctl bootout "$DOMAIN/$LABEL"
        echo "Stopped. It will start again at next login; use '$0 off' to disable it for good."
    else
        echo "Not loaded."
    fi
}

cmd_restart() {
    require_macos
    if is_loaded; then
        launchctl kickstart -k "$DOMAIN/$LABEL"
        sleep 2
        echo "Restarted."
        cmd_status
    else
        cmd_start
    fi
}

cmd_status() {
    require_macos
    if is_loaded; then
        launchctl print "$DOMAIN/$LABEL" 2>/dev/null | grep -E "state =|pid =|last exit code" | sed 's/^[[:space:]]*//' | head -3
        curl -s --noproxy '*' -o /dev/null -m 5 -w "Health: HTTP %{http_code} (http://127.0.0.1:$PORT/health)\n" "http://127.0.0.1:$PORT/health" || \
            echo "Health: not responding yet (service may still be starting)"
    else
        echo "Service not loaded. Use '$0 start'."
    fi
}

cmd_logs() { tail -n 50 -f "$LOG"; }

cmd_off() {
    require_macos
    if is_loaded; then launchctl bootout "$DOMAIN/$LABEL" || true; fi
    launchctl disable "$DOMAIN/$LABEL" 2>/dev/null || true
    echo "Disabled: it will NOT start at login anymore. Use '$0 on' to re-enable."
}

cmd_on() { cmd_start; }

# Rebuild the binary from source and reinstall it (run from project dir).
cmd_update() {
    mkdir -p "$(dirname "$BIN")"
    ( cd "$PROJECT_DIR" && go build -o "$BIN" . )
    if [[ "$(uname -s)" == "Darwin" ]]; then
        codesign -s - --force "$BIN"
    fi
    echo "Binary rebuilt at $BIN."
    if is_loaded; then cmd_restart; else echo "Service not running. Use '$0 start'."; fi
}

case "${1:-status}" in
    install) cmd_install ;;
    start) cmd_start ;;
    stop) cmd_stop ;;
    restart) cmd_restart ;;
    status) cmd_status ;;
    logs) cmd_logs ;;
    off) cmd_off ;;
    on) cmd_on ;;
    update) cmd_update ;;
    *) echo "Usage: $0 {install|start|stop|restart|status|logs|off|on|update}"; exit 1 ;;
esac
