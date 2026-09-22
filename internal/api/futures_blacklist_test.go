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

func TestNormalizeBlacklistInput(t *testing.T) {
	cases := []struct {
		scope, value, wantScope, wantValue string
		wantErr                            bool
	}{
		{"variety", "jm", store.BlacklistScopeVariety, "JM", false},
		{"VARIETY", " JM0 ", store.BlacklistScopeVariety, "JM", false},
		{"contract", "rb2701", store.BlacklistScopeContract, "RB2701", false},
		{"contract", "JM0", "", "", true}, // 主连代码不该用合约级
		{"variety", "XX", "", "", true},   // 未知品种
		{"", "JM", "", "", true},          // scope 必填
		{"whatever", "JM", "", "", true},
		{"contract", "", "", "", true},
	}
	for _, c := range cases {
		scope, value, err := normalizeBlacklistInput(c.scope, c.value)
		if c.wantErr {
			if err == nil {
				t.Fatalf("%s/%s 应该报错", c.scope, c.value)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s/%s 不该报错：%v", c.scope, c.value, err)
		}
		if scope != c.wantScope || value != c.wantValue {
			t.Fatalf("%s/%s → %s/%s，期望 %s/%s", c.scope, c.value, scope, value, c.wantScope, c.wantValue)
		}
	}
}

func newBlacklistServer(t *testing.T) *Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	w := futures.NewDefaultWatcher()
	w.SetContractResolver(nil) // 测试不打网络
	return &Server{Store: st, Watch: w}
}

func TestFuturesBlacklistHandlers(t *testing.T) {
	s := newBlacklistServer(t)

	post := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.futuresBlacklistAdd(rec, httptest.NewRequest(http.MethodPost, "/api/futures/blacklist", strings.NewReader(body)))
		return rec
	}

	if rec := post(`{"scope":"variety","value":"jm","note":"焦煤不看"}`); rec.Code != http.StatusOK {
		t.Fatalf("加入品种失败：%d %s", rec.Code, rec.Body.String())
	}
	if rec := post(`{"scope":"contract","value":"rb2701"}`); rec.Code != http.StatusOK {
		t.Fatalf("加入合约失败：%d %s", rec.Code, rec.Body.String())
	}
	if rec := post(`{"scope":"contract","value":"JM0"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("主连代码应 400：%d", rec.Code)
	}
	if rec := post(`{"scope":"variety","value":"ZZ"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("未知品种应 400：%d", rec.Code)
	}

	// 列表
	rec := httptest.NewRecorder()
	s.futuresBlacklistList(rec, httptest.NewRequest(http.MethodGet, "/api/futures/blacklist", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("列表失败：%d", rec.Code)
	}
	var env struct {
		OK   bool                          `json:"ok"`
		Data []store.FuturesBlacklistEntry `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if len(env.Data) != 2 {
		t.Fatalf("应有 2 条：%+v", env.Data)
	}

	// 移除
	rec = httptest.NewRecorder()
	s.futuresBlacklistRemove(rec, httptest.NewRequest(
		http.MethodDelete, "/api/futures/blacklist?scope=contract&value=rb2701", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("移除失败：%d %s", rec.Code, rec.Body.String())
	}
	list, _ := s.Store.ListFuturesBlacklist()
	if len(list) != 1 || list[0].Value != "JM" {
		t.Fatalf("移除后不对：%+v", list)
	}

	// 非法删除同样 400
	rec = httptest.NewRecorder()
	s.futuresBlacklistRemove(rec, httptest.NewRequest(
		http.MethodDelete, "/api/futures/blacklist?scope=contract&value=whatever", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法值应 400：%d", rec.Code)
	}
}

func TestApplyFuturesBlacklistFiltersWatch(t *testing.T) {
	s := newBlacklistServer(t)
	if err := s.Store.AddFuturesBlacklist(store.BlacklistScopeVariety, "JM", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.AddFuturesBlacklist(store.BlacklistScopeContract, "RB2701", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.applyFuturesBlacklist(); err != nil {
		t.Fatal(err)
	}

	// 启动监控：JM 整体被拉黑 → 不进扫描范围
	st, err := s.Watch.Start(futures.WatchConfig{
		Params:   futures.Params{Period: "5"},
		Prefixes: []string{"JM", "RB"},
		Interval: futures.WatchMaxInterval,
	})
	s.Watch.Stop()
	if err != nil {
		t.Fatal(err)
	}
	if st.Varieties != 1 {
		t.Fatalf("JM 应被黑名单挡掉：%d", st.Varieties)
	}
}
