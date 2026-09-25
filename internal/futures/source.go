package futures

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// BarSource 行情源：按品种取分钟线 / 日线。
// 品种（Variety）是内部概念，各源自己负责换成自己的代码（新浪 JM0 / 东财 114.jmm）。
type BarSource interface {
	Name() string
	Minute(ctx context.Context, v Variety, period string) ([]Bar, error)
	Daily(ctx context.Context, v Variety) ([]Daily, error)
}

// VarietyFilter 源可声明自己覆盖哪些品种（东财不覆盖中金所），被跳过的源不算失败。
type VarietyFilter interface {
	Supports(v Variety) bool
}

// RangeSource 支持「指定结束时间往前翻页」的源，用于把历史日线挖深
// （东财 end 参数：一页 800 根 ≈ 3.3 年）。
type RangeSource interface {
	DailyRange(ctx context.Context, v Variety, end time.Time, limit int) ([]Daily, error)
}

// MinuteRangeSource 支持按结束时间往前翻分钟线。上游往往只留有限窗口，翻不动时返回空数据。
type MinuteRangeSource interface {
	MinuteRange(ctx context.Context, v Variety, period string, end time.Time, limit int) ([]Bar, error)
}

// SymbolMinuteSource 能按「具体合约代码」取分钟线的源。
// 品种口径（BarSource）只能拿主连，月份合约（JM2701）要走这一层。
type SymbolMinuteSource interface {
	MinuteSymbol(ctx context.Context, symbol, period string) ([]Bar, error)
}

// SymbolMinuteRangeSource 在 SymbolMinuteSource 之上还能按结束时间往前翻页（补全历史用）。
type SymbolMinuteRangeSource interface {
	MinuteRangeSymbol(ctx context.Context, symbol, period string, end time.Time, limit int) ([]Bar, error)
}

// ---------------------------------------------------------------- 多源容错

// SourceStatus 各数据源健康度
type SourceStatus struct {
	Name     string `json:"name"`
	Calls    int    `json:"calls"`
	OK       int    `json:"ok"`
	Fails    int    `json:"fails"`
	Cooldown int    `json:"cooldown"` // 剩余冷却秒数，0 = 正常
}

type sourceHealth struct {
	name     string
	calls    int
	ok       int
	fails    int
	cooldown time.Time
}

const (
	sourceCooldownBase = 30 * time.Second
	sourceCooldownMax  = 5 * time.Minute
)

func sourceCooldown(fails int) time.Duration {
	d := sourceCooldownBase
	for i := 1; i < fails; i++ {
		d *= 2
		if d >= sourceCooldownMax {
			return sourceCooldownMax
		}
	}
	return d
}

// errSourceNotApplicable 该源不适用这次查询（例如不支持翻页），跳过且不算失败。
var errSourceNotApplicable = errors.New("源不适用该查询")

// MultiSource 多源串联：按顺序尝试，失败的源进冷却（冷却期内不优先试），后面的源顶上。
// 监控与回测共用同一套链路。
type MultiSource struct {
	sources []BarSource
	Now     func() time.Time // 可注入时钟，便于测试

	mu     sync.Mutex
	health []sourceHealth
	last   string
}

func NewMultiSource(sources ...BarSource) *MultiSource {
	health := make([]sourceHealth, 0, len(sources))
	for _, s := range sources {
		health = append(health, sourceHealth{name: s.Name()})
	}
	return &MultiSource{sources: sources, Now: time.Now, health: health}
}

func (m *MultiSource) Name() string {
	names := make([]string, 0, len(m.sources))
	for _, s := range m.sources {
		names = append(names, s.Name())
	}
	return strings.Join(names, "+")
}

// Sources 数据源链路（按尝试顺序）。
func (m *MultiSource) Sources() []BarSource {
	out := make([]BarSource, len(m.sources))
	copy(out, m.sources)
	return out
}

// Last 最近一次成功的数据源名。
func (m *MultiSource) Last() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.last
}

