package strategy

import (
	"math"
	"sort"

	"github.com/wbscoder2026/stock-x/internal/indicator"
	"github.com/wbscoder2026/stock-x/internal/store"
)

// Turtle 海龟突破。
type Turtle struct{}

func (Turtle) ID() string   { return "turtle" }
func (Turtle) Name() string { return "海龟突破" }

func (Turtle) DefaultParams() Params {
	return Params{"window": 20, "minTurnover": 1e8}
}

func (Turtle) Run(bars map[string][]store.Bar, asOf string, p Params) []Pick {
	p = MergeParams(Turtle{}.DefaultParams(), p)
	window := int(p["window"])
	minTurnover := p["minTurnover"]
	var out []Pick
	for sym, raw := range bars {
		bs := clipAsOf(raw, asOf)
		if window <= 0 || len(bs) < window+1 {
			continue
		}
		today, yday := bs[len(bs)-1], bs[len(bs)-2]
		peak := indicator.RollingMax(highs(bs[:len(bs)-1]), window)
		mx := peak[len(peak)-1]
		if math.IsNaN(mx) || today.Close <= mx {
			continue
		}
		if today.Turnover <= minTurnover {
			continue
		}
		if today.Close <= today.Open || today.Close <= yday.Close {
			continue
		}
		out = append(out, Pick{Symbol: sym, Extra: map[string]float64{"turnover": today.Turnover}})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Extra["turnover"] != out[j].Extra["turnover"] {
			return out[i].Extra["turnover"] > out[j].Extra["turnover"]
		}
		return out[i].Symbol < out[j].Symbol
	})
	return out
}
