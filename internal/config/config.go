package config

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

type AppConfig struct {
	Port          int    `json:"port"`
	PasswordHash  string `json:"password_hash"`
	PasswordSalt  string `json:"password_salt"`
	SessionSecret string `json:"session_secret"`
}

type Manager struct {
	mu   sync.RWMutex
	path string
	cfg  AppConfig
}

func NewManager(path string) (*Manager, error) {
	m := &Manager{
		path: path,
		cfg: AppConfig{
			Port:          5000,
			PasswordHash:  "", // Empty by default = no authentication required
			PasswordSalt:  "",
			SessionSecret: generateRandomHex(32),
		},
	}
	if err := m.Load(); err != nil {
		if os.IsNotExist(err) {
			_ = m.Save()
			return m, nil
		}
		return m, nil
	}
	if m.cfg.SessionSecret == "" {
		m.cfg.SessionSecret = generateRandomHex(32)
		_ = m.Save()
	}
	if m.cfg.Port == 0 {
		m.cfg.Port = 5000
	}
	return m, nil
}

func (m *Manager) Load() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	data, err := os.ReadFile(m.path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, &m.cfg)
}

func (m *Manager) Save() error {
	m.mu.RLock()
	data, err := json.MarshalIndent(m.cfg, "", "  ")
	m.mu.RUnlock()
	if err != nil {
		return err
	}
	dir := filepath.Dir(m.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return os.WriteFile(m.path, data, 0600)
}

func (m *Manager) IsAuthEnabled() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg.PasswordHash != ""
}

func (m *Manager) GetPort() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg.Port
}

func (m *Manager) SetPassword(newPassword string) error {
	m.mu.Lock()
	if newPassword == "" {
		m.cfg.PasswordHash = ""
		m.cfg.PasswordSalt = ""
	} else {
		salt := generateRandomHex(16)
		m.cfg.PasswordSalt = salt
		m.cfg.PasswordHash = hashPassword(newPassword, salt)
	}
	// Rotate session secret when password changes
	m.cfg.SessionSecret = generateRandomHex(32)
	m.mu.Unlock()
	return m.Save()
}

func (m *Manager) VerifyPassword(password string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.cfg.PasswordHash == "" {
		return true
	}
	expected := hashPassword(password, m.cfg.PasswordSalt)
	return subtle.ConstantTimeCompare([]byte(m.cfg.PasswordHash), []byte(expected)) == 1
}

func (m *Manager) CreateSessionToken() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	mac := hmac.New(sha256.New, []byte(m.cfg.SessionSecret))
	mac.Write([]byte("smb-gui-authenticated-session:" + m.cfg.PasswordHash))
	return hex.EncodeToString(mac.Sum(nil))
}

func (m *Manager) VerifySessionToken(token string) bool {
	m.mu.RLock()
	if m.cfg.PasswordHash == "" {
		m.mu.RUnlock()
		return true
	}
	m.mu.RUnlock()
	expected := m.CreateSessionToken()
	return subtle.ConstantTimeCompare([]byte(token), []byte(expected)) == 1
}

func hashPassword(password, salt string) string {
	h := sha256.New()
	h.Write([]byte(salt + ":" + password))
	return hex.EncodeToString(h.Sum(nil))
}

func generateRandomHex(numBytes int) string {
	b := make([]byte, numBytes)
	if _, err := rand.Read(b); err != nil {
		return "fallback-secret-key-smb-gui"
	}
	return hex.EncodeToString(b)
}
