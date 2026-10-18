#!/usr/bin/env bash
# Manage the qoder2api service on macOS via launchd (label: com.qoder2api.service).
#
# Usage: scripts/service.sh {install|start|stop|restart|status|logs|off|on|update}
#
# Overridable via environment variables:
#   QODER_BIN        installed binary path                (default ~/.local/bin/qoder2api)
#   QODER_DATA_DIR   runtime dir (data.json + logs)       (default ~/.local/share/qoder2api)
#   QODER_LABEL      launchd label                        (default com.qoder2api.service)
#
# The port is deliberately NOT settable here: it lives in data.json, which is
# what the admin panel edits. main.go lets a QODER_PORT environment variable
# outrank that file, so pinning a port into the plist would silently make the
# panel's port setting a no-op. Change the port in the panel, then `restart`.
#
# macOS-only: this script drives `launchctl` and `codesign`. On other systems
# run the binary directly (or use Docker) — see README.
#
# Note: the binary and runtime data default to locations OUTSIDE ~/Documents,
# because macOS TCC can block launchd-launched processes from reading files in
# protected folders (Desktop/Documents/Downloads). Keep them elsewhere.
set -euo pipefail

LABEL="${QODER_LABEL:-com.qoder2api.service}"
BIN="${QODER_BIN:-$HOME/.local/bin/qoder2api}"
RUN_DIR="${QODER_DATA_DIR:-$HOME/.local/share/qoder2api}"
DATA_FILE="$RUN_DIR/data.json"
PLIST="$HOME/Library/LaunchAgents/$LABEL.plist"
LOG="$RUN_DIR/service.log"
ERRLOG="$RUN_DIR/service.err.log"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd -P)"
DOMAIN="gui/$(id -u)"
DEFAULT_PORT=10081

require_macos() {
    if [[ "$(uname -s)" != "Darwin" ]] || ! command -v launchctl >/dev/null 2>&1; then
        echo "ERROR: this script manages a macOS launchd service and only works on macOS." >&2
        echo "       On Linux, run the binary directly or use Docker (see README)." >&2
        exit 1
    fi
}

is_loaded() { launchctl print "$DOMAIN/$LABEL" &>/dev/null; }

