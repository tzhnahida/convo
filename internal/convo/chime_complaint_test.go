package convo

import "testing"

// seedChatty 让"它最近确实在说话"这条前提成立 —— 2026-10-18 起,
// 判定"被抱怨过"除了词和 @, 还要看它当时到底在不在说。
func seedChatty(t *testing.T, e *Engine, sceneKey string) {
	t.Helper()
	for i := 0; i < ChimeComplaintMinRecentSelf; i++ {
		if _, err := e.Store.InsertMessageFull("ai", "qq", "text", "随便说说", MsgMeta{SceneKey: sceneKey}); err != nil {
			t.Fatal(err)
		}
	}
}

// ⚠️ 2026-10-16 误判的回归: 它在群里**两小时只说了 1 条**, 用户 @它说了句
// 「别刷屏闭嘴」, 词表命中 → 永久收敛到 1%。用户真正要的是"被骂话多才降级",
// 而那次它根本没在话多 —— 于是判据补一条事实依据: 它最近确实在说话。
func TestNoteChimeComplaint_NeedsEvidenceOfChatter(t *testing.T) {
	s := newTestStore(t)
	e := &Engine{Store: s}
	sc := SceneMsg{SceneKey: "group:1034712035", Text: "别刷屏闭嘴"}

	// ① 它最近没说话 → 不算收敛。
	if e.noteChimeComplaint(sc, true) {
		t.Fatal("它没在说话, 不该被判'被抱怨过'")
	}
	if ok, _ := s.ChimeComplained(e.ownerKey(), sc.SceneKey); ok {
		t.Fatal("标记不该落库")
	}

	// ② 它最近确实在说话 (3 条) + 被 @ 抱怨 → 收敛。
	for i := 0; i < ChimeComplaintMinRecentSelf; i++ {
		if _, err := s.InsertMessageFull("ai", "qq", "text", "随便说说", MsgMeta{SceneKey: sc.SceneKey}); err != nil {
			t.Fatal(err)
		}
	}
	if !e.noteChimeComplaint(sc, true) {
		t.Fatal("话多 + 被点名抱怨 → 应该收敛")
	}
	if ok, _ := s.ChimeComplained(e.ownerKey(), sc.SceneKey); !ok {
		t.Fatal("标记应该落库")
	}
}

// 反面: 没被 @ (群友之间互喷) → 永远不算到它头上。
func TestNoteChimeComplaint_IgnoresUnaddressed(t *testing.T) {
	s := newTestStore(t)
	e := &Engine{Store: s}
	sc := SceneMsg{SceneKey: "group:1034712035", Text: "你话真多"}
	for i := 0; i < 5; i++ {
		_, _ = s.InsertMessageFull("ai", "qq", "text", "随便说说", MsgMeta{SceneKey: sc.SceneKey})
	}
	if e.noteChimeComplaint(sc, false) {
		t.Fatal("没 @ 它的抱怨不许算")
	}
}
