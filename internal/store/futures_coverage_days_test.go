package store

import (
	"testing"
	"time"
)

func TestFuturesCoverageDaysGroupsByBeijingDay(t *testing.T) {
	st := openTestStore(t)
	defer st.Close()

	cst := time.FixedZone("CST", 8*3600)
	// 北京时间 9/21、9/22 两天各若干根；9/23 故意留空（模拟缺口）
	mk := func(y, m, d, h, min int) FuturesBar {
		return FuturesBar{
			Symbol: "PS0", Period: "1",
			Time: time.Date(y, time.Month(m), d, h, min, 0, 0, cst).UTC(),
			Open: 1, High: 2, Low: 0.5, Close: 1.5, Volume: 10, Hold: 100,
		}
	}
	rows := []FuturesBar{
		mk(2026, 9, 21, 9, 1), mk(2026, 9, 21, 9, 2), mk(2026, 9, 21, 9, 3),
		mk(2026, 9, 22, 9, 1), mk(2026, 9, 22, 9, 2),
	}
	if _, err := st.UpsertFuturesBars(rows); err != nil {
		t.Fatal(err)
	}

	got, err := st.FuturesCoverageDays("PS0", "1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("应有 2 天，实际 %d：%+v", len(got), got)
	}
	if got[0].Day != "2026-09-21" || got[0].Bars != 3 {
		t.Fatalf("第一天不对：%+v", got[0])
	}
	if got[1].Day != "2026-09-22" || got[1].Bars != 2 {
		t.Fatalf("第二天不对：%+v", got[1])
	}

	// 换一个周期：不该把 1 分钟的数据算进来
	other, err := st.FuturesCoverageDays("PS0", "5")
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 0 {
		t.Fatalf("周期要分开统计：%+v", other)
	}

	// 没同步过的合约 → 空而不是报错
	none, err := st.FuturesCoverageDays("XX0", "1")
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Fatalf("未知合约应为空：%+v", none)
	}
}
