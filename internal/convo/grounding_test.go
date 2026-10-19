package convo

import "testing"

// ⚠️ 2026-10-18/19 审核机的打回里, 一半以上是这类: 编造自己做过/看过什么。
// 这些用例钉的就是"不许编"的三条边。
func TestGrounding_CatchesInventedExperience(t *testing.T) {
	in := DialogInput{UserText: "你在玩什么"}
	if got := GroundingViolations("我玩过RTS 挺好上手的", in); len(got) == 0 {
		t.Fatal("凭空说自己玩过 RTS, 没被算出来")
	}
	// 群友聊过 ≠ 它经历过: 聊天里出现 RTS 也**不算**出处。
	in2 := DialogInput{UserText: "RTS 好玩吗", History: "群友: 我最近在打RTS"}
	if got := GroundingViolations("我玩过RTS", in2); len(got) == 0 {
		t.Fatal("群友聊过就被当成自己的经历 —— 这正是那次事故")
	}
}

func TestGrounding_AllowsGroundedExperience(t *testing.T) {
	in := DialogInput{Memories: []Memory{{MemID: "m1", V: "以前玩过RTS, 喜欢星际"}}}
	if got := GroundingViolations("我玩过RTS啊", in); len(got) != 0 {
		t.Fatalf("记忆里有出处却被拦: %v", got)
	}
	// 今天的行程也算出处。
	in2 := DialogInput{DayPlan: DayPlan{Items: []DayItem{{What: "去图书馆还书"}}}}
	if got := GroundingViolations("我去过图书馆了", in2); len(got) != 0 {
		t.Fatalf("日程里有出处却被拦: %v", got)
	}
}

func TestGrounding_HedgePasses(t *testing.T) {
	in := DialogInput{}
	for _, s := range []string{"我好像玩过RTS", "我记不清了", "我瞎猜的", "我可能去过吧"} {
		if got := GroundingViolations(s, in); len(got) != 0 {
			t.Fatalf("%q 是带不确定语气的写法, 不该拦: %v", s, got)
		}
	}
}

func TestGrounding_ImageDetailsNeedAnImage(t *testing.T) {
	// 这一轮没有图 → 说"资料卡上写着"必错。
	if got := GroundingViolations("你资料卡上写着舞 活跃才半小时", DialogInput{}); len(got) == 0 {
		t.Fatal("没图却编图里的细节, 没被算出来")
	}
	// 有图 → 放行 (细节对不对交审核机)。
	if got := GroundingViolations("你资料卡上写着舞", DialogInput{ImagePath: "/tmp/a.jpg"}); len(got) != 0 {
		t.Fatalf("有图却拦了: %v", got)
	}
	// 群路径把图写进 UserText 也算有图。
	if got := GroundingViolations("图上这是街霸6", DialogInput{UserText: "(照片) [图: 街霸6资料卡]"}); len(got) != 0 {
		t.Fatalf("群路径的图被当成没有图: %v", got)
	}
}

func TestGrounding_LeavesOrdinaryTalkAlone(t *testing.T) {
	in := DialogInput{}
	for _, s := range []string{"我也没吃呢", "布丁算不算午饭啊", "？", "等你凉了再吃", "我今天不想动"} {
		if got := GroundingViolations(s, in); len(got) != 0 {
			t.Fatalf("普通聊天被误拦: %q → %v", s, got)
		}
	}
}