// Status 各源健康度快照。
func (m *MultiSource) Status() []SourceStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	out := make([]SourceStatus, 0, len(m.health))
	for _, h := range m.health {
		left := 0
		if now.Before(h.cooldown) {
			left = int(h.cooldown.Sub(now).Seconds())
		}
		out = append(out, SourceStatus{Name: h.name, Calls: h.calls, OK: h.ok, Fails: h.fails, Cooldown: left})
	}
	return out
}

func (m *MultiSource) now() time.Time {
	if m.Now == nil {
		return time.Now()
	}
	return m.Now()
}

// order 尝试顺序：健康的源按原顺序在前，冷却中的排后面（全冷却时仍会被试一遍）。
func (m *MultiSource) order() []int {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	ready := make([]int, 0, len(m.sources))
	cooling := make([]int, 0, len(m.sources))
	for i, h := range m.health {
		if now.Before(h.cooldown) {
			cooling = append(cooling, i)
		} else {
			ready = append(ready, i)
		}
	}
	return append(ready, cooling...)
}

func (m *MultiSource) success(i int) {
	m.mu.Lock()
	h := &m.health[i]
	h.ok++
	h.fails = 0
	h.cooldown = time.Time{}
	m.last = h.name
	m.mu.Unlock()
}

func (m *MultiSource) failure(i int, now time.Time) {
	m.mu.Lock()
	h := &m.health[i]
	h.fails++
	h.cooldown = now.Add(sourceCooldown(h.fails))
	m.mu.Unlock()
}

// fetch 依次尝试各源；不支持的品种（VarietyFilter）直接跳过、不算失败。
func fetch[T any](m *MultiSource, v Variety, call func(BarSource) ([]T, error)) ([]T, error) {
	var lastErr error
	for _, i := range m.order() {
		src := m.sources[i]
		if filter, ok := src.(VarietyFilter); ok && !filter.Supports(v) {
			continue
		}
		m.mu.Lock()
		m.health[i].calls++
		m.mu.Unlock()

		list, err := call(src)
		if errors.Is(err, errSourceNotApplicable) {
			continue
		}
		if err == nil && len(list) == 0 {
			err = fmt.Errorf("空数据")
		}
		if err == nil {
			m.success(i)
			return list, nil
		}
		m.failure(i, m.now())
		lastErr = fmt.Errorf("%s: %w", src.Name(), err)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("没有可用的数据源")
	}
	return nil, lastErr
}

// MinuteRange 分钟线翻页。end 为零值表示最新一页；不支持翻页的源在指定 end 时会被跳过。
func (m *MultiSource) MinuteRange(ctx context.Context, v Variety, period string, end time.Time, limit int) ([]Bar, error) {
	return fetch(m, v, func(src BarSource) ([]Bar, error) {
		var bars []Bar
		var err error
		if rs, ok := src.(MinuteRangeSource); ok {
			bars, err = rs.MinuteRange(ctx, v, period, end, limit)
		} else if !end.IsZero() {
			return nil, errSourceNotApplicable
		} else {
			bars, err = src.Minute(ctx, v, period)
		}
		if err != nil {
			return nil, err
		}
		if err := checkBarScale(v.Prefix, barCloses(bars)); err != nil {
			return nil, err
		}
		return bars, nil
	})
}

// MinuteRangeSymbol 按具体合约代码往前翻分钟线：主连（JM0）与月份合约（JM2701）都走这里。
// end 为零值表示最新一页；不支持翻页的源在指定 end 时会被跳过。
func (m *MultiSource) MinuteRangeSymbol(ctx context.Context, symbol, period string, end time.Time, limit int) ([]Bar, error) {
	v, _ := VarietyOfSymbol(symbol)
	return fetch(m, v, func(src BarSource) ([]Bar, error) {
		var bars []Bar
		var err error
		if rs, ok := src.(SymbolMinuteRangeSource); ok {
			bars, err = rs.MinuteRangeSymbol(ctx, symbol, period, end, limit)
		} else if !end.IsZero() {
			return nil, errSourceNotApplicable
		} else if ss, ok := src.(SymbolMinuteSource); ok {
			bars, err = ss.MinuteSymbol(ctx, symbol, period)
		} else {
			return nil, errSourceNotApplicable
		}
		if err != nil {
			return nil, err
		}
		if v.Prefix != "" {
			// 脏数据（价格量级混叠）也算失败 → 自动降级到下一个源
			if err := checkBarScale(v.Prefix, barCloses(bars)); err != nil {
				return nil, err
			}
		}
		return bars, nil
	})
}

