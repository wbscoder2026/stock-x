package indicator

import "math"

// SMA 简单移动平均；窗口不足或 n<=0 的位置为 NaN。
func SMA(xs []float64, n int) []float64 {
	out := nanSlice(len(xs))
	if n <= 0 {
		return out
	}
	var sum float64
	for i, x := range xs {
		sum += x
		if i >= n {
			sum -= xs[i-n]
		}
		if i >= n-1 {
			out[i] = sum / float64(n)
		}
	}
	return out
}

// RollingMax 滚动最高；窗口不足或 n<=0 的位置为 NaN。
func RollingMax(xs []float64, n int) []float64 {
	out := nanSlice(len(xs))
	if n <= 0 {
		return out
	}
	for i := n - 1; i < len(xs); i++ {
		m := xs[i-n+1]
		for j := i - n + 2; j <= i; j++ {
			if xs[j] > m {
				m = xs[j]
			}
		}
		out[i] = m
	}
	return out
}

func nanSlice(n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = math.NaN()
	}
	return out
}
