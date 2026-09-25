package futures

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// 行业 / 企业新闻源。
//
// 快讯源（东财 7x24、新浪 7x24）给的是「期货盘面 + 宏观」，
// 但看焦煤这类品种，真正有用的还有产业侧的消息：煤矿停产/复产、焦企提涨提降、
// 港口库存、上市公司公告……这些在综合快讯里几乎看不到，得去商品垂直站和关键词检索捞。

// ---------------------------------------------------------------- 生意社（100ppi）

const defaultPPPIListURL = "https://www.100ppi.com/news/list--%s-1.html"

// 只要「国内」和「企业」两个栏目：全量列表里混着「均差」「招聘」，
// 那两类一个是算法排名、一个是招聘广告，都不是新闻。
var pppiCategories = []string{"12", "1211"}

// 产业子站。综合栏目里一个品种一天可能一条都没有，但产业子站是按品种持续更新的，
// 煤焦钢这类现货消息主要靠它们。实测只有「焦炭」「钢材」两个子站有内容。
var pppiIndustryPages = []string{
	"https://coke.100ppi.com/",  // 焦炭/冶金焦/兰炭，兼有焦煤
	"https://steel.100ppi.com/", // 钢材
}

// PPPISource 生意社行业资讯：综合栏目 + 产业子站。
type PPPISource struct {
	HTTP *http.Client
	URL  string // 栏目列表模板，含一个 %s：栏目号
	// Pages 产业子站首页，为空则用默认那两个
	Pages []string
}

func NewPPPISource() *PPPISource {
	return &PPPISource{URL: defaultPPPIListURL, Pages: pppiIndustryPages}
}

func (s *PPPISource) Name() string { return "100ppi" }

// FuturesScoped 商品行业站，但栏目里也有期货没覆盖的品种（甲酸、废钢…），仍要按品种筛一遍。
func (s *PPPISource) FuturesScoped() bool { return false }

