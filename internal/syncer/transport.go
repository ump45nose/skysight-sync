package syncer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"strings"
	"sync"

	"github.com/pkg/sftp"
)

// Remote is deliberately file-only: fn needs no custom daemon or shell commands.
type Remote interface {
	Read(string, int64) ([]byte, error)
	WriteAtomic(string, []byte) error
	Close() error
}
type Connector func(context.Context, Config) (Remote, error)
type TransportError struct {
	Err   error
	Retry bool
}

func (e *TransportError) Error() string { return e.Err.Error() }
func (e *TransportError) Unwrap() error { return e.Err }
func Retryable(err error) bool          { var e *TransportError; return errors.As(err, &e) && e.Retry }

// exec.Cmd may write stderr while a failed SFTP handshake is being inspected.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.b.Len() > 8192 {
		return len(p), nil
	}
	return b.b.Write(p)
}
func (b *lockedBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.b.String() }

type sftpRemote struct {
	client           *sftp.Client
	cmd              *exec.Cmd
	root             string
	privateRootReady bool
}

func ConnectSFTP(ctx context.Context, c Config) (Remote, error) {
	args := []string{"-T", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=15", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=2"}
	if c.SSHHostname != "" {
		args = append(args, "-o", "Hostname="+c.SSHHostname)
	}
	args = append(args, "-s", c.SSHTarget, "sftp")
	cmd := exec.CommandContext(ctx, "/usr/bin/ssh", args...)
	if _, err := os.Stat("/usr/bin/ssh"); os.IsNotExist(err) {
		cmd = exec.CommandContext(ctx, "ssh", args...)
	}
	errout := &lockedBuffer{}
	cmd.Stderr = errout
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		return nil, &TransportError{err, false}
	}
	client, err := sftp.NewClientPipe(out, in)
	if err != nil {
		_ = in.Close()
		_ = cmd.Wait()
		text := strings.TrimSpace(errout.String())
		if text == "" {
			text = err.Error()
		}
		retry := !strings.Contains(text, "Permission denied") && !strings.Contains(text, "Host key verification failed") && !strings.Contains(text, "REMOTE HOST IDENTIFICATION")
		return nil, &TransportError{fmt.Errorf("SFTP connection: %s", text), retry}
	}
	if _, ok := client.HasExtension("posix-rename@openssh.com"); !ok {
		_ = client.Close()
		_ = cmd.Wait()
		return nil, errors.New("fn SFTP must support atomic posix-rename")
	}
	return &sftpRemote{client: client, cmd: cmd, root: c.RemoteDir}, nil
}
func (r *sftpRemote) Read(p string, limit int64) ([]byte, error) {
	f, err := r.client.Open(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, os.ErrNotExist
		}
		return nil, remoteError(err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, remoteError(err)
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("remote file exceeds limit: %s", p)
	}
	return b, nil
}
func remoteError(err error) error {
	var status *sftp.StatusError
	if errors.As(err, &status) {
		return err
	}
	return &TransportError{err, true}
}
func (r *sftpRemote) WriteAtomic(p string, b []byte) error {
	if !r.privateRootReady {
		if err := r.client.MkdirAll(r.root); err != nil {
			return remoteError(err)
		}
		if err := r.client.Chmod(r.root, 0700); err != nil {
			return err
		}
		r.privateRootReady = true
	}
	if err := r.client.MkdirAll(path.Dir(p)); err != nil {
		return remoteError(err)
	}
	// Protect this application's data, never the user's home or other SSH folders.
	if err := r.client.Chmod(path.Dir(p), 0700); err != nil {
		return err
	}
	tmp := path.Join(path.Dir(p), ".upload-"+NewID())
	f, err := r.client.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return remoteError(err)
	}
	defer r.client.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	closeErr := f.Close()
	if err != nil {
		return remoteError(err)
	}
	if closeErr != nil {
		return remoteError(closeErr)
	}
	if err = r.client.PosixRename(tmp, p); err != nil {
		return remoteError(err)
	}
	return nil
}
func (r *sftpRemote) Close() error {
	a := r.client.Close()
	b := r.cmd.Wait()
	if a != nil {
		return a
	}
	return b
}

func remoteObject(c Config, d string, e Entry) string {
	return path.Join(c.RemoteDir, "devices", d, "objects", e.Digest, e.Name)
}
func remoteManifest(c Config, d string) string {
	return path.Join(c.RemoteDir, "devices", d, "manifest.json")
}
