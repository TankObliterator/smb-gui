package updater

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const RepoSlug = "TankObliterator/smb-gui"
const versionFilePath = "/etc/smb-gui/version"

type CheckResult struct {
	CurrentVersion  string   `json:"current_version"`
	LatestVersion   string   `json:"latest_version"`
	UpdateAvailable bool     `json:"update_available"`
	ChangedFiles    []string `json:"changed_files,omitempty"`
	Source          string   `json:"source"`
	ReleaseURL      string   `json:"release_url"`
	Message         string   `json:"message"`
}

type localFileHashes struct {
	gitSHA1Raw   string
	gitSHA1LF    string
	sha256B64Raw string
	sha256B64LF  string
}

// GetCurrentVersion returns the installed commit-date version tag (e.g. "2026.10.13" or "2026.10.13-v2").
func GetCurrentVersion(codeFS fs.FS) string {
	if b, err := os.ReadFile(versionFilePath); err == nil {
		if v := strings.TrimSpace(string(b)); v != "" {
			return v
		}
	}
	return time.Now().Format("2006.01.02")
}

// isRelevantCodeFile returns true only for actual code/runtime files and ignores
// documentation or repository metadata like README.md, LICENSE, .gitignore, etc.
func isRelevantCodeFile(relPath string) bool {
	p := strings.TrimPrefix(filepath.ToSlash(relPath), "/")
	if p == "" {
		return false
	}
	lower := strings.ToLower(p)
	base := filepath.Base(lower)

	// Ignore markdown, docs, license, git/github metadata, editor files
	if strings.HasSuffix(lower, ".md") ||
		strings.HasPrefix(base, "license") ||
		strings.HasPrefix(base, ".") ||
		strings.HasPrefix(lower, ".github/") ||
		strings.HasPrefix(lower, "docs/") ||
		lower == "version" {
		return false
	}

	// Relevant code & asset paths
	if strings.HasSuffix(lower, ".go") ||
		lower == "go.mod" ||
		lower == "go.sum" ||
		strings.HasPrefix(lower, "web/") ||
		strings.HasPrefix(lower, "internal/") ||
		lower == "install.sh" ||
		lower == "uninstall.sh" {
		return true
	}

	return false
}

func computeLocalManifest(codeFS fs.FS) map[string]localFileHashes {
	manifest := make(map[string]localFileHashes)
	if codeFS == nil {
		return manifest
	}

	_ = fs.WalkDir(codeFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		clean := strings.TrimPrefix(filepath.ToSlash(path), "./")
		if !isRelevantCodeFile(clean) {
			return nil
		}
		raw, err := fs.ReadFile(codeFS, path)
		if err != nil {
			return nil
		}
		lf := bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))

		manifest[clean] = localFileHashes{
			gitSHA1Raw:   gitBlobSHA1(raw),
			gitSHA1LF:    gitBlobSHA1(lf),
			sha256B64Raw: sha256Base64(raw),
			sha256B64LF:  sha256Base64(lf),
		}
		return nil
	})
	return manifest
}

