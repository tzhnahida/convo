package convo

import (
	"strings"
	"testing"
	"time"
)

// ——— §3.6.8 事件沉降 ———

func ts(t time.Time) string { return t.Format("2006-01-02 15:04:05") }

func TestSettleStage_BackgroundThreeStages(t *testing.T) {
	base := time.Date(2026, 5, 1, 10, 0, 0, 0, time.Local)
	cases := []struct {
		name string
		ago  time.Duration
		want string
	}{
		{"3 天前被提及 → 可说细节", 3 * 24 * time.Hour, StageDetail},
		{"恰好 7 天 → 进模糊态", StageDetailHours * time.Hour, StageVague},
		{"20 天 → 模糊态", 20 * 24 * time.Hour, StageVague},
		{"恰好 30 天 → 归档", StageVagueHours * time.Hour, StageArchived},
		{"60 天 → 归档", 60 * 24 * time.Hour, StageArchived},
	}
	for _, c := range cases {
		got := SettleStage("background", ts(base.Add(-c.ago)), base)
		if got != c.want {
			t.Errorf("%s: 得到 %q, 期望 %q", c.name, got, c.want)
		}
	}
}

// 🔴 这条是整条规则的**关键**: 时刻以"最后一次被提及"为准, **不是生成时刻**。
// 按生成时刻算的话, 一件被反复聊起的事会莫名其妙地"变模糊" —— 而那恰恰是反的。
func TestSettleStage_UsesLastMentionNotBirth(t *testing.T) {
	base := time.Date(2026, 5, 1, 10, 0, 0, 0, time.Local)
	got := SettleStage("background", ts(base.Add(-24*time.Hour)), base)
	if got != StageDetail {
		t.Fatalf("40 天前发生、昨天被提及的事应仍可给细节, 实得 %q —— 说明用的是生成时刻", got)
	}
}

func TestSettleStage_PulseExpiresSameDay(t *testing.T) {
	base := time.Date(2026, 5, 1, 23, 0, 0, 0, time.Local)
	if got := SettleStage("pulse", ts(base), base); got != StageToday {
		t.Errorf("当天脉冲应还在场上, 实得 %q", got)
	}
	if got := SettleStage("pulse", ts(base.AddDate(0, 0, -1)), base); got != StageGone {
		t.Errorf("隔天的脉冲应已消失 (用完即弃), 实得 %q", got)
	}
}

// 反复型: 升级为稳定事实后**不再沉降**。
func TestSettleStage_StableNeverSettles(t *testing.T) {
	base := time.Date(2026, 5, 1, 10, 0, 0, 0, time.Local)
	got := SettleStage("stable", ts(base.AddDate(0, 0, -365)), base)
	if got != StageDetail {
		t.Fatalf("稳定事实不该随时间变模糊, 实得 %q", got)
	}
}

// ⚠️ 判据必须**宁可漏判也不误判**: 误判会把一条正在沉降的事件重置回"可说细节"。
func TestMentionedIn(t *testing.T) {
	if !MentionedIn("我爸好多了 下周复查", "我爸住院了") {
		t.Error("回复里明摆着提到了, 却没认出来")
	}
	if MentionedIn("今天天气不错啊", "我爸住院了") {
		t.Error("不相干的回复被当成了提及 —— 会把沉降时钟错误重置")
	}
	if MentionedIn("", "我爸住院了") {
		t.Error("空回复不该算提及")
	}
	if MentionedIn("hello world", "hello") {
		t.Error("非中文事件不该被误判为提及")
	}
}

