package futures

import (
	"context"
	"fmt"
	"math"
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

// Blacklist 监控黑名单：命中的品种/合约不进入突破提醒列表。
type Blacklist struct {
	Varieties map[string]bool `json:"varieties"` // 品种码，如 JM（该品种所有合约）
	Contracts map[string]bool `json:"contracts"` // 合约代码，如 JM2701（仅该合约）
}

func (b Blacklist) empty() bool { return len(b.Varieties) == 0 && len(b.Contracts) == 0 }

// MutesVariety 整个品种是否被屏蔽。
func (b Blacklist) MutesVariety(prefix string) bool {
	return b.Varieties[strings.ToUpper(strings.TrimSpace(prefix))]
}

// MutesContract 该合约是否被屏蔽。
func (b Blacklist) MutesContract(symbol string) bool {
	return b.Contracts[strings.ToUpper(strings.TrimSpace(symbol))]
}

// normalizeBlacklist 统一成大写键，避免 "jm" / "JM" 不一致。
func normalizeBlacklist(b Blacklist) Blacklist {
	out := Blacklist{Varieties: map[string]bool{}, Contracts: map[string]bool{}}
	for key, on := range b.Varieties {
		if on && strings.TrimSpace(key) != "" {
			out.Varieties[strings.ToUpper(strings.TrimSpace(key))] = true
		}
	}
	for key, on := range b.Contracts {
		if on && strings.TrimSpace(key) != "" {
			out.Contracts[strings.ToUpper(strings.TrimSpace(key))] = true
		}
	}
	return out
}

// filterBlacklisted 剔掉黑名单命中的事件：品种级直接屏蔽；
// 合约级看「该品种当前主力合约」是否在黑名单里（未解析出来时先放行）。
func filterBlacklisted(events []Event, prefix, contract string, bl Blacklist) []Event {
	if bl.empty() || len(events) == 0 {
		return events
	}
	if bl.MutesVariety(prefix) {
		return nil
	}
	if len(bl.Contracts) == 0 || contract == "" {
		return events
	}
	if bl.MutesContract(contract) {
		return nil
	}
	return events
}

// 提醒保留时长（分钟）：页面上可改，服务端启动时从库里恢复。
const (
	DefaultAlertTTLMin = 30   // 默认 30 分钟
	MinAlertTTLMin     = 1    // 至少 1 分钟（0 会被当成「没填」）
	MaxAlertTTLMin     = 1440 // 最多 24 小时
)

// AlertTTL 默认提醒时效（保留给默认值引用与文档）。
const AlertTTL = DefaultAlertTTLMin * time.Minute

// AlertConfig 外发提醒开关（真正的发送由调用方实现，见 Watcher.OnEvents）
type AlertConfig struct {
	Feishu  bool `json:"feishu"`  // 飞书推送（服务端需配 FEISHU_WEBHOOK_URL）
	Desktop bool `json:"desktop"` // 本机系统通知（macOS 通知中心 / Linux notify-send）
}

// WatchConfig 监控配置。Params 内嵌成扁平 JSON，前端可直接复用突破页的参数对象。
type WatchConfig struct {
	Params
	Interval    int         `json:"interval"`      // 轮询秒数，0 → 默认 30
	Prefixes    []string    `json:"prefixes"`      // 要监控的品种码；空 = 全市场
	Alert       AlertConfig `json:"alert"`         // 系统级提醒（离开浏览器也能收到）
	AlertTTLMin int         `json:"alert_ttl_min"` // 提醒保留时长（分钟），0 → 默认 30，范围 1~1440
	// Enabled 是「是否开着监控」的持久化意愿：服务端启动时据此自动恢复。
	// 注意它与 running 是两回事：真正在跑没跑以 Watcher 的运行时状态为准，
	// 保存时由调用方按当时的运行状态回填（不信任前端传值）。
	Enabled bool `json:"enabled"`
}

// DefaultWatchConfig 用户没配过时的初值：默认开启监控 + 默认开桌面通知。
// （飞书要配 FEISHU_WEBHOOK_URL，默认不开，免得每轮都报通道未配置。）
func DefaultWatchConfig() WatchConfig {
	return WatchConfig{
		Params:      DefaultParams(),
		Interval:    WatchDefaultInterval,
		Alert:       AlertConfig{Desktop: true},
		AlertTTLMin: DefaultAlertTTLMin,
		Enabled:     true,
	}
}

// clampAlertTTLMin 提醒保留时长：没填/非法 → 默认值，超范围 → 夹到边界。
func clampAlertTTLMin(min int) int {
	if min <= 0 {
		return DefaultAlertTTLMin
	}
	if min < MinAlertTTLMin {
		return MinAlertTTLMin
	}
	if min > MaxAlertTTLMin {
		return MaxAlertTTLMin
	}
	return min
}

// NormalizeWatchConfig 补齐缺省项：老版本存下的配置、前端没传的字段都能安全落地。
// 注意不动 Enabled（false 是合法值，代表用户主动关了监控）。
func NormalizeWatchConfig(cfg WatchConfig) WatchConfig {
	cfg.Params = mergeParams(cfg.Params)
	cfg.Interval = clampInterval(cfg.Interval)
	cfg.AlertTTLMin = clampAlertTTLMin(cfg.AlertTTLMin)
	return cfg
}

// WatchEvent 一条突破提醒
type WatchEvent struct {
	Seq           int64   `json:"seq"`
	Fresh         bool    `json:"fresh"` // true = 出现在上一轮之后新长出来的 K 线上（弹窗）；false = 启动时已存在
	Day           string  `json:"day"`
	Time          string  `json:"time"`
	TimeMS        int64   `json:"time_ms"` // K 线时间（毫秒）；提醒按它判时效
	Symbol        string  `json:"symbol"`
	Prefix        string  `json:"prefix"`
	Name          string  `json:"name"`
	Contract      string  `json:"contract"`       // 主力月份合约，如 JM2701（异步补齐）
	ContractLabel string  `json:"contract_label"` // 月份标签，如 2701
	Direction     string  `json:"direction"`
	Level         string  `json:"level"`
	Close         float64 `json:"close"`
	LevelPrice    float64 `json:"level_price"`
	Volume        int64   `json:"volume"`
	StopPrice     float64 `json:"stop_price"`  // 推荐止损价（已对齐最小变动价位；0 = ATR 数据不足）
	TPPrice       float64 `json:"tp_price"`    // 推荐止盈价（止盈距离 = 止损距离 × 盈亏比）
	RR            float64 `json:"rr"`          // 本次推荐用的盈亏比
	StopATR       float64 `json:"stop_atr"`    // 本次推荐用的止损 ATR 倍数
	StopMode      string  `json:"stop_mode"`   // 止损方式：atr / prev_low
	StopPoints    float64 `json:"stop_points"` // prev_low 模式的缓冲点数
	TickSize      float64 `json:"tick_size"`   // 该品种最小变动价位（前端据此决定小数位）
}

// WatchStatus 监控状态
type WatchStatus struct {
	Running     bool           `json:"running"`
	Config      WatchConfig    `json:"config"`
	Source      string         `json:"source"` // 最近一次成功的数据源
	Sources     []SourceStatus `json:"sources"`
	Varieties   int            `json:"varieties"`
	StartedAt   string         `json:"started_at"`
	LastTick    string         `json:"last_tick"`
	Ticks       int            `json:"ticks"`
	Scanned     int            `json:"scanned"`
	Failures    int            `json:"failures"`
	LastError   string         `json:"last_error"`
	LastMS      int64          `json:"last_ms"`
	Events      int            `json:"events"`
	AlertTTLSec int            `json:"alert_ttl_sec"` // 提醒时效（秒）：超时的提醒不显示
	LatestSeq   int64          `json:"latest_seq"`
	Backoff     int            `json:"backoff"`    // 当前退避倍数（1 = 正常；限流时自动拉长间隔）
	AlertNote   string         `json:"alert_note"` // 最近一次外发提醒的结果
}

// Watcher 全市场突破监控：后台按间隔扫一遍所有品种主连，把「新出现」的突破事件推给调用方。
type Watcher struct {
	bars *MultiSource
	now  func() time.Time

	// OnEvents 每轮扫出「刚发生」的突破后回调（在扫描锁外执行，别在里面做太久的事）。
	// 飞书 / 系统通知等外发通道由调用方挂上去。
	OnEvents func(cfg WatchConfig, events []WatchEvent)

	mu        sync.Mutex
	cfg       WatchConfig
	varieties []Variety
	running   bool
	stopCh    chan struct{}
	startedAt time.Time
	alertNote string

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

	contracts       ContractResolver        // 解析主力月份合约（不注入就不显示月份）
	contractCache   map[string]contractInfo // 品种 → 已解析结果
	contractPending map[string]bool         // 正在解析中的品种
	blacklist       Blacklist               // 监控黑名单（品种/合约）
}

type contractInfo struct {
	symbol string
	label  string
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
		bars:            NewMultiSource(sources...),
		now:             time.Now,
		seen:            map[string]int64{},
		lastBar:         map[string]time.Time{},
		dailyCache:      map[string][]Daily{},
		contractCache:   map[string]contractInfo{},
		contractPending: map[string]bool{},
	}
	w.bars.Now = func() time.Time { return w.now() } // 与监控共用一个可注入时钟
	return w
}

