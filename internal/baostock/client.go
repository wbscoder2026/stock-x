package baostock

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

const dialTimeout = 15 * time.Second

// Client 是 baostock TCP 会话：自管连接，读写串行。
type Client struct {
	mu     sync.Mutex
	conn   net.Conn
	userID string
}

// Dial 连接行情服务。addr 为空则用 DefaultAddr；拨号超时 15 秒。
func Dial(ctx context.Context, addr string) (*Client, error) {
	if addr == "" {
		addr = DefaultAddr
	}
	d := net.Dialer{Timeout: dialTimeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("baostock: 连接失败: %w", err)
	}
	return &Client{conn: conn, userID: defaultUser}, nil
}

// Login 匿名登录，成功后记住 user_id。
func (c *Client) Login(ctx context.Context) error {
	body, err := c.roundTrip(ctx, msgLoginReq, joinFields("login", defaultUser, defaultPass, defaultOpt))
	if err != nil {
		return err
	}
	if err := replyOK(body); err != nil {
		return err
	}
	uid := defaultUser
	if len(body) > 3 && body[3] != "" {
		uid = body[3]
	}
	c.mu.Lock()
	c.userID = uid
	c.mu.Unlock()
	return nil
}

// Close 尽量先发 logout，再关连接。
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return nil
	}
	uid := c.userID
	if uid == "" {
		uid = defaultUser
	}
	_ = c.conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
	_, _ = c.conn.Write(encodeFrame(msgLogoutReq, joinFields("logout", uid)))
	err := c.conn.Close()
	c.conn = nil
	return err
}

func (c *Client) roundTrip(ctx context.Context, msgType, body string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return nil, fmt.Errorf("baostock: 未连接")
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = c.conn.SetDeadline(dl)
		defer c.conn.SetDeadline(time.Time{})
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = c.conn.SetDeadline(time.Now())
		case <-done:
		}
	}()
	if _, err := c.conn.Write(encodeFrame(msgType, body)); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("baostock: 写失败: %w", err)
	}
	raw, err := readUntilSuffix(c.conn)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("baostock: 读失败: %w", err)
	}
	_, fields, err := parseFrame(raw)
	return fields, err
}

// QueryStockBasic 拉取证券基本资料，只保留 type=1 且 status=1 的 A 股。
func (c *Client) QueryStockBasic(ctx context.Context, code, codeName string) ([]BasicStock, error) {
	uid := c.currentUser()
	req := joinFields("query_stock_basic", uid, "1", strconv.Itoa(perPageCount), code, codeName)
	rows, err := c.pagedRecords(ctx, msgBasicReq, req)
	if err != nil {
		return nil, err
	}
	out := make([]BasicStock, 0, len(rows))
	for _, row := range rows {
		if len(row) < 6 {
			continue
		}
		if row[4] != "1" || row[5] != "1" {
			continue
		}
		out = append(out, BasicStock{Code: row[0], Name: row[1], Type: row[4], Status: row[5]})
	}
	return out, nil
}

// StockBasics 全市场上市 A 股列表（QueryStockBasic 的无过滤封装）。
func (c *Client) StockBasics(ctx context.Context) ([]BasicStock, error) {
	return c.QueryStockBasic(ctx, "", "")
}

// HistoryK 查询日 K（已 Login 的会话）。adjust 空则后复权 "1"。
func (c *Client) HistoryK(ctx context.Context, code, start, end, adjust string) ([]KBar, error) {
	if adjust == "" {
		adjust = "1"
	}
	code = ToBSCode(code)
	uid := c.currentUser()
	req := joinFields("query_history_k_data_plus", uid, "1", strconv.Itoa(perPageCount),
		code, kFields, start, end, "d", adjust)
	rows, err := c.pagedRecords(ctx, msgKPlusReq, req)
	if err != nil {
		return nil, err
	}
	idx := fieldIndex(kFields)
	out := make([]KBar, 0, len(rows))
	for _, row := range rows {
		closeStr := fieldAt(row, idx, "close")
		cl, ok := parseClose(closeStr)
		if !ok {
			continue
		}
		vol := parseFloat(fieldAt(row, idx, "volume"))
		if vol <= 0 {
			continue
		}
		out = append(out, KBar{
			Date:   fieldAt(row, idx, "date"),
			Open:   parseFloat(fieldAt(row, idx, "open")),
			High:   parseFloat(fieldAt(row, idx, "high")),
			Low:    parseFloat(fieldAt(row, idx, "low")),
			Close:  cl,
			Volume: vol,
			Amount: parseFloat(fieldAt(row, idx, "amount")),
			Turn:   parseFloat(fieldAt(row, idx, "turn")),
		})
	}
	return out, nil
}

func (c *Client) currentUser() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.userID == "" {
		return defaultUser
	}
	return c.userID
}

func (c *Client) pagedRecords(ctx context.Context, msgType, reqBody string) ([][]string, error) {
	var all [][]string
	body := reqBody
	for {
		fields, err := c.roundTrip(ctx, msgType, body)
		if err != nil {
			return nil, err
		}
		if err := replyOK(fields); err != nil {
			return nil, err
		}
		recs, err := decodeRecords(fields)
		if err != nil {
			return nil, err
		}
		all = append(all, recs...)
		if len(recs) != perPageCount {
			return all, nil
		}
		next, ok := nextPageBody(body)
		if !ok {
			return all, nil
		}
		body = next
	}
}

func decodeRecords(body []string) ([][]string, error) {
	if len(body) < 7 {
		return nil, fmt.Errorf("baostock: 响应缺少 JSON")
	}
	raw := compactJSON(body[6])
	if raw == "" || raw == "null" {
		return nil, nil
	}
	var payload struct {
		Record [][]string `json:"record"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, fmt.Errorf("baostock: JSON 解析失败: %w", err)
	}
	return payload.Record, nil
}

func replyOK(body []string) error {
	if len(body) > 0 && body[0] == "0" {
		return nil
	}
	code, msg := "", "请求失败"
	if len(body) > 0 {
		code = body[0]
	}
	if len(body) > 1 && body[1] != "" {
		msg = body[1]
	}
	if code == "10001011" || strings.Contains(msg, "黑名单") {
		return fmt.Errorf("%w（%s）", ErrBlacklisted, msg)
	}
	return fmt.Errorf("baostock: %s", msg)
}

func joinFields(parts ...string) string {
	return strings.Join(parts, fieldSep)
}

func fieldIndex(fields string) map[string]int {
	names := strings.Split(fields, ",")
	idx := make(map[string]int, len(names))
	for i, n := range names {
		idx[n] = i
	}
	return idx
}

func fieldAt(row []string, idx map[string]int, name string) string {
	i, ok := idx[name]
	if !ok || i >= len(row) {
		return ""
	}
	return row[i]
}

func parseClose(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func readUntilSuffix(r io.Reader) ([]byte, error) {
	var buf bytes.Buffer
	tmp := make([]byte, 32*1024)
	suf := []byte(cdataSuffix)
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			buf.Write(tmp[:n])
			if i := bytes.Index(buf.Bytes(), suf); i >= 0 {
				return buf.Bytes()[:i+len(suf)], nil
			}
			if buf.Len() > 64<<20 {
				return nil, fmt.Errorf("响应过大")
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil, fmt.Errorf("响应不完整")
			}
			return nil, err
		}
	}
}