func (s *PPPISource) Fetch(ctx context.Context, _ int) ([]NewsItem, error) {
	// 同一个 id 可能同时出现在栏目列表（带时间）和子站首页（只有日期），
	// 按 id 收一次，优先留带时间的那份。
	byID := map[string]NewsItem{}
	order := make([]string, 0, 160)
	add := func(items []NewsItem) {
		for _, it := range items {
			old, ok := byID[it.ID]
			if !ok {
				byID[it.ID] = it
				order = append(order, it.ID)
				continue
			}
			if len(it.Published) > len(old.Published) {
				byID[it.ID] = it
			}
		}
	}

	var lastErr error
	for _, cid := range pppiCategories {
		body, err := newsGet(ctx, s.HTTP, fmt.Sprintf(s.url(), cid), "https://www.100ppi.com/")
		if err != nil {
			lastErr = err
			continue // 一个栏目挂了不影响别的
		}
		add(parsePPPI(body))
	}
	for _, page := range s.pages() {
		body, err := newsGet(ctx, s.HTTP, page, "https://www.100ppi.com/")
		if err != nil {
			lastErr = err
			continue
		}
		add(parsePPPIIndustry(body))
	}

	out := make([]NewsItem, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	if len(out) == 0 {
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, fmt.Errorf("没有新闻")
	}
	return out, nil
}

func (s *PPPISource) url() string {
	if s.URL != "" {
		return s.URL
	}
	return defaultPPPIListURL
}

func (s *PPPISource) pages() []string {
	if s.Pages != nil {
		return s.Pages
	}
	return pppiIndustryPages
}

// 列表项形如：
//
//	<li>[<a href="…list-14-1218-1.html" class="blueq">均差</a>]
//	    <a href="…/news/detail-20260925-6272965.html" target="_blank" class="blueq">标题</a>
//	    <span>2026-09-25 18:11</span></li>
//
// 标题里可能夹着 <em> 之类的标签，所以标题用 (.+?) 而不是 [^<]*，之后统一 stripHTML。
var pppiItemRe = regexp.MustCompile(
	`<li>\s*\[<a href="[^"]*"[^>]*>([^<]*)</a>\]\s*` +
		`<a href="([^"]*?/news/detail-(\d+)-(\d+)\.html)"[^>]*>(.+?)</a>\s*` +
		`<span>\s*([^<]+?)\s*</span>\s*</li>`)

func parsePPPI(body []byte) []NewsItem {
	matches := pppiItemRe.FindAllSubmatch(body, -1)
	out := make([]NewsItem, 0, len(matches))
	for _, m := range matches {
		category := strings.TrimSpace(string(m[1]))
		link := strings.TrimSpace(string(m[2]))
		title := strings.TrimSpace(stripHTML(string(m[5])))
		published := strings.TrimSpace(string(m[6]))
		if title == "" || link == "" {
			continue
		}
		// 「均差/均线」是生意社的算法排名贴（每个品种一天一条），不是新闻，
		// 一个栏目里能占一半，全放进来会把正经消息挤掉。
		if strings.Contains(title, "均差") || strings.Contains(title, "均线") {
			continue
		}
		out = append(out, NewsItem{
			// 详情链接里就带着日期和序号，直接拿来做去重 id
			ID:       string(m[3]) + "-" + string(m[4]),
			Title:    clipRunes(title, 120),
			URL:      link,
			Media:    firstNonEmpty(category, "生意社"),
			Provider: "100ppi",
			// 生意社的列表只给日期到分钟，够排序用了
			Published: published,
			TS:        parseNewsTime(published),
		})
	}
	return out
}

// 产业子站首页的条目只有「详情链接 + 标题」，没有到分钟的时间，形如：
//
//	<li class="black_li …">[<a href="/news/">商品动态</a>]
//	    <a href="…/news/detail-20260924-6265749.html" target="_blank">标题</a></li>
var pppiIndustryRe = regexp.MustCompile(
	`<a href="(https?://[^"]*?/news/detail-(\d{4})(\d{2})(\d{2})-(\d+)\.html)"[^>]*>([^<]+)</a>`)

// parsePPPIIndustry 解析产业子站首页。
// 这类页面只给到「日期」——时间就取当天 00:00，展示时也只显示日期，
// 不编一个页面上没有的分钟数出来。
func parsePPPIIndustry(body []byte) []NewsItem {
	matches := pppiIndustryRe.FindAllSubmatch(body, -1)
	out := make([]NewsItem, 0, len(matches))
	seen := map[string]bool{}
	for _, m := range matches {
		link := string(m[1])
		day := string(m[2]) + "-" + string(m[3]) + "-" + string(m[4])
		id := string(m[2]) + string(m[3]) + string(m[4]) + "-" + string(m[5])
		title := strings.TrimSpace(stripHTML(string(m[6])))
		if title == "" || seen[id] {
			continue
		}
		if strings.Contains(title, "均差") || strings.Contains(title, "均线") {
			continue
		}
		seen[id] = true
		out = append(out, NewsItem{
			ID: id, Title: clipRunes(title, 120), URL: link,
			Media: "生意社", Provider: "100ppi",
			Published: day,
			TS:        parseNewsTime(day),
		})
	}
	return out
}

// ---------------------------------------------------------------- 东财关键词检索

const defaultEMSearchURL = "https://search-api-web.eastmoney.com/search/jsonp"

// EastmoneySearchSource 按关键词捞行业 / 企业新闻。
//
// 关键词由配置给（默认就是煤焦钢矿那几个），所以抓回来不再二次过滤——
// 你填「焦煤」要的就是焦煤，填「煤矿」要的就是煤矿，别再拿通用规则把它筛掉。
type EastmoneySearchSource struct {
	HTTP     *http.Client
	URL      string
	Keywords []string
	PerPage  int
}

func NewEastmoneySearchSource(keywords []string) *EastmoneySearchSource {
	return &EastmoneySearchSource{URL: defaultEMSearchURL, Keywords: keywords, PerPage: 20}
}

func (e *EastmoneySearchSource) Name() string { return "emsearch" }

func (e *EastmoneySearchSource) FuturesScoped() bool { return true }

func (e *EastmoneySearchSource) Fetch(ctx context.Context, limit int) ([]NewsItem, error) {
	if len(e.Keywords) == 0 {
		return nil, fmt.Errorf("没配关键词")
	}
	per := e.PerPage
	if limit > 0 && limit < per {
		per = limit
	}
	if per <= 0 {
		per = 30
	}
	out := make([]NewsItem, 0, per*len(e.Keywords))
	var lastErr error
	for _, kw := range e.Keywords {
		items, err := e.search(ctx, kw, per)
		if err != nil {
			lastErr = err
			continue // 某个词挂了不影响其他的
		}
		out = append(out, items...)
	}
	if len(out) == 0 {
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, fmt.Errorf("没有检索结果")
	}
	return out, nil
}

func (e *EastmoneySearchSource) search(ctx context.Context, keyword string, per int) ([]NewsItem, error) {
	payload := map[string]any{
		"uid": "", "keyword": keyword,
		"type":   []string{"cmsArticleWebOld"},
		"client": "web", "clientType": "web", "clientVersion": "curr",
		"param": map[string]any{
			"cmsArticleWebOld": map[string]any{
				"searchScope": "default", "sort": "time",
				"pageIndex": 1, "pageSize": per,
				"preTag": "", "postTag": "",
			},
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	u := e.url() + "?cb=cb&param=" + url.QueryEscape(string(raw))
	body, err := newsGet(ctx, e.HTTP, u, "https://so.eastmoney.com/")
	if err != nil {
		return nil, err
	}
	items, err := parseEMSearch(body)
	if err != nil {
		return nil, err
	}
	// 东财检索命中少的时候会兜底给一堆不相关的结果（搜「焦炭」能返回疫苗新闻），
	// 所以按关键词回筛一遍：标题或正文里真的出现这个词才算。
	kept := make([]NewsItem, 0, len(items))
	for _, it := range items {
		if strings.Contains(it.Title, keyword) || strings.Contains(it.Summary, keyword) {
			kept = append(kept, it)
		}
	}
	return kept, nil
}

func (e *EastmoneySearchSource) url() string {
	if e.URL != "" {
		return e.URL
	}
	return defaultEMSearchURL
}

func parseEMSearch(body []byte) ([]NewsItem, error) {
	raw, err := parseJSONP(body) // cb({...})
	if err != nil {
		return nil, err
	}
	var resp struct {
		Code   int `json:"code"`
		Result struct {
			Articles []struct {
				Date      string     `json:"date"`
				Code      flexString `json:"code"`
				Title     string     `json:"title"`
				Content   string     `json:"content"`
				MediaName string     `json:"mediaName"`
				URL       string     `json:"url"`
			} `json:"cmsArticleWebOld"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("解析失败：%w", err)
	}
	if resp.Code != 0 {
		return nil, fmt.Errorf("上游返回 code=%d", resp.Code)
	}
	out := make([]NewsItem, 0, len(resp.Result.Articles))
	for _, r := range resp.Result.Articles {
		title := strings.TrimSpace(stripHTML(r.Title))
		if title == "" {
			continue
		}
		id := r.Code.String()
		if id == "" {
			id = firstNonEmpty(r.URL, fmt.Sprintf("%s|%s", r.Date, normTitle(title)))
		}
		out = append(out, NewsItem{
			ID: id, Title: title,
			Summary:   clipRunes(stripHTML(strings.TrimSpace(r.Content)), 300),
			URL:       strings.TrimSpace(r.URL),
			Media:     firstNonEmpty(r.MediaName, "东方财富"),
			Provider:  "emsearch",
			Published: r.Date,
			TS:        parseNewsTime(r.Date),
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("没有检索结果")
	}
	return out, nil
}
