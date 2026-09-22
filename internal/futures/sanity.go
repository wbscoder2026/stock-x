package futures

import (
	"fmt"
	"sort"
)

// 上游数据健康度校验
//
// 背景：行情源的分钟线偶尔会给出「量级错误」的数据，而且是**解析成功**的坏数据——
// 实测东财主连（114.jmm / klt=60）把历史段返回成 1153 万量级、最新段才是 1525，
// 97% 的 K 线是两个数量级以外的垃圾值。这类数据不会报错，但会把 ATR、ATR缓冲、
// 推荐止损、回测统计全部算废（止损距离只剩价格的 0.005%）。
//
// 所以：源返回的数据必须先过健康度检查，判脏就当作该源本次失败 → 链路自动降级到下一个源。

const (
	// barScaleGapRatio 相邻价格簇的相对间隔阈值：2 倍以上 = 量级混叠（真实行情里做不到）
	barScaleGapRatio = 2.0
	// barScaleMinShare 两簇各自至少要占的比例，才认定为「混叠」；
	// 低于这个比例的孤立坏 tick 不算混叠（不做整段丢弃，避免误杀）
	barScaleMinShare = 0.02
	// barScaleMinBars 样本太少不做判断
	barScaleMinBars = 20
	// minuteDailyMaxDrift 分钟线尾价与日线尾价允许的最大偏差（同一品种的现价不该有两套）
	minuteDailyMaxDrift = 0.5
)

// checkBarScale 检查一组价格是否「量级连续」。
// 单调趋势不会被误判（趋势的排序后相邻间隔很小），只有明显分簇才会报错。
func checkBarScale(prefix string, closes []float64) error {
	if len(closes) < barScaleMinBars {
		return nil
	}
	sorted := make([]float64, len(closes))
	copy(sorted, closes)
	sort.Float64s(sorted)

	bestGap, bestAt := 0.0, -1
	for i := 1; i < len(sorted); i++ {
		prev, cur := sorted[i-1], sorted[i]
		if !finite(prev) || !finite(cur) || prev <= 0 || cur <= 0 {
			return fmt.Errorf("%s 价格非法（%.4f / %.4f）", prefix, prev, cur)
		}
		if gap := cur/prev - 1; gap > bestGap {
			bestGap, bestAt = gap, i
		}
	}
	if bestAt < 0 || bestGap < barScaleGapRatio-1 {
		return nil // 价格连续，正常
	}
	lowN, highN := bestAt, len(sorted)-bestAt
	minN := int(float64(len(sorted)) * barScaleMinShare)
	if minN < 2 {
		minN = 2
	}
	if lowN < minN || highN < minN {
		return nil // 孤立坏点，交给上层清理
	}
	return fmt.Errorf("%s 价格量级混叠：%d 根在 %.1f 附近、%d 根在 %.1f 附近（相差 %.0f 倍），该源数据不可信",
		prefix, lowN, sorted[bestAt-1], highN, sorted[bestAt], sorted[bestAt]/sorted[bestAt-1])
}

// checkMinuteDailyAgree 分钟线尾价与日线尾价必须同量级。
// 就算某个源整体错量级（没有混叠），这一步还能兜住，避免回测出一堆假样本。
func checkMinuteDailyAgree(prefix string, minutes []Bar, daily []Daily) error {
	if len(minutes) == 0 || len(daily) == 0 {
		return nil
	}
	m, d := minutes[len(minutes)-1].Close, daily[len(daily)-1].Close
	if !finite(m) || !finite(d) || m <= 0 || d <= 0 {
		return fmt.Errorf("%s 价格非法（分钟 %.4f / 日线 %.4f）", prefix, m, d)
	}
	ratio := m / d
	if ratio > 1+minuteDailyMaxDrift || ratio < 1/(1+minuteDailyMaxDrift) {
		return fmt.Errorf("%s 分钟线与日线价差异常：%.1f vs %.1f（差 %.1f 倍）", prefix, m, d, ratio)
	}
	return nil
}

// barCloses / dailyCloses 抽价格用于校验。
func barCloses(bars []Bar) []float64 {
	out := make([]float64, 0, len(bars))
	for _, b := range bars {
		out = append(out, b.Close)
	}
	return out
}

func dailyCloses(days []Daily) []float64 {
	out := make([]float64, 0, len(days))
	for _, d := range days {
		out = append(out, d.Close)
	}
	return out
}
