package baostock

import (
	"errors"
	"strings"
)

// ErrBlacklisted 并发/频率过高时服务端拉黑匿名账号（常为临时）。
var ErrBlacklisted = errors.New("baostock 账号被临时拉黑，请等 10～30 分钟后再继续回填")

// IsBlacklisted 是否为黑名单类错误（勿重试，否则封得更久）。
func IsBlacklisted(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrBlacklisted) {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "黑名单") || strings.Contains(s, "10001011")
}

// BasicStock 为证券基本信息（baostock query_stock_basic 一行）。
type BasicStock struct {
	Code   string
	Name   string
	Type   string
	Status string
}

// KBar 为日 K 一行（query_history_k_data_plus，字段见 kFields）。
type KBar struct {
	Date   string
	Open   float64
	High   float64
	Low    float64
	Close  float64
	Volume float64
	Amount float64
	Turn   float64
}
