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
}

type Params struct {
	Symbol    string  `json:"symbol"`
	Period    string  `json:"period"`
	ORB       int     `json:"orb"`
	Donchian  int     `json:"donchian"`
	ATRPeriod int     `json:"atr_period"`
	ATRK      float64 `json:"atr_k"`
	VolRatio  float64 `json:"vol_ratio"`
	HoldBars  int     `json:"hold_bars"`
}

func DefaultParams() Params {
	return Params{
		Symbol: "JM0", Period: "5",
		ORB: 30, Donchian: 20, ATRPeriod: 14,
		ATRK: 0.25, VolRatio: 1.5, HoldBars: 6,
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
	if p.ATRK <= 0 {
		p.ATRK = d.ATRK
	}
	if p.VolRatio <= 0 {
		p.VolRatio = d.VolRatio
	}
	if p.HoldBars <= 0 {
		p.HoldBars = d.HoldBars
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
	Return     float64 `json:"return"`
	Correct    bool    `json:"correct"`
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
	Symbol    string     `json:"symbol"`
	Period    string     `json:"period"`
	WinRate   float64    `json:"win_rate"`
	AvgReturn float64    `json:"avg_return"`
	Trades    int        `json:"trades"`
	Correct   int        `json:"correct"`
	Items     []Outcome  `json:"items"`
	Bars      []KlineBar `json:"bars"`
}
