package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/wbscoder2026/stock-x/internal/api"
	"github.com/wbscoder2026/stock-x/internal/baostock"
	"github.com/wbscoder2026/stock-x/internal/config"
	"github.com/wbscoder2026/stock-x/internal/futures"
	"github.com/wbscoder2026/stock-x/internal/futuresync"
	"github.com/wbscoder2026/stock-x/internal/job"
	"github.com/wbscoder2026/stock-x/internal/store"
	"github.com/wbscoder2026/stock-x/internal/syncer"
)

func main() {
	cfg := config.Load()
	cmd := "serve"
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-") {
		cmd = os.Args[1]
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()
	if err := st.SeedStrategies(job.SeedConfigs()); err != nil {
		log.Fatal(err)
	}

	sy := &syncer.Syncer{
		Store:     st,
		StartDate: cfg.StartDate,
		Workers:   cfg.Workers,
		Addr:      baostock.DefaultAddr,
	}
	mgr := job.New(st, sy, cfg)
	ctx := context.Background()

	switch cmd {
	case "serve", "":
		runServe(cfg, st, mgr)
	case "backfill":
		if err := mgr.DoBackfill(ctx, logProgress, "", "", nil); err != nil {
			log.Fatal(err)
		}
	case "sync":
		n, err := mgr.DoSync(ctx, logProgress)
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("增量同步完成 bars=%d", n)
	case "scan":
		if err := mgr.DoScan(ctx, logProgress, "", ""); err != nil {
			log.Fatal(err)
		}
	case "futures-sync":
		if err := runFuturesSync(st, false); err != nil {
			log.Fatal(err)
		}
	case "futures-backfill":
		if err := runFuturesSync(st, true); err != nil {
			log.Fatal(err)
		}
	default:
		log.Fatalf("未知子命令 %s（serve|backfill|sync|scan|futures-sync|futures-backfill）", cmd)
	}
}

// runFuturesSync 把期货历史 K 线落到本地 SQLite（futures-backfill 会额外往前翻页挖日线历史）。
//
//	go run ./cmd/stock-x futures-sync
//	go run ./cmd/stock-x futures-sync --periods=1d,5,60 --only=JM,RB
//	go run ./cmd/stock-x futures-backfill --periods=1d --pages=4
func runFuturesSync(st *store.Store, deep bool) error {
	opts, err := futuresSyncOptions(os.Args[2:], deep)
	if err != nil {
		return err
	}
	src := futures.NewMultiSource(futures.DefaultSources()...)
	opts.Progress = func(done, total int, msg string) {
		if done == total || done%20 == 0 {
			log.Printf("[%d/%d] %s", done, total, msg)
		}
	}

	stats, err := futuresync.Sync(context.Background(), st, []futures.BarSource{src}, opts)
	if err != nil {
		return err
	}
	log.Printf("期货同步完成：%d 个品种 × %d 个任务，网络拉取 %d 根，本地新增 %d 根，失败 %d 个",
		stats.Symbols, stats.Jobs, stats.Fetched, stats.Saved, len(stats.Failed))
	if deep && stats.Saved == 0 {
		log.Printf("提示：深挖没推进——东财（唯一支持翻页的源）可能被限流，或本地已有全部历史，稍后重跑即可")
	}
	for i, f := range stats.Failed {
		if i >= 5 {
			log.Printf("  ...还有 %d 个失败", len(stats.Failed)-5)
			break
		}
		log.Printf("  失败：%s", f)
	}
	if symbols, bars, err := st.FuturesStats(); err == nil {
		log.Printf("本地期货库：%d 个「品种/周期」，共 %d 根 K 线", symbols, bars)
	}
	return nil
}

func futuresSyncOptions(args []string, deep bool) (futuresync.Options, error) {
	opts := futuresync.Options{Deep: deep}
	if deep {
		opts.Pages = futuresync.DefaultPages
	}
	for _, arg := range args {
		key, value, _ := strings.Cut(arg, "=")
		switch key {
		case "--periods":
			periods, err := futuresync.ParsePeriods(value)
			if err != nil {
				return opts, err
			}
			opts.Periods = periods
		case "--only":
			for _, p := range strings.Split(value, ",") {
				if p = strings.TrimSpace(p); p != "" {
					opts.Prefixes = append(opts.Prefixes, p)
				}
			}
		case "--pages":
			n, err := strconv.Atoi(value)
			if err != nil || n <= 0 {
				return opts, fmt.Errorf("--pages 需为正整数：%s", value)
			}
			opts.Pages = n
		case "--start":
			t, err := time.ParseInLocation("2006-01-02", value, cstZone())
			if err != nil {
				return opts, fmt.Errorf("--start 需为 YYYY-MM-DD：%s", value)
			}
			opts.Start = t
		case "--workers":
			n, err := strconv.Atoi(value)
			if err != nil || n <= 0 {
				return opts, fmt.Errorf("--workers 需为正整数：%s", value)
			}
			opts.Workers = n
		case "--bars":
			n, err := strconv.Atoi(value)
			if err != nil || n <= 0 {
				return opts, fmt.Errorf("--bars 需为正整数：%s", value)
			}
			opts.MinuteBars = n
		default:
			return opts, fmt.Errorf("未知参数 %s（可用 --periods --only --pages --start --workers --bars）", arg)
		}
	}
	return opts, nil
}

func cstZone() *time.Location { return time.FixedZone("CST", 8*3600) }

func logProgress(pct int, line string) {
	if line != "" {
		log.Printf("[%d%%] %s", pct, line)
	}
}

func runServe(cfg config.Config, st *store.Store, mgr *job.Manager) {
	sched := job.NewScheduler()
	cronFn := func() {
		if _, err := mgr.Submit(context.Background(), "sync"); err != nil {
			log.Printf("定时同步提交失败: %v", err)
		}
		if _, err := mgr.Submit(context.Background(), "scan"); err != nil {
			log.Printf("定时选股提交失败: %v", err)
		}
	}
	spec := cfg.CronSpec
	if v, ok, err := st.GetMeta("cron"); err == nil && ok && strings.TrimSpace(v) != "" {
		spec = v
	} else {
		_ = st.SetMeta("cron", spec)
	}
	if err := sched.Start(spec, cronFn); err != nil {
		log.Fatalf("启动 cron: %v", err)
	}
	defer sched.Stop()

	srvAPI := api.New(st, mgr, sched, cfg, cronFn)
	httpSrv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           srvAPI.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		log.Printf("stock-x 监听 %s", cfg.HTTPAddr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
}