// SetBlacklist 设置监控黑名单：命中的品种/合约不再进入突破提醒列表。
// 运行中调用会立刻收敛扫描范围（品种级立即生效；合约级用当前主力合约判断）。
func (t *Watcher) SetBlacklist(bl Blacklist) {
	bl = normalizeBlacklist(bl)
	t.mu.Lock()
	t.blacklist = bl
	if t.running {
		t.rebuildVarietiesLocked()
	}
	t.dropMutedEventsLocked()
	// 合约级黑名单：把对应品种的主力合约先解析出来（条数很少，同步做，保证立刻生效）
	pending := make([]Variety, 0, len(bl.Contracts))
	for contract := range bl.Contracts {
		v, ok := VarietyOfSymbol(contract)
		if !ok {
			continue
		}
		if _, cached := t.contractCache[v.Prefix]; cached {
			continue
		}
		if t.contractPending[v.Prefix] {
			continue
		}
		t.contractPending[v.Prefix] = true
		pending = append(pending, v)
	}
	resolver := t.contracts
	t.mu.Unlock()

	if resolver == nil || len(pending) == 0 {
		return
	}
	var wg sync.WaitGroup
	for _, v := range pending {
		wg.Add(1)
		go func(v Variety) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			symbol, label, err := resolver.Resolve(ctx, v)

			t.mu.Lock()
			delete(t.contractPending, v.Prefix)
			if err == nil && symbol != "" {
				t.contractCache[v.Prefix] = contractInfo{symbol: symbol, label: label}
				t.fillContractLocked([]string{v.Prefix})
				if t.blacklist.MutesContract(symbol) {
					t.dropPrefixEventsLocked(v.Prefix)
					if t.running {
						t.rebuildVarietiesLocked()
					}
				}
			}
			t.mu.Unlock()
		}(v)
	}
	wg.Wait()
}

