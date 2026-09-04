package strategy

import (
	"math"

	"github.com/wbscoder2026/stock-x/internal/indicator"
	"github.com/wbscoder2026/stock-x/internal/store"
)

// UptrendLimitDown 上升趋势中的跌停。
type UptrendLimitDown struct{}

func (UptrendLimitDown) ID() string   { return "uptrend_limit_down" }
func (UptrendLimitDown) Name() string { return "上升跌停" }

func (UptrendLimitDown) DefaultParams() Params {
	return Params{"maFast": 20, "maSlow": 60, "limitPct": 0.905, "volMult": 2}
}

func (UptrendLimitDown) Run(bars map[string][]store.Bar, asOf string, p Params) []Pick {
	p = MergeParams(UptrendLimitDown{}.DefaultParams(), p)
	fast := int(p["maFast"])
	slow := int(p["maSlow"])
	limitPct := p["limitPct"]
	volMult := p["volMult"]
	var out []Pick
	for sym, raw := range bars {
		bs := clipAsOf(raw, asOf)
		if fast <= 0 || slow <= 0 || len(bs) < slow || len(bs) < 2 {
			continue
		}
		cs, vs := closes(bs), volumes(bs)
		smaF, smaS := indicator.SMA(cs, fast), indicator.SMA(cs, slow)
		smaV := indicator.SMA(vs, 20)
		i, y := len(bs)-1, len(bs)-2
		if math.IsNaN(smaF[y]) || math.IsNaN(smaS[y]) || math.IsNaN(smaV[i]) {
			continue
		}
		if smaF[y] <= smaS[y] {
			continue
		}
		if bs[i].Close > bs[y].Close*limitPct {
			continue
		}
		if vs[i] <= smaV[i]*volMult {
			continue
		}
		out = append(out, Pick{Symbol: sym})
	}
	return out
}
