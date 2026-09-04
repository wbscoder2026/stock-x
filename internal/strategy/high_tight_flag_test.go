package strategy

import (
	"testing"

	"github.com/wbscoder2026/stock-x/internal/store"
)

func TestHighTightFlag(t *testing.T) {
	hit := nBars("HIT", 40, func(i int, b *store.Bar) {
		if i < 30 {
			c := 10 + float64(i)*(10.0/29)
			b.Open, b.High, b.Low, b.Close = c, c, 10, c
			b.Volume = 100
			return
		}
		b.Open, b.High, b.Low, b.Close = 18, 19, 17, 18
		b.Volume = 100
		if i == 39 {
			b.Volume = 50
		}
	})
	asOf := lastDate(hit)
	got := HighTightFlag{}.Run(map[string][]store.Bar{"HIT": hit}, asOf, nil)
	picked(t, got, "HIT")
}
