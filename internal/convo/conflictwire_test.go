package convo

import (
	"strings"
	"testing"
	"time"

	"github.com/tzhnahida/convo/internal/clock"
)

// ─────────────────────────────────────────────────────────────
// §6.9 冲突介入 —— 判定侧的接线
//
// 这一节原来只接了一半: 「怎么劝」(conflictActNote) 已接,
// 「要不要劝」(GateConflict) **生产调用者为零**。
//
// ⚠️ 而"怎么劝"接上了、"要不要劝"没接, 是最难看出来的一种半接线:
// **它会劝架, 只是劝得毫无节制** —— 而那看起来像"它很热心"。
// ─────────────────────────────────────────────────────────────

// cflSeedWindow 把一段冲突窗口**真的写进库里** —— 接线测试必须走
// 「从库里读窗口」那条真实路径, 而不是把切片直接喂给 GateConflict。
func cflSeedWindow(t *testing.T, s *Store, sceneKey string, msgs []ConflictMsg, endAt time.Time) {
	t.Helper()
	// ⚠️ **必须冻结时钟逐条推进。**
	//
	// `InsertMessageFull` 的 ts 取自 `clock.Now()` —— 不推的话六条消息全是同一个时刻,
	// 于是 ④「回复间隔骤短」**永远算不出来**(实测 MedianGapSec=0), 而
	// 三信号门槛(② 互相 @ 在库里没有数据, 见 conflictWindowOf 的注释)本来就要求 3/3。
	// 第一版就是这么写的, 结果信号只中了 1 个, 而失败信息只说"没有抬成发言"。
	old := clock.Now
	t.Cleanup(func() { clock.Now = old })
	for _, m := range msgs {
		at := m.TS
		clock.Now = func() time.Time { return at }
		if _, err := s.InsertMessageFull("user", "qq", "text", m.Text,
			MsgMeta{Origin: OriginUser, SceneKey: sceneKey, SenderUIN: m.Uin}); err != nil {
			t.Fatal(err)
		}
	}
	// 让"现在"停在冲突窗口之后 —— 冷却与窗口都以它为参照。
	clock.Now = func() time.Time { return endAt }
}

// cflConflictHistory 造出**带前窗**的一段历史: ③ 负面密度上升要跟"前窗"比,
// 只有冲突窗口本身的话 `PrevNegDensity` 恒为 0, 上升判不出来。
func cflConflictHistory(t *testing.T, s *Store, sceneKey string) time.Time {
	t.Helper()
	msgs, _, _ := cflTestHistory(cflTestT0, cflTestConflictWindow(cflTestT0))
	cflSeedWindow(t, s, sceneKey, msgs, cflTestT0)
	return cflTestT0
}

// cflSceneAt 建一行场景并把阶段定死 —— A1 只认**场景阶段**。
func cflSceneAt(t *testing.T, s *Store, sceneKey, stage string) {
	t.Helper()
	if _, err := s.DB.Exec("INSERT OR REPLACE INTO convo_scene "+
		"(scene_key, kind, platform, group_id, peer_uin, self_role, stage, intimacy, first_seen_ts, last_msg_ts, updated_ts) "+
		"VALUES (?,?,?,?,?,?,?,?,?,?,?)",
		sceneKey, SceneKindGroup, "qq", "1034712035", "", "member", stage, 0.5,
		clock.TodayTS(), clock.TodayTS(), clock.TodayTS()); err != nil {
		t.Fatal(err)
	}
}

// TestGateGroupMessage_ConflictInterventionReachesTheStance
//
// ⚠️ **这条是"消费者在生产路径被调用"的尺子。**
// 光测 GateConflict 是个库函数, 证明不了真的有人调它 ——
// 而这一节原来的状态正是"库函数全绿、生产调用者为零"。
func TestGateGroupMessage_ConflictInterventionReachesTheStance(t *testing.T) {
	e := gateEngine(t)
	s := e.Store
	scene := "group:12345"
	if err := EnsureConflictSchema(s); err != nil {
		t.Fatal(err)
	}
	cflSceneAt(t, s, scene, ConflictStageIntimate)
	cflConflictHistory(t, s, scene)

	res := e.GateGroupMessage(SceneMsg{SceneKey: scene, UIN: "u1", Text: "你有病吧", TS: cflTestT0},
		SelfIdentity{UIN: "777"}, gateCfg())
	if res.Stance != StanceSpeak {
		t.Fatalf("该介入时没有抬成发言: stance=%s reason=%s", res.Stance, res.Reason)
	}
	if !strings.Contains(res.Reason, "§6.9") {
		t.Fatalf("发言理由不是冲突介入给的: %q", res.Reason)
	}
	// ⚠️ 而且**决策必须落库** —— 否则 A5 冷却永远读不到东西。
	var n int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM convo_conflict_action").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("决策没有落库 —— 同一对可以被无限次劝 (A5 冷却失效)")
	}
}

