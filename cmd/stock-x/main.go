package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/wbscoder2026/stock-x/internal/api"
	"github.com/wbscoder2026/stock-x/internal/baostock"
	"github.com/wbscoder2026/stock-x/internal/config"
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
	default:
		log.Fatalf("未知子命令 %s（serve|backfill|sync|scan）", cmd)
	}
}

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
