package convo

import (
	"errors"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────
// §4.4.4 **惊醒与求助** 的三道闸
//
//   设计稿: 「**睡眠窗内绝不发消息**(打扰红线, **优先级高于一切**)」
//          「**可以求助**: 若惊醒且**不在睡眠窗内**, 可发一条极短的消息」
//          「接收对象**硬绑定 owner**」
//          「频率 **≤1 次/月**」
//
// ⚠️ 而**第一道闸的失败方向**是这一组里最要紧的一条:
// 它是**打扰那一侧**的红线 —— 与插话同族, 而项目在插话那一处定过规矩:
// 「插话是"打扰"那一侧的例外, **例外的失败只能往'不做'倒**」。
// 所以**日程读不出来时也不发**, 不是"判不出就发"。
// ─────────────────────────────────────────────────────────────

// startleSender 记账用的假发送口。
type startleSender struct {
	texts []string
	to    []int64
	fail  bool
}

func (s *startleSender) SendText(userID int64, text string) error {
	if s.fail {
		return errors.New("boom")
	}
	s.to = append(s.to, userID)
	s.texts = append(s.texts, text)
	return nil
}
func (s *startleSender) SendImage(_ int64, _ string) error { return nil }

const startleOwner int64 = 3487037837

func startleEngine(t *testing.T, sender *startleSender) *Engine {
	t.Helper()
	s := newTestStore(t)
	if err := EnsureHeatSchema(s); err != nil {
		t.Fatal(err)
	}
	return &Engine{Store: s, Cfg: DefaultConfig(), Sender: sender, UserID: startleOwner}
}

// TestStartle_AsksWhenAwake
//
// 正面: **不在睡眠窗内** + 本月没用过 → 恰好一条, 发给 owner, 正文是那个常量。
//
// ⚠️ 夹具里**没有日程行** —— `LoadSchedule` 返回 `(nil, nil)`,
// 而那是"**未提供**"(不存在睡眠窗可违反), 不是"读失败"。
func TestStartle_AsksWhenAwake(t *testing.T) {
	sender := &startleSender{}
	e := startleEngine(t, sender)
	e.startleAsk(time.Now())
	if len(sender.texts) != 1 {
		t.Fatalf("清醒时惊醒应当恰好发一条, 实得 %d: %v", len(sender.texts), sender.texts)
	}
	if sender.texts[0] != StartleAskLine {
		t.Fatalf("正文必须是那条极短的常量, 实得 %q", sender.texts[0])
	}
	if sender.to[0] != startleOwner {
		t.Fatalf("**没有发给 owner**: %d", sender.to[0])
	}
	// ⚠️ 而额度**记在发出去之后**。
	if n, _ := e.Store.CounterGet("nightmare_ask", time.Now().Format("2006-01")); n != 1 {
		t.Fatalf("发出去之后额度应当记 1, 实得 %d", n)
	}
}

// TestStartle_SleepWindowBlocksIt (本条最重要)
//
// ⚠️ **打扰红线, 优先级高于一切** —— 睡眠窗内一条都不许发。
func TestStartle_SleepWindowBlocksIt(t *testing.T) {
	sender := &startleSender{}
	e := startleEngine(t, sender)
	if err := e.Store.SaveSchedule(&WeekSchedule{SleepStart: "23:30", SleepEnd: "07:30"}); err != nil {
		t.Fatal(err)
	}
	// 03:00 —— 睡眠窗正中间。
	night := time.Date(2026, 10, 16, 3, 0, 0, 0, time.Local)
	e.startleAsk(night)
	if len(sender.texts) != 0 {
		t.Fatalf("**睡眠窗内发了消息**: %v —— 那是打扰红线", sender.texts)
	}
	if n, _ := e.Store.CounterGet("nightmare_ask", night.Format("2006-01")); n != 0 {
		t.Fatalf("没发却记了额度: %d —— 那会白吃掉这个月唯一的一次", n)
	}
}

// TestStartle_ReadFailureDoesNotSend (反面, 与"未提供"成对)
//
// ⚠️ **打扰侧失败往"不说"倒** —— 日程读不出来时不许发。
// (而"没有日程"是**未提供**, 走另一条路, 见 `..._AsksWhenAwake`。)
func TestStartle_ReadFailureDoesNotSend(t *testing.T) {
	sender := &startleSender{}
	e := startleEngine(t, sender)
	if err := e.Store.DB.Close(); err != nil {
		t.Fatal(err)
	}
	e.startleAsk(time.Now())
	if len(sender.texts) != 0 {
		t.Fatalf("**读库失败时发了消息**: %v —— 打扰侧的失败只能往「不做」倒", sender.texts)
	}
}

// TestStartle_OncePerMonth
//
// ⚠️ 「频率 **≤1 次/月**(不是每周)」—— 而它是**生活上限**,
// 只判在代码里, **绝不告诉模型数字**(§4.6.1)。
func TestStartle_OncePerMonth(t *testing.T) {
	sender := &startleSender{}
	e := startleEngine(t, sender)
	e.startleAsk(time.Now())
	e.startleAsk(time.Now())
	e.startleAsk(time.Now())
	if len(sender.texts) != 1 {
		t.Fatalf("同月三次惊醒应当只发一条, 实得 %d", len(sender.texts))
	}
}

// TestStartle_SendFailureDoesNotBurnTheQuota
//
// ⚠️ 与 `MarkSparkSaidAfter` 同规: **记在发出去之后**。
// 先记的话, 一次发送失败就会吃掉整月额度, 而它其实谁都没说过。
func TestStartle_SendFailureDoesNotBurnTheQuota(t *testing.T) {
	sender := &startleSender{fail: true}
	e := startleEngine(t, sender)
	now := time.Now()
	e.startleAsk(now)
	if n, _ := e.Store.CounterGet("nightmare_ask", now.Format("2006-01")); n != 0 {
		t.Fatalf("**发送失败却吃掉了月度额度**: %d", n)
	}
	// 而"这次没发成"不该影响下一次: 关掉失败之后仍然发得出去。
	sender.fail = false
	e.startleAsk(now)
	if len(sender.texts) != 1 {
		t.Fatal("发送失败之后那次没发出去 —— 那这个月就白瞎了")
	}
}

// TestStartleMaySend_TakesTheUnionOfBothSources
//
// ⚠️ "睡没睡"有两个来源, 而它们**会打架**(`dayplangen.go` 记着这条实证:
// 「一个"周三没有 sleep 槽"的日程会让它以为周三也要 23:30 睡」)。
//
// ⚠️ 所以红线只有一种安全的合取方式: **任一来源说"在睡", 就按在睡处理。**
//
// 第一版 `StartleMaySend` 只读了展示字段(`SleepStart`/`SleepEnd`) ——
// 那样"槽表说在睡、展示字段说醒"时红线**会被绕过**,
// 而绕过它正是这一条要防的事(半夜发一条"睡不着")。
func TestStartleMaySend_TakesTheUnionOfBothSources(t *testing.T) {
	// 03:00 —— **槽表**里是睡眠段。
	night := time.Date(2026, 10, 16, 3, 0, 0, 0, time.Local)
	// ⚠️ 而**展示字段**说这段时间是**醒的**(09:00-18:00)。
	//
	// ⚠️ 这正是缺口所在: 第一版只读展示字段 ⇒ 03:00 不在那个窗里 ⇒ **放行** ✗
	// (把 `SleepStart/SleepEnd` 也写成"在睡"的话, 旧代码照样拦得住, 那就测不到东西)。
	ws := &WeekSchedule{
		SleepStart: "09:00", SleepEnd: "18:00",
		Days: []DaySchedule{{Day: weekdayKey(night), Slots: []Slot{
			{Start: "00:00", End: "06:00", State: StateSleep},
		}}},
	}
	if StartleMaySend(night, ws) {
		t.Fatal("**槽表说在睡, 而红线放行了** —— 两个来源必须取并集, 不是二选一")
	}

	// ⚠️ 反向: 展示字段说睡、槽表说醒 → 也要拦。
	day := time.Date(2026, 10, 16, 14, 0, 0, 0, time.Local)
	ws2 := &WeekSchedule{
		SleepStart: "09:00", SleepEnd: "18:00", // 展示字段说在睡
		Days: []DaySchedule{{Day: weekdayKey(day), Slots: []Slot{
			{Start: "00:00", End: "23:59", State: StateFree},
		}}},
	}
	if StartleMaySend(day, ws2) {
		t.Fatal("展示字段说在睡, 而红线放行了")
	}

	// ⚠️ 而两个都说醒时**必须放行** —— 否则这条红线会把正常时段也堵死。
	ws3 := &WeekSchedule{SleepStart: "23:30", SleepEnd: "07:30",
		Days: []DaySchedule{{Day: weekdayKey(day), Slots: []Slot{
			{Start: "00:00", End: "23:59", State: StateFree},
		}}}}
	if !StartleMaySend(day, ws3) {
		t.Fatal("两个来源都说醒, 却拦住了 —— 那它永远发不出求助")
	}
}
