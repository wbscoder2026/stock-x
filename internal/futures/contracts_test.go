package futures

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsMain(t *testing.T) {
	if !isMain("JM0") || !isMain("T0") || isMain("JM2701") || isMain("JM2612") {
		t.Fatal("isMain")
	}
}

func TestMatchVarieties(t *testing.T) {
	jm := matchVarieties("焦煤")
	if len(jm) != 1 || jm[0].Prefix != "JM" {
		t.Fatalf("焦煤 %+v", jm)
	}
	if g := matchVarieties("JM2701"); len(g) != 1 || g[0].Prefix != "JM" {
		t.Fatalf("JM2701 %+v", g)
	}
	j := matchVarieties("J")
	if len(j) != 1 || j[0].Prefix != "J" {
		t.Fatalf("J should be 焦炭, got %+v", j)
	}
}

func TestListVarieties(t *testing.T) {
	got := ListVarieties()
	var jm bool
	for _, v := range got {
		if v.Prefix == "JM" && v.Name == "焦煤" {
			jm = true
		}
	}
	if !jm {
		t.Fatal("missing 焦煤")
	}
}

func TestContractsByPrefix(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("node") != "jm_qh" {
			t.Errorf("node=%s", r.URL.Query().Get("node"))
		}
		_, _ = w.Write([]byte(`[{"symbol":"JM0","name":"焦煤连续","position":"1"},{"symbol":"JM2701","name":"焦煤2701","position":"1"},{"symbol":"JM2612","name":"焦煤2612","position":"2"}]`))
	}))
	t.Cleanup(srv.Close)
	c := &Client{HQURL: srv.URL}
	got, err := c.ContractsByPrefix(context.Background(), "JM")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Label != "主连" || got[0].Symbol != "JM0" {
		t.Fatalf("%+v", got)
	}
	labels := map[string]string{}
	for _, x := range got {
		labels[x.Symbol] = x.Label
	}
	if labels["JM2612"] != "2612" || labels["JM2701"] != "2701" {
		t.Fatalf("labels=%v", labels)
	}
}

func TestParseHQContracts(t *testing.T) {
	raw := `[{"symbol":"JM0","name":"焦煤连续","position":"444"},{"symbol":"JM2701","name":"焦煤2701","position":"444"},{"symbol":"JM2612","name":"焦煤2612","position":"73"}]`
	v := Variety{Name: "焦煤", Prefix: "JM", Exchange: "大商所"}
	got, err := parseHQContracts([]byte(raw), v)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Kind != "main" || got[0].Name != "焦煤主连" || got[1].Symbol != "JM2701" {
		t.Fatalf("%+v", got)
	}
}

func TestSearchContractsEmptyIsMain(t *testing.T) {
	got, err := (*Client)(nil).SearchContracts(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 10 || got[0].Kind != "main" {
		t.Fatalf("len=%d %+v", len(got), got[:1])
	}
}

func TestSearchContractsFetchesNode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("node") != "jm_qh" {
			t.Errorf("node=%s", r.URL.Query().Get("node"))
		}
		_, _ = w.Write([]byte(`[{"symbol":"JM0","name":"焦煤连续","position":"1"},{"symbol":"JM2701","name":"焦煤2701","position":"1"},{"symbol":"JM2612","name":"焦煤2612","position":"2"}]`))
	}))
	t.Cleanup(srv.Close)
	c := &Client{HQURL: srv.URL}
	got, err := c.SearchContracts(context.Background(), "焦煤")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Symbol != "JM0" {
		t.Fatalf("%+v", got)
	}
	got, err = c.SearchContracts(context.Background(), "2612")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("bare 2612 should not fetch all: %+v", got)
	}
	got, err = c.SearchContracts(context.Background(), "JM2612")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Symbol != "JM2612" {
		t.Fatalf("%+v", got)
	}
}
