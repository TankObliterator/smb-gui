#!/usr/bin/env bash
set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
  echo "Error: Please run as root (sudo smb-gui-uninstall or sudo ./uninstall.sh)"
  exit 1
fi

FULL_UNINSTALL=false
for arg in "$@"; do
  case "$arg" in
    --full|-f|--purge)
      FULL_UNINSTALL=true
      ;;
  esac
done

echo "=== Uninstalling Samba Manager (smb-gui) ==="

# 1. Stop and disable smb-gui systemd service
if command -v systemctl >/dev/null 2>&1; then
  systemctl stop smb-gui.service >/dev/null 2>&1 || true
  systemctl disable smb-gui.service >/dev/null 2>&1 || true
  rm -f /etc/systemd/system/smb-gui.service
  systemctl daemon-reload
fi

# 2. Remove smb-gui binary and Web UI config
rm -f /usr/local/bin/smb-gui
rm -f /usr/local/bin/smb-gui-uninstall
rm -rf /etc/smb-gui

if [ "$FULL_UNINSTALL" = false ]; then
  echo ""
  echo "=== smb-gui Uninstalled (Native Data Preserved) ==="
  echo "Your native Linux Samba shares, users, and mounts remain active:"
  echo " - Samba Shares & Config:  /etc/samba/smb.conf"
  echo " - Samba Share Users:      tdbsam (smbpasswd / pdbedit)"
  echo " - Samba Mounts:           /etc/fstab"
  echo " - Samba Mount Users:      /etc/samba/credentials/*.cred"
  exit 0
fi

echo "-> Performing FULL uninstall (removing CIFS mounts, Samba configs, and packages)..."

# 3. Unmount active CIFS mounts and remove CIFS entries from /etc/fstab
if [ -f /etc/fstab ]; then
  awk '$3 == "cifs" || $3 == "smb3" {print $2}' /etc/fstab | while read -r mp; do
    if [ -n "$mp" ]; then
      umount "$mp" >/dev/null 2>&1 || umount -l "$mp" >/dev/null 2>&1 || true
    fi
  done
  cp /etc/fstab /etc/fstab.smb-gui.bak
  awk '$3 != "cifs" && $3 != "smb3"' /etc/fstab.smb-gui.bak >/etc/fstab
fi

# 4. Stop Samba services
if command -v systemctl >/dev/null 2>&1; then
  systemctl stop smbd nmbd smb nmb >/dev/null 2>&1 || true
  systemctl disable smbd nmbd smb nmb >/dev/null 2>&1 || true
fi

# 5. Remove Samba & CIFS packages
if command -v apt-get >/dev/null 2>&1; then
  export DEBIAN_FRONTEND=noninteractive
  apt-get purge -y -qq samba samba-common samba-common-bin smbclient cifs-utils || true
  apt-get autoremove -y -qq || true
elif command -v dnf >/dev/null 2>&1; then
  dnf remove -y samba samba-common-tools samba-client cifs-utils || true
elif command -v yum >/dev/null 2>&1; then
  yum remove -y samba samba-common samba-client cifs-utils || true
elif command -v pacman >/dev/null 2>&1; then
  pacman -Rns --noconfirm samba cifs-utils || true
elif command -v zypper >/dev/null 2>&1; then
  zypper remove -y samba samba-client cifs-utils || true
fi

# 6. Remove remaining Samba configuration & credential directories
rm -rf /etc/samba /var/lib/samba /var/log/samba

echo ""
echo "=== Full Uninstall Complete ==="
echo "Removed smb-gui, Samba/CIFS packages, /etc/samba configs, and CIFS mounts."
