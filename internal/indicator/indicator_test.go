package indicator

import (
	"math"
	"testing"
)

func TestSMA_LastOfFivePeriod3Is4(t *testing.T) {
	got := SMA([]float64{1, 2, 3, 4, 5}, 3)
	if len(got) != 5 {
		t.Fatalf("len=%d", len(got))
	}
	if !math.IsNaN(got[0]) || !math.IsNaN(got[1]) {
		t.Fatalf("前两根应为 NaN: %v", got)
	}
	if got[2] != 2 || got[3] != 3 || got[4] != 4 {
		t.Fatalf("SMA=%v want [NaN NaN 2 3 4]", got)
	}
}

func TestRollingMax_InsufficientIsNaN(t *testing.T) {
	got := RollingMax([]float64{1, 3, 2, 5, 4}, 3)
	if !math.IsNaN(got[0]) || !math.IsNaN(got[1]) {
		t.Fatalf("前两根应为 NaN: %v", got)
	}
	if got[2] != 3 || got[3] != 5 || got[4] != 5 {
		t.Fatalf("RollingMax=%v want [NaN NaN 3 5 5]", got)
	}
}
