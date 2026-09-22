package futures

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultMinuteURL = "https://stock2.finance.sina.com.cn/futures/api/jsonp.php/=/InnerFuturesNewService.getFewMinLine"
	defaultDailyURL  = "https://stock2.finance.sina.com.cn/futures/api/jsonp.php/var%20_x=/InnerFuturesNewService.getDailyKLine"
)

type Client struct {
	HTTP      *http.Client
	MinuteURL string
	DailyURL  string
	HQURL     string
}

func (c *Client) http() *http.Client {
	if c != nil && c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 20 * time.Second}
}

func (c *Client) Minute(ctx context.Context, symbol, period string) ([]Bar, error) {
	u := defaultMinuteURL
	if c != nil && c.MinuteURL != "" {
		u = c.MinuteURL
	}
	q := url.Values{"symbol": {symbol}, "type": {period}}
	body, err := c.get(ctx, u+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	return parseMinuteJSONP(body)
}

func (c *Client) Daily(ctx context.Context, symbol string) ([]Daily, error) {
	u := defaultDailyURL
	if c != nil && c.DailyURL != "" {
		u = c.DailyURL
	}
	q := url.Values{"symbol": {symbol}}
	body, err := c.get(ctx, u+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	return parseDailyJSONP(body)
}

func (c *Client) Scan(ctx context.Context, p Params) (Snapshot, error) {
	p = mergeParams(p)
	min5, err := c.Minute(ctx, p.Symbol, "5")
	if err != nil {
		return Snapshot{}, fmt.Errorf("5分钟: %w", err)
	}
	min15, err := c.Minute(ctx, p.Symbol, "15")
	if err != nil {
		return Snapshot{}, fmt.Errorf("15分钟: %w", err)
	}
	daily, err := c.Daily(ctx, p.Symbol)
	if err != nil {
		return Snapshot{}, fmt.Errorf("日线: %w", err)
	}
	return ScanBars(min5, min15, daily, p), nil
}

func (c *Client) Backtest(ctx context.Context, p Params) (Result, error) {
	p = mergeParams(p)
	min, err := c.Minute(ctx, p.Symbol, p.Period)
	if err != nil {
		return Result{}, fmt.Errorf("%s分钟: %w", p.Period, err)
	}
	daily, err := c.Daily(ctx, p.Symbol)
	if err != nil {
		return Result{}, fmt.Errorf("日线: %w", err)
	}
	return BacktestBars(min, daily, p), nil
}

func (c *Client) get(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Referer", "https://finance.sina.com.cn")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36")
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return b, nil
}

func parseJSONP(b []byte) ([]byte, error) {
	s := strings.TrimSpace(string(b))
	i := strings.Index(s, "(")
	j := strings.LastIndex(s, ")")
	if i < 0 || j <= i {
		return nil, fmt.Errorf("无效 jsonp")
	}
	inner := strings.TrimSpace(s[i+1 : j])
	if inner == "" || inner == "null" {
		return nil, fmt.Errorf("空数据")
	}
	return []byte(inner), nil
}

func parseMinuteJSONP(b []byte) ([]Bar, error) {
	raw, err := parseJSONP(b)
	if err != nil {
		return nil, err
	}
	rows, err := parseOHLCRows(raw)
	if err != nil {
		return nil, err
	}
	out := make([]Bar, 0, len(rows))
	for _, r := range rows {
		ts, err := parseTime(r.D)
		if err != nil {
			continue
		}
		out = append(out, Bar{
			Time: ts, Open: parseFloatStr(r.O), High: parseFloatStr(r.H), Low: parseFloatStr(r.L),
			Close: parseFloatStr(r.C), Volume: parseFloatStr(r.V), Hold: parseFloatStr(r.P),
		})
	}
	return out, nil
}

func parseDailyJSONP(b []byte) ([]Daily, error) {
	raw, err := parseJSONP(b)
	if err != nil {
		return nil, err
	}
	rows, err := parseOHLCRows(raw)
	if err != nil {
		return nil, err
	}
	out := make([]Daily, 0, len(rows))
	for _, r := range rows {
		d, err := time.ParseInLocation("2006-01-02", r.D, locCST)
		if err != nil {
			continue
		}
		out = append(out, Daily{
			Date: d, Open: parseFloatStr(r.O), High: parseFloatStr(r.H), Low: parseFloatStr(r.L),
			Close: parseFloatStr(r.C), Volume: parseFloatStr(r.V), Hold: parseFloatStr(r.P), Settle: parseFloatStr(r.S),
		})
	}
	return out, nil
}

type ohlcRow struct {
	D, O, H, L, C, V, P, S string
}

func parseOHLCRows(raw []byte) ([]ohlcRow, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	out := make([]ohlcRow, 0, len(items))
	for _, item := range items {
		s := strings.TrimSpace(string(item))
		if s == "" {
			continue
		}
		if s[0] == '{' {
			var obj struct {
				D json.RawMessage `json:"d"`
				O json.RawMessage `json:"o"`
				H json.RawMessage `json:"h"`
				L json.RawMessage `json:"l"`
				C json.RawMessage `json:"c"`
				V json.RawMessage `json:"v"`
				P json.RawMessage `json:"p"`
				S json.RawMessage `json:"s"`
			}
			if err := json.Unmarshal(item, &obj); err != nil {
				continue
			}
			out = append(out, ohlcRow{
				D: rawString(obj.D), O: rawString(obj.O), H: rawString(obj.H), L: rawString(obj.L),
				C: rawString(obj.C), V: rawString(obj.V), P: rawString(obj.P), S: rawString(obj.S),
			})
			continue
		}
		var arr []json.RawMessage
		if err := json.Unmarshal(item, &arr); err != nil || len(arr) < 5 {
			continue
		}
		out = append(out, ohlcRow{
			D: rawString(arr[0]), O: rawString(arr[1]), H: rawString(arr[2]), L: rawString(arr[3]),
			C: rawString(arr[4]), V: rawStringAt(arr, 5), P: rawStringAt(arr, 6), S: rawStringAt(arr, 7),
		})
	}
	return out, nil
}

func parseTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if t, err := time.ParseInLocation("2006-01-02 15:04:05", s, locCST); err == nil {
		return t, nil
	}
	return time.ParseInLocation("2006-01-02 15:04", s, locCST)
}

func rawString(r json.RawMessage) string {
	var s string
	if json.Unmarshal(r, &s) == nil {
		return s
	}
	return strings.Trim(string(r), `"`)
}

func rawFloat(r json.RawMessage) float64 {
	if s := rawString(r); s != "" {
		f, _ := strconv.ParseFloat(s, 64)
		return f
	}
	return 0
}

func rawFloatAt(r []json.RawMessage, i int) float64 {
	if i >= len(r) {
		return 0
	}
	return rawFloat(r[i])
}

func rawStringAt(r []json.RawMessage, i int) string {
	if i >= len(r) {
		return ""
	}
	return rawString(r[i])
}
