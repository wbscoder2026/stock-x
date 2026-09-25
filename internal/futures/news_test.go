package futures

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------------------------------------------------------------- 解析

func TestParseSinaFlash(t *testing.T) {
	// 真实返回里 id 是数字、正文是 rich_text
	body := []byte(`{"result":{"status":{"code":0},"data":{"feed":{"list":[
		{"id":1001,"create_time":"2026-09-26 10:30:00","rich_text":"焦煤主力合约涨超2%。大商所数据显示持仓量上升。","docurl":"https://finance.sina.com.cn/a/1001","media_name":"新浪财经"},
		{"id":1002,"create_time":"2026-09-26 09:15:00","rich_text":"<p>螺纹钢期货震荡走强</p>","docurl":""}
	]}}}}`)
	got, err := parseSinaFlash(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("应解析出 2 条：%d", len(got))
	}
	first := got[0]
	if first.ID != "1001" || first.Provider != "sina" || first.Media != "新浪财经" {
		t.Fatalf("数字 id 也要收下：%+v", first)
	}
	if first.Published != "2026-09-26 10:30:00" {
		t.Fatalf("时间不对：%s", first.Published)
	}
	if first.TS == 0 {
		t.Fatal("应解析出时间戳")
	}
	if first.Title == "" || first.Summary == "" {
		t.Fatalf("快讯应拆出标题和摘要：%+v", first)
	}
	// 第二条带 HTML 标签，要剥掉
	if got[1].Title != "螺纹钢期货震荡走强" {
		t.Fatalf("HTML 应被剥掉：%q", got[1].Title)
	}
	// 没有 docurl 时用 id 兜底
	if got[1].URL != "" {
		t.Fatalf("没给链接就别编：%q", got[1].URL)
	}
}

func TestParseSinaFlashUpstreamError(t *testing.T) {
	if _, err := parseSinaFlash([]byte(`{"result":{"status":{"code":1,"msg":"boom"}}}`)); err == nil {
		t.Fatal("上游报错应返回错误")
	}
	if _, err := parseSinaFlash([]byte(`not json`)); err == nil {
		t.Fatal("坏 JSON 应返回错误")
	}
}

// 东财快讯的真实返回：成功码是字符串 "1"，id 在 code 字段
func TestParseEMFlash(t *testing.T) {
	body := []byte(`{"req_trace":"1","code":"1","message":"success","data":{"fastNewsList":[
		{"code":"202609263884347171","title":"国际原油期货跌幅扩大 美油、布油均跌超3%",
		 "summary":"国际原油期货跌幅扩大，美油、布油均跌超3%。截至目前，WTI原油期货价格跌3.08%。",
		 "showTime":"2026-09-26 00:12:27","realSort":"1790352747047171"}
	]}}`)
	got, err := parseEMFlash(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("应解析出 1 条：%d", len(got))
	}
	it := got[0]
	if it.ID != "202609263884347171" || it.Provider != "eastmoney" {
		t.Fatalf("字段不对：%+v", it)
	}
	if it.Title != "国际原油期货跌幅扩大 美油、布油均跌超3%" {
		t.Fatalf("标题不对：%q", it.Title)
	}
	if it.Published != "2026-09-26 00:12:27" || it.TS == 0 {
		t.Fatalf("时间不对：%s %d", it.Published, it.TS)
	}
	if it.Summary == "" {
		t.Fatal("快讯正文应作为摘要")
	}
	if _, err := parseEMFlash([]byte(`{"code":"9"}`)); err == nil {
		t.Fatal("非成功码应报错")
	}
}

