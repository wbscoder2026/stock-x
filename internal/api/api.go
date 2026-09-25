package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wbscoder2026/stock-x/internal/backtest"
	"github.com/wbscoder2026/stock-x/internal/config"
	"github.com/wbscoder2026/stock-x/internal/futures"
	"github.com/wbscoder2026/stock-x/internal/futuresync"
	"github.com/wbscoder2026/stock-x/internal/job"
	"github.com/wbscoder2026/stock-x/internal/notify"
	"github.com/wbscoder2026/stock-x/internal/store"
	"github.com/wbscoder2026/stock-x/internal/strategy"
	"github.com/wbscoder2026/stock-x/internal/webembed"
)

const Version = "0.1.0"

type Server struct {
	Store   *store.Store
	Jobs    *job.Manager
	Sched   *job.Scheduler
	Cfg     config.Config
	Watch   *futures.Watcher
	Quotes  *futures.QuoteService    // 浮窗实时报价（懒初始化）
	Catalog *futures.ContractCatalog // 全市场合约清单（总览页）

	quotesOnce  sync.Once
	catalogOnce sync.Once

	Bars     *futuresync.StoredSource // 回测/研究用：本地优先，缺数据走网络并回写
	Backfill *futuresync.Backfiller   // 后台慢慢补 1 分钟历史
	News     *futures.NewsHub         // 期货新闻（多源抓取 + 去重排序）
	cronFn   func()
}

func New(st *store.Store, jobs *job.Manager, sched *job.Scheduler, cfg config.Config, cronFn func()) *Server {
	srv := &Server{
		Store: st, Jobs: jobs, Sched: sched, Cfg: cfg, cronFn: cronFn,
		Watch: futures.NewDefaultWatcher(),
		Bars:  futuresync.NewStoredSource(st, futures.NewMultiSource(futures.DefaultSources()...)),
	}
	if cfg.FuturesNews {
		names := strings.Split(cfg.FuturesNewsSources, ",")
		srv.News = futures.NewNewsHub(futures.NewsSourcesByName(names, cfg.FuturesNewsColumn)...)
	}
	srv.Backfill = futuresync.NewBackfiller(st, srv.Bars.Cache, srv.Bars.Live)
	srv.Watch.OnEvents = srv.pushFuturesAlerts // 系统级提醒：飞书 / 本机通知
	_ = srv.applyFuturesBlacklist()            // 重启后黑名单仍然生效
	srv.restoreFuturesWatch()                  // 重启后按上次的配置自动恢复监控（默认开启）
	return srv
}

// loadFuturesWatchConfig 读已保存的监控配置；没保存过或数据坏了 → 默认配置。
// 页面上「参数填了又没了」的根因就是它以前只活在内存里，现在落到 SQLite。
func (s *Server) loadFuturesWatchConfig() futures.WatchConfig {
	raw, ok, err := s.Store.LoadFuturesWatchConfig()
	if err != nil || !ok {
		return futures.DefaultWatchConfig()
	}
	var cfg futures.WatchConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return futures.DefaultWatchConfig() // 脏数据退回默认，别让页面打不开
	}
	return futures.NormalizeWatchConfig(cfg)
}

// saveFuturesWatchConfig 保存监控配置。enabled 以监控器实际的运行状态为准，
// 不信任前端传值（前端漏传会把「默认开启」写坏）。
func (s *Server) saveFuturesWatchConfig(cfg futures.WatchConfig) error {
	cfg = futures.NormalizeWatchConfig(cfg)
	cfg.Enabled = s.Watch.Status().Running
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return s.Store.SaveFuturesWatchConfig(raw)
}

// restoreFuturesWatch 启动时按上次保存的配置恢复监控（默认开启）。
// 恢复失败不该让服务起不来：把原因写进状态提示，页面上看得见。
func (s *Server) restoreFuturesWatch() {
	cfg := s.loadFuturesWatchConfig()
	if !cfg.Enabled {
		return // 上次是用户主动停的，不要自作主张开起来
	}
	if _, err := s.Watch.Start(cfg); err != nil {
		s.Watch.SetAlertNote("自动恢复监控失败：" + err.Error())
	}
}

