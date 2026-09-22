package api

import (
	"context"
	"encoding/json"
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

// fakeBarSource 固定行情源（不联网）：一段「突破后走一小段就回落」的图形，
// 小盈亏比能止盈、大盈亏比拿不到 → 扫描的不同组合会给出不同结果。
type fakeBarSource struct{}

func (f *fakeBarSource) Name() string { return "fake" }

func (f *fakeBarSource) Minute(_ context.Context, _ futures.Variety, _ string) ([]futures.Bar, error) {
	loc := futures.CSTZone()
	out := make([]futures.Bar, 0, 35)
	// 前一天：20 根窄幅（喂 ATR / Donchian）
	t0 := time.Date(2026, 9, 18, 9, 0, 0, 0, loc)
	for i := 0; i < 20; i++ {
		out = append(out, futures.Bar{
			Time: t0.Add(time.Duration(i) * 5 * time.Minute),
			Open: 90, High: 92, Low: 88, Close: 90, Volume: 10000,
		})
	}
	// 今天：6 根窄幅 → 突破根（收 112 > 昨高 100）→ 8 根跟随（冲 118 后回落到 113）
	t1 := time.Date(2026, 9, 21, 9, 0, 0, 0, loc)
	for i := 0; i < 6; i++ {
		out = append(out, futures.Bar{
			Time: t1.Add(time.Duration(i) * 5 * time.Minute),
			Open: 90, High: 92, Low: 88, Close: 90, Volume: 10000,
		})
	}
	out = append(out, futures.Bar{Time: t1.Add(30 * time.Minute), Open: 90, High: 113, Low: 88, Close: 112, Volume: 20000})
	for i := 1; i <= 8; i++ {
		h, c := 118.0, 114.0
		if i > 2 {
			h, c = 115, 113
		}
		out = append(out, futures.Bar{
			Time: t1.Add(time.Duration(30+i*5) * time.Minute),
			Open: 112, High: h, Low: 111, Close: c, Volume: 15000,
		})
	}
	return out, nil
}

func (f *fakeBarSource) Daily(_ context.Context, _ futures.Variety) ([]futures.Daily, error) {
	loc := futures.CSTZone()
	return []futures.Daily{{
		Date: time.Date(2026, 9, 18, 0, 0, 0, 0, loc),
		Open: 90, High: 100, Low: 80, Close: 90, Volume: 100000,
	}}, nil
}

func newSweepServer(t *testing.T) *Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return &Server{
		Store: st,
		Bars:  futuresync.NewStoredSource(st, &fakeBarSource{}),
	}
}

func TestFuturesSweepEndToEnd(t *testing.T) {
	srv := httptest.NewServer(newSweepServer(t).Handler()) // 顺带验证路由注册
	defer srv.Close()

	// 级别也作为轴：2 个级别 × (3×1×2×3=18) = 36 个组合
	body := `{"symbol":"RB0","periods":["5","15"],"donchian":[10,20,50],"vol_ratio":[1.5],` +
		`"hold_bars":[4,6],"rr":[0.5,1.5,3],"objective":"avg_return","min_trades":1}`
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/futures/sweep", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 %d", resp.StatusCode)
	}

	var env struct {
		OK   bool                `json:"ok"`
		Data futures.SweepResult `json:"data"`
		Err  string              `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	if !env.OK {
		t.Fatalf("响应不是成功：%s", env.Err)
	}
	if env.Data.Combos != 2*3*1*2*3 || len(env.Data.Rows) != 36 {
		t.Fatalf("组合数不对：combos=%d rows=%d", env.Data.Combos, len(env.Data.Rows))
	}
	if len(env.Data.Periods) != 2 || env.Data.PeriodBars["5"] == 0 || env.Data.PeriodBars["15"] == 0 {
		t.Fatalf("要回带级别与各级别根数：%v %v", env.Data.Periods, env.Data.PeriodBars)
	}
	byPeriod := map[string]int{}
	for _, r := range env.Data.Rows {
		byPeriod[r.Params.Period]++
	}
	if byPeriod["5"] != 18 || byPeriod["15"] != 18 {
		t.Fatalf("两个级别各占一半：%v", byPeriod)
	}
	if env.Data.Objective != "avg_return" || env.Data.Best == nil {
		t.Fatalf("结果不完整：%+v", env.Data)
	}
	if env.Data.Best.Params.RR != env.Data.Rows[0].Params.RR {
		t.Fatalf("best 应是第一行：%+v", env.Data.Best.Params)
	}
	for i := 1; i < len(env.Data.Rows); i++ {
		if env.Data.Rows[i].AvgReturn > env.Data.Rows[i-1].AvgReturn+1e-12 {
			t.Fatalf("第 %d 行排序不对", i)
		}
	}
	// 轴真的生效：18 个组合不该只有一种结果
	distinct := map[float64]bool{}
	for _, r := range env.Data.Rows {
		distinct[r.AvgReturn] = true
	}
	if len(distinct) < 2 {
		t.Fatalf("盈亏比/持有轴没生效：%+v", env.Data.Rows[0])
	}
}
