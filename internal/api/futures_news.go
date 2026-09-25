package api

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/wbscoder2026/stock-x/internal/futures"
	"github.com/wbscoder2026/stock-x/internal/store"
)

// futuresNews 新闻列表：默认读库（按时间从新到旧），没抓过或要求刷新时才打网络。
func (s *Server) futuresNews(w http.ResponseWriter, r *http.Request) {
	if s.News == nil {
		writeErr(w, http.StatusServiceUnavailable, "新闻未启用（FUTURES_NEWS=0 或未配置源）")
		return
	}
	limit := s.newsLimit(r.URL.Query().Get("limit"))
	// 还没抓过就先抓一轮；已经在跑就直接返回库里已有的
	_, _, fetched := s.News.Latest()
	if fetched.IsZero() && s.Store != nil {
		if _, _, err := s.News.Refresh(r.Context(), limit); err != nil {
			// 抓失败也别让页面开天窗：库里有什么就先显示什么
			_ = err
		} else {
			s.saveNews(limit)
		}
	}
	items := s.loadNews(limit)
	_, status, at := s.News.Latest()
	writeOK(w, map[string]any{
		"items":    items,
		"sources":  status,
		"updated":  at.Format("2006-01-02 15:04:05"),
		"count":    len(items),
		"interval": s.Cfg.FuturesNewsEvery.String(),
	})
}

// futuresNewsRefresh 手动立刻抓一轮（页面上的「刷新」按钮）。
func (s *Server) futuresNewsRefresh(w http.ResponseWriter, r *http.Request) {
	if s.News == nil {
		writeErr(w, http.StatusServiceUnavailable, "新闻未启用（FUTURES_NEWS=0 或未配置源）")
		return
	}
	limit := s.newsLimit(r.URL.Query().Get("limit"))
	items, status, err := s.News.Refresh(r.Context(), limit)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	s.saveNews(limit)
	_, _, at := s.News.Latest()
	writeOK(w, map[string]any{
		"items":   items,
		"sources": status,
		"updated": at.Format("2006-01-02 15:04:05"),
		"count":   len(items),
	})
}

func (s *Server) newsLimit(raw string) int {
	n := s.Cfg.FuturesNewsLimit
	if n <= 0 {
		n = 200
	}
	if raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 && v < 1000 {
			n = v
		}
	}
	return n
}

// saveNews 抓到的新闻落库（顺手只留最近一批，别让表无限长）。
func (s *Server) saveNews(limit int) {
	if s.Store == nil {
		return
	}
	items, _, _ := s.News.Latest()
	if len(items) == 0 {
		return
	}
	rows := make([]store.FuturesNews, 0, len(items))
	for _, it := range items {
		rows = append(rows, store.FuturesNews{
			Provider: it.Provider, ID: it.ID, Title: it.Title, Summary: it.Summary,
			URL: it.URL, Media: it.Media, TS: it.TS, PublishedAt: it.Published, Tags: it.Tags,
		})
	}
	if _, err := s.Store.UpsertFuturesNews(rows); err != nil {
		return
	}
	keep := limit * 10
	if keep < 500 {
		keep = 500
	}
	_, _ = s.Store.FuturesNewsPrune(keep)
}

// loadNews 读库。库里没有（第一次还没落库）就用内存里刚抓到的。
func (s *Server) loadNews(limit int) []futures.NewsItem {
	if s.Store != nil {
		if rows, err := s.Store.FuturesNewsList(limit); err == nil && len(rows) > 0 {
			out := make([]futures.NewsItem, 0, len(rows))
			for _, r := range rows {
				out = append(out, futures.NewsItem{
					ID: r.ID, Title: r.Title, Summary: r.Summary, URL: r.URL, Media: r.Media,
					Provider: r.Provider, Published: r.PublishedAt, TS: r.TS, Tags: r.Tags,
				})
			}
			return out
		}
	}
	items, _, _ := s.News.Latest()
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items
}

// RefreshFuturesNewsLoop 后台定时抓新闻（main 里 go 起来）。
func RefreshFuturesNewsLoop(ctx context.Context, s *Server) {
	if s == nil || s.News == nil {
		return
	}
	interval := s.Cfg.FuturesNewsEvery
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	limit := s.Cfg.FuturesNewsLimit
	if limit <= 0 {
		limit = 200
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, _, err := s.News.Refresh(ctx, limit); err == nil {
				s.saveNews(limit)
			}
		}
	}
}
