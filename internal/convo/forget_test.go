package convo

import (
	"testing"
	"time"
)

func TestForgetRequested(t *testing.T) {
	for _, s := range []string{"别记我", "这些你忘了吧", "把我忘掉", "不要存我的信息"} {
		if !ForgetRequested(s) {
			t.Fatalf("%q 应当被认成遗忘请求 —— 漏判的代价是「用户说过别记我而它还在记」", s)
		}
	}
	for _, s := range []string{"", "今天天气不错", "记一下我明天要开会"} {
		if ForgetRequested(s) {
			t.Fatalf("%q 不是遗忘请求", s)
		}
	}
}

// ⚠️ 本批最要紧的一条: **删除必须配墓碑**, 否则下一轮会话提取会再学一遍。
func TestForgetSubject_NotReversibleBySocialLearning(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	owner := e.ownerKey()
	scene := SceneKeyPrivate("999")
	// 先让它记住一些关于这个人的事。
	if err := e.Store.UpsertMemory(MemoryDelta{MemID: "f1", K: "work", Label: "工作",
		Kind: "fact", V: "在准备跳槽", Confidence: 0.9, Source: "user_said", SceneKey: scene}, "active"); err != nil {
		t.Fatal(err)
	}
	if err := SaveBond(e.Store.DB, owner, scene, Bond{Closeness: 0.8}); err != nil {
		t.Fatal(err)
	}
	var before int
	if err := e.Store.DB.QueryRow("SELECT COUNT(*) FROM convo_memory WHERE scene_key = ?", scene).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before == 0 {
		t.Fatal("前置条件不成立")
	}

	rep, err := e.Store.ForgetSubject("999", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Total == 0 {
		t.Fatal("一行都没删 —— 遗忘权是**物理删除**, 不是标记")
	}
	if rep.Deleted["convo_memory"] == 0 || rep.Deleted["convo_bond"] == 0 {
		t.Fatalf("删除清单不完整: %v", rep.Deleted)
	}
	// 物理的: 库里真的没有了。
	var after int
	e.Store.DB.QueryRow("SELECT COUNT(*) FROM convo_memory WHERE scene_key = ?", scene).Scan(&after)
	if after != 0 {
		t.Fatalf("还有 %d 行记忆 —— 那不是物理删除", after)
	}
	// ⚠️ **不可被社会学习逆转**: 再写一次必须被拒。
	if err := e.Store.UpsertMemory(MemoryDelta{MemID: "f2", K: "work", Label: "工作",
		Kind: "fact", V: "又在准备跳槽", Confidence: 0.9, Source: "user_said", SceneKey: scene}, "active"); err != nil {
		t.Fatal(err)
	}
	var again int
	e.Store.DB.QueryRow("SELECT COUNT(*) FROM convo_memory WHERE scene_key = ?", scene).Scan(&again)
	if again != 0 {
		t.Fatal("遗忘之后又被学回来了 —— 墓碑没生效, 这是「不可被逆转」的全部内容")
	}
	if !e.Store.SubjectForgotten("999") {
		t.Fatal("墓碑没立上")
	}
	// 别的场景不受影响。
	if e.Store.ForgetScene("group:1") {
		t.Fatal("群场景不应当被私聊的遗忘权波及")
	}
}

// §7.4 第一条: 边界**降权而非永久禁止**。
func TestBoundaryStrengthAt(t *testing.T) {
	now := time.Now()
	if got := BoundaryStrengthAt(now.Format(bodyTSLayout), now); got != "hard" {
		t.Fatalf("刚升格应当是硬线, got %q", got)
	}
	soft := now.Add(-(BoundaryHardDays + 1) * 24 * time.Hour).Format(bodyTSLayout)
	if got := BoundaryStrengthAt(soft, now); got != "soft" {
		t.Fatalf("过了硬线期应当软化, got %q", got)
	}
	none := now.Add(-(BoundarySoftDays + 1) * 24 * time.Hour).Format(bodyTSLayout)
	if got := BoundaryStrengthAt(none, now); got != "none" {
		t.Fatalf("过了软化期应当不再注入, got %q —— 「否则越骂越畏缩」", got)
	}
	// 读不出来的时刻 → 保守按保护期处理。
	if got := BoundaryStrengthAt("前天", now); got != "hard" {
		t.Fatalf("解析不了应当按 hard 处理 (少犯错), got %q", got)
	}
}

func TestLoadBoundaryLines_DecaysByAge(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	owner := e.ownerKey()
	scene := "group:1"
	for i := 0; i < FeedbackPromoteThreshold; i++ {
		if _, err := RecordFeedback(e.Store.DB, owner, scene, FeedbackScold, "工资"); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	lines := e.Store.LoadBoundaryLines(owner, scene, now)
	if len(lines) != 1 {
		t.Fatalf("want 1, got %v", lines)
	}
	// 把它推到过期。
	old := now.Add(-(BoundarySoftDays + 1) * 24 * time.Hour).Format(bodyTSLayout)
	if _, err := e.Store.DB.Exec("UPDATE convo_feedback SET updated_ts = ? WHERE owner=? AND scene_key=?", old, owner, scene); err != nil {
		t.Fatal(err)
	}
	if got := e.Store.LoadBoundaryLines(owner, scene, now); len(got) != 0 {
		t.Fatalf("过期边界仍然被注入: %v —— 一条永久硬线会让它在这个话题上永远闭嘴", got)
	}
}
