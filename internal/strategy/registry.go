package strategy

func init() {
	Register(Turtle{})
	Register(MAVolume{})
	Register(HighTightFlag{})
	Register(LimitUpShakeout{})
	Register(UptrendLimitDown{})
	Register(RPSBreakout{})
}

// ByID 按 ID 查找策略。
func ByID(id string) (Strategy, bool) {
	for _, s := range All() {
		if s.ID() == id {
			return s, true
		}
	}
	return nil, false
}
