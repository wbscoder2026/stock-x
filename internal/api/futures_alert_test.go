package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wbscoder2026/stock-x/internal/config"
	"github.com/wbscoder2026/stock-x/internal/futures"
)

func sampleEvents() []futures.WatchEvent {
	return []futures.WatchEvent{
		{
			Seq: 1, Fresh: true, Time: "2026-09-22 09:05", Symbol: "JM0", Prefix: "JM", Name: "焦煤",
			Direction: "向上突破", Level: "Donchian高(20根)", Close: 1523, LevelPrice: 1516,
		},
		{
			Seq: 2, Fresh: true, Time: "2026-09-22 09:45", Symbol: "RB0", Prefix: "RB", Name: "螺纹钢",
			Direction: "向下跌破", Level: "ORB低(开盘30分钟)", Close: 3108, LevelPrice: 3112,
		},
	}
}

func TestFuturesAlertMessage(t *testing.T) {
	title, lines := FuturesAlertMessage(sampleEvents(), 12)
	if title != "⚡ 期货突破 2 条" {
		t.Fatalf("title=%s", title)
	}
	if len(lines) != 2 {
		t.Fatalf("lines=%d", len(lines))
	}
	if !strings.Contains(lines[0], "📈") || !strings.Contains(lines[0], "**焦煤 JM**") ||
		!strings.Contains(lines[0], "现价 1523.0 / 关键位 1516.0") {
		t.Fatalf("第一行不对：%s", lines[0])
	}
	if !strings.Contains(lines[1], "📉") {
		t.Fatalf("向下跌破该用 📉：%s", lines[1])
	}

	// 超出上限要折叠
	many := make([]futures.WatchEvent, 0, 20)
	for i := 0; i < 20; i++ {
		many = append(many, sampleEvents()[0])
	}
	_, lines = FuturesAlertMessage(many, 12)
	if len(lines) != 13 || !strings.Contains(lines[12], "等共 20 条") {
		t.Fatalf("折叠不对：%d %v", len(lines), lines[len(lines)-1])
	}
}

func TestFuturesAlertSummary(t *testing.T) {
	got := FuturesAlertSummary(sampleEvents(), 3)
	if !strings.Contains(got, "焦煤 向上突破 1523.0") || !strings.Contains(got, "螺纹钢") {
		t.Fatalf("summary=%s", got)
	}
	many := make([]futures.WatchEvent, 0, 5)
	for i := 0; i < 5; i++ {
		many = append(many, sampleEvents()[0])
	}
	if got := FuturesAlertSummary(many, 3); !strings.Contains(got, "等 5 条") {
		t.Fatalf("summary 未折叠：%s", got)
	}
}

func TestPushFuturesAlertsFeishu(t *testing.T) {
	var payload map[string]any
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&payload)
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok"}`))
	}))
	t.Cleanup(hook.Close)

	s := &Server{Cfg: config.Config{FeishuWebhook: hook.URL}, Watch: futures.NewDefaultWatcher()}
	s.pushFuturesAlerts(futures.WatchConfig{Alert: futures.AlertConfig{Feishu: true}}, sampleEvents())

	note := s.Watch.Status().AlertNote
	if !strings.Contains(note, "飞书已推送 2 条") {
		t.Fatalf("note=%s", note)
	}
	card, _ := payload["card"].(map[string]any)
	if card == nil {
		t.Fatalf("没发出飞书卡片：%+v", payload)
	}
}

func TestPushFuturesAlertsWithoutWebhook(t *testing.T) {
	s := &Server{Cfg: config.Config{}, Watch: futures.NewDefaultWatcher()}
	s.pushFuturesAlerts(futures.WatchConfig{Alert: futures.AlertConfig{Feishu: true}}, sampleEvents())
	if note := s.Watch.Status().AlertNote; !strings.Contains(note, "飞书未配置") {
		t.Fatalf("note=%s", note)
	}
}

func TestPushFuturesAlertsFeishuFailure(t *testing.T) {
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":9499,"msg":"hook 失效"}`))
	}))
	t.Cleanup(hook.Close)
	s := &Server{Cfg: config.Config{FeishuWebhook: hook.URL}, Watch: futures.NewDefaultWatcher()}
	s.pushFuturesAlerts(futures.WatchConfig{Alert: futures.AlertConfig{Feishu: true}}, sampleEvents())
	if note := s.Watch.Status().AlertNote; !strings.Contains(note, "飞书推送失败") {
		t.Fatalf("note=%s", note)
	}
}

func TestFuturesWatchAlertTestValidation(t *testing.T) {
	s := &Server{Cfg: config.Config{}, Watch: futures.NewDefaultWatcher()}
	rec := httptest.NewRecorder()
	s.futuresWatchAlertTest(rec, httptest.NewRequest(http.MethodPost, "/api/futures/watch/alert/test", strings.NewReader(`{}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("两个通道都没勾应 400：%d", rec.Code)
	}
}
