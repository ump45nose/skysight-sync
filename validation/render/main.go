// This validation-only command uses the production renderer, with explicit real
// inputs. It never writes into resources or changes existing native summaries.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/ump45nose/skysight-sync/internal/syncer"
)

func main() {
	if len(os.Args) < 4 || len(os.Args)%2 != 0 {
		panic("usage: render ROOT DEVICE FILE [DEVICE FILE ...]")
	}
	root := os.Args[1]
	sources := map[string]syncer.Manifest{}
	for i := 2; i < len(os.Args); i += 2 {
		device, p := os.Args[i], os.Args[i+1]
		b, err := os.ReadFile(p)
		must(err)
		s, err := syncer.Parse(device, filepath.Base(p), b)
		must(err)
		m := sources[device]
		m.DeviceID = device
		if m.Entries == nil {
			m.Entries = map[string]syncer.Entry{}
		}
		m.Entries[s.Entry.Name] = s.Entry
		sources[device] = m
		archive := filepath.Join(root, "archive", device, "objects", s.Entry.Digest, s.Entry.Name)
		must(os.MkdirAll(filepath.Dir(archive), 0700))
		must(os.WriteFile(archive, b, 0600))
	}
	cutoff, _ := syncer.DayCutoff(time.Now())
	windows, err := syncer.Select(sources, cutoff)
	must(err)
	keys := []string{}
	for key := range windows {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	outputs := []map[string]string{}
	for _, key := range keys {
		b, err := syncer.Render(root, key, windows[key], os.ReadFile)
		must(err)
		t, err := time.Parse(time.RFC3339, key)
		must(err)
		name := t.Format("2006-01-02T15-04-05") + "-sync" + syncer.NewID() + "-6h-memory-summary.md"
		p := filepath.Join(root, "output", name)
		must(os.MkdirAll(filepath.Dir(p), 0700))
		must(os.WriteFile(p, b, 0600))
		outputs = append(outputs, map[string]string{"window": key, "file": p})
	}
	data, err := json.MarshalIndent(outputs, "", "  ")
	must(err)
	fmt.Println(string(data))
}
func must(err error) {
	if err != nil {
		panic(err)
	}
}
