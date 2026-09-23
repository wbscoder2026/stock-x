package futures

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestEastmoneySecidOfSymbol(t *testing.T) {
	// 主连 JM0 → 114.jmm（和原来 eastmoneySecid 一致）
	if got, ok := eastmoneySecidOf("JM0"); !ok || got != "114.jmm" {
		t.Fatalf("主连 secid=%s ok=%v", got, ok)
	}
	// 月份合约 JM2601 → 114.jm2601
	if got, ok := eastmoneySecidOf("JM2601"); !ok || got != "114.jm2601" {
		t.Fatalf("合约 secid=%s ok=%v", got, ok)
	}
	// 上期所 螺纹钢：RB0 → 113.rbm，RB2601 → 113.rb2601
	if got, ok := eastmoneySecidOf("RB0"); !ok || got != "113.rbm" {
		t.Fatalf("RB 主连 secid=%s ok=%v", got, ok)
	}
	if got, ok := eastmoneySecidOf("rb2601"); !ok || got != "113.rb2601" {
		t.Fatalf("小写也应认：secid=%s ok=%v", got, ok)
	}
	if _, ok := eastmoneySecidOf("NOPE0"); ok {
		t.Fatal("未知品种不应认")
	}
	if _, ok := eastmoneySecidOf(""); ok {
		t.Fatal("空代码不应认")
	}
}

func TestEastmoneyMinuteRangeSymbolUsesContractCode(t *testing.T) {
	var secid string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secid = r.URL.Query().Get("secid")
		_, _ = w.Write([]byte(`{"rc":0,"data":{"code":"rbm","market":113,"klines":["2026-09-18 09:01,100,101,102,99,10,20"]}}`))
	}))
	t.Cleanup(srv.Close)
	src := NewEastmoneySource(srv.URL)
	ctx := context.Background()
	stop := time.Date(2026, 9, 18, 0, 0, 0, 0, locCST)

	// 主连
	if _, err := src.MinuteRangeSymbol(ctx, "RB0", "1", stop, 1000); err != nil {
		t.Fatal(err)
	}
	if secid != "113.rbm" {
		t.Fatalf("主连 secid=%s", secid)
	}
	// 月份合约
	bars, err := src.MinuteRangeSymbol(ctx, "RB2601", "1", stop, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if secid != "113.rb2601" {
		t.Fatalf("合约 secid=%s", secid)
	}
	if len(bars) != 1 || bars[0].Close != 101 {
		t.Fatalf("%+v", bars)
	}
	// 不支持的代码 → 报错
	if _, err := src.MinuteRangeSymbol(ctx, "NOPE2601", "1", stop, 1000); err == nil {
		t.Fatal("未知代码应报错")
	}
}