func settleStore(t *testing.T, base time.Time) (*Store, func(time.Time)) {
	t.Helper()
	s, err := OpenStore(t.TempDir(), "test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	setNow := withFrozenClock(t, base)
	return s, setNow
}

func TestNoteEventMentions_ResetsClockAndUpgrades(t *testing.T) {
	base := time.Date(2026, 5, 1, 10, 0, 0, 0, time.Local)
	s, setNow := settleStore(t, base)
	setNow(base)
	ev := WorldEvent{TS: ts(base.AddDate(0, 0, -40)), Kind: "environment", Source: "生活",
		Text: "我爸住院了", Intensity: 0.6, Weight: 1.0, Duration: "background"}
	if _, err := s.IngestWorldEvents([]WorldEvent{ev}); err != nil {
		t.Fatal(err)
	}
	if n, err := s.NoteEventMentions("我爸住院了 我明天去看看", base, 20); err != nil || n != 1 {
		t.Fatalf("应识别出 1 条提及, 实得 %d (err=%v)", n, err)
	}
	notes, err := s.InPlayEvents(base, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 || notes[0].Stage != StageDetail {
		t.Fatalf("刚被提及的背景事件应处在可说细节阶段, 实得 %+v", notes)
	}
	for i := 0; i < RepeatedUpgradeAt; i++ {
		if _, err := s.NoteEventMentions("又在说我爸住院了这事", base, 20); err != nil {
			t.Fatal(err)
		}
	}
	if got := SettleStage(durationOf(t, s, ev.Key()), ts(base), base); got != StageDetail {
		t.Fatalf("反复聊过的事应升级为稳定事实, 实得 %q", got)
	}
}

func durationOf(t *testing.T, s *Store, key string) string {
	t.Helper()
	var d string
	if err := s.DB.QueryRow(`SELECT duration FROM convo_world_event WHERE event_key=?`, key).Scan(&d); err != nil {
		t.Fatal(err)
	}
	return d
}

// 注入段必须**明说「不许无缘无故主动提」** —— 否则它就从句子里学会了复述,
// 那正是 §3.6.7.6 要防的(无缘无故重提与道具依赖同源)。
func TestEventNotesLine_ForbidsProactive(t *testing.T) {
	line := EventNotesLine([]EventNote{{Stage: StageDetail, Text: "我爸住院了"}})
	if !strings.Contains(line, "不许无缘无故主动提") {
		t.Errorf("注入段缺少「不许主动提」的约束:\n%s", line)
	}
	vague := EventNotesLine([]EventNote{{Stage: StageVague, Text: "我爸住院了"}})
	if !strings.Contains(vague, "记不清") {
		t.Errorf("模糊态必须明说细节已不可用:\n%s", vague)
	}
	arch := EventNotesLine([]EventNote{{Stage: StageArchived, Text: "我爸住院了"}})
	if !strings.Contains(arch, "绝不主动提") {
		t.Errorf("归档态必须明说绝不主动提:\n%s", arch)
	}
	if got := EventNotesLine(nil); got != "" {
		t.Errorf("无事件时不该注入 (空段白占 token 且是噪音), 实得 %q", got)
	}
}

// 🔴 这条钉的是**静默失败**这一类, 而不是某个具体功能。
//
// 本轮实测踩到: `IngestWorldEvents` 的 INSERT 列名清单与值个数一度不一致 (10 vs 11) ——
// 成因是一次**半落盘的编辑**(参数那半句写进去了, 列名那半句没有)。
//
// 它为什么危险: 调用方是 `ingestWorld`, 判据是 `err == nil && n > 0` ——
// **SQL 报错会被悄悄吞掉**, 于是世界事件一条都不入库, 而仿真报告照样跑完、
// 心情照样"有个数"(退回基线), 看不出任何异常。
//
// 所以这里同时断言两件事: **写进去了** 且 **查得出来**。只断言前者会漏掉读取侧的同类错误。
func TestIngestWorldEvents_ActuallyPersists(t *testing.T) {
	base := time.Date(2026, 5, 1, 10, 0, 0, 0, time.Local)
	s, setNow := settleStore(t, base)
	setNow(base)
	ev := WorldEvent{
		TS: ts(base), Kind: "environment", Source: "生活", Text: "快递到了",
		Valence: 0.2, Arousal: 0.5, Intensity: 0.4, Weight: 1.0, Duration: "pulse",
	}
	n, err := s.IngestWorldEvents([]WorldEvent{ev})
	if err != nil {
		t.Fatalf("入库报错 (典型成因: 列名与值个数不匹配): %v", err)
	}
	if n != 1 {
		t.Fatalf("应入账 1 条, 实得 %d", n)
	}
	notes, err := s.InPlayEvents(base, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 {
		t.Fatalf("入库了却查不出来 —— 读侧同样可能是静默坏的, 实得 %d 条", len(notes))
	}
	if notes[0].Text != "快递到了" {
		t.Errorf("取回的内容不对: %+v", notes[0])
	}
}
