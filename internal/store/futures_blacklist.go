package store

import "time"

// 黑名单作用域
const (
	BlacklistScopeVariety  = "variety"  // 整个品种（如 JM，含所有合约）
	BlacklistScopeContract = "contract" // 单个合约（如 JM2701）
)

// FuturesBlacklistEntry 监控黑名单条目
type FuturesBlacklistEntry struct {
	Scope     string `json:"scope"`
	Value     string `json:"value"`
	Note      string `json:"note"`
	CreatedAt string `json:"created_at"`
}

const futuresBlacklistDDL = `
CREATE TABLE IF NOT EXISTS futures_blacklist (
	scope TEXT NOT NULL,
	value TEXT NOT NULL,
	note TEXT,
	created_at TEXT,
	PRIMARY KEY(scope, value)
);
`

// AddFuturesBlacklist 加入黑名单（同 scope+value 覆盖 note）。
func (s *Store) AddFuturesBlacklist(scope, value, note string) error {
	_, err := s.db.Exec(`
INSERT INTO futures_blacklist(scope,value,note,created_at) VALUES(?,?,?,?)
ON CONFLICT(scope,value) DO UPDATE SET note=excluded.note`,
		scope, value, note, time.Now().UTC().Format(time.RFC3339))
	return err
}

// RemoveFuturesBlacklist 移出黑名单，返回删除条数。
func (s *Store) RemoveFuturesBlacklist(scope, value string) (int, error) {
	res, err := s.db.Exec(`DELETE FROM futures_blacklist WHERE scope = ? AND value = ?`, scope, value)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// ListFuturesBlacklist 列出全部黑名单（合约在前，便于阅读）。
func (s *Store) ListFuturesBlacklist() ([]FuturesBlacklistEntry, error) {
	rows, err := s.db.Query(`
SELECT scope, value, COALESCE(note,''), COALESCE(created_at,'')
FROM futures_blacklist ORDER BY scope, value`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FuturesBlacklistEntry{}
	for rows.Next() {
		var e FuturesBlacklistEntry
		if err := rows.Scan(&e.Scope, &e.Value, &e.Note, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
