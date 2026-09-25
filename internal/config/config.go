package config

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config 为进程级运行配置，由环境变量加载。
type Config struct {
	HTTPAddr      string // 默认 :8080
	DBPath        string // 默认 data/stock-x.db
	StartDate     string // 默认 2024-01-01
	FeishuWebhook string
	CronSpec      string // 默认 15 19 * * 1-5
	Workers       int    // 默认 4，上限见 syncer.maxWorkers
	// 服务启动后在后台把期货 K 线增量写入本地库，并装进内存给扫描用。
	FuturesSync        bool
	FuturesSyncEvery   time.Duration
	FuturesSyncPeriods string
	FuturesSyncWorkers int
	FuturesSyncDerive  bool          // 1 分钟够深时，其他分钟周期本地合成（不再各问上游要一遍）
	FuturesNews        bool          // 后台定时抓期货新闻。0 = 关闭
	FuturesNewsEvery   time.Duration // 抓取间隔
	FuturesNewsSources string        // 新闻源，逗号分隔（sina / eastmoney）
	FuturesNewsLimit   int           // 列表最多返回多少条
	FuturesNewsColumn  string        // 东财快讯栏目号 fastColumn（默认 102 = 国际财经，含商品）
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
		HTTPAddr:           getenv("HTTP_ADDR", ":8080"),
		DBPath:             getenv("DB_PATH", "data/stock-x.db"),
		StartDate:          getenv("START_DATE", "2024-01-01"),
		FeishuWebhook:      strings.TrimSpace(os.Getenv("FEISHU_WEBHOOK_URL")),
		CronSpec:           getenv("CRON_SPEC", "15 19 * * 1-5"),
		Workers:            workers,
		FuturesSync:        getenvBool("FUTURES_SYNC", true),
		FuturesSyncEvery:   getenvDuration("FUTURES_SYNC_EVERY", 30*time.Minute),
		FuturesSyncPeriods: getenv("FUTURES_SYNC_PERIODS", "1d,5,15,30,60,120"),
		FuturesSyncWorkers: getenvInt("FUTURES_SYNC_WORKERS", 4),
		FuturesSyncDerive:  getenvBool("FUTURES_SYNC_DERIVE", true),
		FuturesNews:        getenvBool("FUTURES_NEWS", true),
		FuturesNewsEvery:   getenvDuration("FUTURES_NEWS_EVERY", 5*time.Minute),
		FuturesNewsSources: getenv("FUTURES_NEWS_SOURCES", "eastmoney,sina"),
		FuturesNewsLimit:   getenvInt("FUTURES_NEWS_LIMIT", 200),
		FuturesNewsColumn:  getenv("FUTURES_NEWS_EM_COLUMN", "102"),
	}
}

func getenvBool(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

func getenvDuration(key string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return def
	}
	return d
}

func getenvInt(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
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
