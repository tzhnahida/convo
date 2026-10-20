package convo

import (
	"strings"
	"testing"
	"time"

	"github.com/tzhnahida/convo/internal/clock"
)

// ⚠️ 2026-10-20 生产实录: 20:11 系统替它换了头像, 20:48 群主问起,
// 它答「换头像？我这边没动过啊」—— 它不是撒谎, 是真不知道。
// 这一组用例钉的就是"它必须知道自己做过什么"。
func TestRenderSelfActionNote(t *testing.T) {
	out := RenderSelfActionNote([]SelfAction{
		{Kind: SelfKindAvatar, CreatedTS: clock.Now().Format("2006-01-02") + " 20:11:11"},
		{Kind: SelfKindQzone, CreatedTS: "2026-10-17 00:52:14"},
	})
	for _, want := range []string{"你最近对外做过的事", "换了头像", "发了一条说说", "今天 20:11", "10-17 00:52", "别否认"} {
		if !strings.Contains(out, want) {
			t.Fatalf("渲染缺了 %q:\n%s", want, out)
		}
	}
	// 没做过 / 只有不认识的类型 → 一个字都不注入。
	if RenderSelfActionNote(nil) != "" {
		t.Fatal("没动作却注入了")
	}
	if RenderSelfActionNote([]SelfAction{{Kind: "never-heard"}}) != "" {
		t.Fatal("未知类型不该渲染")
	}
}

// ⚠️ 时间必须说人话: 2026-10-20 实测, 注入 "10-20 20:11 你换了头像",
// 它回成"昨晚换头像" —— 一个裸日期它读不出"这是几小时前"。
func TestFormatSelfActionTS(t *testing.T) {
	cases := []struct{ created, today, want string }{
		{"2026-10-20 20:11:11", "2026-10-20", "今天 20:11"},
		{"2026-10-17 00:52:14", "2026-10-20", "10-17 00:52"},
		{"", "2026-10-20", ""},
	}
	for _, c := range cases {
		if got := formatSelfActionTS(c.created, c.today); got != c.want {
			t.Errorf("formatSelfActionTS(%q,%q)=%q, 期望 %q", c.created, c.today, got, c.want)
		}
	}
}

// 只回灌**已生效且在窗口内**的动作: 被它自己否掉的、太老的都不算。
func TestRecentAppliedSelfActions_SkipsRejectedAndOld(t *testing.T) {
	s := newTestStore(t)
	if err := EnsureSelfActionSchema(s.DB); err != nil {
		t.Fatal(err)
	}
	now := clock.Now().Format("2006-01-02 15:04:05")
	old := clock.Now().Add(-72 * time.Hour).Format("2006-01-02 15:04:05")
	for _, a := range []SelfAction{
		{Kind: SelfKindAvatar, State: SelfStateApplied, CreatedTS: now},
		{Kind: SelfKindQzone, State: SelfStateRejected, CreatedTS: now},
		{Kind: SelfKindCard, State: SelfStateApplied, CreatedTS: now}, // 时间在下面改旧
	} {
		id, err := s.InsertSelfAction(a)
		if err != nil {
			t.Fatal(err)
		}
		// ⚠️ InsertSelfAction 自己会写 created_ts —— 想让一条"变老"只能直接改库。
		if a.Kind == SelfKindCard {
			if _, err := s.DB.Exec("UPDATE convo_selfaction SET created_ts=? WHERE id=?", old, id); err != nil {
				t.Fatal(err)
			}
		}
	}
	got, err := s.RecentAppliedSelfActions(SelfNoteLookback, SelfNoteLimit)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Kind != SelfKindAvatar {
		t.Fatalf("应当只有 1 条(换头像), 实际 %+v", got)
	}
}

// 注入段必须真的进提示词。
func TestBuildSystem_SelfNoteReachesPrompt(t *testing.T) {
	d := &DialogEngine{}
	p := &PersonaProfile{Name: "小夜"}
	note := RenderSelfActionNote([]SelfAction{{Kind: SelfKindAvatar, CreatedTS: "2026-10-20 20:11:11"}})
	sys := d.buildSystem(DialogInput{UserText: "你怎么还换头像了", SelfNote: note}, p, "ja")
	if !strings.Contains(sys, "你最近对外做过的事") || !strings.Contains(sys, "换了头像") {
		t.Fatalf("自我动作没有进提示词:\n%s", sys)
	}
	// 反面: 没动作时不许有这一段。
	sys2 := d.buildSystem(DialogInput{UserText: "在吗"}, p, "ja")
	if strings.Contains(sys2, "你最近对外做过的事") {
		t.Fatalf("没动作却注入了这一段:\n%s", sys2)
	}
}
