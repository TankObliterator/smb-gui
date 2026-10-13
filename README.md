# Samba Manager (`smb-gui`)

A lightweight, native Linux Samba & CIFS Web UI written in Go (single compiled binary with embedded dark-mode UI, zero runtime dependencies, ~5–10 MB RAM).

All configurations are written directly to standard Linux files (`/etc/samba/smb.conf`, `smbpasswd`/`pdbedit`, `/etc/fstab`, and `/etc/samba/credentials/*.cred`), so your shares and mounts continue working even if `smb-gui` is stopped or uninstalled.

---

## Install (Installs Dependencies + Builds & Starts `smb-gui`)
```bash
curl -fsSL https://raw.githubusercontent.com/TankObliterator/smb-gui/main/install.sh | sudo bash
```
Once installed, open **`http://localhost:5000`** (or `http://<server-ip>:5000`).

## Uninstall (Keeps Native Shares, Users & Mounts Intact)
```bash
curl -fsSL https://raw.githubusercontent.com/TankObliterator/smb-gui/main/uninstall.sh | sudo bash
```

## Full Uninstall (Removes `smb-gui`, Dependencies & All Samba/CIFS Configs)
```bash
curl -fsSL https://raw.githubusercontent.com/TankObliterator/smb-gui/main/uninstall.sh | sudo bash -s -- --full
```

---

## Features
- **Dashboard** (`/dashboard`): Live `smbd` / `nmbd` daemon controls, active client sessions, and overview of shares & mounts.
- **Samba Shares** (`/shares`): Create, edit, and delete shares in `/etc/samba/smb.conf`.
- **Share Users** (`/share-users`): Manage Samba user accounts natively via `smbpasswd` and `pdbedit`.
- **Samba Mounts** (`/mounts`): Manage remote CIFS/SMB mounts in `/etc/fstab` with instant mount/unmount.
- **Mount Users** (`/mount-users`): Manage root-only (`0600`) CIFS credential files in `/etc/samba/credentials/`.
- **Disk Analytics** (`/analytics`): Instantaneous `statvfs` storage metrics for configured Samba shares and CIFS mounts.
- **Settings** (`/settings`): Global `[global]` server settings, optional Web UI password, `smb.conf` backup, and on-demand updates via GitHub/jsDelivr.
