package futures

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const defaultHQListURL = "https://hq.sinajs.cn/list="

// RealtimeTick 新浪实时行情（`hq.sinajs.cn/list=nf_XX0`）里我们用得上的字段。
//
// 字段布局**以真实返回为准**（踩过坑：之前按"名称后面就是开盘价"解析，整体错位一位，
// 结果把卖量当成持仓量、把昨收当成买一价 —— 数字都有，全都错）。
// 真实行形如：
//
//	var hq_str_Y1309="豆油1309,145958,7120,7190,7112,7150,7150,7156,7152,7164,7140,55,42,318796,97838,连,豆油,2013-06-28";
//	                    名称   时间   开   高   低  昨收  买价  卖价 最新价 结算 昨结算 买量 卖量 持仓量 成交量
//
// 注意**索引 1 是时间**（HHMMSS），所以所有字段比"没有时间"的布局往后挪一位。
// 两种布局都见过，所以解析时按 index 1 的形状自动判别（见 looksLikeClock）。
type RealtimeTick struct {
	Symbol string
	Price  float64
	High   float64
	Low    float64
	Bid    float64 // 买一价
	Ask    float64 // 卖一价
	BidVol float64 // 买一量
	AskVol float64 // 卖一量
	Hold   float64 // 持仓量
	Volume float64 // 成交量
	Day    string
	Clock  string
}

// valid 自校验：解析出来的必须是「像行情的一行」，否则不认。
func (t RealtimeTick) valid() bool {
	if t.Price <= 0 || t.Hold <= 0 {
		return false
	}
	if t.High > 0 && t.Low > 0 && t.High >= t.Low {
		if t.Price < t.Low*0.98 || t.Price > t.High*1.02 {
			return false // 最新价跑到最高最低之外 → 位置肯定认错了
		}
	}
	if t.Bid > 0 && t.Ask > 0 && t.Bid > t.Ask*1.02 {
		return false // 买价高于卖价 → 买价/卖价认反了
	}
	return true
}

// HQListURL 便于测试指定假上游。
func (c *Client) hqListURL() string {
	if c != nil && c.HQListURL != "" {
		return c.HQListURL
	}
	return defaultHQListURL
}

// RealtimeTicks 批量取实时行情（一次请求可带多个代码）。返回 map[symbol]tick，
// 只包含**校验通过**的行；调用方对缺失的代码自行退回分钟线取价。
func (c *Client) RealtimeTicks(ctx context.Context, symbols []string) (map[string]RealtimeTick, error) {
	if len(symbols) == 0 {
		return map[string]RealtimeTick{}, nil
	}
	codes := make([]string, 0, len(symbols))
	for _, s := range symbols {
		sym := strings.ToUpper(strings.TrimSpace(s))
		if sym == "" {
			continue
		}
		codes = append(codes, "nf_"+sym)
	}
	if len(codes) == 0 {
		return map[string]RealtimeTick{}, nil
	}
	body, err := c.getHQ(ctx, c.hqListURL()+strings.Join(codes, ","))
	if err != nil {
		return nil, err
	}
	return parseHQTicks(body), nil
}

// getHQ 请求 hq.sinajs.cn（这个口必须带 Referer，否则返回空）。
func (c *Client) getHQ(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
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
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("实时行情 HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
}

// looksLikeClock 判断某个字段是不是 HHMMSS / HH:MM:SS —— 用来判别「带时间」的布局。
func looksLikeClock(s string) bool {
	s = strings.TrimSpace(s)
	digits := strings.ReplaceAll(s, ":", "")
	if len(digits) != 6 {
		return false
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return false
		}
	}
	hh, _ := strconv.Atoi(digits[:2])
	mm, _ := strconv.Atoi(digits[2:4])
	ss, _ := strconv.Atoi(digits[4:])
	return hh < 24 && mm < 60 && ss < 60
}

func parseHQTicks(body []byte) map[string]RealtimeTick {
	out := map[string]RealtimeTick{}
	for symbol, fields := range splitHQRows(body) {
		tick, ok := tickFromFields(symbol, fields)
		if !ok {
			continue
		}
		out[symbol] = tick
	}
	return out
}

