package syncer

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"
)

func readLimited(p string, limit int64) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err == nil && int64(len(b)) > limit {
		return nil, fmt.Errorf("file exceeds size limit: %s", p)
	}
	return b, err
}
func readJSON(p string, v any) error {
	b, err := readLimited(p, 128<<20)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
func writeAtomic(p string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".sync-tmp-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp, p); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(p))
	if err != nil {
		return err
	}
	defer d.Close()
	return syncDirectory(d)
}
func writeJSON(p string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(p, append(b, '\n'))
}
func archive(root, device string, e Entry, b []byte) error {
	p := objectPath(root, device, e)
	old, err := readLimited(p, MaxDocument)
	if err == nil {
		if !bytes.Equal(old, b) {
			return fmt.Errorf("immutable archive conflict: %s/%s", device, e.Name)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	return writeAtomic(p, b)
}
func acquire(root string) (func(), error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(root, 0700); err != nil {
		return nil, err
	}
	l := flock.New(filepath.Join(root, "run.lock"))
	ok, err := l.TryLock()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("another sync or restore is running")
	}
	return func() { _ = l.Unlock() }, nil
}

type Addition struct {
	Filename string `json:"filename"`
	Content  []byte `json:"content"`
}
type Removal struct {
	Filename string `json:"filename"`
	Digest   string `json:"sha256"`
}
type Batch struct {
	Adds    []Addition `json:"adds"`
	Removes []Removal  `json:"removes"`
	Next    State      `json:"next_state"`
}

// The caller writes this journal only after both archive and remote commit succeed.
// Each file operation is replayable; state is committed after the native directory.
func applyBatch(root, resources string, b Batch, hook func(string) error) error {
	changedRaw := false
	for _, a := range b.Adds {
		if _, _, err := ParseName(a.Filename); err != nil {
			return err
		}
		p := filepath.Join(resources, a.Filename)
		old, err := readLimited(p, 64<<20)
		if err == nil {
			if !bytes.Equal(old, a.Content) {
				return fmt.Errorf("native file conflict: %s", a.Filename)
			}
		} else if !os.IsNotExist(err) {
			return err
		} else if err = writeAtomic(p, a.Content); err != nil {
			return err
		}
		if hook != nil {
			if err = hook("after-add"); err != nil {
				return err
			}
		}
	}
	for _, r := range b.Removes {
		if _, _, err := ParseName(r.Filename); err != nil {
			return err
		}
		p := filepath.Join(resources, r.Filename)
		old, err := readLimited(p, 64<<20)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if Digest(old) != r.Digest {
			if _, raw := b.Next.Native[r.Filename]; raw {
				// A late native edit is a new source revision, not a reason to erase
				// it or leave the journal permanently unrecoverable.
				changedRaw = true
				continue
			}
			return fmt.Errorf("file changed during publication; preserved %s", r.Filename)
		}
		if err = os.Remove(p); err != nil {
			return err
		}
		if hook != nil {
			if err = hook("after-remove"); err != nil {
				return err
			}
		}
	}
	if changedRaw {
		current, err := loadState(root)
		if err != nil {
			return err
		}
		b.Next.SuccessDay = current.SuccessDay
		b.Next.LastSuccess = current.LastSuccess
		b.Next.Cutoff = current.Cutoff
	}
	dir, err := os.Open(resources)
	if err != nil {
		return err
	}
	err = syncDirectory(dir)
	_ = dir.Close()
	if err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(root, "state.json"), b.Next); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(root, "pending.json")); err != nil {
		return err
	}
	if changedRaw {
		return errors.New("native source changed during publication and was preserved; run sync again to archive its new revision")
	}
	return nil
}
func recoverBatch(root, resources string) error {
	var b Batch
	err := readJSON(filepath.Join(root, "pending.json"), &b)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("pending publication: %w", err)
	}
	return applyBatch(root, resources, b, nil)
}

func migrationOrigins(root string) (map[string]string, error) {
	result := map[string]string{}
	files, err := filepath.Glob(filepath.Join(root, "validation", "*", "*-to-*.json"))
	if err != nil {
		return nil, err
	}
	for _, p := range files {
		var m struct {
			Source   string `json:"source_device"`
			Filename string `json:"filename"`
		}
		if err = readJSON(p, &m); err != nil {
			return nil, fmt.Errorf("read import provenance: %w", err)
		}
		if m.Source == "" || m.Filename == "" {
			continue
		}
		if !devicePattern.MatchString(m.Source) {
			return nil, fmt.Errorf("invalid import source in %s", p)
		}
		if _, _, err = ParseName(m.Filename); err != nil {
			return nil, err
		}
		if old := result[m.Filename]; old != "" && old != m.Source {
			return nil, fmt.Errorf("conflicting provenance: %s", m.Filename)
		}
		result[m.Filename] = m.Source
	}
	return result, nil
}
