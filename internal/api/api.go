package api

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/wbscoder2026/stock-x/internal/backtest"
	"github.com/wbscoder2026/stock-x/internal/config"
	"github.com/wbscoder2026/stock-x/internal/job"
	"github.com/wbscoder2026/stock-x/internal/notify"
	"github.com/wbscoder2026/stock-x/internal/store"
	"github.com/wbscoder2026/stock-x/internal/strategy"
	"github.com/wbscoder2026/stock-x/internal/webembed"
)

const Version = "0.1.0"

type Server struct {
	Store  *store.Store
	Jobs   *job.Manager
	Sched  *job.Scheduler
	Cfg    config.Config
	cronFn func()
}

func New(st *store.Store, jobs *job.Manager, sched *job.Scheduler, cfg config.Config, cronFn func()) *Server {
	return &Server{Store: st, Jobs: jobs, Sched: sched, Cfg: cfg, cronFn: cronFn}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/stocks", s.stocks)
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
	if id, typ, ok := s.Jobs.Current(); ok {
		out["running"] = map[string]string{"id": id, "type": typ, "status": "running"}
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
	var list []store.ScanPick
	var err error
	switch {
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
		Type string `json:"type"`
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "无效 JSON")
		return
	}
	j, err := s.Jobs.Submit(r.Context(), body.Type, job.ScanArg{From: body.From, To: body.To})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusAccepted)
	b, _ := json.Marshal(jobDTO(j))
	_ = json.NewEncoder(w).Encode(envelope{OK: true, Data: b})
}

func (s *Server) pauseJob(w http.ResponseWriter, _ *http.Request) {
	if err := s.Jobs.Pause(); err != nil {
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
