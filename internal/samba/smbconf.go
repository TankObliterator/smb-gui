package samba

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
	smbMu          sync.Mutex
	validShareName = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)
)

type Share struct {
	Name           string            `json:"name"`
	Path           string            `json:"path"`
	Comment        string            `json:"comment"`
	Browseable     bool              `json:"browseable"`
	ReadOnly       bool              `json:"read_only"`
	GuestOK        bool              `json:"guest_ok"`
	ValidUsers     string            `json:"valid_users"`
	WriteList      string            `json:"write_list"`
	CreateMask     string            `json:"create_mask"`
	DirectoryMask  string            `json:"directory_mask"`
	ForceUser      string            `json:"force_user"`
	ForceGroup     string            `json:"force_group"`
	MaxConnections string            `json:"max_connections"`
	PathExists     bool              `json:"path_exists"`
	IsSystem       bool              `json:"is_system"`
	Extra          map[string]string `json:"extra,omitempty"`
}

type GlobalSettings struct {
	Workgroup            string            `json:"workgroup"`
	ServerString         string            `json:"server_string"`
	Security             string            `json:"security"`
	MapToGuest           string            `json:"map_to_guest"`
	GuestAccount         string            `json:"guest_account"`
	LogLevel             string            `json:"log_level"`
	HostsAllow           string            `json:"hosts_allow"`
	HostsDeny            string            `json:"hosts_deny"`
	UsershareAllowGuests string            `json:"usershare_allow_guests"`
	Extra                map[string]string `json:"extra,omitempty"`
}

type ServiceStatus struct {
	Installed   bool   `json:"installed"`
	SmbdStatus  string `json:"smbd_status"`
	NmbdStatus  string `json:"nmbd_status"`
	Version     string `json:"version"`
	Connections int    `json:"connections"`
}

type smbSection struct {
	name    string
	entries []smbKeyVal
}

type smbKeyVal struct {
	key string
	val string
}

func ValidateShareName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("share name cannot be empty")
	}
	if len(name) > 80 {
		return errors.New("share name too long (max 80 characters)")
	}
	if !validShareName.MatchString(name) {
		return errors.New("share name can only contain letters, numbers, dots, underscores, and hyphens")
	}
	lower := strings.ToLower(name)
	if lower == "global" || lower == "homes" || lower == "printers" || lower == "print$" {
		return fmt.Errorf("share name %q is reserved by Samba", name)
	}
	return nil
}

func ValidateSharePath(p string) error {
	p = strings.TrimSpace(p)
	if p == "" {
		return errors.New("share path cannot be empty")
	}
	if !strings.HasPrefix(p, "/") {
		return errors.New("share path must be an absolute Linux path starting with /")
	}
	clean := filepath.Clean(p)
	forbidden := []string{"/etc", "/proc", "/sys", "/dev", "/boot", "/bin", "/sbin", "/usr/bin", "/usr/sbin"}
	for _, f := range forbidden {
		if clean == f || strings.HasPrefix(clean, f+"/") {
			return fmt.Errorf("cannot share sensitive system directory %s", f)
		}
	}
	return nil
}

func EnsureDefaultSmbConf(confPath string) error {
	if _, err := os.Stat(confPath); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(confPath), 0755); err != nil {
		return err
	}
	defaultContent := `[global]
   workgroup = WORKGROUP
   server string = Samba Server (%h)
   security = user
   map to guest = Bad User
   guest account = nobody
   log file = /var/log/samba/log.%m
   max log size = 1000
   logging = file
   server role = standalone server
   obey pam restrictions = yes
   unix password sync = yes
   pam password change = yes
   usershare allow guests = yes
`
	return os.WriteFile(confPath, []byte(defaultContent), 0644)
}

