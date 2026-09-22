package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func sweepPost(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	s := &Server{}
	rec := httptest.NewRecorder()
	s.futuresSweep(rec, httptest.NewRequest(http.MethodPost, "/api/futures/sweep", strings.NewReader(body)))
	return rec
}

// bigGridBody 30×30×30 = 27000 组（超过 10000 上限）
func bigGridBody() string {
	nums := make([]string, 0, 30)
	for i := 1; i <= 30; i++ {
		nums = append(nums, strconv.Itoa(i))
	}
	grid := "[" + strings.Join(nums, ",") + "]"
	return fmt.Sprintf(`{"symbol":"RB0","hold_bars":%s,"donchian":%s,"atr_period":%s}`, grid, grid, grid)
}

func TestFuturesSweepValidatesRequest(t *testing.T) {
	// 参数问题应当是 400（不是 502，避免让用户以为是数据源故障）
	cases := []struct {
		name, body string
	}{
		{"未知目标", `{"symbol":"RB0","objective":"whatever"}`},
		{"非法级别", `{"symbol":"RB0","periods":["7"]}`},
		{"组合数超上限", bigGridBody()},
		{"非法 JSON", `{`},
	}
	for _, c := range cases {
		rec := sweepPost(t, c.body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s 应 400，实际 %d %s", c.name, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"ok":false`) {
			t.Fatalf("%s 响应格式不对：%s", c.name, rec.Body.String())
		}
	}
}

func TestFuturesSweepRejectsUnknownVariety(t *testing.T) {
	// 未知品种由 SweepWithSource 报错 → 502（数据层错误）
	s := &Server{}
	rec := httptest.NewRecorder()
	s.futuresSweep(rec, httptest.NewRequest(http.MethodPost, "/api/futures/sweep",
		strings.NewReader(`{"symbol":"ZZZ0","rr":[1]}`)))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("未知品种应 502，实际 %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "未知品种") {
		t.Fatalf("错误信息应说明原因：%s", rec.Body.String())
	}
}
