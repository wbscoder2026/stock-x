package futures

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// fakeSource 可控的假数据源，用来验证熔断切换（不联网）
type fakeSource struct {
	name        string
	minute      []Bar
	minuteErr   error
	daily       []Daily
	dailyErr    error
	supportsAll bool
	supports    map[string]bool
	minuteCalls int32
	dailyCalls  int32
}

func (f *fakeSource) Name() string { return f.name }

func (f *fakeSource) Supports(v Variety) bool {
	if f.supports == nil {
		return true
	}
	return f.supports[v.Prefix]
}

func (f *fakeSource) Minute(_ context.Context, _ Variety, _ string) ([]Bar, error) {
	atomic.AddInt32(&f.minuteCalls, 1)
	if f.minuteErr != nil {
		return nil, f.minuteErr
	}
	return f.minute, nil
}

func (f *fakeSource) Daily(_ context.Context, _ Variety) ([]Daily, error) {
	atomic.AddInt32(&f.dailyCalls, 1)
	if f.dailyErr != nil {
		return nil, f.dailyErr
	}
	return f.daily, nil
}

func fakeBars() []Bar {
	return []Bar{{Time: time.Date(2026, 9, 21, 14, 45, 0, 0, locCST), Open: 1, High: 2, Low: 0.5, Close: 1.5, Volume: 10}}
}

func fakeDays() []Daily {
	return []Daily{{Date: time.Date(2026, 9, 18, 0, 0, 0, 0, locCST), Open: 1, High: 2, Low: 0.5, Close: 1.5, Volume: 10}}
}

func TestFailoverPrefersHealthyPrimary(t *testing.T) {
	primary := &fakeSource{name: "primary", minute: fakeBars(), daily: fakeDays()}
	backup := &fakeSource{name: "backup", minute: fakeBars(), daily: fakeDays()}
	w := NewWatcherSources(primary, backup)

	bars, err := w.fetchMinute(context.Background(), mustVariety(t, "RB"), "5")
	if err != nil || len(bars) != 1 {
		t.Fatalf("bars=%d err=%v", len(bars), err)
	}
	if atomic.LoadInt32(&primary.minuteCalls) != 1 || atomic.LoadInt32(&backup.minuteCalls) != 0 {
		t.Fatalf("主源正常时不该动备用源：%d/%d", primary.minuteCalls, backup.minuteCalls)
	}
	if st := w.Status(); st.Source != "primary" || len(st.Sources) != 2 {
		t.Fatalf("状态不对：%+v", st.Sources)
	}
}

func TestFailoverSwitchesWhenPrimaryFails(t *testing.T) {
	primary := &fakeSource{name: "primary", minuteErr: errors.New("HTTP 456"), dailyErr: errors.New("HTTP 456")}
	backup := &fakeSource{name: "backup", minute: fakeBars(), daily: fakeDays()}
	w := NewWatcherSources(primary, backup)
	now := time.Date(2026, 9, 21, 14, 50, 0, 0, locCST)
	w.now = func() time.Time { return now }

	days, err := w.fetchDaily(context.Background(), mustVariety(t, "RB"))
	if err != nil || len(days) != 1 {
		t.Fatalf("备用源应顶上：days=%d err=%v", len(days), err)
	}
	st := w.Status()
	if st.Source != "backup" {
		t.Fatalf("当前源应为 backup：%s", st.Source)
	}
	if st.Sources[0].Fails != 1 || st.Sources[0].Cooldown != int(sourceCooldown(1).Seconds()) {
		t.Fatalf("主源应进入冷却：%+v", st.Sources[0])
	}
	if st.Sources[1].OK != 1 || st.Sources[1].Fails != 0 {
		t.Fatalf("备用源应记成功：%+v", st.Sources[1])
	}

	// 冷却期内不再尝试主源
	if _, err := w.fetchDaily(context.Background(), mustVariety(t, "RB")); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&primary.dailyCalls) != 1 {
		t.Fatalf("冷却期内不该再打主源：%d", primary.dailyCalls)
	}
}

func TestFailoverRetriesPrimaryAfterCooldown(t *testing.T) {
	primary := &fakeSource{name: "primary", minuteErr: errors.New("boom"), minute: fakeBars()}
	backup := &fakeSource{name: "backup", minute: fakeBars()}
	w := NewWatcherSources(primary, backup)
	now := time.Date(2026, 9, 21, 14, 50, 0, 0, locCST)
	w.now = func() time.Time { return now }

	if _, err := w.fetchMinute(context.Background(), mustVariety(t, "RB"), "5"); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&primary.minuteCalls) != 1 || w.Status().Source != "backup" {
		t.Fatal("首次应切到备用源")
	}

	now = now.Add(sourceCooldown(1) + time.Second) // 冷却结束，主源已恢复
	primary.minuteErr = nil
	if _, err := w.fetchMinute(context.Background(), mustVariety(t, "RB"), "5"); err != nil {
		t.Fatal(err)
	}
	st := w.Status()
	if atomic.LoadInt32(&primary.minuteCalls) != 2 {
		t.Fatalf("冷却结束应重试主源：%d", primary.minuteCalls)
	}
	if st.Source != "primary" || st.Sources[0].Fails != 0 || st.Sources[0].Cooldown != 0 {
		t.Fatalf("主源恢复后应清零：%+v", st.Sources[0])
	}
}

func TestFailoverSkipsUnsupportedSource(t *testing.T) {
	primary := &fakeSource{name: "primary", minute: fakeBars(), supports: map[string]bool{}} // 不支持任何品种
	backup := &fakeSource{name: "backup", minute: fakeBars()}
	w := NewWatcherSources(primary, backup)

	if _, err := w.fetchMinute(context.Background(), mustVariety(t, "IF"), "5"); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&primary.minuteCalls) != 0 {
		t.Fatal("不支持的品种不该去请求该源")
	}
	st := w.Status()
	if st.Sources[0].Fails != 0 || st.Sources[0].Calls != 0 {
		t.Fatalf("跳过不算失败：%+v", st.Sources[0])
	}
}

func TestFailoverAllSourcesDown(t *testing.T) {
	a := &fakeSource{name: "a", minuteErr: errors.New("HTTP 456")}
	b := &fakeSource{name: "b", minuteErr: errors.New("HTTP 456")}
	w := NewWatcherSources(a, b)

	_, err := w.fetchMinute(context.Background(), mustVariety(t, "RB"), "5")
	if err == nil {
		t.Fatal("全挂应报错")
	}
	if err.Error() != "b: HTTP 456" {
		t.Fatalf("错误应带上最后一个源：%v", err)
	}
	st := w.Status()
	if st.Sources[0].Fails != 1 || st.Sources[1].Fails != 1 {
		t.Fatalf("两个源都该记失败：%+v", st.Sources)
	}
}

func TestSourceCooldownBackoff(t *testing.T) {
	cases := map[int]time.Duration{1: 30 * time.Second, 2: 60 * time.Second, 3: 120 * time.Second, 4: 240 * time.Second, 9: sourceCooldownMax}
	for fails, want := range cases {
		if got := sourceCooldown(fails); got != want {
			t.Fatalf("sourceCooldown(%d)=%v want %v", fails, got, want)
		}
	}
}
