package config

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// Config 为进程级运行配置，由环境变量加载。
type Config struct {
	HTTPAddr      string // 默认 :8080
	DBPath        string // 默认 data/stock-x.db
	StartDate     string // 默认 2024-01-01
	FeishuWebhook string
	CronSpec      string // 默认 15 19 * * 1-5
	Workers       int    // 默认 4，上限见 syncer.maxWorkers
}

// Load 读取环境变量；若存在 .env 则先填入尚未设置的键（不覆盖已有环境变量）。
func Load() Config {
	loadDotEnv(".env")
	workers := 4
	if s := strings.TrimSpace(os.Getenv("BAOSTOCK_WORKERS")); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			workers = n
		}
	}
	return Config{
		HTTPAddr:      getenv("HTTP_ADDR", ":8080"),
		DBPath:        getenv("DB_PATH", "data/stock-x.db"),
		StartDate:     getenv("START_DATE", "2024-01-01"),
		FeishuWebhook: strings.TrimSpace(os.Getenv("FEISHU_WEBHOOK_URL")),
		CronSpec:      getenv("CRON_SPEC", "15 19 * * 1-5"),
		Workers:       workers,
	}
}

func getenv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
		return def
	}
	return def
}

// loadDotEnv 简易 KEY=VAL，忽略空行与 # 注释；已存在的环境变量不覆盖。
func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		v = strings.TrimSpace(v)
		if n := len(v); n >= 2 {
			if (v[0] == '"' && v[n-1] == '"') || (v[0] == '\'' && v[n-1] == '\'') {
				v = v[1 : n-1]
			}
		}
		if _, exists := os.LookupEnv(k); !exists {
			_ = os.Setenv(k, v)
		}
	}
}
