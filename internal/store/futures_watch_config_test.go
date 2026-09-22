package store

import "testing"

func TestFuturesWatchConfigRoundTrip(t *testing.T) {
	st := openTestStore(t)

	if _, ok, err := st.LoadFuturesWatchConfig(); err != nil || ok {
		t.Fatalf("没保存过应返回 ok=false：%v %v", ok, err)
	}

	first := []byte(`{"period":"5","alert":{"desktop":true},"alert_ttl_min":30,"enabled":true}`)
	if err := st.SaveFuturesWatchConfig(first); err != nil {
		t.Fatal(err)
	}
	got, ok, err := st.LoadFuturesWatchConfig()
	if err != nil || !ok {
		t.Fatalf("应能读回：%v %v", ok, err)
	}
	if string(got) != string(first) {
		t.Fatalf("内容不一致：%s", got)
	}

	// 覆盖写：只留一行，不能越存越多
	second := []byte(`{"period":"15","alert":{"desktop":false},"alert_ttl_min":60,"enabled":false}`)
	if err := st.SaveFuturesWatchConfig(second); err != nil {
		t.Fatal(err)
	}
	got, _, err = st.LoadFuturesWatchConfig()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(second) {
		t.Fatalf("覆盖写失败：%s", got)
	}
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM futures_watch_config`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("配置应只有一行：%d", n)
	}
}