func parseSmbConf(confPath string) ([]smbSection, error) {
	if err := EnsureDefaultSmbConf(confPath); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(confPath)
	if err != nil {
		return nil, err
	}

	var sections []smbSection
	currentIdx := -1

	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			secName := strings.TrimSpace(line[1 : len(line)-1])
			sections = append(sections, smbSection{name: secName})
			currentIdx = len(sections) - 1
			continue
		}
		if currentIdx >= 0 {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				k := normalizeKey(parts[0])
				v := strings.TrimSpace(parts[1])
				sections[currentIdx].entries = append(sections[currentIdx].entries, smbKeyVal{key: k, val: v})
			}
		}
	}
	return sections, scanner.Err()
}

func writeSmbConf(confPath string, sections []smbSection) error {
	// Backup existing file if it exists
	if existing, err := os.ReadFile(confPath); err == nil {
		_ = os.WriteFile(confPath+".bak", existing, 0644)
	}

	var buf bytes.Buffer
	buf.WriteString("# Managed by smb-gui (Native Samba Configuration)\n")
	buf.WriteString("# Standard /etc/samba/smb.conf - Fully functional without smb-gui installed.\n\n")

	for i, sec := range sections {
		if i > 0 {
			buf.WriteString("\n")
		}
		buf.WriteString(fmt.Sprintf("[%s]\n", sec.name))
		for _, kv := range sec.entries {
			if strings.TrimSpace(kv.val) == "" {
				continue
			}
			buf.WriteString(fmt.Sprintf("   %s = %s\n", kv.key, kv.val))
		}
	}

	return os.WriteFile(confPath, buf.Bytes(), 0644)
}

func normalizeKey(k string) string {
	k = strings.ToLower(strings.TrimSpace(k))
	fields := strings.Fields(k)
	k = strings.Join(fields, " ")
	switch k {
	case "browsable":
		return "browseable"
	case "writeable":
		return "writable"
	case "public":
		return "guest ok"
	case "create mode":
		return "create mask"
	case "directory mode":
		return "directory mask"
	}
	return k
}

func parseBool(v string, defaultVal bool) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "yes", "true", "1":
		return true
	case "no", "false", "0":
		return false
	default:
		return defaultVal
	}
}

func boolToYesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func ListShares(confPath string) ([]Share, error) {
	smbMu.Lock()
	defer smbMu.Unlock()

	sections, err := parseSmbConf(confPath)
	if err != nil {
		return nil, err
	}

	var shares []Share
	for _, sec := range sections {
		lower := strings.ToLower(sec.name)
		if lower == "global" || lower == "printers" || lower == "print$" {
			continue
		}

		sh := Share{
			Name:          sec.name,
			Browseable:    true,
			ReadOnly:      false,
			GuestOK:       false,
			CreateMask:    "0744",
			DirectoryMask: "0755",
			IsSystem:      lower == "homes",
			Extra:         make(map[string]string),
		}

		hasReadOnly := false
		hasWritable := false
		writableVal := false

		for _, kv := range sec.entries {
			switch kv.key {
			case "path":
				sh.Path = kv.val
			case "comment":
				sh.Comment = kv.val
			case "browseable":
				sh.Browseable = parseBool(kv.val, true)
			case "read only":
				hasReadOnly = true
				sh.ReadOnly = parseBool(kv.val, false)
			case "writable":
				hasWritable = true
				writableVal = parseBool(kv.val, true)
			case "guest ok":
				sh.GuestOK = parseBool(kv.val, false)
			case "valid users":
				sh.ValidUsers = kv.val
			case "write list":
				sh.WriteList = kv.val
			case "create mask":
				sh.CreateMask = kv.val
			case "directory mask":
				sh.DirectoryMask = kv.val
			case "force user":
				sh.ForceUser = kv.val
			case "force group":
				sh.ForceGroup = kv.val
			case "max connections":
				sh.MaxConnections = kv.val
			default:
				sh.Extra[kv.key] = kv.val
			}
		}

		if !hasReadOnly && hasWritable {
			sh.ReadOnly = !writableVal
		}
		if sh.Path != "" {
			if info, err := os.Stat(sh.Path); err == nil && info.IsDir() {
				sh.PathExists = true
			}
		}
		shares = append(shares, sh)
	}
	return shares, nil
}

