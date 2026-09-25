package futures

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeLister 按品种返回固定月份合约，并记录被调用过哪些品种（测试不碰网络）。
type fakeLister struct {
	by    map[string][]Contract
	err   error
	mu    sync.Mutex
	calls []string
}

func (f *fakeLister) List(_ context.Context, v Variety) ([]Contract, error) {
	f.mu.Lock()
	f.calls = append(f.calls, v.Prefix)
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return f.by[v.Prefix], nil
}

func (f *fakeLister) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func jmMonths() []Contract {
	return []Contract{
		{Symbol: "JM2611", Label: "2611", Kind: "month", Position: 500},
		{Symbol: "JM2612", Label: "2612", Kind: "month", Position: 1200},
		{Symbol: "JM2701", Label: "2701", Kind: "month", Position: 88000},
	}
}

func TestContractCacheKeepsEveryMonthAndPicksMain(t *testing.T) {
	l := &fakeLister{by: map[string][]Contract{"JM": jmMonths()}}
	c := NewContractCache(l, 30*time.Minute)
	c.Refresh(context.Background(), ListVarieties())

	got := c.MonthSymbols("jm")
	if len(got) != 3 || got[0] != "JM2611" || got[2] != "JM2701" {
		t.Fatalf("应缓存全部月份合约：%v", got)
	}
	main, ok := c.Get("JM")
	if !ok || main.Symbol != "JM2701" {
		t.Fatalf("主力应是持仓最大的 JM2701：%+v", main)
	}
	snap := c.MonthSnapshot()
	if len(snap["JM"]) != 3 {
		t.Fatalf("快照应带全部月份：%v", snap)
	}

	// 缓存新鲜期内的刷新不该再打接口
	c.Refresh(context.Background(), ListVarieties())
	if n := len(l.seen()); n != len(ListVarieties()) {
		t.Fatalf("新鲜期内不该重复解析：%d 次", n)
	}
}

func TestContractCacheRetriesFailedVariety(t *testing.T) {
	l := &fakeLister{err: context.DeadlineExceeded}
	c := NewContractCache(l, 30*time.Minute)
	c.Refresh(context.Background(), []Variety{mustVariety(t, "JM")})
	if len(c.MonthSymbols("JM")) != 0 {
		t.Fatal("失败时不该留下脏数据")
	}
	// 失败的品种不会每轮都重打（否则页面一轮询就变成请求风暴）
	c.Refresh(context.Background(), []Variety{mustVariety(t, "JM")})
	if n := len(l.seen()); n != 1 {
		t.Fatalf("失败后不该立刻重试：%d 次", n)
	}
	// 过了重试窗口就该补上：这里直接把缓存时间拨老来模拟
	c.mu.Lock()
	c.at["JM"] = time.Now().Add(-2 * c.ttl)
	c.mu.Unlock()
	l.err = nil
	l.by = map[string][]Contract{"JM": jmMonths()}
	c.Refresh(context.Background(), []Variety{mustVariety(t, "JM")})
	if got := c.MonthSymbols("JM"); len(got) != 3 {
		t.Fatalf("过了重试窗口应补上月份合约：%v", got)
	}
}

func TestContractCacheReadOnlyNeverCallsUpstream(t *testing.T) {
	l := &fakeLister{by: map[string][]Contract{"JM": jmMonths()}}
	c := NewContractCache(l, 30*time.Minute)
	// 只读入口：不触发任何解析
	if _, ok := c.Get("JM"); ok {
		t.Fatal("空缓存不该命中")
	}
	if got := c.ListOf("JM"); len(got) != 0 {
		t.Fatalf("空缓存不该有列表：%v", got)
	}
	if len(c.MonthSnapshot()) != 0 {
		t.Fatal("空缓存快照应为空")
	}
	if n := len(l.seen()); n != 0 {
		t.Fatalf("只读方法不该打接口：%d 次", n)
	}
	if got, _ := c.Get(""); strings.TrimSpace(got.Symbol) != "" {
		t.Fatal("空品种不该命中")
	}
}
