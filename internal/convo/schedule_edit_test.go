package convo

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func schedWS(t *testing.T) *WeekSchedule {
	t.Helper()
	ws := dayPlanFixtureWeek()
	if err := ValidateWeekSchedule(ws); err != nil {
		t.Fatalf("夹具本身就不合法: %v", err)
	}
	return ws
}

func slotAtHM(ws *WeekSchedule, day, at string) Slot {
	d := dayPlanDay(ws, day)
	if d == nil {
		return Slot{}
	}
	m, _ := parseHM(at)
	for _, sl := range d.Slots {
		s, e := 0, 0
		s, _ = parseHM(sl.Start)
		e, _ = parseHM(sl.End)
		if m >= s && m < e {
			return sl
		}
	}
	return Slot{}
}

func jsonOf(t *testing.T, ws *WeekSchedule) string {
	t.Helper()
	b, err := json.Marshal(ws)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestInsertAppointment_CarvesTheSlot
//
// ⚠️ §4.5.3 第三条写入路径。在这之前 week_json **只被人工接口写过** ——
// 约定插进日程这件事根本不存在, 于是"它有约"和"它那个时段照常能被随机事件占用"
// 同时成立。
func TestInsertAppointment_CarvesTheSlot(t *testing.T) {
	ws := schedWS(t)
	before := slotAtHM(ws, "thu", "15:00")
	if before.State != StateFree || before.Reason != "" {
		t.Fatalf("夹具前提不对: 15:00 应当是普通 free, 实得 %+v", before)
	}

	changed, why := InsertAppointment(ws, "thu", "15:00", 60)
	if !changed {
		t.Fatalf("插不进去: %s", why)
	}
	if err := ValidateWeekSchedule(ws); err != nil {
		t.Fatalf("插完之后槽表不合法: %v", err)
	}
	got := slotAtHM(ws, "thu", "15:30")
	if got.State != StateBusy || got.Reason != SlotReasonAppointment {
		t.Fatalf("15:30 应当是 busy+appointment, 实得 %+v", got)
	}
	// 前后两格必须还是原来的样子 —— 不许"顺手把整槽吃掉"。
	if s := slotAtHM(ws, "thu", "14:00"); s.State != StateFree || s.Reason != "" {
		t.Errorf("开始之前那一格被改动了: %+v", s)
	}
	if s := slotAtHM(ws, "thu", "16:30"); s.State != StateFree || s.Reason != "" {
		t.Errorf("结束之后那一格被改动了: %+v", s)
	}
	// 相邻同态必须合并 —— 否则插十次约定之后一天会从 3 个槽碎成 40 个。
	d := dayPlanDay(ws, "thu")
	for i := 1; i < len(d.Slots); i++ {
		a, b := d.Slots[i-1], d.Slots[i]
		if a.State == b.State && a.Reason == b.Reason {
			t.Fatalf("相邻同态槽没合并: %+v / %+v", a, b)
		}
	}
}

// TestInsertAppointment_RefusesDuringSleep
//
// 反面: 凌晨三点不可能有约 —— 那时候它在睡觉, 而"睡着却有约"是最难查的一类脏数据。
func TestInsertAppointment_RefusesDuringSleep(t *testing.T) {
	ws := schedWS(t)
	before := jsonOf(t, ws)
	changed, why := InsertAppointment(ws, "thu", "03:00", 60)
	if changed {
		t.Fatal("凌晨三点的约定应当被拒")
	}
	if !strings.Contains(why, "睡觉") {
		t.Errorf("拒绝的理由不明确: %q", why)
	}
	if jsonOf(t, ws) != before {
		t.Fatal("被拒了却改了日程 —— 拒绝必须是**零副作用**的")
	}
}

// TestApplySleepShift_MovesOnlyTheLateBoundary
//
// ⚠️ 语义与 SleepPointState.OffsetMin 对齐: **睡点动, 起床点不动**。
// 搬错了(比如把早上那一段也搬)会让睡眠窗长度算错, 而那是 §4.4.6 全部护栏的输入。
func TestApplySleepShift_MovesOnlyTheLateBoundary(t *testing.T) {
	ws := schedWS(t)
	morningBefore := slotAtHM(ws, "thu", "03:00")

	changed, why := ApplySleepShift(ws, -30) // 提前半小时
	if !changed {
		t.Fatalf("搬不动: %s", why)
	}
	if ws.SleepStart != "23:00" {
		t.Fatalf("SleepStart 应当是 23:00, 实得 %q", ws.SleepStart)
	}
	if err := ValidateWeekSchedule(ws); err != nil {
		t.Fatalf("搬完之后槽表不合法: %v", err)
	}
	// 夜里那一段的起点动了。
	if s := slotAtHM(ws, "thu", "23:15"); s.State != StateSleep {
		t.Errorf("23:15 应当是 sleep, 实得 %+v", s)
	}
	if s := slotAtHM(ws, "thu", "22:45"); s.State == StateSleep {
		t.Errorf("22:45 不该还是 sleep(它 23:00 才睡), 实得 %+v", s)
	}
	// 早上那一段**一个字不动**。
	if s := slotAtHM(ws, "thu", "03:00"); s != morningBefore {
		t.Errorf("早上那一段被搬动了: %+v → %+v", morningBefore, s)
	}
	// 七天都要搬 —— 只搬一天的话周一和周三的时间感会互相矛盾。
	for _, d := range ws.Days {
		if got := slotAtHM(ws, d.Day, "23:15"); got.State != StateSleep {
			t.Errorf("%s 的夜里那一段没搬: %+v", d.Day, got)
		}
	}
}

// TestApplySleepShift_IsAtomicOnRefusal
//
// 反面: 搬太多会越界 → **整份不改**(不许出现"改了一半"的周表)。
func TestApplySleepShift_IsAtomicOnRefusal(t *testing.T) {
	ws := schedWS(t)
	before := jsonOf(t, ws)
	if changed, _ := ApplySleepShift(ws, 600); changed {
		t.Fatal("搬 600 分钟会跑到一天之外, 应当被拒")
	}
	if jsonOf(t, ws) != before {
		t.Fatal("被拒了却改了日程 —— 拒绝必须是**零副作用**的")
	}
	// 0 分钟 = 没变化(而不是"改成了原样")。
	if changed, _ := ApplySleepShift(ws, 0); changed {
		t.Fatal("偏移 0 不该报 changed")
	}
}

// TestScheduleRevision_RequiresReason
//
// ⚠️ §4.5.4-2「每次微调必须有理由」。**没有理由的日程变化 = 它今天莫名其妙不一样了**。
func TestScheduleRevision_RequiresReason(t *testing.T) {
	s := newTestStore(t)
	if err := s.SaveSchedule(schedWS(t)); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendScheduleRevision(ScheduleRevision{Source: "insomnia", DeltaMin: -30}); err == nil {
		t.Fatal("没有理由的微调必须被拒")
	}
	if err := s.AppendScheduleRevision(ScheduleRevision{Source: "insomnia", DeltaMin: -30, Reason: "连着三夜没睡好"}); err != nil {
		t.Fatalf("有理由却写不进去: %v", err)
	}
}

// TestScheduleRevision_RoundTripsNewestFirst
//
// ⚠️ revision_history 列**一直是死的**: 有定义、无写入、无读取。
// 于是「每次微调必须有理由」这条护栏前半截生效、后半截被丢掉 —— 代码强制要求理由非空, 然后把理由扔了。
func TestScheduleRevision_RoundTripsNewestFirst(t *testing.T) {
	s := newTestStore(t)
	if err := s.SaveSchedule(schedWS(t)); err != nil {
		t.Fatal(err)
	}
	for _, r := range []string{"第一条", "第二条", "第三条"} {
		if err := s.AppendScheduleRevision(ScheduleRevision{Source: "insomnia", Reason: r}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ScheduleRevisions(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("应当有 3 条, 实得 %d", len(got))
	}
	if got[0].Reason != "第三条" {
		t.Errorf("最新的一条应当排在最前, 实得 %q", got[0].Reason)
	}
	// 只增: 老的还在。
	if got[2].Reason != "第一条" {
		t.Errorf("老的那条被冲掉了: %q", got[2].Reason)
	}
}

// TestSaveScheduleWithRevision_RefusesBadWeekWithoutFakeHistory
//
// ⚠️ 顺序是**先日程后理由**: 反过来的话, 日程保存失败会留下一条"改过了"的假历史 ——
// 而 owner 查历史时看到的正是它。
func TestSaveScheduleWithRevision_RefusesBadWeekWithoutFakeHistory(t *testing.T) {
	s := newTestStore(t)
	if err := s.SaveSchedule(schedWS(t)); err != nil {
		t.Fatal(err)
	}
	bad := schedWS(t)
	bad.Days[1].Slots[len(bad.Days[1].Slots)-1].End = "23:59" // 不覆盖 24 小时
	if err := s.SaveScheduleWithRevision(bad, ScheduleRevision{Source: "insomnia", Reason: "x"}); err == nil {
		t.Fatal("坏日程必须被拒")
	}
	if list, _ := s.ScheduleRevisions(10); len(list) != 0 {
		t.Fatalf("坏日程被拒了, 却留下了一条假历史: %+v", list)
	}
}

// TestNoteAppointmentInSchedule_ReachesLayerOne
//
// ⚠️ **接线尺子**: 引擎侧的写路径必须在**生产路径上**真的被调用。
// 只测 InsertAppointment 是纯函数是不够的 —— 那样"引擎从来不调它"也照样绿。
func TestNoteAppointmentInSchedule_ReachesLayerOne(t *testing.T) {
	s := newTestStore(t)
	if err := s.SaveSchedule(schedWS(t)); err != nil {
		t.Fatal(err)
	}
	e := &Engine{Store: s, Cfg: DefaultConfig()}
	due := time.Date(2026, 10, 15, 15, 0, 0, 0, time.Local) // 2026-10-15 是周四
	e.noteAppointmentInSchedule("一起吃饭", due)

	ws, err := s.LoadSchedule()
	if err != nil {
		t.Fatal(err)
	}
	got := slotAtHM(ws, "thu", "15:15")
	if got.State != StateBusy || got.Reason != SlotReasonAppointment {
		t.Fatalf("约定没写进层 1: 15:15 = %+v", got)
	}
	// 理由必须**真的落库**了(而不只是函数签名要求了)。
	revs, err := s.ScheduleRevisions(5)
	if err != nil {
		t.Fatal(err)
	}
	if len(revs) != 1 {
		t.Fatalf("应当记 1 条微调历史, 实得 %d", len(revs))
	}
	if !strings.Contains(revs[0].Reason, "一起吃饭") {
		t.Errorf("历史里没写清是哪件事: %q", revs[0].Reason)
	}
}

// TestApplySleepShiftToSchedule_ReachesLayerOne
func TestApplySleepShiftToSchedule_ReachesLayerOne(t *testing.T) {
	s := newTestStore(t)
	if err := s.SaveSchedule(schedWS(t)); err != nil {
		t.Fatal(err)
	}
	e := &Engine{Store: s, Cfg: DefaultConfig()}
	e.applySleepShiftToSchedule(-30, "连着三夜没睡好")

	ws, err := s.LoadSchedule()
	if err != nil {
		t.Fatal(err)
	}
	if ws.SleepStart != "23:00" {
		t.Fatalf("睡点没落到日程上: %q", ws.SleepStart)
	}
	revs, _ := s.ScheduleRevisions(5)
	if len(revs) != 1 || revs[0].DeltaMin != -30 {
		t.Fatalf("微调历史不对: %+v", revs)
	}
	// 反面: 落不上的时候**不许**记一条历史 —— 否则历史说改过了, 日程说没改。
	ws2, _ := s.LoadSchedule()
	before := jsonOf(t, ws2)
	e.applySleepShiftToSchedule(0, "空操作")
	if after := jsonOf(t, mustLoadWS(t, s)); after != before {
		t.Fatal("0 偏移却改了日程")
	}
	if revs2, _ := s.ScheduleRevisions(5); len(revs2) != 1 {
		t.Fatalf("空操作记了历史: %+v", revs2)
	}
}

func mustLoadWS(t *testing.T, s *Store) *WeekSchedule {
	t.Helper()
	ws, err := s.LoadSchedule()
	if err != nil || ws == nil {
		t.Fatalf("读不到日程: %v", err)
	}
	return ws
}
