package futures

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const defaultHQURL = "https://vip.stock.finance.sina.com.cn/quotes_service/api/json_v2.php/Market_Center.getHQFuturesData"

type Variety struct {
	Name     string `json:"name"`
	Prefix   string `json:"prefix"`
	Node     string `json:"-"`
	Exchange string `json:"exchange"`
}

type Contract struct {
	Symbol   string  `json:"symbol"`
	Name     string  `json:"name"`
	Label    string  `json:"label"`
	Variety  string  `json:"variety"`
	Exchange string  `json:"exchange"`
	Kind     string  `json:"kind"`
	Position float64 `json:"position"`
}

var varieties = []Variety{
	{Name: "焦煤", Prefix: "JM", Node: "jm_qh", Exchange: "大商所"},
	{Name: "焦炭", Prefix: "J", Node: "jt_qh", Exchange: "大商所"},
	{Name: "铁矿石", Prefix: "I", Node: "tks_qh", Exchange: "大商所"},
	{Name: "豆粕", Prefix: "M", Node: "dp_qh", Exchange: "大商所"},
	{Name: "豆油", Prefix: "Y", Node: "dy_qh", Exchange: "大商所"},
	{Name: "豆一", Prefix: "A", Node: "dd_qh", Exchange: "大商所"},
	{Name: "豆二", Prefix: "B", Node: "de_qh", Exchange: "大商所"},
	{Name: "玉米", Prefix: "C", Node: "hym_qh", Exchange: "大商所"},
	{Name: "淀粉", Prefix: "CS", Node: "bz_qh", Exchange: "大商所"},
	{Name: "棕榈油", Prefix: "P", Node: "zly_qh", Exchange: "大商所"},
	{Name: "塑料", Prefix: "L", Node: "lldpe_qh", Exchange: "大商所"},
	{Name: "PVC", Prefix: "V", Node: "pvc_qh", Exchange: "大商所"},
	{Name: "PP", Prefix: "PP", Node: "jbx_qh", Exchange: "大商所"},
	{Name: "乙二醇", Prefix: "EG", Node: "ymdf_qh", Exchange: "大商所"},
	{Name: "苯乙烯", Prefix: "EB", Node: "yec_qh", Exchange: "大商所"},
	{Name: "液化气", Prefix: "PG", Node: "pg_qh", Exchange: "大商所"},
	{Name: "鸡蛋", Prefix: "JD", Node: "jd_qh", Exchange: "大商所"},
	{Name: "生猪", Prefix: "LH", Node: "lh_qh", Exchange: "大商所"},
	{Name: "原木", Prefix: "LG", Node: "lg_qh", Exchange: "大商所"},
	{Name: "粳米", Prefix: "RR", Node: "gm_qh", Exchange: "大商所"},
	{Name: "螺纹钢", Prefix: "RB", Node: "lwg_qh", Exchange: "上期所"},
	{Name: "热卷", Prefix: "HC", Node: "xc_qh", Exchange: "上期所"},
	{Name: "黄金", Prefix: "AU", Node: "hj_qh", Exchange: "上期所"},
	{Name: "白银", Prefix: "AG", Node: "by_qh", Exchange: "上期所"},
	{Name: "铜", Prefix: "CU", Node: "tong_qh", Exchange: "上期所"},
	{Name: "铝", Prefix: "AL", Node: "lv_qh", Exchange: "上期所"},
	{Name: "锌", Prefix: "ZN", Node: "xing_qh", Exchange: "上期所"},
	{Name: "铅", Prefix: "PB", Node: "qian_qh", Exchange: "上期所"},
	{Name: "镍", Prefix: "NI", Node: "ni_qh", Exchange: "上期所"},
	{Name: "锡", Prefix: "SN", Node: "xi_qh", Exchange: "上期所"},
	{Name: "不锈钢", Prefix: "SS", Node: "bxg_qh", Exchange: "上期所"},
	{Name: "燃油", Prefix: "FU", Node: "ry_qh", Exchange: "上期所"},
	{Name: "沥青", Prefix: "BU", Node: "lq_qh", Exchange: "上期所"},
	{Name: "橡胶", Prefix: "RU", Node: "rzjb_qh", Exchange: "上期所"},
	{Name: "纸浆", Prefix: "SP", Node: "zj_qh", Exchange: "上期所"},
	{Name: "20号胶", Prefix: "NR", Node: "ehj_qh", Exchange: "上期所"},
	{Name: "氧化铝", Prefix: "AO", Node: "ao_qh", Exchange: "上期所"},
	{Name: "低硫燃油", Prefix: "LU", Node: "lu_qh", Exchange: "上期所"},
	{Name: "原油", Prefix: "SC", Node: "yy_qh", Exchange: "上期能源"},
	{Name: "PTA", Prefix: "TA", Node: "pta_qh", Exchange: "郑商所"},
	{Name: "甲醇", Prefix: "MA", Node: "zc_qh", Exchange: "郑商所"},
	{Name: "菜油", Prefix: "OI", Node: "czy_qh", Exchange: "郑商所"},
	{Name: "菜粕", Prefix: "RM", Node: "czp_qh", Exchange: "郑商所"},
	{Name: "白糖", Prefix: "SR", Node: "bst_qh", Exchange: "郑商所"},
	{Name: "棉花", Prefix: "CF", Node: "mh_qh", Exchange: "郑商所"},
	{Name: "动力煤", Prefix: "ZC", Node: "dlm_qh", Exchange: "郑商所"},
	{Name: "玻璃", Prefix: "FG", Node: "bl_qh", Exchange: "郑商所"},
	{Name: "纯碱", Prefix: "SA", Node: "cj_qh", Exchange: "郑商所"},
	{Name: "尿素", Prefix: "UR", Node: "ns_qh", Exchange: "郑商所"},
	{Name: "短纤", Prefix: "PF", Node: "pf_qh", Exchange: "郑商所"},
	{Name: "花生", Prefix: "PK", Node: "pk_qh", Exchange: "郑商所"},
	{Name: "苹果", Prefix: "AP", Node: "xpg_qh", Exchange: "郑商所"},
	{Name: "红枣", Prefix: "CJ", Node: "hz_qh", Exchange: "郑商所"},
	{Name: "烧碱", Prefix: "SH", Node: "sh_qh", Exchange: "郑商所"},
	{Name: "对二甲苯", Prefix: "PX", Node: "px_qh", Exchange: "郑商所"},
	{Name: "硅铁", Prefix: "SF", Node: "gt_qh", Exchange: "郑商所"},
	{Name: "锰硅", Prefix: "SM", Node: "mg_qh", Exchange: "郑商所"},
	{Name: "工业硅", Prefix: "SI", Node: "si_qh", Exchange: "广期所"},
	{Name: "碳酸锂", Prefix: "LC", Node: "lc_qh", Exchange: "广期所"},
	{Name: "多晶硅", Prefix: "PS", Node: "ps_qh", Exchange: "广期所"},
	{Name: "沪深300", Prefix: "IF", Node: "qz_qh", Exchange: "中金所"},
	{Name: "上证50", Prefix: "IH", Node: "szgz_qh", Exchange: "中金所"},
	{Name: "中证500", Prefix: "IC", Node: "zzgz_qh", Exchange: "中金所"},
	{Name: "中证1000", Prefix: "IM", Node: "im_qh", Exchange: "中金所"},
	{Name: "十年国债", Prefix: "T", Node: "sngz_qh", Exchange: "中金所"},
	{Name: "五年国债", Prefix: "TF", Node: "gz_qh", Exchange: "中金所"},
	{Name: "二年国债", Prefix: "TS", Node: "engz_qh", Exchange: "中金所"},
}

