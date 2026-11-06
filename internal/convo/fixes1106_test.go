package convo

import (
	"strings"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────
// 2026-11-06 用户批准 A–E 五项 (m01386) 的回归用例。
//
// 五项都不是"提示词调一调", 而是**出口/入口的硬闸**:
// A 延迟回复三道闸 · B 历史时间锚 · C 折叠去图注 · D 表情节奏 · E 问句配额。
// 每条正面断言都配一条反面用例: 只钉"拦得住"的测试, 会把功能冻死。
// ─────────────────────────────────────────────────────────────

// A1: busy 时**没有一句话可回**的消息不排延迟回复。
//
// 事故 (11-05 21:10): owner 连发两条纯表情 → 排了 79 分钟的延迟回复 →
// 22:30 它醒来对着四天前的旧对话说了三句。表情不是"欠一句话"。
func TestBusyNeedsDelayedReply(t *testing.T) {
	cases := []struct {
		name string
		text string
		img  string
		want bool
	}{
		{"纯 QQ 表情", "[QQ表情265]", "", false},
		{"落库版表情标注", "(表情 — 陈述) [图: 一个表情]", "", false},
		{"纯占位", "[表情]", "", false},
		{"空串", "", "", false},
		{"表情 + 图", "[表情]", "/data/convo/img/a.jpg", true},
		{"一句普通话", "周末干嘛", "", true},
		{"图注后还有正文", "[图: 猫] 这也太草了", "", true},
	}
	for _, c := range cases {
		if got := busyNeedsDelayedReply(c.text, c.img); got != c.want {
			t.Errorf("%s: busyNeedsDelayedReply(%q, %q) = %v, 想要 %v", c.name, c.text, c.img, got, c.want)
		}
	}
}

// A2: 已经排了一条就不再排 —— 覆盖 = 把回复越推越晚。
func TestDelayedReply_NoOverwrite(t *testing.T) {
	s, err := OpenStore(t.TempDir(), "delay-test.db")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer s.Close()
	e := &Engine{Store: s, Cfg: DefaultConfig()}
	base := time.Date(2026, 11, 5, 21, 10, 51, 0, time.Local)
	adv := withFrozenClock(t, base)

	e.scheduleDelayedReply()
	due1, ok := e.pendingReplyDue()
	if !ok {
		t.Fatal("没有排上延迟回复")
	}
	if d := due1.Sub(base); d < 30*time.Minute || d > 90*time.Minute {
		t.Fatalf("延时应落在 30-90 分钟, 实际 %s", d)
	}
	at1, _, _ := s.MetaGet("pending_reply_at")
	if strings.TrimSpace(at1) == "" {
		t.Fatal("触发时刻没落库 —— 到点后就无法判它有多旧")
	}

	// 5 分钟后又来一条: 必须**保留原来那条**, 不重掷。
	adv(base.Add(5 * time.Minute))
	e.scheduleDelayedReply()
	due2, _ := e.pendingReplyDue()
	if !due2.Equal(due1) {
		t.Fatalf("排队被覆盖: %s → %s", due1.Format(time.RFC3339), due2.Format(time.RFC3339))
	}
	at2, _, _ := s.MetaGet("pending_reply_at")
	if at2 != at1 {
		t.Fatalf("触发时刻被覆盖: %q → %q", at1, at2)
	}
}

// A3: 到点后重判 —— 迟到太久 / 触发太旧 / 跨天, 一律作废。
func TestConsumeDelayedReply(t *testing.T) {
	s, err := OpenStore(t.TempDir(), "delay-consume.db")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer s.Close()
	e := &Engine{Store: s, Cfg: DefaultConfig()}
	base := time.Date(2026, 11, 5, 22, 30, 0, 0, time.Local)
	withFrozenClock(t, base)
	set := func(due, at time.Time) {
		_ = s.MetaSet("pending_reply_due", due.Format(time.RFC3339))
		_ = s.MetaSet("pending_reply_at", at.Format(time.RFC3339))
	}

	// ① 正常: 5 分钟前到点, 一小时前触发 → 放行, 且清掉排队状态。
	set(base.Add(-5*time.Minute), base.Add(-65*time.Minute))
	if ok, why := e.consumeDelayedReply(); !ok {
		t.Fatalf("正常的延迟回复被作废: %s", why)
	}
	if _, still := e.pendingReplyDue(); still {
		t.Fatal("consume 之后排队状态没清 —— 会每分钟重判一次")
	}

	// ② 迟到两小时: 那时群里早换了话题。
	set(base.Add(-2*time.Hour), base.Add(-3*time.Hour))
	if ok, _ := e.consumeDelayedReply(); ok {
		t.Fatal("迟到两小时还发")
	}

	// ③ 触发在四小时前 (同一天): 接的是早就凉了的话头。
	set(base.Add(-5*time.Minute), base.Add(-4*time.Hour))
	if ok, _ := e.consumeDelayedReply(); ok {
		t.Fatal("触发太旧还发")
	}

	// ④ 跨天: 昨天排的队今天不能还。
	set(base.Add(-5*time.Minute), base.AddDate(0, 0, -1))
	if ok, _ := e.consumeDelayedReply(); ok {
		t.Fatal("跨天还发")
	}

	// ⑤ 没有排队 → 不许凭空发。
	if ok, _ := e.consumeDelayedReply(); ok {
		t.Fatal("没有排队却放行")
	}

	// ⑥ 旧版本排的队 (没有触发时刻): 放行 —— 否则升级后老数据永远发不出去。
	_ = s.MetaSet("pending_reply_due", base.Add(-5*time.Minute).Format(time.RFC3339))
	_ = s.MetaSet("pending_reply_at", "")
	if ok, why := e.consumeDelayedReply(); !ok {
		t.Fatalf("缺触发时刻的旧排队应当放行, 却被作废: %s", why)
	}
}

// B: 注入的历史必须有时间锚 —— 跨天与断档 ≥3 小时要插一行分隔。
//
// 事故: 11-05 22:30 的【最近对话】只有行首序号 (1,2,3 跨天重号, 无日期),
// 模型读到的是"刚才", 于是把 10-31 的话头当成当下接着演。
func TestHistoryTimeGap(t *testing.T) {
	today := "2026-11-05"
	cases := []struct {
		name string
		prev string
		cur  string
		want string
	}{
		{"第一条", "", "2026-11-05 22:30:00", ""},
		{"紧接着", "2026-11-05 22:29:00", "2026-11-05 22:30:00", ""},
		{"同日两小时", "2026-11-05 20:00:00", "2026-11-05 22:30:00", ""},
		{"同日四小时", "2026-11-05 18:00:00", "2026-11-05 22:30:00", "隔了 4 小时"},
		{"跨天五天", "2026-10-31 10:29:00", "2026-11-05 22:30:00", "10-31"},
		{"跨天昨天", "2026-11-04 23:00:00", "2026-11-05 08:00:00", "昨天"},
		{"时间倒流", "2026-11-05 22:30:00", "2026-11-05 20:00:00", ""},
	}
	for _, c := range cases {
		got := HistoryTimeGap(c.prev, c.cur, today)
		if c.want == "" {
			if got != "" {
				t.Errorf("%s: 不该插分隔, 却得到 %q", c.name, got)
			}
			continue
		}
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: %q 里没有 %q", c.name, got, c.want)
		}
	}
	if !strings.Contains(HistoryTimeGap("2026-10-31 10:29:00", "2026-11-05 22:30:00", today), "5 天前") {
		t.Error("跨天分隔里应当带上「N 天前」")
	}
}

