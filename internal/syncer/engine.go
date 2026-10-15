package syncer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
)

type Engine struct {
	Config  Config
	Root    string
	Connect Connector
	Now     func() time.Time
	// Test-only crash injection; never set by the installed command.
	PublishHook func(string) error
}
type Report struct {
	Device         string    `json:"device"`
	Cutoff         time.Time `json:"cutoff"`
	AlreadySynced  bool      `json:"already_synced"`
	Upload         []string  `json:"upload"`
	Download       []string  `json:"download"`
	Windows        []string  `json:"windows"`
	Replace        []string  `json:"replace"`
	Suppressed     []string  `json:"suppressed"`
	MissingDevices []string  `json:"missing_devices"`
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now().UTC()
	}
	return time.Now().UTC()
}
func (e *Engine) Run(ctx context.Context, dry bool) (report Report, err error) {
	if err = e.Config.Validate(); err != nil {
		return report, err
	}
	if e.Connect == nil {
		e.Connect = ConnectSFTP
	}
	if !dry {
		var unlock func()
		unlock, err = acquire(e.Root)
		if err != nil {
			return report, err
		}
		defer unlock()
		defer func() {
			if err != nil {
				e.recordFailure(err)
			}
		}()
		if err = recoverBatch(e.Root, e.Config.Resources()); err != nil {
			return report, err
		}
	} else {
		if _, x := os.Stat(filepath.Join(e.Root, "pending.json")); x == nil {
			return report, errors.New("pending publication; run sync before dry-run")
		} else if !os.IsNotExist(x) {
			return report, x
		}
	}
	s, err := loadState(e.Root)
	if err != nil {
		return report, err
	}
	if s.Restored {
		return report, errors.New("native history restored; use init --resume before syncing again")
	}
	now := e.now()
	cutoff, day := DayCutoff(now)
	report = Report{Device: e.Config.DeviceID, Cutoff: cutoff}
	if !dry && s.SuccessDay == day {
		report.AlreadySynced = true
		return report, nil
	}
	origins, err := migrationOrigins(e.Root)
	if err != nil {
		return report, err
	}
	knownOutputs := map[string]bool{}
	for _, p := range s.Published {
		knownOutputs[p.Filename] = true
	}
	for window, p := range s.Published {
		_, x := os.Stat(filepath.Join(e.Config.Resources(), p.Filename))
		if os.IsNotExist(x) {
			s.Suppressed[window] = true
		} else if x != nil {
			return report, x
		}
	}
	own := s.Sources[e.Config.DeviceID]
	if own.DeviceID == "" {
		own = Manifest{DeviceID: e.Config.DeviceID, InstanceID: e.Config.InstanceID, Entries: map[string]Entry{}}
	}
	if own.InstanceID != e.Config.InstanceID {
		return report, errors.New("local manifest belongs to another installation")
	}
	own.Cutoff = cutoff
	// New source bytes are kept in memory for a genuinely read-only dry-run.
	staged := map[string][]byte{}
	files, err := os.ReadDir(e.Config.Resources())
	if err != nil {
		return report, fmt.Errorf("read native history: %w", err)
	}
	for _, f := range files {
		name := f.Name()
		if knownOutputs[name] || !namePattern.MatchString(name) {
			continue
		}
		if m := namePattern.FindStringSubmatch(name); len(m[2]) == 36 && strings.HasPrefix(m[2], "sync") {
			return report, fmt.Errorf("untracked generated summary %s; recover state instead of uploading it as a source", name)
		}
		t, d, _ := ParseName(name)
		if t.Add(d).After(cutoff) {
			continue
		}
		if !f.Type().IsRegular() {
			return report, fmt.Errorf("native summary is not a regular file: %s", name)
		}
		b, x := readLimited(filepath.Join(e.Config.Resources(), name), MaxDocument)
		if x != nil {
			return report, x
		}
		device := e.Config.DeviceID
		if n, ok := s.Native[name]; ok {
			device = n.DeviceID
		} else if origin := origins[name]; origin != "" {
			device = origin
		}
		if !contains(e.Config.Devices, device) {
			return report, fmt.Errorf("source %s is not a participating device", device)
		}
		summary, x := Parse(device, name, b)
		if x != nil {
			return report, x
		}
		entry := summary.Entry
		staged[objectPath(e.Root, device, entry)] = b
		if !dry {
			if x = archive(e.Root, device, entry, b); x != nil {
				return report, x
			}
		}
		s.Native[name] = NativeRef{device, entry.Digest}
		if device == e.Config.DeviceID {
			own.Entries[name] = entry
		}
	}
	s.Sources[e.Config.DeviceID] = own
	if !dry {
		if err = writeJSON(filepath.Join(e.Root, "state.json"), s); err != nil {
			return report, err
		}
	}
	remote, err := e.Connect(ctx, e.Config)
	if err != nil {
		return report, err
	}
	defer remote.Close()
	remoteSources := map[string]Manifest{}
	for _, device := range e.Config.Devices {
		b, x := remote.Read(remoteManifest(e.Config, device), MaxManifest)
		if os.IsNotExist(x) {
			if device != e.Config.DeviceID {
				report.MissingDevices = append(report.MissingDevices, device)
			}
			continue
		}
		if x != nil {
			return report, x
		}
		var m Manifest
		if x = json.Unmarshal(b, &m); x != nil {
			return report, fmt.Errorf("%s manifest: %w", device, x)
		}
		if x = validateManifest(m, device); x != nil {
			return report, x
		}
		remoteSources[device] = m
	}
	oldRemote := remoteSources[e.Config.DeviceID]
	if oldRemote.DeviceID != "" && oldRemote.InstanceID != e.Config.InstanceID {
		return report, errors.New("remote device ID is owned by another installation; preserve its config or choose a distinct device")
	}
	// A remote-only entry can occur after restoring a saved local state. Keep it,
	// rather than making an older local manifest erase an existing source record.
	for name, entry := range oldRemote.Entries {
		if _, ok := own.Entries[name]; !ok {
			own.Entries[name] = entry
		}
	}
	for _, name := range sortedEntryNames(own.Entries) {
		entry := own.Entries[name]
		old, exists := oldRemote.Entries[name]
		if exists && old == entry {
			continue
		}
		b, x := e.readObject(entry, e.Config.DeviceID, staged)
		if x != nil {
			return report, x
		}
		report.Upload = append(report.Upload, name)
		if !dry {
			p := remoteObject(e.Config, e.Config.DeviceID, entry)
			existing, x := remote.Read(p, MaxDocument)
			if x == nil {
				if !bytes.Equal(existing, b) {
					return report, fmt.Errorf("remote immutable object conflict: %s", name)
				}
			} else if !os.IsNotExist(x) {
				return report, x
			} else {
				if x = remote.WriteAtomic(p, b); x != nil {
					return report, x
				}
				verified, x := remote.Read(p, MaxDocument)
				if x != nil {
					return report, x
				}
				if !bytes.Equal(b, verified) {
					return report, fmt.Errorf("uploaded source failed verification: %s", name)
				}
			}
		}
	}
	if !dry {
		b, x := json.Marshal(own)
		if x != nil {
			return report, x
		}
		if x = remote.WriteAtomic(remoteManifest(e.Config, e.Config.DeviceID), b); x != nil {
			return report, x
		}
		check, x := remote.Read(remoteManifest(e.Config, e.Config.DeviceID), MaxManifest)
		if x != nil {
			return report, x
		}
		if !bytes.Equal(check, b) {
			return report, errors.New("remote manifest commit verification failed")
		}
	}
	remoteSources[e.Config.DeviceID] = own
	for device, m := range remoteSources {
		for _, name := range sortedEntryNames(m.Entries) {
			entry := m.Entries[name]
			p := objectPath(e.Root, device, entry)
			if b, ok := staged[p]; ok {
				if Digest(b) != entry.Digest {
					return report, fmt.Errorf("imported source conflicts with its origin: %s/%s", device, name)
				}
				continue
			}
			info, x := os.Stat(p)
			if x == nil {
				if info.Size() != entry.Size {
					return report, fmt.Errorf("archive size mismatch: %s", p)
				}
				continue
			}
			if !os.IsNotExist(x) {
				return report, x
			}
			b, x := remote.Read(remoteObject(e.Config, device, entry), MaxDocument)
			if x != nil {
				return report, x
			}
			if int64(len(b)) != entry.Size || Digest(b) != entry.Digest {
				return report, fmt.Errorf("download integrity mismatch: %s/%s", device, name)
			}
			if _, x = Parse(device, name, b); x != nil {
				return report, x
			}
			report.Download = append(report.Download, device+"/"+name)
			staged[p] = b
			if !dry {
				if x = archive(e.Root, device, entry, b); x != nil {
					return report, x
				}
			}
		}
		s.Sources[device] = m
	}
	if len(report.MissingDevices) > 0 {
		if dry {
			return report, nil
		}
		if err = writeJSON(filepath.Join(e.Root, "state.json"), s); err != nil {
			return report, err
		}
		return report, fmt.Errorf("uploaded this device; waiting for initial manifest from %s (native history unchanged)", strings.Join(report.MissingDevices, ", "))
	}
	selected, err := Select(s.Sources, cutoff)
	if err != nil {
		return report, err
	}
	// A user may delete a window while the network transfer is in progress.
	for window, p := range s.Published {
		if _, x := os.Stat(filepath.Join(e.Config.Resources(), p.Filename)); os.IsNotExist(x) {
			s.Suppressed[window] = true
		} else if x != nil {
			return report, x
		}
	}
	batch := Batch{Next: cloneState(s)}
	keys := make([]string, 0, len(selected))
	for k := range selected {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		refs := selected[key]
		inputs := InputKeys(refs)
		previous, exists := s.Published[key]
		if s.Suppressed[key] {
			report.Suppressed = append(report.Suppressed, key)
			continue
		}
		if exists && reflect.DeepEqual(previous.Inputs, inputs) {
			continue
		}
		b, x := Render(e.Root, key, refs, func(p string) ([]byte, error) {
			if data, ok := staged[p]; ok {
				return data, nil
			}
			return readLimited(p, MaxDocument)
		})
		if x != nil {
			return report, x
		}
		t, _ := time.Parse(time.RFC3339, key)
		name := t.Format("2006-01-02T15-04-05") + "-sync" + NewID() + "-6h-memory-summary.md"
		batch.Adds = append(batch.Adds, Addition{name, b})
		batch.Next.Published[key] = Published{name, inputs, Digest(b)}
		report.Windows = append(report.Windows, key)
		if exists {
			batch.Removes = append(batch.Removes, Removal{previous.Filename, previous.Digest})
		}
	}
	// Only raw files currently on disk are retired, and only after their exact
	// immutable version is present in the committed source manifest.
	for _, f := range files {
		name := f.Name()
		n, ok := s.Native[name]
		if !ok || knownOutputs[name] {
			continue
		}
		t, d, x := ParseName(name)
		if x != nil {
			return report, x
		}
		if t.Add(d).After(cutoff) {
			continue
		}
		m, ok := s.Sources[n.DeviceID]
		if !ok {
			return report, fmt.Errorf("source manifest unavailable for %s", name)
		}
		entry, ok := m.Entries[name]
		if !ok || entry.Digest != n.Digest {
			return report, fmt.Errorf("origin manifest does not confirm imported file: %s", name)
		}
		if _, x = os.Stat(objectPath(e.Root, n.DeviceID, entry)); x != nil && !dry {
			return report, x
		}
		batch.Removes = append(batch.Removes, Removal{name, n.Digest})
		report.Replace = append(report.Replace, name)
	}
	sort.Strings(report.Upload)
	sort.Strings(report.Download)
	sort.Strings(report.Replace)
	if dry {
		return report, nil
	}
	completedAt := e.now()
	batch.Next.LastSuccess = &completedAt
	batch.Next.SuccessDay = day
	batch.Next.Cutoff = cutoff
	batch.Next.FailureSince = nil
	batch.Next.LastError = ""
	batch.Next.Notified = false
	if err = writeJSON(filepath.Join(e.Root, "pending.json"), batch); err != nil {
		return report, err
	}
	if err = applyBatch(e.Root, e.Config.Resources(), batch, e.PublishHook); err != nil {
		return report, err
	}
	return report, nil
}
func (e *Engine) readObject(entry Entry, device string, staged map[string][]byte) ([]byte, error) {
	p := objectPath(e.Root, device, entry)
	b, ok := staged[p]
	var err error
	if !ok {
		b, err = readLimited(p, MaxDocument)
		if err != nil {
			return nil, err
		}
	}
	if int64(len(b)) != entry.Size || Digest(b) != entry.Digest {
		return nil, fmt.Errorf("archive integrity mismatch: %s", p)
	}
	return b, nil
}
func (e *Engine) recordFailure(cause error) {
	s, err := loadState(e.Root)
	if err != nil {
		return
	}
	now := e.now()
	if s.FailureSince == nil {
		s.FailureSince = &now
	}
	s.LastError = cause.Error()
	_ = writeJSON(filepath.Join(e.Root, "state.json"), s)
}
func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
func sortedEntryNames(m map[string]Entry) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
