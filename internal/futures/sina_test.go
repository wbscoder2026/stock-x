package futures

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseMinuteJSONP(t *testing.T) {
	raw := `=([["2024-06-04 09:05:00","100","101","99","100.5","1234","50000"]]);`
	got, err := parseMinuteJSONP([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Close != 100.5 || got[0].Volume != 1234 || got[0].Hold != 50000 {
		t.Fatalf("%+v", got)
	}
	if got[0].Time.Hour() != 9 || got[0].Time.Minute() != 5 {
		t.Fatalf("time=%v", got[0].Time)
	}
}

func TestParseMinuteJSONPObject(t *testing.T) {
	raw := `/*<script>location.href='//sina.com';</script>*/
=([{"d":"2026-08-31 10:10:00","o":"1724.000","h":"1729.500","l":"1723.500","c":"1724.500","v":"15983","p":"662835"}]);`
	got, err := parseMinuteJSONP([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Close != 1724.5 || got[0].Volume != 15983 || got[0].Hold != 662835 {
		t.Fatalf("%+v", got)
	}
}

func TestParseDailyJSONP(t *testing.T) {
	raw := `var _x=([["2024-06-03","110","120","100","115","9","8","114"]]);`
	got, err := parseDailyJSONP([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].High != 120 || got[0].Settle != 114 {
		t.Fatalf("%+v", got)
	}
	obj := `var _x=([{"d":"2013-03-22","o":"1280","h":"1304","l":"1257","c":"1267","v":"8","p":"9","s":"1276"}]);`
	got, err = parseDailyJSONP([]byte(obj))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Settle != 1276 || got[0].Close != 1267 {
		t.Fatalf("%+v", got)
	}
}

func TestClientMinute(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("symbol") != "JM0" || r.URL.Query().Get("type") != "5" {
			t.Errorf("query %s", r.URL.RawQuery)
		}
		if r.Header.Get("Referer") == "" {
			t.Error("missing referer")
		}
		_, _ = w.Write([]byte(`=([["2024-06-04 09:05:00","1","2","0.5","1.5","10","20"]]);`))
	}))
	t.Cleanup(srv.Close)
	c := &Client{MinuteURL: srv.URL}
	got, err := c.Minute(context.Background(), "JM0", "5")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Close != 1.5 {
		t.Fatalf("%+v", got)
	}
}