// EnsureSharePermissions verifies that the target share directory on the Linux
// filesystem belongs to group smbusers with setgid (2775), ensuring all authenticated
// Samba users have write permissions regardless of when their account was created.
func EnsureSharePermissions(dirPath string) {
	clean := filepath.Clean(strings.TrimSpace(dirPath))
	if clean == "" || clean == "/" || clean == "." {
		return
	}
	info, err := os.Stat(clean)
	if err != nil || !info.IsDir() {
		return
	}

	// 1. Ensure smbusers group exists
	_ = exec.Command("groupadd", "-f", "smbusers").Run()

	// 2. Set group of share directory to smbusers
	_ = exec.Command("chgrp", "smbusers", clean).Run()

	// 3. Set SGID (2775) on directory so all new files/folders inherit group smbusers
	_ = exec.Command("chmod", "2775", clean).Run()

	// 4. In background, ensure existing files/dirs in the share have group write access
	go func(target string) {
		_ = exec.Command("chgrp", "-R", "smbusers", target).Run()
		_ = exec.Command("chmod", "-R", "g+rwX", target).Run()
	}(clean)
}

func SaveShare(confPath string, originalName string, share Share, createDir bool) error {
	if err := ValidateShareName(share.Name); err != nil {
		return err
	}
	if err := ValidateSharePath(share.Path); err != nil {
		return err
	}

	smbMu.Lock()
	defer smbMu.Unlock()

	if createDir {
		if err := os.MkdirAll(share.Path, 0775); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", share.Path, err)
		}
	} else {
		info, err := os.Stat(share.Path)
		if err != nil || !info.IsDir() {
			return fmt.Errorf("directory %s does not exist (enable 'Create directory if missing' to create it)", share.Path)
		}
	}
	EnsureSharePermissions(share.Path)

	sections, err := parseSmbConf(confPath)
	if err != nil {
		return err
	}

	targetName := share.Name
	if originalName != "" {
		targetName = originalName
	}

	// If renaming or creating new, ensure new name doesn't conflict
	if originalName == "" || !strings.EqualFold(originalName, share.Name) {
		for _, sec := range sections {
			if strings.EqualFold(sec.name, share.Name) {
				return fmt.Errorf("a share named %q already exists", share.Name)
			}
		}
	}

	if share.CreateMask == "" {
		share.CreateMask = "0775"
	}
	if share.DirectoryMask == "" {
		share.DirectoryMask = "0775"
	}
	if share.ForceGroup == "" {
		share.ForceGroup = "smbusers"
	}

	buildEntries := func(existing []smbKeyVal) []smbKeyVal {
		managedKeys := map[string]bool{
			"path": true, "comment": true, "browseable": true, "read only": true,
			"writable": true, "guest ok": true, "valid users": true, "write list": true,
			"create mask": true, "directory mask": true, "force create mode": true,
			"force directory mode": true, "force user": true, "force group": true,
			"max connections": true,
		}
		var entries []smbKeyVal
		entries = append(entries, smbKeyVal{"path", share.Path})
		if share.Comment != "" {
			entries = append(entries, smbKeyVal{"comment", share.Comment})
		}
		entries = append(entries, smbKeyVal{"browseable", boolToYesNo(share.Browseable)})
		entries = append(entries, smbKeyVal{"read only", boolToYesNo(share.ReadOnly)})
		entries = append(entries, smbKeyVal{"guest ok", boolToYesNo(share.GuestOK)})
		if share.ValidUsers != "" {
			entries = append(entries, smbKeyVal{"valid users", share.ValidUsers})
		}
		if share.WriteList != "" {
			entries = append(entries, smbKeyVal{"write list", share.WriteList})
		}
		if share.CreateMask != "" {
			entries = append(entries, smbKeyVal{"create mask", share.CreateMask})
			entries = append(entries, smbKeyVal{"force create mode", "0664"})
		}
		if share.DirectoryMask != "" {
			entries = append(entries, smbKeyVal{"directory mask", share.DirectoryMask})
			entries = append(entries, smbKeyVal{"force directory mode", "0775"})
		}
		if share.ForceUser != "" {
			entries = append(entries, smbKeyVal{"force user", share.ForceUser})
		}
		if share.ForceGroup != "" {
			entries = append(entries, smbKeyVal{"force group", share.ForceGroup})
		}
		if share.MaxConnections != "" {
			entries = append(entries, smbKeyVal{"max connections", share.MaxConnections})
		}
		// Preserve any extra keys from existing section
		for _, kv := range existing {
			if !managedKeys[kv.key] {
				entries = append(entries, kv)
			}
		}
		return entries
	}

	found := false
	for i, sec := range sections {
		if originalName != "" && strings.EqualFold(sec.name, targetName) {
			sections[i].name = share.Name
			sections[i].entries = buildEntries(sec.entries)
			found = true
			break
		}
	}
	if !found {
		sections = append(sections, smbSection{
			name:    share.Name,
			entries: buildEntries(nil),
		})
	}

	if err := writeSmbConf(confPath, sections); err != nil {
		return err
	}
	_ = ReloadSamba()
	return nil
}