// TestGateGroupMessage_ConflictCooldownEngagesOnTheSecondTry
//
// ⚠️ A5: 同一对在 120 分钟内**只许劝一次**。
// 它靠的是"上一次决策落过库" —— 没落库的话这条永远不生效, 而表现是**它反复劝**。
func TestGateGroupMessage_ConflictCooldownEngagesOnTheSecondTry(t *testing.T) {
	e := gateEngine(t)
	s := e.Store
	scene := "group:12345"
	cflSceneAt(t, s, scene, ConflictStageIntimate)
	cflConflictHistory(t, s, scene)
	now := clock.Now()

	first := e.GateGroupMessage(SceneMsg{SceneKey: scene, UIN: "u1", Text: "你有病吧", TS: now},
		SelfIdentity{UIN: "777"}, gateCfg())
	if !strings.Contains(first.Reason, "§6.9") {
		t.Fatalf("第一次没有介入: %q", first.Reason)
	}
	// 一分钟后再来一条 —— 同一次冲突还没完。
	second := e.GateGroupMessage(SceneMsg{SceneKey: scene, UIN: "u2", Text: "闭嘴", TS: cflTestT0.Add(time.Minute)},
		SelfIdentity{UIN: "777"}, gateCfg())
	if strings.Contains(second.Reason, "§6.9 冲突介入") {
		t.Fatalf("一分钟内又劝了一次 —— A5 冷却没有生效: %q", second.Reason)
	}
}

// TestGateGroupMessage_EscalateVerdictSilencesIt
//
// ⚠️ **硬线 4**: 涉及人身攻击 / 歧视 / 安全 → **不劝和**, 沉默。
//
// ⚠️ 而它在裁定里叫 ConflictEscalate, **不是** ConflictSilent ——
// 我第一版只处理了 ConflictSilent, 那等于硬线 4 又成了一条不生效的闸门。
func TestGateGroupMessage_EscalateVerdictSilencesIt(t *testing.T) {
	e := gateEngine(t)
	s := e.Store
	scene := "group:12345"
	cflSceneAt(t, s, scene, ConflictStageIntimate)
	msgs, _, _ := cflTestHistory(cflTestT0, cflTestConflictWindow(cflTestT0))
	// 往窗口里塞一条**硬线词** —— 人身攻击那一类。
	msgs = append(msgs, ConflictMsg{Uin: "u1", Text: "你这种人就该去死", TS: cflTestT0})
	cflSeedWindow(t, s, scene, msgs, cflTestT0)

	res := e.GateGroupMessage(SceneMsg{SceneKey: scene, UIN: "u1", Text: "你这种人就该去死", TS: cflTestT0},
		SelfIdentity{UIN: "777"}, gateCfg())
	if res.Stance != StanceSilent {
		t.Fatalf("命中硬线词表却仍然开口: stance=%s reason=%s", res.Stance, res.Reason)
	}
}

// TestGateGroupMessage_NormalChatIsUntouched (反面锚点)
//
// ⚠️ §6.9 是**覆盖**规则, 不是"所有消息都过一遍冲突逻辑":
// 一段正常的群聊**一个字都不该被它改**。
func TestGateGroupMessage_NormalChatIsUntouched(t *testing.T) {
	e := gateEngine(t)
	s := e.Store
	scene := "group:12345"
	cflSceneAt(t, s, scene, ConflictStageIntimate)
	for _, tx := range []string{"今天天气不错", "是啊挺暖和的", "晚上吃啥"} {
		if _, err := s.InsertMessageFull("user", "qq", "text", tx,
			MsgMeta{Origin: OriginUser, SceneKey: scene, SenderUIN: "u1"}); err != nil {
			t.Fatal(err)
		}
	}
	res := e.GateGroupMessage(SceneMsg{SceneKey: scene, UIN: "u1", Text: "晚上吃啥", TS: cflTestT0},
		SelfIdentity{UIN: "777"}, gateCfg())
	if strings.Contains(res.Reason, "§6.9") {
		t.Fatalf("正常群聊被冲突闸门改了: %q", res.Reason)
	}
}
