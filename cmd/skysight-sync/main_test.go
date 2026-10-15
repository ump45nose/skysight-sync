package main

import (
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
