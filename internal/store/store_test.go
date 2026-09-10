package store

import (
	"path/filepath"
	"testing"
)

func TestUpsertBarsOHLCVLastDate(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	bars := []Bar{
		{Symbol: "000001", Date: "2024-01-03", Open: 10, High: 11, Low: 9, Close: 10.5, Volume: 100, Turnover: 1000, Turn: 1.2},
		{Symbol: "000001", Date: "2024-01-02", Open: 9, High: 10, Low: 8, Close: 9.5, Volume: 80, Turnover: 800, Turn: 1.0},
		{Symbol: "000002", Date: "2024-01-03", Open: 20, High: 21, Low: 19, Close: 20.5, Volume: 50, Turnover: 500, Turn: 0.5},
	}
	if err := s.UpsertBars(bars); err != nil {
		t.Fatal(err)
	}
	// 覆盖同日
	if err := s.UpsertBars([]Bar{{
		Symbol: "000001", Date: "2024-01-03", Open: 10, High: 12, Low: 9, Close: 11, Volume: 110, Turnover: 1100, Turn: 1.3,
	}}); err != nil {
		t.Fatal(err)
	}

	all, err := s.OHLCV("000001", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].Date != "2024-01-02" || all[1].Date != "2024-01-03" || all[1].Close != 11 {
		t.Fatalf("%+v", all)
	}
	cut, err := s.OHLCV("000001", "2024-01-02")
	if err != nil {
		t.Fatal(err)
	}
	if len(cut) != 1 || cut[0].Date != "2024-01-02" {
		t.Fatalf("%+v", cut)
	}

	d, ok, err := s.LastDate("000001")
	if err != nil || !ok || d != "2024-01-03" {
		t.Fatalf("last=%q ok=%v err=%v", d, ok, err)
	}
	_, ok, err = s.LastDate("999999")
	if err != nil || ok {
		t.Fatalf("missing last ok=%v err=%v", ok, err)
	}

	m, err := s.LastDates()
	if err != nil {
		t.Fatal(err)
	}
	if m["000001"] != "2024-01-03" || m["000002"] != "2024-01-03" {
		t.Fatalf("%v", m)
	}

	kl, err := s.Kline("000001", "2024-01-02", "2024-01-02")
	if err != nil || len(kl) != 1 {
		t.Fatalf("kline %+v %v", kl, err)
	}

	mk, err := s.LoadMarketAsOf("2024-01-03")
	if err != nil {
		t.Fatal(err)
	}
	if len(mk["000001"]) != 2 || len(mk["000002"]) != 1 {
		t.Fatalf("%+v", mk)
	}
}

func TestListLatestPicksEmptyDate(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	id, err := s.InsertScanRun("2024-01-03")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.InsertPicks([]ScanPick{{RunID: id, Strategy: "s", Symbol: "000001", Name: "平安"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishScanRun(id, "success"); err != nil {
		t.Fatal(err)
	}

	got, err := s.ListPicks("", "")
	if err != nil || len(got) != 1 || got[0].Symbol != "000001" {
		t.Fatalf("%+v %v", got, err)
	}
	got, err = s.ListPicksRange("2024-01-01", "2024-01-03", "")
	if err != nil || len(got) != 1 || got[0].AsOf != "2024-01-03" {
		t.Fatalf("range %+v %v", got, err)
	}
	got, err = s.ListPicksBySymbol("000001", "")
	if err != nil || len(got) != 1 || got[0].Strategy != "s" {
		t.Fatalf("symbol %+v %v", got, err)
	}
	got, err = s.ListPicksBySymbol("000002", "")
	if err != nil || len(got) != 0 {
		t.Fatalf("missing %+v %v", got, err)
	}
}

func TestListKlineCoverage(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.UpsertStocks([]Stock{
		{Symbol: "000001", Name: "平安", Market: "sz", Listed: true},
		{Symbol: "600000", Name: "浦发", Market: "sh", Listed: true},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertBars([]Bar{
		{Symbol: "000001", Date: "2024-01-02", Close: 10},
		{Symbol: "000001", Date: "2024-03-01", Close: 11},
	}); err != nil {
		t.Fatal(err)
	}
	list, total, err := s.ListKlineCoverage("", 0, 50)
	if err != nil || total != 2 || len(list) != 2 {
		t.Fatalf("list=%+v total=%d err=%v", list, total, err)
	}
	var ping KlineCoverage
	for _, c := range list {
		if c.Symbol == "000001" {
			ping = c
		}
	}
	if ping.StartDate != "2024-01-02" || ping.EndDate != "2024-03-01" || ping.Bars != 2 {
		t.Fatalf("%+v", ping)
	}
	hit, total, err := s.ListKlineCoverage("浦发", 0, 10)
	if err != nil || total != 1 || len(hit) != 1 || hit[0].Symbol != "600000" || hit[0].Bars != 0 {
		t.Fatalf("q %+v total=%d err=%v", hit, total, err)
	}
}
