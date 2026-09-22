package futures

import "time"

const (
	DirUp   = "向上突破"
	DirDown = "向下跌破"
	KindR   = "R"
	KindS   = "S"
)

var locCST = time.FixedZone("CST", 8*3600)

type Bar struct {
	Time                   time.Time
	Open, High, Low, Close float64
	Volume, Hold           float64
}

type Daily struct {
	Date                                         time.Time
	Open, High, Low, Close, Volume, Hold, Settle float64
}

type Level struct {
	Name  string  `json:"name"`
	Kind  string  `json:"kind"`
	Value float64 `json:"value"`
}

type Event struct {
	Time       time.Time
	Direction  string
	Level      string
	Close      float64
	Volume     int64
	LevelPrice float64
	ATR        float64 // 突破当根的 ATR（推荐止损/止盈用；数据不足时为 0）
}

// DefaultRR 默认盈亏比：止盈距离 = 止损距离 × RR。
const DefaultRR = 1.5

// DefaultStopATR 默认止损距离 = 该倍数 × ATR（1 = 一倍 ATR）。
// 大周期（60 分钟）的 ATR 天然更大，想要更紧的止损可以调小这个倍数。
const DefaultStopATR = 1.0

type Params struct {
	Symbol    string  `json:"symbol"`
	Period    string  `json:"period"`
	ORB       int     `json:"orb"`
	Donchian  int     `json:"donchian"`
	ATRPeriod int     `json:"atr_period"`
	ATRK      float64 `json:"atr_k"`
	VolRatio  float64 `json:"vol_ratio"`
	HoldBars  int     `json:"hold_bars"`
	StopATR   float64 `json:"stop_atr"` // 止损 = 入场 ∓ 该倍数 × ATR
	// NoOvernight 日内策略：当日「日盘」收盘前必须平仓，不持隔夜（夜盘属次日交易时段，也不持有）。
	NoOvernight bool `json:"no_overnight"`
	// From / To 回测信号的时间范围（含边界）；空 = 不限。
	// 支持 "2006-01-02"（整天）或 "2006-01-02 15:04"。
	From string  `json:"from"`
	To   string  `json:"to"`
	RR   float64 `json:"rr"`
}

func DefaultParams() Params {
	return Params{
		Symbol: "JM0", Period: "5",
		ORB: 30, Donchian: 20, ATRPeriod: 14,
		ATRK: 0.25, VolRatio: 1.5, HoldBars: 6,
		StopATR: DefaultStopATR, RR: DefaultRR,
	}
}

func mergeParams(p Params) Params {
	d := DefaultParams()
	if p.Symbol == "" {
		p.Symbol = d.Symbol
	}
	if p.Period == "" {
		p.Period = d.Period
	}
	if p.ORB <= 0 {
		p.ORB = d.ORB
	}
	if p.Donchian <= 0 {
		p.Donchian = d.Donchian
	}
	if p.ATRPeriod <= 0 {
		p.ATRPeriod = d.ATRPeriod
	}
	// 浮点参数都要防 NaN：NaN <= 0 为 false 会原样漏下去，
	// 一旦进了价格，encoding/json 会拒绝编码 NaN → 整个响应 500。
	if !finite(p.ATRK) || p.ATRK <= 0 {
		p.ATRK = d.ATRK
	}
	if !finite(p.VolRatio) || p.VolRatio <= 0 {
		p.VolRatio = d.VolRatio
	}
	if p.HoldBars <= 0 {
		p.HoldBars = d.HoldBars
	}
	if !finite(p.RR) || p.RR <= 0 {
		p.RR = d.RR
	}
	if !finite(p.StopATR) || p.StopATR <= 0 {
		p.StopATR = d.StopATR
	}
	return p
}

type BarView struct {
	Time   string  `json:"time"`
	Close  float64 `json:"close"`
	Volume int64   `json:"volume"`
	Hold   float64 `json:"hold"`
}

