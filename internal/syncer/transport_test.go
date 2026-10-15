package syncer

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/pkg/sftp"
)

func TestRealSFTPAtomicPublicationAndPrivatePermissions(t *testing.T) {
	a, b := net.Pipe()
	server, err := sftp.NewServer(b)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	go server.Serve()
	client, err := sftp.NewClientPipe(a, a)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	root := filepath.Join(t.TempDir(), "private-root")
	r := &sftpRemote{client: client, root: root}
	if _, err := r.Read(filepath.Join(root, "manifest.json"), MaxManifest); !os.IsNotExist(err) {
		t.Fatalf("missing manifest: %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("read-only connection created the remote root")
	}
	p := filepath.Join(root, "devices", "macair", "manifest.json")
	for _, value := range []string{"first immutable snapshot", "updated manifest"} {
		if err := r.WriteAtomic(p, []byte(value)); err != nil {
			t.Fatal(err)
		}
		got, err := r.Read(p, MaxManifest)
		if err != nil || string(got) != value {
			t.Fatalf("atomic readback: %s %v", got, err)
		}
	}
	for p, want := range map[string]os.FileMode{root: 0700, p: 0600} {
		info, err := os.Stat(p)
		if err != nil || info.Mode().Perm() != want {
			t.Fatalf("private permissions %s: %v %v", p, info, err)
		}
	}
}
