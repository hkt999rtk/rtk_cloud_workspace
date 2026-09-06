#!/usr/bin/env bash
set -euo pipefail

WORKSPACE_DIR="${WORKSPACE_DIR:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)}"
BINARY_DIR="${BINARY_DIR:-$HOME/.local/bin}"
BINARY_NAME="rtk-ci-dashboard"
BINARY_PATH="${BINARY_DIR}/${BINARY_NAME}"
PLIST_NAME="com.rtk-ci-dashboard.plist"
PLIST_PATH="$HOME/Library/LaunchAgents/$PLIST_NAME"
ADDRESS="${ADDRESS:-127.0.0.1:8787}"
UNINSTALL="${1:-0}"

if [[ "$UNINSTALL" == "uninstall" ]]; then
  launchctl bootout "gui/$(id -u)" "$PLIST_PATH" 2>/dev/null || true
  rm -f "$PLIST_PATH"
  echo "Removed $PLIST_PATH"
  echo "To uninstall binary, run: rm -f \"$BINARY_PATH\""
  exit 0
fi

if [[ "$UNINSTALL" != "0" ]]; then
  echo "Usage: $(basename "$0") [uninstall]" >&2
  echo "  Optional env: WORKSPACE_DIR (default to repo root), ADDRESS (default 127.0.0.1:8787), BINARY_DIR (default \$HOME/.local/bin)" >&2
  exit 1
fi

tmp_bin="$(mktemp)"
trap 'rm -f "$tmp_bin"' EXIT

(
  cd "$WORKSPACE_DIR/tools/ci-dashboard"
  GOWORK=off go build -o "$tmp_bin" .
)

echo "Install binary to $BINARY_PATH ..."
mkdir -p "$BINARY_DIR"
if ! install -m 0755 "$tmp_bin" "$BINARY_PATH" 2>/tmp/rtk-ci-dashboard-install.err; then
  echo "安裝到 ${BINARY_DIR} 失敗，請確認該目錄可寫："
  echo "  mkdir -p ${BINARY_DIR} && chmod u+w ${BINARY_DIR}"
  cat /tmp/rtk-ci-dashboard-install.err >&2
  exit 1
fi

mkdir -p "$HOME/Library/LaunchAgents"

cat > "$PLIST_PATH" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
  <dict>
    <key>Label</key>
    <string>com.rtk-ci-dashboard</string>
    <key>ProgramArguments</key>
    <array>
      <string>$BINARY_PATH</string>
      <string>-address</string>
      <string>$ADDRESS</string>
      <string>-workspace</string>
      <string>$WORKSPACE_DIR</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>LimitLoadToSessionType</key>
    <string>Aqua</string>
    <key>WorkingDirectory</key>
    <string>$WORKSPACE_DIR</string>
    <key>StandardOutPath</key>
    <string>/tmp/rtk-ci-dashboard.log</string>
    <key>StandardErrorPath</key>
    <string>/tmp/rtk-ci-dashboard.err</string>
    <key>EnvironmentVariables</key>
    <dict>
      <key>PATH</key>
      <string>/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
    </dict>
  </dict>
</plist>
EOF

chmod 0644 "$PLIST_PATH"

launchctl bootout "gui/$(id -u)" "$PLIST_PATH" 2>/dev/null || true
launchctl bootstrap "gui/$(id -u)" "$PLIST_PATH"
launchctl enable "gui/$(id -u)/com.rtk-ci-dashboard"

echo "Installed. plist: $PLIST_PATH"
echo "Logs: /tmp/rtk-ci-dashboard.log and /tmp/rtk-ci-dashboard.err"
