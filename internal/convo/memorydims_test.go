package convo

import (
	"testing"

	"github.com/tzhnahida/convo/internal/clock"
)

// TestMemoriesList_ExposesAllThreeDimensions
//
// ⚠️ §7.3 的**三个正交维度必须一起暴露** —— 它们是"这条记忆为什么没进来"的
// **唯一**可查依据:
//
//	· 敏感度 → 能不能跨人用;
//	· 主体归属 → 对谁说;
//	· 时间 → 旧事实有没有被顶掉。
//
// 少任何一维, owner 看到的就是"这条记忆莫名其妙不见了"。
func TestMemoriesList_ExposesAllThreeDimensions(t *testing.T) {
	s := adminServer(t)
	if err := s.Store.UpsertMemory(MemoryDelta{
		MemID: "m-dim", K: "fact", V: "在群里说过的事", Label: "群里的事",
		Confidence: 0.8, Source: "user_said",
		SceneKey: "group:1034712035", Sensitivity: SensitivityPublic,
		SlotKey: "slot-x", ValidUntil: "2027-01-01 00:00:00",
	}, "active"); err != nil {
		t.Fatal(err)
	}
	out := getJSON(t, s.handleMemoriesList, "/api/v1/convo/memories")
	list, ok := out["memories"].([]interface{})
	if !ok || len(list) == 0 {
		t.Fatalf("没取到记忆: %v", out)
	}
	var hit map[string]interface{}
	for _, it := range list {
		m := it.(map[string]interface{})
		if m["mem_id"] == "m-dim" {
			hit = m
		}
	}
	if hit == nil {
		t.Fatal("刚写下的那条不在列表里")
	}
	// 三维逐项核对。
	want := map[string]string{
		"sensitivity": SensitivityPublic,     // 维一 敏感度
		"scene_key":   "group:1034712035",    // 维二 主体归属
		"slot_key":    "slot-x",              // 维三 时间
		"valid_until": "2027-01-01 00:00:00", // 维三 失效区间右端
		"source":      "user_said",
	}
	for k, v := range want {
		if got, _ := hit[k].(string); got != v {
			t.Errorf("字段 %q = %q, 想要 %q —— 少这一维就查不出它为什么没进来", k, got, v)
		}
	}
	// 检索诱导遗忘那两个也要在 (它解释"取用多但注入少")。
	for _, k := range []string{"inject_count", "last_injected_ts", "superseded_ts"} {
		if _, ok := hit[k]; !ok {
			t.Errorf("缺字段 %q", k)
		}
	}
	_ = clock.Now
}
