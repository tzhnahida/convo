package convo

import (
	"os"
	"strings"
	"testing"

	"github.com/tzhnahida/VocabLock/internal/clock"
)

// ─────────────────────────────────────────────────────────────
// #11 / #12: 清掉**说谎的读数**
//
// 这两条是同一类病: 一个读数**读错了源**, 而它是静默的 ——
// 界面上一切正常, 只是那个数字**不是真的**。而"读数撒谎比没有读数更糟":
// 你会照着它去修一个根本没坏的东西。
// ─────────────────────────────────────────────────────────────

// ——— #12 睡眠窗: 槽表是真相源, 展示字段只是"一般情况" ———

func sleepWindowWeek(t *testing.T, sleepSlotStart string) *WeekSchedule {
	t.Helper()
	ws := &WeekSchedule{SleepStart: "23:30", SleepEnd: "07:30"} // 展示字段**照旧**写 23:30
	for _, d := range dayOrder {
		slots := stdDay("07:30", StateFree, StateFocus, "09:00", "17:30")
		if sleepSlotStart != "" {
			// 把夜里那一段的起点挪走, 但**不动展示字段** —— 这正是撒谎的场景。
			slots[len(slots)-1].Start = sleepSlotStart
			slots[len(slots)-2].End = sleepSlotStart
		} else {
			// 整天没有 sleep 槽 (生产上真出现过: 周三整日 0 分钟睡眠)。
			var kept []Slot
			for _, sl := range slots {
				if sl.State != StateSleep {
					kept = append(kept, sl)
				}
			}
			slots = kept
		}
		ws.Days = append(ws.Days, DaySchedule{Day: d, Slots: slots})
	}
	return ws
}

// TestSleepWindowOf_DerivesFromSlotsNotDisplayFields
//
// ⚠️ 展示字段写 23:30, 而槽表里它 23:00 就睡了 —— **槽表才是真的**。
// 读展示字段的每个地方都在撒谎, 而撒谎是静默的。
func TestSleepWindowOf_DerivesFromSlotsNotDisplayFields(t *testing.T) {
	ws := sleepWindowWeek(t, "23:00")
	sleep, wake, ok := SleepWindowOf(ws, "thu")
	if !ok {
		t.Fatal("槽表里有完整的睡眠段, 却报 ok=false")
	}
	if got := fmtHM(sleep); got != "23:00" {
		t.Errorf("睡点应当从**槽表**解出 23:00, 实得 %q (展示字段是 %q)", got, ws.SleepStart)
	}
	if got := fmtHM(wake); got != "07:30" {
		t.Errorf("起床点应当是 07:30, 实得 %q", got)
	}
}

// TestSleepWindowOf_ReportsMissingInsteadOfPretending
//
// ⚠️ 生产事故的形状: **周三整日没有 sleep 槽, 而 sleep_start 照样写着 23:30。**
// 那时正确的答案是"那天它没有睡眠段", 不是"用默认值补上"。
func TestSleepWindowOf_ReportsMissingInsteadOfPretending(t *testing.T) {
	ws := sleepWindowWeek(t, "")
	if _, _, ok := SleepWindowOf(ws, "wed"); ok {
		t.Fatal("整天没有 sleep 槽, 却报 ok=true —— 那就是拿展示字段在冒充")
	}
	// 反面锚点: 展示字段**还在**, 所以"用展示字段"的写法会返回 ok=true —— 这条测试才有的可测。
	if strings.TrimSpace(ws.SleepStart) == "" {
		t.Fatal("夹具坏了: 展示字段应当是有的, 否则测不出'冒充'")
	}
}

// TestWakingElapsed_UsesThatDaysSlots
//
// ⚠️ 反面: 在"今天没有睡眠段"的日程上, 正确的答案是**它整天都醒着**(=1),
// 而读展示字段会算出一个 0.84 之类的"深夜没预算"—— 那是假的。
func TestWakingElapsed_UsesThatDaysSlots(t *testing.T) {
	s := newTestStore(t)
	ws := sleepWindowWeek(t, "")
	// 把今天那天换成没有 sleep 槽的版本(夹具里七天都已经是了)。
	if err := s.SaveSchedule(ws); err != nil {
		t.Fatal(err)
	}
	e := &Engine{Store: s, Cfg: DefaultConfig()}
	if got := e.wakingElapsed(); got != 1 {
		t.Fatalf("那天没有睡眠段 → 它整天都醒着(1.0), 实得 %.2f —— 说明还在读展示字段", got)
	}
	_ = clock.Now()
}

