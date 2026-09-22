package store

import (
	"path/filepath"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func bar(symbol, period string, minute int, close float64) FuturesBar {
	return FuturesBar{
		Symbol: symbol, Period: period,
		Time: time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC).Add(time.Duration(minute) * time.Minute),
		Open: close - 1, High: close + 1, Low: close - 2, Close: close,
		Volume: 100, Hold: 1000,
	}
}

func TestFuturesUpsertDedupAndLoad(t *testing.T) {
	s := openTestStore(t)
	bars := []FuturesBar{bar("JM0", "5", 0, 100), bar("JM0", "5", 5, 101), bar("JM0", "5", 10, 102)}
	if n, err := s.UpsertFuturesBars(bars); err != nil || n != 3 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	// 重复写同一根 → 覆盖而不新增
	dup := bar("JM0", "5", 0, 999)
	if _, err := s.UpsertFuturesBars([]FuturesBar{dup}); err != nil {
		t.Fatal(err)
	}
	all, err := s.FuturesBars("JM0", "5", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("去重失败：%d", len(all))
	}
	if all[0].Close != 999 {
		t.Fatalf("重复写应覆盖：%v", all[0].Close)
	}
	if !all[0].Time.Before(all[2].Time) {
		t.Fatalf("应升序返回：%v %v", all[0].Time, all[2].Time)
	}

	// limit 取最后 N 根，仍升序
	tail, err := s.FuturesBars("JM0", "5", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(tail) != 2 || tail[1].Close != 102 || tail[0].Close != 101 {
		t.Fatalf("tail=%+v", tail)
	}

	// 周期隔离
	if other, err := s.FuturesBars("JM0", "15", 0); err != nil || len(other) != 0 {
		t.Fatalf("周期应隔离：%d %v", len(other), err)
	}
}

func TestFuturesRangeLastTimeAndStats(t *testing.T) {
	s := openTestStore(t)
	_, _ = s.UpsertFuturesBars([]FuturesBar{bar("JM0", "5", 0, 100), bar("JM0", "5", 10, 102)})
	_, _ = s.UpsertFuturesBars([]FuturesBar{bar("RB0", "1d", 0, 3000)})

	first, last, n, err := s.FuturesRange("JM0", "5")
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if !last.After(first) {
		t.Fatalf("首末时间不对：%v %v", first, last)
	}

	got, ok, err := s.FuturesLastTime("JM0", "5")
	if err != nil || !ok || !got.Equal(last) {
		t.Fatalf("last=%v ok=%v err=%v", got, ok, err)
	}
	if _, ok, _ := s.FuturesLastTime("CU0", "5"); ok {
		t.Fatal("没数据的品种不该报 ok")
	}

	symbols, bars, err := s.FuturesStats()
	if err != nil || symbols != 2 || bars != 3 {
		t.Fatalf("stats=%d/%d err=%v", symbols, bars, err)
	}

	cov, err := s.FuturesCoverage()
	if err != nil || len(cov) != 2 {
		t.Fatalf("coverage=%d err=%v", len(cov), err)
	}
	if cov[0].Symbol != "JM0" || cov[0].Period != "5" || cov[0].Bars != 2 {
		t.Fatalf("coverage[0]=%+v", cov[0])
	}
}

func TestFuturesDelete(t *testing.T) {
	s := openTestStore(t)
	_, _ = s.UpsertFuturesBars([]FuturesBar{bar("JM0", "5", 0, 100), bar("JM0", "15", 0, 100)})
	n, err := s.DeleteFuturesBars("JM0", "5")
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if left, _ := s.FuturesBars("JM0", "15", 0); len(left) != 1 {
		t.Fatal("不该误删其它周期")
	}
}