var mainSymRe = regexp.MustCompile(`^[A-Za-z]+0$`)

func ListVarieties() []Variety {
	out := make([]Variety, len(varieties))
	copy(out, varieties)
	return out
}

// MainSymbol 主力连续标的，如 JM → JM0。
func MainSymbol(v Variety) string { return mainOf(v).Symbol }

func (c *Client) ContractsByPrefix(ctx context.Context, prefix string) ([]Contract, error) {
	v, ok := varietyByPrefix(prefix)
	if !ok {
		return nil, fmt.Errorf("未知品种 %s", prefix)
	}
	rows, err := c.fetchVariety(ctx, v)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		rows = []Contract{mainOf(v)}
	}
	sortContracts(rows)
	return rows, nil
}

func (c *Client) SearchContracts(ctx context.Context, q string) ([]Contract, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return mainContracts(), nil
	}
	matched := matchVarieties(q)
	if len(matched) == 0 {
		return filterContracts(mainContracts(), q), nil
	}
	if len(matched) > 6 {
		matched = matched[:6]
	}
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		all []Contract
	)
	for _, v := range matched {
		wg.Add(1)
		go func(v Variety) {
			defer wg.Done()
			rows, err := c.fetchVariety(ctx, v)
			if err != nil {
				return
			}
			mu.Lock()
			all = append(all, rows...)
			mu.Unlock()
		}(v)
	}
	wg.Wait()
	out := filterContracts(all, q)
	sortContracts(out)
	return out, nil
}

