//go:build !windows

package syncer

import "os"

func syncDirectory(d *os.File) error { return d.Sync() }
