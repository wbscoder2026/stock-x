package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wbscoder2026/stock-x/internal/futures"
	"github.com/wbscoder2026/stock-x/internal/store"
)

func newWatchConfigServer(t *testing.T) *Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{Store: st, Watch: futures.NewDefaultWatcher()}
	srv.Watch.SetContractResolver(nil) // 测试不打网络
	t.Cleanup(func() {
		srv.Watch.Stop()
		_ = st.Close()
	})
	return srv
}

func getConfig(t *testing.T, s *Server) futures.WatchConfig {
	t.Helper()
	rec := httptest.NewRecorder()
	s.futuresWatchConfigGet(rec, httptest.NewRequest(http.MethodGet, "/api/futures/watch/config", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("读配置失败：%d %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data futures.WatchConfig `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	return env.Data
}

func postConfig(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.futuresWatchConfigSave(rec, httptest.NewRequest(http.MethodPost, "/api/futures/watch/config", strings.NewReader(body)))
	return rec
}

func TestFuturesWatchConfigDefaults(t *testing.T) {
	s := newWatchConfigServer(t)
	cfg := getConfig(t, s)
	// 用户没配过 → 默认开启监控 + 默认开桌面通知 + 保留 30 分钟
	if !cfg.Enabled || !cfg.Alert.Desktop || cfg.AlertTTLMin != futures.DefaultAlertTTLMin {
		t.Fatalf("默认配置不对：%+v", cfg)
	}
	if cfg.Interval != futures.WatchDefaultInterval || cfg.Params.Period == "" {
		t.Fatalf("默认项要补齐：%+v", cfg)
	}
}

func TestFuturesWatchConfigSavedWithoutRunning(t *testing.T) {
	s := newWatchConfigServer(t)

	// 没在跑时保存 = 只落库，不顺手启动（语义要清晰）
	rec := postConfig(t, s, `{"period":"15","interval":60,"prefixes":["JM"],"alert":{"desktop":false},"alert_ttl_min":7,"rr":2}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("保存失败：%d %s", rec.Code, rec.Body.String())
	}
	if s.Watch.Status().Running {
		t.Fatal("保存配置不该把监控启动起来")
	}

	cfg := getConfig(t, s)
	if cfg.Period != "15" || cfg.Interval != 60 || cfg.AlertTTLMin != 7 || cfg.RR != 2 {
		t.Fatalf("参数没存住：%+v", cfg)
	}
	if len(cfg.Prefixes) != 1 || cfg.Prefixes[0] != "JM" {
		t.Fatalf("品种没存住：%+v", cfg.Prefixes)
	}
	if cfg.Alert.Desktop {
		t.Fatal("关掉的桌面通知不该被打开")
	}
	if cfg.Enabled {
		t.Fatal("没在跑 → enabled 应为 false（重启后不会自己开起来）")
	}
}

func TestFuturesWatchConfigHotUpdateWhileRunning(t *testing.T) {
	s := newWatchConfigServer(t)
	rec := httptest.NewRecorder()
	s.futuresWatchStart(rec, httptest.NewRequest(http.MethodPost, "/api/futures/watch/start", strings.NewReader(
		`{"period":"5","interval":600,"prefixes":["JM","RB"],"alert":{"desktop":true},"alert_ttl_min":30}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("启动失败：%d %s", rec.Code, rec.Body.String())
	}
	if cfg := getConfig(t, s); !cfg.Enabled {
		t.Fatal("启动后 enabled 应为 true")
	}

	// 运行中改参数：立刻热生效 + 落库
	if rec := postConfig(t, s, `{"period":"5","interval":600,"prefixes":["JM","RB"],"alert_ttl_min":5,"rr":3}`); rec.Code != http.StatusOK {
		t.Fatalf("热更新失败：%d %s", rec.Code, rec.Body.String())
	}
	st := s.Watch.Status()
	if st.Config.AlertTTLMin != 5 || st.AlertTTLSec != 300 || st.Config.RR != 3 {
		t.Fatalf("没热生效：%+v", st.Config)
	}
	if getConfig(t, s).AlertTTLMin != 5 {
		t.Fatal("热更新也要落库")
	}
}

func TestFuturesWatchStopPersistsDisabled(t *testing.T) {
	s := newWatchConfigServer(t)
	rec := httptest.NewRecorder()
	s.futuresWatchStart(rec, httptest.NewRequest(http.MethodPost, "/api/futures/watch/start", strings.NewReader(
		`{"period":"5","interval":600,"prefixes":["JM"],"alert":{"desktop":true}}`)))
	rec = httptest.NewRecorder()
	s.futuresWatchStop(rec, httptest.NewRequest(http.MethodPost, "/api/futures/watch/stop", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("停止失败：%d", rec.Code)
	}
	cfg := getConfig(t, s)
	if cfg.Enabled {
		t.Fatal("停止后 enabled 应为 false")
	}
	if cfg.Period != "5" || len(cfg.Prefixes) != 1 { // 停止不该丢掉已保存的参数
		t.Fatalf("停止后参数丢了：%+v", cfg)
	}

	// 再恢复：主动停过的不该自己开起来
	s2 := &Server{Store: s.Store, Watch: futures.NewDefaultWatcher()}
	s2.Watch.SetContractResolver(nil)
	s2.restoreFuturesWatch()
	if s2.Watch.Status().Running {
		t.Fatal("用户主动停过，重启后不该自动开")
	}
}

func TestRestoreFuturesWatchAutoStarts(t *testing.T) {
	s := newWatchConfigServer(t)
	if rec := postConfig(t, s, `{"period":"15","interval":600,"prefixes":["JM"],"alert":{"desktop":true},"alert_ttl_min":7}`); rec.Code != http.StatusOK {
		t.Fatalf("保存失败：%d", rec.Code)
	}
	// 手工把 enabled 置回 true（模拟「上次是开着的」）
	cfg := getConfig(t, s)
	cfg.Enabled = true
	raw, _ := json.Marshal(cfg)
	if err := s.Store.SaveFuturesWatchConfig(raw); err != nil {
		t.Fatal(err)
	}

	// 新进程起来 → 自动恢复监控，并且用的是存下来的参数
	s2 := &Server{Store: s.Store, Watch: futures.NewDefaultWatcher()}
	s2.Watch.SetContractResolver(nil)
	t.Cleanup(func() { s2.Watch.Stop() })
	s2.restoreFuturesWatch()

	st := s2.Watch.Status()
	if !st.Running {
		t.Fatalf("应按保存的配置自动开启监控：%+v", st)
	}
	if st.Config.Period != "15" || st.AlertTTLSec != 420 {
		t.Fatalf("恢复的配置不对：%+v", st.Config)
	}
}

func TestRestoreFuturesWatchBadConfigDoesNotCrash(t *testing.T) {
	s := newWatchConfigServer(t)
	// 库里有非法品种码：自动恢复要失败但不 panic，且原因能看到
	if err := s.Store.SaveFuturesWatchConfig([]byte(`{"period":"5","prefixes":["ZZ"],"enabled":true}`)); err != nil {
		t.Fatal(err)
	}
	s.restoreFuturesWatch()
	st := s.Watch.Status()
	if st.Running {
		t.Fatal("非法配置不该启动起来")
	}
	if !strings.Contains(st.AlertNote, "自动恢复监控失败") {
		t.Fatalf("失败原因要写在状态里：%q", st.AlertNote)
	}
}

func TestFuturesWatchConfigDirtyPayloadFallsBack(t *testing.T) {
	s := newWatchConfigServer(t)
	if err := s.Store.SaveFuturesWatchConfig([]byte(`{ 这不是 JSON`)); err != nil {
		t.Fatal(err)
	}
	cfg := getConfig(t, s) // 不 panic、退回默认
	if !cfg.Enabled || cfg.AlertTTLMin != futures.DefaultAlertTTLMin {
		t.Fatalf("脏数据应退回默认：%+v", cfg)
	}
}

// 前面的用例都是直接调 handler，这里走一遍真实路由（注册写错就会漏）
func TestWatchConfigRoutesRegistered(t *testing.T) {
	s := newWatchConfigServer(t)
	h := s.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/futures/watch/config", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/futures/watch/config 未通：%d", rec.Code)
	}
	var env struct {
		Data futures.WatchConfig `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if !env.Data.Enabled || !env.Data.Alert.Desktop {
		t.Fatalf("默认应为「开启监控 + 桌面通知」：%+v", env.Data)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/futures/watch/config",
		strings.NewReader(`{"period":"30","interval":120,"alert_ttl_min":15}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST 配置未通：%d %s", rec.Code, rec.Body.String())
	}
	cfg := getConfig(t, s)
	if cfg.Period != "30" || cfg.AlertTTLMin != 15 || cfg.Interval != 120 {
		t.Fatalf("路由写库失败：%+v", cfg)
	}
}

// 回归：先停、再改参数、又点一次停止 —— 不能把刚保存的参数覆盖回「上次运行时的配置」
func TestFuturesWatchStopKeepsSavedParams(t *testing.T) {
	s := newWatchConfigServer(t)

	rec := httptest.NewRecorder()
	s.futuresWatchStart(rec, httptest.NewRequest(http.MethodPost, "/api/futures/watch/start", strings.NewReader(
		`{"period":"5","interval":600,"prefixes":["JM"],"alert_ttl_min":30,"rr":1.5}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("启动失败：%d", rec.Code)
	}
	s.futuresWatchStop(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/futures/watch/stop", nil))

	// 用户改成另一套参数（没在跑时只落库）
	if rec := postConfig(t, s, `{"period":"15","interval":60,"prefixes":["JM","RB"],"alert_ttl_min":12,"rr":2.5}`); rec.Code != http.StatusOK {
		t.Fatalf("保存失败：%d", rec.Code)
	}
	// 再点一次停止（幂等操作）—— 参数必须还在
	s.futuresWatchStop(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/futures/watch/stop", nil))

	cfg := getConfig(t, s)
	if cfg.Period != "15" || cfg.AlertTTLMin != 12 || cfg.RR != 2.5 || cfg.Interval != 60 {
		t.Fatalf("停止把刚保存的参数覆盖了：%+v", cfg)
	}
	if len(cfg.Prefixes) != 2 {
		t.Fatalf("品种也被覆盖了：%+v", cfg.Prefixes)
	}
	if cfg.Enabled {
		t.Fatal("停止后 enabled 应为 false")
	}
}
