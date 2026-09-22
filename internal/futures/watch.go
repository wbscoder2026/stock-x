package futures

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// 监控默认值与上限
const (
	WatchDefaultInterval = 30 // 轮询秒数
	WatchMinInterval     = 5
	WatchMaxInterval     = 600
	watchWorkers         = 6
	watchMaxEvents       = 500
	watchFetchTimeout    = 15 * time.Second
	watchKeepDays        = 2 // 去重记录保留天数
	watchFailRatio       = 0.5
	watchMaxBackoff      = 4 // 最多把间隔拉长到 5 倍
)

// WatchConfig 监控配置。Params 内嵌成扁平 JSON，前端可直接复用突破页的参数对象。
type WatchConfig struct {
	Params
	Interval int      `json:"interval"` // 轮询秒数，0 → 默认 30
	Prefixes []string `json:"prefixes"` // 要监控的品种码；空 = 全市场
}

// WatchEvent 一条突破提醒
type WatchEvent struct {
	Seq        int64   `json:"seq"`
	Fresh      bool    `json:"fresh"` // true = 出现在上一轮之后新长出来的 K 线上（弹窗）；false = 启动时已存在
	Day        string  `json:"day"`
	Time       string  `json:"time"`
	Symbol     string  `json:"symbol"`
	Prefix     string  `json:"prefix"`
	Name       string  `json:"name"`
	Direction  string  `json:"direction"`
	Level      string  `json:"level"`
	Close      float64 `json:"close"`
	LevelPrice float64 `json:"level_price"`
	Volume     int64   `json:"volume"`
}

// WatchStatus 监控状态
type WatchStatus struct {
	Running   bool           `json:"running"`
	Config    WatchConfig    `json:"config"`
	Source    string         `json:"source"` // 最近一次成功的数据源
	Sources   []SourceStatus `json:"sources"`
	Varieties int            `json:"varieties"`
	StartedAt string         `json:"started_at"`
	LastTick  string         `json:"last_tick"`
	Ticks     int            `json:"ticks"`
	Scanned   int            `json:"scanned"`
	Failures  int            `json:"failures"`
	LastError string         `json:"last_error"`
	LastMS    int64          `json:"last_ms"`
	Events    int            `json:"events"`
	LatestSeq int64          `json:"latest_seq"`
	Backoff   int            `json:"backoff"` // 当前退避倍数（1 = 正常；限流时自动拉长间隔）
}

// Watcher 全市场突破监控：后台按间隔扫一遍所有品种主连，把「新出现」的突破事件推给调用方。
type Watcher struct {
	bars *MultiSource
	now  func() time.Time

	mu        sync.Mutex
	cfg       WatchConfig
	varieties []Variety
	running   bool
	stopCh    chan struct{}
	startedAt time.Time

	day     string
	seen    map[string]int64
	lastBar map[string]time.Time
	events  []WatchEvent
	seq     int64

	ticks      int
	lastTick   time.Time
	scanned    int
	failures   int
	lastErr    string
	lastMS     int64
	failStreak int

	dailyDay   string
	dailyCache map[string][]Daily
}

// nextFailStreak 大面积失败（多为新浪限流 HTTP 456 或断网）时累加退避，恢复正常即清零。
func nextFailStreak(streak, scanned, failures int) int {
	total := scanned + failures
	if total == 0 {
		return streak
	}
	if float64(failures)/float64(total) <= watchFailRatio {
		return 0
	}
	if streak >= watchMaxBackoff {
		return watchMaxBackoff
	}
	return streak + 1
}

// backoffFactor 本轮间隔倍数（1 = 正常，最大 watchMaxBackoff+1）。
func backoffFactor(streak int) int {
	if streak < 0 {
		streak = 0
	}
	if streak > watchMaxBackoff {
		streak = watchMaxBackoff
	}
	return streak + 1
}

// NewWatcher 创建只走新浪的监控器（不启动）。
func NewWatcher(c *Client) *Watcher {
	return NewWatcherSources(NewSinaSource(c))
}

