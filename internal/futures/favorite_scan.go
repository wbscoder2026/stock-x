package futures

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"
)

// FavConfig 一条要拿去全市场试的收藏配置。
type FavConfig struct {
	ID     int64
	Name   string
	Params Params
}

// SymbolStat 一条配置在单个品种上的回测结果。没有成交的品种不进平均值。
type SymbolStat struct {
	Symbol       string  `json:"symbol"`
	Name         string  `json:"name"`
	Trades       int     `json:"trades"`
	Correct      int     `json:"correct"`
	WinRate      float64 `json:"win_rate"`
	AvgReturn    float64 `json:"avg_return"`
	AvgR         float64 `json:"avg_r"`
	ProfitFactor float64 `json:"profit_factor"`
	Error        string  `json:"error,omitempty"`
}

// ConfigScanSummary 一条配置扫完全部品种后的汇总。
// 胜率/收益的「平均」是有成交品种的等权平均，用来看规则泛不泛用；
// 「加权」按成交笔数，避免一个品种只有 1 笔就把等权平均带偏时无从对照。
type ConfigScanSummary struct {
	ID              int64        `json:"id"`
	Name            string       `json:"name"`
	Params          Params       `json:"params"`
	Symbols         int          `json:"symbols"`
	Covered         int          `json:"covered"`
	Reliable        int          `json:"reliable"`
	NoSample        int          `json:"no_sample"`
	Failed          int          `json:"failed"`
	TotalTrades     int          `json:"total_trades"`
	TotalCorrect    int          `json:"total_correct"`
	AvgWinRate      float64      `json:"avg_win_rate"`
	AvgReturn       float64      `json:"avg_return"`
	AvgR            float64      `json:"avg_r"`
	AvgProfitFactor float64      `json:"avg_profit_factor"`
	PooledWinRate   float64      `json:"pooled_win_rate"`
	PooledAvgReturn float64      `json:"pooled_avg_return"`
	Details         []SymbolStat `json:"details"`
}

// OverallScan 多条配置放在一起看的总平均：每条规则一票（先各自按品种等权）。
type OverallScan struct {
	Configs         int     `json:"configs"`
	AvgWinRate      float64 `json:"avg_win_rate"`
	AvgReturn       float64 `json:"avg_return"`
	AvgR            float64 `json:"avg_r"`
	AvgProfitFactor float64 `json:"avg_profit_factor"`
	PooledWinRate   float64 `json:"pooled_win_rate"`
	PooledAvgReturn float64 `json:"pooled_avg_return"`
	TotalTrades     int     `json:"total_trades"`
}

// AcrossResult 一次「若干配置 × 全部品种」的扫描结果。
type AcrossResult struct {
	Symbols   int                 `json:"symbols"`
	MinTrades int                 `json:"min_trades"`
	Configs   []ConfigScanSummary `json:"configs"`
	Overall   OverallScan         `json:"overall"`
	Skipped   []string            `json:"skipped,omitempty"`
	ElapsedMS int64               `json:"elapsed_ms"`
}

const favScanMaxWorkers = 8

// ScanAcross 用一组配置去扫给定品种。同一品种的同一周期只取一次行情，再套到该周期的每条配置上。
// 某个品种取数失败只记入跳过，不让整次扫描失败。
func ScanAcross(ctx context.Context, src BarSource, varieties []Variety, configs []FavConfig, workers, minTrades int) (AcrossResult, error) {
	if src == nil {
		return AcrossResult{}, fmt.Errorf("没有行情源")
	}
	if len(configs) == 0 {
		return AcrossResult{}, fmt.Errorf("没有要扫描的配置")
	}
	if len(varieties) == 0 {
		return AcrossResult{}, fmt.Errorf("没有品种")
	}
	if minTrades <= 0 {
		minTrades = 1
	}
	if workers <= 0 {
		workers = 4
	}
	if workers > favScanMaxWorkers {
		workers = favScanMaxWorkers
	}

	prepared := make([]FavConfig, len(configs))
	periods := map[string]struct{}{}
	for i, cfg := range configs {
		p := mergeParams(cfg.Params)
		cfg.Params = p
		prepared[i] = cfg
		periods[p.Period] = struct{}{}
	}
	periodList := make([]string, 0, len(periods))
	for period := range periods {
		periodList = append(periodList, period)
	}

	start := time.Now()
	loaded := make([]varietyOutcome, len(varieties))
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i, v := range varieties {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, v Variety) {
			defer wg.Done()
			defer func() { <-sem }()
			loaded[i] = evalVariety(ctx, src, v, prepared, periodList)
		}(i, v)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return AcrossResult{}, err
	}

	out := AcrossResult{
		Symbols:   len(varieties),
		MinTrades: minTrades,
		Configs:   make([]ConfigScanSummary, len(prepared)),
	}
	for i, cfg := range prepared {
		rows := make([]SymbolStat, 0, len(varieties))
		for _, item := range loaded {
			if i < len(item.byConfig) {
				rows = append(rows, item.byConfig[i])
			}
		}
		out.Configs[i] = summaryFrom(cfg, rows, minTrades)
	}
	for _, item := range loaded {
		if item.skipped != "" {
			out.Skipped = append(out.Skipped, item.skipped)
		}
	}
	out.Overall = overallFrom(out.Configs)
	out.ElapsedMS = time.Since(start).Milliseconds()
	return out, nil
}

type varietyOutcome struct {
	skipped  string
	byConfig []SymbolStat
}

