package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"
)

// Pick 选股条目，Send 使用同等字段的匿名结构体。
type Pick struct {
	Symbol, Name string
}

// ToXueqiu 将 A 股代码转为雪球代码：6→SH，4/8→BJ，其余 SZ。
func ToXueqiu(symbol string) string {
	s := strings.TrimSpace(symbol)
	s = strings.ReplaceAll(s, " ", "")
	if i := strings.LastIndex(s, "."); i >= 0 {
		pre := strings.ToLower(s[:i])
		num := strings.ToUpper(s[i+1:])
		switch pre {
		case "bj":
			return "BJ" + num
		case "sh":
			return "SH" + num
		case "sz":
			return "SZ" + num
		}
		s = num
	} else {
		s = strings.ToUpper(s)
	}
	for _, p := range []string{"SH", "SZ", "BJ"} {
		if strings.HasPrefix(s, p) && len(s) > len(p) && unicode.IsDigit(rune(s[len(p)])) {
			s = s[len(p):]
			return p + s
		}
	}
	if s == "" {
		return s
	}
	switch s[0] {
	case '6':
		return "SH" + s
	case '4', '8':
		return "BJ" + s
	default:
		return "SZ" + s
	}
}

// Send 向飞书 webhook 推送 interactive 卡片。webhook 为空则直接成功。
func Send(webhook, strategyName string, picks []struct{ Symbol, Name string }) error {
	webhook = strings.TrimSpace(webhook)
	if webhook == "" {
		return nil
	}
	today := time.Now().Format("2006-01-02")
	links := make([]string, 0, len(picks))
	for _, p := range picks {
		xq := ToXueqiu(p.Symbol)
		name := strings.TrimSpace(p.Name)
		if name == "" {
			name = xq
		}
		links = append(links, fmt.Sprintf("[%s](https://xueqiu.com/S/%s)", name, xq))
	}
	symbolText := "（无选股结果）"
	if len(links) > 0 {
		symbolText = strings.Join(links, " ")
	}
	payload := map[string]any{
		"msg_type": "interactive",
		"card": map[string]any{
			"header": map[string]any{
				"title": map[string]any{
					"tag":     "plain_text",
					"content": "📈 stock-x 选股播报 | " + strategyName,
				},
				"template": "blue",
			},
			"elements": []any{
				map[string]any{
					"tag": "div",
					"text": map[string]any{
						"tag":     "lark_md",
						"content": fmt.Sprintf("**日期：** %s\n**策略：** %s\n**选股数量：** %d", today, strategyName, len(picks)),
					},
				},
				map[string]any{"tag": "hr"},
				map[string]any{
					"tag": "div",
					"text": map[string]any{
						"tag":     "lark_md",
						"content": "**选股列表：**\n" + symbolText,
					},
				},
			},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, webhook, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("飞书 HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var parsed struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return fmt.Errorf("飞书响应非 JSON: %s", strings.TrimSpace(string(raw)))
	}
	if parsed.Code != 0 {
		return fmt.Errorf("飞书失败 code=%d msg=%s", parsed.Code, parsed.Msg)
	}
	return nil
}
