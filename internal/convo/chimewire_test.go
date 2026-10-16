package convo

import (
	"testing"
	"time"

	"github.com/tzhnahida/convo/internal/clock"
)

func chimeEngine(t *testing.T, roll int) *Engine {
	t.Helper()
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	e.ChimeRollFn = func(n int) int { return roll }
	return e
}

// ⚠️ **本批的头号契约**: 配额真的封得住 —— 判了但没插不消耗配额,
// 插了就必须记账, 否则它会一直插。
func TestChimeAt_QuotaIsConsumed(t *testing.T) {
	e := chimeEngine(t, 0) // 永远掷中
	sc := SceneMsg{SceneKey: "group:12345", UIN: "999", Text: "随便聊聊"}
	owner := e.ownerKey()
	// ⚠️ 三次**不能连着用** —— 25 分钟冷却会挡。所以直接播种计数,
	// 而不是连插三次 (那测的是冷却, 不是配额)。
	last := clock.Now().Add(-time.Hour).Format(bodyTSLayout)
	if _, err := e.Store.DB.Exec("INSERT INTO convo_chime_state (owner, scene_key, used_day, used_n, topic_n, last_ts, updated_ts) "+
		"VALUES (?,?,?,?,0,?,?)", owner, sc.SceneKey, clock.Today(), ChimeQuotaPerDay, last, clock.TodayTS()); err != nil {
		t.Fatal(err)
	}
	if d := e.chimeAt(sc); d.Speak {
		t.Fatal("配额用完了还在插 —— 那 10% 就是刷屏")
	}
	// 配额没满时同样的条件必须能插 (否则上面那条可能是别的原因挡的)。
	if _, err := e.Store.DB.Exec("UPDATE convo_chime_state SET used_n = 0 WHERE owner = ? AND scene_key = ?", owner, sc.SceneKey); err != nil {
		t.Fatal(err)
	}
	if d := e.chimeAt(sc); !d.Speak {
		t.Fatal("配额归零后应当能插 —— 否则上面那条是被别的东西挡的")
	}
}

// ⚠️ 冷却也必须真的封得住。
func TestChimeAt_CooldownBlocks(t *testing.T) {
	e := chimeEngine(t, 0)
	sc := SceneMsg{SceneKey: "group:1", Text: "闲聊"}
	if !e.chimeAt(sc).Speak {
		t.Fatal("第一次应当能插")
	}
	if err := e.Store.BumpChime(e.ownerKey(), sc.SceneKey, false); err != nil {
		t.Fatal(err)
	}
	if e.chimeAt(sc).Speak {
		t.Fatal("刚插完又插 —— 冷却没生效, 连着插比不插更烦人")
	}
}

// ⚠️ 被群友说过之后, 5% 的骰子不该再命中。
func TestChimeAt_ComplaintLowersRate(t *testing.T) {
	e := chimeEngine(t, 5) // 常态 10% 会中, 收敛后 1% 不会
	sc := SceneMsg{SceneKey: "group:2", Text: "闲聊"}
	if !e.chimeAt(sc).Speak {
		t.Fatal("常态下 5% 应当命中")
	}
	if err := e.Store.MarkChimeComplaint(e.ownerKey(), sc.SceneKey); err != nil {
		t.Fatal(err)
	}
	if e.chimeAt(sc).Speak {
		t.Fatal("被说过之后 5% 不该命中 —— 收敛没生效")
	}
}

// ⚠️ 读失败/无场景 → **退回沉默**。插话是打扰那一侧的例外, 失败只能往"不做"倒。
func TestChimeAt_FailsClosed(t *testing.T) {
	e := chimeEngine(t, 0)
	if e.chimeAt(SceneMsg{SceneKey: "", Text: "x"}).Speak {
		t.Fatal("没有场景键不该插话")
	}
}

