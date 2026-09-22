package store

import (
	"database/sql"
	"errors"
	"time"
)

// 监控配置只存一行：页面上的参数/开关是「全局设置」，不是多用户设置。
// payload 存 JSON 原文，存储层不关心它的结构（由上层负责序列化）。
const futuresWatchConfigDDL = `
CREATE TABLE IF NOT EXISTS futures_watch_config (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	payload TEXT NOT NULL,
	updated_at TEXT NOT NULL
);`

// SaveFuturesWatchConfig 保存监控配置（覆盖单行）。payload 为 JSON 原文。
func (s *Store) SaveFuturesWatchConfig(payload []byte) error {
	_, err := s.db.Exec(
		`INSERT INTO futures_watch_config (id, payload, updated_at) VALUES (1, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET payload = excluded.payload, updated_at = excluded.updated_at`,
		string(payload), time.Now().UTC().Format(time.RFC3339),
	)
	return err
}

// LoadFuturesWatchConfig 读监控配置；没保存过返回 ok=false（调用方用默认值）。
func (s *Store) LoadFuturesWatchConfig() ([]byte, bool, error) {
	var payload string
	err := s.db.QueryRow(`SELECT payload FROM futures_watch_config WHERE id = 1`).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return []byte(payload), true, nil
}