// tickFromFields 按字段数量/位置判别布局后取值。
func tickFromFields(symbol string, fields []string) (RealtimeTick, bool) {
	if len(fields) < 14 {
		return RealtimeTick{}, false
	}
	tick := RealtimeTick{Symbol: symbol}
	// 带时间布局：0 名称,1 时间,2 开,3 高,4 低,5 昨收,6 买价,7 卖价,8 最新价,
	//              9 结算,10 昨结算,11 买量,12 卖量,13 持仓量,14 成交量,…
	if looksLikeClock(fields[1]) {
		tick.Clock = strings.TrimSpace(fields[1])
		tick.High = hqFloat(fields[3])
		tick.Low = hqFloat(fields[4])
		tick.Bid = hqFloat(fields[6])
		tick.Ask = hqFloat(fields[7])
		tick.Price = hqFloat(fields[8])
		tick.BidVol = hqFloat(fields[11])
		tick.AskVol = hqFloat(fields[12])
		tick.Hold = hqFloat(fields[13])
		if len(fields) > 14 {
			tick.Volume = hqFloat(fields[14])
		}
		if len(fields) > 17 {
			tick.Day = strings.TrimSpace(fields[17])
		}
	} else {
		// 不带时间布局：0 名称,1 开,2 高,3 低,4 昨收,5 买价,6 卖价,7 最新价,
		//                8 结算,9 昨结算,10 买量,11 卖量,12 持仓量,13 成交量,…
		tick.High = hqFloat(fields[2])
		tick.Low = hqFloat(fields[3])
		tick.Bid = hqFloat(fields[5])
		tick.Ask = hqFloat(fields[6])
		tick.Price = hqFloat(fields[7])
		tick.BidVol = hqFloat(fields[10])
		tick.AskVol = hqFloat(fields[11])
		tick.Hold = hqFloat(fields[12])
		if len(fields) > 13 {
			tick.Volume = hqFloat(fields[13])
		}
		if len(fields) > 14 {
			tick.Day = strings.TrimSpace(fields[14])
		}
		if len(fields) > 15 {
			tick.Clock = strings.TrimSpace(fields[15])
		}
	}
	if !tick.valid() {
		return RealtimeTick{}, false
	}
	return tick, true
}

// splitHQRows 把 `var hq_str_nf_JM0="…";` 拆成 symbol → 字段数组。
func splitHQRows(body []byte) map[string][]string {
	out := map[string][]string{}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		i := strings.Index(line, "hq_str_")
		if i < 0 {
			continue
		}
		rest := line[i+len("hq_str_"):]
		eq := strings.Index(rest, "=")
		qs := strings.Index(rest, "\"")
		qe := strings.LastIndex(rest, "\"")
		if eq < 0 || qs < 0 || qe <= qs {
			continue
		}
		code := strings.TrimSpace(rest[:eq])
		code = strings.TrimPrefix(strings.ToUpper(code), "NF_") // nf_ 前缀只是表示国内期货
		if code == "" {
			continue
		}
		out[code] = strings.Split(rest[qs+1:qe], ",")
	}
	return out
}

func hqFloat(s string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return v
}

// TickTime 组合成「2026-09-24 14:59」；缺字段就返回空。
func (t RealtimeTick) TickTime() string {
	day := strings.TrimSpace(t.Day)
	clock := strings.TrimSpace(t.Clock)
	digits := strings.ReplaceAll(clock, ":", "")
	if len(digits) >= 4 {
		clock = digits[:2] + ":" + digits[2:4]
	}
	switch {
	case day != "" && clock != "":
		return day + " " + clock
	case day != "":
		return day
	case clock != "":
		return time.Now().In(locCST).Format("2006-01-02") + " " + clock
	}
	return ""
}

// RawTick 诊断用：原始那一行 + 按索引切好的字段。
type RawTick struct {
	Symbol string   `json:"symbol"`
	Line   string   `json:"line"`
	Fields []string `json:"fields"`
}

// RawTicks 取原始行（不做任何校验，诊断专用）。
func (c *Client) RawTicks(ctx context.Context, symbols []string) map[string]RawTick {
	out := map[string]RawTick{}
	codes := make([]string, 0, len(symbols))
	for _, s := range symbols {
		sym := strings.ToUpper(strings.TrimSpace(s))
		if sym == "" {
			continue
		}
		codes = append(codes, "nf_"+sym)
	}
	if len(codes) == 0 {
		return out
	}
	body, err := c.getHQ(ctx, c.hqListURL()+strings.Join(codes, ","))
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, "hq_str_") {
			continue
		}
		for symbol, fields := range splitHQRows([]byte(line)) {
			out[symbol] = RawTick{Symbol: symbol, Line: line, Fields: fields}
		}
	}
	return out
}