func (s *Server) futuresWatchConfigGet(w http.ResponseWriter, _ *http.Request) {
	writeOK(w, s.loadFuturesWatchConfig())
}

// futuresWatchConfigSave 保存配置：没在跑就只存起来，在跑就立刻热生效。
// 它不会「顺手启动」监控（要启动用 /watch/start），语义清晰点不容易踩坑。
func (s *Server) futuresWatchConfigSave(w http.ResponseWriter, r *http.Request) {
	var cfg futures.WatchConfig
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&cfg); err != nil {
		writeErr(w, http.StatusBadRequest, "无效 JSON")
		return
	}
	cfg = futures.NormalizeWatchConfig(cfg)
	if s.Watch.Status().Running {
		st, err := s.Watch.Start(cfg) // 运行中 → 立即生效
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := s.saveFuturesWatchConfig(st.Config); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeOK(w, st)
		return
	}
	if err := s.saveFuturesWatchConfig(cfg); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOK(w, s.Watch.Status())
}

// normalizeBlacklistInput 校验并规整黑名单入参。
func normalizeBlacklistInput(scope, value string) (string, string, error) {
	scope = strings.ToLower(strings.TrimSpace(scope))
	value = strings.ToUpper(strings.TrimSpace(value))
	if value == "" {
		return "", "", fmt.Errorf("缺少 value")
	}
	v, ok := futures.VarietyOfSymbol(value)
	if !ok {
		return "", "", fmt.Errorf("未知品种/合约 %s", value)
	}
	switch scope {
	case store.BlacklistScopeVariety:
		return scope, strings.ToUpper(v.Prefix), nil
	case store.BlacklistScopeContract:
		if strings.EqualFold(value, futures.MainSymbol(v)) {
			return "", "", fmt.Errorf("%s 是主连代码，屏蔽整个品种请选「品种」", value)
		}
		return scope, value, nil
	}
	return "", "", fmt.Errorf("scope 只能是 variety（整个品种）或 contract（单个合约）")
}

// applyFuturesBlacklist 把库里的黑名单灌进监控器。
func (s *Server) applyFuturesBlacklist() error {
	entries, err := s.Store.ListFuturesBlacklist()
	if err != nil {
		return err
	}
	bl := futures.Blacklist{Varieties: map[string]bool{}, Contracts: map[string]bool{}}
	for _, e := range entries {
		switch e.Scope {
		case store.BlacklistScopeVariety:
			bl.Varieties[e.Value] = true
		case store.BlacklistScopeContract:
			bl.Contracts[e.Value] = true
		}
	}
	s.Watch.SetBlacklist(bl)
	return nil
}

func (s *Server) futuresBlacklistList(w http.ResponseWriter, _ *http.Request) {
	list, err := s.Store.ListFuturesBlacklist()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOK(w, list)
}

