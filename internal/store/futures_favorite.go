package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// FuturesFavorite 一条收藏的期货扫描配置（一组固定参数，不是整张扫描网格）。
type FuturesFavorite struct {
	ID                   int64
	Name                 string
	Note                 string
	ParamsJSON           string
	OriginSymbol         string
	OriginWinRate        float64
	OriginAvgReturn      float64
	OriginAvgR           float64
	OriginProfitFactor   float64
	OriginTrades         int
	CreatedAt            string
	UpdatedAt            string
}

const futuresFavoriteDDL = `
CREATE TABLE IF NOT EXISTS futures_favorite (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL,
	note TEXT,
	params_json TEXT NOT NULL,
	origin_symbol TEXT,
	origin_win_rate REAL,
	origin_avg_return REAL,
	origin_avg_r REAL,
	origin_profit_factor REAL,
	origin_trades INTEGER,
	created_at TEXT,
	updated_at TEXT
);
`

func (s *Store) CreateFuturesFavorite(f FuturesFavorite) (FuturesFavorite, error) {
	if err := validateFavorite(f); err != nil {
		return FuturesFavorite{}, err
	}
	now := time.Now().Format(time.RFC3339)
	res, err := s.db.Exec(`
INSERT INTO futures_favorite(name,note,params_json,origin_symbol,origin_win_rate,origin_avg_return,origin_avg_r,origin_profit_factor,origin_trades,created_at,updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		strings.TrimSpace(f.Name), strings.TrimSpace(f.Note), f.ParamsJSON, f.OriginSymbol,
		f.OriginWinRate, f.OriginAvgReturn, f.OriginAvgR, f.OriginProfitFactor, f.OriginTrades,
		now, now,
	)
	if err != nil {
		return FuturesFavorite{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return FuturesFavorite{}, err
	}
	return s.GetFuturesFavorite(id)
}

func (s *Store) GetFuturesFavorite(id int64) (FuturesFavorite, error) {
	row := s.db.QueryRow(`
SELECT id,name,note,params_json,origin_symbol,origin_win_rate,origin_avg_return,origin_avg_r,origin_profit_factor,origin_trades,created_at,updated_at
FROM futures_favorite WHERE id = ?`, id)
	f, err := scanFavorite(row)
	if err == sql.ErrNoRows {
		return FuturesFavorite{}, fmt.Errorf("收藏不存在")
	}
	return f, err
}

func (s *Store) ListFuturesFavorites() ([]FuturesFavorite, error) {
	rows, err := s.db.Query(`
SELECT id,name,note,params_json,origin_symbol,origin_win_rate,origin_avg_return,origin_avg_r,origin_profit_factor,origin_trades,created_at,updated_at
FROM futures_favorite ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FuturesFavorite{}
	for rows.Next() {
		f, err := scanFavorite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Store) UpdateFuturesFavorite(f FuturesFavorite) (FuturesFavorite, error) {
	if f.ID <= 0 {
		return FuturesFavorite{}, fmt.Errorf("缺少收藏 id")
	}
	if err := validateFavorite(f); err != nil {
		return FuturesFavorite{}, err
	}
	now := time.Now().Format(time.RFC3339)
	res, err := s.db.Exec(`
UPDATE futures_favorite
SET name=?, note=?, params_json=?, updated_at=?
WHERE id=?`,
		strings.TrimSpace(f.Name), strings.TrimSpace(f.Note), f.ParamsJSON, now, f.ID,
	)
	if err != nil {
		return FuturesFavorite{}, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return FuturesFavorite{}, fmt.Errorf("收藏不存在")
	}
	return s.GetFuturesFavorite(f.ID)
}

func (s *Store) DeleteFuturesFavorites(ids []int64) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	holders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		if id <= 0 {
			return 0, fmt.Errorf("非法收藏 id")
		}
		holders[i] = "?"
		args[i] = id
	}
	res, err := s.db.Exec(`DELETE FROM futures_favorite WHERE id IN (`+strings.Join(holders, ",")+`)`, args...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func validateFavorite(f FuturesFavorite) error {
	if strings.TrimSpace(f.Name) == "" {
		return fmt.Errorf("收藏名称不能为空")
	}
	if strings.TrimSpace(f.ParamsJSON) == "" {
		return fmt.Errorf("缺少参数")
	}
	return nil
}

type favoriteScanner interface {
	Scan(dest ...any) error
}

func scanFavorite(row favoriteScanner) (FuturesFavorite, error) {
	var f FuturesFavorite
	err := row.Scan(
		&f.ID, &f.Name, &f.Note, &f.ParamsJSON, &f.OriginSymbol,
		&f.OriginWinRate, &f.OriginAvgReturn, &f.OriginAvgR, &f.OriginProfitFactor, &f.OriginTrades,
		&f.CreatedAt, &f.UpdatedAt,
	)
	return f, err
}