// NewWatcherSources 创建多源监控器：按传入顺序尝试，失败的源进冷却，后面的源顶上。
func NewWatcherSources(sources ...BarSource) *Watcher {
	if len(sources) == 0 {
		sources = []BarSource{NewSinaSource(nil)}
	}
	w := &Watcher{
		bars:       NewMultiSource(sources...),
		now:        time.Now,
		seen:       map[string]int64{},
		lastBar:    map[string]time.Time{},
		dailyCache: map[string][]Daily{},
	}
	w.bars.Now = func() time.Time { return w.now() } // 与监控共用一个可注入时钟
	return w
}

// NewDefaultWatcher 生产用三级数据源：
//
//  1. 东财（商品 5 所 60 个品种，限流宽松，覆盖分钟线 + 日线）
//  2. 新浪（全部 67 个品种，含中金所；连打会 HTTP 456）
//  3. 新浪备用域名（限流可能与主域名独立计数）
func NewDefaultWatcher() *Watcher {
	return NewWatcherSources(DefaultSources()...)
}

// DefaultSources 生产默认数据源链路（监控与回测共用）。
func DefaultSources() []BarSource {
	return []BarSource{NewEastmoneySource(""), NewSinaSource(nil), NewSinaAltSource()}
}

func (t *Watcher) fetchMinute(ctx context.Context, v Variety, period string) ([]Bar, error) {
	return t.bars.Minute(ctx, v, period)
}

func (t *Watcher) fetchDaily(ctx context.Context, v Variety) ([]Daily, error) {
	return t.bars.Daily(ctx, v)
}

// ScanDay 用「某个交易日 + 关键位」算出当日突破事件（监控用，与回测共用同一套 ScanTimeframe）。
// ORB 只在 5 分钟级别参与（与 BacktestBars 一致）。
func ScanDay(minutes []Bar, daily []Daily, day time.Time, p Params) []Event {
	p = mergeParams(p)
	levels := PivotLevels(daily, day)
	if p.Period == "5" {
		if orb := ORBLevels(minutes, day, p.ORB); len(orb) > 0 {
			levels = append(levels, orb...)
		}
	}
	return ScanTimeframe(minutes, day, levels, p)
}

// watchUniverse 把品种码过滤成品种列表；空 = 全市场，未知品种报错。
func watchUniverse(prefixes []string) ([]Variety, error) {
	all := ListVarieties()
	want := map[string]bool{}
	for _, raw := range prefixes {
		key := strings.ToUpper(strings.TrimSpace(raw))
		if key == "" {
			continue
		}
		if _, ok := varietyByPrefix(key); !ok {
			return nil, fmt.Errorf("未知品种 %s", raw)
		}
		want[key] = true
	}
	if len(want) == 0 {
		return all, nil
	}
	out := make([]Variety, 0, len(want))
	for _, v := range all {
		if want[strings.ToUpper(v.Prefix)] {
			out = append(out, v)
		}
	}
	return out, nil
}

func clampInterval(sec int) int {
	if sec <= 0 {
		return WatchDefaultInterval
	}
	if sec < WatchMinInterval {
		return WatchMinInterval
	}
	if sec > WatchMaxInterval {
		return WatchMaxInterval
	}
	return sec
}

func lastBarTime(bars []Bar) time.Time {
	var last time.Time
	for _, b := range bars {
		if b.Time.After(last) {
			last = b.Time
		}
	}
	return last
}

func watchKey(prefix string, e Event) string {
	return fmt.Sprintf("%s|%d|%s|%s", prefix, e.Time.Unix(), e.Direction, e.Level)
}