func (s *Server) futuresBlacklistAdd(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Scope string `json:"scope"`
		Value string `json:"value"`
		Note  string `json:"note"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "无效 JSON")
		return
	}
	scope, value, err := normalizeBlacklistInput(body.Scope, body.Value)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.Store.AddFuturesBlacklist(scope, value, strings.TrimSpace(body.Note)); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.applyFuturesBlacklist(); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOK(w, map[string]string{"scope": scope, "value": value})
}

func (s *Server) futuresBlacklistRemove(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	scope, value, err := normalizeBlacklistInput(q.Get("scope"), q.Get("value"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	n, err := s.Store.RemoveFuturesBlacklist(scope, value)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.applyFuturesBlacklist(); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOK(w, map[string]int{"removed": n})
}

// FuturesAlertMessage 组装突破提醒文案（标题 + 飞书 markdown 行）。
func FuturesAlertMessage(events []futures.WatchEvent, limit int) (string, []string) {
	if limit <= 0 {
		limit = 12
	}
	title := "⚡ 期货突破 " + strconv.Itoa(len(events)) + " 条"
	lines := make([]string, 0, limit+1)
	for i, e := range events {
		if i >= limit {
			lines = append(lines, "…等共 "+strconv.Itoa(len(events))+" 条")
			break
		}
		icon := "📈"
		if strings.Contains(e.Direction, "向下") {
			icon = "📉"
		}
		advice := ""
		if e.StopPrice > 0 && e.TPPrice > 0 {
			advice = fmt.Sprintf("　推荐止损 %s / 止盈 %s（盈亏比 %.1f）",
				futures.FormatPrice(e.StopPrice, e.TickSize),
				futures.FormatPrice(e.TPPrice, e.TickSize), e.RR)
		}
		lines = append(lines, fmt.Sprintf("%s **%s %s** %s %s　现价 %.1f / 关键位 %.1f%s　%s",
			icon, e.Name, e.Prefix, e.Direction, e.Level, e.Close, e.LevelPrice, advice, e.Time))
	}
	return title, lines
}

// FuturesAlertSummary 一行短文案（系统通知正文，太长会被系统截断）。
func FuturesAlertSummary(events []futures.WatchEvent, limit int) string {
	if limit <= 0 {
		limit = 3
	}
	parts := make([]string, 0, limit+1)
	for i, e := range events {
		if i >= limit {
			parts = append(parts, fmt.Sprintf("等 %d 条", len(events)))
			break
		}
		parts = append(parts, fmt.Sprintf("%s %s %.1f", e.Name, e.Direction, e.Close))
	}
	return strings.Join(parts, "；")
}

// pushFuturesAlerts 把「刚发生」的突破外发（离开浏览器也能收到）。
func (s *Server) pushFuturesAlerts(cfg futures.WatchConfig, events []futures.WatchEvent) {
	title, lines := FuturesAlertMessage(events, 12)
	var notes []string

	if cfg.Alert.Feishu {
		if strings.TrimSpace(s.Cfg.FeishuWebhook) == "" {
			notes = append(notes, "飞书未配置（需设 FEISHU_WEBHOOK_URL）")
		} else if err := notify.SendCard(s.Cfg.FeishuWebhook, title, lines, "red"); err != nil {
			notes = append(notes, "飞书推送失败："+err.Error())
		} else {
			notes = append(notes, fmt.Sprintf("飞书已推送 %d 条", len(events)))
		}
	}
	if cfg.Alert.Desktop {
		if err := notify.NotifyDesktop(title, FuturesAlertSummary(events, 3), "Glass"); err != nil {
			notes = append(notes, "系统通知失败："+err.Error())
		} else {
			notes = append(notes, fmt.Sprintf("系统通知已弹 %d 条", len(events)))
		}
	}
	if len(notes) > 0 {
		s.Watch.SetAlertNote(strings.Join(notes, "；"))
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/stocks", s.stocks)
	mux.HandleFunc("GET /api/kline-coverage", s.klineCoverage)
	mux.HandleFunc("GET /api/stocks/{code}/kline", s.kline)
	mux.HandleFunc("GET /api/strategies", s.listStrategies)
	mux.HandleFunc("PUT /api/strategies/{id}", s.putStrategy)
	mux.HandleFunc("GET /api/picks", s.picks)
	mux.HandleFunc("POST /api/jobs", s.postJob)
	mux.HandleFunc("POST /api/jobs/pause", s.pauseJob)
	mux.HandleFunc("POST /api/jobs/resume", s.resumeJob)
	mux.HandleFunc("GET /api/jobs", s.listJobs)
	mux.HandleFunc("GET /api/jobs/{id}", s.getJob)
	mux.HandleFunc("GET /api/jobs/{id}/events", s.jobEvents)
	mux.HandleFunc("GET /api/schedule", s.getSchedule)
	mux.HandleFunc("PUT /api/schedule", s.putSchedule)
	mux.HandleFunc("POST /api/notify/test", s.notifyTest)
	mux.HandleFunc("POST /api/backtest", s.postBacktest)
	mux.HandleFunc("GET /api/futures/varieties", s.futuresVarieties)
	mux.HandleFunc("GET /api/futures/favorites", s.futuresFavoritesList)
	mux.HandleFunc("POST /api/futures/favorites", s.futuresFavoritesCreate)
	mux.HandleFunc("PUT /api/futures/favorites/{id}", s.futuresFavoritesUpdate)
	mux.HandleFunc("DELETE /api/futures/favorites/{id}", s.futuresFavoritesDelete)
	mux.HandleFunc("POST /api/futures/favorites/delete", s.futuresFavoritesDelete)
	mux.HandleFunc("POST /api/futures/favorites/scan", s.futuresFavoritesScan)
	mux.HandleFunc("GET /api/futures/local", s.futuresLocal)
	mux.HandleFunc("GET /api/futures/local/detail", s.futuresLocalDetail)
	mux.HandleFunc("POST /api/futures/local/backfill", s.futuresLocalBackfill)
	mux.HandleFunc("GET /api/futures/news", s.futuresNews)
	mux.HandleFunc("POST /api/futures/news/refresh", s.futuresNewsRefresh)
	mux.HandleFunc("POST /api/futures/local/pause", s.futuresLocalPause)
	mux.HandleFunc("POST /api/futures/local/resume", s.futuresLocalResume)
	mux.HandleFunc("GET /api/futures/contracts", s.futuresContracts)
	mux.HandleFunc("GET /api/futures/quotes", s.futuresQuotes)
	mux.HandleFunc("GET /api/futures/quotes/raw", s.futuresQuotesRaw)
	mux.HandleFunc("GET /api/futures/overview", s.futuresOverview)
	mux.HandleFunc("GET /api/futures/scan", s.futuresScan)
	mux.HandleFunc("POST /api/futures/backtest", s.futuresBacktest)
	mux.HandleFunc("POST /api/futures/sweep", s.futuresSweep)
	mux.HandleFunc("GET /api/futures/sweep/progress", s.futuresSweepProgress)
	mux.HandleFunc("POST /api/futures/sweep/workers", s.futuresSweepWorkers)
	mux.HandleFunc("POST /api/futures/watch/start", s.futuresWatchStart)
	mux.HandleFunc("GET /api/futures/watch/config", s.futuresWatchConfigGet)
	mux.HandleFunc("POST /api/futures/watch/config", s.futuresWatchConfigSave)
	mux.HandleFunc("POST /api/futures/watch/stop", s.futuresWatchStop)
	mux.HandleFunc("GET /api/futures/watch/status", s.futuresWatchStatus)
	mux.HandleFunc("GET /api/futures/watch/events", s.futuresWatchEvents)
	mux.HandleFunc("POST /api/futures/watch/alert/test", s.futuresWatchAlertTest)
	mux.HandleFunc("GET /api/futures/blacklist", s.futuresBlacklistList)
	mux.HandleFunc("POST /api/futures/blacklist", s.futuresBlacklistAdd)
	mux.HandleFunc("DELETE /api/futures/blacklist", s.futuresBlacklistRemove)
	mux.Handle("/", webembed.Handler())
	return mux
}

type envelope struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error string          `json:"error,omitempty"`
}

func writeOK(w http.ResponseWriter, data any) {
	b, err := json.Marshal(data)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(envelope{OK: true, Data: b})
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(envelope{OK: false, Error: msg})
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	stocks, bars, maxDate, err := s.Store.DataStats()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := map[string]any{
		"status":   "ok",
		"version":  Version,
		"stocks":   stocks,
		"bars":     bars,
		"max_date": maxDate,
	}
	if running := s.Jobs.Running(); len(running) > 0 {
		jobs := make([]map[string]string, 0, len(running))
		for _, j := range running {
			jobs = append(jobs, map[string]string{"id": j.ID, "type": j.Type, "status": "running"})
		}
		out["running_jobs"] = jobs
		pick := jobs[0]
		for _, j := range jobs {
			if j["type"] == "backfill" {
				pick = j
				break
			}
		}
		out["running"] = pick
	}
	writeOK(w, out)
}

func (s *Server) stocks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	list, err := s.Store.ListStocks(q, 50)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	type hit struct {
		Symbol string `json:"symbol"`
		Name   string `json:"name"`
		Market string `json:"market"`
	}
	out := make([]hit, 0, len(list))
	for _, st := range list {
		out = append(out, hit{st.Symbol, st.Name, st.Market})
	}
	writeOK(w, out)
}

func (s *Server) klineCoverage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	offset, _ := strconv.Atoi(q.Get("offset"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	list, total, err := s.Store.ListKlineCoverage(q.Get("q"), offset, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	type row struct {
		Symbol    string `json:"symbol"`
		Name      string `json:"name"`
		Market    string `json:"market"`
		StartDate string `json:"start_date"`
		EndDate   string `json:"end_date"`
		Bars      int    `json:"bars"`
	}
	items := make([]row, 0, len(list))
	for _, c := range list {
		items = append(items, row{c.Symbol, c.Name, c.Market, c.StartDate, c.EndDate, c.Bars})
	}
	writeOK(w, map[string]any{"total": total, "items": items})
}

func (s *Server) kline(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	bars, err := s.Store.Kline(code, r.URL.Query().Get("start"), r.URL.Query().Get("end"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	type row struct {
		Date     string  `json:"date"`
		Open     float64 `json:"open"`
		High     float64 `json:"high"`
		Low      float64 `json:"low"`
		Close    float64 `json:"close"`
		Volume   float64 `json:"volume"`
		Turnover float64 `json:"turnover"`
	}
	out := make([]row, 0, len(bars))
	for _, b := range bars {
		out = append(out, row{b.Date, b.Open, b.High, b.Low, b.Close, b.Volume, b.Turnover})
	}
	writeOK(w, out)
}

type strategyDTO struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	Description string             `json:"description"`
	Enabled     bool               `json:"enabled"`
	Params      map[string]float64 `json:"params"`
	WebhookURL  string             `json:"webhook_url"`
}

func toStrategyDTO(c store.StrategyConfig) strategyDTO {
	params := map[string]float64{}
	if strings.TrimSpace(c.ParamsJSON) != "" {
		_ = json.Unmarshal([]byte(c.ParamsJSON), &params)
	}
	if params == nil {
		params = map[string]float64{}
	}
	return strategyDTO{
		ID: c.ID, Name: c.Name, Description: c.Description,
		Enabled: c.Enabled, Params: params, WebhookURL: c.WebhookURL,
	}
}

func (s *Server) listStrategies(w http.ResponseWriter, _ *http.Request) {
	list, err := s.Store.ListStrategyConfigs()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]strategyDTO, 0, len(list))
	for _, c := range list {
		out = append(out, toStrategyDTO(c))
	}
	writeOK(w, out)
}

func (s *Server) putStrategy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	cur, err := s.Store.GetStrategyConfig(id)
	if err == sql.ErrNoRows {
		writeErr(w, http.StatusNotFound, "策略不存在")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	var body struct {
		Enabled    *bool              `json:"enabled"`
		Params     map[string]float64 `json:"params"`
		WebhookURL *string            `json:"webhook_url"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "无效 JSON")
		return
	}
	en := cur.Enabled
	if body.Enabled != nil {
		en = *body.Enabled
	}
	paramsJSON := cur.ParamsJSON
	if body.Params != nil {
		b, _ := json.Marshal(body.Params)
		paramsJSON = string(b)
	}
	hook := cur.WebhookURL
	if body.WebhookURL != nil {
		hook = *body.WebhookURL
	}
	if err := s.Store.UpdateStrategyConfig(id, en, paramsJSON, hook); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	updated, err := s.Store.GetStrategyConfig(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOK(w, toStrategyDTO(updated))
}