// 东财资讯栏目
func TestParseEMNews(t *testing.T) {
	body := []byte(`{"code":"1","message":"success","data":{"list":[
		{"code":"n1","showTime":"2026-09-26 11:00:00","title":"铁矿石期货午后拉升","mediaName":"期货日报","summary":"受补库预期影响…","url":"https://futures.eastmoney.com/a/n1"},
		{"code":"n2","showTime":"2026-09-26 10:00:00","title":"沪铜震荡","mediaName":"","summary":"","url":""}
	]}}`)
	got, err := parseEMNews(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("应解析出 2 条：%d", len(got))
	}
	if got[0].ID != "n1" || got[0].Provider != "emnews" || got[0].Media != "期货日报" {
		t.Fatalf("字段不对：%+v", got[0])
	}
	if got[0].URL != "https://futures.eastmoney.com/a/n1" {
		t.Fatalf("链接不对：%q", got[0].URL)
	}
	if got[1].Media != "东方财富" {
		t.Fatalf("没给媒体名应兜底：%q", got[1].Media)
	}
	if _, err := parseEMNews([]byte(`{"code":"9"}`)); err == nil {
		t.Fatal("非成功码应报错")
	}
}

// 源真的挂了要报错，但不能让整块新闻空白（由 NewsHub 兜住）
func TestNewsSourceHTTPFailureReportsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	src := &SinaFlashSource{URL: srv.URL}
	if _, err := src.Fetch(context.Background(), 10); err == nil {
		t.Fatal("HTTP 502 应报错")
	}
}

func TestNewsSourceEndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("zhibo_id") != "152" {
			t.Errorf("zhibo_id=%s", r.URL.Query().Get("zhibo_id"))
		}
		_, _ = w.Write([]byte(`{"result":{"status":{"code":0},"data":{"feed":{"list":[
			{"id":"7","create_time":"2026-09-26 10:00:00","rich_text":"纯碱期货涨停","docurl":"u1"}
		]}}}}`))
	}))
	t.Cleanup(srv.Close)
	src := &SinaFlashSource{URL: srv.URL, ZhiboID: "152"}
	got, err := src.Fetch(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Title != "纯碱期货涨停" {
		t.Fatalf("%+v", got)
	}
	if src.FuturesScoped() {
		t.Fatal("新浪 7x24 是综合快讯，需要按关键词过滤")
	}
}

// ---------------------------------------------------------------- 合并去重排序

func item(id, provider, title, published string, ts int64) NewsItem {
	return NewsItem{ID: id, Provider: provider, Title: title, Published: published, TS: ts}
}

func TestMergeNewsSortsNewestFirst(t *testing.T) {
	got := mergeNews([]NewsItem{
		item("a", "sina", "旧新闻", "2026-09-20 09:00:00", 100),
		item("b", "eastmoney", "新新闻", "2026-09-26 09:00:00", 300),
		item("c", "sina", "中间", "2026-09-23 09:00:00", 200),
	}, 0)
	if len(got) != 3 || got[0].ID != "b" || got[1].ID != "c" || got[2].ID != "a" {
		t.Fatalf("应从新到旧：%+v", got)
	}
}

func TestMergeNewsDropsDuplicatesAcrossSources(t *testing.T) {
	// 同一条被两个源转发：标题和时间一样 → 只留一条
	got := mergeNews([]NewsItem{
		{ID: "1", Provider: "sina", Title: "焦煤期货大涨", Published: "2026-09-26 10:00:00", TS: 300},
		{ID: "2", Provider: "eastmoney", Title: "焦煤期货大涨", Published: "2026-09-26 10:00:00", TS: 300,
			Summary: "现货跟涨，基差走强", URL: "https://x"},
	}, 0)
	if len(got) != 1 {
		t.Fatalf("重复的新闻应合并：%+v", got)
	}
	// 留下信息更全的那条
	if got[0].URL != "https://x" {
		t.Fatalf("应留信息更全的那条：%+v", got[0])
	}
}

func TestMergeNewsKeepsSameTitleDifferentTime(t *testing.T) {
	got := mergeNews([]NewsItem{
		{ID: "1", Provider: "sina", Title: "早评：商品多数上涨", Published: "2026-09-25 08:00:00", TS: 100},
		{ID: "2", Provider: "sina", Title: "早评：商品多数上涨", Published: "2026-09-26 08:00:00", TS: 200},
	}, 0)
	if len(got) != 2 {
		t.Fatalf("不同时间不算重复：%+v", got)
	}
}