func DeleteShare(confPath string, name string) error {
	name = strings.TrimSpace(name)
	if name == "" || strings.EqualFold(name, "global") {
		return errors.New("cannot delete [global] section")
	}

	smbMu.Lock()
	defer smbMu.Unlock()

	sections, err := parseSmbConf(confPath)
	if err != nil {
		return err
	}

	var updated []smbSection
	found := false
	for _, sec := range sections {
		if strings.EqualFold(sec.name, name) {
			found = true
			continue
		}
		updated = append(updated, sec)
	}
	if !found {
		return fmt.Errorf("share %q not found", name)
	}
	if err := writeSmbConf(confPath, updated); err != nil {
		return err
	}
	_ = ReloadSamba()
	return nil
}

func GetGlobalSettings(confPath string) (GlobalSettings, error) {
	smbMu.Lock()
	defer smbMu.Unlock()

	sections, err := parseSmbConf(confPath)
	if err != nil {
		return GlobalSettings{}, err
	}

	gs := GlobalSettings{
		Workgroup:            "WORKGROUP",
		ServerString:         "Samba Server (%h)",
		Security:             "user",
		MapToGuest:           "Bad User",
		GuestAccount:         "nobody",
		LogLevel:             "1",
		UsershareAllowGuests: "yes",
		Extra:                make(map[string]string),
	}

	for _, sec := range sections {
		if strings.EqualFold(sec.name, "global") {
			for _, kv := range sec.entries {
				switch kv.key {
				case "workgroup":
					gs.Workgroup = kv.val
				case "server string":
					gs.ServerString = kv.val
				case "security":
					gs.Security = kv.val
				case "map to guest":
					gs.MapToGuest = kv.val
				case "guest account":
					gs.GuestAccount = kv.val
				case "log level":
					gs.LogLevel = kv.val
				case "hosts allow":
					gs.HostsAllow = kv.val
				case "hosts deny":
					gs.HostsDeny = kv.val
				case "usershare allow guests":
					gs.UsershareAllowGuests = kv.val
				default:
					gs.Extra[kv.key] = kv.val
				}
			}
			break
		}
	}
	return gs, nil
}

