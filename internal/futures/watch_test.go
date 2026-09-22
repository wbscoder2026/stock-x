package futures

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type rawBar struct {
	D, O, H, L, C, V string
}

func mkBar(ts time.Time, o, h, l, c float64, v int) rawBar {
	format := func(f float64) string { return strconv.FormatFloat(f, 'f', 1, 64) }
	return rawBar{
		D: ts.In(locCST).Format("2006-01-02 15:04:05"),
		O: format(o), H: format(h), L: format(l), C: format(c), V: strconv.Itoa(v),
	}
}

// quietBars 09:00 起 n 根 5 分钟窄幅 K 线（高 100 / 低 99）
func quietBars(n int) []rawBar {
	t0 := time.Date(2026, 1, 5, 9, 0, 0, 0, locCST)
	out := make([]rawBar, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, mkBar(t0.Add(time.Duration(i)*5*time.Minute), 99.5, 100, 99, 99.5, 100))
	}
	return out
}

// breakoutBar 收 102（> ORB高 100 + 1 缓冲）且放量
func breakoutBar(offsetBars int) rawBar {
	t0 := time.Date(2026, 1, 5, 9, 0, 0, 0, locCST)
	return mkBar(t0.Add(time.Duration(offsetBars)*5*time.Minute), 99.5, 102.5, 99.5, 102, 1000)
}

func minuteJSONP(rows []rawBar) string {
	parts := make([]string, 0, len(rows))
	for _, r := range rows {
		parts = append(parts, fmt.Sprintf(
			`{"d":"%s","o":"%s","h":"%s","l":"%s","c":"%s","v":"%s","p":"1000"}`,
			r.D, r.O, r.H, r.L, r.C, r.V,
		))
	}
	return "=([" + strings.Join(parts, ",") + "])"
}

// 前一日：高 200 / 低 50 / 收 100 → 枢轴位都不会被 102 触发，方便只观察 ORB
const dailyJSONP = `var _x=([{"d":"2026-01-02","o":"100","h":"200","l":"50","c":"100","v":"1000000","p":"1000","s":"100"},` +
	`{"d":"2026-01-05","o":"100","h":"105","l":"99","c":"102","v":"800000","p":"1200","s":"101"}])`

func stubClient(t *testing.T, payload func() (string, string)) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		minute, daily := payload()
		if r.URL.Path == "/d" {
			_, _ = w.Write([]byte(daily))
			return
		}
		_, _ = w.Write([]byte(minute))
	}))
	t.Cleanup(srv.Close)
	return &Client{MinuteURL: srv.URL + "/m", DailyURL: srv.URL + "/d"}
}

func parseBars(t *testing.T, minute, daily string) ([]Bar, []Daily) {
	t.Helper()
	bars, err := parseMinuteJSONP([]byte(minute))
	if err != nil {
		t.Fatalf("parse minute: %v", err)
	}
	days, err := parseDailyJSONP([]byte(daily))
	if err != nil {
		t.Fatalf("parse daily: %v", err)
	}
	return bars, days
}

func TestScanDayOrbOnlyForFiveMinute(t *testing.T) {
	minute := minuteJSONP(append(quietBars(11), breakoutBar(11)))
	bars, daily := parseBars(t, minute, dailyJSONP)
	day := time.Date(2026, 1, 5, 0, 0, 0, 0, locCST)

	ev5 := ScanDay(bars, daily, day, Params{Period: "5"})
	if len(ev5) != 1 {
		t.Fatalf("5 分钟应只报 ORB 突破，实际 %+v", ev5)
	}
	if ev5[0].Direction != DirUp || !strings.Contains(ev5[0].Level, "ORB高") {
		t.Fatalf("事件不对 %+v", ev5[0])
	}
	if ev5[0].Close != 102 {
		t.Fatalf("close=%v", ev5[0].Close)
	}

	ev15 := ScanDay(bars, daily, day, Params{Period: "15"})
	if len(ev15) != 0 {
		t.Fatalf("15 分钟不该带 ORB，实际 %+v", ev15)
	}
}