func TestMergeNewsRespectsLimitAndDropsEmptyTitle(t *testing.T) {
	got := mergeNews([]NewsItem{
		{ID: "1", Provider: "sina", Title: "   ", Published: "x", TS: 1},
		{ID: "2", Provider: "sina", Title: "一", Published: "x", TS: 3},
		{ID: "3", Provider: "sina", Title: "二", Published: "x", TS: 2},
	}, 1)
	if len(got) != 1 || got[0].ID != "2" {
		t.Fatalf("应截到 limit 且丢掉空标题：%+v", got)
	}
}

// ---------------------------------------------------------------- 期货相关性

func TestIsFuturesRelated(t *testing.T) {
	yes := []string{
		"焦煤主力合约涨停",
		"大商所调整保证金",
		"RB2610 成交放量",
		"商品期货多数上涨",
		"持仓量创年内新高",
		"洲际交易所 10 月柴油期货结算价为每吨 1460.75 美元",
	}
	for _, s := range yes {
		if !IsFuturesRelated(s, "") {
			t.Fatalf("应判为相关：%s", s)
		}
	}
	no := []string{
		"某公司发布三季度财报，营收同比增长",
		"央行开展逆回购操作",
		"",
	}
	for _, s := range no {
		if IsFuturesRelated(s, "") {
			t.Fatalf("不该判为相关：%s", s)
		}
	}
}

// 硬信号只看标题：正文里顺带一句「纳指期货」不该把美股新闻算成期货新闻
func TestIsFuturesRelatedHardSignalTitleOnly(t *testing.T) {
	if IsFuturesRelated("美股盘前普涨", "纳指期货涨0.45%，半导体股走强") {
		t.Fatal("正文里顺带一句期货字样不该算硬信号")
	}
	if IsFuturesRelated("美股三大指数小幅高开", "") {
		t.Fatal("标题既没有期货字样也没有品种名，不该判为相关")
	}
}

// 品种名是常用词，光提到不算，得有商品/行情语境
func TestIsFuturesRelatedNeedsCommodityContext(t *testing.T) {
	// 这些句子里有品种名（黄金/苹果/燃油/玉米），但跟期货没关系
	noise := []string{
		"“请3休13”超长版黄金周首日：多地景区启动限流",
		"老铺黄金国内最大门店在沪亮相",
		"汇顶科技回应是否向苹果折叠屏手机供货",
		"德国议员批准了一项新的燃油税减免计划",
	}
	for _, s := range noise {
		if IsFuturesRelated(s, "") {
			t.Fatalf("不该判为相关：%s", s)
		}
	}
	// 同一个品种名，带上行情语境就该认
	good := []string{
		"现货黄金向上触及4300美元",
		"国际原油期货跌幅扩大，美油、布油均跌超3%",
		"WTI原油日内跌3%，现报91.76美元/桶",
		"秘鲁矿业部长预计今年铜产量为250万至270万吨",
	}
	for _, s := range good {
		if !IsFuturesRelated(s, "") {
			t.Fatalf("应判为相关：%s", s)
		}
	}
}

// 英文句子里的字母不能被当成品种代码
func TestIsFuturesRelatedIgnoresLettersInsideEnglishWords(t *testing.T) {
	noise := []string{
		"ANTHROPIC 与 OPENAI 发布新模型",
		"美国政府问责局（GAO）发布报告",
		"RMX 与 Technologent 签署算力供货订单",
	}
	for _, s := range noise {
		if IsFuturesRelated(s, "") {
			t.Fatalf("不该判为相关：%s", s)
		}
	}
	if !IsFuturesRelated("RB2610 成交放量", "") {
		t.Fatal("合约代码应判为相关")
	}
}

func TestMatchVarietiesTagsChineseNames(t *testing.T) {
	got := MatchVarieties("焦煤、焦炭与铁矿石夜盘集体走强，螺纹钢跟涨")
	want := map[string]bool{"焦煤": true, "焦炭": true, "铁矿石": true, "螺纹钢": true}
	if len(got) != len(want) {
		t.Fatalf("应命中 4 个品种：%v", got)
	}
	for _, g := range got {
		if !want[g] {
			t.Fatalf("多命中了 %s：%v", g, got)
		}
	}
}

