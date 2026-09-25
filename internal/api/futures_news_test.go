package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wbscoder2026/stock-x/internal/futures"
	"github.com/wbscoder2026/stock-x/internal/store"
)

// stubNewsSource 固定返回几条新闻（测试不碰网络）
type stubNewsSource struct {
	name   string
	scoped bool
	items  []futures.NewsItem
	err    error
}

func (s *stubNewsSource) Name() string        { return s.name }
func (s *stubNewsSource) FuturesScoped() bool { return s.scoped }
func (s *stubNewsSource) Fetch(context.Context, int) ([]futures.NewsItem, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.items, nil
}

func newsServer(t *testing.T, sources ...futures.NewsSource) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "news.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s := &Server{Store: st, News: futures.NewNewsHub(sources...)}
	s.Cfg.FuturesNewsLimit = 200
	return s, st
}

func TestFuturesNewsListsNewestFirstAndPersists(t *testing.T) {
	src := &stubNewsSource{name: "em", scoped: true, items: []futures.NewsItem{
		{ID: "1", Provider: "em", Title: "旧闻", Published: "2026-09-20 09:00:00", TS: 1000},
		{ID: "2", Provider: "em", Title: "焦煤期货涨停", Published: "2026-09-26 09:00:00", TS: 3000},
		{ID: "3", Provider: "em", Title: "中间", Published: "2026-09-23 09:00:00", TS: 2000},
	}}
	s, st := newsServer(t, src)

	rec := httptest.NewRecorder()
	s.futuresNews(rec, httptest.NewRequest(http.MethodGet, "/api/futures/news", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("应 200：%d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Index(body, "焦煤期货涨停") > strings.Index(body, "旧闻") {
		t.Fatalf("应从新到旧：%s", body)
	}
	// 抓到的要落库，下次直接读库
	rows, err := st.FuturesNewsList(0)
	if err != nil || len(rows) != 3 {
		t.Fatalf("应落库 3 条：%d %v", len(rows), err)
	}
	if rows[0].Title != "焦煤期货涨停" {
		t.Fatalf("库里也该是从新到旧：%+v", rows[0])
	}
	if !strings.Contains(body, `"sources"`) || !strings.Contains(body, `"updated"`) {
		t.Fatalf("应带上源状态和更新时间：%s", body)
	}
}

// 源挂了不能让页面开天窗：有旧数据就先显示旧的，并报告哪个源没抓到
func TestFuturesNewsReportsFailingSource(t *testing.T) {
	bad := &stubNewsSource{name: "sina", err: context.DeadlineExceeded}
	s, _ := newsServer(t, bad)

	rec := httptest.NewRecorder()
	s.futuresNewsRefresh(rec, httptest.NewRequest(http.MethodPost, "/api/futures/news/refresh", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("单个源失败不该让整个接口失败：%d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "sina") {
		t.Fatalf("应报告是哪个源没抓到：%s", rec.Body.String())
	}
}

func TestFuturesNewsRefreshEndpoint(t *testing.T) {
	src := &stubNewsSource{name: "em", scoped: true, items: []futures.NewsItem{
		{ID: "1", Provider: "em", Title: "螺纹钢期货走强", Published: "2026-09-26 10:00:00", TS: 3000},
	}}
	s, _ := newsServer(t, src)

	rec := httptest.NewRecorder()
	s.futuresNewsRefresh(rec, httptest.NewRequest(http.MethodPost, "/api/futures/news/refresh", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("应 200：%d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "螺纹钢期货走强") {
		t.Fatalf("应带上抓到的新闻：%s", rec.Body.String())
	}
}

func TestFuturesNewsDisabled(t *testing.T) {
	s := &Server{}
	rec := httptest.NewRecorder()
	s.futuresNews(rec, httptest.NewRequest(http.MethodGet, "/api/futures/news", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("没启用应 503：%d", rec.Code)
	}
	rec = httptest.NewRecorder()
	s.futuresNewsRefresh(rec, httptest.NewRequest(http.MethodPost, "/api/futures/news/refresh", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("没启用应 503：%d", rec.Code)
	}
}

func TestNewsLimitFallsBack(t *testing.T) {
	s := &Server{}
	if got := s.newsLimit(""); got != 200 {
		t.Fatalf("没配就用默认：%d", got)
	}
	s.Cfg.FuturesNewsLimit = 50
	if got := s.newsLimit("10"); got != 10 {
		t.Fatalf("传入值优先：%d", got)
	}
	if got := s.newsLimit("abc"); got != 50 {
		t.Fatalf("非法值回退配置：%d", got)
	}
	if got := s.newsLimit("9999"); got != 50 {
		t.Fatalf("超上限回退配置：%d", got)
	}
}
