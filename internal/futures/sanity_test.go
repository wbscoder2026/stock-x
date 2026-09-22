package futures

import (
	"context"
	"strings"
	"testing"
	"time"
)

// 真实案例：东财主连（114.jmm / klt=60）把「历史段」分钟线返回成 1153 万量级，
// 只有最新一段是正常的 1525。这种脏数据会让 ATR、ATR缓冲、推荐止损、回测统计全失真。

func dirtyCloses(nBad, nGood int) []float64 {
	out := make([]float64, 0, nBad+nGood)
	for i := 0; i < nBad; i++ {
		out = append(out, 11530000+float64(i)*13)
	}
	for i := 0; i < nGood; i++ {
		out = append(out, 1525+float64(i))
	}
	return out
}

func TestCheckBarScaleRejectsMixedMagnitudes(t *testing.T) {
	err := checkBarScale("JM", dirtyCloses(480, 18))
	if err == nil {
		t.Fatal("价格量级混叠应判为不可信")
	}
	if !strings.Contains(err.Error(), "量级") || !strings.Contains(err.Error(), "JM") {
		t.Fatalf("错误信息要能定位问题：%v", err)
	}
}

func TestCheckBarScaleAcceptsTrendAndSingleOutlier(t *testing.T) {
	// 单边趋势（一个月涨 60%）不能误判
	trend := make([]float64, 0, 500)
	for i := 0; i < 500; i++ {
		trend = append(trend, 1500+float64(i)*2)
	}
	if err := checkBarScale("RB", trend); err != nil {
		t.Fatalf("趋势不该报错：%v", err)
	}

	// 单个坏 tick 不足以作废整个源（会被后续清理，不该整段丢弃）
	odd := append([]float64(nil), trend...)
	odd[100] = 99999
	if err := checkBarScale("RB", odd); err != nil {
		t.Fatalf("单个坏点不该作废：%v", err)
	}

	// 样本太少不做判断（避免误杀）
	if err := checkBarScale("RB", []float64{1, 2, 3}); err != nil {
		t.Fatalf("样本少不该报错：%v", err)
	}
	if err := checkBarScale("RB", nil); err != nil {
		t.Fatalf("空序列不该报错：%v", err)
	}

	// 非法价（0 / 负 / NaN）直接判脏
	for _, bad := range []float64{0, -1} {
		if err := checkBarScale("RB", append(append([]float64(nil), trend...), bad)); err == nil {
			t.Fatalf("价格 %v 应判脏", bad)
		}
	}
}

func TestCheckMinuteDailyAgree(t *testing.T) {
	minutes := []Bar{{Time: time.Date(2026, 9, 22, 14, 0, 0, 0, locCST), Close: 1525}, {Close: 1530}}
	days := []Daily{{Close: 1528}}
	if err := checkMinuteDailyAgree("JM", minutes, days); err != nil {
		t.Fatalf("同量级应通过：%v", err)
	}

	// 分钟线整体错量级（就算没有混叠，也要拦住）
	if err := checkMinuteDailyAgree("JM", minutes, []Daily{{Close: 11530000}}); err == nil {
		t.Fatal("分钟与日线差 7000 倍应报错")
	}

	// 空序列交给上游的空数据校验，这里不报错
	if err := checkMinuteDailyAgree("JM", nil, days); err != nil {
		t.Fatalf("%v", err)
	}
	if err := checkMinuteDailyAgree("JM", minutes, nil); err != nil {
		t.Fatalf("%v", err)
	}
}

func TestFailoverSkipsSourceWithDirtyPrices(t *testing.T) {
	// 关键行为：脏数据的源要被判失败，自动降级到干净的源（而不是"解析成功就用"）
	dirty := make([]Bar, 0, 498)
	bad := dirtyCloses(480, 18)
	for i, c := range bad {
		dirty = append(dirty, Bar{
			Time: time.Date(2026, 8, 21, 21, 15, 0, 0, locCST).Add(time.Duration(i) * 15 * time.Minute),
			Open: c, High: c, Low: c, Close: c, Volume: 1000,
		})
	}
	primary := &fakeSource{name: "eastmoney", minute: dirty, daily: fakeDays()}
	backup := &fakeSource{name: "sina", minute: fakeBars(), daily: fakeDays()}
	w := NewWatcherSources(primary, backup)

	bars, err := w.fetchMinute(context.Background(), mustVariety(t, "JM"), "15")
	if err != nil {
		t.Fatal(err)
	}
	if len(bars) != 1 || bars[0].Close != 1.5 {
		t.Fatalf("应拿到干净源的数据：%+v", bars)
	}
	st := w.Status()
	if st.Source != "sina" {
		t.Fatalf("应降级到 sina：%s", st.Source)
	}
	if st.Sources[0].Fails != 1 || st.Sources[0].OK != 0 {
		t.Fatalf("脏数据源应记失败：%+v", st.Sources[0])
	}
	if st.Sources[1].OK != 1 {
		t.Fatalf("干净源应记成功：%+v", st.Sources[1])
	}
}

func TestFailoverAllSourcesDirty(t *testing.T) {
	// 全链路都脏 → 必须报错（让上层退避/提示），绝不能静默用脏数据出信号
	dirty := make([]Bar, 0, 498)
	bad := dirtyCloses(480, 18)
	for i, c := range bad {
		dirty = append(dirty, Bar{Time: time.Unix(int64(i)*900, 0), Open: c, High: c, Low: c, Close: c, Volume: 1})
	}
	a := &fakeSource{name: "a", minute: dirty}
	b := &fakeSource{name: "b", minute: dirty}
	w := NewWatcherSources(a, b)

	_, err := w.fetchMinute(context.Background(), mustVariety(t, "JM"), "15")
	if err == nil {
		t.Fatal("全脏应报错")
	}
	if !strings.Contains(err.Error(), "量级") {
		t.Fatalf("错误应说明原因：%v", err)
	}
}
