package strategy

import (
	"testing"
	"time"

	"github.com/wbscoder2026/stock-x/internal/store"
)

func nBars(sym string, n int, fill func(i int, b *store.Bar)) []store.Bar {
	t0 := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	out := make([]store.Bar, n)
	for i := range n {
		b := store.Bar{Symbol: sym, Date: t0.AddDate(0, 0, i).Format("2006-01-02")}
		fill(i, &b)
		out[i] = b
	}
	return out
}

func lastDate(bs []store.Bar) string {
	return bs[len(bs)-1].Date
}

func picked(t *testing.T, got []Pick, want ...string) {
	t.Helper()
	m := symbolsOf(got)
	if len(m) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for _, s := range want {
		if !m[s] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func notPicked(t *testing.T, got []Pick, sym string) {
	t.Helper()
	if symbolsOf(got)[sym] {
		t.Fatalf("unexpected pick %s in %v", sym, got)
	}
}
