package convo

import (
	"strings"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────
// §3.7.1 时间散布的落点
//
// ⚠️ SpreadNightTurns 把 100 轮按合理时刻铺到 08:00-23:00, NightTurn.TS 也带着它 ——
// 而 NightWriteBack 组装 NightMemory 时**把它丢了**(那一格原来根本不存在)。
//
// 于是那句「次日看起来就是昨天白天聊的」**没有任何东西记着"什么时候"**。
// 这是本批第五次「算出来了但被丢掉」。
// ─────────────────────────────────────────────────────────────

// TestNightWriteBack_CarriesTheScatteredMoment
//
// ⚠️ **这条是"散布真的落到了产物上"的尺子。**
// 光测 SpreadNightTurns 会返回递增的时刻, 证明不了**有人用它**。
func TestNightWriteBack_CarriesTheScatteredMoment(t *testing.T) {
	day := "2026-03-02"
	stamps := SpreadNightTurns(day, 20, "group:night")
	if len(stamps) == 0 {
		t.Fatal("散布没产出时刻")
	}
	var turns []NightTurn
	for i, ts := range stamps {
		who := "德克萨斯"
		if i%2 == 1 {
			who = "小夜"
		}
		turns = append(turns, NightTurn{SceneKey: "group:night", Kind: NightSessionGroup,
			Sender: who, Text: "第" + itoa(i) + "句", TS: ts})
	}
	mems := NightWriteBack(NightSocialResult{Turns: turns, Spent: len(turns)}, "小夜", 6)
	if len(mems) == 0 {
		t.Fatal("写回是空的")
	}
	// ⚠️ 每一条都必须带着**它自己那一刻** —— 而不是"最后一条的时刻"。
	zero := 0
	for _, m := range mems {
		if m.At.IsZero() {
			zero++
		}
	}
	if zero > 0 {
		t.Fatalf("%d/%d 条写回没有时刻 —— 散布在这一层又丢了", zero, len(mems))
	}
	// 而且时段词要真的出现在最终那一行里。
	line := NightMemoryLine(mems[0])
	if !strings.Contains(line, "昨天") {
		t.Fatalf("写回的那一行没有时段: %q", line)
	}
}

// TestNightPeriodWord_ZeroIsNotMidnight
//
// ⚠️ **零值 = 未提供 ≠ 午夜**: 折成"凌晨"会让每一条没有时刻的写回都谎报成深夜。
// 与 BondSignalsFrom 里那个 Hour() >= 0 是同一个坑 —— 而那个坑已经咬过一次。
func TestNightPeriodWord_ZeroIsNotMidnight(t *testing.T) {
	if got := NightPeriodWord(time.Time{}); got != "" {
		t.Fatalf("零值时刻应当返回空串, 实得 %q", got)
	}
	if got := NightMemoryLine(NightMemory{Text: "德克萨斯: 在吗"}); got != "德克萨斯: 在吗" {
		t.Fatalf("没有时刻时不该编一个: %q", got)
	}
	// 各个时段
	cases := []struct {
		h    int
		want string
	}{
		{2, "凌晨"}, {7, "早上"}, {10, "上午"}, {13, "中午"},
		{15, "下午"}, {19, "傍晚"}, {22, "晚上"},
	}
	for _, c := range cases {
		at := time.Date(2026, 3, 2, c.h, 30, 0, 0, time.Local)
		if got := NightPeriodWord(at); got != c.want {
			t.Errorf("%d 点应当是 %q, 实得 %q", c.h, c.want, got)
		}
	}
	// §3.7.1 的散布范围是 08:00-23:00 —— 折出来的词必须落在这个区间里,
	// **不许出现"凌晨"**(那说明散布跑出了设计范围)。
	stamps := SpreadNightTurns("2026-03-02", 40, "group:night")
	for _, ts := range stamps {
		if w := NightPeriodWord(ts); w == "凌晨" {
			t.Fatalf("散布跑到了 %s, 而设计范围是 08:00-23:00", ts.Format("15:04"))
		}
	}
}
