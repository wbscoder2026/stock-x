package futures

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func mustVariety(t *testing.T, prefix string) Variety {
	t.Helper()
	v, ok := varietyByPrefix(prefix)
	if !ok {
		t.Fatalf("未知品种 %s", prefix)
	}
	return v
}

func emServer(t *testing.T, payload string) *EastmoneySource {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("klt") == "" {
			t.Errorf("缺 klt 参数")
		}
		if r.URL.Query().Get("secid") == "" {
			t.Errorf("缺 secid 参数")
		}
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(srv.Close)
	return NewEastmoneySource(srv.URL)
}

func TestEastmoneySecid(t *testing.T) {
	cases := map[string]string{
		"JM": "114.jmm", // 大商所
		"RB": "113.rbm", // 上期所
		"TA": "115.tam", // 郑商所（东财代码不分大小写）
		"SC": "142.scm", // 上期能源
		"SI": "225.sim", // 广期所
	}
	for prefix, want := range cases {
		got, ok := eastmoneySecid(mustVariety(t, prefix))
		if !ok || got != want {
			t.Fatalf("%s → %s/%v，期望 %s", prefix, got, ok, want)
		}
	}
	// 中金所只有「当月连续」编号，不是主力连续 → 不支持，交给新浪
	for _, prefix := range []string{"IF", "IH", "IC", "IM", "T", "TF", "TS"} {
		if _, ok := eastmoneySecid(mustVariety(t, prefix)); ok {
			t.Fatalf("中金所 %s 不该被东财覆盖", prefix)
		}
	}
}

func TestEastmoneySupports(t *testing.T) {
	src := NewEastmoneySource("")
	for _, prefix := range []string{"JM", "RB", "TA", "SC", "SI", "LU", "NR"} {
		if !src.Supports(mustVariety(t, prefix)) {
			t.Fatalf("%s 应支持", prefix)
		}
	}
	if src.Supports(mustVariety(t, "IF")) {
		t.Fatal("IF 不该支持")
	}
	if src.Name() != "eastmoney" {
		t.Fatalf("name=%s", src.Name())
	}
}

func TestEastmoneyMinuteFieldOrder(t *testing.T) {
	// 东财字段序是 时间,开,收,高,低,量,额 —— 和「开高低收」不同，必须钉住
	payload := `{"rc":0,"data":{"code":"rbm","market":113,"name":"螺纹钢主连","klines":[` +
		`"2026-09-21 14:40,3116,3117,3118,3115,8996,280385840",` +
		`"2026-09-21 14:45,3117,3118,3118,3116,4634,144456660"]}}`
	bars, err := emServer(t, payload).Minute(context.Background(), mustVariety(t, "RB"), "5")
	if err != nil {
		t.Fatal(err)
	}
	if len(bars) != 2 {
		t.Fatalf("bars=%d", len(bars))
	}
	b := bars[0]
	if b.Open != 3116 || b.Close != 3117 || b.High != 3118 || b.Low != 3115 || b.Volume != 8996 {
		t.Fatalf("字段解析错了：%+v", b)
	}
	if got := b.Time.In(locCST).Format("2006-01-02 15:04"); got != "2026-09-21 14:40" {
		t.Fatalf("时间=%s", got)
	}
}

func TestEastmoneyDailyParse(t *testing.T) {
	payload := `{"rc":0,"data":{"code":"rbm","market":113,"name":"螺纹钢主连","klines":[` +
		`"2026-09-18,3123,3096,3132,3089,884858,27472875776"]}}`
	src := emServer(t, payload)
	v := mustVariety(t, "RB")
	days, err := src.Daily(context.Background(), v)
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 1 {
		t.Fatalf("days=%d", len(days))
	}
	d := days[0]
	if d.Open != 3123 || d.Close != 3096 || d.High != 3132 || d.Low != 3089 || d.Volume != 884858 {
		t.Fatalf("日线解析错了：%+v", d)
	}
	if d.Date.In(locCST).Format("2006-01-02") != "2026-09-18" {
		t.Fatalf("日期=%s", d.Date)
	}
}

func TestEastmoneyErrors(t *testing.T) {
	v := mustVariety(t, "RB")
	src := emServer(t, `{"rc":100,"rt":1,"data":null}`)
	if _, err := src.Minute(context.Background(), v, "5"); err == nil {
		t.Fatal("data=null 应报错，否则熔断切换不会触发")
	}
	if _, err := src.Daily(context.Background(), v); err == nil {
		t.Fatal("data=null 日线应报错")
	}

	ok := emServer(t, `{"rc":0,"data":{"klines":["2026-09-21 14:40,1,1,1,1,1"]}}`)
	if _, err := ok.Minute(context.Background(), v, "7"); err == nil {
		t.Fatal("非法周期应报错")
	}
	if _, err := ok.Minute(context.Background(), mustVariety(t, "IF"), "5"); err == nil {
		t.Fatal("不支持的品种应报错")
	}
}

func TestDefaultWatcherSourcesOrder(t *testing.T) {
	w := NewDefaultWatcher()
	list := w.bars.Sources()
	names := make([]string, 0, len(list))
	for _, s := range list {
		names = append(names, s.Name())
	}
	want := []string{"eastmoney", "sina", "sina-alt"}
	if len(names) != len(want) {
		t.Fatalf("源数量不对：%v", names)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("源顺序不对：%v want %v", names, want)
		}
	}
	if len(w.bars.Status()) != len(want) {
		t.Fatalf("健康度未初始化：%d", len(w.bars.Status()))
	}
	alt, ok := list[2].(*SinaSource)
	if !ok || !strings.Contains(alt.Client.MinuteURL, "stock.finance.sina.com.cn") {
		t.Fatalf("备用域名没生效：%+v", list[2])
	}
	if w.bars.Name() != "eastmoney+sina+sina-alt" {
		t.Fatalf("链路名不对：%s", w.bars.Name())
	}
}

func TestEastmoneyDailyRangePaging(t *testing.T) {
	var gotEnd string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotEnd = r.URL.Query().Get("end")
		_, _ = w.Write([]byte(`{"rc":0,"data":{"klines":["2025-12-31,3123,3096,3132,3089,884858,1"]}}`))
	}))
	t.Cleanup(srv.Close)
	src := NewEastmoneySource(srv.URL)
	v := mustVariety(t, "RB")

	// 指定 end → 格式化成 YYYYMMDD（挖历史靠它往前翻页）
	if _, err := src.DailyRange(context.Background(), v, time.Date(2026, 1, 5, 0, 0, 0, 0, locCST), 0); err != nil {
		t.Fatal(err)
	}
	if gotEnd != "20260105" {
		t.Fatalf("end=%s", gotEnd)
	}
	// 不指定 → 取最新
	if _, err := src.Daily(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	if gotEnd != "20500101" {
		t.Fatalf("默认 end=%s", gotEnd)
	}
}

func TestEastmoneyHTTPStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)
	src := NewEastmoneySource(srv.URL)
	if _, err := src.Minute(context.Background(), mustVariety(t, "RB"), "5"); err == nil {
		t.Fatal("非 200 应报错")
	}
}
