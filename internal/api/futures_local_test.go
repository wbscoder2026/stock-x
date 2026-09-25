package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wbscoder2026/stock-x/internal/futures"
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

// 主连和月份合约都要能补：直接给合约代码时，两个标的一起排队（进度按整批算）。
func TestFuturesLocalBackfillTakesSymbols(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cache := futuresync.NewBarCache()
	bf := futuresync.NewBackfiller(st, cache, nil)
	// 解析器传 nil：只走缓存，测试不碰网络
	s := &Server{
		Store:     st,
		Bars:      &futuresync.StoredSource{Store: st, Cache: cache},
		Backfill:  bf,
		Contracts: futures.NewContractCache(nil, time.Minute),
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/futures/local/backfill",
		strings.NewReader(`{"symbols":["JM0","JM2701"]}`))
	s.futuresLocalBackfill(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("按合约代码补全应 200：%d %s", rec.Code, rec.Body.String())
	}
	if stt := bf.Status(); stt.Total != 2 || stt.Queued != 2 {
		t.Fatalf("主连+月份应排 2 个：total=%d queued=%d", stt.Total, stt.Queued)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/futures/local/backfill",
		strings.NewReader(`{"symbol":"NOPE99"}`))
	s.futuresLocalBackfill(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("未知合约应 400：%d %s", rec.Code, rec.Body.String())
	}

	// 只给品种：月份合约还没解析到时也不能报错，至少把主连排上
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/futures/local/backfill",
		strings.NewReader(`{"prefix":"RB","kinds":["main","months"],"from":"2026-01-01","to":"2026-09-01"}`))
	s.futuresLocalBackfill(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("只给品种应 200：%d %s", rec.Code, rec.Body.String())
	}
}

// 品种缺好几个月份时（2611/2612…），一次要把这些月份全排上队。
func TestFuturesLocalBackfillEnqueuesEveryMonth(t *testing.T) {
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
		Contracts: futures.NewContractCache(&stubLister{by: map[string][]futures.Contract{
			"JM": {
				{Symbol: "JM2611", Label: "2611", Kind: "month", Position: 500},
				{Symbol: "JM2612", Label: "2612", Kind: "month", Position: 1200},
				{Symbol: "JM2701", Label: "2701", Kind: "month", Position: 88000},
			},
		}}, time.Minute),
	}
	s.Contracts.Refresh(context.Background(), futures.ListVarieties())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/futures/local/backfill",
		strings.NewReader(`{"prefix":"JM","kinds":["main","months"]}`))
	s.futuresLocalBackfill(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("按品种补全应 200：%d %s", rec.Code, rec.Body.String())
	}
	// 主连 1 个 + 3 个月份
	if stt := bf.Status(); stt.Total != 4 {
		t.Fatalf("主连 + 全部月份应排 4 个：total=%d", stt.Total)
	}

	// 覆盖表里这些月份都要有行，否则页面上看不到、也没法单独补
	rec = httptest.NewRecorder()
	s.futuresLocal(rec, httptest.NewRequest(http.MethodGet, "/api/futures/local", nil))
	body := rec.Body.String()
	for _, sym := range []string{"JM0", "JM2611", "JM2612", "JM2701"} {
		if !strings.Contains(body, sym) {
			t.Fatalf("覆盖表应列出 %s", sym)
		}
	}
}

// stubLister 固定返回某品种的全部月份合约（测试不碰网络）。
type stubLister struct {
	by map[string][]futures.Contract
}

func (l *stubLister) List(_ context.Context, v futures.Variety) ([]futures.Contract, error) {
	return l.by[v.Prefix], nil
}

func TestFuturesLocalProgress(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	bf := futuresync.NewBackfiller(st, futuresync.NewBarCache(), nil)
	s := &Server{Store: st, Backfill: bf}

	rec := httptest.NewRecorder()
	s.futuresLocalProgress(rec, httptest.NewRequest(http.MethodGet, "/api/futures/local/progress", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"percent"`) {
		t.Fatalf("进度接口应带 percent：%d %s", rec.Code, rec.Body.String())
	}
}