// ⚠️ 只数**别人**说的 —— 把 AI 自己插的也数进去的话,
// 它插得越多额度越高, 是个正反馈。
func TestGroupMsgsToday_ExcludesAI(t *testing.T) {
	s := newTestStore(t)
	sk := "group:3"
	for i := 0; i < 5; i++ {
		if _, err := s.InsertMessageFull("user", "qq", "text", "群友的话", MsgMeta{SceneKey: sk}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 9; i++ {
		if _, err := s.InsertMessageFull("ai", "qq", "text", "它自己插的", MsgMeta{SceneKey: sk}); err != nil {
			t.Fatal(err)
		}
	}
	if got := s.GroupMsgsToday(sk); got != 5 {
		t.Fatalf("应当只数别人的 5 条, got %d", got)
	}
	// 场景隔离。
	if got := s.GroupMsgsToday("group:other"); got != 0 {
		t.Fatalf("串场景了: %d", got)
	}
}

// ⚠️ 两笔账分开, 而且**跨天自动归零** (但 complained_ts 不归零)。
func TestChimeUsage_DayRolloverAndTwoAccounts(t *testing.T) {
	s := newTestStore(t)
	owner, sk := "u1", "group:4"
	if u, tp, since, err := s.ChimeUsage(owner, sk); err != nil || u != 0 || tp != 0 || since != 0 {
		t.Fatalf("空库应当是零值: %d %d %v %v", u, tp, since, err)
	}
	if err := s.BumpChime(owner, sk, false); err != nil {
		t.Fatal(err)
	}
	if err := s.BumpChime(owner, sk, true); err != nil {
		t.Fatal(err)
	}
	if err := s.BumpChime(owner, sk, true); err != nil {
		t.Fatal(err)
	}
	u, tp, since, err := s.ChimeUsage(owner, sk)
	if err != nil {
		t.Fatal(err)
	}
	if u != 1 || tp != 2 {
		t.Fatalf("两笔账应当分开记 (随机 1 / 话题 2), got %d / %d", u, tp)
	}
	if since <= 0 {
		t.Fatal("应当记下距上次插话多久")
	}
	// 伪造一条昨天的记录 → 计数必须归零, 收敛不许归零。
	if _, err := s.DB.Exec("UPDATE convo_chime_state SET used_day = ? WHERE owner = ? AND scene_key = ?", "2000-01-01", owner, sk); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkChimeComplaint(owner, sk); err != nil {
		t.Fatal(err)
	}
	u2, tp2, _, _ := s.ChimeUsage(owner, sk)
	if u2 != 0 || tp2 != 0 {
		t.Fatalf("跨天应当归零, got %d / %d", u2, tp2)
	}
	if ok, _ := s.ChimeComplained(owner, sk); !ok {
		t.Fatal("抱怨状态**不该**随天数归零 —— 收敛是永久的")
	}
}

func TestHhmm(t *testing.T) {
	cases := map[string]int{"00:00": 0, "07:30": 450, "23:30": 1410, "24:00": -1}
	for in, want := range cases {
		got, ok := hhmm(in)
		if want < 0 {
			if ok {
				t.Fatalf("%q 不该合法", in)
			}
			continue
		}
		if !ok || got != want {
			t.Fatalf("%q → %d ok=%v, want %d", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "7:30", "0730", "aa:bb", "25:00", "07:60"} {
		if _, ok := hhmm(bad); ok {
			t.Fatalf("%q 不该合法", bad)
		}
	}
}

// ⚠️ 用"醒着的时间"而不是"一天过了几分之几": 它凌晨 3 点本来就该在睡,
// 按整天算的话深夜会凭空多出一截预算。
func TestWakingElapsed_NoScheduleIsOne(t *testing.T) {
	e := chimeEngine(t, 0)
	if got := e.wakingElapsed(); got != 1 {
		t.Fatalf("读不到日程应当当作一整天 (保守方向是让它能说话), got %v", got)
	}
}

// 话题匹配: 它自己什么都没攒下时不该"正好知道"任何事。
func TestChimeTopicMatch_EmptyIsFalse(t *testing.T) {
	e := chimeEngine(t, 0)
	if e.chimeTopicMatch(SceneMsg{SceneKey: "group:5", Text: "随便什么话题"}) {
		t.Fatal("灵光和学过的都是空的时候, 不该判定为话题命中")
	}
}

// 攒下东西之后, 群友聊到同样的词就该命中。
func TestChimeTopicMatch_HitsOnSharedCue(t *testing.T) {
	e := chimeEngine(t, 0)
	if _, err := e.Store.SaveLearned(LearnedItem{Kind: LearnFact, TopicKey: "枣椰蜜糖",
		Content: "枣椰蜜糖是须弥的一种甜点", Origin: "自测"}); err != nil {
		t.Fatal(err)
	}
	if !e.chimeTopicMatch(SceneMsg{SceneKey: "group:6", Text: "你们吃过枣椰蜜糖吗"}) {
		t.Fatal("群友聊到了它学过的东西, 应当命中")
	}
	if e.chimeTopicMatch(SceneMsg{SceneKey: "group:6", Text: "今天股市怎么样"}) {
		t.Fatal("毫无关系不该命中")
	}
}

var _ = clock.Now
var _ = time.Hour
