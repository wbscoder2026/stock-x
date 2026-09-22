package futures

import (
	"testing"
	"time"
)

func TestDefaultWatchConfig(t *testing.T) {
	cfg := DefaultWatchConfig()
	if !cfg.Enabled {
		t.Fatal("监控应默认开启")
	}
	if !cfg.Alert.Desktop {
		t.Fatal("桌面通知应默认开启")
	}
	if cfg.Alert.Feishu {
		t.Fatal("飞书推送要配 webhook，默认应关闭")
	}
	if cfg.AlertTTLMin != DefaultAlertTTLMin {
		t.Fatalf("提醒保留时长默认 %d，实际 %d", DefaultAlertTTLMin, cfg.AlertTTLMin)
	}
	if cfg.Interval != WatchDefaultInterval {
		t.Fatalf("间隔默认 %d，实际 %d", WatchDefaultInterval, cfg.Interval)
	}
	if cfg.Params.VolRatio != DefaultParams().VolRatio {
		t.Fatalf("品种参数应带默认值：%+v", cfg.Params)
	}
}

func TestNormalizeWatchConfigClampsTTL(t *testing.T) {
	cases := []struct {
		in, want int
	}{
		{0, DefaultAlertTTLMin}, // 没填 → 默认 30
		{-5, DefaultAlertTTLMin},
		{1, 1},
		{45, 45},
		{MaxAlertTTLMin, MaxAlertTTLMin},
		{99999, MaxAlertTTLMin}, // 上限 24 小时
	}
	for _, c := range cases {
		got := NormalizeWatchConfig(WatchConfig{AlertTTLMin: c.in})
		if got.AlertTTLMin != c.want {
			t.Fatalf("AlertTTLMin %d → 期望 %d，实际 %d", c.in, c.want, got.AlertTTLMin)
		}
	}
	// 其它缺省项也要补上
	got := NormalizeWatchConfig(WatchConfig{})
	if got.Interval != WatchDefaultInterval {
		t.Fatalf("间隔应补默认：%d", got.Interval)
	}
	if got.Params.Period == "" || got.Params.ATRPeriod == 0 {
		t.Fatalf("参数应补默认：%+v", got.Params)
	}
}

func TestAlertTTLConfigurable(t *testing.T) {
	w := NewWatcherSources(&fakeSource{name: "s"})
	now := time.Now()

	// 默认（没配）→ 30 分钟：5 分钟前的那条还在
	w.events = []WatchEvent{
		{Seq: 1, Prefix: "JM", TimeMS: now.Add(-5 * time.Minute).UnixMilli()},
		{Seq: 2, Prefix: "JM", TimeMS: now.Add(-40 * time.Minute).UnixMilli()},
	}
	if got := w.Events(0); len(got) != 1 || got[0].Seq != 1 {
		t.Fatalf("默认 30 分钟：%+v", got)
	}
	if sec := w.Status().AlertTTLSec; sec != DefaultAlertTTLMin*60 {
		t.Fatalf("默认 TTL 应为 %d 秒：%d", DefaultAlertTTLMin*60, sec)
	}

	// 配成 60 分钟 → 40 分钟前的那条要保留下来
	w.cfg.AlertTTLMin = 60
	w.events = []WatchEvent{
		{Seq: 1, Prefix: "JM", TimeMS: now.Add(-5 * time.Minute).UnixMilli()},
		{Seq: 2, Prefix: "JM", TimeMS: now.Add(-40 * time.Minute).UnixMilli()},
	}
	if got := w.Events(0); len(got) != 2 {
		t.Fatalf("60 分钟应都保留：%+v", got)
	}
	if sec := w.Status().AlertTTLSec; sec != 3600 {
		t.Fatalf("TTL 应为 3600 秒：%d", sec)
	}

	// 配成 1 分钟 → 5 分钟前的那条要被清掉
	w.cfg.AlertTTLMin = 1
	w.events = []WatchEvent{
		{Seq: 1, Prefix: "JM", TimeMS: now.Add(-5 * time.Minute).UnixMilli()},
		{Seq: 2, Prefix: "JM", TimeMS: now.Add(-30 * time.Second).UnixMilli()},
	}
	if got := w.Events(0); len(got) != 1 || got[0].Seq != 2 {
		t.Fatalf("1 分钟只该留最新那条：%+v", got)
	}
	if sec := w.Status().AlertTTLSec; sec != 60 {
		t.Fatalf("TTL 应为 60 秒：%d", sec)
	}
}

func TestStartAppliesTTLConfig(t *testing.T) {
	// 启动/热更新时要把页面传的时长规整进 watcher，并立刻按新时长清理
	src := &fakeSource{name: "s", minute: breakoutBars(), daily: daysOf()}
	w := NewWatcherSources(src)
	if _, err := w.Start(WatchConfig{
		Params: Params{Period: "5"}, Interval: WatchMaxInterval, AlertTTLMin: 7,
	}); err != nil {
		t.Fatal(err)
	}
	if sec := w.Status().AlertTTLSec; sec != 7*60 {
		t.Fatalf("启动后应按 7 分钟：%d", sec)
	}
	// 热更新成 2 分钟
	if _, err := w.Start(WatchConfig{
		Params: Params{Period: "5"}, Interval: WatchMaxInterval, AlertTTLMin: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if sec := w.Status().AlertTTLSec; sec != 2*60 {
		t.Fatalf("热更新后应按 2 分钟：%d", sec)
	}
	// 非法值回落到默认
	if _, err := w.Start(WatchConfig{
		Params: Params{Period: "5"}, Interval: WatchMaxInterval, AlertTTLMin: -1,
	}); err != nil {
		t.Fatal(err)
	}
	if sec := w.Status().AlertTTLSec; sec != DefaultAlertTTLMin*60 {
		t.Fatalf("非法值应回落默认：%d", sec)
	}
	w.Stop()
}