// rebuildVarietiesLocked 按当前配置 + 黑名单重算要扫描的品种（需持锁）。
func (t *Watcher) rebuildVarietiesLocked() {
	all, err := watchUniverse(t.cfg.Prefixes)
	if err != nil {
		return // 配置非法时保持原样（Start 已校验过）
	}
	out := make([]Variety, 0, len(all))
	for _, v := range all {
		if t.blacklist.MutesVariety(v.Prefix) {
			continue
		}
		if info, ok := t.contractCache[v.Prefix]; ok && t.blacklist.MutesContract(info.symbol) {
			continue // 该品种当前主力合约被拉黑
		}
		out = append(out, v)
	}
	t.varieties = out
}

// dropMutedEventsLocked 撤掉缓冲区里已被拉黑的事件（需持锁）。
func (t *Watcher) dropMutedEventsLocked() {
	if t.blacklist.empty() || len(t.events) == 0 {
		return
	}
	kept := t.events[:0]
	for _, e := range t.events {
		if t.mutedEventLocked(e) {
			continue
		}
		kept = append(kept, e)
	}
	t.events = kept
}

func (t *Watcher) dropPrefixEventsLocked(prefix string) {
	kept := t.events[:0]
	for _, e := range t.events {
		if e.Prefix == prefix {
			continue
		}
		kept = append(kept, e)
	}
	t.events = kept
}