func gitBlobSHA1(data []byte) string {
	header := fmt.Sprintf("blob %d\x00", len(data))
	h := sha1.New()
	h.Write([]byte(header))
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

func sha256Base64(data []byte) string {
	sum := sha256.Sum256(data)
	return base64.StdEncoding.EncodeToString(sum[:])
}

// CheckForUpdate checks TankObliterator/smb-gui for new commits affecting code files,
// ignoring commits that only touch README.md or other non-code files.
// Automatically falls back to jsDelivr if GitHub rate-limits the request.
func CheckForUpdate(codeFS fs.FS) (CheckResult, error) {
	localManifest := computeLocalManifest(codeFS)
	currentVer := GetCurrentVersion(codeFS)

	res := CheckResult{
		CurrentVersion: currentVer,
		ReleaseURL:     "https://github.com/" + RepoSlug,
	}

	client := &http.Client{Timeout: 8 * time.Second}

	// 1. Try GitHub Commits + Git Tree API
	latestDateVer, changed, rateLimited, ghErr := checkGitHubTree(client, localManifest)
	if ghErr == nil {
		res.LatestVersion = latestDateVer
		res.Source = "GitHub"
		res.ChangedFiles = changed
		res.UpdateAvailable = len(changed) > 0
		if res.UpdateAvailable {
			res.Message = fmt.Sprintf("Update %s available (%d code file(s) changed).", latestDateVer, len(changed))
		} else {
			res.Message = fmt.Sprintf("Up to date (%s).", currentVer)
		}
		return res, nil
	}

	// 2. Fallback to jsDelivr Flat Package API (unaffected by GitHub API rate limits)
	jsdChanged, _, jsdErr := checkJsDelivrTree(client, localManifest)
	if jsdErr == nil {
		nextVer := nextVersionForToday(currentVer)
		res.Source = "jsDelivr"
		res.ChangedFiles = jsdChanged
		res.UpdateAvailable = len(jsdChanged) > 0
		if res.UpdateAvailable {
			res.LatestVersion = nextVer
			res.Message = fmt.Sprintf("Update %s available via jsDelivr (%d code file(s) changed).", nextVer, len(jsdChanged))
		} else {
			res.LatestVersion = currentVer
			res.Message = fmt.Sprintf("Up to date (%s, verified via jsDelivr).", currentVer)
		}
		return res, nil
	}

	if rateLimited {
		return res, fmt.Errorf("update check failed: GitHub API rate limit exceeded and jsDelivr fallback failed (%v)", jsdErr)
	}
	return res, fmt.Errorf("update check failed: repository %q was not found on GitHub or jsDelivr (404 Not Found)", RepoSlug)
}

func checkGitHubTree(client *http.Client, localManifest map[string]localFileHashes) (dateVersion string, changed []string, rateLimited bool, err error) {
	for _, branch := range []string{"main", "master"} {
		// Fetch recent commits on branch to determine YYYY.MM.DD and -v# count for that day
		commitsURL := fmt.Sprintf("https://api.github.com/repos/%s/commits?sha=%s&per_page=30", RepoSlug, branch)
		req, err := http.NewRequest(http.MethodGet, commitsURL, nil)
		if err != nil {
			continue
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("User-Agent", "smb-gui-updater")

		resp, err := client.Do(req)
		if err != nil {
			return "", nil, false, err
		}
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
			resp.Body.Close()
			return "", nil, true, fmt.Errorf("github rate limited (%d)", resp.StatusCode)
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			continue
		}

		var commits []struct {
			SHA    string `json:"sha"`
			Commit struct {
				Committer struct {
					Date time.Time `json:"date"`
				} `json:"committer"`
				Tree struct {
					SHA string `json:"sha"`
				} `json:"tree"`
			} `json:"commit"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&commits); err != nil || len(commits) == 0 {
			resp.Body.Close()
			continue
		}
		resp.Body.Close()

		latestDate := commits[0].Commit.Committer.Date.Local().Format("2006.01.02")
		if latestDate == "0001.01.01" {
			latestDate = time.Now().Format("2006.01.02")
		}
		sameDayCount := 0
		for _, c := range commits {
			d := c.Commit.Committer.Date.Local().Format("2006.01.02")
			if d == latestDate {
				sameDayCount++
			} else {
				break
			}
		}
		computedVer := latestDate
		if sameDayCount > 1 {
			computedVer = fmt.Sprintf("%s-%d", latestDate, sameDayCount)
		}

		treeSHA := commits[0].Commit.Tree.SHA
		if treeSHA == "" {
			treeSHA = branch
		}

		// Fetch recursive tree to inspect all relevant code files
		treeURL := fmt.Sprintf("https://api.github.com/repos/%s/git/trees/%s?recursive=1", RepoSlug, treeSHA)
		tReq, err := http.NewRequest(http.MethodGet, treeURL, nil)
		if err != nil {
			continue
		}
		tReq.Header.Set("Accept", "application/vnd.github+json")
		tReq.Header.Set("User-Agent", "smb-gui-updater")

		tResp, err := client.Do(tReq)
		if err != nil {
			return "", nil, false, err
		}
		if tResp.StatusCode == http.StatusForbidden || tResp.StatusCode == http.StatusTooManyRequests {
			tResp.Body.Close()
			return "", nil, true, fmt.Errorf("github rate limited (%d)", tResp.StatusCode)
		}
		if tResp.StatusCode != http.StatusOK {
			tResp.Body.Close()
			continue
		}

		var treeData struct {
			Tree []struct {
				Path string `json:"path"`
				Type string `json:"type"`
				SHA  string `json:"sha"`
			} `json:"tree"`
		}
		if err := json.NewDecoder(tResp.Body).Decode(&treeData); err != nil {
			tResp.Body.Close()
			continue
		}
		tResp.Body.Close()

		var diffFiles []string
		remoteSeen := make(map[string]bool)
		for _, item := range treeData.Tree {
			if item.Type != "blob" || !isRelevantCodeFile(item.Path) {
				continue
			}
			remoteSeen[item.Path] = true
			local, exists := localManifest[item.Path]
			if !exists || (local.gitSHA1Raw != item.SHA && local.gitSHA1LF != item.SHA) {
				diffFiles = append(diffFiles, item.Path)
			}
		}
		for localPath := range localManifest {
			if !remoteSeen[localPath] {
				diffFiles = append(diffFiles, localPath)
			}
		}

		return computedVer, diffFiles, false, nil
	}
	return "", nil, false, fmt.Errorf("repository branch not found on GitHub")
}

func checkJsDelivrTree(client *http.Client, localManifest map[string]localFileHashes) (changed []string, branchUsed string, err error) {
	for _, branch := range []string{"main", "master"} {
		flatURL := fmt.Sprintf("https://data.jsdelivr.com/v1/package/gh/%s@%s/flat", RepoSlug, branch)
		req, err := http.NewRequest(http.MethodGet, flatURL, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", "smb-gui-updater")

		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			continue
		}

		var flatData struct {
			Files []struct {
				Name string `json:"name"`
				Hash string `json:"hash"`
			} `json:"files"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&flatData); err != nil {
			resp.Body.Close()
			continue
		}
		resp.Body.Close()

		if len(flatData.Files) == 0 {
			continue
		}

		var diffFiles []string
		remoteSeen := make(map[string]bool)
		for _, f := range flatData.Files {
			clean := strings.TrimPrefix(f.Name, "/")
			if !isRelevantCodeFile(clean) {
				continue
			}
			remoteSeen[clean] = true
			local, exists := localManifest[clean]
			if !exists || (local.sha256B64Raw != f.Hash && local.sha256B64LF != f.Hash) {
				diffFiles = append(diffFiles, clean)
			}
		}
		for localPath := range localManifest {
			if !remoteSeen[localPath] {
				diffFiles = append(diffFiles, localPath)
			}
		}

		return diffFiles, branch, nil
	}
	return nil, "", fmt.Errorf("not indexed on jsDelivr")
}

// ApplyUpdate clones the latest commits from TankObliterator/smb-gui, compiles the new binary,
// records the commit-date version (YYYY.MM.DD or YYYY.MM.DD-v#), and restarts smb-gui.service.
func ApplyUpdate(binPath string) (string, error) {
	tmpDir, err := os.MkdirTemp("", "smb-gui-update-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmpDir)

	repoURL := fmt.Sprintf("https://github.com/%s.git", RepoSlug)
	if out, err := exec.Command("git", "clone", "--depth", "50", repoURL, tmpDir).CombinedOutput(); err != nil {
		return "", fmt.Errorf("failed to clone %s: %s", repoURL, strings.TrimSpace(string(out)))
	}

	newVer := computeGitRepoDateVersion(tmpDir, GetCurrentVersion(nil))

	tmpBin := binPath + ".new"
	cmd := exec.Command("go", "build", "-ldflags=-s -w", "-o", tmpBin, ".")
	cmd.Dir = tmpDir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build failed: %s", strings.TrimSpace(string(out)))
	}

	if err := os.Rename(tmpBin, binPath); err != nil {
		return "", err
	}

	_ = os.MkdirAll("/etc/smb-gui", 0750)
	_ = os.WriteFile(versionFilePath, []byte(newVer+"\n"), 0644)

	go func() {
		time.Sleep(600 * time.Millisecond)
		_ = exec.Command("systemctl", "restart", "smb-gui.service").Run()
	}()
	return newVer, nil
}

func computeGitRepoDateVersion(repoDir, existingVer string) string {
	out, err := exec.Command("git", "-C", repoDir, "log", "-n", "50", "--date=format:%Y.%m.%d", "--format=%cd").Output()
	if err == nil {
		lines := strings.Fields(string(out))
		if len(lines) > 0 {
			latestDate := lines[0]
			count := 0
			for _, l := range lines {
				if l == latestDate {
					count++
				} else {
					break
				}
			}
			ver := latestDate
			if count > 1 {
				ver = fmt.Sprintf("%s-%d", latestDate, count)
			}
			// If existing version already has the same date with an equal or higher -#, increment it
			if strings.HasPrefix(existingVer, latestDate) && ver == existingVer {
				return incrementDateVersion(existingVer)
			}
			return ver
		}
	}
	return nextVersionForToday(existingVer)
}

func nextVersionForToday(existingVer string) string {
	today := time.Now().Format("2006.01.02")
	if strings.HasPrefix(existingVer, today) {
		return incrementDateVersion(existingVer)
	}
	return today
}

func incrementDateVersion(ver string) string {
	parts := strings.SplitN(ver, "-", 2)
	baseDate := parts[0]
	if len(parts) == 2 {
		suffix := strings.TrimPrefix(parts[1], "v")
		if n, err := strconv.Atoi(suffix); err == nil && n >= 1 {
			return fmt.Sprintf("%s-%d", baseDate, n+1)
		}
	}
	return fmt.Sprintf("%s-2", baseDate)
}