// D: 表情包节奏 —— 文字后 20 秒内不发, 两张之间至少 60 秒。
func TestStickerPaceOK(t *testing.T) {
	s, err := OpenStore(t.TempDir(), "sticker-pace.db")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer s.Close()
	e := &Engine{Store: s, Cfg: DefaultConfig()}
	base := time.Date(2026, 11, 6, 19, 26, 0, 0, time.Local)
	adv := withFrozenClock(t, base)

	// 没有任何消息 → 可以发。
	if !e.StickerPaceOK(DefaultSceneKey) {
		t.Fatal("空白场景应当允许发表情")
	}

	// 刚说完一句话 → 不许贴图 (实测 12:54:46 文字与表情同一秒)。
	if _, err := s.InsertMessage("ai", "qq", "text", "这也太草了"); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	if e.StickerPaceOK(DefaultSceneKey) {
		t.Fatal("文字发出后立刻贴表情, 应当被拦")
	}
	adv(base.Add(25 * time.Second))
	if !e.StickerPaceOK(DefaultSceneKey) {
		t.Fatal("文字已过 25 秒, 应当放行")
	}

	// 刚发过一张表情 → 60 秒内不许再发 (实测 19:26:15 连发两张一样的)。
	if _, err := s.InsertMessage("ai", "qq", "sticker", "[表情]"); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	if e.StickerPaceOK(DefaultSceneKey) {
		t.Fatal("两张表情挨在一起, 应当被拦")
	}
	adv(base.Add(90 * time.Second)) // 这一张是在 +25s 发的, 到 +90s 才满 60 秒
	if !e.StickerPaceOK(DefaultSceneKey) {
		t.Fatal("表情已隔 60 秒以上, 应当放行")
	}
}

