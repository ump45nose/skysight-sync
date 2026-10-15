//go:build windows

package syncer

import "os"

// Windows does not support fsync on a directory handle opened by os.Open.
func syncDirectory(d *os.File) error { return nil }
