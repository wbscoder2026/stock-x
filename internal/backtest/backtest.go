package backtest

import (
	"fmt"
	"math"

	"github.com/wbscoder2026/stock-x/internal/store"
	"github.com/wbscoder2026/stock-x/internal/strategy"
)

const maxLookbackDays = 60

type Trade struct {
	Date   string  `json:"date"`
	Symbol string  `json:"symbol"`
	Ret    float64 `json:"ret"`
}

type Result struct {
	WinRate   float64 `json:"win_rate"`
	AvgReturn float64 `json:"avg_return"`
	Trades    []Trade `json:"trades"`
}

// Run 用策略在每个 asOf 选出的票：当日收盘买入，holdDays 个交易日后收盘卖出。
func Run(st *store.Store, strat strategy.Strategy, params strategy.Params, from, to string, holdDays int) (Result, error) {
	if strat == nil {
		return Result{}, fmt.Errorf("策略为空")
	}
	if holdDays <= 0 {
		holdDays = 5
	}
	days, err := st.TradingDays(from, to)
	if err != nil {
		return Result{}, err
	}
	if len(days) > maxLookbackDays {
		days = days[len(days)-maxLookbackDays:]
	}
	all, err := st.LoadMarketAsOf("")
	if err != nil {
		return Result{}, err
	}
	p := strategy.MergeParams(strat.DefaultParams(), params)
	var trades []Trade
	for _, asOf := range days {
		clipped := clipMarket(all, asOf)
		picks := strat.Run(clipped, asOf, p)
		for _, pk := range picks {
			buy, sell, ok := buySellClose(all[pk.Symbol], asOf, holdDays)
			if !ok || buy <= 0 {
				continue
			}
			ret := sell/buy - 1
			trades = append(trades, Trade{Date: asOf, Symbol: pk.Symbol, Ret: ret})
		}
	}
	var win int
	var sum float64
	for _, t := range trades {
		sum += t.Ret
		if t.Ret > 0 {
			win++
		}
	}
	out := Result{Trades: trades}
	if n := len(trades); n > 0 {
		out.WinRate = float64(win) / float64(n)
		out.AvgReturn = sum / float64(n)
	}
	return out, nil
}

func clipMarket(all map[string][]store.Bar, asOf string) map[string][]store.Bar {
	out := make(map[string][]store.Bar, len(all))
	for sym, bars := range all {
		n := len(bars)
		for n > 0 && bars[n-1].Date > asOf {
			n--
		}
		if n > 0 {
			out[sym] = bars[:n]
		}
	}
	return out
}

func buySellClose(bars []store.Bar, asOf string, holdDays int) (buy, sell float64, ok bool) {
	idx := -1
	for i, b := range bars {
		if b.Date == asOf {
			idx = i
			break
		}
	}
	if idx < 0 {
		return 0, 0, false
	}
	j := idx + holdDays
	if j >= len(bars) {
		return 0, 0, false
	}
	if bars[idx].Close <= 0 || bars[j].Close <= 0 || math.IsNaN(bars[idx].Close) {
		return 0, 0, false
	}
	return bars[idx].Close, bars[j].Close, true
}
