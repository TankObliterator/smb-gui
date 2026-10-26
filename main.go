package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"smb-gui/internal/analytics"
	"smb-gui/internal/config"
	"smb-gui/internal/mounts"
	"smb-gui/internal/samba"
	"smb-gui/internal/updater"
)

//go:embed web/*
var embeddedWeb embed.FS

//go:embed main.go go.mod install.sh uninstall.sh internal web
var embeddedCodeFS embed.FS

const sessionCookieName = "smb_gui_session"

type Server struct {
	cfgMgr      *config.Manager
	smbConfPath string
	fstabPath   string
	credDir     string
}

func main() {
	portFlag := flag.Int("port", 0, "Port override to listen on (0 = use config.json, default 5000)")
	hostFlag := flag.String("host", "0.0.0.0", "Host interface to bind to")
	cfgPath := flag.String("config", "/etc/smb-gui/config.json", "Path to smb-gui config file")
	smbConfPath := flag.String("smb-conf", "/etc/samba/smb.conf", "Path to Samba smb.conf")
	fstabPath := flag.String("fstab", "/etc/fstab", "Path to /etc/fstab")
	credDir := flag.String("cred-dir", "/etc/samba/credentials", "Path to CIFS credentials directory")
	flag.Parse()

	cfgMgr, err := config.NewManager(*cfgPath)
	if err != nil {
		log.Printf("Warning loading config (%s): %v", *cfgPath, err)
	}

	_ = samba.EnsureDefaultSmbConf(*smbConfPath)
	_ = os.MkdirAll(*credDir, 0700)

	srv := &Server{
		cfgMgr:      cfgMgr,
		smbConfPath: *smbConfPath,
		fstabPath:   *fstabPath,
		credDir:     *credDir,
	}

	mux := http.NewServeMux()

	// Auth endpoints (unprotected so user can check status & log in)
	mux.HandleFunc("/api/auth/status", srv.handleAuthStatus)
	mux.HandleFunc("/api/auth/login", srv.handleAuthLogin)
	mux.HandleFunc("/api/auth/logout", srv.handleAuthLogout)

	// Protected API endpoints (only require auth when password is set in Settings)
	mux.HandleFunc("/api/overview", srv.requireAuth(srv.handleOverview))
	mux.HandleFunc("/api/service", srv.requireAuth(srv.handleService))
	mux.HandleFunc("/api/shares", srv.requireAuth(srv.handleShares))
	mux.HandleFunc("/api/mounts", srv.requireAuth(srv.handleMounts))
	mux.HandleFunc("/api/mounts/action", srv.requireAuth(srv.handleMountAction))
	mux.HandleFunc("/api/share-users", srv.requireAuth(srv.handleShareUsers))
	mux.HandleFunc("/api/share-users/status", srv.requireAuth(srv.handleShareUserStatus))
	mux.HandleFunc("/api/mount-users", srv.requireAuth(srv.handleMountUsers))
	mux.HandleFunc("/api/analytics", srv.requireAuth(srv.handleAnalytics))
	mux.HandleFunc("/api/settings/password", srv.requireAuth(srv.handleSettingsPassword))
	mux.HandleFunc("/api/settings/port", srv.requireAuth(srv.handleSettingsPort))
	mux.HandleFunc("/api/settings/global", srv.requireAuth(srv.handleSettingsGlobal))
	mux.HandleFunc("/api/settings/export-smbconf", srv.requireAuth(srv.handleExportSmbConf))
	mux.HandleFunc("/api/settings/update/check", srv.requireAuth(srv.handleUpdateCheck))
	mux.HandleFunc("/api/settings/update/apply", srv.requireAuth(srv.handleUpdateApply))

	// Static Web UI with SPA slug routing (/dashboard, /shares, /share-users, /mounts, /mount-users, /analytics, /settings)
	webFS, err := fs.Sub(embeddedWeb, "web")
	if err != nil {
		log.Fatalf("Failed to mount embedded web assets: %v", err)
	}
	fileServer := http.FileServer(http.FS(webFS))
	indexBytes, err := fs.ReadFile(webFS, "index.html")
	if err != nil {
		log.Fatalf("Failed to read embedded index.html: %v", err)
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
		cleanPath := strings.TrimPrefix(r.URL.Path, "/")
		if cleanPath != "" {
			if f, err := webFS.Open(cleanPath); err == nil {
				_ = f.Close()
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(indexBytes)
	})

	listenPort := cfgMgr.GetPort()
	if *portFlag > 0 && listenPort == 5000 {
		listenPort = *portFlag
	}
	addr := fmt.Sprintf("%s:%d", *hostFlag, listenPort)
	log.Printf("Samba Manager (smb-gui) listening on http://%s", addr)

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	if err := httpServer.ListenAndServe(); err != nil {
		log.Fatalf("Server exited: %v", err)
	}
}

func (s *Server) isAuthenticated(r *http.Request) bool {
	if !s.cfgMgr.IsAuthEnabled() {
		return true
	}
	c, err := r.Cookie(sessionCookieName)
	if err != nil || c.Value == "" {
		return false
	}
	return s.cfgMgr.VerifySessionToken(c.Value)
}

func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.isAuthenticated(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Authentication required"})
			return
		}
		next(w, r)
	}
}

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"auth_enabled":  s.cfgMgr.IsAuthEnabled(),
		"authenticated": s.isAuthenticated(r),
	})
}

