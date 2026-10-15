package syncer

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

func Initialize(configPath string, c Config, resume bool) error {
	root := filepath.Dir(configPath)
	if err := c.Validate(); err != nil {
		return err
	}
	if _, err := os.Stat(c.CodexHome); err != nil {
		return fmt.Errorf("existing CODEX_HOME: %w", err)
	}
	unlock, err := acquire(root)
	if err != nil {
		return err
	}
	defer unlock()
	if old, err := LoadConfig(configPath); err == nil {
		if old.DeviceID != c.DeviceID || old.CodexHome != c.CodexHome {
			return errors.New("existing configuration has a different device or CODEX_HOME")
		}
		c.InstanceID = old.InstanceID
	} else if !os.IsNotExist(err) {
		return err
	}
	if err = writeJSON(configPath, c); err != nil {
		return err
	}
	if resume {
		s, x := loadState(root)
		if x != nil {
			return x
		}
		s.Restored = false
		s.SuccessDay = ""
		return writeJSON(filepath.Join(root, "state.json"), s)
	}
	return nil
}

type SourceStatus struct {
	Device  string    `json:"device"`
	Cutoff  time.Time `json:"cutoff"`
	Records int       `json:"records"`
}
type Status struct {
	Device             string         `json:"device"`
	Version            string         `json:"version"`
	LastSuccess        *time.Time     `json:"last_success,omitempty"`
	Cutoff             time.Time      `json:"cutoff"`
	Sources            []SourceStatus `json:"sources"`
	Windows            int            `json:"windows"`
	Suppressed         int            `json:"suppressed"`
	PendingPublication bool           `json:"pending_publication"`
	FailureSince       *time.Time     `json:"failure_since,omitempty"`
	Error              string         `json:"error,omitempty"`
	Restored           bool           `json:"restored"`
}

func (e *Engine) Status() (Status, error) {
	s, err := loadState(e.Root)
	if err != nil {
		return Status{}, err
	}
	status := Status{Device: e.Config.DeviceID, Version: Version, LastSuccess: s.LastSuccess, Cutoff: s.Cutoff, Windows: len(s.Published), Suppressed: len(s.Suppressed), FailureSince: s.FailureSince, Error: s.LastError, Restored: s.Restored}
	_, err = os.Stat(filepath.Join(e.Root, "pending.json"))
	if err == nil {
		status.PendingPublication = true
	} else if !os.IsNotExist(err) {
		return status, err
	}
	for device, m := range s.Sources {
		status.Sources = append(status.Sources, SourceStatus{device, m.Cutoff, len(m.Entries)})
	}
	sort.Slice(status.Sources, func(i, j int) bool { return status.Sources[i].Device < status.Sources[j].Device })
	return status, nil
}

func (e *Engine) Restore() error {
	unlock, err := acquire(e.Root)
	if err != nil {
		return err
	}
	defer unlock()
	if err = recoverBatch(e.Root, e.Config.Resources()); err != nil {
		return err
	}
	s, err := loadState(e.Root)
	if err != nil {
		return err
	}
	batch := Batch{Next: cloneState(s)}
	entries := map[string]NativeRef{}
	for name, entry := range s.Sources[e.Config.DeviceID].Entries {
		entries[name] = NativeRef{e.Config.DeviceID, entry.Digest}
	}
	for name, ref := range s.Native {
		if ref.DeviceID != e.Config.DeviceID {
			entries[name] = ref
		}
	}
	names := make([]string, 0, len(entries))
	for n := range entries {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		ref := entries[name]
		p := objectPath(e.Root, ref.DeviceID, Entry{Name: name, Digest: ref.Digest})
		b, x := readLimited(p, MaxDocument)
		if x != nil {
			return x
		}
		if Digest(b) != ref.Digest {
			return fmt.Errorf("restore archive corrupt: %s", name)
		}
		if existing, x := readLimited(filepath.Join(e.Config.Resources(), name), MaxDocument); x == nil {
			if Digest(existing) != ref.Digest {
				return fmt.Errorf("restore would overwrite a changed file: %s", name)
			}
		} else if !os.IsNotExist(x) {
			return x
		}
		batch.Adds = append(batch.Adds, Addition{name, b})
		batch.Next.Native[name] = ref
	}
	for _, p := range s.Published {
		batch.Removes = append(batch.Removes, Removal{p.Filename, p.Digest})
	}
	batch.Next.Published = map[string]Published{}
	batch.Next.Suppressed = map[string]bool{}
	batch.Next.Restored = true
	batch.Next.SuccessDay = ""
	if err = writeJSON(filepath.Join(e.Root, "pending.json"), batch); err != nil {
		return err
	}
	return applyBatch(e.Root, e.Config.Resources(), batch, nil)
}