// 单字母代码（A/B/C/I…）在英文句子里太容易误命中，只认两位及以上的代码
func TestMatchVarietiesSkipsSingleLetterPrefix(t *testing.T) {
	if got := MatchVarieties("THE INDEX IS UP"); len(got) != 0 {
		t.Fatalf("单字母不该命中：%v", got)
	}
	if got := MatchVarieties("JM 与 RB 走强"); len(got) == 0 {
		t.Fatalf("两位代码应命中：%v", got)
	}
}

func TestKeepFuturesFiltersGeneralFlash(t *testing.T) {
	got := keepFutures([]NewsItem{
		{ID: "1", Title: "期货市场多数上涨"},
		{ID: "2", Title: "某上市公司披露年报"},
		{ID: "3", Title: "焦煤供应偏紧"},
	})
	if len(got) != 2 || got[0].ID != "1" || got[1].ID != "3" {
		t.Fatalf("应只留期货相关的：%+v", got)
	}
}

// ---------------------------------------------------------------- NewsHub

type fakeNewsSource struct {
	name     string
	scoped   bool
	items    []NewsItem
	err      error
	gotLimit int
}

func (f *fakeNewsSource) Name() string        { return f.name }
func (f *fakeNewsSource) FuturesScoped() bool { return f.scoped }
func (f *fakeNewsSource) Fetch(_ context.Context, limit int) ([]NewsItem, error) {
	f.gotLimit = limit
	if f.err != nil {
		return nil, f.err
	}
	return f.items, nil
}

func TestNewsHubMergesSourcesAndReportsStatus(t *testing.T) {
	ok := &fakeNewsSource{name: "eastmoney", scoped: true, items: []NewsItem{
		{ID: "e1", Provider: "eastmoney", Title: "焦煤期货涨停", Published: "2026-09-26 10:00:00", TS: 200},
	}}
	bad := &fakeNewsSource{name: "sina", err: context.DeadlineExceeded}
	hub := NewNewsHub(ok, bad)

	items, status, err := hub.Refresh(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("坏源不该影响好源：%+v", items)
	}
	if len(status) != 2 {
		t.Fatalf("每个源都要报状态：%+v", status)
	}
	byName := map[string]NewsSourceStatus{}
	for _, s := range status {
		byName[s.Name] = s
	}
	if !byName["eastmoney"].OK || byName["eastmoney"].Count != 1 {
		t.Fatalf("好源状态不对：%+v", byName["eastmoney"])
	}
	if byName["sina"].OK || byName["sina"].Error == "" {
		t.Fatalf("坏源应带上错误：%+v", byName["sina"])
	}
	// 坏源挂了，页面还是能显示已抓到的
	cached, _, at := hub.Latest()
	if len(cached) != 1 || at.IsZero() {
		t.Fatalf("应缓存住结果：%d %v", len(cached), at)
	}
}

// 综合快讯源要按关键词过滤，期货频道源不用
func TestNewsHubFiltersOnlyUnscopedSources(t *testing.T) {
	general := &fakeNewsSource{name: "sina", items: []NewsItem{
		{ID: "1", Provider: "sina", Title: "期货市场大涨", Published: "p", TS: 2},
		{ID: "2", Provider: "sina", Title: "某公司披露年报", Published: "p", TS: 1},
	}}
	scoped := &fakeNewsSource{name: "em", scoped: true, items: []NewsItem{
		{ID: "3", Provider: "em", Title: "库存周报", Published: "p", TS: 3},
	}}
	hub := NewNewsHub(general, scoped)
	items, _, err := hub.Refresh(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("综合源过滤掉 1 条、频道源全留，应共 2 条：%+v", items)
	}
	// 打上品种标签
	if len(items[0].Tags) == 0 && items[0].ID == "1" {
		t.Fatal("期货相关的应打上品种标签")
	}
}

func TestNewsHubWithoutSources(t *testing.T) {
	if _, _, err := NewNewsHub().Refresh(context.Background(), 10); err == nil {
		t.Fatal("没有源应报错")
	}
}

// ---------------------------------------------------------------- 行业/企业源

