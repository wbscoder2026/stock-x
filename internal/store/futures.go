package store

import (
	"database/sql"
	"time"
)

// FuturesBar 期货 K 线（分钟线 period=5/15/30/60/120，日线 period=1d）。
// 时间统一按 UTC 秒存储，读写时按本地（CST）时区解释；日线约定钉在当日 15:00。
type FuturesBar struct {
	Symbol string
	Period string
	Time   time.Time
	Open   float64
	High   float64
	Low    float64
	Close  float64
	Volume float64
	Hold   float64
}

// FuturesCoverage 某个品种某个周期的本地覆盖情况。
type FuturesCoverage struct {
	Symbol string
	Period string
	Bars   int
	First  time.Time
	Last   time.Time
}

const futuresDDL = `
CREATE TABLE IF NOT EXISTS futures_bar (
	symbol TEXT NOT NULL,
	period TEXT NOT NULL,
	ts INTEGER NOT NULL,
	open REAL,
	high REAL,
	low REAL,
	close REAL,
	volume REAL,
	hold REAL,
	PRIMARY KEY(symbol, period, ts)
);
CREATE INDEX IF NOT EXISTS idx_futures_bar_period_ts ON futures_bar(period, ts);
`

// UpsertFuturesBars 批量写入（同一 symbol/period/ts 覆盖），返回写入条数。
func (s *Store) UpsertFuturesBars(bars []FuturesBar) (int, error) {
	if len(bars) == 0 {
		return 0, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	stmt, err := tx.Prepare(`
INSERT INTO futures_bar(symbol,period,ts,open,high,low,close,volume,hold)
VALUES(?,?,?,?,?,?,?,?,?)
ON CONFLICT(symbol,period,ts) DO UPDATE SET
	open=excluded.open, high=excluded.high, low=excluded.low, close=excluded.close,
	volume=excluded.volume, hold=excluded.hold`)
	if err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	defer stmt.Close()

	n := 0
	for _, b := range bars {
		if b.Symbol == "" || b.Period == "" {
			continue
		}
		if _, err := stmt.Exec(
			b.Symbol, b.Period, b.Time.Unix(),
			b.Open, b.High, b.Low, b.Close, b.Volume, b.Hold,
		); err != nil {
			_ = tx.Rollback()
			return n, err
		}
		n++
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

// FuturesBars 取某品种某周期的 K 线，升序返回；limit > 0 时只取最后 limit 根。
func (s *Store) FuturesBars(symbol, period string, limit int) ([]FuturesBar, error) {
	q := `SELECT symbol,period,ts,open,high,low,close,volume,hold FROM futures_bar WHERE symbol = ? AND period = ?`
	args := []any{symbol, period}
	if limit > 0 {
		q += ` ORDER BY ts DESC LIMIT ?`
		args = append(args, limit)
	} else {
		q += ` ORDER BY ts ASC`
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]FuturesBar, 0, 1024)
	for rows.Next() {
		var b FuturesBar
		var ts int64
		if err := rows.Scan(&b.Symbol, &b.Period, &ts, &b.Open, &b.High, &b.Low, &b.Close, &b.Volume, &b.Hold); err != nil {
			return nil, err
		}
		b.Time = time.Unix(ts, 0).UTC()
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if limit > 0 { // DESC 取回来后翻正
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	return out, nil
}

// FuturesRange 返回某品种某周期的首末时间与条数。
func (s *Store) FuturesRange(symbol, period string) (time.Time, time.Time, int, error) {
	var minTS, maxTS sql.NullInt64
	var n int
	err := s.db.QueryRow(
		`SELECT MIN(ts), MAX(ts), COUNT(*) FROM futures_bar WHERE symbol = ? AND period = ?`,
		symbol, period,
	).Scan(&minTS, &maxTS, &n)
	if err != nil {
		return time.Time{}, time.Time{}, 0, err
	}
	var first, last time.Time
	if minTS.Valid {
		first = time.Unix(minTS.Int64, 0).UTC()
	}
	if maxTS.Valid {
		last = time.Unix(maxTS.Int64, 0).UTC()
	}
	return first, last, n, nil
}

// FuturesLastTime 某品种某周期已存的最新时间（用于增量同步）。
func (s *Store) FuturesLastTime(symbol, period string) (time.Time, bool, error) {
	var ts sql.NullInt64
	err := s.db.QueryRow(
		`SELECT MAX(ts) FROM futures_bar WHERE symbol = ? AND period = ?`, symbol, period,
	).Scan(&ts)
	if err != nil || !ts.Valid {
		return time.Time{}, false, err
	}
	return time.Unix(ts.Int64, 0).UTC(), true, nil
}

// FuturesCoverage 全部品种/周期的覆盖情况（给 CLI 与前端看）。
// FuturesDayCount 某个合约某级别在「某一天」有多少根 K 线（按北京时间分组）。
type FuturesDayCount struct {
	Day  string `json:"day"` // 2006-01-02（北京时间）
	Bars int    `json:"bars"`
}

// FuturesCoverageDayCounts 一次拿全市场「每个 symbol+period 覆盖了多少天」。
// key 是 `symbol|period`。页面每 2 秒轮询一次，所以必须一次查完，不能逐品种问。
func (s *Store) FuturesCoverageDayCounts() (map[string]int, error) {
	rows, err := s.db.Query(`
SELECT symbol, period, COUNT(DISTINCT date(ts,'unixepoch','+8 hours'))
FROM futures_bar GROUP BY symbol, period`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var symbol, period string
		var days int
		if err := rows.Scan(&symbol, &period, &days); err != nil {
			return nil, err
		}
		out[symbol+"|"+period] = days
	}
	return out, rows.Err()
}

// FuturesCoverageDays 按天统计覆盖情况（升序）。
// 页面要回答「到底同步了哪些日期」—— 光看起止日期看不出中间有没有断档。
func (s *Store) FuturesCoverageDays(symbol, period string) ([]FuturesDayCount, error) {
	if symbol == "" || period == "" {
		return []FuturesDayCount{}, nil
	}
	rows, err := s.db.Query(`
SELECT date(ts,'unixepoch','+8 hours') AS d, COUNT(*) AS n
FROM futures_bar WHERE symbol=? AND period=?
GROUP BY d ORDER BY d`, symbol, period)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FuturesDayCount{}
	for rows.Next() {
		var item FuturesDayCount
		if err := rows.Scan(&item.Day, &item.Bars); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) FuturesCoverage() ([]FuturesCoverage, error) {
	rows, err := s.db.Query(`
SELECT symbol, period, COUNT(*) AS n, MIN(ts), MAX(ts)
FROM futures_bar GROUP BY symbol, period ORDER BY symbol, period`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FuturesCoverage{}
	for rows.Next() {
		var c FuturesCoverage
		var first, last int64
		if err := rows.Scan(&c.Symbol, &c.Period, &c.Bars, &first, &last); err != nil {
			return nil, err
		}
		c.First = time.Unix(first, 0).UTC()
		c.Last = time.Unix(last, 0).UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

// FuturesStats 本地期货行情规模。
func (s *Store) FuturesStats() (symbols, bars int, err error) {
	if err = s.db.QueryRow(`SELECT COUNT(DISTINCT symbol) FROM futures_bar`).Scan(&symbols); err != nil {
		return
	}
	err = s.db.QueryRow(`SELECT COUNT(*) FROM futures_bar`).Scan(&bars)
	return
}

// DeleteFuturesBars 清掉某品种某周期的本地数据（重灌时用）。
func (s *Store) DeleteFuturesBars(symbol, period string) (int, error) {
	res, err := s.db.Exec(`DELETE FROM futures_bar WHERE symbol = ? AND period = ?`, symbol, period)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
