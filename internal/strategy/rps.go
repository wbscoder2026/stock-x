package strategy

import (
	"math"
	"sort"

	"github.com/wbscoder2026/stock-x/internal/indicator"
	"github.com/wbscoder2026/stock-x/internal/store"
)

// RPSBreakout RPS 突破。
type RPSBreakout struct{}

func (RPSBreakout) ID() string   { return "rps_breakout" }
func (RPSBreakout) Name() string { return "RPS突破" }

func (RPSBreakout) DefaultParams() Params {
	return Params{"period": 120, "threshold": 90, "nearHigh": 0.90}
}

func (RPSBreakout) Run(bars map[string][]store.Bar, asOf string, p Params) []Pick {
	p = MergeParams(RPSBreakout{}.DefaultParams(), p)
	period := int(p["period"])
	threshold := p["threshold"]
	nearHigh := p["nearHigh"]
	if period <= 0 {
		return nil
	}
	type row struct {
		symbol           string
		pct, close, maxH float64
	}
	var rows []row
	for sym, raw := range bars {
		bs := clipAsOf(raw, asOf)
		if len(bs) < period+1 {
			continue
		}
		past := bs[len(bs)-1-period].Close
		if past == 0 {
			continue
		}
		today := bs[len(bs)-1]
		rm := indicator.RollingMax(highs(bs), period)
		maxH := rm[len(rm)-1]
		if math.IsNaN(maxH) {
			continue
		}
		rows = append(rows, row{
			symbol: sym,
			pct:    today.Close/past - 1,
			close:  today.Close,
			maxH:   maxH,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].pct != rows[j].pct {
			return rows[i].pct < rows[j].pct
		}
		return rows[i].symbol < rows[j].symbol
	})
	n := len(rows)
	var out []Pick
	for rank, r := range rows {
		rps := 100.0
		if n > 1 {
			rps = float64(rank) / float64(n-1) * 100
		}
		if rps < threshold || r.close < r.maxH*nearHigh {
			continue
		}
		out = append(out, Pick{Symbol: r.symbol, Extra: map[string]float64{"rps": rps, "pct": r.pct}})
	}
	return out
}