// 生意社列表项：分类 + 标题链接 + 时间，按真实结构构造
const pppiFixture = `<div class="list-c"><ul style="clear:both">
<li>[<a href="https://www.100ppi.com/news/list-12-12-1.html" class="blueq">国内</a>]        <a href="https://www.100ppi.com/news/detail-20260925-6272965.html" target="_blank" class="blueq">生意社：焦煤供给持续收紧 焦炭市场价格偏强运行</a>
        <span>2026-09-25 18:11</span></li>
<li>[<a href="https://www.100ppi.com/news/list-1211-1211-1.html" class="blueq">企业</a>]        <a href="https://www.100ppi.com/news/detail-20260924-6271000.html" target="_blank" class="blueq">山西焦煤：子公司煤矿复产 <em>公告</em></a>
        <span>2026-09-24 09:05</span></li>
</ul></div>`

func TestParsePPPI(t *testing.T) {
	got := parsePPPI([]byte(pppiFixture))
	if len(got) != 2 {
		t.Fatalf("应解析出 2 条：%d", len(got))
	}
	first := got[0]
	if first.ID != "20260925-6272965" {
		t.Fatalf("id 应从详情链接里取：%q", first.ID)
	}
	if first.Provider != "100ppi" || first.Media != "国内" {
		t.Fatalf("来源/分类不对：%+v", first)
	}
	if first.Title != "生意社：焦煤供给持续收紧 焦炭市场价格偏强运行" {
		t.Fatalf("标题不对：%q", first.Title)
	}
	if first.URL != "https://www.100ppi.com/news/detail-20260925-6272965.html" {
		t.Fatalf("链接不对：%q", first.URL)
	}
	if first.Published != "2026-09-25 18:11" || first.TS == 0 {
		t.Fatalf("时间不对：%s %d", first.Published, first.TS)
	}
	// 标题里的 <em> 要剥掉
	if got[1].Title != "山西焦煤：子公司煤矿复产 公告" {
		t.Fatalf("HTML 没剥干净：%q", got[1].Title)
	}
}

func TestParsePPPISkipsBadRows(t *testing.T) {
	// 没有时间的行不该被当成新闻收下
	body := []byte(`<li>[<a href="/a" class="blueq">国内</a>] <a href="https://www.100ppi.com/news/detail-20260925-1.html">无时间</a></li>`)
	if got := parsePPPI(body); len(got) != 0 {
		t.Fatalf("结构不完整的不该收：%+v", got)
	}
}

// 「均差/均线」是生意社的算法排名贴（一个品种一天一条），不是新闻，要丢掉
func TestParsePPPIskipsAlgorithmPosts(t *testing.T) {
	body := []byte(`<ul>
<li>[<a href="/a" class="blueq">均差</a>] <a href="https://www.100ppi.com/news/detail-20260925-1.html">生意社焦煤9月25日均差为12.00元/吨 由正向缩小重新扩大</a><span>2026-09-25 18:11</span></li>
<li>[<a href="/a" class="blueq">国内</a>] <a href="https://www.100ppi.com/news/detail-20260925-2.html">生意社焦煤9月25日均线下穿</a><span>2026-09-25 18:11</span></li>
<li>[<a href="/a" class="blueq">企业</a>] <a href="https://www.100ppi.com/news/detail-20260925-3.html">生意社：焦煤供给持续收紧 焦炭市场价格偏强运行</a><span>2026-09-25 18:11</span></li>
</ul>`)
	got := parsePPPI(body)
	if len(got) != 1 {
		t.Fatalf("只该留下正经那条：%+v", got)
	}
	if strings.Contains(got[0].Title, "均") {
		t.Fatalf("算法贴没滤掉：%q", got[0].Title)
	}
}

func TestPPPISourceEndToEnd(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		_, _ = w.Write([]byte(pppiFixture))
	}))
	t.Cleanup(srv.Close)
	// URL 模板里带 %s（栏目号）；Pages 置空，避免测试打到真实站点
	src := &PPPISource{URL: srv.URL + "/news/list--%s-1.html", Pages: []string{}}
	items, err := src.Fetch(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	// 两个栏目都返回同一份 fixture（同两个 id），按 id 去重后只剩 2 条
	if len(items) != 2 {
		t.Fatalf("同一篇出现在多个栏目应只算一条：%d", len(items))
	}
	if len(paths) != 2 || paths[0] != "/news/list--12-1.html" || paths[1] != "/news/list--1211-1.html" {
		t.Fatalf("应抓「国内」和「企业」两个栏目：%v", paths)
	}
}

