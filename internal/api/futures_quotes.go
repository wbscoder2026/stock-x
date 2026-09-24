package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/wbscoder2026/stock-x/internal/futures"
)

// maxQuoteSymbols 单次批量报价的代码上限：
// 浮窗最多 5 行（前端只发 5 个），期货总览页按当前页批量取，所以给宽一点。
const maxQuoteSymbols = 30

// quoteService 懒初始化 + 复用：报价服务带短缓存，多个页面/多次轮询共用一份。
func (s *Server) quoteService() *futures.QuoteService {
	s.quotesOnce.Do(func() {
		if s.Quotes == nil {
			s.Quotes = futures.NewQuoteService(&futures.Client{})
		}
	})
	return s.Quotes
}

// parseQuoteSymbols 解析 ?symbols=JM0,RB0：去重 + 上限校验，返回是否通过。
func parseQuoteSymbols(w http.ResponseWriter, r *http.Request, limit int) ([]string, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get("symbols"))
	if raw == "" {
		writeErr(w, http.StatusBadRequest, "缺少 symbols（逗号分隔）")
		return nil, false
	}
	seen := map[string]bool{}
	symbols := []string{}
	for _, part := range strings.Split(raw, ",") {
		v := strings.ToUpper(strings.TrimSpace(part))
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		symbols = append(symbols, v)
	}
	if len(symbols) == 0 {
		writeErr(w, http.StatusBadRequest, "symbols 为空")
		return nil, false
	}
	if len(symbols) > limit {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("最多 %d 个代码", limit))
		return nil, false
	}
	return symbols, true
}

// futuresQuotes 实时报价：价格 / 持仓量 / 成交量 以分钟线为准，
// 买一卖一来自实时口（要过价格交叉校验，见 futures.QuoteService）。
func (s *Server) futuresQuotes(w http.ResponseWriter, r *http.Request) {
	symbols, ok := parseQuoteSymbols(w, r, maxQuoteSymbols)
	if !ok {
		return
	}
	writeOK(w, s.quoteService().Snapshot(r.Context(), symbols))
}

// futuresQuotesRaw 诊断接口：把上游那行**原样**吐出来、字段按索引列好，
// 再附上分钟线的价格/持仓量做对照 —— 实时口字段位置有歧义时靠它一次定位。
func (s *Server) futuresQuotesRaw(w http.ResponseWriter, r *http.Request) {
	symbols, ok := parseQuoteSymbols(w, r, 5)
	if !ok {
		return
	}
	svc := s.quoteService()
	ctx := r.Context()
	raw := svc.Client.RawTicks(ctx, symbols)

	out := map[string]any{}
	for _, sym := range symbols {
		item := map[string]any{}
		if tick, ok := raw[sym]; ok {
			item["raw_line"] = tick.Line
			indexed := make([]string, 0, len(tick.Fields))
			for i, f := range tick.Fields {
				indexed = append(indexed, fmt.Sprintf("%d=%s", i, strings.TrimSpace(f)))
			}
			item["fields"] = indexed
		}
		// 分钟线对照：价格/持仓量以它为准，实时口的字段对不对一眼就能比出来
		if bars, err := svc.Client.Minute(ctx, sym, "1"); err == nil && len(bars) > 0 {
			last := bars[len(bars)-1]
			item["kline"] = map[string]any{
				"price": last.Close, "hold": last.Hold, "volume": last.Volume,
				"time": last.Time.Format("2006-01-02 15:04"),
			}
		}
		out[sym] = item
	}
	writeOK(w, out)
}