func SaveGlobalSettings(confPath string, gs GlobalSettings) error {
	smbMu.Lock()
	defer smbMu.Unlock()

	sections, err := parseSmbConf(confPath)
	if err != nil {
		return err
	}

	managedKeys := map[string]bool{
		"workgroup": true, "server string": true, "security": true,
		"map to guest": true, "guest account": true, "log level": true,
		"hosts allow": true, "hosts deny": true, "usershare allow guests": true,
	}

	buildGlobal := func(existing []smbKeyVal) []smbKeyVal {
		var entries []smbKeyVal
		if gs.Workgroup != "" {
			entries = append(entries, smbKeyVal{"workgroup", gs.Workgroup})
		}
		if gs.ServerString != "" {
			entries = append(entries, smbKeyVal{"server string", gs.ServerString})
		}
		if gs.Security != "" {
			entries = append(entries, smbKeyVal{"security", gs.Security})
		}
		if gs.MapToGuest != "" {
			entries = append(entries, smbKeyVal{"map to guest", gs.MapToGuest})
		}
		if gs.GuestAccount != "" {
			entries = append(entries, smbKeyVal{"guest account", gs.GuestAccount})
		}
		if gs.LogLevel != "" {
			entries = append(entries, smbKeyVal{"log level", gs.LogLevel})
		}
		if gs.HostsAllow != "" {
			entries = append(entries, smbKeyVal{"hosts allow", gs.HostsAllow})
		}
		if gs.HostsDeny != "" {
			entries = append(entries, smbKeyVal{"hosts deny", gs.HostsDeny})
		}
		if gs.UsershareAllowGuests != "" {
			entries = append(entries, smbKeyVal{"usershare allow guests", gs.UsershareAllowGuests})
		}
		for _, kv := range existing {
			if !managedKeys[kv.key] {
				entries = append(entries, kv)
			}
		}
		return entries
	}

	found := false
	for i, sec := range sections {
		if strings.EqualFold(sec.name, "global") {
			sections[i].entries = buildGlobal(sec.entries)
			found = true
			break
		}
	}
	if !found {
		globalSec := smbSection{
			name:    "global",
			entries: buildGlobal(nil),
		}
		sections = append([]smbSection{globalSec}, sections...)
	}

	if err := writeSmbConf(confPath, sections); err != nil {
		return err
	}
	_ = ReloadSamba()
	return nil
}

func GetServiceStatus() ServiceStatus {
	st := ServiceStatus{
		SmbdStatus: "inactive",
		NmbdStatus: "inactive",
	}

	if path, err := exec.LookPath("smbd"); err == nil && path != "" {
		st.Installed = true
		if out, err := exec.Command("smbd", "--version").Output(); err == nil {
			st.Version = strings.TrimSpace(string(out))
		}
	}

	st.SmbdStatus = checkDaemonStatus("smbd", "smb")
	st.NmbdStatus = checkDaemonStatus("nmbd", "nmb")
	st.Connections = countActiveConnections()

	return st
}

func checkDaemonStatus(primaryUnit, altUnit string) string {
	for _, unit := range []string{primaryUnit, altUnit} {
		out, err := exec.Command("systemctl", "is-active", unit).Output()
		status := strings.TrimSpace(string(out))
		if err == nil && status == "active" {
			return "active"
		}
	}
	// Fallback to pgrep if systemd unit check didn't match
	if err := exec.Command("pgrep", "-x", primaryUnit).Run(); err == nil {
		return "active"
	}
	return "inactive"
}

func countActiveConnections() int {
	out, err := exec.Command("smbstatus", "-b").Output()
	if err != nil {
		return 0
	}
	lines := strings.Split(string(out), "\n")
	count := 0
	passedHeader := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "---") {
			passedHeader = true
			continue
		}
		if passedHeader && line != "" {
			count++
		}
	}
	return count
}

func ControlSambaService(action string) error {
	switch action {
	case "start", "stop", "restart", "reload":
	default:
		return fmt.Errorf("invalid service action: %s", action)
	}

	// Try Debian/Ubuntu service names (smbd, nmbd) then RHEL/Arch (smb, nmb)
	errSmbd := exec.Command("systemctl", action, "smbd").Run()
	if errSmbd != nil {
		errSmbd = exec.Command("systemctl", action, "smb").Run()
	}
	_ = exec.Command("systemctl", action, "nmbd").Run()
	_ = exec.Command("systemctl", action, "nmb").Run()

	if errSmbd != nil {
		// Fallback to service command
		if err := exec.Command("service", "smbd", action).Run(); err != nil {
			return fmt.Errorf("failed to %s Samba service: %w", action, errSmbd)
		}
	}
	return nil
}

func ReloadSamba() error {
	if err := exec.Command("smbcontrol", "all", "reload-config").Run(); err == nil {
		return nil
	}
	return ControlSambaService("reload")
}