func TestScanDayQuietDayHasNoEvent(t *testing.T) {
	bars, daily := parseBars(t, minuteJSONP(quietBars(11)), dailyJSONP)
	day := time.Date(2026, 1, 5, 0, 0, 0, 0, locCST)
	if ev := ScanDay(bars, daily, day, Params{Period: "5"}); len(ev) != 0 {
		t.Fatalf("横盘不该有事件 %+v", ev)
	}
}

func TestCollectNewDedupAndFresh(t *testing.T) {
	seen := map[string]int64{}
	v := Variety{Name: "焦煤", Prefix: "JM"}
	t1 := time.Date(2026, 1, 5, 9, 50, 0, 0, locCST)
	t2 := t1.Add(5 * time.Minute)
	first := []Event{{Time: t1, Direction: DirUp, Level: "ORB高(开盘30分钟)", Close: 102, LevelPrice: 100, Volume: 1000}}

	got := collectNew(seen, first, time.Time{}, "2026-01-05", v)
	if len(got) != 1 {
		t.Fatalf("首轮应报 1 条，实际 %+v", got)
	}
	if got[0].Fresh {
		t.Fatal("启动时已有的事件不应标记 Fresh")
	}
	if got[0].Symbol != "JM0" || got[0].Name != "焦煤" || got[0].Time != "2026-01-05 09:50" {
		t.Fatalf("事件字段不对 %+v", got[0])
	}

	if again := collectNew(seen, first, t1, "2026-01-05", v); len(again) != 0 {
		t.Fatalf("同一根 K 线不该重复提醒 %+v", again)
	}

	next := []Event{{Time: t2, Direction: DirUp, Level: "ORB高(开盘30分钟)", Close: 103, LevelPrice: 100, Volume: 1200}}
	fresh := collectNew(seen, next, t1, "2026-01-05", v)
	if len(fresh) != 1 || !fresh[0].Fresh {
		t.Fatalf("新 K 线上的突破应标记 Fresh：%+v", fresh)
	}
}

func TestWatchUniverseFilter(t *testing.T) {
	all, err := watchUniverse(nil)
	if err != nil || len(all) != len(ListVarieties()) {
		t.Fatalf("空过滤应返回全市场：%d %v", len(all), err)
	}
	sub, err := watchUniverse([]string{"jm", "RB"})
	if err != nil || len(sub) != 2 || sub[0].Prefix != "JM" || sub[1].Prefix != "RB" {
		t.Fatalf("过滤结果不对 %+v %v", sub, err)
	}
	if _, err := watchUniverse([]string{"XX"}); err == nil {
		t.Fatal("未知品种应报错")
	}
}

func TestClampInterval(t *testing.T) {
	cases := map[int]int{0: WatchDefaultInterval, -3: WatchDefaultInterval, 1: WatchMinInterval, 30: 30, 99999: WatchMaxInterval}
	for in, want := range cases {
		if got := clampInterval(in); got != want {
			t.Fatalf("clampInterval(%d)=%d want %d", in, got, want)
		}
	}
}

func TestFailStreakAndBackoff(t *testing.T) {
	cases := []struct {
		streak, scanned, failures, want int
	}{
		{0, 0, 67, 1}, // 全失败（限流 456）→ 退避 1 档
		{1, 0, 67, 2},
		{watchMaxBackoff, 0, 67, watchMaxBackoff}, // 封顶
		{3, 60, 7, 0}, // 恢复 → 清零
		{2, 0, 0, 2},  // 没数据不动
		{1, 5, 5, 0},  // 失败率 = 50% → 不算大面积失败
		{1, 10, 5, 0},
	}
	for _, c := range cases {
		if got := nextFailStreak(c.streak, c.scanned, c.failures); got != c.want {
			t.Fatalf("nextFailStreak(%d,%d,%d)=%d want %d", c.streak, c.scanned, c.failures, got, c.want)
		}
	}
	if backoffFactor(0) != 1 || backoffFactor(2) != 3 || backoffFactor(watchMaxBackoff+9) != watchMaxBackoff+1 {
		t.Fatalf("backoffFactor 不对：%d %d %d", backoffFactor(0), backoffFactor(2), backoffFactor(watchMaxBackoff+9))
	}
}

