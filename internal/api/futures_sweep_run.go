package api

import (
	"encoding/json"
	"io"
	"net/http"
	"sync"

	"github.com/wbscoder2026/stock-x/internal/futures"
)

// 扫描运行时句柄表：一个 token 一个 futures.SweepRun（进度 + 可调并发）。
// 扫描本身是同步 POST（返回 = 结束），前端在跑的过程中靠它读进度、改并发；
// 请求返回时注销，所以表里只有「正在跑」的扫描。token 由前端生成。

var sweepRunTable = struct {
	mu sync.Mutex
	m  map[string]*futures.SweepRun
}{m: map[string]*futures.SweepRun{}}

func registerSweepRun(token string, workers int) *futures.SweepRun {
	if token == "" {
		return nil
	}
	run := futures.NewSweepRun(workers)
	sweepRunTable.mu.Lock()
	sweepRunTable.m[token] = run
	sweepRunTable.mu.Unlock()
	return run
}

// unregisterSweepRun 只删自己那一份：同一个 token 又起了新扫描时，别把新的删掉。
func unregisterSweepRun(token string, run *futures.SweepRun) {
	if token == "" || run == nil {
		return
	}
	sweepRunTable.mu.Lock()
	if cur, ok := sweepRunTable.m[token]; ok && cur == run {
		delete(sweepRunTable.m, token)
	}
	sweepRunTable.mu.Unlock()
}

func lookupSweepRun(token string) *futures.SweepRun {
	if token == "" {
		return nil
	}
	sweepRunTable.mu.Lock()
	defer sweepRunTable.mu.Unlock()
	return sweepRunTable.m[token]
}

// SweepProgressView 进度响应。running=false（没带 token / 还没开始 / 已经结束）
// 时前端不该拿 done/total/workers 去画界面。
type SweepProgressView struct {
	Running bool  `json:"running"`
	Done    int64 `json:"done"`
	Total   int64 `json:"total"`
	Workers int   `json:"workers"` // 当前实际并发（中途改过就是改后的值）
}

func (s *Server) futuresSweepProgress(w http.ResponseWriter, r *http.Request) {
	run := lookupSweepRun(r.URL.Query().Get("token"))
	if run == nil {
		writeOK(w, SweepProgressView{})
		return
	}
	done, total := run.Progress()
	writeOK(w, SweepProgressView{Running: true, Done: done, Total: total, Workers: run.Workers()})
}

// futuresSweepWorkers 扫描途中改并发：立刻 Tune 协程池 —— 正在跑的那几个任务
// 不打断，之后提交的任务按新并发上（调小则等当前任务跑完自然收敛）。
func (s *Server) futuresSweepWorkers(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token   string `json:"token"`
		Workers int    `json:"workers"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "无效 JSON")
		return
	}
	run := lookupSweepRun(body.Token)
	if run == nil {
		// 没在跑（已结束 / token 不认）：并发值前端表单自己存着，不算错误
		writeOK(w, map[string]any{"running": false, "workers": futures.NormalizeSweepWorkers(body.Workers)})
		return
	}
	run.SetWorkers(body.Workers)
	writeOK(w, map[string]any{"running": true, "workers": run.Workers()})
}
