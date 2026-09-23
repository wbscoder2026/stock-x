package api

import (
	"strings"
	"testing"

	"github.com/wbscoder2026/stock-x/internal/futures"
)

func TestFuturesAlertMessageCarriesRecommendation(t *testing.T) {
	events := []futures.WatchEvent{{
		Name: "焦煤", Prefix: "JM", Direction: "向上突破", Level: "ORB高(开盘30分钟)",
		Close: 1523, LevelPrice: 1520, Time: "2026-09-22 09:50",
		StopPrice: 1511, TPPrice: 1541, RR: 1.5, TickSize: 0.5, // 焦煤：0.5 的整数倍 → 1 位小数
	}}
	_, lines := FuturesAlertMessage(events, 12)
	if len(lines) != 1 {
		t.Fatalf("lines=%v", lines)
	}
	if !strings.Contains(lines[0], "推荐止损 1511.0 / 止盈 1541.0") {
		t.Fatalf("提醒里没带推荐价：%s", lines[0])
	}
	if !strings.Contains(lines[0], "现价 1523.0") {
		t.Fatalf("原字段被破坏：%s", lines[0])
	}
}

func TestFuturesAlertMessageFormatsByTick(t *testing.T) {
	// 螺纹钢报价单位是 1 → 推送里不该出现 3416.0 这种小数
	_, lines := FuturesAlertMessage([]futures.WatchEvent{{
		Name: "螺纹钢", Prefix: "RB", Direction: "向上突破", Level: "PDH(昨高)",
		Close: 3456, LevelPrice: 3450, Time: "2026-09-22 09:50",
		StopPrice: 3416, TPPrice: 3516, RR: 1.5, TickSize: 1,
	}}, 12)
	if !strings.Contains(lines[0], "推荐止损 3416 / 止盈 3516") {
		t.Fatalf("应按报价单位取整显示：%s", lines[0])
	}
}

func TestFuturesAlertMessageWithoutRecommendation(t *testing.T) {
	// ATR 数据不足（推荐价 0）时不留空占位，老文案保持原样
	_, lines := FuturesAlertMessage([]futures.WatchEvent{{
		Name: "焦煤", Prefix: "JM", Direction: "向上突破", Level: "PDH(昨高)",
		Close: 100, LevelPrice: 99, Time: "2026-09-22 09:50",
	}}, 12)
	if strings.Contains(lines[0], "推荐止损") {
		t.Fatalf("没有推荐价不该出现占位：%s", lines[0])
	}
}

func TestFuturesWatchAlertTestIncludesAdvice(t *testing.T) {
	// 「测试提醒」发出的样例会带上真实格式的推荐价
	stop, tp := futures.RecommendStop(futures.StopInput{
		Prefix: "JM", Direction: "向上突破", Entry: 1523, ATR: 12,
		StopMode: futures.StopModeATR, StopATR: 1, RR: futures.DefaultRR,
	})
	if stop != 1511 || tp != 1541 {
		t.Fatalf("样例子计算不对：stop=%v tp=%v", stop, tp)
	}
}
