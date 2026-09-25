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

// goodTickLine 按**真实返回布局**写：索引 1 是时间（HHMMSS），字段整体比"无时间"布局往后挪一位。
// 真实样例见 TestRealtimeTickFromRealSample。
const goodTickLine = `var hq_str_nf_JM0="焦煤主连,145959,1155.000,1175.000,1150.000,1155.000,1159.500,1160.500,1160.000,1162.000,1155.000,320,210,150000,88000,连,焦煤,2026-09-24";`

func TestRealtimeTicksParsesBidAsk(t *testing.T) {
	var gotList string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotList = r.URL.Path
		if r.Header.Get("Referer") == "" {
			t.Error("少了 Referer，新浪会返回空")
		}
		_, _ = w.Write([]byte(goodTickLine + "\n" +
			`var hq_str_nf_RB0="螺纹钢主连,145959,3100,3120,3090,3105,3104,3105,3104,3110,3105,100,200,900000,50000,连,螺纹钢,2026-09-24";`))
	}))
	t.Cleanup(srv.Close)

	c := &Client{HQListURL: srv.URL + "/list="}
	got, err := c.RealtimeTicks(context.Background(), []string{"jm0", "RB0"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotList, "nf_JM0,nf_RB0") {
		t.Fatalf("请求的代码不对：%s", gotList)
	}
	jm, ok := got["JM0"]
	if !ok {
		t.Fatalf("缺 JM0：%+v", got)
	}
	if jm.Price != 1160 || jm.Hold != 150000 || jm.Volume != 88000 {
		t.Fatalf("价格/持仓/成交不对：%+v", jm)
	}
	if jm.Bid != 1159.5 || jm.Ask != 1160.5 || jm.BidVol != 320 || jm.AskVol != 210 {
		t.Fatalf("买一卖一不对：%+v", jm)
	}
	if jm.TickTime() != "2026-09-24 14:59" {
		t.Fatalf("时间不对：%s", jm.TickTime())
	}
	if _, ok := got["RB0"]; !ok {
		t.Fatal("缺 RB0")
	}
}

func TestRealtimeTicksRejectsInsaneRows(t *testing.T) {
	// 这些行用「无时间」布局（index 1 不是 HHMMSS），顺便验另一种布局也能识别。
	// 三行都有问题：① 最新价跑到最高之外 ② 买价高于卖价 ③ 持仓量为 0
	// 这些必须被丢掉（调用方会退回分钟线取价），绝不能当实时价显示。
	body := strings.Join([]string{
		`var hq_str_nf_A0="X,100,120,110,115,114,115,9999,116,115,10,20,1234,5678,2026-09-24,14:59:59";`,
		`var hq_str_nf_B0="X,100,120,90,110,118,112,115,116,110,10,20,1234,5678,2026-09-24,14:59:59";`,
		`var hq_str_nf_C0="X,100,120,90,110,114,115,115,116,110,10,20,0,5678,2026-09-24,14:59:59";`,
		`var hq_str_nf_D0="字段太少";`,
	}, "\n")
	got := parseHQTicks([]byte(body))
	if len(got) != 0 {
		t.Fatalf("这些行都该被拒：%+v", got)
	}
}

func TestQuoteServiceUsesRealtimeTickForBidAsk(t *testing.T) {
	var minHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/hq=") {
			_, _ = w.Write([]byte(goodTickLine))
			return
		}
		if strings.Contains(r.URL.RawQuery, "min") {
			atomic.AddInt32(&minHits, 1) // 只数分钟线（昨收那次日线不算）
			// 收盘价要和实时口的「最新价」(1160) 对得上，否则盘口会被交叉校验丢掉
			_, _ = w.Write([]byte(`=([["2026-09-24 09:02:00","1159","1162","1158","1160","20","5200"]]);`))
			return
		}
		_, _ = w.Write([]byte(`=([["2026-09-23","90","96","89","95","5","4000","94"]]);`))
	}))
	t.Cleanup(srv.Close)

	svc := NewQuoteService(&Client{
		MinuteURL: srv.URL + "?min",
		DailyURL:  srv.URL + "?day",
		HQListURL: srv.URL + "/hq=",
	})
	svc.Now = func() time.Time { return time.Date(2026, 9, 24, 15, 0, 0, 0, locCST) }

	got := svc.Snapshot(context.Background(), []string{"JM0"})
	if len(got) != 1 {
		t.Fatalf("应 1 条：%+v", got)
	}
	q := got[0]
	if q.Bid != 1159.5 || q.Ask != 1160.5 || q.BidVol != 320 || q.AskVol != 210 {
		t.Fatalf("应带上买一卖一：%+v", q)
	}
	// 价格/持仓量以分钟线为准（实时口那个持仓量索引靠不住），盘口才用实时口
	if q.Price != 1160 || q.Hold != 5200 {
		t.Fatalf("价格/持仓量应取分钟线：%+v", q)
	}
	if q.Source != "kline+hq" {
		t.Fatalf("带盘口时应标 kline+hq：%+v", q)
	}
	if atomic.LoadInt32(&minHits) == 0 {
		t.Fatal("必须去取分钟线（持仓量只有它准）")
	}
}

func TestQuoteServiceFallsBackWhenTickRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/hq=") {
			// 买价高于卖价 → 校验不过 → 必须退回分钟线
			_, _ = w.Write([]byte(`var hq_str_nf_JM0="X,100,120,90,110,118,112,115,116,110,10,20,1234,5678,2026-09-24,14:59:59";`))
			return
		}
		if strings.Contains(r.URL.RawQuery, "min") {
			_, _ = w.Write([]byte(`=([["2026-09-24 09:02:00","100","102","99","101","20","5200"]]);`))
			return
		}
		_, _ = w.Write([]byte(`=([["2026-09-23","90","96","89","95","5","4000","94"]]);`))
	}))
	t.Cleanup(srv.Close)

	svc := NewQuoteService(&Client{MinuteURL: srv.URL + "?min", DailyURL: srv.URL + "?day", HQListURL: srv.URL + "/hq="})
	svc.Now = func() time.Time { return time.Date(2026, 9, 24, 15, 0, 0, 0, locCST) }

	got := svc.Snapshot(context.Background(), []string{"JM0"})
	if got[0].Price != 101 || got[0].Hold != 5200 {
		t.Fatalf("校验不过时应退回分钟线：%+v", got[0])
	}
	if got[0].Bid != 0 {
		t.Fatalf("退回时不该带买一卖一：%+v", got[0])
	}
	if got[0].Source != "kline" {
		t.Fatalf("来源应标 kline：%+v", got[0])
	}
}

// 实时口的「最新价」和分钟线对不上 → 说明字段认错了 → 只保留价格，不要盘口
func TestQuoteServiceDropsBookWhenPriceMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/hq=") {
			// 实时口报 5000，分钟线是 101 → 明显不是同一合约/认错了字段
			_, _ = w.Write([]byte(`var hq_str_nf_JM0="X,4990,5100,4900,5000,4999,5001,5000,5000,5000,10,20,1234,5678,2026-09-24,14:59:59";`))
			return
		}
		if strings.Contains(r.URL.RawQuery, "min") {
			_, _ = w.Write([]byte(`=([["2026-09-24 09:02:00","100","102","99","101","20","5200"]]);`))
			return
		}
		_, _ = w.Write([]byte(`=([["2026-09-23","90","96","89","95","5","4000","94"]]);`))
	}))
	t.Cleanup(srv.Close)

	svc := NewQuoteService(&Client{MinuteURL: srv.URL + "?min", DailyURL: srv.URL + "?day", HQListURL: srv.URL + "/hq="})
	svc.Now = func() time.Time { return time.Date(2026, 9, 24, 15, 0, 0, 0, locCST) }

	got := svc.Snapshot(context.Background(), []string{"JM0"})
	if got[0].Price != 101 || got[0].Hold != 5200 {
		t.Fatalf("应仍用分钟线：%+v", got[0])
	}
	if got[0].Bid != 0 || got[0].Ask != 0 {
		t.Fatalf("价格对不上时不该采用它的盘口：%+v", got[0])
	}
	if got[0].Source != "kline" {
		t.Fatalf("来源应只剩 kline：%+v", got[0])
	}
}

// 分钟线取不到时才拿实时口兜底（这时持仓量可能不如分钟线准，用 source 能看出来）
func TestQuoteServiceUsesTickOnlyWhenKlineUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/hq=") {
			_, _ = w.Write([]byte(goodTickLine))
			return
		}
		if strings.Contains(r.URL.RawQuery, "min") {
			_, _ = w.Write([]byte(`=();`)) // 分钟线空数据
			return
		}
		_, _ = w.Write([]byte(`=([["2026-09-23","1150","1175","1145","1155","5","140000","1152"]]);`))
	}))
	t.Cleanup(srv.Close)

	svc := NewQuoteService(&Client{MinuteURL: srv.URL + "?min", DailyURL: srv.URL + "?day", HQListURL: srv.URL + "/hq="})
	svc.Now = func() time.Time { return time.Date(2026, 9, 24, 15, 0, 0, 0, locCST) }

	got := svc.Snapshot(context.Background(), []string{"JM0"})
	if got[0].Price != 1160 || got[0].Hold != 150000 {
		t.Fatalf("分钟线没有时用实时口兜底：%+v", got[0])
	}
	if got[0].Bid != 1159.5 || got[0].Source != "hq" {
		t.Fatalf("兜底时也带盘口并标 hq：%+v", got[0])
	}
}

// 用**真实返回样例**钉住字段布局：索引 1 是时间，所以买价/卖价/持仓量比"无时间"布局各挪一位。
// 这条用例的作用是防止再把「卖量当持仓量、昨收当买一价」这种错位放过去。
func TestRealtimeTickFromRealSample(t *testing.T) {
	// 样例来自公开文章（新浪期货接口，豆油1309）
	line := `var hq_str_Y1309="豆油1309,145958,7120,7190,7112,7150,7150,7156,7152,7164,7140,55,42,318796,97838,连,豆油,2013-06-28";`
	got := parseHQTicks([]byte(line))
	tick, ok := got["Y1309"]
	if !ok {
		t.Fatalf("真实样例应能解析出来：%+v", got)
	}
	if tick.Price != 7152 {
		t.Fatalf("最新价应是 7152（索引 8）：%+v", tick)
	}
	if tick.Bid != 7150 || tick.Ask != 7156 {
		t.Fatalf("买价 7150 / 卖价 7156（索引 6/7）：%+v", tick)
	}
	if tick.BidVol != 55 || tick.AskVol != 42 {
		t.Fatalf("买量 55 / 卖量 42（索引 11/12）：%+v", tick)
	}
	if tick.Hold != 318796 {
		t.Fatalf("持仓量应是 318796（索引 13，不是卖量 42）：%+v", tick)
	}
	if tick.Volume != 97838 {
		t.Fatalf("成交量应是 97838（索引 14）：%+v", tick)
	}
	if tick.Day != "2013-06-28" {
		t.Fatalf("日期应取到：%+v", tick)
	}
	if tick.TickTime() != "2013-06-28 14:59" {
		t.Fatalf("时间应组合成 2013-06-28 14:59：%s", tick.TickTime())
	}
}
