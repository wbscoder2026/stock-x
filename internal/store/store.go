package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Bar struct {
	Symbol, Date                                   string
	Open, High, Low, Close, Volume, Turnover, Turn float64
}

type Stock struct {
	Symbol, Name, Market string
	Listed               bool
}

type StrategyConfig struct {
	ID, Name, Description  string
	Enabled                bool
	ParamsJSON, WebhookURL string
}

type ScanPick struct {
	RunID                             int64
	Strategy, Symbol, Name, ExtraJSON string
	AsOf                              string
}

type JobRun struct {
	ID, Type, Status, Log string
	Progress              int
	StartedAt, FinishedAt string
}

type KlineCoverage struct {
	Symbol, Name, Market string
	StartDate, EndDate   string
	Bars                 int
}

type Store struct {
	db *sql.DB
}

// Open 打开 SQLite（WAL + busy_timeout），并确保表存在。
func Open(path string) (*Store, error) {
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(15000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)", filepath.ToSlash(path))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	if _, err := db.Exec(`PRAGMA busy_timeout = 15000; PRAGMA journal_mode = WAL; PRAGMA synchronous = NORMAL;`); err != nil {
		_ = db.Close()
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS stock_info (
	symbol TEXT PRIMARY KEY,
	name TEXT,
	market TEXT,
	listed INTEGER
);
CREATE TABLE IF NOT EXISTS stock_daily (
	symbol TEXT NOT NULL,
	date TEXT NOT NULL,
	open REAL,
	high REAL,
	low REAL,
	close REAL,
	volume REAL,
	turnover REAL,
	turn REAL,
	UNIQUE(symbol, date)
);
CREATE INDEX IF NOT EXISTS idx_stock_daily_symbol_date ON stock_daily(symbol, date);
CREATE TABLE IF NOT EXISTS strategy_config (
	id TEXT PRIMARY KEY,
	name TEXT,
	description TEXT,
	enabled INTEGER,
	params_json TEXT,
	webhook_url TEXT
);
CREATE TABLE IF NOT EXISTS scan_run (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	as_of TEXT,
	status TEXT,
	started_at TEXT,
	finished_at TEXT
);
CREATE TABLE IF NOT EXISTS scan_pick (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	run_id INTEGER,
	strategy TEXT,
	symbol TEXT,
	name TEXT,
	extra_json TEXT
);
CREATE INDEX IF NOT EXISTS idx_scan_pick_symbol ON scan_pick(symbol);
CREATE TABLE IF NOT EXISTS job_run (
	id TEXT PRIMARY KEY,
	type TEXT,
	status TEXT,
	progress INTEGER,
	log TEXT,
	started_at TEXT,
	finished_at TEXT
);
CREATE TABLE IF NOT EXISTS app_meta (
	key TEXT PRIMARY KEY,
	value TEXT
);
`
	_, err := s.db.Exec(ddl)
	return err
}

func (s *Store) UpsertStocks(stocks []Stock) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO stock_info(symbol,name,market,listed) VALUES(?,?,?,?)
		ON CONFLICT(symbol) DO UPDATE SET name=excluded.name, market=excluded.market, listed=excluded.listed`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, st := range stocks {
		listed := 0
		if st.Listed {
			listed = 1
		}
		if _, err := stmt.Exec(st.Symbol, st.Name, st.Market, listed); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ListStocks(q string, limit int) ([]Stock, error) {
	q = strings.TrimSpace(q)
	args := []any{}
	sqlStr := `SELECT symbol, name, market, listed FROM stock_info`
	if q != "" {
		sqlStr += ` WHERE symbol LIKE ? OR name LIKE ?`
		like := "%" + q + "%"
		args = append(args, like, like)
	}
	sqlStr += ` ORDER BY symbol`
	if limit > 0 {
		sqlStr += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.Query(sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanStocks(rows)
}

func (s *Store) GetStock(symbol string) (Stock, bool, error) {
	var st Stock
	var listed int
	err := s.db.QueryRow(`SELECT symbol, name, market, listed FROM stock_info WHERE symbol = ?`, symbol).
		Scan(&st.Symbol, &st.Name, &st.Market, &listed)
	if err == sql.ErrNoRows {
		return Stock{}, false, nil
	}
	if err != nil {
		return Stock{}, false, err
	}
	st.Listed = listed != 0
	return st, true, nil
}

func (s *Store) UpsertBars(bars []Bar) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT OR REPLACE INTO stock_daily(symbol,date,open,high,low,close,volume,turnover,turn)
		VALUES(?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, b := range bars {
		if _, err := stmt.Exec(b.Symbol, b.Date, b.Open, b.High, b.Low, b.Close, b.Volume, b.Turnover, b.Turn); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) LastDate(symbol string) (string, bool, error) {
	var d sql.NullString
	err := s.db.QueryRow(`SELECT MAX(date) FROM stock_daily WHERE symbol = ?`, symbol).Scan(&d)
	if err != nil {
		return "", false, err
	}
	if !d.Valid || d.String == "" {
		return "", false, nil
	}
	return d.String, true, nil
}

func (s *Store) ListKlineCoverage(q string, offset, limit int) ([]KlineCoverage, int, error) {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	q = strings.TrimSpace(q)
	where := `WHERE 1=1`
	args := []any{}
	if q != "" {
		where += ` AND (s.symbol LIKE ? OR s.name LIKE ?)`
		like := "%" + q + "%"
		args = append(args, like, like)
	}
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM stock_info s `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	listSQL := `SELECT s.symbol, s.name, s.market, MIN(d.date), MAX(d.date), COUNT(d.date)
		FROM stock_info s
		LEFT JOIN stock_daily d ON d.symbol = s.symbol
		` + where + `
		GROUP BY s.symbol, s.name, s.market
		ORDER BY s.symbol
		LIMIT ? OFFSET ?`
	listArgs := append(append([]any{}, args...), limit, offset)
	rows, err := s.db.Query(listSQL, listArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []KlineCoverage
	for rows.Next() {
		var c KlineCoverage
		var start, end sql.NullString
		if err := rows.Scan(&c.Symbol, &c.Name, &c.Market, &start, &end, &c.Bars); err != nil {
			return nil, 0, err
		}
		if start.Valid {
			c.StartDate = start.String
		}
		if end.Valid {
			c.EndDate = end.String
		}
		out = append(out, c)
	}
	return out, total, rows.Err()
}

func (s *Store) LastDates() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT symbol, MAX(date) FROM stock_daily GROUP BY symbol`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var sym, d string
		if err := rows.Scan(&sym, &d); err != nil {
			return nil, err
		}
		out[sym] = d
	}
	return out, rows.Err()
}

func (s *Store) LocalSymbols() ([]string, error) {
	rows, err := s.db.Query(`SELECT symbol FROM stock_info ORDER BY symbol`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var sym string
		if err := rows.Scan(&sym); err != nil {
			return nil, err
		}
		out = append(out, sym)
	}
	return out, rows.Err()
}

func (s *Store) OHLCV(symbol, asOf string) ([]Bar, error) {
	var rows *sql.Rows
	var err error
	if strings.TrimSpace(asOf) == "" {
		rows, err = s.db.Query(`SELECT symbol,date,open,high,low,close,volume,turnover,turn FROM stock_daily
			WHERE symbol = ? ORDER BY date`, symbol)
	} else {
		rows, err = s.db.Query(`SELECT symbol,date,open,high,low,close,volume,turnover,turn FROM stock_daily
			WHERE symbol = ? AND date <= ? ORDER BY date`, symbol, asOf)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanBars(rows)
}

func (s *Store) Kline(symbol, start, end string) ([]Bar, error) {
	q := `SELECT symbol,date,open,high,low,close,volume,turnover,turn FROM stock_daily WHERE symbol = ?`
	args := []any{symbol}
	if start != "" {
		q += ` AND date >= ?`
		args = append(args, start)
	}
	if end != "" {
		q += ` AND date <= ?`
		args = append(args, end)
	}
	q += ` ORDER BY date`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanBars(rows)
}

func (s *Store) LoadMarketAsOf(asOf string) (map[string][]Bar, error) {
	var rows *sql.Rows
	var err error
	if strings.TrimSpace(asOf) == "" {
		rows, err = s.db.Query(`SELECT symbol,date,open,high,low,close,volume,turnover,turn FROM stock_daily
			ORDER BY symbol, date`)
	} else {
		rows, err = s.db.Query(`SELECT symbol,date,open,high,low,close,volume,turnover,turn FROM stock_daily
			WHERE date <= ? ORDER BY symbol, date`, asOf)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]Bar{}
	for rows.Next() {
		b, err := scanBar(rows)
		if err != nil {
			return nil, err
		}
		out[b.Symbol] = append(out[b.Symbol], b)
	}
	return out, rows.Err()
}

func (s *Store) SeedStrategies(defaults []StrategyConfig) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO strategy_config(id,name,description,enabled,params_json,webhook_url)
		VALUES(?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, c := range defaults {
		en := 0
		if c.Enabled {
			en = 1
		}
		if _, err := stmt.Exec(c.ID, c.Name, c.Description, en, c.ParamsJSON, c.WebhookURL); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ListStrategyConfigs() ([]StrategyConfig, error) {
	rows, err := s.db.Query(`SELECT id,name,description,enabled,params_json,webhook_url FROM strategy_config ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StrategyConfig
	for rows.Next() {
		c, err := scanStrategy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) GetStrategyConfig(id string) (StrategyConfig, error) {
	row := s.db.QueryRow(`SELECT id,name,description,enabled,params_json,webhook_url FROM strategy_config WHERE id = ?`, id)
	return scanStrategy(row)
}

func (s *Store) UpdateStrategyConfig(id string, enabled bool, paramsJSON, webhook string) error {
	en := 0
	if enabled {
		en = 1
	}
	res, err := s.db.Exec(`UPDATE strategy_config SET enabled=?, params_json=?, webhook_url=? WHERE id=?`,
		en, paramsJSON, webhook, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) InsertScanRun(asOf string) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO scan_run(as_of,status,started_at) VALUES(?,?,?)`,
		asOf, "running", time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) FinishScanRun(id int64, status string) error {
	_, err := s.db.Exec(`UPDATE scan_run SET status=?, finished_at=? WHERE id=?`,
		status, time.Now().UTC().Format(time.RFC3339), id)
	return err
}

func (s *Store) InsertPicks(picks []ScanPick) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO scan_pick(run_id,strategy,symbol,name,extra_json) VALUES(?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, p := range picks {
		if _, err := stmt.Exec(p.RunID, p.Strategy, p.Symbol, p.Name, p.ExtraJSON); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ListPicks(date, strategy string) ([]ScanPick, error) {
	if strings.TrimSpace(date) == "" {
		return s.ListLatestPicks(strategy)
	}
	return s.ListPicksRange(date, date, strategy)
}

func queryPicks(s *Store, q string, args []any) ([]ScanPick, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScanPick
	for rows.Next() {
		var p ScanPick
		if err := rows.Scan(&p.RunID, &p.Strategy, &p.Symbol, &p.Name, &p.ExtraJSON, &p.AsOf); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

const pickJoinSQL = `SELECT p.run_id, p.strategy, p.symbol, p.name, p.extra_json, r.as_of
	FROM scan_pick p JOIN scan_run r ON r.id = p.run_id
	WHERE r.status = 'success'`

// ListLatestPicks 取最近一次 status=success 的 scan_run 下的选股。
func (s *Store) ListLatestPicks(strategy string) ([]ScanPick, error) {
	var runID int64
	err := s.db.QueryRow(`SELECT id FROM scan_run WHERE status = 'success' ORDER BY as_of DESC, id DESC LIMIT 1`).Scan(&runID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	q := pickJoinSQL + ` AND p.run_id = ?`
	args := []any{runID}
	if strings.TrimSpace(strategy) != "" {
		q += ` AND p.strategy = ?`
		args = append(args, strategy)
	}
	q += ` ORDER BY p.id`
	return queryPicks(s, q, args)
}

// ListPicksBySymbol 某只股票全部成功扫描的选股。
func (s *Store) ListPicksBySymbol(symbol, strategy string) ([]ScanPick, error) {
	q := pickJoinSQL + ` AND p.symbol = ?`
	args := []any{symbol}
	if strings.TrimSpace(strategy) != "" {
		q += ` AND p.strategy = ?`
		args = append(args, strategy)
	}
	q += ` ORDER BY r.as_of, p.id`
	return queryPicks(s, q, args)
}

// ListPicksRange 列出 [from,to] 内成功扫描的选股（含 as_of）。
func (s *Store) ListPicksRange(from, to, strategy string) ([]ScanPick, error) {
	q := pickJoinSQL
	args := []any{}
	if strings.TrimSpace(from) != "" {
		q += ` AND r.as_of >= ?`
		args = append(args, from)
	}
	if strings.TrimSpace(to) != "" {
		q += ` AND r.as_of <= ?`
		args = append(args, to)
	}
	if strings.TrimSpace(strategy) != "" {
		q += ` AND p.strategy = ?`
		args = append(args, strategy)
	}
	q += ` ORDER BY r.as_of, p.id`
	return queryPicks(s, q, args)
}

// MaxBarDate 返回库内 K 线最大日期。
func (s *Store) MaxBarDate() (string, bool, error) {
	var d sql.NullString
	err := s.db.QueryRow(`SELECT MAX(date) FROM stock_daily`).Scan(&d)
	if err != nil {
		return "", false, err
	}
	if !d.Valid || d.String == "" {
		return "", false, nil
	}
	return d.String, true, nil
}

// DataStats 本地行情规模，给前端空库提示用。
func (s *Store) DataStats() (stocks, bars int, maxDate string, err error) {
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM stock_info`).Scan(&stocks); err != nil {
		return
	}
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM stock_daily`).Scan(&bars); err != nil {
		return
	}
	maxDate, _, err = s.MaxBarDate()
	return
}

// TradingDays 返回 [from,to] 内出现过行情的交易日（升序）。
func (s *Store) TradingDays(from, to string) ([]string, error) {
	q := `SELECT DISTINCT date FROM stock_daily WHERE 1=1`
	args := []any{}
	if strings.TrimSpace(from) != "" {
		q += ` AND date >= ?`
		args = append(args, from)
	}
	if strings.TrimSpace(to) != "" {
		q += ` AND date <= ?`
		args = append(args, to)
	}
	q += ` ORDER BY date`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) InsertJob(j JobRun) error {
	_, err := s.db.Exec(`INSERT INTO job_run(id,type,status,progress,log,started_at,finished_at) VALUES(?,?,?,?,?,?,?)`,
		j.ID, j.Type, j.Status, j.Progress, j.Log, j.StartedAt, j.FinishedAt)
	return err
}

func (s *Store) UpdateJob(j JobRun) error {
	_, err := s.db.Exec(`UPDATE job_run SET type=?, status=?, progress=?, log=?, started_at=?, finished_at=? WHERE id=?`,
		j.Type, j.Status, j.Progress, j.Log, j.StartedAt, j.FinishedAt, j.ID)
	return err
}

func (s *Store) GetJob(id string) (JobRun, error) {
	var j JobRun
	err := s.db.QueryRow(`SELECT id,type,status,progress,log,started_at,finished_at FROM job_run WHERE id=?`, id).
		Scan(&j.ID, &j.Type, &j.Status, &j.Progress, &j.Log, &j.StartedAt, &j.FinishedAt)
	return j, err
}

func (s *Store) ListJobs(limit int) ([]JobRun, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT id,type,status,progress,log,started_at,finished_at FROM job_run
		ORDER BY started_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []JobRun
	for rows.Next() {
		var j JobRun
		if err := rows.Scan(&j.ID, &j.Type, &j.Status, &j.Progress, &j.Log, &j.StartedAt, &j.FinishedAt); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

func (s *Store) GetMeta(key string) (string, bool, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM app_meta WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

func (s *Store) SetMeta(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO app_meta(key,value) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

func scanStocks(rows *sql.Rows) ([]Stock, error) {
	var out []Stock
	for rows.Next() {
		var st Stock
		var listed int
		if err := rows.Scan(&st.Symbol, &st.Name, &st.Market, &listed); err != nil {
			return nil, err
		}
		st.Listed = listed != 0
		out = append(out, st)
	}
	return out, rows.Err()
}

func scanBars(rows *sql.Rows) ([]Bar, error) {
	var out []Bar
	for rows.Next() {
		b, err := scanBar(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

type barScanner interface {
	Scan(dest ...any) error
}

func scanBar(sc barScanner) (Bar, error) {
	var b Bar
	err := sc.Scan(&b.Symbol, &b.Date, &b.Open, &b.High, &b.Low, &b.Close, &b.Volume, &b.Turnover, &b.Turn)
	return b, err
}

func scanStrategy(sc barScanner) (StrategyConfig, error) {
	var c StrategyConfig
	var en int
	err := sc.Scan(&c.ID, &c.Name, &c.Description, &en, &c.ParamsJSON, &c.WebhookURL)
	c.Enabled = en != 0
	return c, err
}