func (t *Watcher) mutedEventLocked(e WatchEvent) bool {
	if t.blacklist.MutesVariety(e.Prefix) {
		return true
	}
	if len(t.blacklist.Contracts) == 0 {
		return false
	}
	if e.Contract != "" {
		return t.blacklist.MutesContract(e.Contract)
	}
	if info, ok := t.contractCache[e.Prefix]; ok {
		return t.blacklist.MutesContract(info.symbol)
	}
	return false
}

// SetContractResolver 注入主力月份合约解析器（不注入则事件里不带月份）。
func (t *Watcher) SetContractResolver(r ContractResolver) {
	t.mu.Lock()
	t.contracts = r
	t.mu.Unlock()
}

// resolveContracts 补齐这些品种的主力月份合约：命中缓存的立刻回填事件，
// 未命中的丢到后台异步解析（限并发 4），解析完再回填——不阻塞扫描。
func (t *Watcher) resolveContracts(prefixes []string) {
	t.mu.Lock()
	resolver := t.contracts
	if resolver == nil {
		t.mu.Unlock()
		return
	}
	t.fillContractLocked(prefixes)
	jobs := make([]Variety, 0, len(prefixes))
	for _, prefix := range prefixes {
		if _, ok := t.contractCache[prefix]; ok {
			continue
		}
		if t.contractPending[prefix] {
			continue
		}
		v, ok := varietyByPrefix(prefix)
		if !ok {
			continue
		}
		t.contractPending[prefix] = true
		jobs = append(jobs, v)
	}
	t.mu.Unlock()

	if len(jobs) == 0 {
		return
	}
	sem := make(chan struct{}, 4)
	for _, v := range jobs {
		go func(v Variety) {
			sem <- struct{}{}
			defer func() { <-sem }()
			ctx, cancel := context.WithTimeout(context.Background(), watchFetchTimeout)
			defer cancel()
			symbol, label, err := resolver.Resolve(ctx, v)

			t.mu.Lock()
			delete(t.contractPending, v.Prefix)
			if err == nil && symbol != "" {
				t.contractCache[v.Prefix] = contractInfo{symbol: symbol, label: label}
				t.fillContractLocked([]string{v.Prefix})
				if t.blacklist.MutesContract(symbol) { // 主力合约被拉黑 → 撤事件 + 不再扫该品种
					t.dropPrefixEventsLocked(v.Prefix)
					if t.running {
						t.rebuildVarietiesLocked()
					}
				}
			}
			t.mu.Unlock()
		}(v)
	}
}

// fillContractLocked 把已解析的月份合约回填到历史事件（调用前需持锁）。
func (t *Watcher) fillContractLocked(prefixes []string) {
	for _, prefix := range prefixes {
		info, ok := t.contractCache[prefix]
		if !ok {
			continue
		}
		for i := range t.events {
			if t.events[i].Prefix == prefix && t.events[i].Contract == "" {
				t.events[i].Contract = info.symbol
				t.events[i].ContractLabel = info.label
			}
		}
	}
}

