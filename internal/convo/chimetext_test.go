package convo

import "testing"

// 纯表情/图片占位不是"话题"。
func TestHasSubstantiveText(t *testing.T) {
	no := []string{
		"[QQ表情178][QQ表情178][QQ表情178]",
		"[表情]",
		"(照片) [图: 展示游戏角色的属性面板]",
		"(表情 — 嘲讽) [图: 表情 — 嘲讽 厌]",
		"？？ ",
		"",
	}
	for _, s := range no {
		if hasSubstantiveText(s) {
			t.Errorf("%q 不该算有实质文字", s)
		}
	}
	yes := []string{"在吗", "这个表情包好可爱", "哈哈哈", "nV", "8点可以吗"}
	for _, s := range yes {
		if !hasSubstantiveText(s) {
			t.Errorf("%q 应当算有实质文字", s)
		}
	}
}

// 2026-10-22 实测现场: 群友刷「[QQ表情178]×3」, 它插了一句「？刷屏呢」。
// 根因: bigram「表情」命中了它学过的词。纯占位消息不许触发话题插话。
func TestChimeTopicMatch_StickerSpamIsNotATopic(t *testing.T) {
	e := chimeEngine(t, 0)
	if _, err := e.Store.SaveLearned(LearnedItem{Kind: LearnFact, TopicKey: "表情",
		Content: "表情包真有意思", Origin: "自测"}); err != nil {
		t.Fatal(err)
	}
	if e.chimeTopicMatch(SceneMsg{SceneKey: "group:7", Text: "[QQ表情178][QQ表情178]"}) {
		t.Fatal("纯表情消息被判成了话题命中")
	}
	if !e.chimeTopicMatch(SceneMsg{SceneKey: "group:7", Text: "这个表情包好可爱"}) {
		t.Fatal("有文字的同话题消息应当命中")
	}
}
