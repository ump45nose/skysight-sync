package syncer

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixture(name, title string) []byte {
	return []byte(fmt.Sprintf("---\ntitle: %s\ndescription: %s 的原始描述。\napplications: [com.example.editor, com.example.browser]\n---\n\n## Memory summary\n\n%s 正文，保留中文与标点。\n", title, title, title))
}
func mustTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}
func TestCutoffAndSelection(t *testing.T) {
	cut, day := DayCutoff(mustTime("2026-10-15T01:00:00Z"))
	if !cut.Equal(mustTime("2026-10-14T16:00:00Z")) || day != "2026-10-15" {
		t.Fatalf("cutoff %s %s", cut, day)
	}
	names := map[string][]string{"macair": {"2026-10-14T12-00-00-Abcd-6h-memory-summary.md", "2026-10-14T15-40-00-Abcd-10min-memory-summary.md", "2026-10-14T16-00-00-Abcd-10min-memory-summary.md"}, "macpro": {"2026-10-14T15-40-00-Efgh-10min-memory-summary.md"}}
	sources := map[string]Manifest{}
	for d, ns := range names {
		m := Manifest{DeviceID: d, Entries: map[string]Entry{}}
		for _, n := range ns {
			m.Entries[n] = Entry{Name: n}
		}
		sources[d] = m
	}
	sel, err := Select(sources, cut)
	if err != nil {
		t.Fatal(err)
	}
	refs := sel["2026-10-14T12:00:00Z"]
	if len(refs) != 2 {
		t.Fatalf("cross-midnight 6h must be deferred; eligible ten-minute summaries retained: %+v", refs)
	}
	six := "2026-10-14T06-00-00-Abcd-6h-memory-summary.md"
	ten := "2026-10-14T06-20-00-Abcd-10min-memory-summary.md"
	other := "2026-10-14T06-20-00-Efgh-10min-memory-summary.md"
	sources = map[string]Manifest{"macair": {Entries: map[string]Entry{six: {Name: six}, ten: {Name: ten}}}, "macpro": {Entries: map[string]Entry{other: {Name: other}}}}
	sel, err = Select(sources, cut)
	if err != nil {
		t.Fatal(err)
	}
	refs = sel["2026-10-14T06:00:00Z"]
	if len(refs) != 2 || refs[0].Entry.Name != six || refs[1].Entry.Name != other {
		t.Fatalf("suppression crossed device boundary: %+v", refs)
	}
}
func TestRenderRetainsOriginalAndSortsMetadata(t *testing.T) {
	root := t.TempDir()
	sources := map[string]Manifest{}
	contents := map[string][]byte{}
	for _, d := range []string{"macpro", "macair"} {
		name := "2026-10-13T06-00-00-" + d + "-6h-memory-summary.md"
		raw := fixture(name, d+"：测试")
		s, err := Parse(d, name, raw)
		if err != nil {
			t.Fatal(err)
		}
		sources[d] = Manifest{Entries: map[string]Entry{name: s.Entry}}
		contents[objectPath(root, d, s.Entry)] = raw
	}
	sel, err := Select(sources, mustTime("2026-10-15T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	refs := sel["2026-10-13T06:00:00Z"]
	b, err := Render(root, "2026-10-13T06:00:00Z", refs, func(p string) ([]byte, error) { return contents[p], nil })
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := Parse("macair", "2026-10-13T06-00-00-sync0123456789abcdef0123456789abcdef-6h-memory-summary.md", b)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Meta.Applications) != 2 || !strings.Contains(parsed.Meta.Title, "macair + macpro") {
		t.Fatalf("bad metadata: %+v", parsed.Meta)
	}
	for _, raw := range contents {
		if !bytes.Contains(b, raw) {
			t.Fatal("original content was modified")
		}
	}
	if !strings.Contains(string(b), filepath.ToSlash(root)+"/archive/macpro/objects/") {
		t.Fatal("missing local archive link")
	}
	for p := range contents {
		contents[p] = []byte("tampered")
		break
	}
	if _, err = Render(root, "", refs, func(p string) ([]byte, error) { return contents[p], nil }); err == nil {
		t.Fatal("archive integrity not checked")
	}
}
func TestMalformedInputAndPaths(t *testing.T) {
	for _, name := range []string{"../escape.md", "2026-02-30T00-00-00-AAAA-6h-memory-summary.md", "2026-10-15T00-00-00-device-id-6h-memory-summary.md", "2026-10-15T00-00-00-AA/../../outside-6h-memory-summary.md"} {
		if _, _, err := ParseName(name); err == nil {
			t.Errorf("accepted %s", name)
		}
	}
	name := "2026-10-15T00-00-00-AAAA-6h-memory-summary.md"
	for _, raw := range []string{"no frontmatter", "---\ntitle: incomplete\n---\nbody", "---\ntitle: x\ndescription: y\napplications: wrong-type\n---\nbody", "---\ntitle: x\ndescription: y\napplications: []\n---invalid\nbody"} {
		if _, err := Parse("macair", name, []byte(raw)); err == nil {
			t.Errorf("accepted malformed input: %s", raw)
		}
	}
	c := Config{DeviceID: "macair", InstanceID: "test", CodexHome: t.TempDir(), SSHTarget: "--evil", RemoteDir: "/hub", Devices: []string{"macair"}}
	if c.Validate() == nil {
		t.Fatal("SSH option injection accepted")
	}
}
func TestLaunchAgentAndMigration(t *testing.T) {
	plist := LaunchAgent("/some path/config.json", "/some path/bin", "/some path")
	for _, expected := range []string{"<integer>9</integer>", "<integer>0</integer>", "<key>RunAtLoad</key><true/>", "--scheduled", "Asia/Shanghai"} {
		if !strings.Contains(plist, expected) {
			t.Fatalf("missing launch option %s", expected)
		}
	}
	if !serviceMatchesConfig([]byte(plist), "/some path/config.json") || serviceMatchesConfig([]byte(plist), "/some path/offline-case/config.json") {
		t.Fatal("one configuration could stop another configuration's service")
	}
	root := t.TempDir()
	p := filepath.Join(root, "validation", "2026-10-15", "macpro-to-macair.json")
	if err := writeJSON(p, map[string]string{"source_device": "macpro", "filename": "2026-10-13T06-00-00-XAkr-6h-memory-summary.md"}); err != nil {
		t.Fatal(err)
	}
	m, err := migrationOrigins(root)
	if err != nil || m["2026-10-13T06-00-00-XAkr-6h-memory-summary.md"] != "macpro" {
		t.Fatalf("provenance migration failed: %v %v", m, err)
	}
	if err = os.WriteFile(p, []byte("not json"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = migrationOrigins(root); err == nil {
		t.Fatal("ignored damaged provenance")
	}
}
