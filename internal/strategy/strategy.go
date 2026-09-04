package strategy

import "github.com/wbscoder2026/stock-x/internal/store"

// Params 策略参数；缺 key 时用 DefaultParams。
type Params map[string]float64

// Pick 一只入选标的。
type Pick struct {
	Symbol string
	Extra  map[string]float64
}

// Strategy 选股策略。
type Strategy interface {
	ID() string
	Name() string
	DefaultParams() Params
	// bars: 全市场 symbol -> 按日期升序且 date<=asOf 的 K 线
	Run(bars map[string][]store.Bar, asOf string, p Params) []Pick
}

// MergeParams 用 override 覆盖 def；缺 key 保留默认。
func MergeParams(def, override Params) Params {
	out := make(Params, len(def)+len(override))
	for k, v := range def {
		out[k] = v
	}
	for k, v := range override {
		out[k] = v
	}
	return out
}

func param(p Params, key string, def float64) float64 {
	if p != nil {
		if v, ok := p[key]; ok {
			return v
		}
	}
	return def
}

// clipAsOf 取 date<=asOf 的前缀；asOf 为空则原样返回。
func clipAsOf(bars []store.Bar, asOf string) []store.Bar {
	if asOf == "" {
		return bars
	}
	n := len(bars)
	for n > 0 && bars[n-1].Date > asOf {
		n--
	}
	return bars[:n]
}

func closes(bars []store.Bar) []float64 {
	xs := make([]float64, len(bars))
	for i, b := range bars {
		xs[i] = b.Close
	}
	return xs
}

func highs(bars []store.Bar) []float64 {
	xs := make([]float64, len(bars))
	for i, b := range bars {
		xs[i] = b.High
	}
	return xs
}

func lows(bars []store.Bar) []float64 {
	xs := make([]float64, len(bars))
	for i, b := range bars {
		xs[i] = b.Low
	}
	return xs
}

func volumes(bars []store.Bar) []float64 {
	xs := make([]float64, len(bars))
	for i, b := range bars {
		xs[i] = b.Volume
	}
	return xs
}

func symbolsOf(picks []Pick) map[string]bool {
	m := make(map[string]bool, len(picks))
	for _, p := range picks {
		m[p.Symbol] = true
	}
	return m
}

var builtins []Strategy

// Register 注册内置策略（策略文件 init 中调用）。
func Register(s Strategy) {
	builtins = append(builtins, s)
}

// All 返回已注册的内置策略。
func All() []Strategy {
	return builtins
}
