package mounts

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

var (
	mountMu          sync.Mutex
	validProfileName = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)
)

type MountUser struct {
	Name        string   `json:"name"`
	Username    string   `json:"username"`
	Domain      string   `json:"domain"`
	HasPassword bool     `json:"has_password"`
	CredPath    string   `json:"cred_path"`
	UsedBy      []string `json:"used_by"`
}

type SambaMount struct {
	RemotePath   string `json:"remote_path"`
	MountPoint   string `json:"mount_point"`
	MountUser    string `json:"mount_user"`
	Guest        bool   `json:"guest"`
	ReadOnly     bool   `json:"read_only"`
	SmbVersion   string `json:"smb_version"`
	UID          string `json:"uid"`
	GID          string `json:"gid"`
	ExtraOptions string `json:"extra_options"`
	RawOptions   string `json:"raw_options"`
	Mounted      bool   `json:"mounted"`
	InFstab      bool   `json:"in_fstab"`
}

func ValidateProfileName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("profile name cannot be empty")
	}
	if len(name) > 64 {
		return errors.New("profile name too long (max 64 characters)")
	}
	if !validProfileName.MatchString(name) {
		return errors.New("profile name can only contain letters, numbers, dots, underscores, and hyphens")
	}
	return nil
}

func ValidateRemoteUNC(remote string) (string, error) {
	remote = strings.TrimSpace(strings.ReplaceAll(remote, `\`, `/`))
	if !strings.HasPrefix(remote, "//") {
		return "", errors.New("remote share must be a UNC path starting with //server/share")
	}
	trimmed := strings.TrimPrefix(remote, "//")
	parts := strings.SplitN(trimmed, "/", 2)
	if len(parts) < 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", errors.New("remote share must include both server and share name (e.g. //192.168.1.10/media)")
	}
	if strings.ContainsAny(remote, " \t\n\r") {
		return "", errors.New("remote share path cannot contain spaces (use \\040 for spaces in fstab or avoid spaces)")
	}
	return remote, nil
}

func ValidateMountPoint(mp string) (string, error) {
	mp = strings.TrimSpace(mp)
	if mp == "" {
		return "", errors.New("mount point cannot be empty")
	}
	if !strings.HasPrefix(mp, "/") {
		return "", errors.New("mount point must be an absolute path starting with /")
	}
	if strings.ContainsAny(mp, " \t\n\r") {
		return "", errors.New("mount point cannot contain whitespace")
	}
	clean := filepath.Clean(mp)
	forbidden := []string{"/", "/etc", "/proc", "/sys", "/dev", "/boot", "/bin", "/sbin", "/usr", "/var", "/root", "/run"}
	for _, f := range forbidden {
		if clean == f {
			return "", fmt.Errorf("cannot mount directly onto system directory %s", f)
		}
	}
	for _, prefix := range []string{"/etc/", "/proc/", "/sys/", "/dev/", "/boot/", "/bin/", "/sbin/"} {
		if strings.HasPrefix(clean, prefix) {
			return "", fmt.Errorf("cannot mount inside critical system directory %s", prefix)
		}
	}
	return clean, nil
}

func CredFilePath(credDir, profileName string) string {
	return filepath.Join(credDir, profileName+".cred")
}

func ListMountUsers(credDir, fstabPath string) ([]MountUser, error) {
	mountMu.Lock()
	defer mountMu.Unlock()

	if err := os.MkdirAll(credDir, 0700); err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(credDir)
	if err != nil {
		return nil, err
	}

	fstabMounts, _ := parseFstabMounts(fstabPath, credDir)

	var users []MountUser
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".cred") {
			continue
		}
		profileName := strings.TrimSuffix(entry.Name(), ".cred")
		fullPath := filepath.Join(credDir, entry.Name())
		uname, pass, dom := readCredFile(fullPath)

		var usedBy []string
		for _, m := range fstabMounts {
			if m.MountUser == profileName {
				usedBy = append(usedBy, m.MountPoint)
			}
		}

		users = append(users, MountUser{
			Name:        profileName,
			Username:    uname,
			Domain:      dom,
			HasPassword: pass != "",
			CredPath:    fullPath,
			UsedBy:      usedBy,
		})
	}
	return users, nil
}

func readCredFile(path string) (username, password, domain string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", ""
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		k := strings.ToLower(strings.TrimSpace(parts[0]))
		v := strings.TrimSpace(parts[1])
		switch k {
		case "username", "user":
			username = v
		case "password", "pass":
			password = v
		case "domain", "workgroup":
			domain = v
		}
	}
	return username, password, domain
}

func sanitizeForFilename(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	res := strings.Trim(b.String(), "-._")
	if res == "" {
		return "smbuser"
	}
	if len(res) > 48 {
		res = res[:48]
	}
	return res
}

func SaveMountUser(credDir, profileName, username, password, domain string) (string, error) {
	username = strings.TrimSpace(username)
	domain = strings.TrimSpace(domain)
	profileName = strings.TrimSpace(profileName)
	if username == "" {
		return "", errors.New("SMB username cannot be empty")
	}
	if strings.ContainsAny(username, "\n\r") || strings.ContainsAny(password, "\n\r") || strings.ContainsAny(domain, "\n\r") {
		return "", errors.New("credentials cannot contain newline characters")
	}

	mountMu.Lock()
	defer mountMu.Unlock()

	if err := os.MkdirAll(credDir, 0700); err != nil {
		return "", err
	}

	if profileName == "" {
		// Auto-generate readable filename with username visible (e.g. nasadmin.cred, or nasadmin-2.cred if another exists)
		base := sanitizeForFilename(username)
		candidate := base
		for i := 2; i < 1000; i++ {
			if _, err := os.Stat(CredFilePath(credDir, candidate)); os.IsNotExist(err) {
				break
			}
			candidate = fmt.Sprintf("%s-%d", base, i)
		}
		profileName = candidate
	} else {
		if err := ValidateProfileName(profileName); err != nil {
			return "", err
		}
	}

	fullPath := CredFilePath(credDir, profileName)
	if password == "" {
		// Check if updating an existing profile and preserve existing password
		_, existingPass, _ := readCredFile(fullPath)
		if existingPass != "" {
			password = existingPass
		} else {
			return "", errors.New("password cannot be empty when creating a new mount user credential")
		}
	}

	var buf bytes.Buffer
	buf.WriteString(fmt.Sprintf("username=%s\n", username))
	buf.WriteString(fmt.Sprintf("password=%s\n", password))
	if domain != "" {
		buf.WriteString(fmt.Sprintf("domain=%s\n", domain))
	}

	if err := os.WriteFile(fullPath, buf.Bytes(), 0600); err != nil {
		return "", err
	}
	return profileName, nil
}

func DeleteMountUser(credDir, fstabPath, profileName string) error {
	if err := ValidateProfileName(profileName); err != nil {
		return err
	}

	mountMu.Lock()
	defer mountMu.Unlock()

	fstabMounts, _ := parseFstabMounts(fstabPath, credDir)
	for _, m := range fstabMounts {
		if m.MountUser == profileName {
			return fmt.Errorf("credential profile %q is currently used by mount %s in /etc/fstab; remove or update the mount first", profileName, m.MountPoint)
		}
	}

	fullPath := CredFilePath(credDir, profileName)
	if err := os.Remove(fullPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("credential profile %q not found", profileName)
		}
		return err
	}
	return nil
}

func ListMounts(fstabPath, credDir string) ([]SambaMount, error) {
	mountMu.Lock()
	defer mountMu.Unlock()

	fstabMounts, err := parseFstabMounts(fstabPath, credDir)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}

	activeMap := getActiveCifsMounts(credDir)

	var result []SambaMount
	seenMountPoints := make(map[string]bool)

	for _, m := range fstabMounts {
		if _, active := activeMap[m.MountPoint]; active {
			m.Mounted = true
		}
		seenMountPoints[m.MountPoint] = true
		result = append(result, m)
	}

	// Include any live CIFS mounts that are not in /etc/fstab
	for mp, activeMount := range activeMap {
		if !seenMountPoints[mp] {
			result = append(result, activeMount)
		}
	}

	return result, nil
}

func parseFstabMounts(fstabPath, credDir string) ([]SambaMount, error) {
	data, err := os.ReadFile(fstabPath)
	if err != nil {
		return nil, err
	}

	var mounts []SambaMount
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		fsType := strings.ToLower(fields[2])
		if fsType != "cifs" && fsType != "smb3" {
			continue
		}
		m := parseMountOptions(fields[0], filepath.Clean(fields[1]), fields[3], credDir)
		m.InFstab = true
		mounts = append(mounts, m)
	}
	return mounts, scanner.Err()
}

func getActiveCifsMounts(credDir string) map[string]SambaMount {
	res := make(map[string]SambaMount)
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return res
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 4 {
			continue
		}
		fsType := strings.ToLower(fields[2])
		if fsType != "cifs" && fsType != "smb3" {
			continue
		}
		mp := filepath.Clean(strings.ReplaceAll(fields[1], `\040`, " "))
		m := parseMountOptions(fields[0], mp, fields[3], credDir)
		m.Mounted = true
		m.InFstab = false
		res[mp] = m
	}
	return res
}

func parseMountOptions(remote, mountPoint, rawOpts, credDir string) SambaMount {
	m := SambaMount{
		RemotePath: remote,
		MountPoint: mountPoint,
		RawOptions: rawOpts,
	}

	opts := strings.Split(rawOpts, ",")
	var extra []string
	for _, opt := range opts {
		opt = strings.TrimSpace(opt)
		if opt == "" {
			continue
		}
		switch {
		case opt == "ro":
			m.ReadOnly = true
		case opt == "rw":
			m.ReadOnly = false
		case opt == "guest":
			m.Guest = true
		case opt == "_netdev" || opt == "nofail":
			// Standard managed persistence options
		case strings.HasPrefix(opt, "credentials="):
			cpath := strings.TrimPrefix(opt, "credentials=")
			base := filepath.Base(cpath)
			if strings.HasSuffix(base, ".cred") {
				m.MountUser = strings.TrimSuffix(base, ".cred")
			} else {
				m.MountUser = base
			}
		case strings.HasPrefix(opt, "vers="):
			m.SmbVersion = strings.TrimPrefix(opt, "vers=")
		case strings.HasPrefix(opt, "uid="):
			m.UID = strings.TrimPrefix(opt, "uid=")
		case strings.HasPrefix(opt, "gid="):
			m.GID = strings.TrimPrefix(opt, "gid=")
		default:
			extra = append(extra, opt)
		}
	}
	m.ExtraOptions = strings.Join(extra, ",")
	return m
}

func buildMountOptions(m SambaMount, credDir string) (string, error) {
	var opts []string
	if m.ReadOnly {
		opts = append(opts, "ro")
	} else {
		opts = append(opts, "rw")
	}

	if m.Guest || m.MountUser == "" {
		opts = append(opts, "guest")
	} else {
		if err := ValidateProfileName(m.MountUser); err != nil {
			return "", err
		}
		cpath := CredFilePath(credDir, m.MountUser)
		if _, err := os.Stat(cpath); err != nil {
			return "", fmt.Errorf("mount user credential file %s does not exist", cpath)
		}
		opts = append(opts, "credentials="+cpath)
	}

	if m.SmbVersion != "" {
		opts = append(opts, "vers="+strings.TrimSpace(m.SmbVersion))
	}
	if m.UID != "" {
		opts = append(opts, "uid="+strings.TrimSpace(m.UID))
	}
	if m.GID != "" {
		opts = append(opts, "gid="+strings.TrimSpace(m.GID))
	}

	opts = append(opts, "_netdev", "nofail")

	if m.ExtraOptions != "" {
		for _, eo := range strings.Split(m.ExtraOptions, ",") {
			eo = strings.TrimSpace(eo)
			if eo != "" && !strings.ContainsAny(eo, " \t\n\r") {
				opts = append(opts, eo)
			}
		}
	}
	return strings.Join(opts, ","), nil
}

func SaveMount(fstabPath, credDir, originalMountPoint string, req SambaMount, mountNow bool) (string, error) {
	remote, err := ValidateRemoteUNC(req.RemotePath)
	if err != nil {
		return "", err
	}
	mp, err := ValidateMountPoint(req.MountPoint)
	if err != nil {
		return "", err
	}
	req.RemotePath = remote
	req.MountPoint = mp

	optsStr, err := buildMountOptions(req, credDir)
	if err != nil {
		return "", err
	}

	mountMu.Lock()
	defer mountMu.Unlock()

	if err := os.MkdirAll(mp, 0755); err != nil {
		return "", fmt.Errorf("failed to create mount point directory %s: %w", mp, err)
	}

	existingData, err := os.ReadFile(fstabPath)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if len(existingData) > 0 {
		_ = os.WriteFile(fstabPath+".bak", existingData, 0644)
	}

	origClean := ""
	if originalMountPoint != "" {
		origClean = filepath.Clean(originalMountPoint)
	}

	lines := strings.Split(string(existingData), "\n")
	newFstabLine := fmt.Sprintf("%s\t%s\tcifs\t%s\t0\t0", remote, mp, optsStr)

	replaced := false
	var outLines []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			fields := strings.Fields(trimmed)
			if len(fields) >= 3 && (fields[2] == "cifs" || fields[2] == "smb3") {
				entryMP := filepath.Clean(fields[1])
				if origClean != "" && entryMP == origClean {
					outLines = append(outLines, newFstabLine)
					replaced = true
					continue
				}
				if origClean == "" && entryMP == mp {
					return "", fmt.Errorf("a CIFS mount at %s already exists in /etc/fstab", mp)
				}
			}
		}
		outLines = append(outLines, line)
	}

	if !replaced {
		// Remove trailing empty line before appending cleanly
		for len(outLines) > 0 && strings.TrimSpace(outLines[len(outLines)-1]) == "" {
			outLines = outLines[:len(outLines)-1]
		}
		outLines = append(outLines, newFstabLine, "")
	}

	if err := os.WriteFile(fstabPath, []byte(strings.Join(outLines, "\n")), 0644); err != nil {
		return "", err
	}

	_ = exec.Command("systemctl", "daemon-reload").Run()

	if mountNow {
		out, err := exec.Command("mount", mp).CombinedOutput()
		if err != nil {
			return fmt.Sprintf("Saved to /etc/fstab, but immediate mount returned: %s", strings.TrimSpace(string(out))), nil
		}
		return "Saved to /etc/fstab and mounted successfully", nil
	}

	return "Saved to /etc/fstab", nil
}

func DeleteMount(fstabPath, mountPoint string) error {
	mp, err := ValidateMountPoint(mountPoint)
	if err != nil {
		return err
	}

	mountMu.Lock()
	defer mountMu.Unlock()

	// Unmount if currently mounted
	_ = exec.Command("umount", mp).Run()

	existingData, err := os.ReadFile(fstabPath)
	if err != nil {
		return err
	}
	_ = os.WriteFile(fstabPath+".bak", existingData, 0644)

	lines := strings.Split(string(existingData), "\n")
	var outLines []string
	removed := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			fields := strings.Fields(trimmed)
			if len(fields) >= 3 && (fields[2] == "cifs" || fields[2] == "smb3") {
				if filepath.Clean(fields[1]) == mp {
					removed = true
					continue
				}
			}
		}
		outLines = append(outLines, line)
	}

	if removed {
		if err := os.WriteFile(fstabPath, []byte(strings.Join(outLines, "\n")), 0644); err != nil {
			return err
		}
		_ = exec.Command("systemctl", "daemon-reload").Run()
	}
	return nil
}

func ToggleMount(mountPoint string, action string) error {
	mp, err := ValidateMountPoint(mountPoint)
	if err != nil {
		return err
	}
	switch action {
	case "mount":
		if out, err := exec.Command("mount", mp).CombinedOutput(); err != nil {
			return fmt.Errorf("mount failed: %s (%w)", strings.TrimSpace(string(out)), err)
		}
	case "unmount", "umount":
		if out, err := exec.Command("umount", mp).CombinedOutput(); err != nil {
			// Fallback to lazy unmount if busy
			if outLazy, errLazy := exec.Command("umount", "-l", mp).CombinedOutput(); errLazy != nil {
				return fmt.Errorf("umount failed: %s / %s", strings.TrimSpace(string(out)), strings.TrimSpace(string(outLazy)))
			}
		}
	default:
		return fmt.Errorf("invalid mount action: %s", action)
	}
	return nil
}