func evalVariety(ctx context.Context, src BarSource, v Variety, configs []FavConfig, periods []string) varietyOutcome {
	symbol := MainSymbol(v)
	out := varietyOutcome{byConfig: make([]SymbolStat, len(configs))}
	blank := func(err string) SymbolStat {
		return SymbolStat{Symbol: symbol, Name: v.Name, Error: err}
	}
	if ctx.Err() != nil {
		msg := ctx.Err().Error()
		out.skipped = v.Name + " " + symbol + ": " + msg
		for i := range out.byConfig {
			out.byConfig[i] = blank(msg)
		}
		return out
	}
	daily, err := src.Daily(ctx, v)
	if err != nil {
		out.skipped = fmt.Sprintf("%s %s: %s", v.Name, symbol, err.Error())
		for i := range out.byConfig {
			out.byConfig[i] = blank(err.Error())
		}
		return out
	}
	minutes := map[string][]Bar{}
	periodErr := map[string]string{}
	for _, period := range periods {
		bars, err := src.Minute(ctx, v, period)
		if err != nil {
			periodErr[period] = err.Error()
			continue
		}
		if err := checkMinuteDailyAgree(v.Prefix, bars, daily); err != nil {
			periodErr[period] = err.Error()
			continue
		}
		minutes[period] = bars
	}
	for i, cfg := range configs {
		p := cfg.Params
		p.Symbol = symbol
		if msg, ok := periodErr[p.Period]; ok {
			out.byConfig[i] = blank(msg)
			continue
		}
		res := runBacktest(minutes[p.Period], daily, p, false)
		out.byConfig[i] = SymbolStat{
			Symbol: symbol, Name: v.Name,
			Trades: res.Trades, Correct: res.Correct,
			WinRate: res.WinRate, AvgReturn: res.AvgReturn, AvgR: res.AvgR,
			ProfitFactor: res.ProfitFactor,
		}
	}
	return out
}

func summaryFrom(cfg FavConfig, rows []SymbolStat, minTrades int) ConfigScanSummary {
	agg := summarizeSymbolStats(rows, minTrades)
	return ConfigScanSummary{
		ID: cfg.ID, Name: cfg.Name, Params: cfg.Params,
		Symbols: len(rows),
		Covered: agg.covered, Reliable: agg.reliable, NoSample: agg.noSample, Failed: agg.failed,
		TotalTrades: agg.totalTrades, TotalCorrect: agg.totalCorrect,
		AvgWinRate: agg.avgWin, AvgReturn: agg.avgRet, AvgR: agg.avgR, AvgProfitFactor: agg.avgPF,
		PooledWinRate: agg.pooledWin, PooledAvgReturn: agg.pooledRet,
		Details: rows,
	}
}

type symbolAgg struct {
	covered, reliable, noSample, failed int
	totalTrades, totalCorrect           int
	avgWin, avgRet, avgR, avgPF         float64
	pooledWin, pooledRet                float64
}

func summarizeSymbolStats(rows []SymbolStat, minTrades int) symbolAgg {
	if minTrades <= 0 {
		minTrades = 1
	}
	var agg symbolAgg
	var winSum, retSum, rSum, pfSum, weighted float64
	pfN := 0
	for _, row := range rows {
		if row.Error != "" {
			agg.failed++
			continue
		}
		if row.Trades <= 0 {
			agg.noSample++
			continue
		}
		agg.covered++
		if row.Trades >= minTrades {
			agg.reliable++
		}
		agg.totalTrades += row.Trades
		agg.totalCorrect += row.Correct
		winSum += row.WinRate
		retSum += row.AvgReturn
		rSum += row.AvgR
		weighted += row.AvgReturn * float64(row.Trades)
		if row.ProfitFactor > 0 && !math.IsNaN(row.ProfitFactor) && !math.IsInf(row.ProfitFactor, 0) {
			pfSum += row.ProfitFactor
			pfN++
		}
	}
	if agg.covered > 0 {
		n := float64(agg.covered)
		agg.avgWin = winSum / n
		agg.avgRet = retSum / n
		agg.avgR = rSum / n
	}
	if pfN > 0 {
		agg.avgPF = pfSum / float64(pfN)
	}
	if agg.totalTrades > 0 {
		agg.pooledWin = float64(agg.totalCorrect) / float64(agg.totalTrades)
		agg.pooledRet = weighted / float64(agg.totalTrades)
	}
	return agg
}

func overallFrom(configs []ConfigScanSummary) OverallScan {
	var out OverallScan
	var winSum, retSum, rSum, pfSum, weighted float64
	pfN := 0
	correct := 0
	for _, cfg := range configs {
		if cfg.Covered == 0 {
			continue
		}
		out.Configs++
		winSum += cfg.AvgWinRate
		retSum += cfg.AvgReturn
		rSum += cfg.AvgR
		out.TotalTrades += cfg.TotalTrades
		correct += cfg.TotalCorrect
		weighted += cfg.PooledAvgReturn * float64(cfg.TotalTrades)
		if cfg.AvgProfitFactor > 0 && !math.IsNaN(cfg.AvgProfitFactor) && !math.IsInf(cfg.AvgProfitFactor, 0) {
			pfSum += cfg.AvgProfitFactor
			pfN++
		}
	}
	if out.Configs > 0 {
		n := float64(out.Configs)
		out.AvgWinRate = winSum / n
		out.AvgReturn = retSum / n
		out.AvgR = rSum / n
	}
	if pfN > 0 {
		out.AvgProfitFactor = pfSum / float64(pfN)
	}
	if out.TotalTrades > 0 {
		out.PooledWinRate = float64(correct) / float64(out.TotalTrades)
		out.PooledAvgReturn = weighted / float64(out.TotalTrades)
	}
	return out
}
