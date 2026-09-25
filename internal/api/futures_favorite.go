package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/wbscoder2026/stock-x/internal/futures"
	"github.com/wbscoder2026/stock-x/internal/store"
)

type favoriteBody struct {
	Name               string         `json:"name"`
	Note               string         `json:"note"`
	Params             futures.Params `json:"params"`
	OriginSymbol       string         `json:"origin_symbol"`
	OriginWinRate      float64        `json:"origin_win_rate"`
	OriginAvgReturn    float64        `json:"origin_avg_return"`
	OriginAvgR         float64        `json:"origin_avg_r"`
	OriginProfitFactor float64        `json:"origin_profit_factor"`
	OriginTrades       int            `json:"origin_trades"`
}

type favoriteView struct {
	ID                 int64          `json:"id"`
	Name               string         `json:"name"`
	Note               string         `json:"note"`
	Params             futures.Params `json:"params"`
	OriginSymbol       string         `json:"origin_symbol"`
	OriginWinRate      float64        `json:"origin_win_rate"`
	OriginAvgReturn    float64        `json:"origin_avg_return"`
	OriginAvgR         float64        `json:"origin_avg_r"`
	OriginProfitFactor float64        `json:"origin_profit_factor"`
	OriginTrades       int            `json:"origin_trades"`
	CreatedAt          string         `json:"created_at"`
	UpdatedAt          string         `json:"updated_at"`
}

func (s *Server) futuresFavoritesList(w http.ResponseWriter, _ *http.Request) {
	rows, err := s.Store.ListFuturesFavorites()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]favoriteView, 0, len(rows))
	for _, row := range rows {
		view, err := favoriteToView(row)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		out = append(out, view)
	}
	writeOK(w, out)
}

func (s *Server) futuresFavoritesCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Items []favoriteBody `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "无效 JSON")
		return
	}
	if len(body.Items) == 0 {
		writeErr(w, http.StatusBadRequest, "没有要保存的配置")
		return
	}
	out := make([]favoriteView, 0, len(body.Items))
	for _, item := range body.Items {
		row, err := favoriteFromBody(item)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		created, err := s.Store.CreateFuturesFavorite(row)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		view, err := favoriteToView(created)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		out = append(out, view)
	}
	writeOK(w, out)
}

func (s *Server) futuresFavoritesUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "非法收藏 id")
		return
	}
	var body favoriteBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "无效 JSON")
		return
	}
	row, err := favoriteFromBody(body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	row.ID = id
	updated, err := s.Store.UpdateFuturesFavorite(row)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	view, err := favoriteToView(updated)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOK(w, view)
}

func (s *Server) futuresFavoritesDelete(w http.ResponseWriter, r *http.Request) {
	if idText := r.PathValue("id"); idText != "" {
		id, err := strconv.ParseInt(idText, 10, 64)
		if err != nil || id <= 0 {
			writeErr(w, http.StatusBadRequest, "非法收藏 id")
			return
		}
		n, err := s.Store.DeleteFuturesFavorites([]int64{id})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeOK(w, map[string]int{"removed": n})
		return
	}
	var body struct {
		IDs []int64 `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "无效 JSON")
		return
	}
	n, err := s.Store.DeleteFuturesFavorites(body.IDs)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeOK(w, map[string]int{"removed": n})
}

func (s *Server) futuresFavoritesScan(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs       []int64 `json:"ids"`
		Workers   int     `json:"workers"`
		MinTrades int     `json:"min_trades"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "无效 JSON")
		return
	}
	if len(body.IDs) == 0 {
		writeErr(w, http.StatusBadRequest, "没有要扫描的配置")
		return
	}
	if s.Bars == nil {
		writeErr(w, http.StatusInternalServerError, "没有行情源")
		return
	}
	configs := make([]futures.FavConfig, 0, len(body.IDs))
	for _, id := range body.IDs {
		row, err := s.Store.GetFuturesFavorite(id)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		view, err := favoriteToView(row)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		configs = append(configs, futures.FavConfig{ID: view.ID, Name: view.Name, Params: view.Params})
	}
	res, err := futures.ScanAcross(r.Context(), s.Bars, futures.ListVarieties(), configs, body.Workers, body.MinTrades)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeOK(w, res)
}

func favoriteFromBody(body favoriteBody) (store.FuturesFavorite, error) {
	name := strings.TrimSpace(body.Name)
	if name == "" {
		return store.FuturesFavorite{}, errString("收藏名称不能为空")
	}
	if utf8.RuneCountInString(name) > 80 {
		return store.FuturesFavorite{}, errString("收藏名称不能超过 80 字")
	}
	note := strings.TrimSpace(body.Note)
	if utf8.RuneCountInString(note) > 500 {
		return store.FuturesFavorite{}, errString("备注不能超过 500 字")
	}
	raw, err := json.Marshal(body.Params)
	if err != nil {
		return store.FuturesFavorite{}, err
	}
	return store.FuturesFavorite{
		Name: name, Note: note, ParamsJSON: string(raw),
		OriginSymbol:  strings.TrimSpace(body.OriginSymbol),
		OriginWinRate: body.OriginWinRate, OriginAvgReturn: body.OriginAvgReturn,
		OriginAvgR: body.OriginAvgR, OriginProfitFactor: body.OriginProfitFactor,
		OriginTrades: body.OriginTrades,
	}, nil
}

func favoriteToView(row store.FuturesFavorite) (favoriteView, error) {
	var params futures.Params
	if err := json.Unmarshal([]byte(row.ParamsJSON), &params); err != nil {
		return favoriteView{}, err
	}
	return favoriteView{
		ID: row.ID, Name: row.Name, Note: row.Note, Params: params,
		OriginSymbol: row.OriginSymbol, OriginWinRate: row.OriginWinRate,
		OriginAvgReturn: row.OriginAvgReturn, OriginAvgR: row.OriginAvgR,
		OriginProfitFactor: row.OriginProfitFactor, OriginTrades: row.OriginTrades,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}, nil
}

type errString string

func (e errString) Error() string { return string(e) }