func (c *Client) fetchVariety(ctx context.Context, v Variety) ([]Contract, error) {
	u := defaultHQURL
	if c != nil && c.HQURL != "" {
		u = c.HQURL
	}
	q := url.Values{
		"page": {"1"}, "num": {"80"}, "sort": {"position"}, "asc": {"0"},
		"node": {v.Node}, "base": {"futures"},
	}
	body, err := c.get(ctx, u+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	return parseHQContracts(body, v)
}

func parseHQContracts(body []byte, v Variety) ([]Contract, error) {
	s := strings.TrimSpace(string(body))
	if s == "" || s == "null" {
		return nil, nil
	}
	var rows []struct {
		Symbol, Exchange, Name string
		Position               json.RawMessage `json:"position"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, err
	}
	out := make([]Contract, 0, len(rows))
	seen := map[string]bool{}
	for _, r := range rows {
		sym := strings.TrimSpace(r.Symbol)
		if sym == "" || seen[sym] {
			continue
		}
		seen[sym] = true
		kind := "contract"
		name := strings.TrimSpace(r.Name)
		if isMain(sym) {
			kind = "main"
			name = v.Name + "主连"
		} else if name == "" {
			name = v.Name + strings.TrimPrefix(strings.ToUpper(sym), v.Prefix)
		}
		out = append(out, Contract{
			Symbol: sym, Name: name, Label: contractLabel(sym, v),
			Variety: v.Name, Exchange: v.Exchange,
			Kind: kind, Position: parseFloatStr(rawString(r.Position)),
		})
	}
	return out, nil
}

func mainContracts() []Contract {
	out := make([]Contract, 0, len(varieties))
	for _, v := range varieties {
		out = append(out, mainOf(v))
	}
	return out
}

func matchVarieties(q string) []Variety {
	qn := strings.ToUpper(strings.TrimSpace(q))
	letter := letterPrefix(qn)
	var exact, fuzzy []Variety
	for _, v := range varieties {
		up := strings.ToUpper(v.Prefix)
		if letter != "" && up == letter {
			exact = append(exact, v)
			continue
		}
		if strings.Contains(v.Name, q) || strings.Contains(q, v.Name) ||
			strings.Contains(strings.ToUpper(v.Exchange), qn) ||
			(letter != "" && strings.HasPrefix(up, letter) && len(letter) >= 2) {
			fuzzy = append(fuzzy, v)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	return fuzzy
}

func filterContracts(in []Contract, q string) []Contract {
	q = strings.TrimSpace(q)
	if q == "" {
		return in
	}
	qu := strings.ToUpper(q)
	out := make([]Contract, 0, len(in))
	for _, c := range in {
		if strings.Contains(strings.ToUpper(c.Symbol), qu) ||
			strings.Contains(c.Name, q) ||
			strings.Contains(c.Variety, q) ||
			strings.Contains(q, c.Variety) {
			out = append(out, c)
		}
	}
	return out
}

func sortContracts(in []Contract) {
	sort.Slice(in, func(i, j int) bool {
		if in[i].Kind != in[j].Kind {
			return in[i].Kind == "main"
		}
		if in[i].Variety != in[j].Variety {
			return in[i].Variety < in[j].Variety
		}
		if in[i].Position != in[j].Position {
			return in[i].Position > in[j].Position
		}
		return in[i].Symbol < in[j].Symbol
	})
}

func varietyByPrefix(prefix string) (Variety, bool) {
	p := strings.ToUpper(strings.TrimSpace(prefix))
	for _, v := range varieties {
		if strings.ToUpper(v.Prefix) == p {
			return v, true
		}
	}
	return Variety{}, false
}

func mainOf(v Variety) Contract {
	sym := v.Prefix + "0"
	return Contract{
		Symbol: sym, Name: v.Name + "主连", Label: "主连",
		Variety: v.Name, Exchange: v.Exchange, Kind: "main",
	}
}

func contractLabel(sym string, v Variety) string {
	if isMain(sym) {
		return "主连"
	}
	rest := strings.TrimPrefix(strings.ToUpper(sym), strings.ToUpper(v.Prefix))
	if rest == "" {
		return strings.ToUpper(sym)
	}
	return rest
}

func isMain(symbol string) bool {
	return mainSymRe.MatchString(strings.ToUpper(symbol))
}

func letterPrefix(s string) string {
	i := 0
	for i < len(s) && s[i] >= 'A' && s[i] <= 'Z' {
		i++
	}
	return s[:i]
}

func parseFloatStr(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}