func waitTicks(t *testing.T, w *Watcher, n int) WatchStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if st := w.Status(); st.Ticks >= n {
			return st
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待第 %d 轮超时：%+v", n, w.Status())
	return WatchStatus{}
}

func TestWatcherStartTickAndAlerts(t *testing.T) {
	var phase atomic.Value
	phase.Store("quiet")
	quiet := quietBars(11)
	payload := func() (string, string) {
		if phase.Load() == "break" {
			return minuteJSONP(append(append([]rawBar{}, quiet...), breakoutBar(11))), dailyJSONP
		}
		return minuteJSONP(quiet), dailyJSONP
	}

	w := NewWatcher(stubClient(t, payload))
	st, err := w.Start(WatchConfig{Params: Params{Period: "5"}, Interval: WatchMaxInterval, Prefixes: []string{"JM"}})
	if err != nil {
		t.Fatal(err)
	}
	if !st.Running || st.Varieties != 1 {
		t.Fatalf("启动状态不对 %+v", st)
	}

	st = waitTicks(t, w, 1)
	if st.Scanned != 1 || st.Failures != 0 {
		t.Fatalf("首轮扫描统计不对 %+v", st)
	}
	if events := w.Events(0); len(events) != 0 {
		t.Fatalf("横盘不该有提醒 %+v", events)
	}

	phase.Store("break")
	w.tick()
	events := w.Events(0)
	if len(events) != 1 {
		t.Fatalf("应有 1 条及时提醒，实际 %+v", events)
	}
	e := events[0]
	if !e.Fresh || e.Seq != 1 || e.Symbol != "JM0" || e.Prefix != "JM" || e.Name != "焦煤" {
		t.Fatalf("提醒字段不对 %+v", e)
	}
	if e.Direction != DirUp || !strings.Contains(e.Level, "ORB高(开盘30分钟)") {
		t.Fatalf("提醒内容不对 %+v", e)
	}
	if e.Time != "2026-01-05 09:55" {
		t.Fatalf("提醒时间不对 %s", e.Time)
	}

	w.tick()
	if again := w.Events(0); len(again) != 1 {
		t.Fatalf("重复轮次不该再提醒 %+v", again)
	}
	if after := w.Events(e.Seq); len(after) != 0 {
		t.Fatalf("游标之后不该有内容 %+v", after)
	}

	if stopped := w.Stop(); stopped.Running {
		t.Fatal("Stop 后应停止")
	}
}

func TestWatcherConfigHotUpdate(t *testing.T) {
	w := NewWatcher(stubClient(t, func() (string, string) {
		return minuteJSONP(quietBars(11)), dailyJSONP
	}))

	st, err := w.Start(WatchConfig{
		Params:   Params{Period: "15", ORB: 20, ATRPeriod: 7, ATRK: 0.5, VolRatio: 2},
		Interval: 10,
		Prefixes: []string{"JM", "RB"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if st.Config.Params.ORB != 20 || st.Config.Params.ATRPeriod != 7 || st.Config.Interval != 10 || st.Varieties != 2 {
		t.Fatalf("首配置不对 %+v", st.Config)
	}
	if st.Config.Params.HoldBars != 6 { // 默认值补齐
		t.Fatalf("默认值没补齐 %+v", st.Config.Params)
	}

	st, err = w.Start(WatchConfig{Params: Params{Period: "5"}, Interval: 1, Prefixes: []string{"JM"}})
	if err != nil {
		t.Fatal(err)
	}
	if st.Config.Params.Period != "5" || st.Varieties != 1 || st.Config.Interval != WatchMinInterval {
		t.Fatalf("热更新不对 %+v", st.Config)
	}
	if !st.Running {
		t.Fatal("热更新不应中断运行")
	}
	w.Stop()
}

func TestWatcherStartRejectsUnknownPrefix(t *testing.T) {
	w := NewWatcher(stubClient(t, func() (string, string) {
		return minuteJSONP(quietBars(11)), dailyJSONP
	}))
	if _, err := w.Start(WatchConfig{Prefixes: []string{"JM", "XX"}}); err == nil {
		t.Fatal("未知品种应报错")
	}
	if w.Status().Running {
		t.Fatal("出错不应启动")
	}
}
