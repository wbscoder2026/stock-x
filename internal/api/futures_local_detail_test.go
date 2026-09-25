package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wbscoder2026/stock-x/internal/futuresync"
	"github.com/wbscoder2026/stock-x/internal/store"
)

func newLocalServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{Store: st}
	t.Cleanup(func() { _ = st.Close() })
	return srv, st
}

func TestFuturesLocalDetailReturnsSyncedDays(t *testing.T) {
	srv, st := newLocalServer(t)
	cst := time.FixedZone("CST", 8*3600)
	row := func(period string, d, h int) store.FuturesBar {
		return store.FuturesBar{
			Symbol: "PS0", Period: period,
			Time: time.Date(2026, 9, d, h, 0, 0, 0, cst).UTC(),
			Open: 1, High: 2, Low: 0.5, Close: 1.5, Volume: 10, Hold: 100,
		}
	}
	if _, err := st.UpsertFuturesBars([]store.FuturesBar{
		row("1", 21, 9), row("1", 22, 9), row("5", 22, 9),
	}); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	srv.futuresLocalDetail(rec, httptest.NewRequest(http.MethodGet, "/api/futures/local/detail?prefix=PS", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("应 200，实际 %d %s", rec.Code, rec.Body.String())
	}
	var env struct {
		OK   bool                     `json:"ok"`
		Data futuresync.VarietyDetail `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if !env.OK || !env.Data.MinuteSynced {
		t.Fatalf("应标记 1 分钟已同步：%+v", env.Data)
	}
	if env.Data.MinuteDays != 2 {
		t.Fatalf("1 分钟天数应为 2：%+v", env.Data)
	}
	var minute *futuresync.PeriodDetail
	for i := range env.Data.Periods {
		if env.Data.Periods[i].Period == "1" {
			minute = &env.Data.Periods[i]
		}
	}
	if minute == nil || len(minute.Days) != 2 {
		t.Fatalf("应列出每一天：%+v", env.Data.Periods)
	}
}

func TestFuturesLocalDetailRejectsBadInput(t *testing.T) {
	srv, _ := newLocalServer(t)
	// 缺品种
	rec := httptest.NewRecorder()
	srv.futuresLocalDetail(rec, httptest.NewRequest(http.MethodGet, "/api/futures/local/detail", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("缺 prefix 应 400：%d", rec.Code)
	}
	// 未知品种
	rec = httptest.NewRecorder()
	srv.futuresLocalDetail(rec, httptest.NewRequest(http.MethodGet, "/api/futures/local/detail?prefix=NOPE", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("未知品种应 400：%d", rec.Code)
	}
}

// 走真实路由（注册写错 handler 也会漏）
func TestFuturesLocalDetailRouteRegistered(t *testing.T) {
	srv, st := newLocalServer(t)
	cst := time.FixedZone("CST", 8*3600)
	if _, err := st.UpsertFuturesBars([]store.FuturesBar{{
		Symbol: "JM0", Period: "1",
		Time: time.Date(2026, 9, 22, 9, 0, 0, 0, cst).UTC(),
		Open: 1, High: 2, Low: 0.5, Close: 1.5, Volume: 10, Hold: 100,
	}}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/futures/local/detail?prefix=JM", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("路由未注册：%d %s", rec.Code, rec.Body.String())
	}
}

func TestFuturesLocalBackfillAcceptsContractSymbol(t *testing.T) {
	srv, _ := newLocalServer(t)
	srv.Backfill = futuresync.NewBackfiller(srv.Store, nil, nil)

	// 指定月份合约 → 收下
	rec := httptest.NewRecorder()
	srv.futuresLocalBackfill(rec, httptest.NewRequest(http.MethodPost, "/api/futures/local/backfill",
		strings.NewReader(`{"prefix":"JM","symbol":"JM2601","from":"2026-09-01","to":"2026-09-22"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("合约补全应 200：%d %s", rec.Code, rec.Body.String())
	}

	// 代码不属于该品种 → 400（不能把 RB 的数据补到 JM 名下）
	rec = httptest.NewRecorder()
	srv.futuresLocalBackfill(rec, httptest.NewRequest(http.MethodPost, "/api/futures/local/backfill",
		strings.NewReader(`{"prefix":"JM","symbol":"RB2601"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("不匹配的合约应 400：%d %s", rec.Code, rec.Body.String())
	}

	// 乱填也要拦住
	rec = httptest.NewRecorder()
	srv.futuresLocalBackfill(rec, httptest.NewRequest(http.MethodPost, "/api/futures/local/backfill",
		strings.NewReader(`{"prefix":"JM","symbol":"ABC"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法代码应 400：%d", rec.Code)
	}

	// 不填代码 → 补主连，照旧收下
	rec = httptest.NewRecorder()
	srv.futuresLocalBackfill(rec, httptest.NewRequest(http.MethodPost, "/api/futures/local/backfill",
		strings.NewReader(`{"prefix":"JM"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("主连补全应 200：%d %s", rec.Code, rec.Body.String())
	}
}

func TestFuturesLocalDetailBySymbolParam(t *testing.T) {
	srv, st := newLocalServer(t)
	cst := time.FixedZone("CST", 8*3600)
	row := func(symbol, period string, d int) store.FuturesBar {
		return store.FuturesBar{
			Symbol: symbol, Period: period,
			Time: time.Date(2026, 9, d, 9, 0, 0, 0, cst).UTC(),
			Open: 1, High: 2, Low: 0.5, Close: 1.5, Volume: 10, Hold: 100,
		}
	}
	if _, err := st.UpsertFuturesBars([]store.FuturesBar{
		row("JM2601", "1", 21), row("JM2601", "1", 22), row("JM0", "1", 22),
	}); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	srv.futuresLocalDetail(rec, httptest.NewRequest(http.MethodGet, "/api/futures/local/detail?prefix=JM&symbol=JM2601", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("应 200：%d %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data futuresync.VarietyDetail `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Data.Symbol != "JM2601" || env.Data.MinuteDays != 2 {
		t.Fatalf("应按合约代码返回：%+v", env.Data)
	}

	// 别的代码 → 400
	rec = httptest.NewRecorder()
	srv.futuresLocalDetail(rec, httptest.NewRequest(http.MethodGet, "/api/futures/local/detail?prefix=JM&symbol=RB2601", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("不匹配应 400：%d", rec.Code)
	}
}
