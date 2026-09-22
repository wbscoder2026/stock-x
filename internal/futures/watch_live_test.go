package futures

import (
	"os"
	"strings"
	"testing"
	"time"
)

// 真实接口冒烟（默认跳过，避免 CI 依赖外网）：
//
//	FUTURES_LIVE=1 go test ./internal/futures/ -run Live -v
//	FUTURES_LIVE=1 FUTURES_LIVE_ALL=1 go test ./internal/futures/ -run Live -v          # 全市场 67 个品种
//	FUTURES_LIVE=1 FUTURES_LIVE_PREFIXES=JM,RB,CU go test ./internal/futures/ -run Live -v
//	FUTURES_LIVE=1 FUTURES_LIVE_SOURCE=sina go test ./internal/futures/ -run Live -v    # 只用新浪（对比用）
func TestWatcherLiveSmoke(t *testing.T) {
	if os.Getenv("FUTURES_LIVE") != "1" {
		t.Skip("设 FUTURES_LIVE=1 才跑真实网络")
	}
	prefixes := []string{"JM", "RB"}
	if raw := os.Getenv("FUTURES_LIVE_PREFIXES"); raw != "" {
		prefixes = strings.Split(raw, ",")
	}
	if os.Getenv("FUTURES_LIVE_ALL") == "1" {
		prefixes = nil // 全市场
	}
	w := NewDefaultWatcher() // 东财优先 + 新浪兜底
	if os.Getenv("FUTURES_LIVE_SOURCE") == "sina" {
		w = NewWatcher(&Client{})
	}
	if _, err := w.Start(WatchConfig{
		Params:   Params{Period: "5"},
		Interval: 60,
		Prefixes: prefixes,
	}); err != nil {
		t.Fatal(err)
	}
	defer w.Stop()

	st := w.Status()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		st = w.Status()
		if st.Ticks >= 1 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if st.Ticks == 0 {
		t.Fatalf("等不到首轮扫描：%+v", st)
	}
	if st.Scanned == 0 && strings.Contains(st.LastError, "456") {
		t.Skipf("新浪限流（HTTP 456），等几分钟再跑：%s", st.LastError)
	}
	if st.Failures > 0 {
		t.Logf("有 %d 个品种失败（新浪限流/网络抖动，重跑即可）：%s", st.Failures, st.LastError)
	}
	if st.Scanned*2 < st.Varieties {
		t.Fatalf("过半品种扫描失败：%+v", st)
	}
	t.Logf("覆盖 %d 个品种，扫描 %d，失败 %d，耗时 %dms，提醒 %d 条",
		st.Varieties, st.Scanned, st.Failures, st.LastMS, st.Events)
	t.Logf("数据源：当前 %s，明细 %+v", st.Source, st.Sources)
	// 等主力月份合约异步补齐（事件里要能显示「2701」这种月份）
	deadline = time.Now().Add(10 * time.Second)
	var withContract int
	for time.Now().Before(deadline) {
		events := w.Events(0)
		withContract = 0
		for _, e := range events {
			if e.Contract != "" {
				withContract++
			}
		}
		if len(events) == 0 || withContract > 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	for _, e := range w.Events(0) {
		t.Logf("%s %s %s%s %s %s 现价 %.1f 关键位 %.1f", e.Time, e.Name, e.Prefix,
			contractText(e), e.Direction, e.Level, e.Close, e.LevelPrice)
	}
	t.Logf("带月份合约的事件：%d 条", withContract)
}

func contractText(e WatchEvent) string {
	if e.ContractLabel != "" {
		return "/" + e.ContractLabel
	}
	return ""
}