// collectNew 过滤出没见过的事件；fresh = 事件落在上一轮最后 K 线之后（刚发生的）。
func collectNew(seen map[string]int64, evs []Event, prevLastBar time.Time, day string, v Variety) []WatchEvent {
	out := make([]WatchEvent, 0, len(evs))
	for _, e := range evs {
		key := watchKey(v.Prefix, e)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = e.Time.Unix()
		out = append(out, WatchEvent{
			Fresh:      !prevLastBar.IsZero() && e.Time.After(prevLastBar),
			Day:        day,
			Time:       e.Time.In(locCST).Format("2006-01-02 15:04"),
			Symbol:     mainOf(v).Symbol,
			Prefix:     v.Prefix,
			Name:       v.Name,
			Direction:  e.Direction,
			Level:      e.Level,
			Close:      e.Close,
			LevelPrice: e.LevelPrice,
			Volume:     e.Volume,
		})
	}
	return out
}

// Start 启动监控；已在运行则用新配置继续（配置随时可改）。
func (t *Watcher) Start(cfg WatchConfig) (WatchStatus, error) {
	cfg.Params = mergeParams(cfg.Params)
	cfg.Interval = clampInterval(cfg.Interval)
	varieties, err := watchUniverse(cfg.Prefixes)
	if err != nil {
		return WatchStatus{}, err
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	t.cfg = cfg
	t.varieties = varieties
	if t.seen == nil {
		t.seen = map[string]int64{}
	}
	if t.lastBar == nil {
		t.lastBar = map[string]time.Time{}
	}
	if !t.running {
		t.running = true
		t.stopCh = make(chan struct{})
		t.startedAt = t.now()
		go t.loop(t.stopCh)
	}
	return t.statusLocked(), nil
}

// Stop 停止监控（保留已产生的提醒与去重表，避免重启后重复弹窗）。
func (t *Watcher) Stop() WatchStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.running && t.stopCh != nil {
		close(t.stopCh)
		t.stopCh = nil
		t.running = false
	}
	return t.statusLocked()
}

// Status 当前状态。
func (t *Watcher) Status() WatchStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.statusLocked()
}

// Events 取 seq 大于 since 的提醒（前端用游标增量拉取）。
func (t *Watcher) Events(since int64) []WatchEvent {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]WatchEvent, 0, len(t.events))
	for _, e := range t.events {
		if e.Seq > since {
			out = append(out, e)
		}
	}
	return out
}

func (t *Watcher) statusLocked() WatchStatus {
	st := WatchStatus{
		Running:   t.running,
		Config:    t.cfg,
		Source:    t.bars.Last(),
		Sources:   t.bars.Status(),
		Varieties: len(t.varieties),
		Ticks:     t.ticks,
		Scanned:   t.scanned,
		Failures:  t.failures,
		LastError: t.lastErr,
		LastMS:    t.lastMS,
		Events:    len(t.events),
		LatestSeq: t.seq,
		Backoff:   backoffFactor(t.failStreak),
	}
	if !t.startedAt.IsZero() {
		st.StartedAt = t.startedAt.In(locCST).Format("2006-01-02 15:04:05")
	}
	if !t.lastTick.IsZero() {
		st.LastTick = t.lastTick.In(locCST).Format("2006-01-02 15:04:05")
	}
	return st
}

func (t *Watcher) loop(stop chan struct{}) {
	for {
		t.tick()
		t.mu.Lock()
		interval := time.Duration(clampInterval(t.cfg.Interval)) * time.Second * time.Duration(backoffFactor(t.failStreak))
		t.mu.Unlock()
		select {
		case <-stop:
			return
		case <-time.After(interval):
		}
	}
}

type watchOutcome struct {
	v       Variety
	events  []Event
	lastBar time.Time
	err     error
}

