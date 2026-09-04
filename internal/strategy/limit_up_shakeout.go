package strategy

import "github.com/wbscoder2026/stock-x/internal/store"

// LimitUpShakeout 涨停洗盘。
type LimitUpShakeout struct{}

func (LimitUpShakeout) ID() string   { return "limit_up_shakeout" }
func (LimitUpShakeout) Name() string { return "涨停洗盘" }

func (LimitUpShakeout) DefaultParams() Params {
	return Params{"limitPct": 1.095, "volMult": 2}
}

func (LimitUpShakeout) Run(bars map[string][]store.Bar, asOf string, p Params) []Pick {
	p = MergeParams(LimitUpShakeout{}.DefaultParams(), p)
	limitPct := p["limitPct"]
	volMult := p["volMult"]
	var out []Pick
	for sym, raw := range bars {
		bs := clipAsOf(raw, asOf)
		if len(bs) < 3 {
			continue
		}
		d0, yday, today := bs[len(bs)-3], bs[len(bs)-2], bs[len(bs)-1]
		if yday.Close < d0.Close*limitPct {
			continue
		}
		if today.Close >= today.Open {
			continue
		}
		if today.Volume <= yday.Volume*volMult {
			continue
		}
		if today.Low < yday.Close {
			continue
		}
		out = append(out, Pick{Symbol: sym})
	}
	return out
}