func (s *Server) picks(w http.ResponseWriter, r *http.Request) {
	date := r.URL.Query().Get("date")
	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")
	stgy := r.URL.Query().Get("strategy")
	symbol := r.URL.Query().Get("symbol")
	var list []store.ScanPick
	var err error
	switch {
	case strings.TrimSpace(symbol) != "":
		list, err = s.Store.ListPicksBySymbol(symbol, stgy)
	case strings.TrimSpace(from) != "" || strings.TrimSpace(to) != "":
		list, err = s.Store.ListPicksRange(from, to, stgy)
	default:
		list, err = s.Store.ListPicks(date, stgy)
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	type row struct {
		AsOf     string          `json:"as_of"`
		Strategy string          `json:"strategy"`
		Symbol   string          `json:"symbol"`
		Name     string          `json:"name"`
		Extra    json.RawMessage `json:"extra,omitempty"`
	}
	out := make([]row, 0, len(list))
	for _, p := range list {
		item := row{AsOf: p.AsOf, Strategy: p.Strategy, Symbol: p.Symbol, Name: p.Name}
		if strings.TrimSpace(p.ExtraJSON) != "" && json.Valid([]byte(p.ExtraJSON)) {
			item.Extra = json.RawMessage(p.ExtraJSON)
		}
		out = append(out, item)
	}
	writeOK(w, out)
}

func jobDTO(j store.JobRun) map[string]any {
	return map[string]any{
		"id":          j.ID,
		"type":        j.Type,
		"status":      j.Status,
		"progress":    j.Progress,
		"log":         j.Log,
		"started_at":  j.StartedAt,
		"finished_at": j.FinishedAt,
	}
}

func (s *Server) postJob(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Type    string   `json:"type"`
		From    string   `json:"from"`
		To      string   `json:"to"`
		Symbols []string `json:"symbols"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "无效 JSON")
		return
	}
	j, err := s.Jobs.Submit(r.Context(), body.Type, job.ScanArg{From: body.From, To: body.To, Symbols: body.Symbols})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusAccepted)
	b, _ := json.Marshal(jobDTO(j))
	_ = json.NewEncoder(w).Encode(envelope{OK: true, Data: b})
}

func (s *Server) pauseJob(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Type string `json:"type"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body)
	if err := s.Jobs.Pause(body.Type); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeOK(w, map[string]string{"status": "pausing"})
}

