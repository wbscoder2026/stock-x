package store

import (
	"testing"
	"time"
)

func TestFuturesCoverageDayCountsOneQueryForAll(t *testing.T) {
	st := openTestStore(t)
	defer st.Close()

	cst := time.FixedZone("CST", 8*3600)
	mk := func(symbol, period string, d, h int) FuturesBar {
		return FuturesBar{
			Symbol: symbol, Period: period,
			Time: time.Date(2026, 9, d, h, 0, 0, 0, cst).UTC(),
			Open: 1, High: 2, Low: 0.5, Close: 1.5, Volume: 10, Hold: 100,
		}
	}
	// PS0 的 1 分钟跨 3 天（其中一天 2 根，天数只算 1 天）；5 分钟只有 1 天
	rows := []FuturesBar{
		mk("PS0", "1", 21, 9), mk("PS0", "1", 21, 10),
		mk("PS0", "1", 22, 9),
		mk("PS0", "1", 23, 9),
		mk("PS0", "5", 23, 9),
		mk("JM0", "1", 22, 9),
	}
	if _, err := st.UpsertFuturesBars(rows); err != nil {
		t.Fatal(err)
	}

	got, err := st.FuturesCoverageDayCounts()
	if err != nil {
		t.Fatal(err)
	}
	if got["PS0|1"] != 3 {
		t.Fatalf("PS0 1分钟应 3 天：%v", got)
	}
	if got["PS0|5"] != 1 {
		t.Fatalf("PS0 5分钟应 1 天：%v", got)
	}
	if got["JM0|1"] != 1 {
		t.Fatalf("JM0 1分钟应 1 天：%v", got)
	}
	if _, ok := got["JM0|5"]; ok {
		t.Fatal("没数据的组合不该出现")
	}
}