func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request body"})
		return
	}
	if !s.cfgMgr.VerifyPassword(body.Password) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Invalid password"})
		return
	}
	token := s.cfgMgr.CreateSessionToken()
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   86400 * 30,
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "logged_out"})
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	status := samba.GetServiceStatus()
	shares, _ := samba.ListShares(s.smbConfPath)
	if shares == nil {
		shares = []samba.Share{}
	}
	mnts, _ := mounts.ListMounts(s.fstabPath, s.credDir)
	if mnts == nil {
		mnts = []mounts.SambaMount{}
	}
	shareUsers, _ := samba.ListShareUsers(s.smbConfPath)
	if shareUsers == nil {
		shareUsers = []samba.ShareUser{}
	}
	mountUsers, _ := mounts.ListMountUsers(s.credDir, s.fstabPath)
	if mountUsers == nil {
		mountUsers = []mounts.MountUser{}
	}
	diskReport := analytics.GetAnalytics(s.smbConfPath, s.fstabPath, s.credDir)
	globalSettings, _ := samba.GetGlobalSettings(s.smbConfPath)

	writeJSON(w, http.StatusOK, map[string]any{
		"status":      status,
		"shares":      shares,
		"mounts":      mnts,
		"share_users": shareUsers,
		"mount_users": mountUsers,
		"analytics":   diskReport,
		"settings": map[string]any{
			"auth_enabled": s.cfgMgr.IsAuthEnabled(),
			"port":         s.cfgMgr.GetPort(),
			"global":       globalSettings,
			"version":      updater.GetCurrentVersion(embeddedCodeFS),
		},
	})
}

func (s *Server) handleService(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
		return
	}
	var body struct {
		Action string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON"})
		return
	}
	if err := samba.ControlSambaService(body.Action); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleShares(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		shares, err := samba.ListShares(s.smbConfPath)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, shares)

	case http.MethodPost:
		var body struct {
			OriginalName string      `json:"original_name"`
			CreateDir    bool        `json:"create_dir"`
			Share        samba.Share `json:"share"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request payload"})
			return
		}
		if err := samba.SaveShare(s.smbConfPath, body.OriginalName, body.Share, body.CreateDir); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})

	case http.MethodDelete:
		name := r.URL.Query().Get("name")
		if err := samba.DeleteShare(s.smbConfPath, name); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})

	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
	}
}

func (s *Server) handleMounts(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		mnts, err := mounts.ListMounts(s.fstabPath, s.credDir)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, mnts)

	case http.MethodPost:
		var body struct {
			OriginalMountPoint string            `json:"original_mount_point"`
			MountNow           bool              `json:"mount_now"`
			Mount              mounts.SambaMount `json:"mount"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request payload"})
			return
		}
		msg, err := mounts.SaveMount(s.fstabPath, s.credDir, body.OriginalMountPoint, body.Mount, body.MountNow)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "saved", "message": msg})

	case http.MethodDelete:
		mp := r.URL.Query().Get("mount_point")
		if err := mounts.DeleteMount(s.fstabPath, mp); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})

	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
	}
}

func (s *Server) handleMountAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
		return
	}
	var body struct {
		MountPoint string `json:"mount_point"`
		Action     string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request payload"})
		return
	}
	if err := mounts.ToggleMount(body.MountPoint, body.Action); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleShareUsers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		users, err := samba.ListShareUsers(s.smbConfPath)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, users)

	case http.MethodPost:
		var body struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request payload"})
			return
		}
		if err := samba.AddOrUpdateShareUser(s.smbConfPath, body.Username, body.Password); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})

	case http.MethodDelete:
		username := r.URL.Query().Get("username")
		if err := samba.DeleteShareUser(username, false); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})

	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
	}
}

