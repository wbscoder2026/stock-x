package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/wbscoder2026/stock-x/internal/futures"
	"github.com/wbscoder2026/stock-x/internal/futuresync"
)

func (s *Server) futuresLocal(w http.ResponseWriter, _ *http.Request) {
	if s.Store == nil {
		writeErr(w, http.StatusInternalServerError, "缺少本地存储")
		return
	}
	// 月份合约清单来自缓存（不打接口）；缺的品种后台慢慢补，下个轮询就出现。
	var months map[string][]string
	if s.Contracts != nil {
		months = s.Contracts.MonthSnapshot()
		s.Contracts.RefreshAsync(futures.ListVarieties())
	}
	items, err := futuresync.LocalCoverage(s.Store, months)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	var backfill futuresync.BackfillStatus
	if s.Backfill != nil {
		backfill = s.Backfill.Status()
	}
	var memory futuresync.MemoryView
	if s.Bars != nil {
		memory = s.Bars.Cache.MemoryView()
	}
	writeOK(w, map[string]any{
		"items":    items,
		"backfill": backfill,
		"memory":   memory,
	})
}

// futuresLocalProgress 只回补全状态（不查覆盖表），给进度条高频轮询用。
func (s *Server) futuresLocalProgress(w http.ResponseWriter, _ *http.Request) {
	if s.Backfill == nil {
		writeErr(w, http.StatusServiceUnavailable, "补全未启动")
		return
	}
	writeOK(w, s.Backfill.Status())
}

// backfillBody 补全请求。三种给法，优先用更具体的：
//   - symbols：直接给合约代码列表（主连 JM0、月份 JM2611 混着给也行）
//   - symbol：单个合约代码
//   - prefix + kinds：按品种补，kinds 默认 ["main","months"]
//     （主连 + 该品种在交易的全部月份合约一起补；只想要主力月份就传 "month"）
type backfillBody struct {
	Prefix  string   `json:"prefix"`
	Symbol  string   `json:"symbol"`
	Symbols []string `json:"symbols"`
	Kinds   []string `json:"kinds"`
	From    string   `json:"from"`
	To      string   `json:"to"`
}

func (s *Server) futuresLocalBackfill(w http.ResponseWriter, r *http.Request) {
	if s.Backfill == nil {
		writeErr(w, http.StatusServiceUnavailable, "补全未启动")
		return
	}
	var body backfillBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "无效 JSON")
		return
	}
	from, err := parseLocalDay(body.From, false)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "开始日期格式应为 YYYY-MM-DD")
		return
	}
	to, err := parseLocalDay(body.To, true)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "结束日期格式应为 YYYY-MM-DD")
		return
	}
	reqs, err := s.backfillRequests(body, from, to)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.Backfill.EnqueueAll(reqs); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeOK(w, s.Backfill.Status())
}

// backfillRequests 把请求体翻译成一批补全任务。
func (s *Server) backfillRequests(body backfillBody, from, to time.Time) ([]futuresync.BackfillRequest, error) {
	reqs := make([]futuresync.BackfillRequest, 0, 2)
	switch {
	case len(body.Symbols) > 0:
		for _, sym := range body.Symbols {
			if strings.TrimSpace(sym) == "" {
				continue
			}
			reqs = append(reqs, futuresync.BackfillRequest{Symbol: sym, From: from, To: to})
		}
	case strings.TrimSpace(body.Symbol) != "":
		reqs = append(reqs, futuresync.BackfillRequest{Symbol: body.Symbol, From: from, To: to})
	default:
		prefix := strings.ToUpper(strings.TrimSpace(body.Prefix))
		if prefix == "" {
			return nil, fmt.Errorf("请指定品种或合约")
		}
		kinds := body.Kinds
		if len(kinds) == 0 {
			kinds = []string{"main", "months"}
		}
		for _, k := range kinds {
			switch strings.ToLower(strings.TrimSpace(k)) {
			case "main", "":
				reqs = append(reqs, futuresync.BackfillRequest{Prefix: prefix, From: from, To: to})
			case "month": // 只补主力月份
				if sym := s.mainMonthOf(prefix); sym != "" {
					reqs = append(reqs, futuresync.BackfillRequest{Symbol: sym, From: from, To: to})
				}
			case "months": // 该品种在交易的全部月份合约
				for _, sym := range s.monthSymbolsOf(prefix) {
					reqs = append(reqs, futuresync.BackfillRequest{Symbol: sym, From: from, To: to})
				}
			}
		}
	}
	if len(reqs) == 0 {
		return nil, fmt.Errorf("没有可补的标的（月份合约还没解析到，稍后再试）")
	}
	return reqs, nil
}

// mainMonthOf 品种当前的主力月份合约（读缓存，不打接口）。
func (s *Server) mainMonthOf(prefix string) string {
	if s.Contracts == nil {
		return ""
	}
	got, ok := s.Contracts.Get(prefix)
	if !ok {
		return ""
	}
	return got.Symbol
}

// monthSymbolsOf 品种在交易的全部月份合约（读缓存，不打接口）。
func (s *Server) monthSymbolsOf(prefix string) []string {
	if s.Contracts == nil {
		return nil
	}
	return s.Contracts.MonthSymbols(prefix)
}

func (s *Server) futuresLocalPause(w http.ResponseWriter, _ *http.Request) {
	if s.Backfill == nil {
		writeErr(w, http.StatusServiceUnavailable, "补全未启动")
		return
	}
	if err := s.Backfill.Pause(); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOK(w, s.Backfill.Status())
}

func (s *Server) futuresLocalResume(w http.ResponseWriter, _ *http.Request) {
	if s.Backfill == nil {
		writeErr(w, http.StatusServiceUnavailable, "补全未启动")
		return
	}
	if err := s.Backfill.Resume(); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOK(w, s.Backfill.Status())
}

func parseLocalDay(raw string, endOfDay bool) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, nil
	}
	t, err := time.ParseInLocation("2006-01-02", raw, time.FixedZone("CST", 8*3600))
	if err != nil {
		return time.Time{}, err
	}
	if endOfDay {
		t = time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 0, 0, t.Location())
	}
	return t, nil
}
