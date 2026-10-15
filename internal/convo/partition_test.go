package convo

import "testing"

// ——— §9 消息分区: origin + scene_key ———
//
// 这两个列存在的**唯一理由**是 §3.7 的夜间批算:
// 它补算昨天的 AI↔AI 对话, 写回 ts 落在昨天。没有 origin,
// 那些行会被 recentDialog 当成"真人昨天说过的话"读进 L1 —— 而它从没发生过。

// TestMessages_OriginPartition 反面: RecentMessages 去掉 origin 过滤 → 红。
func TestMessages_OriginPartition(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.InsertMessage("user", "qq", "text", "真人说的"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertMessageEx("ai", "qq", "text", "AI 之间说的", OriginAIToAI, "group:1"); err != nil {
		t.Fatal(err)
	}
	msgs, err := s.RecentMessages(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("L1 应当只拿到 1 条 (真人那条), 实际 %d 条 —— AI↔AI 混进来了", len(msgs))
	}
	if msgs[0].Payload != "真人说的" {
		t.Fatalf("L1 拿到了 AI↔AI 的行: %q", msgs[0].Payload)
	}
}

// TestInsertMessageEx_ZeroOriginFallsBack 反面: 零值被当成"空来源" → 红。
// 纪律: 零值 = 未提供 ≠ 极值。忘了传 origin 不该产生一行"来源不明"的消息。
func TestInsertMessageEx_ZeroOriginFallsBack(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.InsertMessageEx("ai", "qq", "text", "x", "", ""); err != nil {
		t.Fatal(err)
	}
	var origin, scene string
	if err := s.DB.QueryRow("SELECT COALESCE(origin,''), COALESCE(scene_key,'') FROM convo_messages ORDER BY id DESC LIMIT 1").
		Scan(&origin, &scene); err != nil {
		t.Fatal(err)
	}
	if origin != OriginAIToUser {
		t.Fatalf("零值 origin 应回落到 ai_to_user (按 dir 推), 得到 %q", origin)
	}
	if scene != DefaultSceneKey {
		t.Fatalf("零值 scene_key 应回落到默认场景, 得到 %q", scene)
	}
}

// TestUpsertMemory_PrivateNeverPersisted §6.4.2 的唯一执行点。
// 反面: 去掉 UpsertMemory 开头的私域过滤 → 红。
func TestUpsertMemory_PrivateNeverPersisted(t *testing.T) {
	s := newTestStore(t)
	priv := MemoryDelta{MemID: "priv_health", K: "health", V: "我最近在吃抗抑郁药", Label: "健康", Confidence: 0.9}
	if err := s.UpsertMemory(priv, "active"); err != nil {
		t.Fatalf("刻意丢弃不该报错 (它不是故障): %v", err)
	}
	var n int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM convo_memory WHERE mem_id='priv_health'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("私域内容落库了 —— 它一旦落库就进了备份/检索/上下文, 事后删除追不回来")
	}
	// 反面: 过滤不能宽到"什么都不存" —— 普通偏好必须照常落库。
	ok := MemoryDelta{MemID: "ok_food", K: "food", V: "喜欢吃火锅", Label: "吃的", Confidence: 0.9}
	if err := s.UpsertMemory(ok, "active"); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM convo_memory WHERE mem_id='ok_food'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("普通内容被误杀了 —— 防线太宽, 它会把正常记忆一起挡掉")
	}
}
