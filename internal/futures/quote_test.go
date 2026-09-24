package futures

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeQuoteServer 假上游：分钟线给两行（最新 101 / 持仓 5200），日线给昨天 95 + 今天 100
func fakeQuoteServer(t *testing.T, hits *int32, fail *atomic.Bool) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(hits, 1)
		if fail != nil && fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if strings.Contains(r.URL.RawQuery, "getFewMinLine") {
			_, _ = w.Write([]byte(`=([["2026-09-24 09:01:00","99","100","98","100","10","5000"],` +
				`["2026-09-24 09:02:00","100","102","99","101","20","5200"]]);`))
			return
		}
		_, _ = w.Write([]byte(`=([["2026-09-23","90","96","89","95","5","4000","94"],` +
			`["2026-09-24","95","101","94","100","6","4100","99"]]);`))
	}))
	t.Cleanup(srv.Close)
	// 实时口返回空 → 走分钟线（这些用例测的就是分钟线那条路）
	return &Client{MinuteURL: srv.URL + "?api=getFewMinLine", DailyURL: srv.URL + "?api=getDaily", HQListURL: srv.URL + "/hq="}
}

func TestQuoteServiceSnapshot(t *testing.T) {
	var hits int32
	c := fakeQuoteServer(t, &hits, nil)
	svc := NewQuoteService(c)
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, locCST)
	svc.Now = func() time.Time { return now }

	got := svc.Snapshot(context.Background(), []string{"JM0"})
	if len(got) != 1 {
		t.Fatalf("应返回 1 条：%+v", got)
	}
	q := got[0]
	if q.Symbol != "JM0" || q.Name != "焦煤主连" {
		t.Fatalf("代码/名称不对：%+v", q)
	}
	if q.Price != 101 || q.Hold != 5200 || q.Volume != 20 {
		t.Fatalf("价格/持仓/成交量不对：%+v", q)
	}
	if q.Time != "2026-09-24 09:02" {
		t.Fatalf("时间不对：%s", q.Time)
	}
	// 昨收 = 今天之前的最后一根日线收盘 = 95
	if q.PrevClose != 95 {
		t.Fatalf("昨收应为 95：%+v", q)
	}
	if want := (101.0 - 95.0) / 95.0 * 100; q.ChangePct < want-0.01 || q.ChangePct > want+0.01 {
		t.Fatalf("涨跌幅应为 %.2f，实际 %.2f", want, q.ChangePct)
	}

	// 缓存：TTL 内再问不该再打上游
	before := atomic.LoadInt32(&hits)
	svc.Snapshot(context.Background(), []string{"JM0"})
	if after := atomic.LoadInt32(&hits); after != before {
		t.Fatalf("缓存没生效：hits %d → %d", before, after)
	}

	// 过了 TTL 应该重新取
	now = now.Add(10 * time.Second)
	svc.Snapshot(context.Background(), []string{"JM0"})
	if after := atomic.LoadInt32(&hits); after <= before {
		t.Fatal("过了 TTL 应重新请求")
	}
}

func TestQuoteServiceContractNameAndBatchOrder(t *testing.T) {
	var hits int32
	svc := NewQuoteService(fakeQuoteServer(t, &hits, nil))
	svc.Now = func() time.Time { return time.Date(2026, 9, 24, 10, 0, 0, 0, locCST) }

	got := svc.Snapshot(context.Background(), []string{"JM2601", "RB0", "NOPE"})
	if len(got) != 3 {
		t.Fatalf("应原样返回请求的条数（含错误项）：%+v", got)
	}
	// 顺序要和请求一致（前端按顺序渲染）
	if got[0].Symbol != "JM2601" || got[1].Symbol != "RB0" || got[2].Symbol != "NOPE" {
		t.Fatalf("顺序不对：%+v", got)
	}
	if got[0].Name != "焦煤2601" {
		t.Fatalf("合约名应是 焦煤2601：%+v", got[0])
	}
	if got[2].Error == "" {
		t.Fatalf("未知代码应带错误：%+v", got[2])
	}
	// 未知代码不该污染其它项
	if got[1].Price == 0 {
		t.Fatalf("RB0 应照常有价格：%+v", got[1])
	}
}

func TestQuoteServiceKeepsLastOnFailure(t *testing.T) {
	var hits int32
	var fail atomic.Bool
	svc := NewQuoteService(fakeQuoteServer(t, &hits, &fail))
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, locCST)
	svc.Now = func() time.Time { return now }

	first := svc.Snapshot(context.Background(), []string{"JM0"})
	if first[0].Price == 0 {
		t.Fatalf("首次应成功：%+v", first[0])
	}

	// 上游挂了：要给出上一次的价格 + 明确的错误，而不是留空
	fail.Store(true)
	now = now.Add(time.Minute)
	got := svc.Snapshot(context.Background(), []string{"JM0"})
	if got[0].Price != 101 {
		t.Fatalf("应保留上一次价格：%+v", got[0])
	}
	if got[0].Error == "" {
		t.Fatalf("应标记错误让页面能提示：%+v", got[0])
	}
}