const serviceLabel = "com.skysight-sync.daily"

func servicePath() (string, error) {
	home, err := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", serviceLabel+".plist"), err
}
func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
func LaunchAgent(configPath, executable, root string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>%s</string>
<key>ProgramArguments</key><array><string>%s</string><string>--config</string><string>%s</string><string>sync</string><string>--scheduled</string></array>
<key>RunAtLoad</key><true/>
<key>StartCalendarInterval</key><dict><key>Hour</key><integer>9</integer><key>Minute</key><integer>0</integer></dict>
<key>EnvironmentVariables</key><dict><key>PATH</key><string>/usr/bin:/bin:/usr/sbin:/sbin</string><key>TZ</key><string>Asia/Shanghai</string></dict>
<key>ProcessType</key><string>Background</string><key>LowPriorityIO</key><true/>
<key>StandardOutPath</key><string>%s</string><key>StandardErrorPath</key><string>%s</string>
</dict></plist>
`, serviceLabel, xmlEscape(executable), xmlEscape(configPath), xmlEscape(filepath.Join(root, "logs", "service.log")), xmlEscape(filepath.Join(root, "logs", "service.log")))
}
func InstallService(configPath string) error {
	if runtime.GOOS != "darwin" {
		return errors.New("LaunchAgent installation requires macOS")
	}
	// launchd interprets calendar components in the host's timezone, not TZ in the
	// child environment. Reject another timezone rather than silently running late.
	_, offset := time.Now().Zone()
	if offset != 8*3600 {
		return errors.New("09:00 schedule requires this Mac's timezone to be Asia/Shanghai (UTC+08:00)")
	}
	configPath, err := filepath.Abs(configPath)
	if err != nil {
		return err
	}
	if _, err := LoadConfig(configPath); err != nil {
		return err
	}
	root := filepath.Dir(configPath)
	state, err := loadState(root)
	if err != nil {
		return err
	}
	if state.Restored {
		return errors.New("run init --resume before installing the service")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Join(root, "logs"), 0700); err != nil {
		return err
	}
	p, err := servicePath()
	if err != nil {
		return err
	}
	if _, err = os.Stat(p); err == nil {
		installed, x := os.ReadFile(p)
		if x != nil {
			return x
		}
		if !serviceMatchesConfig(installed, configPath) {
			return errors.New("daily service belongs to another configuration; it was not replaced")
		}
		if err = UninstallService(configPath); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err = writeAtomic(p, []byte(LaunchAgent(configPath, exe, root))); err != nil {
		return err
	}
	out, err := exec.Command("/bin/launchctl", "bootstrap", fmt.Sprintf("gui/%d", os.Getuid()), p).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl bootstrap: %s: %w", out, err)
	}
	return nil
}
func serviceMatchesConfig(plist []byte, configPath string) bool {
	abs, err := filepath.Abs(configPath)
	return err == nil && strings.Contains(string(plist), "<string>"+xmlEscape(abs)+"</string>")
}

func UninstallService(configPath string) error {
	if runtime.GOOS != "darwin" {
		return errors.New("LaunchAgent removal requires macOS")
	}
	p, err := servicePath()
	if err != nil {
		return err
	}
	if _, err = os.Stat(p); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	installed, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	if !serviceMatchesConfig(installed, configPath) {
		return nil
	}
	// Query before bootout: a missing job is normal after a logout/reboot.
	domain := fmt.Sprintf("gui/%d/%s", os.Getuid(), serviceLabel)
	if exec.Command("/bin/launchctl", "print", domain).Run() == nil {
		if out, x := exec.Command("/bin/launchctl", "bootout", domain).CombinedOutput(); x != nil {
			return fmt.Errorf("launchctl bootout: %s: %w", out, x)
		}
	}
	return os.Remove(p)
}
func (e *Engine) NotifyFailure() error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	unlock, err := acquire(e.Root)
	if err != nil {
		return err
	}
	defer unlock()
	s, err := loadState(e.Root)
	if err != nil {
		return err
	}
	if s.Notified || s.FailureSince == nil || e.now().Sub(*s.FailureSince) < 24*time.Hour {
		return nil
	}
	// Fixed copy only: neither secrets nor untrusted error strings enter AppleScript.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "/usr/bin/osascript", "-e", `display notification "超过 24 小时未能同步，请运行 skysight-sync status 查看原因。" with title "电脑历史同步失败"`).CombinedOutput(); err != nil {
		return fmt.Errorf("notification failed: %s: %w", out, err)
	}
	s.Notified = true
	return writeJSON(filepath.Join(e.Root, "state.json"), s)
}
