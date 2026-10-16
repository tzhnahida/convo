package convo

import (
	"testing"

	"github.com/tzhnahida/convo/internal/clock"
)

// TestRememberCarriesTheSceneKey
//
// ⚠️ **这条是让 §5.7 真的咬得住的那颗螺丝。**
//
// 只做读侧的话: sceneAffinity 永远拿到空场景 → 永远返回中性 → **降权一次都不会发生**,
// 而它在观测面上和"根本没接线"完全一样(测试绿、日志静默、界面正常)。
//
// 生产库实测: 119 条记忆**全部** scene_key 为空 —— 就是这个缺口的直接后果。
func TestRememberCarriesTheSceneKey(t *testing.T) {
	s := agTestStore(t)
	if err := s.SaveProfile(&PersonaProfile{Name: "小夜", Template: "bestie", CreatedTS: clock.TodayTS()}); err != nil {
		t.Fatal(err)
	}
	d := &DialogEngine{Store: s}
	in := DialogInput{SceneKey: DefaultSceneKey, UserText: "在吗"}
	frame := OutputFrame{Reply: []FrameItem{{Kind: "text", Text: "记住了"}}}
	frame.Remember = &FrameRemember{K: "fact", V: "ta 说过想去看海", Label: "想去看海"}
	d.SideEffects(frame, in, &PersonaProfile{Name: "小夜"})

	got, err := s.TopMemoriesFor(DefaultSceneKey, 10, clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range got {
		if m.V != "ta 说过想去看海" {
			continue
		}
		found = true
		if m.SceneKey != DefaultSceneKey {
			t.Errorf("记忆没带上场景键: %q —— 那 §5.7 永远无物可降", m.SceneKey)
		}
	}
	if !found {
		t.Fatal("remember 写进去的记忆取不到")
	}
	// ⚠️ 反面: **别的场景**取不到它(§7.3 的硬闸在起作用)。
	other, err := s.TopMemoriesFor("group:1034712035", 10, clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range other {
		if m.V == "ta 说过想去看海" {
			t.Fatal("私聊里的关系级记忆被别的场景取到了 —— §7.3 的硬闸漏了")
		}
	}
}
