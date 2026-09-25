package futuresync

import (
	"testing"
	"time"

	"github.com/wbscoder2026/stock-x/internal/store"
)

func mkBar(symbol, period string, y, m, d, h int) store.FuturesBar {
	return store.FuturesBar{
		Symbol: symbol, Period: period,
		Time: time.Date(y, time.Month(m), d, h, 0, 0, 0, time.FixedZone("CST", 8*3600)).UTC(),
		Open: 1, High: 2, Low: 0.5, Close: 1.5, Volume: 10, Hold: 100,
	}
}

func TestLocalDetailGroupsDaysByMonth(t *testing.T) {
	st := openStore(t)
	// 1 分钟：8/29 一天、9/1 与 9/2 两天 → 两个月
	if _, err := st.UpsertFuturesBars([]store.FuturesBar{
		mkBar("PS0", "1", 2026, 8, 29, 9),
		mkBar("PS0", "1", 2026, 9, 1, 9), mkBar("PS0", "1", 2026, 9, 1, 10), mkBar("PS0", "1", 2026, 9, 2, 9),
	}); err != nil {
		t.Fatal(err)
	}

	got, err := LocalDetail(st, "PS", "")
	if err != nil {
		t.Fatal(err)
	}
	var minute *PeriodDetail
	for i := range got.Periods {
		if got.Periods[i].Period == "1" {
			minute = &got.Periods[i]
		}
	}
	if minute == nil {
		t.Fatal("缺 1 分钟周期")
	}
	if len(minute.Months) != 2 {
		t.Fatalf("应分 2 个月：%+v", minute.Months)
	}
	if minute.Months[0].Month != "2026-08" || minute.Months[0].Days != 1 || minute.Months[0].Bars != 1 {
		t.Fatalf("8 月不对：%+v", minute.Months[0])
	}
	if minute.Months[1].Month != "2026-09" || minute.Months[1].Days != 2 || minute.Months[1].Bars != 3 {
		t.Fatalf("9 月不对：%+v", minute.Months[1])
	}
	// 月份要升序
	if minute.Months[0].Month > minute.Months[1].Month {
		t.Fatal("月份应升序")
	}
}

func TestLocalDetailByContractSymbol(t *testing.T) {
	st := openStore(t)
	if _, err := st.UpsertFuturesBars([]store.FuturesBar{
		mkBar("JM2601", "1", 2026, 9, 21, 9), mkBar("JM2601", "1", 2026, 9, 22, 9),
		mkBar("JM0", "1", 2026, 9, 22, 9),
	}); err != nil {
		t.Fatal(err)
	}

	// 指定合约 → 只看合约自己的数据
	contract, err := LocalDetail(st, "JM", "JM2601")
	if err != nil {
		t.Fatal(err)
	}
	if contract.Symbol != "JM2601" || contract.MinuteDays != 2 {
		t.Fatalf("合约明细不对：%+v", contract)
	}
	// 不指定 → 主连
	main, err := LocalDetail(st, "JM", "")
	if err != nil {
		t.Fatal(err)
	}
	if main.Symbol != "JM0" || main.MinuteDays != 1 {
		t.Fatalf("主连明细不对：%+v", main)
	}
	// 乱填代码 → 报错
	if _, err := LocalDetail(st, "JM", "JMXXXX"); err == nil {
		t.Fatal("非法代码应报错")
	}
}

// 覆盖率里要能看到「这个品种还有哪些月份合约的数据」
func TestLocalCoverageListsContracts(t *testing.T) {
	st := openStore(t)
	if _, err := st.UpsertFuturesBars([]store.FuturesBar{
		mkBar("JM0", "1", 2026, 9, 22, 9),
		mkBar("JM2601", "1", 2026, 9, 21, 9), mkBar("JM2601", "5", 2026, 9, 21, 9),
	}); err != nil {
		t.Fatal(err)
	}
	items, err := LocalCoverage(st)
	if err != nil {
		t.Fatal(err)
	}
	var jm *VarietySpan
	for i := range items {
		if items[i].Prefix == "JM" {
			jm = &items[i]
		}
	}
	if jm == nil {
		t.Fatal("没找到 JM")
	}
	if len(jm.Contracts) != 1 || jm.Contracts[0].Symbol != "JM2601" {
		t.Fatalf("应列出合约 JM2601：%+v", jm.Contracts)
	}
	if jm.Contracts[0].Periods != 2 {
		t.Fatalf("合约应有 2 个周期：%+v", jm.Contracts[0])
	}
}
