package futures

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// 新闻源都是外部站点，接口哪天改了很正常。所以：
//   - 解析尽量宽容（id 有的给数字有的给字符串、成功码有的用 "1" 有的用 0…都认）
//   - 单个源挂了只记状态，不让整块新闻空白（由 NewsHub 兜住）
//   - 源可以只用一部分，见 config.FuturesNewsSources

var newsCST = time.FixedZone("CST", 8*3600)

// flexString 同一个字段上游有时给数字、有时给字符串（比如新闻 id），两个都收。
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" {
		*f = ""
		return nil
	}
	*f = flexString(strings.Trim(s, `"`))
	return nil
}

func (f flexString) String() string { return string(f) }

// newsGet 带 Referer/UA 的 GET（照抄行情源那套）。
func newsGet(ctx context.Context, client *http.Client, rawURL, referer string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Referer", referer)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	c := client
	if c == nil {
		c = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return body, nil
}

// emCodeOK 东财这几个列表接口用 "1" 表示成功；0（或别的）都是失败，
// 响应里还会带一句 message 说明原因（比如缺了必填参数）。
func emCodeOK(raw string) bool {
	return strings.TrimSpace(raw) == "1"
}

// ---------------------------------------------------------------- 东方财富 7x24 快讯

const defaultEMFlashURL = "https://np-listapi.eastmoney.com/comm/web/getFastNewsList"

// EastmoneyFlashSource 东财 7x24 快讯。快讯里商品/期货的占比不高，
// 所以它不是期货专属源 → 由关键词过滤挑出相关的。
type EastmoneyFlashSource struct {
	HTTP     *http.Client
	URL      string
	Column   string // fastColumn：102 = 国际财经（含原油/金属等商品快讯）
	PageSize int
}

func NewEastmoneyFlashSource(column string) *EastmoneyFlashSource {
	if column == "" {
		column = "102"
	}
	return &EastmoneyFlashSource{URL: defaultEMFlashURL, Column: column, PageSize: 100}
}

func (e *EastmoneyFlashSource) Name() string { return "eastmoney" }

func (e *EastmoneyFlashSource) FuturesScoped() bool { return false }

func (e *EastmoneyFlashSource) Fetch(ctx context.Context, limit int) ([]NewsItem, error) {
	if limit <= 0 {
		limit = e.PageSize
	}
	if limit <= 0 {
		limit = 100
	}
	q := url.Values{
		"client":     {"web"},
		"biz":        {"web_724"},
		"fastColumn": {e.column()},
		"pageSize":   {fmt.Sprintf("%d", limit)},
		"req_trace":  {"1"},
		// sortEnd 是必填参数：留空 = 取最新一页（翻页时填上一页返回的 sortEnd）
		"sortEnd": {""},
	}
	body, err := newsGet(ctx, e.HTTP, e.url()+"?"+q.Encode(), "https://kuaixun.eastmoney.com/")
	if err != nil {
		return nil, err
	}
	return parseEMFlash(body)
}

func (e *EastmoneyFlashSource) url() string {
	if e.URL != "" {
		return e.URL
	}
	return defaultEMFlashURL
}

func (e *EastmoneyFlashSource) column() string {
	if e.Column != "" {
		return e.Column
	}
	return "102"
}

func parseEMFlash(body []byte) ([]NewsItem, error) {
	var resp struct {
		Code flexString `json:"code"`
		Data *struct {
			FastNewsList []struct {
				Code     flexString `json:"code"`
				Title    string     `json:"title"`
				Summary  string     `json:"summary"`
				ShowTime string     `json:"showTime"`
				RealSort string     `json:"realSort"`
			} `json:"fastNewsList"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("解析失败：%w", err)
	}
	if !emCodeOK(resp.Code.String()) {
		return nil, fmt.Errorf("上游返回 code=%s", resp.Code)
	}
	if resp.Data == nil {
		return nil, fmt.Errorf("返回空数据")
	}
	out := make([]NewsItem, 0, len(resp.Data.FastNewsList))
	for _, r := range resp.Data.FastNewsList {
		title := strings.TrimSpace(r.Title)
		if title == "" {
			continue
		}
		id := r.Code.String()
		if id == "" {
			id = fmt.Sprintf("%s|%s", r.ShowTime, normTitle(title))
		}
		out = append(out, NewsItem{
			ID: id, Title: title,
			// 快讯正文就是一句话，标题常带【…】前缀，摘要直接给正文
			Summary:   clipRunes(strings.TrimSpace(r.Summary), 300),
			Media:     "东方财富",
			Provider:  "eastmoney",
			Published: r.ShowTime,
			TS:        parseNewsTime(r.ShowTime),
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("没有快讯")
	}
	return out, nil
}

// ---------------------------------------------------------------- 新浪 7x24 快讯

const defaultSinaFlashURL = "https://zhibo.sina.com.cn/api/zhibo/feed"

// SinaFlashSource 新浪财经 7x24 快讯（综合财经快讯，靠关键词挑期货相关的）。
type SinaFlashSource struct {
	HTTP     *http.Client
	URL      string
	ZhiboID  string // 152 = 财经 7x24
	PageSize int
}

func NewSinaFlashSource() *SinaFlashSource {
	return &SinaFlashSource{URL: defaultSinaFlashURL, ZhiboID: "152", PageSize: 100}
}

func (s *SinaFlashSource) Name() string { return "sina" }

func (s *SinaFlashSource) FuturesScoped() bool { return false }

func (s *SinaFlashSource) Fetch(ctx context.Context, limit int) ([]NewsItem, error) {
	if limit <= 0 {
		limit = s.PageSize
	}
	if limit <= 0 {
		limit = 100
	}
	q := url.Values{
		"page":      {"1"},
		"page_size": {fmt.Sprintf("%d", limit)},
		"zhibo_id":  {s.ZhiboID},
		"tag_id":    {"0"},
		"dire":      {"f"},
		"dpc":       {"1"},
	}
	body, err := newsGet(ctx, s.HTTP, s.url()+"?"+q.Encode(), "https://finance.sina.com.cn/7x24/")
	if err != nil {
		return nil, err
	}
	return parseSinaFlash(body)
}

func (s *SinaFlashSource) url() string {
	if s.URL != "" {
		return s.URL
	}
	return defaultSinaFlashURL
}

func parseSinaFlash(body []byte) ([]NewsItem, error) {
	var resp struct {
		Result struct {
			Status struct {
				Code int    `json:"code"`
				Msg  string `json:"msg"`
			} `json:"status"`
			Data struct {
				Feed struct {
					List []struct {
						ID         flexString `json:"id"`
						CreateTime string     `json:"create_time"`
						RichText   string     `json:"rich_text"`
						Text       string     `json:"text"`
						Title      string     `json:"title"`
						DocURL     string     `json:"docurl"`
						URL        string     `json:"url"`
						MediaName  string     `json:"media_name"`
						Author     string     `json:"author"`
					} `json:"list"`
				} `json:"feed"`
			} `json:"data"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("解析失败：%w", err)
	}
	if resp.Result.Status.Code != 0 {
		return nil, fmt.Errorf("上游返回 %d %s", resp.Result.Status.Code, resp.Result.Status.Msg)
	}
	list := resp.Result.Data.Feed.List
	out := make([]NewsItem, 0, len(list))
	for _, r := range list {
		text := firstNonEmpty(r.Title, r.RichText, r.Text)
		if text == "" {
			continue
		}
		title, summary := splitTitle(stripHTML(text))
		id := firstNonEmpty(r.ID.String(), r.DocURL, r.URL)
		if id == "" {
			id = fmt.Sprintf("%s|%s", r.CreateTime, normTitle(title))
		}
		out = append(out, NewsItem{
			ID: id, Title: title, Summary: summary,
			URL:       firstNonEmpty(r.DocURL, r.URL),
			Media:     firstNonEmpty(r.MediaName, r.Author, "新浪财经"),
			Provider:  "sina",
			Published: r.CreateTime,
			TS:        parseNewsTime(r.CreateTime),
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("没有快讯")
	}
	return out, nil
}

// ---------------------------------------------------------------- 东方财富 资讯栏目

const defaultEMNewsURL = "https://np-listapi.eastmoney.com/comm/web/getNewsByColumns"

// EastmoneyNewsSource 东财资讯栏目（长资讯，和快讯互补；同样是综合栏目，要过滤）。
type EastmoneyNewsSource struct {
	HTTP     *http.Client
	URL      string
	Column   string
	PageSize int
}

func NewEastmoneyNewsSource(column string) *EastmoneyNewsSource {
	if column == "" {
		column = "347"
	}
	return &EastmoneyNewsSource{URL: defaultEMNewsURL, Column: column, PageSize: 100}
}

func (e *EastmoneyNewsSource) Name() string { return "emnews" }

func (e *EastmoneyNewsSource) FuturesScoped() bool { return false }

func (e *EastmoneyNewsSource) Fetch(ctx context.Context, limit int) ([]NewsItem, error) {
	if limit <= 0 {
		limit = e.PageSize
	}
	if limit <= 0 {
		limit = 100
	}
	q := url.Values{
		"client":           {"web"},
		"biz":              {"web_news_col"},
		"column":           {e.column()},
		"order":            {"1"},
		"needInteractData": {"0"},
		"page_index":       {"1"},
		"page_size":        {fmt.Sprintf("%d", limit)},
		"req_trace":        {"1"},
		"fields":           {"code,showTime,title,mediaName,summary,url,uniqueUrl"},
		"types":            {"1,20"},
	}
	body, err := newsGet(ctx, e.HTTP, e.url()+"?"+q.Encode(), "https://futures.eastmoney.com/")
	if err != nil {
		return nil, err
	}
	return parseEMNews(body)
}

func (e *EastmoneyNewsSource) url() string {
	if e.URL != "" {
		return e.URL
	}
	return defaultEMNewsURL
}

func (e *EastmoneyNewsSource) column() string {
	if e.Column != "" {
		return e.Column
	}
	return "347"
}

func parseEMNews(body []byte) ([]NewsItem, error) {
	var resp struct {
		Code flexString `json:"code"`
		Data *struct {
			List []struct {
				Code      flexString `json:"code"`
				ShowTime  string     `json:"showTime"`
				Title     string     `json:"title"`
				MediaName string     `json:"mediaName"`
				Summary   string     `json:"summary"`
				URL       string     `json:"url"`
				UniqueURL string     `json:"uniqueUrl"`
			} `json:"list"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("解析失败：%w", err)
	}
	if !emCodeOK(resp.Code.String()) {
		return nil, fmt.Errorf("上游返回 code=%s", resp.Code)
	}
	if resp.Data == nil {
		return nil, fmt.Errorf("返回空数据")
	}
	out := make([]NewsItem, 0, len(resp.Data.List))
	for _, r := range resp.Data.List {
		title := strings.TrimSpace(r.Title)
		if title == "" {
			continue
		}
		id := firstNonEmpty(r.Code.String(), r.UniqueURL, r.URL)
		if id == "" {
			id = fmt.Sprintf("%s|%s", r.ShowTime, normTitle(title))
		}
		out = append(out, NewsItem{
			ID: id, Title: title,
			Summary:   clipRunes(strings.TrimSpace(r.Summary), 300),
			URL:       firstNonEmpty(r.URL, r.UniqueURL),
			Media:     firstNonEmpty(r.MediaName, "东方财富"),
			Provider:  "emnews",
			Published: r.ShowTime,
			TS:        parseNewsTime(r.ShowTime),
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("没有新闻")
	}
	return out, nil
}

// ---------------------------------------------------------------- 工具

var htmlTagRe = regexp.MustCompile(`<[^>]*>`)

func stripHTML(s string) string {
	s = htmlTagRe.ReplaceAllString(s, "")
	for _, pair := range [][2]string{
		{"&nbsp;", " "}, {"&amp;", "&"}, {"&lt;", "<"}, {"&gt;", ">"}, {"&quot;", `"`}, {"&#39;", "'"},
	} {
		s = strings.ReplaceAll(s, pair[0], pair[1])
	}
	return strings.TrimSpace(s)
}

var sentenceSplitRe = regexp.MustCompile(`[。！？!?；;]+`)

// splitTitle 快讯是一整段文字、没有单独标题：第一句当标题，剩下当摘要。
func splitTitle(text string) (title, summary string) {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\n", " "))
	if text == "" {
		return "", ""
	}
	parts := sentenceSplitRe.Split(text, 2)
	title = strings.TrimSpace(parts[0])
	if title == "" {
		title = text
	}
	title = clipRunes(title, 80)
	if len(parts) > 1 {
		summary = clipRunes(strings.TrimSpace(parts[1]), 200)
	}
	return title, summary
}

// clipRunes 按字符（不是字节）截断，中文才不会被切坏。
func clipRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}

// parseNewsTime 各家时间格式不统一，能认的都认一遍；认不出来返回 0（排在最后）。
func parseNewsTime(raw string) int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	layouts := []string{
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
		"2006-01-02T15:04:05Z07:00",
		"2006-01-02T15:04:05",
		"2006/01/02 15:04:05",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if strings.HasSuffix(layout, "Z07:00") {
			if t, err := time.Parse(layout, raw); err == nil {
				return t.Unix()
			}
			continue
		}
		if t, err := time.ParseInLocation(layout, raw, newsCST); err == nil {
			return t.Unix()
		}
	}
	return 0
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// NewsSourcesByName 按名字建新闻源（给 config.FuturesNewsSources 用）。
// 认不出的名字直接跳过，这样填错了也只是少一个源，不会整个起不来。
//
// emColumn 是东财快讯的 fastColumn；keywords 只给「按关键词检索」的源用。
func NewsSourcesByName(names []string, emColumn string, keywords []string) []NewsSource {
	out := make([]NewsSource, 0, len(names))
	for _, raw := range names {
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "eastmoney", "em":
			out = append(out, NewEastmoneyFlashSource(emColumn))
		case "emnews", "em-news":
			// 长资讯用的是另一套栏目号（默认 347），跟快讯的 fastColumn 不是一回事
			out = append(out, NewEastmoneyNewsSource(""))
		case "sina":
			out = append(out, NewSinaFlashSource())
		case "100ppi", "ppi":
			out = append(out, NewPPPISource())
		case "emsearch", "search":
			out = append(out, NewEastmoneySearchSource(keywords))
		}
	}
	return out
}