func (m *MultiSource) Minute(ctx context.Context, v Variety, period string) ([]Bar, error) {
	return fetch(m, v, func(src BarSource) ([]Bar, error) {
		bars, err := src.Minute(ctx, v, period)
		if err != nil {
			return nil, err
		}
		// 脏数据（价格量级混叠）也算失败 → 自动降级到下一个源
		if err := checkBarScale(v.Prefix, barCloses(bars)); err != nil {
			return nil, err
		}
		return bars, nil
	})
}

func (m *MultiSource) Daily(ctx context.Context, v Variety) ([]Daily, error) {
	return fetch(m, v, func(src BarSource) ([]Daily, error) {
		days, err := src.Daily(ctx, v)
		if err != nil {
			return nil, err
		}
		if err := checkBarScale(v.Prefix, dailyCloses(days)); err != nil {
			return nil, err
		}
		return days, nil
	})
}

// DailyRange 日线翻页（挖历史）：优先用支持翻页的源；end 为零值时也允许普通源兜底。
func (m *MultiSource) DailyRange(ctx context.Context, v Variety, end time.Time, limit int) ([]Daily, error) {
	return fetch(m, v, func(src BarSource) ([]Daily, error) {
		var days []Daily
		var err error
		if rs, ok := src.(RangeSource); ok {
			days, err = rs.DailyRange(ctx, v, end, limit)
		} else if !end.IsZero() {
			return nil, errSourceNotApplicable
		} else {
			days, err = src.Daily(ctx, v)
		}
		if err != nil {
			return nil, err
		}
		if err := checkBarScale(v.Prefix, dailyCloses(days)); err != nil {
			return nil, err
		}
		return days, nil
	})
}

// ---------------------------------------------------------------- 新浪

// SinaSource 新浪期货（现有数据源，覆盖全部 67 个品种）
type SinaSource struct {
	Client *Client
	Label  string // 多域名时用于状态里区分，如 sina / sina-alt
}

func NewSinaSource(c *Client) *SinaSource {
	if c == nil {
		c = &Client{}
	}
	return &SinaSource{Client: c}
}

// NewSinaAltSource 新浪备用域名（stock.finance.sina.com.cn），限流可能与默认域名独立计数；
// 只在主源失败时才会被尝试，正常情况不增加任何请求。
func NewSinaAltSource() *SinaSource {
	return &SinaSource{
		Client: &Client{
			MinuteURL: "https://stock.finance.sina.com.cn/futures/api/jsonp.php/=/InnerFuturesNewService.getFewMinLine",
			DailyURL:  "https://stock.finance.sina.com.cn/futures/api/jsonp.php/var%20_x=/InnerFuturesNewService.getDailyKLine",
		},
		Label: "sina-alt",
	}
}

func (s *SinaSource) Name() string {
	if s.Label != "" {
		return s.Label
	}
	return "sina"
}

func (s *SinaSource) Minute(ctx context.Context, v Variety, period string) ([]Bar, error) {
	return s.Client.Minute(ctx, mainOf(v).Symbol, period)
}

func (s *SinaSource) Daily(ctx context.Context, v Variety) ([]Daily, error) {
	return s.Client.Daily(ctx, mainOf(v).Symbol)
}

// MinuteSymbol 新浪按合约代码取分钟线：主连和月份合约都能取（接口本来就吃具体代码）。
func (s *SinaSource) MinuteSymbol(ctx context.Context, symbol, period string) ([]Bar, error) {
	return s.Client.Minute(ctx, symbol, period)
}

// MinuteRangeSymbol 新浪只给最新一段，没有按日期往前翻页的能力 → 指定 end 时报「不适用」，
// 让 MultiSource 自动落到支持翻页的源（东财）上。
func (s *SinaSource) MinuteRangeSymbol(ctx context.Context, symbol, period string, end time.Time, _ int) ([]Bar, error) {
	if !end.IsZero() {
		return nil, errSourceNotApplicable
	}
	return s.Client.Minute(ctx, symbol, period)
}