// E: 问句配额按最近几轮浮动 —— 刚连着问过, 这一轮一个都不许有。
func TestQuestionCapFor(t *testing.T) {
	s, err := OpenStore(t.TempDir(), "qcap.db")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer s.Close()
	e := &Engine{Store: s, Cfg: DefaultConfig()}
	base := time.Date(2026, 11, 5, 18, 0, 0, 0, time.Local)
	adv := withFrozenClock(t, base)

	if got := e.QuestionCapFor(DefaultSceneKey); got != BubbleMaxQuestions {
		t.Fatalf("没有历史时应当是默认上限 %d, 实际 %d", BubbleMaxQuestions, got)
	}

	// 10 分钟内已经问过一句 → 还能再问一句。
	if _, err := s.InsertMessage("ai", "qq", "text", "这啥？"); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	adv(base.Add(time.Minute))
	if got := e.QuestionCapFor(DefaultSceneKey); got != BubbleMaxQuestions {
		t.Fatalf("只问过一句, 不该禁问: %d", got)
	}

	// 又问一句 (共两句) → 这一轮禁问。
	if _, err := s.InsertMessage("ai", "qq", "text", "笑啥呢？"); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	adv(base.Add(2 * time.Minute))
	if got := e.QuestionCapFor(DefaultSceneKey); got != 0 {
		t.Fatalf("十分钟内问过两句, 这一轮应当禁问, 实际上限 %d", got)
	}

	// 过了疲劳窗口 → 恢复。
	adv(base.Add(QuestionFatigueWindow + 20*time.Minute))
	if got := e.QuestionCapFor(DefaultSceneKey); got != BubbleMaxQuestions {
		t.Fatalf("窗口过了应当恢复, 实际 %d", got)
	}
}

// C1: 折叠正文必须先剥掉视觉标注前缀, 否则「表情」会变成当天的话题词。
func TestFoldPlainText_StripsLabelPrefix(t *testing.T) {
	cases := map[string]string{
		"(表情 — 陈述) [图: 一个表情]": "",
		"(表情 — 陈述) 这也太草了":     "这也太草了",
		"(照片 — 示好)":           "",
		"表情":                  "表情", // 不带括号的「表情」是真在说话
		"这也太草了 (表情 — 陈述)":     "这也太草了 (表情 — 陈述)",
	}
	for in, want := range cases {
		if got := FoldPlainText(in); got != want {
			t.Errorf("FoldPlainText(%q) = %q, 想要 %q", in, got, want)
		}
	}
}

// C2: 折叠文案不是排行榜 —— 人回想一天不会说「聊得最多的是」。
func TestFoldText_NoRankingTone(t *testing.T) {
	r := FoldResult{Total: 10, Topics: []string{"耳机"}}
	got := FoldText(r)
	if strings.Contains(got, "最多") {
		t.Fatalf("还有排行腔: %q", got)
	}
	if !strings.Contains(got, "耳机") {
		t.Fatalf("话题丢了: %q", got)
	}
	r.Quote = "耳机复活了"
	got = FoldText(r)
	if strings.Contains(got, "最多") {
		t.Fatalf("还有排行腔: %q", got)
	}
	if !strings.Contains(got, "耳机复活了") {
		t.Fatalf("具体那句话丢了: %q", got)
	}
}
