package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wbscoder2026/stock-x/internal/futuresync"
	"github.com/wbscoder2026/stock-x/internal/store"
)

func TestFuturesLocalListsAndPauses(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cache := futuresync.NewBarCache()
	bf := futuresync.NewBackfiller(st, cache, nil)
	s := &Server{
		Store:    st,
		Bars:     &futuresync.StoredSource{Store: st, Cache: cache},
		Backfill: bf,
	}

	rec := httptest.NewRecorder()
	s.futuresLocalPause(rec, httptest.NewRequest(http.MethodPost, "/api/futures/local/pause", nil))
	if rec.Code != http.StatusOK || !bf.Status().Paused {
		t.Fatalf("暂停失败：%d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	s.futuresLocal(rec, httptest.NewRequest(http.MethodGet, "/api/futures/local", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "焦煤") {
		t.Fatalf("列表里应有品种：%d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/futures/local/backfill", strings.NewReader(`{"prefix":"NOPE"}`))
	s.futuresLocalBackfill(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("未知品种应 400：%d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	s.futuresLocalResume(rec, httptest.NewRequest(http.MethodPost, "/api/futures/local/resume", nil))
	if rec.Code != http.StatusOK || bf.Status().Paused {
		t.Fatalf("继续失败：%d paused=%v", rec.Code, bf.Status().Paused)
	}
}
