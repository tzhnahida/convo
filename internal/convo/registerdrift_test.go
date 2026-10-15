package convo

import (
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────
// §4.4.1b **语域变硬时关闭夜间社交**
//
// ⚠️ `register.go` 的理由:
//
//   > 夜间社交模拟会**反过来污染**白天的语域。语域已经变硬时再叠一层,
//   > 等于让"变硬"自我强化 —— **这是本设计里唯一会正反馈的路径, 必须掐掉。**
//
// 而 `DriftDisablesNightSocial` 从来没有调用者 ⇒ 那条路一直通着。
// ─────────────────────────────────────────────────────────────

// seedRegister 落一天的语域快照。
func seedRegister(t *testing.T, e *Engine, day string, avgRunes, modal, bang float64) {
	t.Helper()
	if err := EnsureRegisterSchema(e.Store.DB); err != nil {
		t.Fatal(err)
	}
	if err := SaveRegisterVector(e.Store.DB, e.ownerKey(), day, RegisterVector{
		N: 40, AvgRunes: avgRunes, Modal: modal, Bang: bang,
	}); err != nil {
		t.Fatal(err)
	}
}

// TestRegisterDrift_ThreeStates (⚠️ 三态, 不是两态)
//
// ⚠️ 把"读不到"并进"没漂"的话, 闸门**看起来在工作**, 而实际什么都没拦。
func TestRegisterDrift_ThreeStates(t *testing.T) {
	e, _, _ := crossPersonEngine(t)
	if err := EnsureRegisterSchema(e.Store.DB); err != nil {
		t.Fatal(err)
	}

	// ① 库里什么都没有 → **算不出来**
	if _, known, err := e.registerDriftDir(); known || err != nil {
		t.Fatalf("空库应当是「算不出来」(known=false, err=nil), 实得 known=%v err=%v", known, err)
	}

	// ② 造一个"变硬": 基线句长, 最近句短。
	now := time.Now()
	for i := 1; i <= 7; i++ {
		seedRegister(t, e, now.AddDate(0, 0, -i).Format("2006-01-02"), 30, 0.5, 0.3)
	}
	seedRegister(t, e, now.Format("2006-01-02"), 8, 0.05, 0.02)
	dir, known, err := e.registerDriftDir()
	if err != nil || !known {
		t.Fatalf("造出漂移了却算不出来: known=%v err=%v", known, err)
	}
	if !DriftDisablesNightSocial(dir) {
		t.Fatalf("方向是 %q, 而闸门只在 harder 时挡 —— 那这条用例没测到东西", dir)
	}
}

// TestRegisterDrift_SofterDoesNotBlock (反面)
func TestRegisterDrift_SofterDoesNotBlock(t *testing.T) {
	if DriftDisablesNightSocial(RegisterDirSofter) {
		t.Fatal("变软不该关夜间社交 —— 那会让「它变活泼」反而被惩罚")
	}
	if DriftDisablesNightSocial("") {
		t.Fatal("**空方向不该挡住** —— 空是「没漂」, 挡住会让「没数据」等于「漂了」)")
	}
	if !DriftDisablesNightSocial(RegisterDirHarder) {
		t.Fatal("变硬必须挡住")
	}
}

// TestNightSocial_BlockedWhenHarder (端到端)
//
// ⚠️ 两个库跑同一批, **唯一差别是语域漂移** ——
// 与 §2.66 同一条纪律: 别让上一遍的产物污染这一遍的输入。
func TestNightSocial_BlockedWhenHarder(t *testing.T) {
	run := func(harder bool) int {
		c := &countingChatter{out: "OK"}
		e, _ := groupSpeakEngine(t, c)
		if err := EnsureRegisterSchema(e.Store.DB); err != nil {
			t.Fatal(err)
		}
		// 夜里得有名册, 否则整批跳过 —— 那样两个库都没跑, 测不到东西。
		e.NightPeers = []NightPeer{{UIN: 90001, Name: "另一个她", Brief: "同类", Weight: 1}}
		if harder {
			now := time.Now()
			for i := 1; i <= 7; i++ {
				seedRegister(t, e, now.AddDate(0, 0, -i).Format("2006-01-02"), 30, 0.5, 0.3)
			}
			seedRegister(t, e, now.Format("2006-01-02"), 8, 0.05, 0.02)
		}
		now := time.Now()
		e.prepareDailyJobs(time.Date(now.Year(), now.Month(), now.Day(), 20, 0, 0, 0, time.Local))
		return c.calls
	}
	normal := run(false)
	blocked := run(true)
	if normal == 0 {
		t.Fatalf("没漂时整批一次模型调用都没有 —— 夹具没跑起来, 下面那条比较是空的")
	}
	if blocked >= normal {
		t.Fatalf("**语域变硬却照跑夜间社交**(调用次数 %d → %d) —— 那是最会正反馈的那条路",
			normal, blocked)
	}
}