// ---------------------------------------------------------------- 东方财富

const defaultEastmoneyURL = "https://push2his.eastmoney.com/api/qt/stock/kline/get"

// 东财市场号：上期所 113 / 大商所 114 / 郑商所 115 / 上期能源 142 / 广期所 225
var eastmoneyMarkets = map[string]int{
	"上期所":  113,
	"大商所":  114,
	"郑商所":  115,
	"上期能源": 142,
	"广期所":  225,
}

// EastmoneySource 东方财富期货（免费、无需账号；主连代码 = 品种字母 + m，如 rbm / jmm / tam）。
// 不覆盖中金所（「当月连续」编号不等于主力连续），这类品种由 Supports 直接跳过。
type EastmoneySource struct {
	BaseURL string
	HTTP    *http.Client
	// 每次请求取多少根（分钟线要够 ATR/Donchian/ORB 用）
	MinuteLimit int
	DailyLimit  int
}

func NewEastmoneySource(baseURL string) *EastmoneySource {
	if baseURL == "" {
		baseURL = defaultEastmoneyURL
	}
	return &EastmoneySource{BaseURL: baseURL, MinuteLimit: 1000, DailyLimit: 800}
}

func (e *EastmoneySource) Name() string { return "eastmoney" }

func (e *EastmoneySource) Supports(v Variety) bool {
	_, ok := eastmoneySecid(v)
	return ok
}

// eastmoneySecid 品种 → 东财主连 secid，如 JM → 114.jmm；不支持的品种返回 false。
func eastmoneySecid(v Variety) (string, bool) {
	market, ok := eastmoneyMarkets[v.Exchange]
	if !ok {
		return "", false
	}
	code := strings.ToLower(strings.TrimSpace(v.Prefix))
	if code == "" {
		return "", false
	}
	return fmt.Sprintf("%d.%sm", market, code), true
}

// eastmoneySymbolSecid 合约代码 → 东财 secid：主连 JM0 → 114.jmm（沿用连续口径），
// 月份合约 JM2701 → 114.jm2701。认不出来的代码返回 false。
func eastmoneySymbolSecid(symbol string) (string, bool) {
	sym := strings.ToUpper(strings.TrimSpace(symbol))
	v, ok := VarietyOfSymbol(sym)
	if !ok {
		return "", false
	}
	if IsMainSymbol(sym) {
		return eastmoneySecid(v)
	}
	market, ok := eastmoneyMarkets[v.Exchange]
	if !ok {
		return "", false
	}
	return fmt.Sprintf("%d.%s", market, strings.ToLower(sym)), true
}

func eastmoneyKlt(period string) (int, error) {
	switch strings.TrimSpace(period) {
	case "1", "5", "15", "30", "60", "120":
		return strconv.Atoi(period)
	case "1d", "day":
		return 101, nil
	}
	return 0, fmt.Errorf("东财不支持周期 %s", period)
}

func (e *EastmoneySource) httpClient() *http.Client {
	if e.HTTP != nil {
		return e.HTTP
	}
	return &http.Client{Timeout: 20 * time.Second}
}

func (e *EastmoneySource) Minute(ctx context.Context, v Variety, period string) ([]Bar, error) {
	return e.MinuteRange(ctx, v, period, time.Time{}, 0)
}

// MinuteRange 取「截至 end 那天（含）」的分钟线；end 为零值表示最新。用于往前补 1 分钟历史。
func (e *EastmoneySource) MinuteRange(ctx context.Context, v Variety, period string, end time.Time, limit int) ([]Bar, error) {
	secid, ok := eastmoneySecid(v)
	if !ok {
		return nil, fmt.Errorf("东财不支持 %s", v.Prefix)
	}
	return e.fetchMinute(ctx, secid, v.Prefix, period, end, limit)
}

// MinuteRangeSymbol 按合约代码取分钟线：月份合约（JM2701 → 114.jm2701）也能翻页补全。
func (e *EastmoneySource) MinuteRangeSymbol(ctx context.Context, symbol, period string, end time.Time, limit int) ([]Bar, error) {
	secid, ok := eastmoneySymbolSecid(symbol)
	if !ok {
		return nil, fmt.Errorf("东财不支持 %s", symbol)
	}
	return e.fetchMinute(ctx, secid, symbol, period, end, limit)
}

