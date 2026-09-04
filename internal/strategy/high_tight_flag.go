package strategy

import (
	"math"

	"github.com/wbscoder2026/stock-x/internal/indicator"
	"github.com/wbscoder2026/stock-x/internal/store"
)

// HighTightFlag 高窄旗形。
type HighTightFlag struct{}

func (HighTightFlag) ID() string   { return "high_tight_flag" }
func (HighTightFlag) Name() string { return "高窄旗形" }

func (HighTightFlag) DefaultParams() Params {
	return Params{
		"lookback":  40,
		"consDays":  10,
		"momentum":  1.6,
		"consRange": 1.15,
		"volShrink": 0.6,
		"hold":      0.8,
	}
}

func (HighTightFlag) Run(bars map[string][]store.Bar, asOf string, p Params) []Pick {
	p = MergeParams(HighTightFlag{}.DefaultParams(), p)
	lookback := int(p["lookback"])
	consDays := int(p["consDays"])
	momentum := p["momentum"]
	consRange := p["consRange"]
	volShrink := p["volShrink"]
	hold := p["hold"]
	var out []Pick
	for sym, raw := range bars {
		bs := clipAsOf(raw, asOf)
		if lookback <= 0 || consDays <= 0 || len(bs) < lookback || len(bs) < consDays+1 {
			continue
		}
		win := bs[len(bs)-lookback:]
		cons := bs[len(bs)-consDays:]
		hh, ll := extrema(win)
		ch, cl := extrema(cons)
		if ll <= 0 || cl <= 0 {
			continue
		}
		if hh/ll <= momentum {
			continue
		}
		if ch/cl >= consRange {
			continue
		}
		if cl < hh*hold {
			continue
		}
		prev := volumes(bs[:len(bs)-1])
		avg := indicator.SMA(prev, 20)
		a := avg[len(avg)-1]
		if math.IsNaN(a) || bs[len(bs)-1].Volume >= a*volShrink {
			continue
		}
		out = append(out, Pick{Symbol: sym})
	}
	return out
}

func extrema(bs []store.Bar) (h, l float64) {
	h, l = bs[0].High, bs[0].Low
	for _, b := range bs[1:] {
		if b.High > h {
			h = b.High
		}
		if b.Low < l {
			l = b.Low
		}
	}
	return h, l
}
