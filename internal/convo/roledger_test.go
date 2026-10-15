package convo

import "testing"

// ⚠️ 样本不足 → **回落到种子位**, 而不是"沉默位"。
// 这两件事不一样: 前者是"还没处出来", 后者是"处出来了, 就是个不爱说话的"。
func TestDeriveRole_NeedsSamples(t *testing.T) {
	seed := GroupRoleAnchor
	st := RoleStats{Spoke: RoleMinSpoke - 1, Contradicted: 100}
	if got := DeriveRole(st, seed); got != seed {
		t.Fatalf("只说了 %d 次就下了结论, got %s", RoleMinSpoke-1, got)
	}
	st.Spoke = RoleMinSpoke
	if got := DeriveRole(st, seed); got == seed {
		t.Fatal("够样本了却还停在种子 —— 位永远长不出来")
	}
}

func TestDeriveRole_Tiers(t *testing.T) {
	seed := GroupRoleFoiler
	cases := []struct {
		name string
		st   RoleStats
		want GroupRole
	}{
		{"开口不少且常被顶 → 杠精", RoleStats{Spoke: 10, Silent: 5, Contradicted: 5}, GroupRoleContrarian},
		{"开口有人接 → 组织者", RoleStats{Spoke: 10, Silent: 20, RepliedTo: 6}, GroupRoleAnchor},
		{"基本不说话 → 潜水", RoleStats{Spoke: 10, Silent: 100}, GroupRoleLurker},
		{"说了没人接 → 也是潜水", RoleStats{Spoke: 8}, GroupRoleLurker},
		{"说、也有人接, 但不多 → 捧哏", RoleStats{Spoke: 10, Silent: 10, RepliedTo: 2}, GroupRoleFoiler},
	}
	for _, c := range cases {
		if got := DeriveRole(c.st, seed); got != c.want {
			t.Fatalf("%s: want %s, got %s", c.name, c.want, got)
		}
	}
}

// ⚠️ 阈值必须是**硬判据**: 一次被反驳不该改位。
// ⚠️ 夹具必须带上 RepliedTo —— 全是 0 的账本是"说了没人接", 那是潜水, 不是基准。
func TestDeriveRole_OneContradictionDoesNotFlipIt(t *testing.T) {
	base := RoleStats{Spoke: 20, Silent: 5, RepliedTo: 4}
	if got := DeriveRole(base, GroupRoleFoiler); got != GroupRoleFoiler {
		t.Fatalf("干净账本应当是捧哏, got %s", got)
	}
	one := base
	one.Contradicted = 1
	if got := DeriveRole(one, GroupRoleFoiler); got == GroupRoleContrarian {
		t.Fatal("被顶一次就变成杠精 —— 那不是「处出来的位」, 是过敏")
	}
}

func TestRoleOf_NoLedgerFallsBackToSeed(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	sk := "group:777"
	seed := AssignGroupRole("小夜", sk)
	if got := e.RoleOf("小夜", sk); got != seed {
		t.Fatalf("没有账本时应当用种子位, want %s got %s", seed, got)
	}
}

// ⚠️ **本批的核心主张**: 位是处出来的, 不是分出来的。
// 所以这条必须证明"同一个群、同一个种子, 行为不同 → 位不同"。
func TestRoleOf_GrowsFromBehaviour(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	sk := ""
	for i := 0; i < 80; i++ {
		cand := "group:" + itoa(i)
		if AssignGroupRole("小夜", cand) != GroupRoleLurker {
			sk = cand
			break
		}
	}
	if sk == "" {
		t.Fatal("找不到种子位不是潜水的群")
	}
	seed := AssignGroupRole("小夜", sk)
	for i := 0; i < RoleMinSpoke; i++ {
		e.noteRoleOutcome(SceneMsg{SceneKey: sk, Text: "嗯"}, GroupGateResult{Stance: StanceSpeak})
	}
	for i := 0; i < 100; i++ {
		e.noteRoleOutcome(SceneMsg{SceneKey: sk, Text: "别人的话"}, GroupGateResult{Stance: StanceSilent})
	}
	got := e.RoleOf("小夜", sk)
	if got != GroupRoleLurker {
		t.Fatalf("种子位是 %s, 说了 8 次之后沉默了 100 次 —— 位应当长成潜水, got %s", seed, got)
	}
}

// ⚠️ **沉默也必须记账**: 只记"它说了话"的那些, 开口率永远是 100%,
// 于是位**永远长不出来** —— 而现象只是"它一直是个捧哏"。
func TestNoteRoleOutcome_RecordsSilentToo(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	sk := "group:9"
	for i := 0; i < 5; i++ {
		e.noteRoleOutcome(SceneMsg{SceneKey: sk, Text: "无关的话"}, GroupGateResult{Stance: StanceSilent})
	}
	e.noteRoleOutcome(SceneMsg{SceneKey: sk, Text: "在吗"}, GroupGateResult{Stance: StanceReact})
	e.noteRoleOutcome(SceneMsg{SceneKey: sk, Text: "在吗"}, GroupGateResult{Stance: StanceSpeak})
	st, ok, err := e.Store.LoadRoleStats(e.ownerKey(), sk)
	if err != nil || !ok {
		t.Fatalf("账本没落库: ok=%v err=%v", ok, err)
	}
	if st.Silent != 5 || st.Reacted != 1 || st.Spoke != 1 {
		t.Fatalf("三态记录不对: %+v", st)
	}
	if _, ok, _ := e.Store.LoadRoleStats(e.ownerKey(), "group:404"); ok {
		t.Fatal("账本串场景了")
	}
}

// ⚠️ 它说完之后的**窗口只用一次** —— 否则一次开口会被后面十条消息反复算成"被接"。
func TestNoteRoleOutcome_FollowUpWindowUsedOnce(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	sk := "group:11"
	e.noteRoleOutcome(SceneMsg{SceneKey: sk, Text: "在吗"}, GroupGateResult{Stance: StanceSpeak})
	for i := 0; i < 3; i++ {
		e.noteRoleOutcome(SceneMsg{SceneKey: sk, Text: "接一句"}, GroupGateResult{Stance: StanceSilent})
	}
	st, _, _ := e.Store.LoadRoleStats(e.ownerKey(), sk)
	if st.RepliedTo > 1 {
		t.Fatalf("一次开口被算了 %d 次「被接」", st.RepliedTo)
	}
}
