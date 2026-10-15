package convo

import (
	"strings"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────
// #15 §3.6.5c 配角的退出机制
//
// 「退出后转入**过去的人**: 可以被回忆提起, 但不参与当下。」
// 「**不许复活** —— 说过搬家的不能再若无其事地出现。」
// ─────────────────────────────────────────────────────────────

func castBible(t *testing.T, members ...CastMember) WorldBible {
	t.Helper()
	b := WorldBible{Self: "信使"}
	b.Cast = members
	return b
}

func castNow() time.Time { return time.Date(2026, 10, 15, 12, 0, 0, 0, time.Local) }

// TestRetireStaleCast_SixtyDaysWithoutMention
//
// ⚠️ 判据是「**最后一次被提及**」, 不是 StatusDay ——
// 后者只说明状态刷新过, 而刷新是凌晨自动跑的。拿它判的话**永远不会有配角退出**。
func TestRetireStaleCast_SixtyDaysWithoutMention(t *testing.T) {
	now := castNow()
	b := castBible(t,
		CastMember{Name: "可颂", Relation: RelationCompanion, LastSeenDay: now.AddDate(0, 0, -61).Format("2006-01-02")},
		CastMember{Name: "德克萨斯", Relation: RelationCompanion, LastSeenDay: now.AddDate(0, 0, -10).Format("2006-01-02")},
	)
	gone := RetireStaleCast(&b, now)
	if len(gone) != 1 || gone[0] != "可颂" {
		t.Fatalf("应当只退掉 61 天没提的那位, 实得 %v", gone)
	}
	if len(ActiveCast(b)) != 1 || ActiveCast(b)[0].Name != "德克萨斯" {
		t.Fatalf("当下生活层不对: %+v", ActiveCast(b))
	}
	if len(PastCast(b)) != 1 || PastCast(b)[0].RetireWhy == "" {
		t.Fatalf("过去的人没有理由 —— 与「忘了这个人」长得一模一样: %+v", PastCast(b))
	}
}

// TestRetireStaleCast_EmptyLastSeenIsNotProvided
//
// ⚠️ **零值 = 未提供 ≠ 极值**: 从没记过提及时间**不许**当成"六十天没见"——
// 折成后者会让所有新配角**一开始就是过去的人**。
func TestRetireStaleCast_EmptyLastSeenIsNotProvided(t *testing.T) {
	now := castNow()
	b := castBible(t, CastMember{Name: "新来的", Relation: RelationCompanion, Status: "刚认识"})
	if gone := RetireStaleCast(&b, now); len(gone) != 0 {
		t.Fatalf("从没记过提及时间却把它退掉了: %v", gone)
	}
	if len(ActiveCast(b)) != 1 {
		t.Fatal("新配角不在当下生活层里")
	}
	// 坏时刻同样**不动** (失败往"还在"倒)。
	b2 := castBible(t, CastMember{Name: "怪时间", LastSeenDay: "乱写"})
	if gone := RetireStaleCast(&b2, now); len(gone) != 0 {
		t.Fatalf("时刻解析不出却把它退掉了: %v", gone)
	}
}

// TestRetireCastMember_NeedsAReason
//
// ⚠️ 没有理由的退出**与"忘了这个人"长得一模一样** —— 所以 reason 是必填项。
func TestRetireCastMember_NeedsAReason(t *testing.T) {
	now := castNow()
	b := castBible(t, CastMember{Name: "店员小张", Relation: RelationCompanion})
	if RetireCastMember(&b, "店员小张", "", now) {
		t.Fatal("没有理由也能退 —— 那是静默删除")
	}
	if !RetireCastMember(&b, "店员小张", "那家店关门了", now) {
		t.Fatal("有理由却退不掉")
	}
	if len(PastCast(b)) != 1 || !strings.Contains(PastCast(b)[0].RetireWhy, "关门") {
		t.Fatalf("退出的理由没记下来: %+v", PastCast(b))
	}
	// 幂等: 再退一次不报成功(它已经不在当下层了)。
	if RetireCastMember(&b, "店员小张", "又关了一次", now) {
		t.Error("重复退同一个人却报成功")
	}
}

// TestNoteCastMentions_OnlyRealNames
//
// ⚠️ 这是 60 天沉降的**唯一输入** —— 它不记账, 那条规则就永远不触发。
func TestNoteCastMentions_OnlyRealNames(t *testing.T) {
	b := castBible(t,
		CastMember{Name: "可颂", Relation: RelationCompanion},
		CastMember{Name: "德克萨斯", Relation: RelationCompanion},
	)
	hit := NoteCastMentions(&b, "下午去找可颂搬箱子", "2026-10-15")
	if len(hit) != 1 || hit[0] != "可颂" {
		t.Fatalf("记账不对: %v", hit)
	}
	if b.Cast[0].LastSeenDay != "2026-10-15" {
		t.Errorf("提及日没写上: %q", b.Cast[0].LastSeenDay)
	}
	if b.Cast[1].LastSeenDay != "" {
		t.Error("没提到的人也被记上了")
	}
	// 没提到任何人 → 不改任何东西。
	if got := NoteCastMentions(&b, "今天天气不错", "2026-10-16"); len(got) != 0 {
		t.Errorf("空文本也记账了: %v", got)
	}
}

// TestPastCastLine_AllowsRecallForbidsRevival
//
// ⚠️ 三条一起测: **可以回忆** · **不许出现在今天** · **不许无缘无故复活**。
func TestPastCastLine_AllowsRecallForbidsRevival(t *testing.T) {
	b := castBible(t,
		CastMember{Name: "可颂", Retired: true, RetireWhy: "她搬家了"},
		CastMember{Name: "德克萨斯", Retired: false},
	)
	line := PastCastLine(b)
	if line == "" {
		t.Fatal("有过去的人却不注入 —— 那它会把搬走的人当成今天还在")
	}
	if !strings.Contains(line, "可颂") || !strings.Contains(line, "搬家") {
		t.Errorf("过去的人没带上理由:\n%s", line)
	}
	if !strings.Contains(line, "回忆") {
		t.Errorf("没有明确「可以回忆」:\n%s", line)
	}
	if !strings.Contains(line, "不许") && !strings.Contains(line, "不能当成今天") {
		t.Errorf("没有挡住「回到今天」:\n%s", line)
	}
	// 没有过去的人 → 一个字都不加。
	if got := PastCastLine(castBible(t, CastMember{Name: "只有当下"})); got != "" {
		t.Fatalf("没有过去的人却注入了: %q", got)
	}
}

// TestNightPeersFromCast_SkipsRetired
//
// ⚠️ **消费者尺子**: 一个已经搬走的人不该出现在今晚的 AI 群里 ——
// 那是"不许复活"最直白的一种违反。
func TestNightPeersFromCast_SkipsRetired(t *testing.T) {
	b := castBible(t,
		CastMember{Name: "可颂", Relation: RelationCompanion, Retired: true},
		CastMember{Name: "德克萨斯", Relation: RelationCompanion, Status: "在点货"},
	)
	peers := NightPeersFromCast(b)
	for _, p := range peers {
		if p.Name == "可颂" {
			t.Fatal("已经退出的配角还出现在夜间名册里 —— 那就是复活")
		}
	}
	if len(peers) != 1 {
		t.Fatalf("应当只剩 1 人, 实得 %d", len(peers))
	}
}
