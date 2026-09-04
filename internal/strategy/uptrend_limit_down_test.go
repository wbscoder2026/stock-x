package strategy

import (
	"testing"

	"github.com/wbscoder2026/stock-x/internal/store"
)

func TestUptrendLimitDown(t *testing.T) {
	hit := nBars("HIT", 61, func(i int, b *store.Bar) {
		c := 10 + float64(i)*0.2
		b.Open, b.High, b.Low, b.Close = c, c, c, c
		b.Volume = 100
		if i == 60 {
			y := 10 + 59*0.2
			b.Close = y * 0.90
			b.Open = y
			b.High = y
			b.Low = b.Close
			b.Volume = 300
		}
	})
	asOf := lastDate(hit)
	got := UptrendLimitDown{}.Run(map[string][]store.Bar{"HIT": hit}, asOf, nil)
	picked(t, got, "HIT")
}