// tick 扫一遍所有品种并合并出新事件（单 goroutine 驱动，内部并发抓数据）。
func (t *Watcher) tick() {
	t.mu.Lock()
	cfg := t.cfg
	varieties := append([]Variety(nil), t.varieties...)
	t.mu.Unlock()

	started := t.now()
	outcomes := make([]watchOutcome, len(varieties))
	sem := make(chan struct{}, watchWorkers)
	var wg sync.WaitGroup
	for i, v := range varieties {
		wg.Add(1)
		go func(i int, v Variety) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			outcomes[i] = t.fetchOne(cfg, v)
		}(i, v)
	}
	wg.Wait()

	t.mu.Lock()
	defer t.mu.Unlock()

	scanned, failures := 0, 0
	var errs []string
	maxDay := ""
	for _, oc := range outcomes {
		if oc.err != nil {
			failures++
			errs = append(errs, oc.v.Prefix+": "+oc.err.Error())
			continue
		}
		if oc.lastBar.IsZero() {
			continue
		}
		scanned++
		day := truncateDate(oc.lastBar).Format("2006-01-02")
		if day > maxDay {
			maxDay = day
		}
	}

	// 清理过期去重记录（跨夜盘也能认出是同一根 K 线，不重复提醒）
	if maxDay != "" {
		if cut, err := time.ParseInLocation("2006-01-02", maxDay, locCST); err == nil {
			limit := cut.AddDate(0, 0, -watchKeepDays).Unix()
			for key, ts := range t.seen {
				if ts < limit {
					delete(t.seen, key)
				}
			}
		}
		t.day = maxDay
	}

	for _, oc := range outcomes {
		if oc.err != nil || oc.lastBar.IsZero() {
			continue
		}
		day := truncateDate(oc.lastBar).Format("2006-01-02")
		prev := t.lastBar[oc.v.Prefix]
		fresh := collectNew(t.seen, oc.events, prev, day, oc.v)
		for i := range fresh {
			t.seq++
			fresh[i].Seq = t.seq
		}
		t.events = append(t.events, fresh...)
		t.lastBar[oc.v.Prefix] = oc.lastBar
	}
	if len(t.events) > watchMaxEvents {
		t.events = t.events[len(t.events)-watchMaxEvents:]
	}

	t.ticks++
	t.lastTick = t.now()
	t.scanned = scanned
	t.failures = failures
	t.failStreak = nextFailStreak(t.failStreak, scanned, failures)
	t.lastMS = t.lastTick.Sub(started).Milliseconds()
	if len(errs) == 0 {
		t.lastErr = ""
	} else {
		t.lastErr = strings.Join(errs[:min(len(errs), 3)], "; ")
		if len(errs) > 3 {
			t.lastErr += fmt.Sprintf(" 等 %d 个品种失败", len(errs))
		}
	}
}

func (t *Watcher) fetchOne(cfg WatchConfig, v Variety) watchOutcome {
	oc := watchOutcome{v: v}
	ctx, cancel := context.WithTimeout(context.Background(), watchFetchTimeout)
	defer cancel()

	bars, err := t.fetchMinute(ctx, v, cfg.Period)
	if err != nil {
		oc.err = fmt.Errorf("分钟线 %w", err)
		return oc
	}
	daily, err := t.daily(ctx, v)
	if err != nil {
		oc.err = fmt.Errorf("日线 %w", err)
		return oc
	}
	if len(bars) == 0 || len(daily) == 0 {
		return oc
	}
	oc.lastBar = lastBarTime(bars)
	day := truncateDate(oc.lastBar)
	oc.events = ScanDay(bars, daily, day, cfg.Params)
	return oc
}

// daily 日线按自然日缓存（关键位只用「今天之前」的日线，盘中不必反复拉）。
func (t *Watcher) daily(ctx context.Context, v Variety) ([]Daily, error) {
	today := t.now().In(locCST).Format("2006-01-02")
	t.mu.Lock()
	if t.dailyDay == today {
		if cached, ok := t.dailyCache[v.Prefix]; ok {
			t.mu.Unlock()
			return cached, nil
		}
	}
	t.mu.Unlock()

	list, err := t.fetchDaily(ctx, v)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	if t.dailyDay != today {
		t.dailyDay = today
		t.dailyCache = map[string][]Daily{}
	}
	t.dailyCache[v.Prefix] = list
	t.mu.Unlock()
	return list, nil
}
