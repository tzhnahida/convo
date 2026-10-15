package convo

import (
	"context"
	"testing"
)

// TestRecordTrace_CarriesSceneAndStance **护栏 3「偏向可见」的数据源**。
//
// 反面: 不填 scene/speaker/stance → 管理页**看不到"它更愿意接谁的话"** → 红。
// 设计稿 L978: "一个看不见偏向的群友, 比一个没有偏向的群友更危险"。
//
// ⚠️ 这条测试顺带钉住了**列名与值个数对齐**:
// INSERT 三列而 VALUES 只给 16 个占位符的话, SQL 会报错 →
// 轨迹一条都落不下来 → len(tr)==0 → 红。
// (P7 那次事故就是这两半对不上, 而**调用方把错误吞了**。)
func TestRecordTrace_CarriesSceneAndStance(t *testing.T) {
	c := &sitCaptureChatter{}
	e, _ := groupSpeakEngine(t, c)
	if err := e.SpeakInGroup(context.Background(), "group:12345", 12345, "999", "在吗"); err != nil {
		t.Fatalf("SpeakInGroup: %v", err)
	}
	tr, err := e.Store.RecentTraces(1)
	if err != nil {
		t.Fatalf("RecentTraces: %v", err)
	}
	if len(tr) != 1 {
		t.Fatalf("应当落下 1 条轨迹, 实际 %d 条 —— INSERT 的列名与值个数对不上?", len(tr))
	}
	got := tr[0]
	if got.SceneKey != "group:12345" {
		t.Fatalf("轨迹没带场景键: %q —— 那就算不出「它更愿意接谁的话」", got.SceneKey)
	}
	if got.SpeakerUIN != "999" {
		t.Fatalf("轨迹没带发言人: %q", got.SpeakerUIN)
	}
	if got.Stance != string(StanceSpeak) {
		t.Fatalf("轨迹没带三态: %q", got.Stance)
	}
}
