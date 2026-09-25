package futuresync

import (
	"testing"
	"time"

	"github.com/wbscoder2026/stock-x/internal/store"
)

func TestLocalDetailListsSyncedDaysAndMinuteFlag(t *testing.T) {
	st := openStore(t)
	defer st.Close()
	cst := time.FixedZone("CST", 8*3600)

	symbol := "PS0"
	mk := func(period string, y, m, d, h, min int) store.FuturesBar {
		return store.FuturesBar{
			Symbol: symbol, Period: period,
			Time: time.Date(y, time.Month(m), d, h, min, 0, 0, cst).UTC(),
			Open: 1, High: 2, Low: 0.5, Close: 1.5, Volume: 10, Hold: 100,
		}
	}
	// 1 分钟：9/21(周一) 与 9/23(周三) 有数据，9/22(周二) 故意留空
	// 5 分钟：只有 9/23 一天
	rows := []store.FuturesBar{
		mk("1", 2026, 9, 21, 9, 1), mk("1", 2026, 9, 21, 9, 2),
		mk("1", 2026, 9, 23, 9, 1),
		mk("5", 2026, 9, 23, 9, 5),
	}
	if _, err := st.UpsertFuturesBars(rows); err != nil {
		t.Fatal(err)
	}

	got, err := LocalDetail(st, "PS", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Symbol != symbol {
		t.Fatalf("代码不对：%s", got.Symbol)
	}
	if !got.MinuteSynced {
		t.Fatal("有 1 分钟数据时应标记 minute_synced")
	}
	if got.MinuteDays != 2 {
		t.Fatalf("1 分钟覆盖天数应为 2：%d", got.MinuteDays)
	}
	if got.MinuteFirst != "2026-09-21" || got.MinuteLast != "2026-09-23" {
		t.Fatalf("1 分钟起止不对：%s ~ %s", got.MinuteFirst, got.MinuteLast)
	}

	var minute, five *PeriodDetail
	for i := range got.Periods {
		switch got.Periods[i].Period {
		case "1":
			minute = &got.Periods[i]
		case "5":
			five = &got.Periods[i]
		}
	}
	if minute == nil || five == nil {
		t.Fatalf("两个周期都该列出来：%+v", got.Periods)
	}
	if !minute.IsMinute {
		t.Fatal("period=1 应标记 is_minute")
	}
	if five.IsMinute {
		t.Fatal("period=5 不该标记 is_minute")
	}
	if len(minute.Days) != 2 || minute.Days[0].Day != "2026-09-21" || minute.Days[0].Bars != 2 {
		t.Fatalf("按天明细不对：%+v", minute.Days)
	}
	if minute.Days[0].Weekday == "" {
		t.Fatal("应带上星期几，页面才好读")
	}
	// 首末之间的工作日没数据 → 记为疑似缺失（节假日会有误报，页面上按「疑似」措辞）
	if len(minute.Missing) != 1 || minute.Missing[0] != "2026-09-22" {
		t.Fatalf("应报出 9/22 这个工作日缺口：%+v", minute.Missing)
	}

	// 没同步过的品种：不该报错，给出空明细
	empty, err := LocalDetail(st, "JM", "")
	if err != nil {
		t.Fatal(err)
	}
	if empty.MinuteSynced || empty.MinuteDays != 0 {
		t.Fatalf("没数据时不该标记 1 分钟已同步：%+v", empty)
	}
}

func TestLocalDetailRejectsUnknownPrefix(t *testing.T) {
	st := openStore(t)
	defer st.Close()
	if _, err := LocalDetail(st, "NOPE", ""); err == nil {
		t.Fatal("未知品种应报错")
	}
}
