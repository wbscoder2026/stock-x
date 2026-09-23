package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/wbscoder2026/stock-x/internal/futures"
)

func sweepPost(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	s := &Server{}
	rec := httptest.NewRecorder()
	s.futuresSweep(rec, httptest.NewRequest(http.MethodPost, "/api/futures/sweep", strings.NewReader(body)))
	return rec
}

// bigGridBody 30^5 = 2430 万组（超过 1000 万上限）
func bigGridBody() string {
	nums := make([]string, 0, 30)
	for i := 1; i <= 30; i++ {
		nums = append(nums, strconv.Itoa(i))
	}
	grid := "[" + strings.Join(nums, ",") + "]"
	return fmt.Sprintf(`{"symbol":"RB0","hold_bars":%s,"donchian":%s,"atr_period":%s,"orb":%s,"atr_k":%s}`,
		grid, grid, grid, grid, grid)
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

func sweepRunPost(t *testing.T, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	s := &Server{}
	rec := httptest.NewRecorder()
	s.futuresSweepWorkers(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
	return rec
}

func TestSweepRunRegistry(t *testing.T) {
	if registerSweepRun("", 4) != nil {
		t.Fatal("空 token 不该登记")
	}
	if lookupSweepRun("") != nil || lookupSweepRun("nope") != nil {
		t.Fatal("没登记的 token 应查不到")
	}

	run := registerSweepRun("tok-1", 2)
	if run == nil || run.Workers() != 2 {
		t.Fatalf("应登记并按 2 并发建句柄：%v", run)
	}
	run.Report(3, 10)

	rec := httptest.NewRecorder()
	s := &Server{}
	s.futuresSweepProgress(rec, httptest.NewRequest(http.MethodGet, "/api/futures/sweep/progress?token=tok-1", nil))
	var env struct {
		Data SweepProgressView `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if !env.Data.Running || env.Data.Done != 3 || env.Data.Total != 10 || env.Data.Workers != 2 {
		t.Fatalf("进度不对：%+v", env.Data)
	}

	// 同一个 token 又起了一次扫描：旧的那份结束时不该把新的删掉
	run2 := registerSweepRun("tok-1", 8)
	unregisterSweepRun("tok-1", run)
	if lookupSweepRun("tok-1") != run2 {
		t.Fatal("旧扫描注销时误删了新扫描的句柄")
	}
	unregisterSweepRun("tok-1", run2)
	if lookupSweepRun("tok-1") != nil {
		t.Fatal("注销后应查不到")
	}
}

func TestSweepWorkersEndpoint(t *testing.T) {
	type resp struct {
		Data struct {
			Running bool `json:"running"`
			Workers int  `json:"workers"`
		} `json:"data"`
	}
	parse := func(rec *httptest.ResponseRecorder) resp {
		t.Helper()
		if rec.Code != http.StatusOK {
			t.Fatalf("应 200：%d %s", rec.Code, rec.Body.String())
		}
		var out resp
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	// 没在跑：回 running=false + 规整过的并发（超上限截断），不算错误
	got := parse(sweepRunPost(t, "/api/futures/sweep/workers", `{"token":"nope","workers":999}`))
	if got.Data.Running || got.Data.Workers != futures.SweepMaxWorkers {
		t.Fatalf("没在跑时的响应不对：%+v", got.Data)
	}

	// 在跑：改并发立刻生效
	run := registerSweepRun("tok-w", 2)
	t.Cleanup(func() { unregisterSweepRun("tok-w", run) })
	got = parse(sweepRunPost(t, "/api/futures/sweep/workers", `{"token":"tok-w","workers":12}`))
	if !got.Data.Running || got.Data.Workers != 12 || run.Workers() != 12 {
		t.Fatalf("并发没改成功：%+v / %d", got.Data, run.Workers())
	}

	// 非法 JSON → 400
	if rec := sweepRunPost(t, "/api/futures/sweep/workers", `{`); rec.Code != http.StatusBadRequest {
		t.Fatalf("非法 JSON 应 400：%d", rec.Code)
	}
}

func TestSweepRunUnregisteredAfterRequest(t *testing.T) {
	// 请求结束后句柄表不该留残留（否则前端会一直以为在跑）
	rec := sweepPost(t, `{"symbol":"ZZZ0","token":"tok-x","rr":[1]}`)
	if rec.Code != http.StatusBadGateway { // 未知品种 → 502，但句柄必须先注销
		t.Fatalf("应 502：%d %s", rec.Code, rec.Body.String())
	}
	if lookupSweepRun("tok-x") != nil {
		t.Fatal("扫描结束后句柄应被注销")
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
