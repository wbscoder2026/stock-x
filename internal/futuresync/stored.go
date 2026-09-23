// Package futuresync 期货历史数据的本地化：落库（增量同步）、深挖日线、以及「本地优先」数据源。
package futuresync

import (
	"context"
	"fmt"

	"github.com/wbscoder2026/stock-x/internal/futures"
	"github.com/wbscoder2026/stock-x/internal/store"
)

// 日线在本地库里的周期标记
const DailyPeriod = "1d"

// 回测/研究从本地取多少根（够 EMA/ATR/Donchian 与回测用即可）
const (
	DefaultMinuteBars = 5000
	DefaultDailyBars  = 2000
)

// StoredSource 本地优先数据源：先读内存，再读 SQLite，都没有才走网络（可选回写，越用越厚）。
// 监控不能用它（要最新行情），它给回测/研究用。
type StoredSource struct {
	Store      *store.Store
	Live       futures.BarSource
	Cache      *BarCache
	Warm       bool
	MinuteBars int
	DailyBars  int
}

func NewStoredSource(st *store.Store, live futures.BarSource) *StoredSource {
	return &StoredSource{Store: st, Live: live, Cache: NewBarCache(), Warm: true}
}

func (s *StoredSource) Name() string {
	if s.Live != nil {
		return "store+" + s.Live.Name()
	}
	return "store"
}

func (s *StoredSource) Minute(ctx context.Context, v futures.Variety, period string) ([]futures.Bar, error) {
	symbol := futures.MainSymbol(v)
	limit := s.minuteBars()
	if rows, ok := s.cached(symbol, period, limit); ok {
		return barsFromRows(rows), nil
	}
	if s.Store != nil {
		rows, err := s.Store.FuturesBars(symbol, period, limit)
		if err != nil {
			return nil, err
		}
		if len(rows) > 0 {
			s.remember(symbol, period, rows)
			return barsFromRows(rows), nil
		}
	}
	if s.Live == nil {
		return nil, fmt.Errorf("本地没有 %s %s 分钟线，且未配置网络源", symbol, period)
	}
	bars, err := s.Live.Minute(ctx, v, period)
	if err != nil {
		return nil, err
	}
	rows := rowsFromBars(symbol, period, bars)
	if s.Warm && s.Store != nil {
		_, _ = s.Store.UpsertFuturesBars(rows)
	}
	s.remember(symbol, period, rows)
	return bars, nil
}

func (s *StoredSource) Daily(ctx context.Context, v futures.Variety) ([]futures.Daily, error) {
	symbol := futures.MainSymbol(v)
	limit := s.dailyBars()
	if rows, ok := s.cached(symbol, DailyPeriod, limit); ok {
		return daysFromRows(rows), nil
	}
	if s.Store != nil {
		rows, err := s.Store.FuturesBars(symbol, DailyPeriod, limit)
		if err != nil {
			return nil, err
		}
		if len(rows) > 0 {
			s.remember(symbol, DailyPeriod, rows)
			return daysFromRows(rows), nil
		}
	}
	if s.Live == nil {
		return nil, fmt.Errorf("本地没有 %s 日线，且未配置网络源", symbol)
	}
	days, err := s.Live.Daily(ctx, v)
	if err != nil {
		return nil, err
	}
	rows := rowsFromDays(symbol, days)
	if s.Warm && s.Store != nil {
		_, _ = s.Store.UpsertFuturesBars(rows)
	}
	s.remember(symbol, DailyPeriod, rows)
	return days, nil
}

func (s *StoredSource) cached(symbol, period string, limit int) ([]store.FuturesBar, bool) {
	if s == nil || s.Cache == nil {
		return nil, false
	}
	rows, ok := s.Cache.Get(symbol, period)
	if !ok {
		return nil, false
	}
	return tailBars(rows, limit), true
}

func (s *StoredSource) remember(symbol, period string, rows []store.FuturesBar) {
	if s == nil {
		return
	}
	s.Cache.Fill(symbol, period, rows)
}

func (s *StoredSource) minuteBars() int {
	if s.MinuteBars > 0 {
		return s.MinuteBars
	}
	return DefaultMinuteBars
}

func (s *StoredSource) dailyBars() int {
	if s.DailyBars > 0 {
		return s.DailyBars
	}
	return DefaultDailyBars
}

// ---------------------------------------------------------------- 类型转换（store ↔ futures）

func rowsFromBars(symbol, period string, bars []futures.Bar) []store.FuturesBar {
	out := make([]store.FuturesBar, 0, len(bars))
	for _, b := range bars {
		if b.Time.IsZero() {
			continue
		}
		out = append(out, store.FuturesBar{
			Symbol: symbol, Period: period, Time: b.Time,
			Open: b.Open, High: b.High, Low: b.Low, Close: b.Close,
			Volume: b.Volume, Hold: b.Hold,
		})
	}
	return out
}

func barsFromRows(rows []store.FuturesBar) []futures.Bar {
	out := make([]futures.Bar, 0, len(rows))
	for _, r := range rows {
		out = append(out, futures.Bar{
			Time: r.Time, Open: r.Open, High: r.High, Low: r.Low, Close: r.Close,
			Volume: r.Volume, Hold: r.Hold,
		})
	}
	return out
}

func rowsFromDays(symbol string, days []futures.Daily) []store.FuturesBar {
	out := make([]store.FuturesBar, 0, len(days))
	for _, d := range days {
		if d.Date.IsZero() {
			continue
		}
		out = append(out, store.FuturesBar{
			Symbol: symbol, Period: DailyPeriod, Time: d.Date,
			Open: d.Open, High: d.High, Low: d.Low, Close: d.Close,
			Volume: d.Volume, Hold: d.Hold,
		})
	}
	return out
}

func daysFromRows(rows []store.FuturesBar) []futures.Daily {
	out := make([]futures.Daily, 0, len(rows))
	for _, r := range rows {
		out = append(out, futures.Daily{
			Date: r.Time, Open: r.Open, High: r.High, Low: r.Low, Close: r.Close,
			Volume: r.Volume, Hold: r.Hold,
		})
	}
	return out
}
