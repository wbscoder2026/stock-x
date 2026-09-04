package strategy

import (
	"testing"

	"github.com/wbscoder2026/stock-x/internal/store"
)

func rpsSeries(sym string, start, end, peak float64) []store.Bar {
	return nBars(sym, 121, func(i int, b *store.Bar) {
		c := start
		if i == 120 {
			c = end
		} else if i > 0 {
			c = start + (end-start)*float64(i)/120
			if c > peak {
				c = peak
			}
		}
		h := c
		if i > 0 && peak > h {
			h = start + (peak-start)*float64(i)/120
			if h > peak {
				h = peak
			}
		}
		if i == 120 {
			h = peak
			if end > h {
				h = end
			}
		}
		b.Open, b.High, b.Low, b.Close = c, h, c, c
		if h < c {
			b.High = c
		}
	})
}

func TestRPSBreakout(t *testing.T) {
	a := rpsSeries("A", 10, 20, 20)
	b := rpsSeries("B", 10, 10, 10)
	c := rpsSeries("C", 10, 15, 15)
	d := rpsSeries("D", 10, 19, 30)
	asOf := lastDate(a)
	got := RPSBreakout{}.Run(map[string][]store.Bar{
		"A": a, "B": b, "C": c, "D": d,
	}, asOf, nil)
	picked(t, got, "A")
	notPicked(t, got, "B")
	notPicked(t, got, "C")
	notPicked(t, got, "D")
}

func TestRPSSingleStock(t *testing.T) {
	a := rpsSeries("ONLY", 10, 20, 20)
	got := RPSBreakout{}.Run(map[string][]store.Bar{"ONLY": a}, lastDate(a), nil)
	picked(t, got, "ONLY")
	if got[0].Extra["rps"] != 100 {
		t.Fatalf("n=1 rps=%v", got[0].Extra["rps"])
	}
}