// ——— #11 夜伴名册: 唯一口径 ———

func bibleWithCast(names ...string) WorldBible {
	b := WorldBible{Self: "信使"}
	for _, n := range names {
		b.Cast = append(b.Cast, CastMember{Name: n, Relation: RelationCompanion, Status: "在"})
	}
	return b
}

// TestNightRosterPlan_PrefersWorldCastOverTheManualTable
//
// ⚠️ **这条是本项的立案理由。** 控制台原来只读手工表, 而生产库那张表是空的,
// 夜间批走的是世界观 cast(5 人)。于是控制台永远显示"缺名册", 而它昨晚**实跑了 71 轮**。
func TestNightRosterPlan_PrefersWorldCastOverTheManualTable(t *testing.T) {
	s := agTestStore(t)
	owner := "owner"
	if err := s.SaveWorldBible(owner, bibleWithCast("可颂", "德克萨斯", "能天使", "拉普兰德", "空")); err != nil {
		t.Fatal(err)
	}
	// 手工表**故意留空** —— 正是生产的形状。
	peers, src := NightRosterPlan(s, owner, nil, nil)
	if len(peers) != 5 {
		t.Fatalf("世界观 cast 有 5 人, 控制台却只看到 %d 人 —— 这就是那条 bug", len(peers))
	}
	if src != "cast" {
		t.Errorf("来源应当是 cast, 实得 %q", src)
	}
	if got := strings.Join(nightPeerNames(peers), ","); !strings.Contains(got, "可颂") {
		t.Errorf("人名不对: %s", got)
	}
}

// TestNightRosterPlan_FallsBackInOrder
func TestNightRosterPlan_FallsBackInOrder(t *testing.T) {
	s := agTestStore(t)
	// ① 连世界观都没有 → none
	if ps, src := NightRosterPlan(s, "owner", nil, nil); len(ps) != 0 || src != "none" {
		t.Errorf("什么都没有时应当是 none, 实得 %d/%s", len(ps), src)
	}
	// ② 有配置 → configured
	cfg := []NightPeer{{UIN: 1, Name: "配置里的人"}}
	ps, src := NightRosterPlan(s, "owner", nil, cfg)
	if len(ps) != 1 || src != "configured" {
		t.Errorf("应当退回 configured, 实得 %d/%s", len(ps), src)
	}
	// ③ 只有手工表 → manual
	if err := s.SaveNightPeer(NightPeer{UIN: 2, Name: "手工表里的人"}); err != nil {
		t.Fatal(err)
	}
	if _, src := NightRosterPlan(s, "owner", nil, nil); src != "manual" {
		t.Errorf("应当退回 manual, 实得 %s", src)
	}
	// ④ 世界观一旦有 cast, 它**优先** —— 手工名册是补丁不是主路。
	// ⚠️ 世界观的关系层**至少要 2 人**(SaveWorldBible 会拦) —— 一个人讲不出它的一天。
	if err := s.SaveWorldBible("owner", bibleWithCast("世界观里的人", "另一个配角")); err != nil {
		t.Fatal(err)
	}
	if ps, src := NightRosterPlan(s, "owner", nil, cfg); src != "cast" || len(ps) != 2 || ps[0].Name != "世界观里的人" {
		t.Errorf("世界观 cast 应当优先于配置, 实得 %s / %+v", src, ps)
	}
}

// TestObservationRosterComesFromTheSingleSource
//
// ⚠️ 静态尺子: 控制台那次**不许**再直接读手工表。
// 这是一条会失败的断言 —— 谁把 LoadNightPeers 加回 handleObservation, 它立刻红。
func TestObservationRosterComesFromTheSingleSource(t *testing.T) {
	src, err := os.ReadFile("handlers_bias.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	i := strings.Index(body, "func (s *Server) handleObservation")
	if i < 0 {
		t.Fatal("找不到 handleObservation")
	}
	end := strings.Index(body[i:], "\nfunc ")
	if end < 0 {
		end = len(body)
	}
	fn := body[i : i+end]
	if !strings.Contains(fn, "NightRosterPlan(") {
		t.Error("handleObservation 没有走唯一口径 NightRosterPlan —— 控制台会再次撒谎")
	}
	if strings.Contains(fn, "LoadNightPeers(") {
		t.Error("handleObservation 又直接读手工表了 —— 生产库那张表是空的, 读数会再次撒谎")
	}
}
