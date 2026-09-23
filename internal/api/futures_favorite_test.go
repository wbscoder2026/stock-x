package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wbscoder2026/stock-x/internal/futures"
	"github.com/wbscoder2026/stock-x/internal/futuresync"
	"github.com/wbscoder2026/stock-x/internal/store"
)

func TestFuturesFavoritesCRUDAndScan(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s := &Server{Store: st, Bars: futuresync.NewStoredSource(st, emptyBars{})}
	s.Bars.Warm = false

	rec := httptest.NewRecorder()
	s.futuresFavoritesCreate(rec, httptest.NewRequest(http.MethodPost, "/api/futures/favorites", strings.NewReader(`{"items":[]}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("空列表应 400：%d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/futures/favorites", strings.NewReader(`{"items":[{"name":"焦煤日内","note":"试","params":{"period":"15","rr":1.5},"origin_symbol":"JM0","origin_win_rate":0.6,"origin_trades":12}]}`))
	s.futuresFavoritesCreate(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "焦煤日内") {
		t.Fatalf("创建失败：%d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	s.futuresFavoritesList(rec, httptest.NewRequest(http.MethodGet, "/api/futures/favorites", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"origin_symbol":"JM0"`) {
		t.Fatalf("列表失败：%d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	upd := httptest.NewRequest(http.MethodPut, "/api/futures/favorites/1", strings.NewReader(`{"name":"改名","params":{"period":"5","rr":2}}`))
	upd.SetPathValue("id", "1")
	s.futuresFavoritesUpdate(rec, upd)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "改名") || !strings.Contains(rec.Body.String(), "JM0") {
		t.Fatalf("更新应保留来源：%d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	scan := httptest.NewRequest(http.MethodPost, "/api/futures/favorites/scan", strings.NewReader(`{"ids":[1],"workers":4}`))
	s.futuresFavoritesScan(rec, scan)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"configs"`) || !strings.Contains(rec.Body.String(), `"overall"`) {
		t.Fatalf("扫描失败：%d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	missing := httptest.NewRequest(http.MethodPost, "/api/futures/favorites/scan", strings.NewReader(`{"ids":[99]}`))
	s.futuresFavoritesScan(rec, missing)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("不存在的收藏应 400：%d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	del := httptest.NewRequest(http.MethodDelete, "/api/futures/favorites/1", nil)
	del.SetPathValue("id", "1")
	s.futuresFavoritesDelete(rec, del)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"removed":1`) {
		t.Fatalf("删除失败：%d %s", rec.Code, rec.Body.String())
	}
}

type emptyBars struct{}

func (emptyBars) Name() string { return "empty" }
func (emptyBars) Minute(context.Context, futures.Variety, string) ([]futures.Bar, error) {
	return nil, nil
}
func (emptyBars) Daily(context.Context, futures.Variety) ([]futures.Daily, error) {
	return nil, nil
}
