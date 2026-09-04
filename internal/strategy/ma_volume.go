package strategy

import (
	"math"

	"github.com/wbscoder2026/stock-x/internal/indicator"
	"github.com/wbscoder2026/stock-x/internal/store"
)

// MAVolume 均线放量（快线上穿慢线）。
type MAVolume struct{}

func (MAVolume) ID() string   { return "ma_volume" }
func (MAVolume) Name() string { return "均线放量" }

func (MAVolume) DefaultParams() Params {
	return Params{"maFast": 5, "maSlow": 20, "volMult": 1.5}
}

func (MAVolume) Run(bars map[string][]store.Bar, asOf string, p Params) []Pick {
	p = MergeParams(MAVolume{}.DefaultParams(), p)
	fast := int(p["maFast"])
	slow := int(p["maSlow"])
	volMult := p["volMult"]
	var out []Pick
	for sym, raw := range bars {
		bs := clipAsOf(raw, asOf)
		if fast <= 0 || slow <= 0 || len(bs) < slow || len(bs) < 2 {
			continue
		}
		cs, vs := closes(bs), volumes(bs)
		smaF, smaS := indicator.SMA(cs, fast), indicator.SMA(cs, slow)
		smaV := indicator.SMA(vs, slow)
		i, y := len(bs)-1, len(bs)-2
		if math.IsNaN(smaF[y]) || math.IsNaN(smaS[y]) || math.IsNaN(smaF[i]) || math.IsNaN(smaS[i]) || math.IsNaN(smaV[i]) {
			continue
		}
		if !(smaF[y] < smaS[y] && smaF[i] > smaS[i]) {
			continue
		}
		if vs[i] <= smaV[i]*volMult {
			continue
		}
		out = append(out, Pick{Symbol: sym, Extra: map[string]float64{"vol": vs[i]}})
	}
	return out
}
