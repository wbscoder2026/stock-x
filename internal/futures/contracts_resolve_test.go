package futures

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSinaContractsResolvePicksTopPosition(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("node") != "jm_qh" {
			t.Errorf("node=%s", r.URL.Query().Get("node"))
		}
		_, _ = w.Write([]byte(`[{"symbol":"JM0","name":"焦煤连续","position":"99"},` +
			`{"symbol":"JM2612","name":"焦煤2612","position":"1200"},` +
			`{"symbol":"JM2701","name":"焦煤2701","position":"88000"},` +
			`{"symbol":"JM2705","name":"焦煤2705","position":"3000"}]`))
	}))
	t.Cleanup(srv.Close)

	r := NewSinaContracts(&Client{HQURL: srv.URL})
	symbol, label, err := r.Resolve(context.Background(), mustVariety(t, "JM"))
	if err != nil {
		t.Fatal(err)
	}
	// 持仓最大的月份合约 = 主力；连续代码 JM0 不算
	if symbol != "JM2701" || label != "2701" {
		t.Fatalf("%s / %s", symbol, label)
	}
}

func TestSinaContractsResolveNoMonth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"symbol":"JM0","name":"焦煤连续","position":"99"}]`))
	}))
	t.Cleanup(srv.Close)
	r := NewSinaContracts(&Client{HQURL: srv.URL})
	if _, _, err := r.Resolve(context.Background(), mustVariety(t, "JM")); err == nil {
		t.Fatal("只有连续代码时应报错")
	}
}

func TestSinaContractsResolveUpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	r := NewSinaContracts(&Client{HQURL: srv.URL})
	if _, _, err := r.Resolve(context.Background(), mustVariety(t, "JM")); err == nil {
		t.Fatal("上游失败应报错")
	}
}
