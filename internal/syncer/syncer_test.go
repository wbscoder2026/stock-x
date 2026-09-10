package syncer

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/wbscoder2026/stock-x/internal/baostock"
	"github.com/wbscoder2026/stock-x/internal/store"
)

func TestMarketAndKBars(t *testing.T) {
	if got := marketFromBS("sh.600000"); got != "sh" {
		t.Fatalf("sh: %q", got)
	}
	if got := marketFromBS("sz.000001"); got != "sz" {
		t.Fatalf("sz: %q", got)
	}
	if got := marketFromBS("bj.430047"); got != "bj" {
		t.Fatalf("bj: %q", got)
	}
	bars := kbarsToBars("600000", []baostock.KBar{{
		Date: "2024-01-02", Open: 1, High: 2, Low: 0.5, Close: 1.5, Volume: 10, Amount: 100, Turn: 0.3,
	}})
	want := store.Bar{Symbol: "600000", Date: "2024-01-02", Open: 1, High: 2, Low: 0.5, Close: 1.5, Volume: 10, Turnover: 100, Turn: 0.3}
	if bars[0] != want {
		t.Fatalf("%+v", bars[0])
	}
}

func TestJobStartCaughtUp(t *testing.T) {
	if !caughtUp("2024-06-01", true, "2024-06-01") {
		t.Fatal("已到今天应跳过")
	}
	if caughtUp("2024-05-31", true, "2024-06-01") {
		t.Fatal("未到今天不应跳过")
	}
	if jobStart("", false, "2024-01-01") != "2024-01-01" {
		t.Fatal("无 last 应用 StartDate")
	}
	if jobStart("2024-01-10", true, "2024-01-01") != "2024-01-11" {
		t.Fatal("续传应从 last+1 天")
	}
}

func TestClipRange(t *testing.T) {
	from, to, err := clipRange("2024-06-01", "2024-01-01", "2024-12-31", "2024-01-01")
	if err != nil || from != "2024-01-01" || to != "2024-06-01" {
		t.Fatalf("%s %s %v", from, to, err)
	}
	from, to, err = clipRange("", "", "2024-06-01", "2024-01-01")
	if err != nil || from != "2024-01-01" || to != "2024-06-01" {
		t.Fatalf("empty %s %s %v", from, to, err)
	}
	if _, _, err := clipRange("2025-01-01", "2025-02-01", "2024-12-31", "2024-01-01"); err == nil {
		t.Fatal("未来区间应失败")
	}
}

func TestWorkerN(t *testing.T) {
	if (&Syncer{}).workerN() != 4 {
		t.Fatal("默认 4")
	}
	if (&Syncer{Workers: 4}).workerN() != 4 {
		t.Fatal("自定义")
	}
	if (&Syncer{Workers: 99}).workerN() != 8 {
		t.Fatal("上限 8")
	}
	s := &Syncer{Workers: 4}
	if s.SetWorkers(99) != 8 || s.WorkerCount() != 8 {
		t.Fatal("动态上限")
	}
	if s.SetWorkers(0) != 1 || s.WorkerCount() != 1 {
		t.Fatal("动态下限")
	}
	if s.SetWorkers(3) != 3 || s.WorkerCount() != 3 {
		t.Fatal("动态设置")
	}
}

func TestRetry(t *testing.T) {
	ctx := context.Background()
	n := 0
	err := retry(ctx, []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}, func() error {
		n++
		if n < 3 {
			return errors.New("fail")
		}
		return nil
	})
	if err != nil || n != 3 {
		t.Fatalf("n=%d err=%v", n, err)
	}

	n = 0
	err = retry(ctx, []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}, func() error {
		n++
		return errors.New("x")
	})
	if err == nil || n != 4 {
		t.Fatalf("应尝试 1+3 次，n=%d err=%v", n, err)
	}

	ctx2, cancel := context.WithCancel(context.Background())
	cancel()
	err = retry(ctx2, []time.Duration{time.Second}, func() error {
		return errors.New("x")
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("期望 canceled，得到 %v", err)
	}
}
