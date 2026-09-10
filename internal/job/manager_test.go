package job

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/wbscoder2026/stock-x/internal/config"
	"github.com/wbscoder2026/stock-x/internal/store"
)

func TestJobConflicts(t *testing.T) {
	if jobConflicts("scan", "backfill") || jobConflicts("backfill", "scan") {
		t.Fatal("scan 应可与 backfill 并行")
	}
	if !jobConflicts("backfill", "backfill") {
		t.Fatal("同类应互斥")
	}
	if !jobConflicts("backfill", "sync") || !jobConflicts("sync", "backfill") {
		t.Fatal("backfill 与 sync 应互斥")
	}
}

func TestSubmitScanDuringBackfill(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	m := New(st, nil, config.Config{})
	hold := make(chan struct{})
	started := make(chan string, 4)
	m.beforeRun = func(ctx context.Context, typ string) {
		started <- typ
		if typ == "backfill" {
			select {
			case <-hold:
			case <-ctx.Done():
			}
		}
	}

	bf, err := m.Submit(context.Background(), "backfill")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case typ := <-started:
		if typ != "backfill" {
			t.Fatalf("got %s", typ)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("backfill 未启动")
	}

	sc, err := m.Submit(context.Background(), "scan")
	if err != nil {
		t.Fatalf("scan 应可与 backfill 并行: %v", err)
	}
	if _, err := m.Submit(context.Background(), "backfill"); err == nil {
		t.Fatal("第二个 backfill 应被拒绝")
	}
	if _, err := m.Submit(context.Background(), "sync"); err == nil {
		t.Fatal("sync 应与 backfill 互斥")
	}

	close(hold)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		j, err := st.GetJob(bf.ID)
		if err == nil && (j.Status == "failed" || j.Status == "success" || j.Status == "paused") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = sc
}