// 一个栏目挂了不该让整个源失败
func TestPPPISourceToleratesOneBadCategory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "--1211-") {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(pppiFixture))
	}))
	t.Cleanup(srv.Close)
	src := &PPPISource{URL: srv.URL + "/news/list--%s-1.html", Pages: []string{}}
	items, err := src.Fetch(context.Background(), 100)
	if err != nil || len(items) == 0 {
		t.Fatalf("一个栏目挂了仍应返回另一个栏目的新闻：%d %v", len(items), err)
	}
}

// 产业子站：只有日期、没有分钟，时间戳按当天 00:00 算，展示也只显示日期
func TestParsePPPIIndustry(t *testing.T) {
	body := []byte(`<li class="black_li height_24p">[<a href="/news/">商品动态</a>]
<a href="https://www.100ppi.com/news/detail-20260924-6265749.html" target="_blank">生意社：9月24日宜春市场冶金焦价格暂稳运行</a></li>
<li class="black_li"><a href="https://www.100ppi.com/news/detail-20260924-6265749.html" target="_blank">同一条重复出现</a></li>
<li class="black_li"><a href="https://www.100ppi.com/news/detail-20260925-6271428.html" target="_blank">生意社焦炭9月25日均差为12.00元/吨</a></li>`)
	got := parsePPPIIndustry(body)
	if len(got) != 1 {
		t.Fatalf("应只留 1 条（去重 + 去掉算法贴）：%+v", got)
	}
	it := got[0]
	if it.ID != "20260924-6265749" || it.Provider != "100ppi" || it.Media != "生意社" {
		t.Fatalf("字段不对：%+v", it)
	}
	if it.Published != "2026-09-24" {
		t.Fatalf("只该给到日期：%q", it.Published)
	}
	if it.TS != parseNewsTime("2026-09-24") {
		t.Fatalf("时间戳应按当天算：%d", it.TS)
	}
}

