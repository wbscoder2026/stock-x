package store

import "testing"

func TestFuturesFavoriteCRUD(t *testing.T) {
	s := openTestStore(t)
	created, err := s.CreateFuturesFavorite(FuturesFavorite{
		Name: " 焦煤日内 ", Note: "扫出来的", ParamsJSON: `{"period":"5","rr":1.5}`,
		OriginSymbol: "JM0", OriginWinRate: 0.6, OriginAvgReturn: 0.01, OriginTrades: 12,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == 0 || created.Name != "焦煤日内" || created.OriginWinRate != 0.6 {
		t.Fatalf("%+v", created)
	}
	if _, err := s.CreateFuturesFavorite(FuturesFavorite{Name: "", ParamsJSON: `{}`}); err == nil {
		t.Fatal("空名称应拒绝")
	}
	if _, err := s.CreateFuturesFavorite(FuturesFavorite{Name: "x", ParamsJSON: "  "}); err == nil {
		t.Fatal("空参数应拒绝")
	}

	list, err := s.ListFuturesFavorites()
	if err != nil || len(list) != 1 {
		t.Fatalf("list=%v err=%v", list, err)
	}

	updated, err := s.UpdateFuturesFavorite(FuturesFavorite{ID: created.ID, Name: "改名", Note: "", ParamsJSON: `{"period":"15","rr":2}`})
	if err != nil || updated.Name != "改名" || updated.ParamsJSON != `{"period":"15","rr":2}` {
		t.Fatalf("%+v err=%v", updated, err)
	}
	if updated.OriginSymbol != "JM0" {
		t.Fatal("改参数不该清掉来源统计")
	}
	if _, err := s.UpdateFuturesFavorite(FuturesFavorite{ID: 999, Name: "无", ParamsJSON: `{}`}); err == nil {
		t.Fatal("不存在的收藏应报错")
	}

	n, err := s.DeleteFuturesFavorites([]int64{created.ID, created.ID})
	if err != nil || n != 1 {
		t.Fatalf("deleted=%d err=%v", n, err)
	}
	if list, _ = s.ListFuturesFavorites(); len(list) != 0 {
		t.Fatalf("删完还应为空：%v", list)
	}
}
