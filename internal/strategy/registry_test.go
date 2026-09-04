package strategy

import "testing"

func TestRegistry(t *testing.T) {
	want := []string{"turtle", "ma_volume", "high_tight_flag", "limit_up_shakeout", "uptrend_limit_down", "rps_breakout"}
	all := All()
	if len(all) != len(want) {
		t.Fatalf("All len=%d", len(all))
	}
	for i, id := range want {
		if all[i].ID() != id {
			t.Fatalf("All[%d]=%s want %s", i, all[i].ID(), id)
		}
		s, ok := ByID(id)
		if !ok || s.ID() != id {
			t.Fatalf("ByID(%s) ok=%v", id, ok)
		}
	}
	if _, ok := ByID("nope"); ok {
		t.Fatal("ByID nope")
	}
}