// NewDefaultWatcher 生产用三级数据源：
//
//  1. 东财（商品 5 所 60 个品种，限流宽松，覆盖分钟线 + 日线）
//  2. 新浪（全部 67 个品种，含中金所；连打会 HTTP 456）
//  3. 新浪备用域名（限流可能与主域名独立计数）
func NewDefaultWatcher() *Watcher {
	w := NewWatcherSources(DefaultSources()...)
	w.SetContractResolver(NewSinaContracts(nil)) // 事件里带上主力月份合约（JM → JM2701）
	return w
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

// CSTZone 交易所时区（+8），供上层格式化时间用。
func CSTZone() *time.Location { return locCST }

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

// finite 是否是正常有限数（NaN / ±Inf 会让推荐价失去意义，甚至让 JSON 编码失败）。
func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// effectiveRR 盈亏比：非法/未填 → 默认值。
func effectiveRR(rr float64) float64 {
	if !finite(rr) || rr <= 0 {
		return DefaultRR
	}
	return rr
}

// effectiveStopATR 止损 ATR 倍数：非法/未填 → 默认值（0 会让止损贴到入场价上）。
func effectiveStopATR(mult float64) float64 {
	if !finite(mult) || mult <= 0 {
		return DefaultStopATR
	}
	return mult
}

// eventATR 有效 ATR：缺失或非正 → 0（表示没有足够数据给推荐价）。
func eventATR(v float64) float64 {
	if !finite(v) || v <= 0 {
		return 0
	}
	return v
}

// StopInput 计算推荐止损/止盈所需的输入。
type StopInput struct {
	Prefix     string  // 品种码（决定最小变动价位）
	Direction  string  // 突破方向
	Entry      float64 // 入场参考价 = 信号那根 K 线的收盘
	ATR        float64 // atr 模式：止损距离 = StopATR × ATR
	PrevLow    float64 // prev_low 模式：前一根最低（做多用）
	PrevHigh   float64 // prev_low 模式：前一根最高（做空用）
	StopMode   string  // atr | prev_low
	StopATR    float64 // atr 模式的倍数
	StopPoints float64 // prev_low 模式的缓冲点数
	RR         float64 // 盈亏比
}

// RecommendStop 按止损方式算推荐止损/止盈（提醒与回测共用这一份，避免两边漂移）：
//
//	atr      → 止损 = 入场 ∓ StopATR×ATR，止盈距离 = 该距离 × 盈亏比
//	prev_low → 做多止损 = 前一根最低 − StopPoints，做空 = 前一根最高 + StopPoints，
//	           止盈距离 = 实际止损距离 × 盈亏比
//
// 两个价都按品种最小变动价位对齐，并保底离入场 1 个跳。
// 数据不足时（价格非法 / ATR 为 0）返回 0,0；prev_low 缺「前一根」则退回 atr（不丢样本）。
func RecommendStop(in StopInput) (stop, tp float64) {
	if !finite(in.Entry) || in.Entry <= 0 {
		return 0, 0
	}
	tick := TickSize(in.Prefix)
	base := RoundToTick(in.Entry, tick)
	down := in.Direction == DirDown
	rr := effectiveRR(in.RR)

	var risk float64
	if normalizeStopMode(in.StopMode) == StopModePrevLow {
		anchor := in.PrevLow
		if down {
			anchor = in.PrevHigh
		}
		if finite(anchor) && anchor > 0 {
			pts := effectiveStopPoints(in.StopPoints)
			raw := anchor - pts
			if down {
				raw = anchor + pts
			}
			stop = RoundToTick(raw, tick)
			stop = floorStopSide(stop, base, tick, down)
			risk = math.Abs(in.Entry - stop) // 止盈用「实际」止损距离
		}
	}
	if risk <= 0 { // atr 模式；或 prev_low 缺前一根时退回
		atr := eventATR(in.ATR)
		if atr <= 0 {
			return 0, 0
		}
		risk = atr * effectiveStopATR(in.StopATR)
		stop = RoundToTick(entrySide(in.Entry, risk, down), tick)
		stop = floorStopSide(stop, base, tick, down)
	}
	tp = RoundToTick(entrySide(in.Entry, risk*rr, !down), tick)
	if tp > 0 {
		tp = floorTPSide(stop, tp, base, tick, down)
	}
	return stop, tp
}

// entrySide 按突破方向取「减」或「加」：做多要向下（止损）/向上（止盈）。
func entrySide(entry, dist float64, down bool) float64 {
	if down {
		return entry + dist
	}
	return entry - dist
}

// floorStopSide 止损至少离入场 1 个跳（取整把止损压到入场价上等于没止损）。
func floorStopSide(stop, base, tick float64, down bool) float64 {
	if down {
		if stop < base+tick {
			return base + tick
		}
		return stop
	}
	if stop > base-tick {
		return base - tick
	}
	return stop
}

// floorTPSide 止盈同理，方向相反。
func floorTPSide(stop, tp, base, tick float64, down bool) float64 {
	if down {
		if tp > base-tick {
			return base - tick
		}
		return tp
	}
	if tp < base+tick {
		return base + tick
	}
	return tp
}

// collectNew 过滤出没见过的事件；fresh = 事件落在上一轮最后 K 线之后（刚发生的）。
// p 里的止损方式 / 倍数 / 点数 / 盈亏比决定推荐止损与止盈。
func collectNew(seen map[string]int64, evs []Event, prevLastBar time.Time, day string, v Variety, p Params) []WatchEvent {
	out := make([]WatchEvent, 0, len(evs))
	p = mergeParams(p)
	for _, e := range evs {
		key := watchKey(v.Prefix, e)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = e.Time.Unix()
		stop, tp := RecommendStop(StopInput{
			Prefix: v.Prefix, Direction: e.Direction, Entry: e.Close, ATR: e.ATR,
			PrevLow: e.PrevLow, PrevHigh: e.PrevHigh,
			StopMode: p.StopMode, StopATR: p.StopATR, StopPoints: p.StopPoints, RR: p.RR,
		})
		out = append(out, WatchEvent{
			Fresh:      !prevLastBar.IsZero() && e.Time.After(prevLastBar),
			Day:        day,
			Time:       e.Time.In(locCST).Format("2006-01-02 15:04"),
			TimeMS:     e.Time.UnixMilli(),
			Symbol:     mainOf(v).Symbol,
			Prefix:     v.Prefix,
			Name:       v.Name,
			Direction:  e.Direction,
			Level:      e.Level,
			Close:      e.Close,
			LevelPrice: e.LevelPrice,
			Volume:     e.Volume,
			StopPrice:  stop,
			TPPrice:    tp,
			RR:         effectiveRR(p.RR),
			StopATR:    effectiveStopATR(p.StopATR),
			StopMode:   normalizeStopMode(p.StopMode),
			StopPoints: effectiveStopPoints(p.StopPoints),
			TickSize:   TickSize(v.Prefix),
		})
	}
	return out
}

// Start 启动监控；已在运行则用新配置继续（配置随时可改）。
func (t *Watcher) Start(cfg WatchConfig) (WatchStatus, error) {
	cfg = NormalizeWatchConfig(cfg)
	varieties, err := watchUniverse(cfg.Prefixes)
	if err != nil {
		return WatchStatus{}, err
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	t.cfg = cfg
	t.varieties = varieties
	if !t.blacklist.empty() {
		t.rebuildVarietiesLocked() // 黑名单里的品种不进扫描范围
	}
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
// 读取时顺带做过期清理，保证「超过 AlertTTL 的提醒不会再出现在列表里」。
func (t *Watcher) Events(since int64) []WatchEvent {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneExpiredLocked(t.now())
	out := make([]WatchEvent, 0, len(t.events))
	for _, e := range t.events {
		if e.Seq > since {
			out = append(out, e)
		}
	}
	return out
}

// pruneExpiredLocked 剔除过期提醒并按容量截断（需持锁）。
// 注意：只清理「列表」，seen 里仍保留去重记录，所以同一根 K 线不会被重复提醒。
func (t *Watcher) pruneExpiredLocked(now time.Time) {
	if len(t.events) > 0 {
		cut := now.Add(-t.alertTTLLocked()).UnixMilli()
		kept := t.events[:0]
		for _, e := range t.events {
			if e.TimeMS > 0 && e.TimeMS < cut {
				continue
			}
			kept = append(kept, e)
		}
		t.events = kept
	}
	if len(t.events) > watchMaxEvents {
		t.events = append(t.events[:0:0], t.events[len(t.events)-watchMaxEvents:]...)
	}
}

// alertTTLLocked 当前生效的提醒保留时长（需持锁）。
// 页面没配过（或被清零）时用默认值，所以老配置不会突然变成「全清」。
func (t *Watcher) alertTTLLocked() time.Duration {
	return time.Duration(clampAlertTTLMin(t.cfg.AlertTTLMin)) * time.Minute
}

func (t *Watcher) statusLocked() WatchStatus {
	t.pruneExpiredLocked(t.now()) // 状态里的条数也按时效算
	st := WatchStatus{
		Running:     t.running,
		AlertTTLSec: int(t.alertTTLLocked() / time.Second),
		Config:      t.cfg,
		Source:      t.bars.Last(),
		Sources:     t.bars.Status(),
		Varieties:   len(t.varieties),
		Ticks:       t.ticks,
		Scanned:     t.scanned,
		Failures:    t.failures,
		LastError:   t.lastErr,
		LastMS:      t.lastMS,
		Events:      len(t.events),
		LatestSeq:   t.seq,
		Backoff:     backoffFactor(t.failStreak),
		AlertNote:   t.alertNote,
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

	scanned, failures := 0, 0
	alertEvents := make([]WatchEvent, 0, 4)
	newPrefixes := make([]string, 0, 8)
	seenPrefix := map[string]bool{}
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
		events := filterBlacklisted(oc.events, oc.v.Prefix, t.contractCache[oc.v.Prefix].symbol, t.blacklist)
		fresh := collectNew(t.seen, events, prev, day, oc.v, t.cfg.Params)
		for i := range fresh {
			t.seq++
			fresh[i].Seq = t.seq
			if fresh[i].Fresh {
				alertEvents = append(alertEvents, fresh[i]) // 只有「刚发生」的才外发
			}
		}
		if len(fresh) > 0 && !seenPrefix[oc.v.Prefix] {
			seenPrefix[oc.v.Prefix] = true
			newPrefixes = append(newPrefixes, oc.v.Prefix)
		}
		t.events = append(t.events, fresh...)
		t.lastBar[oc.v.Prefix] = oc.lastBar
	}
	t.pruneExpiredLocked(t.now()) // 本轮结束后清掉过期提醒

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

	cfgForAlert := t.cfg
	hook := t.OnEvents
	t.mu.Unlock()

	// 锁外：补齐主力月份合约（异步），事件里就能带上「2701」这种月份
	t.resolveContracts(newPrefixes)

	// 在锁外、且放到后台 goroutine 里回调：推送（飞书 HTTP / 系统通知）再慢也不拖住扫描
	if hook != nil && len(alertEvents) > 0 {
		go hook(cfgForAlert, alertEvents)
	}
}

// SetAlertNote 记录最近一次外发提醒的结果（状态页可见）。
func (t *Watcher) SetAlertNote(note string) {
	t.mu.Lock()
	t.alertNote = note
	t.mu.Unlock()
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
	// 分钟线与日线价差异常 → 本轮跳过该品种（脏数据会让 ATR/关键位全失真）
	if err := checkMinuteDailyAgree(v.Prefix, bars, daily); err != nil {
		oc.err = err
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
