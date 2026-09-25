package futures

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// NewsItem 一条新闻/快讯。
type NewsItem struct {
	ID        string   `json:"id"`        // 源内唯一 id（去重用）
	Title     string   `json:"title"`     // 标题（快讯没有标题时就是正文首行）
	Summary   string   `json:"summary"`   // 摘要/正文
	URL       string   `json:"url"`       // 原文链接，可能为空
	Media     string   `json:"media"`     // 媒体/来源名
	Provider  string   `json:"provider"`  // 抓取源：sina / eastmoney
	Published string   `json:"published"` // 发布时间 2006-01-02 15:04:05（东八区）
	TS        int64    `json:"ts"`        // 发布时间 Unix 秒，排序用
	Tags      []string `json:"tags"`      // 命中的期货品种名
}

// NewsSource 一个新闻源。
type NewsSource interface {
	Name() string
	// FuturesScoped 该源本身就是期货频道 → 不用再按关键词过滤一遍；
	// 综合快讯源（7x24 之类）返回 false，靠关键词挑出期货相关的。
	FuturesScoped() bool
	Fetch(ctx context.Context, limit int) ([]NewsItem, error)
}

// NewsSourceStatus 每个源最近一次的健康度，页面要显示「哪个源没抓到」。
type NewsSourceStatus struct {
	Name  string `json:"name"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	Count int    `json:"count"` // 这一次抓回来多少条
}

// NewsHub 汇总多个新闻源：并发抓、去重、按时间倒序。
// 单个源挂了不影响其他源——页面上能看到是哪个源没抓到，而不是整块空白。
type NewsHub struct {
	Sources []NewsSource

	mu      sync.Mutex
	last    []NewsItem
	status  []NewsSourceStatus
	fetched time.Time
}

func NewNewsHub(sources ...NewsSource) *NewsHub {
	return &NewsHub{Sources: sources}
}

// Refresh 抓一轮并缓存结果。并发抓，各源互不影响。
func (h *NewsHub) Refresh(ctx context.Context, limit int) ([]NewsItem, []NewsSourceStatus, error) {
	if h == nil || len(h.Sources) == 0 {
		return nil, nil, fmt.Errorf("没有配置新闻源")
	}
	if limit <= 0 {
		limit = 50
	}
	// 每个源自己多抓一些：综合快讯里期货相关的只占一小部分，
	// 过滤、去重之后才凑得够 limit 条。
	per := limit * 3
	if per < 100 {
		per = 100
	}
	if per > 200 {
		per = 200
	}

	var wg sync.WaitGroup
	got := make([][]NewsItem, len(h.Sources))
	status := make([]NewsSourceStatus, len(h.Sources))
	for i, src := range h.Sources {
		wg.Add(1)
		go func(i int, src NewsSource) {
			defer wg.Done()
			items, err := src.Fetch(ctx, per)
			st := NewsSourceStatus{Name: src.Name()}
			if err != nil {
				st.Error = err.Error()
			} else {
				st.OK = true
				if !src.FuturesScoped() {
					items = keepFutures(items) // 综合快讯只留期货相关的
				}
				for j := range items {
					items[j].Tags = MatchVarieties(items[j].Title + " " + items[j].Summary)
				}
				got[i] = items
				st.Count = len(items)
			}
			status[i] = st
		}(i, src)
	}
	wg.Wait()

	all := make([]NewsItem, 0, limit*2)
	for _, list := range got {
		all = append(all, list...)
	}
	merged := mergeNews(all, limit)

	h.mu.Lock()
	h.last = merged
	h.status = status
	h.fetched = time.Now()
	h.mu.Unlock()
	return merged, status, nil
}

// Latest 最近一次抓到的结果（不触发网络）。fetched 为零值表示还没抓过。
func (h *NewsHub) Latest() ([]NewsItem, []NewsSourceStatus, time.Time) {
	if h == nil {
		return nil, nil, time.Time{}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.last, h.status, h.fetched
}

// mergeNews 去重 + 按时间从新到旧排序。
// 同一条新闻常被多个源转发，按「标题+时间」认作同一条，留信息更全的那份。
func mergeNews(items []NewsItem, limit int) []NewsItem {
	seen := map[string]int{}
	out := make([]NewsItem, 0, len(items))
	for _, it := range items {
		it.Title = strings.TrimSpace(it.Title)
		if it.Title == "" {
			continue
		}
		key := newsKey(it)
		if idx, ok := seen[key]; ok {
			if better(it, out[idx]) {
				out[idx] = it
			}
			continue
		}
		seen[key] = len(out)
		out = append(out, it)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].TS != out[j].TS {
			return out[i].TS > out[j].TS // 从新到旧
		}
		return out[i].ID > out[j].ID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func newsKey(it NewsItem) string {
	title := normTitle(it.Title)
	return title + "|" + it.Published
}

// normTitle 去掉空白和标点，用于认出「同一条被多家转发」的新闻。
// 中文要保留——只留字母数字的话，所有中文标题都会被压成同一个 key 而被误判成重复。
var normDropRe = regexp.MustCompile(`[\s\p{P}\p{S}]+`)

func normTitle(s string) string {
	out := normDropRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "")
	runes := []rune(out)
	if len(runes) > 60 {
		runes = runes[:60]
	}
	return string(runes)
}

// better 两条重复的新闻里留下信息更全的那条（有摘要/有链接的优先）。
func better(a, b NewsItem) bool {
	return score(a) > score(b)
}

func score(it NewsItem) int {
	n := 0
	if it.Summary != "" {
		n++
	}
	if it.URL != "" {
		n++
	}
	if it.Media != "" {
		n++
	}
	n += len(it.Summary) / 80
	return n
}

// ---------------------------------------------------------------- 期货相关性

// futuresContext 明确的期货语境，命中就一定是相关的。
var futuresContext = []string{
	"期货", "期价", "期市", "主力合约", "持仓", "仓单", "交割", "夜盘",
	"大商所", "郑商所", "上期所", "中金所", "广期所", "上期能源",
	"升水", "贴水", "基差", "涨停", "跌停",
	"大宗商品", "商品期货", "期货市场",
	// 注意：这里不放泛泛的「交易所」——港交所、X交所的个股新闻都会被算进来
}

// commodityWords 光看到品种名还不够——「黄金周」「苹果手机」「燃油税」里都有品种名，
// 得再有个商品/行情语境才认。
var commodityWords = []string{
	"现货", "库存", "产量", "供应", "需求", "开工", "报价", "结算", "收盘",
	"吨", "桶", "盎司", "涨", "跌", "反弹", "回落", "失守", "触及", "走高", "走低",
}

var contractCodeRe = regexp.MustCompile(`\b([A-Z]{2,3})\d{3,4}\b`)

// varietyTokenRe 品种代码只在「独立成词」时算命中。
//
// 直接子串匹配会闹笑话：ANTHROPIC 里的 IC（中证500）、GAO 里的 AO（氧化铝）、
// RMX 里的 RM（菜粕）都会被打上标签，于是无关的美股/时政快讯也混进「期货新闻」。
var varietyTokenRe = regexp.MustCompile(`(?:^|[^A-Za-z0-9])([A-Z]{2,3})(?:[^A-Za-z0-9]|$)`)

// keepFutures 综合快讯里挑出期货相关的。
func keepFutures(items []NewsItem) []NewsItem {
	out := make([]NewsItem, 0, len(items))
	for _, it := range items {
		if IsFuturesRelated(it.Title, it.Summary) {
			out = append(out, it)
		}
	}
	return out
}

// IsFuturesRelated 这条新闻跟期货有没有关系。title 是标题/快讯首句，body 是正文。
//
// 分两档，因为综合快讯的正文里经常顺带提一句「纳指期货涨 0.45%」，
// 光看正文会把一堆美股新闻也算成期货新闻：
//  1. 硬信号（期货/交割/交易所/RB2610…）**只看标题** —— 标题写了才算
//  2. 只提到品种名 → 标题正文一起看，还要求有商品/行情语境，
//     否则「黄金周」「苹果手机」也会被当成黄金/苹果期货
func IsFuturesRelated(title, body string) bool {
	if strings.TrimSpace(title) == "" && strings.TrimSpace(body) == "" {
		return false
	}
	if hasAny(title, futuresContext) || contractCodeRe.MatchString(strings.ToUpper(title)) {
		return true
	}
	// 品种名也要在标题里：标题是「美股三大指数小幅高开」而正文提了苹果公司，
	// 那是苹果的股票不是苹果期货。行情语境则标题正文一起看。
	if len(MatchVarieties(title)) == 0 {
		return false
	}
	return hasAny(title+" "+body, commodityWords)
}

func hasAny(text string, words []string) bool {
	for _, kw := range words {
		if strings.Contains(text, kw) {
			return true
		}
	}
	return false
}

// MatchVarieties 这段文字命中了哪些品种，返回品种中文名（用于打标签）。
//
// 中文名（焦煤、螺纹钢…）直接匹配；英文代码要求独立成词（或写成合约代码 RB2610），
// 单字母代码（A/B/C/I…）一律不认，否则英文句子里的字母会被当成品种。
func MatchVarieties(text string) []string {
	if text == "" {
		return nil
	}
	upper := strings.ToUpper(text)
	prefixes := map[string]bool{}
	for _, m := range varietyTokenRe.FindAllStringSubmatch(upper, -1) {
		prefixes[m[1]] = true
	}
	for _, m := range contractCodeRe.FindAllStringSubmatch(upper, -1) {
		prefixes[m[1]] = true
	}
	out := make([]string, 0, 4)
	seen := map[string]bool{}
	for _, v := range varieties {
		if seen[v.Prefix] {
			continue
		}
		if strings.Contains(text, v.Name) || prefixes[v.Prefix] {
			seen[v.Prefix] = true
			out = append(out, v.Name)
		}
	}
	sort.Strings(out)
	return out
}
