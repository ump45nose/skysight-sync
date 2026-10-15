// Read-only preflight: report all malformed eligible source summaries at once.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/ump45nose/skysight-sync/internal/syncer"
)

func main() {
	if len(os.Args) != 2 {
		panic("usage: audit RESOURCES_DIRECTORY")
	}
	cutoff, _ := syncer.DayCutoff(time.Now())
	files, err := os.ReadDir(os.Args[1])
	if err != nil {
		panic(err)
	}
	bad := []map[string]string{}
	for _, f := range files {
		t, d, err := syncer.ParseName(f.Name())
		if err != nil || t.Add(d).After(cutoff) {
			continue
		}
		p := filepath.Join(os.Args[1], f.Name())
		b, err := os.ReadFile(p)
		if err == nil {
			_, err = syncer.Parse("audit", f.Name(), b)
		}
		if err != nil {
			bad = append(bad, map[string]string{"name": f.Name(), "error": err.Error()})
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(bad); err != nil {
		panic(err)
	}
}
