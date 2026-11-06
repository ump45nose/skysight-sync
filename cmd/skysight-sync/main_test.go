package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ump45nose/skysight-sync/internal/syncer"
)

func command(t *testing.T, args ...string) error {
	t.Helper()
	old := os.Args
	os.Args = append([]string{"skysight-sync"}, args...)
	defer func() { os.Args = old }()
	return run()
}

func TestInitRequiresExplicitRemoteDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	err := command(t, "init", "--device", "macair", "--ssh-target", "history-hub")
	if err == nil || !strings.Contains(err.Error(), "--remote-dir") {
		t.Fatalf("missing remote directory: %v", err)
	}
	p := filepath.Join(home, "skysight-sync", "config.json")
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("incomplete init wrote configuration: %v", err)
	}
	if err = command(t, "init", "--device", "macair", "--ssh-target", "history-hub", "--remote-dir", "/srv/private-history"); err != nil {
		t.Fatal(err)
	}
	c, err := syncer.LoadConfig(p)
	if err != nil || c.RemoteDir != "/srv/private-history" {
		t.Fatalf("explicit remote directory not saved: %+v %v", c, err)
	}
}

func TestInitPreservesExistingConfigurationAndIdentity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	p := filepath.Join(home, "skysight-sync", "config.json")
	c := syncer.Config{DeviceID: "office", InstanceID: "existing-installation", CodexHome: home, SSHTarget: "existing-alias", SSHHostname: "hub.example.com", RemoteDir: "/srv/existing-history", Devices: []string{"office", "home"}}
	if err := syncer.Initialize(p, c, false); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init"}, {"init", "--resume"}} {
		if err := command(t, args...); err != nil {
			t.Fatal(err)
		}
		got, err := syncer.LoadConfig(p)
		if err != nil || !reflect.DeepEqual(got, c) {
			t.Fatalf("existing configuration changed: %+v %v", got, err)
		}
	}
}

func TestStatusPrintsFileIssues(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	if err := command(t, "init", "--device", "macair", "--ssh-target", "history-hub", "--remote-dir", "/srv/private-history"); err != nil {
		t.Fatal(err)
	}
	bad, odd, digest := "2026-10-13T06-00-00-AAAA-6h-memory-summary.md", "2026-10-13T06-20-00-BBBB-10min-memory-summary.md", strings.Repeat("a", 64)
	recovered := "unquoted metadata scalar recovered in memory; original archived unchanged"
	state, err := json.Marshal(syncer.State{Sources: map[string]syncer.Manifest{}, Native: map[string]syncer.NativeRef{}, Published: map[string]syncer.Published{}, Suppressed: map[string]bool{}, Issues: map[string]syncer.FileIssue{
		"macpro/" + bad: {Device: "macpro", Filename: bad, Digest: digest, Reason: "missing YAML frontmatter"},
		"macair/" + odd: {Device: "macair", Filename: odd, Digest: digest, Reason: recovered, Recovered: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(home, "skysight-sync", "state.json"), state, 0600); err != nil {
		t.Fatal(err)
	}
	out, err := os.Create(filepath.Join(t.TempDir(), "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	stdout := os.Stdout
	os.Stdout = out
	err = command(t, "status")
	os.Stdout = stdout
	if err != nil {
		t.Fatal(err)
	}
	printed, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"\n问题 macair/" + odd + "：" + recovered + "\n", "\n问题 macpro/" + bad + "：missing YAML frontmatter\n"} {
		if !strings.Contains(string(printed), line) {
			t.Fatalf("missing %q in status:\n%s", line, printed)
		}
	}
	if strings.Contains(string(printed), digest) {
		t.Fatal("status printed a digest")
	}
}