# The port belongs to data.json (the admin panel owns it); DEFAULT_PORT applies
# only before the panel has ever saved one.
service_port() {
    local p=""
    [[ -f "$DATA_FILE" ]] && p=$(sed -n 's/.*"port"[[:space:]]*:[[:space:]]*\([0-9]\{1,\}\).*/\1/p' \
        "$DATA_FILE" | head -n 1 || true)
    p="${p:-$DEFAULT_PORT}"
    if ! [[ "$p" =~ ^[0-9]{1,5}$ ]] || ((10#$p < 1 || 10#$p > 65535)); then
        echo "ERROR: $DATA_FILE has no usable port ('$p'); set it in the admin panel." >&2
        return 1
    fi
    echo "$((10#$p))"
}

# Port the loaded job is actually listening on (empty when it has no socket).
# lsof against the job's pid is exact; the environment `launchctl print`
# reports is frozen at bootstrap time, so it keeps showing the old port after
# data.json changes.
job_port() {
    command -v lsof >/dev/null 2>&1 || return 0
    local pid
    pid=$(launchctl print "$DOMAIN/$LABEL" 2>/dev/null |
        sed -n 's/^[[:space:]]*pid = \([1-9][0-9]*\).*/\1/p' | head -n 1 || true)
    # launchd reports pid = 0 for a job with no live process (kickstart still
    # pending, or crash-looping under KeepAlive); that is not a listener.
    [[ -n "$pid" ]] || return 0
    lsof -nP -a -p "$pid" -iTCP -sTCP:LISTEN 2>/dev/null |
        sed -n 's/.*:\([0-9]\{2,5\}\) (LISTEN).*/\1/p' | head -n 1 || true
}

write_plist() {
    # A relative QODER_BIN would be resolved against WorkingDirectory, i.e. run
    # from somewhere else entirely; a label is a launchd identifier, not free
    # text. Both would only fail later, inside launchd, without naming the cause.
    if [[ "$BIN" != /* || "$RUN_DIR" != /* ]]; then
        echo "ERROR: QODER_BIN / QODER_DATA_DIR must be absolute paths." >&2
        return 1
    fi
    if ! [[ "$LABEL" =~ ^[A-Za-z0-9._-]+$ ]]; then
        echo "ERROR: QODER_LABEL may only contain letters, digits, '.', '_' and '-'." >&2
        return 1
    fi
    mkdir -p "$(dirname "$PLIST")" "$RUN_DIR" "$(dirname "$BIN")"
    # The plist is XML text: & and < in a path would otherwise make a file
    # launchd cannot parse. Escaping both via parameter expansion stays in-process.
    local b=$BIN r=$RUN_DIR
    b=${b//&/\&amp;}; r=${r//&/\&amp;}
    b=${b//</\&lt;};  r=${r//</\&lt;}
    cat >"$PLIST" <<PLIST_EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key><string>$LABEL</string>
    <key>ProgramArguments</key>
    <array><string>$b</string></array>
    <key>WorkingDirectory</key><string>$r</string>
    <key>EnvironmentVariables</key>
    <dict>
        <key>QODER_DATA_PATH</key><string>$r/data.json</string>
    </dict>
    <key>RunAtLoad</key><true/>
    <key>KeepAlive</key><true/>
    <key>ThrottleInterval</key><integer>10</integer>
    <key>StandardOutPath</key><string>$r/service.log</string>
    <key>StandardErrorPath</key><string>$r/service.err.log</string>
</dict>
</plist>
PLIST_EOF
    # A malformed plist is otherwise only discovered by `launchctl bootstrap`,
    # after install already reported success; drop it instead of leaving a file
    # that breaks every subsequent login.
    if command -v plutil >/dev/null 2>&1 && ! plutil -lint "$PLIST" >/dev/null 2>&1; then
        rm -f "$PLIST"
        echo "ERROR: generated plist failed validation: $PLIST" >&2
        return 1
    fi
    echo "Wrote $PLIST"
}

# Poll /health instead of `sleep 2` + "Started.", so a job that crash-loops on
# an unreadable data.json is not reported as successfully started.
wait_for_health() {
    command -v curl >/dev/null 2>&1 || { sleep 2; return 0; }
    local _
    for _ in 1 2 3 4 5 6 7 8 9 10; do
        curl -s --noproxy '*' -o /dev/null -m 1 -f "http://127.0.0.1:$1/health" 2>/dev/null && return 0
        sleep 0.5
    done
    echo "WARN: launched, but /health did not answer on port $1 within 5s;" >&2
    echo "      check '$0 logs'." >&2
    return 1
}

build_binary() {
    if ! command -v go >/dev/null 2>&1; then
        echo "ERROR: go not found; install Go to build the binary." >&2
        return 1
    fi
    echo "Building from source: $PROJECT_DIR"
    mkdir -p "$(dirname "$BIN")"
    # go build writes through a temp file and renames into place, so a failed
    # build leaves an existing binary intact.
    (cd "$PROJECT_DIR" && go build -o "$BIN" .) || {
        echo "ERROR: go build failed; the installed binary was left untouched." >&2
        return 1
    }
    # A missing or failing codesign must not abort the install: an unsigned
    # local build still runs, it just trips Gatekeeper on some paths.
    if ! command -v codesign >/dev/null 2>&1; then
        echo "WARN: codesign not found; continuing unsigned (install Xcode Command Line Tools to sign)."
    elif ! codesign -s - --force "$BIN" >/dev/null 2>&1; then
        echo "WARN: ad-hoc codesign failed; continuing without a signature."
    fi
    echo "Binary rebuilt at $BIN."
}

cmd_install() {
    require_macos
    local port
    port=$(service_port)
    write_plist
    if [[ ! -x "$BIN" ]]; then
        echo "Binary not found at $BIN. Building from source..."
        build_binary
    fi
    echo "Installed. The port comes from $DATA_FILE (currently $port):"
    echo "the admin panel owns it, so a panel change applies after '$0 restart'."
    echo "Use '$0 start' to start it."
}

cmd_start() {
    require_macos
    local port
    port=$(service_port)
    launchctl enable "$DOMAIN/$LABEL" 2>/dev/null || true
    # Check the loaded state BEFORE the port: when the service is already up,
    # its own listener holds the port, so "kill <pid>" would point at itself.
    if is_loaded; then
        echo "Already loaded (port $port). Use '$0 restart' to restart it."
        cmd_status
        return 0
    fi
    if lsof -nP -iTCP:"$port" -sTCP:LISTEN &>/dev/null; then
        echo "ERROR: port $port is already in use by another process:" >&2
        lsof -nP -iTCP:"$port" -sTCP:LISTEN 2>/dev/null | sed -n '2,6p' >&2 || true
        echo "Stop that process (e.g. 'kill <pid>'), then run '$0 start'." >&2
        echo "If it is a leftover qoder2api, '$0 stop' does not kill it." >&2
        return 1
    fi
    write_plist
    launchctl bootstrap "$DOMAIN" "$PLIST"
    wait_for_health "$port" || true
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
    if ! is_loaded; then
        cmd_start
        return
    fi
    local port
    port=$(service_port)
    # bootout+bootstrap rather than kickstart -k: launchd freezes a job's
    # environment at bootstrap time, so only a reload picks up a rewritten
    # plist (including dropping a QODER_PORT pin left by an older install).
    launchctl bootout "$DOMAIN/$LABEL" 2>/dev/null || true
    write_plist
    launchctl bootstrap "$DOMAIN" "$PLIST"
    wait_for_health "$port" || true
    cmd_status
}

cmd_status() {
    require_macos
    local port active url code
    # Resolve the port first: a corrupt data.json is worth reporting even when
    # the job happens to be unloaded, since it blocks every command after it.
    port=$(service_port) || return 1
    if ! is_loaded; then
        echo "Service not loaded. Use '$0 start'."
        return 0
    fi
    launchctl print "$DOMAIN/$LABEL" 2>/dev/null |
        grep -E '^[[:space:]]*(state|pid|last exit code) = ' |
        sed 's/^[[:space:]]*//' | head -n 3 || true
    # Probe the port the process is really bound to. Probing data.json's port
    # while the job still runs on the previous one is how a healthy service
    # gets reported as "not responding".
    active=$(job_port)
    if [[ -n "$active" && "$active" != "$port" ]]; then
        echo "NOTE: the running job is on port $active, but $DATA_FILE says $port;"
        echo "      run '$0 restart' to apply the change."
    fi
    url="http://127.0.0.1:${active:-$port}/health"
    if ! command -v curl >/dev/null 2>&1; then
        echo "Health: skipped (curl not found)"
    elif code=$(curl -s --noproxy '*' -o /dev/null -m 5 -w '%{http_code}' "$url" 2>/dev/null) &&
        [[ -n "$code" && "$code" != "000" ]]; then
        echo "Health: HTTP $code ($url)"
    else
        echo "Health: not responding ($url)"
        # Every log.Printf in the binary goes to stderr, so the stdout log is
        # normally empty and this file is where a startup failure shows up.
        if [[ -s "$ERRLOG" ]]; then
            echo "Recent errors ($ERRLOG):"
            tail -n 5 "$ERRLOG" | sed 's/^/  /'
        else
            echo "No output in $ERRLOG yet."
        fi
    fi
}

# logs follows stderr, which is where the application actually logs (every
# log.Printf in the binary goes there). --out follows stdout, normally empty.
cmd_logs() {
    local file="$ERRLOG"
    case "${1:-}" in
        --out | out) file="$LOG" ;;
        "" | --err | err) ;;
        *)
            echo "Usage: $0 logs [--err|--out]" >&2
            return 1
            ;;
    esac
    if [[ ! -f "$file" ]]; then
        echo "No log file at $file yet (the service writes it after the first start)." >&2
        return 1
    fi
    # exec: replace the shell with tail so the tracked pid *is* the follower;
    # otherwise killing the wrapper leaves an orphaned `tail -f` on the log.
    exec tail -n 50 -f "$file"
}

cmd_off() {
    require_macos
    if is_loaded; then launchctl bootout "$DOMAIN/$LABEL" || true; fi
    # Keep the disable flag even when the job is already unloaded: a stale
    # `disable` from an earlier `off` is what stops the login auto-start.
    launchctl disable "$DOMAIN/$LABEL" 2>/dev/null || true
    echo "Disabled: it will NOT start at login anymore. Use '$0 on' to re-enable."
}

cmd_on() { cmd_start; }

# Rebuild the binary from source and reinstall it (run from project dir).
cmd_update() {
    require_macos
    build_binary
    if is_loaded; then cmd_restart; else echo "Service not running. Use '$0 start'."; fi
}

usage() {
    echo "Usage: $0 {install|start|stop|restart|status|logs[--out]|off|on|update}"
}

case "${1:-status}" in
    install) cmd_install ;;
    start) cmd_start ;;
    stop) cmd_stop ;;
    restart) cmd_restart ;;
    status) cmd_status ;;
    logs) shift; cmd_logs "${1:-}" ;;
    off) cmd_off ;;
    on) cmd_on ;;
    update) cmd_update ;;
    -h | --help | help) usage ;;
    *)
        usage >&2
        exit 1
        ;;
esac