// fetchMinute 拉分钟线并解析。who 只用于报错文案。
func (e *EastmoneySource) fetchMinute(ctx context.Context, secid, who, period string, end time.Time, limit int) ([]Bar, error) {
	klt, err := eastmoneyKlt(period)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = e.MinuteLimit
	}
	if limit <= 0 {
		limit = 1000
	}
	endDate := "20500101"
	if !end.IsZero() {
		endDate = end.In(locCST).Format("20060102")
	}
	lines, err := e.fetch(ctx, secid, klt, limit, endDate)
	if err != nil {
		return nil, err
	}
	out := make([]Bar, 0, len(lines))
	for _, line := range lines {
		cols := strings.Split(line, ",")
		if len(cols) < 6 {
			continue
		}
		ts, err := parseTime(cols[0])
		if err != nil {
			continue
		}
		// 东财字段序：时间,开,收,高,低,量,额
		out = append(out, Bar{
			Time: ts,
			Open: atof(cols[1]), Close: atof(cols[2]),
			High: atof(cols[3]), Low: atof(cols[4]),
			Volume: atof(cols[5]),
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("东财 %s 无数据", who)
	}
	return out, nil
}

func (e *EastmoneySource) Daily(ctx context.Context, v Variety) ([]Daily, error) {
	return e.DailyRange(ctx, v, time.Time{}, 0)
}

// DailyRange 取「截至 end（含）」的日线；end 为零值表示最新。用于往前翻页挖历史。
func (e *EastmoneySource) DailyRange(ctx context.Context, v Variety, end time.Time, limit int) ([]Daily, error) {
	secid, ok := eastmoneySecid(v)
	if !ok {
		return nil, fmt.Errorf("东财不支持 %s", v.Prefix)
	}
	if limit <= 0 {
		limit = e.DailyLimit
	}
	if limit <= 0 {
		limit = 800
	}
	endDate := "20500101"
	if !end.IsZero() {
		endDate = end.In(locCST).Format("20060102")
	}
	lines, err := e.fetch(ctx, secid, 101, limit, endDate)
	if err != nil {
		return nil, err
	}
	out := make([]Daily, 0, len(lines))
	for _, line := range lines {
		cols := strings.Split(line, ",")
		if len(cols) < 6 {
			continue
		}
		day, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(cols[0]), locCST)
		if err != nil {
			continue
		}
		out = append(out, Daily{
			Date: day,
			Open: atof(cols[1]), Close: atof(cols[2]),
			High: atof(cols[3]), Low: atof(cols[4]),
			Volume: atof(cols[5]),
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("东财 %s 无日线", v.Prefix)
	}
	return out, nil
}

func (e *EastmoneySource) fetch(ctx context.Context, secid string, klt, limit int, end string) ([]string, error) {
	base := e.BaseURL
	if base == "" {
		base = defaultEastmoneyURL
	}
	if end == "" {
		end = "20500101"
	}
	q := url.Values{
		"secid":   {secid},
		"klt":     {strconv.Itoa(klt)},
		"fqt":     {"1"},
		"lmt":     {strconv.Itoa(limit)},
		"end":     {end},
		"fields1": {"f1,f2,f3,f4,f5"},
		"fields2": {"f51,f52,f53,f54,f55,f56,f57"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Referer", "https://quote.eastmoney.com/")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36")
	resp, err := e.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("东财 HTTP %d", resp.StatusCode)
	}

	var parsed struct {
		RC   int `json:"rc"`
		Data *struct {
			Code   string   `json:"code"`
			Market int      `json:"market"`
			Klines []string `json:"klines"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("东财响应解析失败：%w", err)
	}
	if parsed.Data == nil || len(parsed.Data.Klines) == 0 {
		return nil, fmt.Errorf("东财 %s 返回空数据（rc=%d）", secid, parsed.RC)
	}
	return parsed.Data.Klines, nil
}

func atof(s string) float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return f
}