func (s *Server) resumeJob(w http.ResponseWriter, r *http.Request) {
	j, err := s.Jobs.Submit(r.Context(), "backfill")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusAccepted)
	b, _ := json.Marshal(jobDTO(j))
	_ = json.NewEncoder(w).Encode(envelope{OK: true, Data: b})
}

func (s *Server) listJobs(w http.ResponseWriter, _ *http.Request) {
	list, err := s.Store.ListJobs(50)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, j := range list {
		out = append(out, jobDTO(j))
	}
	writeOK(w, out)
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	j, err := s.Store.GetJob(r.PathValue("id"))
	if err == sql.ErrNoRows {
		writeErr(w, http.StatusNotFound, "任务不存在")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOK(w, jobDTO(j))
}

func (s *Server) jobEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	j, err := s.Store.GetJob(id)
	if err == sql.ErrNoRows {
		writeErr(w, http.StatusNotFound, "任务不存在")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "不支持 SSE")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	send := func(j store.JobRun) {
		b, _ := json.Marshal(jobDTO(j))
		_, _ = w.Write([]byte("data: " + string(b) + "\n\n"))
		flusher.Flush()
	}
	send(j)
	if j.Status == "success" || j.Status == "failed" {
		return
	}
	ch, unsub := s.Jobs.Hub().Subscribe(id)
	defer unsub()
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			_, _ = w.Write([]byte(": ping\n\n"))
			flusher.Flush()
		case ev, ok := <-ch:
			if !ok {
				return
			}
			send(ev)
			if ev.Status == "success" || ev.Status == "failed" {
				return
			}
		}
	}
}

