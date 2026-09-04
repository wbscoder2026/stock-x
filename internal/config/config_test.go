package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("HTTP_ADDR", "")
	t.Setenv("DB_PATH", "")
	t.Setenv("START_DATE", "")
	t.Setenv("FEISHU_WEBHOOK_URL", "")
	t.Setenv("CRON_SPEC", "")
	t.Setenv("BAOSTOCK_WORKERS", "")
	cfg := Load()
	if cfg.HTTPAddr != ":8080" {
		t.Fatalf("HTTPAddr=%q", cfg.HTTPAddr)
	}
	if cfg.DBPath != "data/stock-x.db" {
		t.Fatalf("DBPath=%q", cfg.DBPath)
	}
	if cfg.StartDate != "2024-01-01" {
		t.Fatalf("StartDate=%q", cfg.StartDate)
	}
	if cfg.CronSpec != "15 19 * * 1-5" {
		t.Fatalf("CronSpec=%q", cfg.CronSpec)
	}
	if cfg.Workers != 4 {
		t.Fatalf("Workers=%d", cfg.Workers)
	}
}

func TestLoadEnv(t *testing.T) {
	t.Setenv("HTTP_ADDR", ":9090")
	t.Setenv("DB_PATH", "/tmp/x.db")
	t.Setenv("START_DATE", "2020-01-01")
	t.Setenv("FEISHU_WEBHOOK_URL", "https://example/hook")
	t.Setenv("CRON_SPEC", "0 20 * * 1-5")
	t.Setenv("BAOSTOCK_WORKERS", "4")
	cfg := Load()
	if cfg.HTTPAddr != ":9090" || cfg.DBPath != "/tmp/x.db" || cfg.StartDate != "2020-01-01" {
		t.Fatalf("%+v", cfg)
	}
	if cfg.FeishuWebhook != "https://example/hook" || cfg.CronSpec != "0 20 * * 1-5" || cfg.Workers != 4 {
		t.Fatalf("%+v", cfg)
	}
}

func TestLoadDotEnv(t *testing.T) {
	dir := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	body := "HTTP_ADDR=:7777\nDB_PATH=from.env\n# comment\nBAOSTOCK_WORKERS=3\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"HTTP_ADDR", "DB_PATH", "START_DATE", "FEISHU_WEBHOOK_URL", "CRON_SPEC", "BAOSTOCK_WORKERS"} {
		t.Setenv(k, "x")
		_ = os.Unsetenv(k)
	}
	cfg := Load()
	if cfg.HTTPAddr != ":7777" || cfg.DBPath != "from.env" || cfg.Workers != 3 {
		t.Fatalf("%+v", cfg)
	}
}
