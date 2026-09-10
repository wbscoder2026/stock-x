package syncer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wbscoder2026/stock-x/internal/baostock"
	"github.com/wbscoder2026/stock-x/internal/store"
)

type fetchJob struct {
	symbol, start, end string
}

type writeReq struct {
	symbol string
	bars   []store.Bar
	err    error
}

// retryWait 网络抖动短重试，避免单票拖死整批。
var retryWait = []time.Duration{400 * time.Millisecond, time.Second, 2 * time.Second}

const (
	writeBatchBars = 12000 // 大约 20 只票的日 K
	progressEvery  = 20
)

func (s *Syncer) runJobs(ctx context.Context, jobs []fetchJob, progress func(done, total int, msg string), total, already int) (int, error) {
	if len(jobs) == 0 {
		report(progress, already, total, "无待拉取任务")
		return 0, nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	jobsCh := make(chan fetchJob, maxWorkers*4)
	writes := make(chan writeReq, maxWorkers*4)

	var fetchWG sync.WaitGroup
	var running atomic.Int32
	var feederDone atomic.Bool
	lastN := 0

	spawn := func() {
		running.Add(1)
		fetchWG.Add(1)
		go func() {
			defer fetchWG.Done()
			dropped := false
			defer func() {
				if !dropped {
					running.Add(-1)
				}
			}()
			var c *baostock.Client
			defer drop(&c)
			if err := s.ensureClient(ctx, &c); err != nil {
				writes <- writeReq{symbol: "_login", err: err}
				if baostock.IsBlacklisted(err) {
					cancel()
				}
				return
			}
			for {
				want := s.WorkerCount()
				for {
					n := running.Load()
					if int(n) <= want {
						break
					}
					if running.CompareAndSwap(n, n-1) {
						dropped = true
						return
					}
				}
				select {
				case <-ctx.Done():
					return
				case job, ok := <-jobsCh:
					if !ok {
						return
					}
					if ctx.Err() != nil {
						writes <- writeReq{symbol: job.symbol, err: ctx.Err()}
						return
					}
					bars, err := s.fetchSymbol(ctx, &c, job)
					if baostock.IsBlacklisted(err) {
						writes <- writeReq{symbol: job.symbol, err: err}
						cancel()
						return
					}
					writes <- writeReq{symbol: job.symbol, bars: bars, err: err}
				case <-time.After(200 * time.Millisecond):
				}
			}
		}()
	}

	adjust := func() {
		want := s.WorkerCount()
		if want != lastN {
			if lastN == 0 {
				report(progress, already, total, fmt.Sprintf("启动 %d 路并发拉取 %d 只", want, len(jobs)))
			} else {
				report(progress, already, total, fmt.Sprintf("回填并发调整为 %d 路", want))
			}
			lastN = want
		}
		if feederDone.Load() {
			return
		}
		have := int(running.Load())
		for have < want && ctx.Err() == nil {
			spawn()
			have++
		}
	}

	go func() {
		defer func() {
			close(jobsCh)
			feederDone.Store(true)
		}()
		for _, j := range jobs {
			select {
			case <-ctx.Done():
				return
			case jobsCh <- j:
			}
		}
	}()

	adjust()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	doneFetch := make(chan struct{})
	go func() {
		defer close(doneFetch)
		for {
			if feederDone.Load() && running.Load() == 0 {
				fetchWG.Wait()
				return
			}
			select {
			case <-ctx.Done():
				fetchWG.Wait()
				return
			case <-tick.C:
				adjust()
			}
		}
	}()
	go func() {
		<-doneFetch
		close(writes)
	}()

	return s.drainWrites(ctx, writes, progress, total, already)
}

func (s *Syncer) drainWrites(ctx context.Context, writes <-chan writeReq, progress func(done, total int, msg string), total, already int) (int, error) {
	var (
		done    = already
		wrote   int
		errs    []error
		batch   []store.Bar
		pending []writeReq
	)
	flush := func() {
		if len(pending) == 0 {
			return
		}
		var err error
		if len(batch) > 0 {
			err = s.Store.UpsertBars(batch)
		}
		for _, p := range pending {
			n := 0
			e := p.err
			if e == nil {
				e = err
				n = len(p.bars)
			}
			wrote += n
			done++
			if e != nil {
				errs = append(errs, fmt.Errorf("%s: %w", p.symbol, e))
			}
			if progress != nil && (e != nil || done == total || done%progressEvery == 0) {
				msg := fmt.Sprintf("%s 写入 %d 根（并发中）", p.symbol, n)
				if e != nil {
					msg = fmt.Sprintf("%s 失败: %v", p.symbol, e)
				}
				progress(done, total, msg)
			}
		}
		batch = batch[:0]
		pending = pending[:0]
	}

	paused := false
	var banned error
	for w := range writes {
		if baostock.IsBlacklisted(w.err) {
			banned = w.err
			continue
		}
		if errors.Is(w.err, context.Canceled) || errors.Is(w.err, context.DeadlineExceeded) {
			paused = true
			continue
		}
		if w.err != nil || len(w.bars) == 0 {
			pending = append(pending, w)
			flush()
			continue
		}
		batch = append(batch, w.bars...)
		pending = append(pending, w)
		if len(batch) >= writeBatchBars {
			flush()
		}
	}
	flush()
	if banned != nil {
		if progress != nil {
			progress(done, total, banned.Error())
		}
		return wrote, banned
	}
	if paused || ctx.Err() != nil {
		if progress != nil {
			progress(done, total, "收到暂停，已落盘当前批次")
		}
		return wrote, context.Canceled
	}
	return wrote, errors.Join(errs...)
}

func (s *Syncer) fetchSymbol(ctx context.Context, c **baostock.Client, job fetchJob) ([]store.Bar, error) {
	var out []store.Bar
	err := retry(ctx, retryWait, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.ensureClient(ctx, c); err != nil {
			return err
		}
		ks, err := (*c).HistoryK(ctx, baostock.ToBSCode(job.symbol), job.start, job.end, adjustForward)
		if err != nil {
			if !baostock.IsBlacklisted(err) {
				drop(c)
			}
			return err
		}
		out = kbarsToBars(job.symbol, ks)
		return nil
	})
	return out, err
}

func (s *Syncer) ensureClient(ctx context.Context, c **baostock.Client) error {
	if *c != nil {
		return nil
	}
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	if *c != nil {
		return nil
	}
	if !s.lastLogin.IsZero() {
		if wait := loginGap - time.Since(s.lastLogin); wait > 0 {
			if err := sleep(ctx, wait); err != nil {
				return err
			}
		}
	}
	nc, err := baostock.Dial(ctx, s.dialAddr())
	if err != nil {
		return err
	}
	if err := nc.Login(ctx); err != nil {
		_ = nc.Close()
		return err
	}
	s.lastLogin = time.Now()
	*c = nc
	return nil
}

func drop(c **baostock.Client) {
	if c == nil || *c == nil {
		return
	}
	_ = (*c).Close()
	*c = nil
}

func retry(ctx context.Context, waits []time.Duration, fn func() error) error {
	var err error
	for i := 0; ; i++ {
		err = fn()
		if err == nil {
			return nil
		}
		if baostock.IsBlacklisted(err) {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if i >= len(waits) {
			return err
		}
		if e := sleep(ctx, waits[i]); e != nil {
			return e
		}
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
