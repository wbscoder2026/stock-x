package notify

import "testing"

func TestSendEmptyWebhook(t *testing.T) {
	err := Send("", "海龟突破", []struct{ Symbol, Name string }{
		{Symbol: "600000", Name: "浦发银行"},
	})
	if err != nil {
		t.Fatalf("空 webhook 应为成功，得到 %v", err)
	}
}

func TestToXueqiu(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"600000", "SH600000"},
		{"sh.600519", "SH600519"},
		{"000001", "SZ000001"},
		{"sz.000001", "SZ000001"},
		{"430047", "BJ430047"},
		{"830799", "BJ830799"},
		{"bj.920000", "BJ920000"},
		{"SH600000", "SH600000"},
	}
	for _, c := range cases {
		if got := ToXueqiu(c.in); got != c.want {
			t.Errorf("ToXueqiu(%q)=%q 期望 %q", c.in, got, c.want)
		}
	}
}