// 同一篇文章同时出现在栏目列表（带时间）和子站首页（只有日期）时，只留带时间的那份
func TestPPPISourcePrefersTimedDuplicate(t *testing.T) {
	industry := `<a href="https://www.100ppi.com/news/detail-20260925-6272965.html" target="_blank">生意社：焦煤供给持续收紧</a>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/industry") {
			_, _ = w.Write([]byte(industry))
			return
		}
		_, _ = w.Write([]byte(pppiFixture))
	}))
	t.Cleanup(srv.Close)
	src := &PPPISource{
		URL:   srv.URL + "/news/list--%s-1.html",
		Pages: []string{srv.URL + "/industry"},
	}
	items, err := src.Fetch(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]NewsItem{}
	for _, it := range items {
		seen[it.ID] = it
	}
	if len(items) != len(seen) {
		t.Fatalf("同 id 不该出现两次：%+v", items)
	}
	// 子站那条只有日期，不该把栏目列表里的同一条（带时间）挤掉
	if got, ok := seen["20260925-6272965"]; !ok || got.Published != "2026-09-25 18:11" {
		t.Fatalf("重复时应留带时间的那份：%+v", got)
	}
}

// 东财关键词检索：JSONP + cmsArticleWebOld
const emSearchFixture = `cb({"code":0,"msg":"OK","hitsTotal":2542,"result":{"cmsArticleWebOld":[
{"date":"2026-09-24 19:50:05","code":"202609243883852788","title":"焦煤：淮北矿业拟转让煤炭产能置换指标","content":"淮北矿业公告，拟将153.528万吨/年煤炭产能置换指标以公开挂牌方式转让。","mediaName":"东方财富研究中心","url":"http://futures.eastmoney.com/a/202609243883852788.html"},
{"date":"2026-09-24 17:00:00","code":"202609243883755084","title":"5.47亿元主力资金今日抢筹煤炭板块","content":"主力资金流量（万元）","mediaName":"证券时报网","url":"http://finance.eastmoney.com/a/202609243883755084.html"}
]}})`

func TestParseEMSearch(t *testing.T) {
	got, err := parseEMSearch([]byte(emSearchFixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("应解析出 2 条：%d", len(got))
	}
	it := got[0]
	if it.ID != "202609243883852788" || it.Provider != "emsearch" {
		t.Fatalf("字段不对：%+v", it)
	}
	if it.Media != "东方财富研究中心" {
		t.Fatalf("媒体不对：%q", it.Media)
	}
	if it.Published != "2026-09-24 19:50:05" || it.TS == 0 {
		t.Fatalf("时间不对：%s %d", it.Published, it.TS)
	}
	if it.Summary == "" || !strings.Contains(it.Summary, "产能置换") {
		t.Fatalf("正文应作为摘要：%q", it.Summary)
	}
}

func TestEMSearchSourceUsesEveryKeyword(t *testing.T) {
	var params []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		params = append(params, r.URL.Query().Get("param"))
		_, _ = w.Write([]byte(emSearchFixture))
	}))
	t.Cleanup(srv.Close)
	src := &EastmoneySearchSource{URL: srv.URL, Keywords: []string{"焦煤", "煤矿"}, PerPage: 10}
	items, err := src.Fetch(context.Background(), 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(params) != 2 {
		t.Fatalf("每个关键词各请求一次：%d", len(params))
	}
	if !strings.Contains(params[0], "焦煤") || !strings.Contains(params[1], "煤矿") {
		t.Fatalf("关键词没发出去：%v", params)
	}
	// fixture 里只有一条标题带「焦煤」；「煤矿」一条都没有 → 回筛后只剩 1 条
	if len(items) != 1 || !strings.Contains(items[0].Title, "焦煤") {
		t.Fatalf("应按关键词回筛：%+v", items)
	}
}

func TestEMSearchSourceWithoutKeywords(t *testing.T) {
	src := NewEastmoneySearchSource(nil)
	if _, err := src.Fetch(context.Background(), 10); err == nil {
		t.Fatal("没配关键词应报错")
	}
}

// 东财检索命中少时会兜底返回不相关结果，要按关键词回筛
func TestEMSearchDropsIrrelevantFallback(t *testing.T) {
	const fixture = `cb({"code":0,"result":{"cmsArticleWebOld":[
{"date":"2026-09-25 09:00:00","code":"1","title":"焦煤主力合约收跌0.46%","content":"焦煤夜盘走弱","mediaName":"南方财经网","url":"u1"},
{"date":"2026-09-25 08:00:00","code":"2","title":"某医疗公司完成D轮融资","content":"疫苗研发","mediaName":"财联社","url":"u2"},
{"date":"2026-09-25 07:00:00","code":"3","title":"5.47亿元主力资金抢筹煤炭板块","content":"000983 山西焦煤 领涨","mediaName":"证券时报网","url":"u3"}
]}})`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(fixture))
	}))
	t.Cleanup(srv.Close)
	src := &EastmoneySearchSource{URL: srv.URL, Keywords: []string{"焦煤"}, PerPage: 10}
	items, err := src.Fetch(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	// 第 2 条标题和正文都没有「焦煤」→ 丢掉；第 3 条正文里有 → 留下
	if len(items) != 2 {
		t.Fatalf("不相关的兜底结果应被筛掉：%+v", items)
	}
	for _, it := range items {
		if it.ID == "2" {
			t.Fatalf("医疗新闻不该留下：%+v", it)
		}
	}
}

// 关键词全都查不到内容时应该报错，而不是返回空
func TestEMSearchAllKeywordsIrrelevant(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(emSearchFixture)) // 里面没有「焦炭」
	}))
	t.Cleanup(srv.Close)
	src := &EastmoneySearchSource{URL: srv.URL, Keywords: []string{"焦炭"}, PerPage: 10}
	if _, err := src.Fetch(context.Background(), 10); err == nil {
		t.Fatal("一条都不相关时应报错")
	}
}

// 产业侧消息：标题里只有品种名 + 产业词，没有涨跌，也要认得出来
func TestIsFuturesRelatedPicksIndustryNews(t *testing.T) {
	yes := []string{
		"山西焦煤：子公司煤矿复产",
		"吕梁一座焦煤煤矿因任务完成停产",
		"焦炭第四轮提涨落地，钢厂接受度提高",
		"北方港口焦煤库存回升",
	}
	for _, s := range yes {
		if !IsFuturesRelated(s, "") {
			t.Fatalf("产业消息应判为相关：%s", s)
		}
	}
}

func TestNewsSourcesByName(t *testing.T) {
	got := NewsSourcesByName([]string{"eastmoney", "sina", "unknown"}, "102", nil)
	if len(got) != 2 {
		t.Fatalf("认不出的名字应跳过：%d", len(got))
	}
	if len(NewsSourcesByName(nil, "", nil)) != 0 {
		t.Fatal("空列表应返回空")
	}
	// 快讯栏目号要传下去
	if src := NewsSourcesByName([]string{"eastmoney"}, "999", nil)[0].(*EastmoneyFlashSource); src.Column != "999" {
		t.Fatalf("栏目号没传下去：%s", src.Column)
	}
	if src := NewsSourcesByName([]string{"emnews"}, "999", nil)[0].(*EastmoneyNewsSource); src.Column != "347" {
		t.Fatalf("长资讯用的是另一套栏目号：%s", src.Column)
	}
	// 新增的行业/检索源
	if src := NewsSourcesByName([]string{"100ppi"}, "", nil)[0]; src.Name() != "100ppi" || src.FuturesScoped() {
		t.Fatalf("生意社源不对：%s scoped=%v", src.Name(), src.FuturesScoped())
	}
	search := NewsSourcesByName([]string{"emsearch"}, "", []string{"焦煤", "煤矿"})[0].(*EastmoneySearchSource)
	if len(search.Keywords) != 2 || search.Keywords[0] != "焦煤" {
		t.Fatalf("关键词没传下去：%v", search.Keywords)
	}
	if !search.FuturesScoped() {
		t.Fatal("关键词检索的结果不该再按通用规则过滤")
	}
}

// flexString：同一个字段上游有时给数字有时给字符串
func TestFlexString(t *testing.T) {
	var resp struct {
		Num flexString `json:"num"`
		Str flexString `json:"str"`
		Nil flexString `json:"nil"`
	}
	if err := json.Unmarshal([]byte(`{"num":123,"str":"abc","nil":null}`), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Num != "123" || resp.Str != "abc" || resp.Nil != "" {
		t.Fatalf("%q %q %q", resp.Num, resp.Str, resp.Nil)
	}
}

func TestClipRunesKeepsChineseIntact(t *testing.T) {
	if got := clipRunes("一二三四五", 3); got != "一二三…" {
		t.Fatalf("按字符截断：%q", got)
	}
	if got := clipRunes("短", 10); got != "短" {
		t.Fatalf("不截断：%q", got)
	}
}

// ---------------------------------------------------------------- 时间解析

func TestParseNewsTime(t *testing.T) {
	cases := map[string]bool{
		"2026-09-26 10:30:00":  true,
		"2026-09-26 10:30":     true,
		"2026-09-26":           true,
		"2026/01/02 15:04:05":  true,
		"2026-09-26T10:30:00Z": true,
		"乱七八糟":                 false,
		"":                     false,
	}
	for raw, ok := range cases {
		got := parseNewsTime(raw)
		if ok && got == 0 {
			t.Fatalf("%q 应解析出时间戳", raw)
		}
		if !ok && got != 0 {
			t.Fatalf("%q 不该解析出时间戳：%d", raw, got)
		}
	}
}

func TestSplitTitleAndStripHTML(t *testing.T) {
	title, summary := splitTitle("焦煤期货涨停。现货跟涨，基差走强。")
	if title != "焦煤期货涨停" {
		t.Fatalf("标题= %q", title)
	}
	if summary == "" {
		t.Fatal("应拆出摘要")
	}
	if _, s := splitTitle("没有句号的一句话"); s != "" {
		t.Fatalf("单句不该硬拆出摘要：%q", s)
	}
	if got := stripHTML("<p>涨 &amp; 跌</p>"); got != "涨 & 跌" {
		t.Fatalf("HTML 没剥干净：%q", got)
	}
}
