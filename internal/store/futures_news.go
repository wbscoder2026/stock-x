package store

import (
	"encoding/json"
	"strings"
	"time"
)

const futuresNewsDDL = `
CREATE TABLE IF NOT EXISTS futures_news (
	provider TEXT NOT NULL,
	news_id TEXT NOT NULL,
	title TEXT NOT NULL,
	summary TEXT,
	url TEXT,
	media TEXT,
	ts INTEGER NOT NULL,
	published_at TEXT NOT NULL,
	tags TEXT,
	created_at TEXT,
	PRIMARY KEY(provider, news_id)
);
CREATE INDEX IF NOT EXISTS idx_futures_news_ts ON futures_news(ts DESC);
`

// FuturesNews 一条已入库的新闻。
type FuturesNews struct {
	Provider    string   `json:"provider"`
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Summary     string   `json:"summary"`
	URL         string   `json:"url"`
	Media       string   `json:"media"`
	TS          int64    `json:"ts"`
	PublishedAt string   `json:"published_at"`
	Tags        []string `json:"tags"`
}

// UpsertFuturesNews 落库；同一个源里同一条（news_id）重复抓到就覆盖，不会堆积。
func (s *Store) UpsertFuturesNews(items []FuturesNews) (int, error) {
	if len(items) == 0 {
		return 0, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	stmt, err := tx.Prepare(`
INSERT INTO futures_news(provider,news_id,title,summary,url,media,ts,published_at,tags,created_at)
VALUES(?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(provider,news_id) DO UPDATE SET
	title=excluded.title, summary=excluded.summary, url=excluded.url,
	media=excluded.media, ts=excluded.ts, published_at=excluded.published_at,
	tags=excluded.tags`)
	if err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	defer stmt.Close()

	now := time.Now().UTC().Format(time.RFC3339)
	n := 0
	for _, it := range items {
		if it.Provider == "" || it.ID == "" || it.Title == "" {
			continue
		}
		var tags string
		if len(it.Tags) > 0 {
			if raw, err := json.Marshal(it.Tags); err == nil {
				tags = string(raw)
			}
		}
		if _, err := stmt.Exec(it.Provider, it.ID, it.Title, it.Summary, it.URL, it.Media,
			it.TS, it.PublishedAt, tags, now); err != nil {
			_ = tx.Rollback()
			return n, err
		}
		n++
	}
	if err := tx.Commit(); err != nil {
		return n, err
	}
	return n, nil
}

// FuturesNewsList 按时间从新到旧取新闻；limit<=0 表示不限。
func (s *Store) FuturesNewsList(limit int) ([]FuturesNews, error) {
	q := `SELECT provider,news_id,title,summary,url,media,ts,published_at,tags
FROM futures_news ORDER BY ts DESC, news_id DESC`
	var args []any
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []FuturesNews{}
	for rows.Next() {
		var it FuturesNews
		var tags string
		if err := rows.Scan(&it.Provider, &it.ID, &it.Title, &it.Summary, &it.URL, &it.Media,
			&it.TS, &it.PublishedAt, &tags); err != nil {
			return nil, err
		}
		if strings.TrimSpace(tags) != "" {
			_ = json.Unmarshal([]byte(tags), &it.Tags)
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// FuturesNewsPrune 只留最近 keep 条，避免这个表无限长（新闻只关心最新的）。
func (s *Store) FuturesNewsPrune(keep int) (int64, error) {
	if keep <= 0 {
		return 0, nil
	}
	res, err := s.db.Exec(`
DELETE FROM futures_news WHERE rowid IN (
	SELECT rowid FROM futures_news ORDER BY ts DESC, news_id DESC LIMIT -1 OFFSET ?
)`, keep)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return n, nil
}
