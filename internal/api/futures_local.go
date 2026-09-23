package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/wbscoder2026/stock-x/internal/futuresync"
)

func (s *Server) futuresLocal(w http.ResponseWriter, _ *http.Request) {
	if s.Store == nil {
		writeErr(w, http.StatusInternalServerError, "缺少本地存储")
		return
	}
	items, err := futuresync.LocalCoverage(s.Store)
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

func (s *Server) futuresLocalBackfill(w http.ResponseWriter, r *http.Request) {
	if s.Backfill == nil {
		writeErr(w, http.StatusServiceUnavailable, "补全未启动")
		return
	}
	var body struct {
		Prefix string `json:"prefix"`
		From   string `json:"from"`
		To     string `json:"to"`
	}
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
	if err := s.Backfill.Enqueue(futuresync.BackfillRequest{
		Prefix: body.Prefix, From: from, To: to,
	}); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeOK(w, s.Backfill.Status())
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
