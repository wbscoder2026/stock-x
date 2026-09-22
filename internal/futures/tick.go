package futures

import (
	"math"
	"strconv"
	"strings"
)

// DefaultTickSize 未知品种的兜底报价单位（整数报价）。
const DefaultTickSize = 1.0

// tickSizes 品种最小变动价位（元/吨、元/克、点…，按交易所合约规则填）。
// 推荐止损/止盈要对齐到这个单位，否则给出的价格根本挂不出去。
// 发现与实际不符，直接改这里即可；未列出的品种按 DefaultTickSize 处理。
var tickSizes = map[string]float64{
	// ---- 大商所
	"JM": 0.5, "J": 0.5, "I": 0.5, // 焦煤/焦炭/铁矿：0.5 元/吨
	"LG": 0.5,                                // 原木：0.5 元/立方米
	"M":  1, "A": 1, "B": 1, "C": 1, "CS": 1, // 豆粕/豆一/豆二/玉米/淀粉
	"L": 1, "V": 1, "PP": 1, "EG": 1, "EB": 1, "PG": 1, "JD": 1, "RR": 1,
	"Y": 2, "P": 2, // 豆油/棕油：2 元/吨
	"LH": 5, // 生猪：5 元/吨

	// ---- 上期所
	"RB": 1, "HC": 1, "FU": 1, "BU": 1, "AO": 1, "LU": 1,
	"SP": 2,                                              // 纸浆：2 元/吨
	"AL": 5, "ZN": 5, "PB": 5, "SS": 5, "RU": 5, "NR": 5, // 铝/锌/铅/不锈钢/橡胶/20号胶
	"CU": 10, "NI": 10, "SN": 10, // 铜/镍/锡：10 元/吨
	"AG": 1,    // 白银：1 元/千克
	"AU": 0.02, // 黄金：0.02 元/克

	// ---- 上期能源
	"SC": 0.1, // 原油：0.1 元/桶

	// ---- 郑商所
	"MA": 1, "OI": 1, "RM": 1, "SR": 1, "FG": 1, "SA": 1, "UR": 1, "AP": 1, "SH": 1,
	"TA": 2, "PF": 2, "PK": 2, "PX": 2, "SF": 2, "SM": 2, // 2 元/吨
	"CF": 5, "CJ": 5, // 棉花/红枣：5 元/吨
	"ZC": 0.2, // 动力煤：0.2 元/吨

	// ---- 广期所
	"SI": 5,  // 工业硅：5 元/吨
	"LC": 20, // 碳酸锂：20 元/吨
	"PS": 5,  // 多晶硅：5 元/吨

	// ---- 中金所（股指按指数点，国债按元）
	"IF": 0.2, "IH": 0.2, "IC": 0.2, "IM": 0.2,
	"T": 0.005, "TF": 0.005, "TS": 0.002,
}

// TickSize 品种最小变动价位；未知品种回落到 DefaultTickSize。
func TickSize(prefix string) float64 {
	key := strings.ToUpper(strings.TrimSpace(prefix))
	if v, ok := tickSizes[key]; ok && v > 0 {
		return v
	}
	return DefaultTickSize
}

// TickDecimals 报价单位的小数位数（0.02 → 2、0.5 → 1、1 → 0）；非法值按 1 位处理。
// 展示价格时必须用它，否则黄金 623.44 会被格式化成 623.4（挂不出去的价）。
func TickDecimals(tick float64) int {
	if !finite(tick) || tick <= 0 {
		return 1
	}
	s := strconv.FormatFloat(tick, 'f', -1, 64)
	if i := strings.IndexByte(s, '.'); i >= 0 {
		return len(s) - i - 1
	}
	return 0
}

// FormatPrice 按报价单位格式化价格（3416 / 1221.5 / 623.44）。
func FormatPrice(v, tick float64) string {
	return strconv.FormatFloat(v, 'f', TickDecimals(tick), 64)
}

// RoundToTick 把价格按最小变动价位就近取整（挂得出去的价格）。
// tick 非法时原样返回：宁可不取整，也不要算出错价。
func RoundToTick(price, tick float64) float64 {
	if !finite(price) || !finite(tick) || tick <= 0 {
		return price
	}
	v := math.Round(price/tick) * tick
	if d := TickDecimals(tick); d > 0 { // 消掉 0.30000000000000004 这类浮点残渣
		scale := math.Pow10(d)
		v = math.Round(v*scale) / scale
	}
	return v
}
