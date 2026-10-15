package convo

import (
	"testing"
	"time"

	"github.com/tzhnahida/VocabLock/internal/clock"
)

// ─────────────────────────────────────────────────────────────
// §6.10.5 陌生人 / 新人档: 「不接梗、不点名、只给中性反应; **不主动搭话**」
//
// ⚠️ 这一条只该挡**主动搭话**(插话那条路) ——
// **不许挡被 @**: 那是 §6.3 的硬门槛("被 @ 必答"), 而且 2026-10-14
// 用户实测过一次"@ 它没反应"的代价。
// ─────────────────────────────────────────────────────────────

// newMemberSay 让一条群消息走完整裁决。roll 控制插话闸门。
func newMemberSay(t *testing.T, e *Engine, uin, text string, atSelf bool, roll int) GroupGateResult {
	t.Helper()
	e.ChimeRollFn = func(int) int { return roll }
	var at []string
	if atSelf {
		at = []string{"3487037837"}
	}
	return e.GateGroupMessage(SceneMsg{
		SceneKey: sceneWireKey, UIN: uin, Text: text, AtUINs: at, TS: clock.Now(),
	}, SelfIdentity{UIN: "3487037837"}, sceneWireCfg())
}

// TestNewMember_DoesNotInitiateButStillAnswers
//
// ⚠️ 一条用例把**正反两面**都钉住:
//
//	· 新人 + 判不出对谁说的 → **不许主动搭话**;
//	· 同一个新人 **@ 它** → **必须答** (§6.3 硬门槛, 不许被这道闸门吃掉)。
func TestNewMember_DoesNotInitiateButStillAnswers(t *testing.T) {
	e := gateEngine(t)
	if err := EnsureSceneSchema(e.Store); err != nil {
		t.Fatal(err)
	}
	// 他刚在群里说过一句话 → 被登记成"今天第一次见"。
	sceneWireSay(e, "111", "大家好")
	if isNew, _ := e.Store.IsNewMember(sceneWireKey, "111", time.Now(), NewMemberWindow); !isNew {
		t.Fatal("夹具坏了: 刚登记的人应当是新人")
	}

	// ① 判不出对谁说的 + 插话闸门掷中 → **新人仍然不许主动搭话**。
	got := newMemberSay(t, e, "111", "今天天气不错", false, 0)
	if got.Stance == StanceSpeak {
		t.Fatalf("**新人主动搭话了**: %s (%s) —— §6.10.5 说不主动搭话", got.Stance, got.Reason)
	}

	// ② ⚠️ **同一个新人 @ 它 → 必须答**。
	// 把闸门写成无条件早退的话, 这里就红了 —— 而那正是 2026-10-14 踩过的坑。
	at := newMemberSay(t, e, "111", "在吗", true, 99)
	if at.Stance != StanceSpeak {
		t.Fatalf("**被新人 @ 却不答**: %s (%s) —— §6.3 被 @ 必答是硬门槛", at.Stance, at.Reason)
	}
}

// TestNewMember_OldMemberCanStillChime (反面)
//
// ⚠️ 闸门不许把"老成员"也一起挡掉 —— 那它就变成了"它对谁都插不上话"。
func TestNewMember_OldMemberCanStillChime(t *testing.T) {
	e := gateEngine(t)
	if err := EnsureSceneSchema(e.Store); err != nil {
		t.Fatal(err)
	}
	sceneWireSay(e, "222", "先露个面")
	// 把他推到很久以前 —— 不再是新人。
	old := clock.Now().Add(-30 * 24 * time.Hour).Format(sceneTSLayout)
	if _, err := e.Store.DB.Exec(
		"UPDATE convo_scene_member SET first_seen_ts=? WHERE scene_key=? AND uin=?", old, sceneWireKey, "222"); err != nil {
		t.Fatal(err)
	}
	got := newMemberSay(t, e, "222", "今天天气不错", false, 0)
	if got.Stance != StanceSpeak {
		t.Fatalf("老成员掷中插话闸门却没说: %s (%s) —— 那道闸门挡过头了", got.Stance, got.Reason)
	}
}

// TestNewMember_BrokenTimestampDoesNotSilenceIt (反面, 最要紧的一条)
//
// ⚠️ `IsNewMember` 原来在**时间戳解析失败**时返回 `(true, nil)`。
// 在它只当读数的时候无所谓; 一旦它是**"不主动搭话"那道闸门**,
// 返回 true 就意味着「**数据坏了 → 它忽然不说话**」—— 而那是最难查的一类现象,
// 因为没有人会想到去怀疑一个时间戳格式。
//
// ⚠️ 全包同一条纪律: **失败往"正常说话"倒**。
func TestNewMember_BrokenTimestampDoesNotSilenceIt(t *testing.T) {
	e := gateEngine(t)
	if err := EnsureSceneSchema(e.Store); err != nil {
		t.Fatal(err)
	}
	sceneWireSay(e, "333", "先露个面")
	if _, err := e.Store.DB.Exec(
		"UPDATE convo_scene_member SET first_seen_ts=? WHERE scene_key=? AND uin=?",
		"not-a-timestamp", sceneWireKey, "333"); err != nil {
		t.Fatal(err)
	}
	// 坏数据 → 必须算"不是新人"。
	if isNew, err := e.Store.IsNewMember(sceneWireKey, "333", time.Now(), NewMemberWindow); err != nil || isNew {
		t.Fatalf("坏时间戳被判成了新人: isNew=%v err=%v —— 那会让它莫名其妙地安静下来", isNew, err)
	}
	got := newMemberSay(t, e, "333", "今天天气不错", false, 0)
	if got.Stance != StanceSpeak {
		t.Fatalf("**一个坏时间戳把它变成了哑巴**: %s (%s)", got.Stance, got.Reason)
	}
}
