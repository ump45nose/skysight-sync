package syncer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type memoryHub struct {
	files       map[string][]byte
	writes      map[string]int
	offline     bool
	failWrite   string
	connections int
}

func newHub() *memoryHub { return &memoryHub{files: map[string][]byte{}, writes: map[string]int{}} }
func (h *memoryHub) connect(ctx context.Context, c Config) (Remote, error) {
	h.connections++
	if h.offline {
		return nil, &TransportError{errors.New("network offline"), true}
	}
	return h, nil
}
func (h *memoryHub) Read(p string, limit int64) ([]byte, error) {
	b, ok := h.files[p]
	if !ok {
		return nil, os.ErrNotExist
	}
	if int64(len(b)) > limit {
		return nil, errors.New("too large")
	}
	return bytes.Clone(b), nil
}
func (h *memoryHub) WriteAtomic(p string, b []byte) error {
	if strings.Contains(p, h.failWrite) && h.failWrite != "" {
		return &TransportError{errors.New("connection lost before manifest commit"), true}
	}
	h.files[p] = bytes.Clone(b)
	h.writes[p]++
	return nil
}
func (h *memoryHub) Close() error { return nil }
func testClient(t *testing.T, d string, h *memoryHub) *Engine {
	t.Helper()
	home := t.TempDir()
	c := Config{DeviceID: d, InstanceID: d + "-installation", CodexHome: home, SSHTarget: "fn", RemoteDir: "/hub", Devices: []string{"macair", "macpro"}}
	if err := os.MkdirAll(c.Resources(), 0700); err != nil {
		t.Fatal(err)
	}
	return &Engine{Config: c, Root: filepath.Join(home, "skysight-sync"), Connect: h.connect, Now: func() time.Time { return mustTime("2026-10-15T01:00:00Z") }}
}
func add(t *testing.T, e *Engine, name, title string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(e.Config.Resources(), name), fixture(name, title), 0600); err != nil {
		t.Fatal(err)
	}
}
func runOK(t *testing.T, e *Engine) Report {
	t.Helper()
	r, err := e.Run(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func bootstrap(t *testing.T, a, b *Engine) {
	t.Helper()
	if _, err := a.Run(context.Background(), false); err == nil || !strings.Contains(err.Error(), "waiting for initial manifest") {
		t.Fatalf("first source must upload without retiring native files: %v", err)
	}
	runOK(t, b)
	runOK(t, a)
}
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	m := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			r, _ := filepath.Rel(root, p)
			m[r] = string(b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestTwoClientsDailyConvergenceDeleteAndRestore(t *testing.T) {
	h := newHub()
	a := testClient(t, "macair", h)
	b := testClient(t, "macpro", h)
	six := "2026-10-13T06-00-00-AAAA-6h-memory-summary.md"
	airTen := "2026-10-13T06-20-00-BBBB-10min-memory-summary.md"
	proTen := "2026-10-13T06-20-00-CCCC-10min-memory-summary.md"
	today := "2026-10-14T16-10-00-DDDD-10min-memory-summary.md"
	add(t, a, six, "Air 六小时")
	add(t, a, airTen, "Air 细节")
	add(t, b, proTen, "Pro 不可丢失")
	add(t, a, today, "今天仍由原生管理")
	bootstrap(t, a, b)
	sa, _ := loadState(a.Root)
	sb, _ := loadState(b.Root)
	key := "2026-10-13T06:00:00Z"
	pa := sa.Published[key]
	pb := sb.Published[key]
	if !reflect.DeepEqual(pa.Inputs, pb.Inputs) || len(pa.Inputs) != 2 {
		t.Fatalf("selected inputs differ: %v %v", pa.Inputs, pb.Inputs)
	}
	for _, e := range []*Engine{a, b} {
		s, _ := loadState(e.Root)
		raw, err := os.ReadFile(filepath.Join(e.Config.Resources(), s.Published[key].Filename))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(raw, []byte("Pro 不可丢失")) || !bytes.Contains(raw, []byte("Air 六小时")) {
			t.Fatal("another device's ten-minute history disappeared")
		}
		if bytes.Contains(raw, []byte("Air 细节")) {
			t.Fatal("same-device six-hour replacement failed")
		}
	}
	if _, err := os.Stat(filepath.Join(a.Config.Resources(), today)); err != nil {
		t.Fatal("today's history was retired")
	}
	connections := h.connections
	if !runOK(t, a).AlreadySynced || h.connections != connections {
		t.Fatal("daily success guard performed network I/O")
	}
	if err := os.Remove(filepath.Join(a.Config.Resources(), pa.Filename)); err != nil {
		t.Fatal(err)
	}
	next := func() time.Time { return mustTime("2026-10-16T01:00:00Z") }
	a.Now = next
	b.Now = next
	add(t, b, "2026-10-13T06-00-00-EEEE-6h-memory-summary.md", "Pro 迟到六小时")
	runOK(t, b)
	runOK(t, a)
	sa, _ = loadState(a.Root)
	sb, _ = loadState(b.Root)
	if !sa.Suppressed[key] {
		t.Fatal("local deletion was not recorded")
	}
	if _, err := os.Stat(filepath.Join(a.Config.Resources(), sa.Published[key].Filename)); !os.IsNotExist(err) {
		t.Fatal("deleted window was restored")
	}
	if sb.Suppressed[key] {
		t.Fatal("deletion propagated to another machine")
	}
	if sb.Published[key].Filename == pb.Filename {
		t.Fatal("changed window reused cached native filename")
	}
	if err := a.Restore(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{six, airTen, today} {
		if _, err := os.Stat(filepath.Join(a.Config.Resources(), name)); err != nil {
			t.Fatalf("restore lost %s", name)
		}
	}
	if _, err := a.Run(context.Background(), false); err == nil {
		t.Fatal("restored history was immediately taken over again")
	}
}

func TestDryRunDoesNotWriteAndImportsKeepProvenance(t *testing.T) {
	h := newHub()
	a := testClient(t, "macair", h)
	b := testClient(t, "macpro", h)
	name := "2026-10-13T06-20-00-CCCC-10min-memory-summary.md"
	add(t, b, name, "Pro original")
	add(t, a, name, "Pro original")
	if err := writeJSON(filepath.Join(a.Root, "validation", "2026-10-15", "macpro-to-macair.json"), map[string]string{"source_device": "macpro", "filename": name}); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, a.Config.CodexHome)
	_, err := a.Run(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	after := snapshot(t, a.Config.CodexHome)
	if !reflect.DeepEqual(before, after) || len(h.files) != 0 {
		t.Fatal("dry-run mutated local or remote state")
	}
	bootstrap(t, a, b)
	s, err := loadState(a.Root)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Sources["macair"].Entries) != 0 || len(s.Sources["macpro"].Entries) != 1 || s.Native[name].DeviceID != "macpro" {
		t.Fatal("imported history re-uploaded as local source")
	}
}

func TestPublicationRecoversWithoutDuplicates(t *testing.T) {
	h := newHub()
	a := testClient(t, "macair", h)
	b := testClient(t, "macpro", h)
	add(t, a, "2026-10-13T06-00-00-AAAA-6h-memory-summary.md", "Air")
	add(t, b, "2026-10-13T06-20-00-CCCC-10min-memory-summary.md", "Pro")
	_, _ = a.Run(context.Background(), false)
	b.PublishHook = func(stage string) error {
		if stage == "after-add" {
			return errors.New("simulated power loss")
		}
		return nil
	}
	if _, err := b.Run(context.Background(), false); err == nil {
		t.Fatal("crash not injected")
	}
	if _, err := os.Stat(filepath.Join(b.Root, "pending.json")); err != nil {
		t.Fatal("missing recovery journal")
	}
	b.PublishHook = nil
	r := runOK(t, b)
	if !r.AlreadySynced {
		t.Fatal("recovered successful batch not recognized")
	}
	s, _ := loadState(b.Root)
	files, _ := os.ReadDir(b.Config.Resources())
	if len(files) != len(s.Published) {
		t.Fatalf("duplicate raw/projected files after recovery: %d %d", len(files), len(s.Published))
	}
	if _, err := os.Stat(filepath.Join(b.Root, "pending.json")); !os.IsNotExist(err) {
		t.Fatal("journal not retired")
	}
}

func TestInterruptedUploadAndOfflineCatchup(t *testing.T) {
	h := newHub()
	a := testClient(t, "macair", h)
	b := testClient(t, "macpro", h)
	name := "2026-10-13T06-00-00-AAAA-6h-memory-summary.md"
	add(t, a, name, "Air")
	add(t, b, "2026-10-13T06-20-00-CCCC-10min-memory-summary.md", "Pro")
	h.failWrite = "manifest.json"
	if _, err := a.Run(context.Background(), false); !Retryable(err) {
		t.Fatalf("expected recoverable upload interruption: %v", err)
	}
	nativeBefore := snapshot(t, a.Config.Resources())
	h.failWrite = ""
	bootstrap(t, a, b)
	for p, count := range h.writes {
		if strings.Contains(p, "/objects/") && count != 1 {
			t.Fatalf("immutable object uploaded repeatedly: %s %d", p, count)
		}
	}
	if len(nativeBefore) != 1 {
		t.Fatal("failed upload changed native history")
	}
	a.Now = func() time.Time { return mustTime("2026-10-16T01:00:00Z") }
	late := "2026-10-14T06-20-00-EEEE-10min-memory-summary.md"
	add(t, a, late, "late arrival")
	before := snapshot(t, a.Config.Resources())
	h.offline = true
	if _, err := a.Run(context.Background(), false); !Retryable(err) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, snapshot(t, a.Config.Resources())) {
		t.Fatal("offline run changed native history")
	}
	s, _ := loadState(a.Root)
	if s.LastError == "" || s.FailureSince == nil {
		t.Fatal("offline error invisible")
	}
	h.offline = false
	runOK(t, a)
	s, _ = loadState(a.Root)
	if s.LastError != "" || s.FailureSince != nil || s.Sources["macair"].Entries[late].Name == "" {
		t.Fatal("catchup did not clear failure and include late source")
	}
}

func TestConflictCorruptSourceAndConcurrentLock(t *testing.T) {
	h := newHub()
	a := testClient(t, "macair", h)
	name := "2026-10-13T06-00-00-AAAA-6h-memory-summary.md"
	add(t, a, name, "original")
	unlock, err := acquire(a.Root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Run(context.Background(), false); err == nil {
		t.Fatal("concurrent sync accepted")
	}
	unlock()
	if err = os.WriteFile(filepath.Join(a.Config.Resources(), name), []byte("---\ntitle: broken\n---\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Run(context.Background(), false); err == nil {
		t.Fatal("malformed source silently skipped")
	}
	add(t, a, name, "original")
	s, err := Parse("macair", name, fixture(name, "original"))
	if err != nil {
		t.Fatal(err)
	}
	h.files[remoteObject(a.Config, "macair", s.Entry)] = []byte("conflicting remote content")
	if _, err = a.Run(context.Background(), false); err == nil || !strings.Contains(err.Error(), "immutable object conflict") {
		t.Fatalf("conflicting content overwritten: %v", err)
	}
}

func TestChangedSourcePreservesOldVersionAndFreshProjectionID(t *testing.T) {
	h := newHub()
	a := testClient(t, "macair", h)
	b := testClient(t, "macpro", h)
	name := "2026-10-13T06-00-00-AAAA-6h-memory-summary.md"
	add(t, a, name, "v1")
	bootstrap(t, a, b)
	s, _ := loadState(a.Root)
	old := s.Sources["macair"].Entries[name]
	oldProjection := s.Published["2026-10-13T06:00:00Z"]
	add(t, a, name, "v2 corrected")
	a.Now = func() time.Time { return mustTime("2026-10-16T01:00:00Z") }
	runOK(t, a)
	s, _ = loadState(a.Root)
	if s.Sources["macair"].Entries[name].Digest == old.Digest || s.Published["2026-10-13T06:00:00Z"].Filename == oldProjection.Filename {
		t.Fatal("source update not published with fresh native identity")
	}
	if _, err := os.Stat(objectPath(a.Root, "macair", old)); err != nil {
		t.Fatal("old source revision was physically deleted")
	}
	if _, ok := h.files[remoteObject(a.Config, "macair", old)]; !ok {
		t.Fatal("remote history revision deleted")
	}
}

func TestNativeEditDuringPublishIsPreservedAndCanBeRetried(t *testing.T) {
	h := newHub()
	a, b := testClient(t, "macair", h), testClient(t, "macpro", h)
	name := "2026-10-13T06-00-00-AAAA-6h-memory-summary.md"
	add(t, a, name, "before edit")
	_, _ = a.Run(context.Background(), false)
	runOK(t, b)
	a.PublishHook = func(stage string) error {
		if stage == "after-add" {
			add(t, a, name, "native concurrent edit")
			a.PublishHook = nil
		}
		return nil
	}
	if _, err := a.Run(context.Background(), false); err == nil || !strings.Contains(err.Error(), "native source changed") {
		t.Fatalf("concurrent edit not reported: %v", err)
	}
	s, _ := loadState(a.Root)
	if s.LastSuccess != nil {
		t.Fatal("failed concurrent publication recorded a false success")
	}
	if _, err := os.Stat(filepath.Join(a.Config.Resources(), name)); err != nil {
		t.Fatal("concurrently updated native file was removed")
	}
	if _, err := os.Stat(filepath.Join(a.Root, "pending.json")); !os.IsNotExist(err) {
		t.Fatal("source edit left an unrecoverable journal")
	}
	runOK(t, a)
	s, _ = loadState(a.Root)
	if s.LastSuccess == nil {
		t.Fatal("retry failed to complete")
	}
}

func ExampleDayCutoff() {
	t, day := DayCutoff(mustTime("2026-10-15T01:00:00Z"))
	fmt.Println(day, t.Format(time.RFC3339)) // Output: 2026-10-15 2026-10-14T16:00:00Z
}
