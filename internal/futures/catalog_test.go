package futures

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestContractCatalogAllWithCacheAndFallback(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		// 焦煤的 node 故意挂掉：验证「单个品种失败不影响整页」
		if r.URL.Query().Get("node") == "jm_qh" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`[{"symbol":"RB2701","exchange":"shfe","name":"螺纹钢2701","position":"123456"},` +
			`{"symbol":"RB0","exchange":"shfe","name":"螺纹钢主连","position":"999999"}]`))
	}))
	t.Cleanup(srv.Close)

	cat := NewContractCatalog(&Client{HQURL: srv.URL})
	cat.Workers = 4
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, locCST)
	cat.Now = func() time.Time { return now }

	got := cat.All(context.Background())
	if len(got) != len(ListVarieties()) {
		t.Fatalf("应覆盖全部品种：%d != %d", len(got), len(ListVarieties()))
	}
	byPrefix := map[string]VarietyContracts{}
	for _, item := range got {
		byPrefix[item.Prefix] = item
	}
	var rb, jm VarietyContracts
	var okRB, okJM bool
	rb, okRB = byPrefix["RB"]
	jm, okJM = byPrefix["JM"]
	if !okRB || !okJM {
		t.Fatalf("缺少 RB / JM：%+v", byPrefix["RB"])
	}
	if rb.MainSymbol != "RB0" || len(rb.Contracts) != 2 {
		t.Fatalf("RB 合约清单不对：%+v", rb)
	}
	// 取合约失败的品种：仍要在列表里，且退化成只有主连 + 带错误说明
	if len(jm.Contracts) != 1 || jm.Contracts[0].Symbol != "JM0" || jm.Error == "" {
		t.Fatalf("JM 应退化成只有主连并带错误：%+v", jm)
	}

	// 缓存：TTL 内不该再打上游
	before := atomic.LoadInt32(&hits)
	cat.All(context.Background())
	if after := atomic.LoadInt32(&hits); after != before {
		t.Fatalf("缓存没生效：%d → %d", before, after)
	}

	// TTL 过期 → 重新拉
	now = now.Add(20 * time.Minute)
	cat.All(context.Background())
	if after := atomic.LoadInt32(&hits); after <= before {
		t.Fatal("过期应重新拉取")
	}
}
