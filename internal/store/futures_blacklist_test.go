package store

import "testing"

func TestFuturesBlacklistAddListRemove(t *testing.T) {
	s := openTestStore(t)

	if err := s.AddFuturesBlacklist(BlacklistScopeVariety, "JM", "焦煤不看了"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddFuturesBlacklist(BlacklistScopeContract, "RB2701", "只屏蔽这个合约"); err != nil {
		t.Fatal(err)
	}

	list, err := s.ListFuturesBlacklist()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("条数=%d", len(list))
	}
	// 排序：contract 在前（c < v）
	if list[0].Scope != BlacklistScopeContract || list[0].Value != "RB2701" {
		t.Fatalf("排序不对：%+v", list[0])
	}
	if list[1].Note != "焦煤不看了" || list[1].CreatedAt == "" {
		t.Fatalf("字段不对：%+v", list[1])
	}

	// 重复加入 = 覆盖 note，不新增
	if err := s.AddFuturesBlacklist(BlacklistScopeContract, "RB2701", "改了备注"); err != nil {
		t.Fatal(err)
	}
	list, _ = s.ListFuturesBlacklist()
	if len(list) != 2 || list[0].Note != "改了备注" {
		t.Fatalf("重复加入应覆盖：%+v", list)
	}

	n, err := s.RemoveFuturesBlacklist(BlacklistScopeContract, "RB2701")
	if err != nil || n != 1 {
		t.Fatalf("删除 n=%d err=%v", n, err)
	}
	if n, _ := s.RemoveFuturesBlacklist(BlacklistScopeContract, "RB2701"); n != 0 {
		t.Fatalf("重复删除该返回 0，得到 %d", n)
	}
	list, _ = s.ListFuturesBlacklist()
	if len(list) != 1 || list[0].Value != "JM" {
		t.Fatalf("剩下的不对：%+v", list)
	}
}

func TestFuturesBlacklistScopeIsolated(t *testing.T) {
	s := openTestStore(t)
	// 同名不同 scope 互不影响（"JM" 品种 vs 某个叫 JM 的合约）
	if err := s.AddFuturesBlacklist(BlacklistScopeVariety, "JM", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.AddFuturesBlacklist(BlacklistScopeContract, "JM", ""); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.ListFuturesBlacklist(); len(list) != 2 {
		t.Fatalf("scope 应隔离：%+v", list)
	}
	if _, err := s.RemoveFuturesBlacklist(BlacklistScopeVariety, "JM"); err != nil {
		t.Fatal(err)
	}
	list, _ := s.ListFuturesBlacklist()
	if len(list) != 1 || list[0].Scope != BlacklistScopeContract {
		t.Fatalf("只该删掉 variety：%+v", list)
	}
}
