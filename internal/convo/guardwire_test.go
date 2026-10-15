package convo

import (
	"context"
	"strings"
	"testing"
)

// ——— §10.3 依赖护栏的接线 ———

func TestGuardNote_NormalIsSilent(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	if got := e.guardNote("今天天气不错"); got != "" {
		t.Fatalf("平时护栏**不许**出现在提示里 (一个天天念伦理条款的陪伴体比不念的更不像人): %q", got)
	}
}

// 危机必须**当轮**就响 —— 等日终统计的警报不是警报。
func TestGuardNote_CrisisFiresImmediately(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	got := e.guardNote("我不想活了")
	if !strings.Contains(got, "【相处·重要】") {
		t.Fatalf("危机当轮没有注入护栏段: %q", got)
	}
	if !strings.Contains(got, "真人") {
		t.Fatal("危机档必须给出真人出口")
	}
	for _, wd := range guardRefuseWords {
		if strings.Contains(got, wd) {
			t.Fatalf("护栏变成了拒聊: %q", got)
		}
	}
}

func TestGuardNote_ExclusiveTalkAccumulates(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	for i := 0; i < 4; i++ {
		e.guardNote("只有你懂我")
	}
	got := e.guardNote("在干嘛")
	if !strings.Contains(got, "【相处】") {
		t.Fatalf("连续排他表达之后应当出现降温段: %q", got)
	}
	if strings.Contains(got, "【相处·重要】") {
		t.Fatal("排他表达不是危机, 不许升格成危机档")
	}
}

// 端到端: 危机消息走完 OnUserText 之后, 系统提示里必须有护栏段。
// ⚠️ 用「救命」开头是为了命中 urgentWords **跳过打字延迟** ——
// 否则这条测试要等二十多秒, 而"慢到没人跑"的测试等于没有。
func TestOnUserText_CrisisReachesTheSystemPrompt(t *testing.T) {
	c := &sitCaptureChatter{}
	e, _ := groupSpeakEngine(t, c)
	if err := e.OnUserText(context.Background(), "救命 我真的不想活了"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.lastSystem, "【相处·重要】") {
		t.Fatalf("危机护栏没有到达系统提示 —— guard.go 是死代码\n%.400s", c.lastSystem)
	}
}

// 反面: 普通消息**不许**把护栏段带进提示。
func TestOnUserText_NormalMessageHasNoGuard(t *testing.T) {
	c := &sitCaptureChatter{}
	e, _ := groupSpeakEngine(t, c)
	if err := e.OnUserText(context.Background(), "在吗 中午吃什么"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(c.lastSystem, "【相处】") || strings.Contains(c.lastSystem, "【相处·重要】") {
		t.Fatal("普通消息不该带护栏段")
	}
}

// ——— §6.9 冲突执行层的接线 ———

// 没有冲突时**不许**注入劝架指令。
func TestConflictActNote_QuietSceneIsSilent(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	if _, ok := e.conflictActNote("group:501"); ok {
		t.Fatal("空场景不许产生劝架指令")
	}
	if _, ok := e.conflictActNote("private:1"); ok {
		t.Fatal("私聊里没有「劝架」这回事")
	}

	for i := 0; i < 8; i++ {
		if _, err := e.Store.InsertMessageFull("user", "qq", "text", "今天天气不错啊",
			MsgMeta{Origin: OriginUser, SceneKey: "group:502", SenderUIN: "11"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := e.conflictActNote("group:502"); ok {
		t.Fatal("一派和气却给出了劝架指令 —— 那会让它平白无故地劝架")
	}
}

// ⚠️ 「冲突场景下每日上限真的拦住了」这条**没有写**:
// 要构造一个能被 DetectConflict 判成冲突的窗口很难, 而写一条永远绿的测试
// 比不写更糟 —— 它会让人以为这条路径被覆盖了。上限本身的尺子在
// TestConflictAct_Gates 里 (纯函数层), 集成层见 ## 未完成。

// 执行指令必须一路走到提示词里 (与违抗同档: 轮级)。
func TestBuildSystem_ConflictNoteIsPerRound(t *testing.T) {
	d := &DialogEngine{}
	got := d.buildSystem(DialogInput{
		History:      "你: 在吗",
		ConflictNote: "CONFLICT_MARK",
	}, &PersonaProfile{Name: "小夜"}, "ja")
	if !strings.Contains(got, "CONFLICT_MARK") {
		t.Fatal("冲突执行指令没有进提示词")
	}
	if strings.Index(got, "CONFLICT_MARK") < strings.Index(got, "【最近对话】") {
		t.Fatal("劝架指令是**轮级**的, 必须排在最近对话之后 (挪到前面会毁掉前面的缓存前缀)")
	}
}

func TestBuildSystem_GuardNoteReachesPrompt(t *testing.T) {
	d := &DialogEngine{}
	got := d.buildSystem(DialogInput{GuardNote: "GUARD_MARK"}, &PersonaProfile{Name: "小夜"}, "ja")
	if !strings.Contains(got, "GUARD_MARK") {
		t.Fatal("护栏段没有进提示词")
	}
}
