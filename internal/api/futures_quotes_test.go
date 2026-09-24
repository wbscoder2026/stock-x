package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wbscoder2026/stock-x/internal/futures"
)

func TestFuturesQuotesRejectsBadInput(t *testing.T) {
	srv, _ := newLocalServer(t)

	cases := []string{
		"/api/futures/quotes",            // 没给代码
		"/api/futures/quotes?symbols=,,", // 全是空
		"/api/futures/quotes?symbols=A00,A10,A20,A30,A40,A50,A60,A70,A80,A90,A100,A110,A120,A130,A140,A150,A160,A170,A180,A190,A200,A210,A220,A230,A240,A250,A260,A270,A280,A290,A300", // 31 个，超上限
	}
	for _, path := range cases {
		rec := httptest.NewRecorder()
		srv.futuresQuotes(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s 应 400，实际 %d %s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestFuturesQuotesRouteRegistered(t *testing.T) {
	srv, _ := newLocalServer(t)

	// 假上游：分钟线给一行（价 101 / 持仓 5200），日线给昨天 95
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.RawQuery, "min") {
			_, _ = w.Write([]byte(`=([["2026-09-24 09:02:00","100","102","99","101","20","5200"]]);`))
			return
		}
		_, _ = w.Write([]byte(`=([["2026-09-23","90","96","89","95","5","4000","94"]]);`))
	}))
	t.Cleanup(up.Close)
	srv.Quotes = futures.NewQuoteService(&futures.Client{MinuteURL: up.URL + "?min", DailyURL: up.URL + "?day", HQListURL: up.URL + "/hq="})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/futures/quotes?symbols=JM0,RB0", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("路由未通：%d %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data []futures.Quote `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if len(env.Data) != 2 {
		t.Fatalf("应返回 2 条：%+v", env.Data)
	}
	if env.Data[0].Symbol != "JM0" || env.Data[1].Symbol != "RB0" {
		t.Fatalf("顺序应保持请求顺序：%+v", env.Data)
	}
	if env.Data[0].Price != 101 || env.Data[0].Hold != 5200 {
		t.Fatalf("价格/持仓量不对：%+v", env.Data[0])
	}
	if env.Data[0].Name != "焦煤主连" {
		t.Fatalf("名称不对：%+v", env.Data[0])
	}
}

// 上游挂了不能让整个接口 500：要按条返回错误，浮窗逐行变灰并提示
func TestFuturesQuotesDegradesWhenUpstreamDown(t *testing.T) {
	srv, _ := newLocalServer(t)
	srv.Quotes = futures.NewQuoteService(&futures.Client{
		MinuteURL: "http://127.0.0.1:1/min",
		DailyURL:  "http://127.0.0.1:1/day",
		HQListURL: "http://127.0.0.1:1/hq=",
	})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/futures/quotes?symbols=JM0,RB0", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("上游挂了也应 200（按条报错）：%d %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data []futures.Quote `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if len(env.Data) != 2 {
		t.Fatalf("应仍返回 2 条：%+v", env.Data)
	}
	for _, q := range env.Data {
		if q.Error == "" {
			t.Fatalf("每条都该带错误：%+v", q)
		}
	}
}

func TestFuturesOverviewRouteRegistered(t *testing.T) {
	srv, _ := newLocalServer(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"symbol":"RB2701","exchange":"shfe","name":"螺纹钢2701","position":"123456"},` +
			`{"symbol":"RB0","exchange":"shfe","name":"螺纹钢主连","position":"999999"}]`))
	}))
	t.Cleanup(up.Close)
	srv.Catalog = futures.NewContractCatalog(&futures.Client{HQURL: up.URL})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/futures/overview", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("路由未注册：%d %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data []futures.VarietyContracts `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if len(env.Data) != len(futures.ListVarieties()) {
		t.Fatalf("应覆盖全部品种：%d", len(env.Data))
	}
	found := false
	for _, item := range env.Data {
		if item.Prefix == "RB" {
			found = true
			if item.MainSymbol != "RB0" || len(item.Contracts) != 2 {
				t.Fatalf("RB 清单不对：%+v", item)
			}
		}
	}
	if !found {
		t.Fatal("总览里应有 RB")
	}
}

// 诊断口：要能拿到原始行 + 逐字段 + 分钟线对照（字段位置有歧义时靠它定位）
func TestFuturesQuotesRawDiagnostic(t *testing.T) {
	srv, _ := newLocalServer(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/hq=") {
			_, _ = w.Write([]byte(`var hq_str_nf_JM0="焦煤主连,1160.000,1175.000,1150.000,1155.000,1159.500,1160.500,1160.000,1162.000,1155.000,320,210,150000,88000,2026-09-24,14:59:59";`))
			return
		}
		_, _ = w.Write([]byte(`=([["2026-09-24 09:02:00","1159","1162","1158","1160","20","5200"]]);`))
	}))
	t.Cleanup(up.Close)
	srv.Quotes = futures.NewQuoteService(&futures.Client{
		MinuteURL: up.URL + "?min", DailyURL: up.URL + "?day", HQListURL: up.URL + "/hq=",
	})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/futures/quotes/raw?symbols=JM0", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("诊断口未通：%d %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data map[string]struct {
			RawLine string   `json:"raw_line"`
			Fields  []string `json:"fields"`
			Kline   struct {
				Price float64 `json:"price"`
				Hold  float64 `json:"hold"`
			} `json:"kline"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	item, ok := env.Data["JM0"]
	if !ok {
		t.Fatalf("缺 JM0：%s", rec.Body.String())
	}
	if !strings.Contains(item.RawLine, "hq_str_nf_JM0") {
		t.Fatalf("应带原始行：%+v", item)
	}
	if len(item.Fields) < 14 || !strings.HasPrefix(item.Fields[12], "12=") {
		t.Fatalf("应带逐字段索引：%+v", item.Fields)
	}
	if item.Kline.Price != 1160 || item.Kline.Hold != 5200 {
		t.Fatalf("应带分钟线对照：%+v", item.Kline)
	}
}
