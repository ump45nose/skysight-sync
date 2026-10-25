package syncer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPresentationMigrationKeepsDailyRangeAndDeletedWindows(t *testing.T) {
	h := newHub()
	a, b := testClient(t, "macair", h), testClient(t, "macpro", h)
	add(t, a, "2026-10-13T00-00-00-OLD-6h-memory-summary.md", "已删除活动")
	add(t, b, "2026-10-13T06-00-00-ACT-6h-memory-summary.md", "需要更新的活动")
	bootstrap(t, a, b)
	s, _ := loadState(a.Root)
	deleted, active := "2026-10-13T00:00:00Z", "2026-10-13T06:00:00Z"
	old := s.Published[active].Filename
	if err := os.Remove(filepath.Join(a.Config.Resources(), s.Published[deleted].Filename)); err != nil {
		t.Fatal(err)
	}
	s.RenderVersion = renderVersion - 1
	if err := writeJSON(filepath.Join(a.Root, "state.json"), s); err != nil {
		t.Fatal(err)
	}
	today := "2026-10-15T00-00-00-TODAY-10min-memory-summary.md"
	add(t, a, today, "今天不提前接管")
	r, err := a.Run(context.Background(), false)
	if err != nil || r.AlreadySynced || len(r.Windows) != 1 || r.Windows[0] != active {
		t.Fatalf("migration: %+v %v", r, err)
	}
	after, _ := loadState(a.Root)
	if after.RenderVersion != renderVersion || after.Published[active].Filename == old || !after.Suppressed[deleted] {
		t.Fatal("migration state incorrect")
	}
	if _, err = os.Stat(filepath.Join(a.Config.Resources(), today)); err != nil {
		t.Fatal("today native source changed", err)
	}
	if len(after.Sources["macair"].Entries) != 1 {
		t.Fatal("daily range changed")
	}
	if r = runOK(t, a); !r.AlreadySynced {
		t.Fatal("daily guard no longer works")
	}
}
