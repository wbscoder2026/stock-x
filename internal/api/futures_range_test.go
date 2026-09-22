package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFuturesBacktestValidatesRange(t *testing.T) {
	// 起止颠倒 / 时间格式无法解析 → 400，不能等到取数阶段才报 502
	s := &Server{}
	for _, body := range []string{
		`{"symbol":"JM0","from":"2026-09-22","to":"2026-09-01"}`,
		`{"symbol":"JM0","from":"昨天"}`,
	} {
		rec := httptest.NewRecorder()
		s.futuresBacktest(rec, httptest.NewRequest(http.MethodPost, "/api/futures/backtest", strings.NewReader(body)))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("应 400，实际 %d %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"ok":false`) {
			t.Fatalf("响应格式不对：%s", rec.Body.String())
		}
	}
}

func TestFuturesSweepValidatesRange(t *testing.T) {
	rec := sweepPost(t, `{"symbol":"JM0","from":"刚才","to":"2026-09-01"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("扫描也应拦住非法范围：%d %s", rec.Code, rec.Body.String())
	}
}
