package store

import (
	"path/filepath"
	"testing"
	"time"
)

func openNewsStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "news.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func newsRow(provider, id, title string, ts int64, tags ...string) FuturesNews {
	return FuturesNews{
		Provider: provider, ID: id, Title: title, Summary: "摘要",
		URL: "https://x/" + id, Media: "某媒体",
		TS: ts, PublishedAt: time.Unix(ts, 0).Format("2006-01-02 15:04:05"),
		Tags: tags,
	}
}

func TestFuturesNewsUpsertAndOrder(t *testing.T) {
	st := openNewsStore(t)
	rows := []FuturesNews{
		newsRow("sina", "1", "最早", 1000),
		newsRow("eastmoney", "2", "最新", 3000, "焦煤"),
		newsRow("sina", "3", "中间", 2000, "螺纹钢", "铁矿石"),
	}
	n, err := st.UpsertFuturesNews(rows)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("应写入 3 条：%d", n)
	}

	got, err := st.FuturesNewsList(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("应读出 3 条：%d", len(got))
	}
	// 从新到旧
	if got[0].Title != "最新" || got[1].Title != "中间" || got[2].Title != "最早" {
		t.Fatalf("应按时间从新到旧：%+v", got)
	}
	// 标签要能原样取回
	if len(got[1].Tags) != 2 || got[1].Tags[0] != "螺纹钢" {
		t.Fatalf("标签没存对：%+v", got[1].Tags)
	}
}

// 同一个源重复抓到同一条 → 覆盖，不堆积
func TestFuturesNewsUpsertDeduplicates(t *testing.T) {
	st := openNewsStore(t)
	if _, err := st.UpsertFuturesNews([]FuturesNews{newsRow("sina", "1", "原标题", 1000)}); err != nil {
		t.Fatal(err)
	}
	updated := newsRow("sina", "1", "改过的标题", 1000)
	updated.Summary = "补充了内容"
	if _, err := st.UpsertFuturesNews([]FuturesNews{updated}); err != nil {
		t.Fatal(err)
	}
	got, err := st.FuturesNewsList(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("同一条不该堆积：%d", len(got))
	}
	if got[0].Title != "改过的标题" || got[0].Summary != "补充了内容" {
		t.Fatalf("应被覆盖：%+v", got[0])
	}
}

// 不同源转发同一条新闻，各自保留（去重只在同一个源内做）
func TestFuturesNewsKeepsSameIDAcrossProviders(t *testing.T) {
	st := openNewsStore(t)
	if _, err := st.UpsertFuturesNews([]FuturesNews{
		newsRow("sina", "1", "A", 1000),
		newsRow("eastmoney", "1", "B", 1000),
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := st.FuturesNewsList(0)
	if len(got) != 2 {
		t.Fatalf("不同源的同 id 应各留一条：%d", len(got))
	}
}

func TestFuturesNewsUpsertSkipsIncomplete(t *testing.T) {
	st := openNewsStore(t)
	n, err := st.UpsertFuturesNews([]FuturesNews{
		{Provider: "", ID: "1", Title: "没源"},
		{Provider: "sina", ID: "", Title: "没 id"},
		{Provider: "sina", ID: "2", Title: ""},
		newsRow("sina", "3", "正常", 1000),
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("缺字段的应跳过：%d", n)
	}
}

func TestFuturesNewsListLimit(t *testing.T) {
	st := openNewsStore(t)
	rows := make([]FuturesNews, 0, 5)
	for i := 0; i < 5; i++ {
		rows = append(rows, newsRow("sina", string(rune('a'+i)), string(rune('a'+i)), int64(1000+i)))
	}
	if _, err := st.UpsertFuturesNews(rows); err != nil {
		t.Fatal(err)
	}
	got, err := st.FuturesNewsList(2)
	if err != nil || len(got) != 2 {
		t.Fatalf("limit 不生效：%d %v", len(got), err)
	}
	if got[0].TS <= got[1].TS {
		t.Fatalf("应倒序：%+v", got)
	}
}

// 只留最近 N 条，别让新闻表无限长
func TestFuturesNewsPrune(t *testing.T) {
	st := openNewsStore(t)
	rows := make([]FuturesNews, 0, 6)
	for i := 0; i < 6; i++ {
		rows = append(rows, newsRow("sina", string(rune('a'+i)), string(rune('a'+i)), int64(1000+i)))
	}
	if _, err := st.UpsertFuturesNews(rows); err != nil {
		t.Fatal(err)
	}
	if _, err := st.FuturesNewsPrune(3); err != nil {
		t.Fatal(err)
	}
	got, _ := st.FuturesNewsList(0)
	if len(got) != 3 {
		t.Fatalf("应只剩 3 条：%d", len(got))
	}
	for _, it := range got {
		if it.TS < 1003 { // 留下的是最新的三条
			t.Fatalf("应保留最新的：%+v", it)
		}
	}
	if _, err := st.FuturesNewsPrune(0); err != nil {
		t.Fatal(err)
	}
}

func TestFuturesNewsEmptyList(t *testing.T) {
	st := openNewsStore(t)
	got, err := st.FuturesNewsList(0)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("空库应返回空切片而不是 nil：%v", got)
	}
	if n, err := st.UpsertFuturesNews(nil); err != nil || n != 0 {
		t.Fatalf("空写入应直接返回：%d %v", n, err)
	}
}
