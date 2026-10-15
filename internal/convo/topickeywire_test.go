package convo

import (
	"testing"

	"github.com/tzhnahida/VocabLock/internal/clock"
)

// TestTopicOf_TwoTiersAndTheirOrder
//
// ⚠️ 话题键是**两档**: 夜间的**话题词表**优先, 命中的**梗**兜底。
//
// 原来代码里那句注释写的是"当前取命中的梗"(更早的形态) ——
// 一句描述错误形态的注释比没有注释更坏: 读的人会照着它去别处找 bug。
func TestTopicOf_TwoTiersAndTheirOrder(t *testing.T) {
	e, s := heatEngine(t)
	owner := e.ownerKey()

	// ——— 一档: 词表 ———
	if err := s.SaveTopicKeys(owner, clock.Today(), []string{"格斗游戏", "星空笔记"}); err != nil {
		t.Fatal(err)
	}
	if got := e.topicOf("昨晚那个格斗游戏你玩了吗"); got != "格斗游戏" {
		t.Fatalf("词表没命中: %q", got)
	}
	// ⚠️ **最长优先**: 两个键都能命中时取更长的那个。
	if err := s.SaveTopicKeys(owner, clock.Today(), []string{"游戏", "格斗游戏"}); err != nil {
		t.Fatal(err)
	}
	if got := e.topicOf("聊格斗游戏吧"); got != "格斗游戏" {
		t.Fatalf("应当取最长的键, 实得 %q", got)
	}
	// ⚠️ 两个字以下不算键 —— 否则"我"会命中一切。
	if err := s.SaveTopicKeys(owner, clock.Today(), []string{"我"}); err != nil {
		t.Fatal(err)
	}
	if got := e.topicOf("我今天很累"); got != "" {
		t.Fatalf("单字被当成了话题键: %q", got)
	}
}

// TestTopicOf_NotInTheVocabularyMeansNoReveal
//
// ⚠️ **这一条钉的是那个"窄口"本身**, 而它同时说明窄口是**刻意的**:
// 词表由夜间批抽取(生产实测一天 5 个键)加黑话兜底 ——
// 要拓宽只能每轮抽一次话题, 也就是每条消息一次 LLM 调用。
// 那不是"补个小功能", 是给热路径加一次网络往返。
func TestTopicOf_NotInTheVocabularyMeansNoReveal(t *testing.T) {
	e, s := heatEngine(t)
	if err := s.SaveTopicKeys(e.ownerKey(), clock.Today(), []string{"星空笔记"}); err != nil {
		t.Fatal(err)
	}
	if got := e.topicOf("星空笔记写完了"); got == "" {
		t.Fatal("词表里的键没命中 —— 那条路根本没接上")
	}
	// 不在表里 → 没有话题键。
	got := e.topicOf("我想聊聊天气")
	if got != "" {
		// 唯一允许的非空来源是**黑话兜底** —— 那说明这句话真的命中了黑话。
		if hits, err := s.FindSlangHit("我想聊聊天气"); err != nil || len(hits) == 0 {
			t.Fatalf("闭集外的一句话拿到了话题键 %q, 而它既不在词表也没命中黑话", got)
		}
	}
	// 空文本 → 空键 (它不该命中任何东西)。
	if got := e.topicOf("   "); got != "" {
		t.Fatalf("空文本拿到了话题键: %q", got)
	}
}
