#!/usr/bin/env bash
set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
  echo "Error: Please run as root (e.g. curl -fsSL https://raw.githubusercontent.com/TankObliterator/smb-gui/main/install.sh | sudo bash)"
  exit 1
fi

REPO_URL="https://github.com/TankObliterator/smb-gui.git"
PORT="${SMB_GUI_PORT:-5000}"

echo "=== Installing Samba Manager (smb-gui) ==="

# 1. Install native Linux dependencies (Samba server, client, cifs-utils, git, Go compiler)
install_packages() {
  if command -v apt-get >/dev/null 2>&1; then
    export DEBIAN_FRONTEND=noninteractive
    apt-get update -qq
    apt-get install -y -qq --no-install-recommends git curl ca-certificates samba samba-common-bin smbclient cifs-utils golang-go
  elif command -v dnf >/dev/null 2>&1; then
    dnf install -y git curl ca-certificates samba samba-common-tools samba-client cifs-utils golang
  elif command -v yum >/dev/null 2>&1; then
    yum install -y git curl ca-certificates samba samba-common samba-client cifs-utils golang
  elif command -v pacman >/dev/null 2>&1; then
    pacman -Sy --noconfirm git curl ca-certificates samba cifs-utils go
  elif command -v zypper >/dev/null 2>&1; then
    zypper install -y git curl ca-certificates samba samba-client cifs-utils go
  else
    echo "Warning: Could not detect package manager. Ensure git, samba, cifs-utils, and go are installed."
  fi
}

echo "-> Ensuring required dependencies (samba, cifs-utils, git, go) are installed..."
install_packages

# 2. Obtain source (local checkout or clone from GitHub when piped via curl)
CLEANUP_DIR=""
SCRIPT_DIR=""
if [ -n "${BASH_SOURCE[0]:-}" ] && [ -f "$(dirname "${BASH_SOURCE[0]}")/main.go" ]; then
  SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
else
  CLEANUP_DIR="$(mktemp -d /tmp/smb-gui-install.XXXXXX)"
  trap 'rm -rf "$CLEANUP_DIR"' EXIT
  echo "-> Fetching latest source from ${REPO_URL}..."
  git clone --depth 50 "$REPO_URL" "$CLEANUP_DIR"
  SCRIPT_DIR="$CLEANUP_DIR"
fi

# 3. Build static Go binary with embedded Web UI
echo "-> Compiling single static binary /usr/local/bin/smb-gui..."
cd "$SCRIPT_DIR"
CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /usr/local/bin/smb-gui .
chmod 0755 /usr/local/bin/smb-gui

# 4. Compute commit-date version tag (YYYY.MM.DD or YYYY.MM.DD-#)
mkdir -p /etc/smb-gui
chmod 0700 /etc/smb-gui
if git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  LATEST_DATE="$(git log -n 1 --date=format:%Y.%m.%d --format=%cd 2>/dev/null || date +%Y.%m.%d)"
  SAME_DAY_COUNT="$(git log -n 50 --date=format:%Y.%m.%d --format=%cd 2>/dev/null | awk -v d="$LATEST_DATE" '$0 == d {c++} $0 != d {exit} END {print c+0}')"
  if [ "${SAME_DAY_COUNT:-1}" -gt 1 ]; then
    echo "${LATEST_DATE}-${SAME_DAY_COUNT}" >/etc/smb-gui/version
  else
    echo "${LATEST_DATE}" >/etc/smb-gui/version
  fi
else
  date +%Y.%m.%d >/etc/smb-gui/version
fi

# 5. Ensure native directories & group exist
mkdir -p /etc/samba/credentials
chmod 0700 /etc/samba/credentials
groupadd -f smbusers || true

# 6. Install convenient uninstall command
cp "$SCRIPT_DIR/uninstall.sh" /usr/local/bin/smb-gui-uninstall
chmod 0755 /usr/local/bin/smb-gui-uninstall

# 7. Create and enable systemd background service
cat >/etc/systemd/system/smb-gui.service <<EOF
[Unit]
Description=Samba Manager Web UI (smb-gui)
After=network.target smbd.service nmbd.service

[Service]
Type=simple
User=root
ExecStart=/usr/local/bin/smb-gui
Restart=on-failure
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable smbd >/dev/null 2>&1 || true
systemctl start smbd >/dev/null 2>&1 || true
systemctl enable nmbd >/dev/null 2>&1 || true
systemctl start nmbd >/dev/null 2>&1 || true

systemctl enable smb-gui.service
systemctl restart smb-gui.service

IP_ADDR="$(hostname -I 2>/dev/null | awk '{print $1}')"
if [ -z "$IP_ADDR" ]; then
  IP_ADDR="localhost"
fi

echo ""
echo "=== Samba Manager (smb-gui) Installed & Running ==="
echo "Web UI URL (Local):   http://localhost:${PORT}"
echo "Web UI URL (Network): http://${IP_ADDR}:${PORT}"
echo "Uninstall (keep data): sudo smb-gui-uninstall"
echo "Full Uninstall:        sudo smb-gui-uninstall --full"