type EventDTO struct {
	Time       string  `json:"time"`
	Direction  string  `json:"direction"`
	Level      string  `json:"level"`
	Close      float64 `json:"close"`
	Volume     int64   `json:"volume"`
	LevelPrice float64 `json:"level_price"`
}

type Snapshot struct {
	Symbol    string     `json:"symbol"`
	Day       string     `json:"day"`
	Upcoming  bool       `json:"upcoming"`
	Trend     string     `json:"trend"`
	Last5     *BarView   `json:"last_5,omitempty"`
	Last15    *BarView   `json:"last_15,omitempty"`
	Levels    []Level    `json:"levels"`
	VWAP      *float64   `json:"vwap,omitempty"`
	Position  string     `json:"position"`
	Events5   []EventDTO `json:"events_5"`
	Events15  []EventDTO `json:"events_15"`
	Resonance []string   `json:"resonance"`
	Bars5     []KlineBar `json:"bars_5"`
	Bars15    []KlineBar `json:"bars_15"`
	// BarsPeriod 当前请求级别（非 5/15 分钟时）的 K 线，给前端画对应级别的图
	BarsPeriod []KlineBar `json:"bars_period,omitempty"`
}

type Outcome struct {
	Time       string  `json:"time"`
	Direction  string  `json:"direction"`
	Level      string  `json:"level"`
	Close      float64 `json:"close"`
	Volume     int64   `json:"volume"`
	LevelPrice float64 `json:"level_price"`
	ExitTime   string  `json:"exit_time"`
	ExitPrice  float64 `json:"exit_price"`
	Return     float64 `json:"return"` // 已按突破方向折算（做空跌了是正的）
	Correct    bool    `json:"correct"`
	ExitReason string  `json:"exit_reason"` // 止损 / 止盈 / 持有到期
	StopPrice  float64 `json:"stop_price"`  // stopATR×ATR 止损（已按报价单位对齐；0 = ATR 不足）
	TPPrice    float64 `json:"tp_price"`    // 止损距离×盈亏比 止盈
	R          float64 `json:"r_multiple"`  // 以「止损距离」风险为 1R 的收益倍数
	StopATR    float64 `json:"stop_atr"`    // 本次用的止损 ATR 倍数
	TickSize   float64 `json:"tick_size"`   // 该品种最小变动价位（展示用）
}

type KlineBar struct {
	Time   string  `json:"time"`
	Open   float64 `json:"open"`
	High   float64 `json:"high"`
	Low    float64 `json:"low"`
	Close  float64 `json:"close"`
	Volume float64 `json:"volume"`
}

type Result struct {
	Symbol       string     `json:"symbol"`
	Period       string     `json:"period"`
	WinRate      float64    `json:"win_rate"`
	AvgReturn    float64    `json:"avg_return"`
	AvgWin       float64    `json:"avg_win"`
	AvgLoss      float64    `json:"avg_loss"`
	ProfitFactor float64    `json:"profit_factor"` // 总盈利 / 总亏损；0 = 没有亏损单（前端显示「-」）
	AvgR         float64    `json:"avg_r"`         // 期望 R
	Trades       int        `json:"trades"`
	Correct      int        `json:"correct"`
	StopExits    int        `json:"stop_exits"`
	TPExits      int        `json:"tp_exits"`
	HoldExits    int        `json:"hold_exits"`
	EODExits     int        `json:"eod_exits"`   // 日内收盘平仓（禁止隔夜时）
	SkippedEOD   int        `json:"skipped_eod"` // 因禁止隔夜而不可交易、被跳过的信号数
	From         string     `json:"from"`        // 实际生效的起始时间（空 = 不限）
	To           string     `json:"to"`          // 实际生效的结束时间（空 = 不限）
	Items        []Outcome  `json:"items"`
	Bars         []KlineBar `json:"bars"`
}