func (s *Server) getSchedule(w http.ResponseWriter, _ *http.Request) {
	spec := s.Sched.Spec()
	if spec == "" {
		spec = s.Cfg.CronSpec
	}
	writeOK(w, map[string]string{"cron": spec})
}

func (s *Server) putSchedule(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Cron string `json:"cron"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "无效 JSON")
		return
	}
	spec := strings.TrimSpace(body.Cron)
	if spec == "" {
		writeErr(w, http.StatusBadRequest, "cron 不能为空")
		return
	}
	if err := job.ValidateCron(spec); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.Store.SetMeta("cron", spec); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if s.cronFn != nil {
		if err := s.Sched.Reload(spec, s.cronFn); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeOK(w, map[string]string{"cron": spec})
}

func (s *Server) notifyTest(w http.ResponseWriter, _ *http.Request) {
	picks := []struct{ Symbol, Name string }{{"000001", "测试"}}
	if err := notify.Send(s.Cfg.FeishuWebhook, "测试", picks); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeOK(w, map[string]string{"status": "ok"})
}

func (s *Server) postBacktest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Strategy string `json:"strategy"`
		From     string `json:"from"`
		To       string `json:"to"`
		HoldDays int    `json:"holdDays"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "无效 JSON")
		return
	}
	strat, ok := strategy.ByID(body.Strategy)
	if !ok {
		writeErr(w, http.StatusBadRequest, "未知策略")
		return
	}
	params := strategy.Params{}
	if c, err := s.Store.GetStrategyConfig(body.Strategy); err == nil && strings.TrimSpace(c.ParamsJSON) != "" {
		_ = json.Unmarshal([]byte(c.ParamsJSON), &params)
	}
	res, err := backtest.Run(s.Store, strat, params, body.From, body.To, body.HoldDays)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOK(w, res)
}