func (s *Server) handleShareUserStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
		return
	}
	var body struct {
		Username string `json:"username"`
		Enabled  bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request payload"})
		return
	}
	if err := samba.SetShareUserEnabled(body.Username, body.Enabled); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (s *Server) handleMountUsers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		users, err := mounts.ListMountUsers(s.credDir, s.fstabPath)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, users)

	case http.MethodPost:
		var body struct {
			Name     string `json:"name"`
			Username string `json:"username"`
			Password string `json:"password"`
			Domain   string `json:"domain"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request payload"})
			return
		}
		savedName, err := mounts.SaveMountUser(s.credDir, body.Name, body.Username, body.Password, body.Domain)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "saved", "name": savedName})

	case http.MethodDelete:
		name := r.URL.Query().Get("name")
		if err := mounts.DeleteMountUser(s.credDir, s.fstabPath, name); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})

	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
	}
}

func (s *Server) handleAnalytics(w http.ResponseWriter, r *http.Request) {
	report := analytics.GetAnalytics(s.smbConfPath, s.fstabPath, s.credDir)
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) handleSettingsPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request payload"})
		return
	}
	if err := s.cfgMgr.SetPassword(body.Password); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if body.Password != "" {
		token := s.cfgMgr.CreateSessionToken()
		http.SetCookie(w, &http.Cookie{
			Name:     sessionCookieName,
			Value:    token,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   86400 * 30,
		})
	} else {
		http.SetCookie(w, &http.Cookie{
			Name:     sessionCookieName,
			Value:    "",
			Path:     "/",
			HttpOnly: true,
			MaxAge:   -1,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":       "ok",
		"auth_enabled": s.cfgMgr.IsAuthEnabled(),
	})
}

func (s *Server) handleSettingsPort(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
		return
	}
	var body struct {
		Port int `json:"port"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request payload"})
		return
	}
	if body.Port < 1 || body.Port > 65535 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Port must be between 1 and 65535"})
		return
	}
	if body.Port == 139 || body.Port == 445 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Ports 139 and 445 are reserved for Samba (smbd)"})
		return
	}

	currentPort := s.cfgMgr.GetPort()
	if body.Port != currentPort {
		// Verify new port is available before saving
		ln, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", body.Port))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("Port %d is already in use or cannot be bound: %v", body.Port, err)})
			return
		}
		_ = ln.Close()
	}

	if err := s.cfgMgr.SetPort(body.Port); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// Ensure systemd unit file does not hardcode an old -port flag
	const unitPath = "/etc/systemd/system/smb-gui.service"
	if unitBytes, err := os.ReadFile(unitPath); err == nil {
		updatedUnit := strings.ReplaceAll(string(unitBytes), fmt.Sprintf("-port %d", currentPort), "")
		if lines := strings.Split(updatedUnit, "\n"); len(lines) > 0 {
			for i, l := range lines {
				if strings.HasPrefix(strings.TrimSpace(l), "ExecStart=") {
					lines[i] = "ExecStart=/usr/local/bin/smb-gui"
				}
			}
			_ = os.WriteFile(unitPath, []byte(strings.Join(lines, "\n")), 0644)
			_ = exec.Command("systemctl", "daemon-reload").Run()
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"port":    body.Port,
		"changed": body.Port != currentPort,
	})

	if body.Port != currentPort {
		go func() {
			time.Sleep(400 * time.Millisecond)
			_ = exec.Command("systemctl", "restart", "smb-gui.service").Run()
		}()
	}
}

func (s *Server) handleSettingsGlobal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
		return
	}
	var gs samba.GlobalSettings
	if err := json.NewDecoder(r.Body).Decode(&gs); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request payload"})
		return
	}
	if err := samba.SaveGlobalSettings(s.smbConfPath, gs); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

func (s *Server) handleExportSmbConf(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile(s.smbConfPath)
	if err != nil {
		http.Error(w, "Could not read smb.conf", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="smb.conf"`)
	_, _ = w.Write(data)
}

func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
		return
	}
	result, err := updater.CheckForUpdate(embeddedCodeFS)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleUpdateApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed"})
		return
	}
	exePath, err := os.Executable()
	if err != nil || exePath == "" {
		exePath = "/usr/local/bin/smb-gui"
	}
	newVer, err := updater.ApplyUpdate(exePath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "updated",
		"version": newVer,
		"message": fmt.Sprintf("Updated to %s. Restarting smb-gui service...", newVer),
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
