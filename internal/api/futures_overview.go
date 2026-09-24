package api

import (
	"net/http"

	"github.com/wbscoder2026/stock-x/internal/futures"
)

// catalogService 懒初始化 + 复用（全市场合约清单带 10 分钟缓存）。
func (s *Server) catalogService() *futures.ContractCatalog {
	s.catalogOnce.Do(func() {
		if s.Catalog == nil {
			s.Catalog = futures.NewContractCatalog(&futures.Client{})
		}
	})
	return s.Catalog
}

// futuresOverview 期货总览：全部品种 + 各自的月份合约（实时价另走 /api/futures/quotes）。
func (s *Server) futuresOverview(w http.ResponseWriter, r *http.Request) {
	writeOK(w, s.catalogService().All(r.Context()))
}
