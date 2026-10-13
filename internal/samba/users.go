package samba

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

var validUsername = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9._-]{0,31}$`)

type ShareUser struct {
	Username   string   `json:"username"`
	UID        int      `json:"uid"`
	Enabled    bool     `json:"enabled"`
	Flags      string   `json:"flags"`
	Shares     []string `json:"shares"`
	SystemUser bool     `json:"system_user"`
}

func ValidateUsername(username string) error {
	username = strings.TrimSpace(username)
	if username == "" {
		return errors.New("username cannot be empty")
	}
	if !validUsername.MatchString(username) {
		return errors.New("username must start with a letter or underscore and contain only letters, numbers, dots, underscores, or hyphens (max 32 chars)")
	}
	if username == "root" {
		return errors.New("modifying root as a Samba share user is not recommended")
	}
	return nil
}

func ListShareUsers(confPath string) ([]ShareUser, error) {
	systemUIDs := getSystemUserUIDs()
	shares, _ := ListShares(confPath)

	out, err := exec.Command("pdbedit", "-L", "-v").Output()
	if err != nil {
		// Fallback to non-verbose pdbedit -L if -v fails or pdbedit isn't initialized
		outSimple, errSimple := exec.Command("pdbedit", "-L").Output()
		if errSimple != nil {
			return []ShareUser{}, nil
		}
		var users []ShareUser
		scanner := bufio.NewScanner(bytes.NewReader(outSimple))
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			parts := strings.Split(line, ":")
			if len(parts) >= 2 {
				uname := strings.TrimSpace(parts[0])
				uid, _ := strconv.Atoi(strings.TrimSpace(parts[1]))
				users = append(users, ShareUser{
					Username:   uname,
					UID:        uid,
					Enabled:    true,
					Flags:      "[U]",
					Shares:     sharesForUser(uname, shares),
					SystemUser: true,
				})
			}
		}
		return users, nil
	}

	var users []ShareUser
	var current *ShareUser

	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "Unix username:") {
			if current != nil && current.Username != "" {
				users = append(users, *current)
			}
			uname := strings.TrimSpace(strings.TrimPrefix(line, "Unix username:"))
			uid, exists := systemUIDs[uname]
			current = &ShareUser{
				Username:   uname,
				UID:        uid,
				Enabled:    true,
				Shares:     sharesForUser(uname, shares),
				SystemUser: exists,
			}
		} else if current != nil && strings.HasPrefix(line, "Account Flags:") {
			flags := strings.TrimSpace(strings.TrimPrefix(line, "Account Flags:"))
			current.Flags = flags
			current.Enabled = !strings.Contains(flags, "D")
		}
	}
	if current != nil && current.Username != "" {
		users = append(users, *current)
	}

	return users, nil
}

func sharesForUser(username string, shares []Share) []string {
	var matched []string
	for _, sh := range shares {
		if sh.GuestOK && sh.ValidUsers == "" {
			matched = append(matched, sh.Name+" (guest/all)")
			continue
		}
		if sh.ValidUsers == "" && sh.WriteList == "" {
			matched = append(matched, sh.Name)
			continue
		}
		if listContainsUser(sh.ValidUsers, username) || listContainsUser(sh.WriteList, username) {
			matched = append(matched, sh.Name)
		}
	}
	return matched
}

func listContainsUser(listStr, username string) bool {
	tokens := strings.FieldsFunc(listStr, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t'
	})
	for _, t := range tokens {
		if strings.EqualFold(strings.TrimSpace(t), username) || strings.EqualFold(strings.TrimSpace(t), "@smbusers") {
			return true
		}
	}
	return false
}

func getSystemUserUIDs() map[string]int {
	res := make(map[string]int)
	data, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return res
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		parts := strings.Split(scanner.Text(), ":")
		if len(parts) >= 3 {
			uid, _ := strconv.Atoi(parts[2])
			res[parts[0]] = uid
		}
	}
	return res
}

func ListSystemUsers() []string {
	uids := getSystemUserUIDs()
	var list []string
	for uname, uid := range uids {
		if (uid >= 1000 && uid < 65534) || uname != "nobody" {
			if uid >= 1000 && uid < 65534 {
				list = append(list, uname)
			}
		}
	}
	return list
}

func AddOrUpdateShareUser(username, password string) error {
	if err := ValidateUsername(username); err != nil {
		return err
	}
	if len(password) < 1 {
		return errors.New("password cannot be empty")
	}
	if strings.ContainsAny(password, "\n\r") {
		return errors.New("password cannot contain newline characters")
	}

	// Ensure group smbusers exists
	_ = exec.Command("groupadd", "-f", "smbusers").Run()

	// Ensure Linux user exists; if not, create system account with no login shell
	uids := getSystemUserUIDs()
	if _, exists := uids[username]; !exists {
		nologin := "/usr/sbin/nologin"
		if _, err := os.Stat(nologin); err != nil {
			nologin = "/sbin/nologin"
		}
		if out, err := exec.Command("useradd", "-M", "-s", nologin, "-G", "smbusers", username).CombinedOutput(); err != nil {
			return fmt.Errorf("failed to create Linux user %s: %s (%w)", username, strings.TrimSpace(string(out)), err)
		}
	} else {
		_ = exec.Command("usermod", "-aG", "smbusers", username).Run()
	}

	// Set Samba password via smbpasswd -a -s
	cmd := exec.Command("smbpasswd", "-a", "-s", username)
	cmd.Stdin = strings.NewReader(password + "\n" + password + "\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("smbpasswd failed: %s (%w)", strings.TrimSpace(string(out)), err)
	}

	// Ensure user is enabled
	_ = exec.Command("smbpasswd", "-e", username).Run()
	return nil
}

func SetShareUserEnabled(username string, enabled bool) error {
	if err := ValidateUsername(username); err != nil {
		return err
	}
	flag := "-d"
	if enabled {
		flag = "-e"
	}
	if out, err := exec.Command("smbpasswd", flag, username).CombinedOutput(); err != nil {
		return fmt.Errorf("smbpasswd %s failed: %s (%w)", flag, strings.TrimSpace(string(out)), err)
	}
	return nil
}

func DeleteShareUser(username string, removeLinuxUser bool) error {
	if err := ValidateUsername(username); err != nil {
		return err
	}
	if out, err := exec.Command("smbpasswd", "-x", username).CombinedOutput(); err != nil {
		return fmt.Errorf("failed to delete Samba user %s: %s (%w)", username, strings.TrimSpace(string(out)), err)
	}
	if removeLinuxUser {
		uids := getSystemUserUIDs()
		if uid, ok := uids[username]; ok && uid >= 1000 && uid < 65534 {
			_ = exec.Command("userdel", username).Run()
		}
	}
	return nil
}
