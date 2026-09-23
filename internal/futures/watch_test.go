package futures

import (
	"context"
	"fmt"
	"math"
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

// testClock 测试里的「现在」：固定在 2026-01-05 10:00（日盘内、晚于最后一根 09:55）。
// 提醒有时效（AlertTTL=30min），把时钟钉住后测试就不会随真实钟点飘
// （之前用「按当前时间平移 K 线」的写法，平移后 ORB 窗口取不到 6 根，会随时段随机红）。
var testClock = time.Date(2026, 1, 5, 10, 0, 0, 0, locCST)

// pinClock 把 watcher 的时钟钉住（默认 time.Now）。
func pinClock(w *Watcher, at time.Time) { w.now = func() time.Time { return at } }

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

	got := collectNew(seen, first, time.Time{}, "2026-01-05", v, Params{StopATR: 1, RR: DefaultRR})
	if len(got) != 1 {
		t.Fatalf("首轮应报 1 条，实际 %+v", got)
	}
	if got[0].Fresh {
		t.Fatal("启动时已有的事件不应标记 Fresh")
	}
	if got[0].Symbol != "JM0" || got[0].Name != "焦煤" || got[0].Time != "2026-01-05 09:50" {
		t.Fatalf("事件字段不对 %+v", got[0])
	}

	if again := collectNew(seen, first, t1, "2026-01-05", v, Params{StopATR: 1, RR: DefaultRR}); len(again) != 0 {
		t.Fatalf("同一根 K 线不该重复提醒 %+v", again)
	}

	next := []Event{{Time: t2, Direction: DirUp, Level: "ORB高(开盘30分钟)", Close: 103, LevelPrice: 100, Volume: 1200}}
	fresh := collectNew(seen, next, t1, "2026-01-05", v, Params{StopATR: 1, RR: DefaultRR})
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
	full := append(append([]rawBar{}, quietBars(11)...), breakoutBar(11))
	quiet := quietBars(11)
	breakRows := full
	payload := func() (string, string) {
		if phase.Load() == "break" {
			return minuteJSONP(breakRows), dailyJSONP
		}
		return minuteJSONP(quiet), dailyJSONP
	}

	w := NewWatcher(stubClient(t, payload))
	pinClock(w, testClock)
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
	if wantMS := time.Date(2026, 1, 5, 9, 55, 0, 0, locCST).UnixMilli(); e.TimeMS != wantMS {
		t.Fatalf("time_ms 不对 %d（期望 %d）", e.TimeMS, wantMS)
	}
	// 提醒要带推荐止损/止盈：向上突破 → 止损在现价下方、止盈在上方
	if e.StopPrice <= 0 || e.TPPrice <= 0 || e.RR != DefaultRR || e.TickSize <= 0 {
		t.Fatalf("提醒应带推荐止损/止盈：%+v", e)
	}
	if !(e.StopPrice < e.Close && e.TPPrice > e.Close) {
		t.Fatalf("推荐价方向不对：%+v", e)
	}
	// 两个价都得落在该品种最小变动价位上（不然挂不出去）
	for _, p := range []float64{e.StopPrice, e.TPPrice} {
		if math.Abs(p/e.TickSize-math.Round(p/e.TickSize)) > 1e-6 {
			t.Fatalf("推荐价没对齐 %v：stop=%v tp=%v", e.TickSize, e.StopPrice, e.TPPrice)
		}
	}
	// 取整只允许造成「1 个跳」量级的偏差（两个价各自 ≤ 半个跳）
	risk, reward := e.Close-e.StopPrice, e.TPPrice-e.Close
	if math.Abs(reward-DefaultRR*risk) > e.TickSize*(1+DefaultRR) {
		t.Fatalf("取整偏差超出一个跳：risk=%v reward=%v tick=%v", risk, reward, e.TickSize)
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

func barsOf(rows []rawBar) []Bar {
	bars, err := parseMinuteJSONP([]byte(minuteJSONP(rows)))
	if err != nil {
		panic(err)
	}
	return bars
}

func daysOf() []Daily {
	days, err := parseDailyJSONP([]byte(dailyJSONP))
	if err != nil {
		panic(err)
	}
	return days
}

func TestWatcherAlertsOnlyFreshEvents(t *testing.T) {
	full := append(append([]rawBar{}, quietBars(11)...), breakoutBar(11))
	quiet := barsOf(quietBars(11))
	withBreak := barsOf(full)
	src := &fakeSource{name: "s", minute: quiet, daily: daysOf()}

	w := NewWatcherSources(src)
	pinClock(w, testClock)
	got := make(chan []WatchEvent, 4)
	var gotCfg WatchConfig
	w.OnEvents = func(cfg WatchConfig, events []WatchEvent) {
		gotCfg = cfg
		got <- events
	}
	if _, err := w.Start(WatchConfig{
		Params:   Params{Period: "5"},
		Prefixes: []string{"JM"},
		Interval: WatchMaxInterval,
		Alert:    AlertConfig{Feishu: true, Desktop: true},
	}); err != nil {
		t.Fatal(err)
	}
	waitTicks(t, w, 1)
	select {
	case evs := <-got:
		t.Fatalf("横盘不该外发：%+v", evs)
	default:
	}

	// 新 K 线上出现突破 → 外发一次，并带上开关配置
	src.minute = withBreak
	w.tick()
	select {
	case evs := <-got:
		if len(evs) != 1 || !evs[0].Fresh || evs[0].Prefix != "JM" {
			t.Fatalf("外发内容不对：%+v", evs)
		}
		if !gotCfg.Alert.Feishu || !gotCfg.Alert.Desktop {
			t.Fatalf("配置没传给回调：%+v", gotCfg.Alert)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("突破没有触发外发回调")
	}

	// 同一根 K 线不重复外发
	w.tick()
	select {
	case evs := <-got:
		t.Fatalf("重复外发：%+v", evs)
	default:
	}

	w.SetAlertNote("飞书已推送 1 条")
	if note := w.Status().AlertNote; !strings.Contains(note, "飞书已推送") {
		t.Fatalf("alert_note 没进状态：%s", note)
	}
	w.Stop()
}

type fakeResolver struct {
	symbol, label string
	calls         int32
}

func (f *fakeResolver) Resolve(_ context.Context, _ Variety) (string, string, error) {
	atomic.AddInt32(&f.calls, 1)
	return f.symbol, f.label, nil
}

func TestWatcherFillsMainContractAsync(t *testing.T) {
	src := &fakeSource{
		name:   "s",
		minute: breakoutBars(),
		daily:  daysOf(),
	}
	w := NewWatcherSources(src)
	pinClock(w, testClock)
	resolver := &fakeResolver{symbol: "JM2701", label: "2701"}
	w.SetContractResolver(resolver)
	if _, err := w.Start(WatchConfig{
		Params: Params{Period: "5"}, Prefixes: []string{"JM"}, Interval: WatchMaxInterval,
	}); err != nil {
		t.Fatal(err)
	}
	waitTicks(t, w, 1)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		events := w.Events(0)
		if len(events) == 1 && events[0].Contract == "JM2701" && events[0].ContractLabel == "2701" {
			if got := atomic.LoadInt32(&resolver.calls); got != 1 {
				t.Fatalf("同一品种只该解析一次：%d", got)
			}
			w.Stop()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("主力月份合约没回填：%+v", w.Events(0))
}

func TestWatcherWithoutResolverHasNoContract(t *testing.T) {
	src := &fakeSource{
		name:   "s",
		minute: breakoutBars(),
		daily:  daysOf(),
	}
	w := NewWatcherSources(src) // 不注入解析器
	pinClock(w, testClock)
	if _, err := w.Start(WatchConfig{
		Params: Params{Period: "5"}, Prefixes: []string{"JM"}, Interval: WatchMaxInterval,
	}); err != nil {
		t.Fatal(err)
	}
	waitTicks(t, w, 1)
	if events := w.Events(0); len(events) != 1 || events[0].Contract != "" {
		t.Fatalf("不该有月份：%+v", events)
	}
	w.Stop()
}

// breakoutBars 固定 K 线：09:00~09:50 窄幅 + 09:55 突破（收 102 > ORB高 100 + 缓冲）。
func breakoutBars() []Bar {
	return barsOf(append(append([]rawBar{}, quietBars(11)...), breakoutBar(11)))
}

func TestExpiredAlertsArePruned(t *testing.T) {
	// 固定日期的 K 线（早于 AlertTTL）：事件照样被去重记录，但不进列表、不外发
	src := &fakeSource{
		name:   "s",
		minute: barsOf(append(append([]rawBar{}, quietBars(11)...), breakoutBar(11))),
		daily:  daysOf(),
	}
	w := NewWatcherSources(src)
	pinClock(w, testClock.Add(24*time.Hour)) // 时钟拨到第二天：固定 K 线已过期
	seen := make(chan []WatchEvent, 2)
	w.OnEvents = func(_ WatchConfig, evs []WatchEvent) { seen <- evs }

	if _, err := w.Start(WatchConfig{
		Params: Params{Period: "5"}, Prefixes: []string{"JM"}, Interval: WatchMaxInterval,
	}); err != nil {
		t.Fatal(err)
	}
	waitTicks(t, w, 1)
	w.tick()

	if events := w.Events(0); len(events) != 0 {
		t.Fatalf("超过 AlertTTL 的提醒不该出现在列表：%+v", events)
	}
	st := w.Status()
	if st.Events != 0 {
		t.Fatalf("状态里的条数也应按时效算：%d", st.Events)
	}
	if st.AlertTTLSec != int(AlertTTL/time.Second) {
		t.Fatalf("alert_ttl_sec 不对：%d", st.AlertTTLSec)
	}
	select {
	case evs := <-seen:
		t.Fatalf("过期提醒不该外发：%+v", evs)
	default:
	}
	w.Stop()
}

func TestPruneExpiredKeepsFreshOnly(t *testing.T) {
	w := NewWatcherSources(&fakeSource{name: "s"})
	now := time.Now()
	w.events = []WatchEvent{
		{Seq: 1, Prefix: "JM", TimeMS: now.Add(-AlertTTL - time.Minute).UnixMilli()},
		{Seq: 2, Prefix: "JM", TimeMS: now.Add(-time.Minute).UnixMilli()},
	}

	got := w.Events(0)
	if len(got) != 1 || got[0].Seq != 2 {
		t.Fatalf("只该留时效内的那条：%+v", got)
	}
	if len(w.events) != 1 {
		t.Fatalf("缓冲区也应清掉过期的：%+v", w.events)
	}
	if after := w.Events(2); len(after) != 0 {
		t.Fatalf("游标之后不该有内容：%+v", after)
	}
}

func TestFilterBlacklisted(t *testing.T) {
	events := []Event{{
		Time:      time.Date(2026, 9, 22, 9, 50, 0, 0, locCST),
		Direction: DirUp, Level: "ORB高(开盘30分钟)", Close: 102, LevelPrice: 100,
	}}
	if got := filterBlacklisted(events, "JM", "JM2701", Blacklist{}); len(got) != 1 {
		t.Fatal("空黑名单不该过滤")
	}
	byVariety := Blacklist{Varieties: map[string]bool{"JM": true}}
	if got := filterBlacklisted(events, "JM", "JM2701", byVariety); got != nil {
		t.Fatal("品种级黑名单应滤掉")
	}
	if got := filterBlacklisted(events, "RB", "RB2701", byVariety); len(got) != 1 {
		t.Fatal("不该误伤其它品种")
	}
	byContract := Blacklist{Contracts: map[string]bool{"JM2701": true}}
	if got := filterBlacklisted(events, "JM", "JM2701", byContract); got != nil {
		t.Fatal("合约级黑名单应滤掉")
	}
	if got := filterBlacklisted(events, "JM", "JM2705", byContract); len(got) != 1 {
		t.Fatal("换月到别的合约就该放行")
	}
	if got := filterBlacklisted(events, "JM", "", byContract); len(got) != 1 {
		t.Fatal("主力合约还没解析出来时先放行")
	}
}

func TestWatcherBlacklistSkipsVariety(t *testing.T) {
	src := &fakeSource{name: "s", minute: breakoutBars(), daily: daysOf()}
	w := NewWatcherSources(src)
	pinClock(w, testClock)
	w.SetBlacklist(Blacklist{Varieties: map[string]bool{"jm": true}}) // 小写也要命中

	if _, err := w.Start(WatchConfig{
		Params: Params{Period: "5"}, Prefixes: []string{"JM", "RB"}, Interval: WatchMaxInterval,
	}); err != nil {
		t.Fatal(err)
	}
	st := waitTicks(t, w, 1)
	if st.Varieties != 1 {
		t.Fatalf("黑名单品种不该进扫描范围：%d", st.Varieties)
	}
	events := w.Events(0)
	if len(events) == 0 {
		t.Fatal("RB 应该有突破事件")
	}
	for _, e := range events {
		if e.Prefix == "JM" {
			t.Fatalf("黑名单品种不该有事件：%+v", e)
		}
	}
	w.Stop()
}

func TestWatcherBlacklistSkipsResolvedContract(t *testing.T) {
	src := &fakeSource{name: "s", minute: breakoutBars(), daily: daysOf()}
	w := NewWatcherSources(src)
	pinClock(w, testClock)
	w.SetContractResolver(&fakeResolver{symbol: "JM2701", label: "2701"})
	w.SetBlacklist(Blacklist{Contracts: map[string]bool{"JM2701": true}}) // 会先同步解析出主力合约

	if _, err := w.Start(WatchConfig{
		Params: Params{Period: "5"}, Prefixes: []string{"JM"}, Interval: WatchMaxInterval,
	}); err != nil {
		t.Fatal(err)
	}
	st := waitTicks(t, w, 1)
	if st.Varieties != 0 {
		t.Fatalf("主力合约被拉黑的品种应移出扫描：%d", st.Varieties)
	}
	if events := w.Events(0); len(events) != 0 {
		t.Fatalf("不该有事件：%+v", events)
	}
	w.Stop()
}

func TestSetBlacklistWhileRunningRebuilds(t *testing.T) {
	src := &fakeSource{name: "s", minute: barsOf(quietBars(11)), daily: daysOf()}
	w := NewWatcherSources(src)
	if _, err := w.Start(WatchConfig{
		Params: Params{Period: "5"}, Prefixes: []string{"JM", "RB"}, Interval: WatchMaxInterval,
	}); err != nil {
		t.Fatal(err)
	}
	if st := waitTicks(t, w, 1); st.Varieties != 2 {
		t.Fatalf("起始应 2 个品种：%d", st.Varieties)
	}

	w.SetBlacklist(Blacklist{Varieties: map[string]bool{"JM": true}})
	if st := w.Status(); st.Varieties != 1 {
		t.Fatalf("拉黑后应只剩 1 个：%d", st.Varieties)
	}
	// 解除黑名单后能恢复（前端「移除」按钮依赖这个行为）
	w.SetBlacklist(Blacklist{})
	if st := w.Status(); st.Varieties != 2 {
		t.Fatalf("解除后应恢复 2 个：%d", st.Varieties)
	}
	w.Stop()
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