func futuresParamsFromQuery(r *http.Request) futures.Params {
	q := r.URL.Query()
	p := futures.Params{
		Symbol: q.Get("symbol"),
		Period: q.Get("period"),
	}
	if n, err := strconv.Atoi(q.Get("orb")); err == nil {
		p.ORB = n
	}
	if n, err := strconv.Atoi(q.Get("donchian")); err == nil {
		p.Donchian = n
	}
	if n, err := strconv.Atoi(q.Get("atr_period")); err == nil {
		p.ATRPeriod = n
	}
	if n, err := strconv.ParseFloat(q.Get("atr_k"), 64); err == nil {
		p.ATRK = n
	}
	if n, err := strconv.ParseFloat(q.Get("vol_ratio"), 64); err == nil {
		p.VolRatio = n
	}
	if n, err := strconv.Atoi(q.Get("hold_bars")); err == nil {
		p.HoldBars = n
	}
	return p
}

func (s *Server) futuresVarieties(w http.ResponseWriter, _ *http.Request) {
	writeOK(w, futures.ListVarieties())
}

func (s *Server) futuresContracts(w http.ResponseWriter, r *http.Request) {
	prefix := strings.TrimSpace(r.URL.Query().Get("prefix"))
	if prefix == "" {
		writeErr(w, http.StatusBadRequest, "缺少品种 prefix")
		return
	}
	c := &futures.Client{}
	list, err := c.ContractsByPrefix(r.Context(), prefix)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeOK(w, list)
}

func (s *Server) futuresScan(w http.ResponseWriter, r *http.Request) {
	c := &futures.Client{}
	snap, err := c.Scan(r.Context(), futuresParamsFromQuery(r))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeOK(w, snap)
}

