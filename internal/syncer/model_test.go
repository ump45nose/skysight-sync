package syncer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
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
func TestRenderUsesActivityTitlesAndKeepsSourceBody(t *testing.T) {
	root := t.TempDir()
	sources := map[string]Manifest{}
	contents := map[string][]byte{}
	for _, d := range []string{"macpro", "macair"} {
		name := "2026-10-13T06-00-00-" + d + "-6h-memory-summary.md"
		title := map[string]string{"macair": "Aide 修复验收", "macpro": "立绘管理设计"}[d]
		raw := fixture(name, title)
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
	if len(parsed.Meta.Applications) != 2 || parsed.Meta.Title != "Aide 修复验收" {
		t.Fatalf("bad metadata: %+v", parsed.Meta)
	}
	for _, title := range []string{"Aide 修复验收", "立绘管理设计"} {
		if !strings.Contains(parsed.Body, title+" 正文，保留中文与标点。") {
			t.Fatal("source body was lost")
		}
	}
	for _, unwanted := range []string{"UTC", "2026-10-13", "macair", "macpro", "份摘要", "最近摘录"} {
		if strings.Contains(parsed.Meta.Title+parsed.Meta.Description, unwanted) {
			t.Fatalf("transport metadata leaked into preview: %s", unwanted)
		}
	}
	for _, unwanted := range []string{"# 跨设备电脑历史", "## macair", "## macpro", "2026-10-13T06:00:00Z", "来源：", "title:", "description:", "## Memory summary"} {
		if strings.Contains(parsed.Body, unwanted) {
			t.Fatalf("presentation clutter: %s", unwanted)
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

func TestRenderCopiesNativeOverviewInStableSourceOrder(t *testing.T) {
	root := t.TempDir()
	contents := map[string][]byte{}
	makeSource := func(device, name, title, description, body string) SourceRef {
		meta, err := yaml.Marshal(Frontmatter{Title: title, Description: description, Applications: []string{"com.example.editor"}})
		if err != nil {
			t.Fatal(err)
		}
		raw := append(append([]byte("---\n"), meta...), []byte("---\n\n## Memory summary\n\n"+body+"\n")...)
		s, err := Parse(device, name, raw)
		if err != nil {
			t.Fatal(err)
		}
		contents[objectPath(root, device, s.Entry)] = raw
		return SourceRef{device, s.Entry, s.Start, s.Duration}
	}
	longDescription := "第一句原样保留。第二句也不截取。" + strings.Repeat("完整描述", 100)
	a := makeSource("macair", "2026-10-13T06-00-00-AA-6h-memory-summary.md", "  原生活动标题  ", longDescription, "Air 的全部正文。")
	b := makeSource("macpro", "2026-10-13T06-00-00-BB-6h-memory-summary.md", "另一台的概览", "Pro 的描述。", strings.Repeat("更长的 Pro 正文。", 100))
	early := makeSource("macair", "2026-10-13T06-00-00-AA-10min-memory-summary.md", "较早原文", "较早的完整描述。还有第二句。", "较早正文。")
	sameTime := makeSource("macair", "2026-10-13T06-00-00-ZZ-10min-memory-summary.md", "同一时刻较后文件", "按文件名排序。", "同一时刻的正文。")
	later := makeSource("macair", "2026-10-13T06-10-00-AA-10min-memory-summary.md", "较晚原文", "较晚的描述。", "较晚正文。")
	proTen := makeSource("macpro", "2026-10-13T06-00-00-AA-10min-memory-summary.md", "另一来源原文", "另一来源的描述。", "Pro 10min 正文。")
	for _, tc := range []struct {
		name               string
		refs               []SourceRef
		title, description string
	}{
		{"device_before_body_length", []SourceRef{b, a}, "  原生活动标题  ", longDescription},
		{"six_hour_before_device", []SourceRef{early, b}, "另一台的概览", "Pro 的描述。"},
		{"ten_minute_source_time_filename", []SourceRef{later, sameTime, proTen, early}, "较早原文", "较早的完整描述。还有第二句。"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			read := func(p string) ([]byte, error) { return contents[p], nil }
			raw, err := Render(root, "2026-10-13T06:00:00Z", tc.refs, read)
			if err != nil {
				t.Fatal(err)
			}
			s, err := Parse("macair", "2026-10-13T06-00-00-sync0123456789abcdef0123456789abcdef-6h-memory-summary.md", raw)
			if err != nil || s.Meta.Title != tc.title || s.Meta.Description != tc.description {
				t.Fatalf("native metadata changed: %+v %v", s.Meta, err)
			}
			for _, ref := range tc.refs {
				original, err := Parse(ref.Device, ref.Entry.Name, contents[objectPath(root, ref.Device, ref.Entry)])
				if err != nil || !strings.Contains(s.Body, strings.TrimPrefix(original.Body, "## Memory summary\n\n")) {
					t.Fatal("source body lost", err)
				}
			}
			reversed := append([]SourceRef(nil), tc.refs...)
			for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
				reversed[i], reversed[j] = reversed[j], reversed[i]
			}
			again, err := Render(root, "2026-10-13T06:00:00Z", reversed, read)
			if err != nil || string(raw) != string(again) {
				t.Fatal("input enumeration changed rendered content", err)
			}
			if tc.name == "ten_minute_source_time_filename" && strings.Index(s.Body, "较早正文。") > strings.Index(s.Body, "较晚正文。") {
				t.Fatal("body is not chronological")
			}
		})
	}
}

func TestParseSeparatesFrontmatterWithoutChangingRaw(t *testing.T) {
	name := "2026-10-19T06-00-00-TEST-10min-memory-summary.md"
	for _, suffix := range []string{"\r\n\r\n正文。\r\n", ""} {
		raw := []byte("---\r\ntitle: 标题\r\ndescription: 完整描述。\r\napplications: []\r\n---" + suffix)
		s, err := Parse("macair", name, raw)
		if err != nil || string(s.Raw) != string(raw) {
			t.Fatal("raw archive bytes changed", err)
		}
		if s.Body != strings.TrimSpace(strings.ReplaceAll(suffix, "\r\n", "\n")) {
			t.Fatalf("body boundary: %q", s.Body)
		}
	}
}
