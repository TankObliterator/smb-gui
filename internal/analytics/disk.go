package analytics

import (
	"fmt"
	"syscall"

	"smb-gui/internal/mounts"
	"smb-gui/internal/samba"
)

type ShareStat struct {
	Name           string  `json:"name"`
	Path           string  `json:"path"`
	Comment        string  `json:"comment"`
	ReadOnly       bool    `json:"read_only"`
	GuestOK        bool    `json:"guest_ok"`
	ValidUsers     string  `json:"valid_users"`
	TotalBytes     uint64  `json:"total_bytes"`
	UsedBytes      uint64  `json:"used_bytes"`
	AvailableBytes uint64  `json:"available_bytes"`
	UsePercent     float64 `json:"use_percent"`
	TotalHuman     string  `json:"total_human"`
	UsedHuman      string  `json:"used_human"`
	AvailableHuman string  `json:"available_human"`
	Online         bool    `json:"online"`
}

type MountStat struct {
	RemotePath     string  `json:"remote_path"`
	MountPoint     string  `json:"mount_point"`
	MountUser      string  `json:"mount_user"`
	Guest          bool    `json:"guest"`
	ReadOnly       bool    `json:"read_only"`
	SmbVersion     string  `json:"smb_version"`
	TotalBytes     uint64  `json:"total_bytes"`
	UsedBytes      uint64  `json:"used_bytes"`
	AvailableBytes uint64  `json:"available_bytes"`
	UsePercent     float64 `json:"use_percent"`
	TotalHuman     string  `json:"total_human"`
	UsedHuman      string  `json:"used_human"`
	AvailableHuman string  `json:"available_human"`
	Online         bool    `json:"online"`
}

type AnalyticsReport struct {
	Shares []ShareStat `json:"shares"`
	Mounts []MountStat `json:"mounts"`
}

func GetAnalytics(smbConfPath, fstabPath, credDir string) AnalyticsReport {
	report := AnalyticsReport{
		Shares: []ShareStat{},
		Mounts: []MountStat{},
	}

	if shares, err := samba.ListShares(smbConfPath); err == nil {
		for _, sh := range shares {
			total, used, avail, pct, ok := statFilesystem(sh.Path)
			st := ShareStat{
				Name:           sh.Name,
				Path:           sh.Path,
				Comment:        sh.Comment,
				ReadOnly:       sh.ReadOnly,
				GuestOK:        sh.GuestOK,
				ValidUsers:     sh.ValidUsers,
				TotalBytes:     total,
				UsedBytes:      used,
				AvailableBytes: avail,
				UsePercent:     pct,
				TotalHuman:     formatBytes(total),
				UsedHuman:      formatBytes(used),
				AvailableHuman: formatBytes(avail),
				Online:         ok,
			}
			if !ok {
				st.TotalHuman = "-"
				st.UsedHuman = "-"
				st.AvailableHuman = "-"
			}
			report.Shares = append(report.Shares, st)
		}
	}

	if mnts, err := mounts.ListMounts(fstabPath, credDir); err == nil {
		for _, m := range mnts {
			ms := MountStat{
				RemotePath:     m.RemotePath,
				MountPoint:     m.MountPoint,
				MountUser:      m.MountUser,
				Guest:          m.Guest,
				ReadOnly:       m.ReadOnly,
				SmbVersion:     m.SmbVersion,
				TotalHuman:     "-",
				UsedHuman:      "-",
				AvailableHuman: "-",
				Online:         false,
			}
			if m.Mounted {
				total, used, avail, pct, ok := statFilesystem(m.MountPoint)
				if ok {
					ms.TotalBytes = total
					ms.UsedBytes = used
					ms.AvailableBytes = avail
					ms.UsePercent = pct
					ms.TotalHuman = formatBytes(total)
					ms.UsedHuman = formatBytes(used)
					ms.AvailableHuman = formatBytes(avail)
					ms.Online = true
				}
			}
			report.Mounts = append(report.Mounts, ms)
		}
	}

	return report
}

func statFilesystem(path string) (total, used, avail uint64, pct float64, ok bool) {
	if path == "" {
		return 0, 0, 0, 0, false
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, 0, 0, 0, false
	}
	bsize := uint64(stat.Bsize)
	total = stat.Blocks * bsize
	avail = stat.Bavail * bsize
	if total >= stat.Bfree*bsize {
		used = total - (stat.Bfree * bsize)
	}
	if total > 0 {
		pct = (float64(used) / float64(total)) * 100.0
		if pct > 100.0 {
			pct = 100.0
		}
		pct = float64(int(pct*10)) / 10.0
	}
	return total, used, avail, pct, true
}

func formatBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit && exp < 5; n /= unit {
		div *= unit
		exp++
	}
	units := []string{"KB", "MB", "GB", "TB", "PB", "EB"}
	return fmt.Sprintf("%.1f %s", float64(b)/float64(div), units[exp])
}