func (s *Server) futuresWatchStart(w http.ResponseWriter, r *http.Request) {
	var cfg futures.WatchConfig
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&cfg); err != nil {
		writeErr(w, http.StatusBadRequest, "无效 JSON")
		return
	}
	st, err := s.Watch.Start(cfg)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	_ = s.saveFuturesWatchConfig(st.Config) // 记住这套配置（enabled=true → 重启后自动开）
	writeOK(w, st)
}

func (s *Server) futuresWatchStop(w http.ResponseWriter, _ *http.Request) {
	// 停止只翻转「开着没开着」，参数一律以库里保存的为准：
	// 拿监控器里「上次运行时的配置」去覆盖，会把用户刚保存的新参数打回去（踩过）。
	cfg := s.loadFuturesWatchConfig()
	s.Watch.Stop()
	// 此时 Running 已是 false → saveFuturesWatchConfig 会把 enabled 写成 false
	_ = s.saveFuturesWatchConfig(cfg)
	writeOK(w, s.Watch.Status())
}

func (s *Server) futuresWatchStatus(w http.ResponseWriter, _ *http.Request) {
	writeOK(w, s.Watch.Status())
}

// futuresWatchAlertTest 让用户当场验证推送通道（不用等真实突破）。
func (s *Server) futuresWatchAlertTest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Feishu  bool `json:"feishu"`
		Desktop bool `json:"desktop"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body)
	if !body.Feishu && !body.Desktop {
		writeErr(w, http.StatusBadRequest, "至少勾选一个通道")
		return
	}
	// 样例带上推荐价，让用户直接看到真实提醒的格式
	stop, tp := futures.RecommendStop(futures.StopInput{
		Prefix: "JM", Direction: "向上突破", Entry: 1523, ATR: 12,
		StopMode: futures.StopModeATR, StopATR: 1, RR: futures.DefaultRR,
	})
	events := []futures.WatchEvent{{
		Fresh: true, Time: time.Now().In(futures.CSTZone()).Format("2006-01-02 15:04"),
		Symbol: "JM0", Prefix: "JM", Name: "焦煤",
		Direction: "向上突破", Level: "测试消息（非真实突破）",
		Close: 1523, LevelPrice: 1520,
		StopPrice: stop, TPPrice: tp, RR: futures.DefaultRR,
	}}
	s.pushFuturesAlerts(futures.WatchConfig{Alert: futures.AlertConfig{
		Feishu: body.Feishu, Desktop: body.Desktop,
	}}, events)
	writeOK(w, map[string]string{"note": s.Watch.Status().AlertNote})
}

func (s *Server) futuresWatchEvents(w http.ResponseWriter, r *http.Request) {
	var since int64
	if raw := r.URL.Query().Get("since"); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
			since = n
		}
	}
	writeOK(w, s.Watch.Events(since))
}

func (s *Server) futuresBacktest(w http.ResponseWriter, r *http.Request) {
	var p futures.Params
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&p); err != nil {
		writeErr(w, http.StatusBadRequest, "无效 JSON")
		return
	}
	// 时间范围等参数错误要当 400（别让用户以为是数据源故障）
	if err := futures.ValidateBacktestParams(p); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// 本地 SQLite 优先（futures-sync 灌过就有），缺数据才走多源网络并回写
	res, err := futures.BacktestWithSource(r.Context(), s.Bars, p)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeOK(w, res)
}

// futuresSweep 参数扫描：多组候选值跑笛卡尔积，找最优组合（数据只取一次）。
func (s *Server) futuresSweep(w http.ResponseWriter, r *http.Request) {
	var req futures.SweepRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "无效 JSON")
		return
	}
	if err := futures.ValidateSweep(req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// 带 token 就建一个运行时句柄：前端能轮询进度，也能中途改并发
	run := registerSweepRun(req.Token, req.Workers)
	defer unregisterSweepRun(req.Token, run)
	if run != nil {
		req.Run = run
		req.OnProgress = run.Report
	}
	res, err := futures.SweepWithSource(r.Context(), s.Bars, req)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeOK(w, res)
}
