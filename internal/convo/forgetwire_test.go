package convo

import (
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────
// §7.4 **遗忘权** —— 用户在对话里说"别记我"
//
// 设计稿 L1582 / L1859:
//
//	> **遗忘权**: 任何人说"**别记我**" → **物理删除**该主体的全部记忆,
//	> 且**不可被它的社会学习逆转**。
//	> 遗忘权**不能被"它学到的边界"覆盖**。
//
// ⚠️ 而 `ForgetRequested` 那个判据**从来没有调用者** ——
// 也就是说这条权利在接线之前**只存在于代码里**。
// ─────────────────────────────────────────────────────────────

func forgetEngine(t *testing.T) (*Engine, string) {
	t.Helper()
	e := gateEngine(t)
	if err := EnsureForgetSchema(e.Store.DB); err != nil {
		t.Fatal(err)
	}
	if err := EnsureAgencySchema(e.Store); err != nil {
		t.Fatal(err)
	}
	return e, e.ownerKey()
}

// seedPrivateMemory 让"它记得这个人"成立。
func seedPrivateMemory(t *testing.T, e *Engine, uin, memID, v string) {
	t.Helper()
	if err := e.Store.UpsertMemory(MemoryDelta{
		MemID: memID, K: "work", Label: "工作", Kind: "fact", V: v,

		Confidence: 0.9, Source: "user_said", SceneKey: SceneKeyPrivate(uin),
	}, "active"); err != nil {
		t.Fatal(err)
	}
}

// memCount 数某个私有场景还有几条记忆。
func memCount(t *testing.T, e *Engine, uin string) int {
	t.Helper()
	var n int
	if err := e.Store.DB.QueryRow("SELECT COUNT(*) FROM convo_memory WHERE scene_key=?",
		SceneKeyPrivate(uin)).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestForgetRight_ErasesOnExplicitRequest (正面, 本条最要紧)
//
// ⚠️ 「**不可被逆转**」那一半由墓碑保证 —— 所以墓碑必须一起钉。
func TestForgetRight_ErasesOnExplicitRequest(t *testing.T) {
	e, uin := forgetEngine(t)
	seedPrivateMemory(t, e, uin, "f1", "在准备跳槽")
	if memCount(t, e, uin) == 0 {
		t.Fatal("夹具坏了: 先得记得点什么")
	}

	if !e.noteForgetRequest(uin, "以后别记我了") {
		t.Fatal("明确了是遗忘请求却没认出来")
	}
	if n := memCount(t, e, uin); n != 0 {
		t.Fatalf("**说了别记我, 而它还记着 %d 条** —— 这条权利没生效", n)
	}
	// ⚠️ 而**墓碑必须一起立**: 没有它, 下一轮会话提取会再学一遍。
	if !e.Store.SubjectForgotten(uin) {
		t.Fatal("删了但没立碑 —— **可被逆转**, 而设计稿要的是不可逆转")
	}
}

// TestForgetRight_TombstoneBlocksRelearning (正面, 不可逆那一半)
//
// ⚠️ 立碑之后**批量写回必须被挡住** —— 否则下一轮它又"想起来"了。
func TestForgetRight_TombstoneBlocksRelearning(t *testing.T) {
	e, uin := forgetEngine(t)
	seedPrivateMemory(t, e, uin, "f1", "在准备跳槽")
	e.noteForgetRequest(uin, "别记我")
	// 模拟下一轮会话提取: 批量写回同一个场景。
	err := e.Store.ApplySessionDelta("sess-x", SessionDelta{Memories: []MemoryDelta{
		{MemID: "f2", K: "work", Label: "工作", Kind: "fact", V: "又提了一次跳槽",
			Confidence: 0.9, Source: "user_said", SceneKey: SceneKeyPrivate(uin)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if n := memCount(t, e, uin); n != 0 {
		t.Fatalf("**立碑之后又学回来了 %d 条** —— 那「不可逆转」就没做到", n)
	}
}

// TestForgetRight_SoftPhraseDoesNotDelete (反面)
//
// ⚠️ 「忘了吧」可以是在说某件具体的事, 而 `ForgetSubject` 立的是**永久**的碑
// (这个人从此不再被记)。误判一次 = **不可逆地**抹掉一个人。
func TestForgetRight_SoftPhraseDoesNotDelete(t *testing.T) {
	e, uin := forgetEngine(t)
	seedPrivateMemory(t, e, uin, "f1", "在准备跳槽")
	if !e.noteForgetRequest(uin, "那件事你忘了吧") {
		t.Fatal("含糊的也算「疑似遗忘请求」—— 它该被认出来(只是不删)")
	}
	if n := memCount(t, e, uin); n == 0 {
		t.Fatal("**含糊的一句话把这个人抹掉了** —— 措辞不明时不许做不可逆的事")
	}
	if e.Store.SubjectForgotten(uin) {
		t.Fatal("含糊的话立了碑 —— 那这个人从此不再被记")
	}
}

// TestForgetRight_UnrelatedTextIsIgnored (反面)
func TestForgetRight_UnrelatedTextIsIgnored(t *testing.T) {
	e, uin := forgetEngine(t)
	seedPrivateMemory(t, e, uin, "f1", "在准备跳槽")
	for _, s := range []string{"记一下我明天要开会", "今天天气不错", "我们聊点别的"} {
		if e.noteForgetRequest(uin, s) {
			t.Fatalf("%q 被当成了遗忘请求", s)
		}
	}
	if n := memCount(t, e, uin); n == 0 {
		t.Fatal("无关的话把记忆删了")
	}
}

// TestForgetRight_BeatsSocialJudgment (反面, 设计稿最后一句)
//
// ⚠️ 设计稿 L1859: 「遗忘权**不能被"它学到的边界"覆盖**」。
// 这里用一个**极亲近**的关系量来试 —— 不许有任何"关系够熟就网开一面"的分支。
func TestForgetRight_BeatsSocialJudgment(t *testing.T) {
	e, uin := forgetEngine(t)
	if err := EnsureBondSchema(e.Store.DB); err != nil {
		t.Fatal(err)
	}
	if err := SaveBond(e.Store.DB, e.ownerKey(), SceneKeyPrivate(uin), Bond{
		Closeness: 1.0, Regard: 1.0, Familiarity: 99,
	}); err != nil {
		t.Fatal(err)
	}
	seedPrivateMemory(t, e, uin, "f1", "在准备跳槽")
	e.noteForgetRequest(uin, "别记我")
	if n := memCount(t, e, uin); n != 0 {
		t.Fatalf("关系很熟, 于是没删(%d 条) —— **遗忘权被社交判断覆盖了**", n)
	}
	_ = time.Now()
}
