package convo

import (
	"strings"
	"testing"
	"time"

	"github.com/tzhnahida/VocabLock/internal/clock"
)

func thirstEngine(t *testing.T) (*Engine, *Store) {
	t.Helper()
	s := agTestStore(t)
	if err := EnsureThirstSchema(s); err != nil {
		t.Fatal(err)
	}
	return &Engine{Store: s, Cfg: DefaultConfig()}, s
}

// TestThirstBudget_CapsPerDayWithoutTellingItTheNumber
//
// ⚠️ §4.2.4: 每天 ≤2 次。但**生活上限要内在化** ——
// 超了之后的做法是"**不给许可**", 不是"告诉它你今天还有 0 次"。
// 所以这里断言两件事: 闸门真的会关, 而且 Why 里的话**不许进提示词**。
func TestThirstBudget_CapsPerDayWithoutTellingItTheNumber(t *testing.T) {
	e, s := thirstEngine(t)
	now := clock.Now()
	if b := e.thirstBudget(now); !b.Allowed {
		t.Fatal("还没用过就不给 —— 那这个例外等于不存在")
	}
	for i := 0; i < ThirstQuotaPerDay; i++ {
		if _, err := s.CounterInc("thirst", clock.Today()); err != nil {
			t.Fatal(err)
		}
	}
	b := e.thirstBudget(now)
	if b.Allowed {
		t.Fatalf("用满了 %d 次之后还放行", ThirstQuotaPerDay)
	}
	if strings.TrimSpace(b.Why) == "" {
		t.Error("不给许可却不说为什么 —— 日志里会变成空白")
	}
	// ⚠️ 闸门关掉之后, 提示词里**不许出现任何次数**。
	line := BodyAxesLine(Axes{Thirst: 0.9}, b.Allowed)
	if strings.Contains(line, "2") || strings.Contains(line, "次") {
		t.Errorf("配额宣告漏进提示词了:\n%s", line)
	}
	if !strings.Contains(line, "不要说出口") {
		t.Errorf("闸门关了, 提示词却没收回许可:\n%s", line)
	}
}

// TestBodyAxesLine_ThirstExceptionIsConditional
//
// ⚠️ 同一个口渴读数, 许可开着和关着**必须是两句不同的话** ——
// 否则那个开关根本没有消费者。
func TestBodyAxesLine_ThirstExceptionIsConditional(t *testing.T) {
	a := Axes{Thirst: 0.9}
	open := BodyAxesLine(a, true)
	shut := BodyAxesLine(a, false)
	if open == shut {
		t.Fatal("许可开关没有改变提示词 —— 那这个闸门是个摆设")
	}
	if !strings.Contains(open, "可以说出口") {
		t.Errorf("开着的时候没给许可:\n%s", open)
	}
	if strings.Contains(shut, "可以说出口") {
		t.Errorf("关着的时候还留着许可:\n%s", shut)
	}
}

// TestValidateFrame_ThirstSolicitIsHardRejected
//
// ⚠️ §4.2.4 那条线**只有一线之隔**, 但那一线就是全部:
// 「我去倒杯水」是**陈述**, 「我好渴啊」是**等对方接话**。这个可以硬判, 就不靠自觉。
func TestValidateFrame_ThirstSolicitIsHardRejected(t *testing.T) {
	d := &DialogEngine{}
	p := &PersonaProfile{Name: "小夜", Template: "bestie"}
	in := DialogInput{UserText: "在吗"}
	frame := func(text string) OutputFrame {
		return OutputFrame{Reply: []FrameItem{{Kind: "text", Text: text}}}
	}
	// 正面: 陈述式必须放行 —— 收窄才是这条的重点。
	for _, ok := range []string{"等下 我去倒杯水", "接杯水去", "下楼买杯咖啡"} {
		if v := d.ValidateFrame(frame(ok), in, p); len(v) != 0 {
			t.Errorf("误伤了正常的说法 %q: %v", ok, v)
		}
	}
	// 反面: 索取式必须拦。
	for _, bad := range []string{"我好渴啊", "渴死了", "好想喝点冰的"} {
		if v := d.ValidateFrame(frame(bad), in, p); len(v) == 0 {
			t.Errorf("把口渴说成索取 %q —— 必须拦", bad)
		}
	}
}

// TestNoteThirstMention_ClichéTriggersCooldown
//
// ⚠️ §4.2.4「不许变成口头禅」: 7 天内同一句式重复 ≥3 次 → **冷却一周**。
// 这是"随口说"这条通道最容易被滥用的方向 —— 一个天天说同一句的角色, 听起来是坏掉的。
func TestNoteThirstMention_ClichéTriggersCooldown(t *testing.T) {
	e, s := thirstEngine(t)
	for i := 0; i < ThirstClichéLimit; i++ {
		e.NoteThirstMention("等下 我去倒杯水")
	}
	// 句式确实记了账 —— ⚠️ 键是**正则命中的那一段**(「倒杯」), 不是整句话。
	// 这里用正则自己的输出去查, 免得测试和实现各写一份"什么算同一句式"。
	m := thirstMentionRE.FindString("等下 我去倒杯水")
	if m == "" {
		t.Fatal("正则认不出「我去倒杯水」—— 那反口头禅就没有输入")
	}
	n, err := s.CountThirstPhrase(m, clock.Now(), ThirstClichéWindowDays)
	if err != nil {
		t.Fatal(err)
	}
	if n < ThirstClichéLimit {
		t.Fatalf("同一句式记了 %d 次(应当 ≥%d)—— 反口头禅就是空话", n, ThirstClichéLimit)
	}
	// 判定口头禅之后, 出口必须关掉。
	if b := e.thirstBudget(clock.Now()); b.Allowed {
		t.Fatal("同一句式说了 3 次, 冷却没生效")
	}
}

// TestNoteThirstMention_IgnoresNonMentions
//
// ⚠️ 反面: **聊到喝水**和**去喝水**是两件事。认宽了会把正常聊天算成一次额度。
func TestNoteThirstMention_IgnoresNonMentions(t *testing.T) {
	e, s := thirstEngine(t)
	for _, text := range []string{"今天天气不错", "你多喝水", "那家店的杯子挺好看"} {
		e.NoteThirstMention(text)
	}
	if n := counterOrZero(s, "thirst", clock.Today()); n != 0 {
		t.Fatalf("把非提及算成了 %d 次", n)
	}
}

// TestDrinkEvent_IsGeneratableAndSmall
//
// ⚠️ §4.2.4: 饮料事件的幅度**必须小**(±0.05~0.15) ——
// 一杯饮料不该推动一整天的情绪。它只是给"今天买到一杯超难喝的"一个成因链位置。
func TestDrinkEvent_IsGeneratableAndSmall(t *testing.T) {
	rule, ok := eventGenKinds["drink"]
	if !ok {
		t.Fatal("事件池里没有 drink 这一类 —— 那「买了点什么喝」永远不会发生")
	}
	// downstream kind 必须是下游认识的枚举, 否则分支全部落空。
	switch rule.Kind {
	case "environment", "calendar", "social", "relation", "self":
	default:
		t.Fatalf("drink 的 downstream kind 不在枚举里: %q", rule.Kind)
	}
	if rule.Intensity > 0.4 {
		t.Errorf("饮料事件的强度太大了: %.2f", rule.Intensity)
	}
	if len(eventGenDrinkPool) == 0 {
		t.Fatal("drink 模板池是空的")
	}
	// ⚠️ **注意这条断言不够**: 它只证明"声明了", 不证明"接上了"。
	// 第一版就是只测到这里, 而池子从未被拼进生产路径 —— 测试绿着, 部署出去的二进制里连字符串都没有。
	// 真正管用的是下面的 TestDrinkEvent_IsReachableFromTheProductionPath。
	hasPos, hasNeg := false, false
	for _, tpl := range eventGenDrinkPool {
		if tpl.Valence > 0 {
			hasPos = true
		}
		if tpl.Valence < 0 {
			hasNeg = true
		}
		if v := tpl.Valence; v > 0.15 || v < -0.15 {
			t.Errorf("模板幅度超了(±0.15): %q %.2f", tpl.Text, v)
		}
	}
	if !hasPos || !hasNeg {
		t.Error("好喝和难喝都要有 —— 只有一边就推不出「间接影响心情」")
	}
	_ = time.Now
}

// TestDrinkEvent_IsReachableFromTheProductionPath
//
// ⚠️ **这条是补的, 而且它是这一项唯一真正管用的尺子。**
//
// 第一版只断言了「`len(eventGenDrinkPool) > 0`」—— 那条测试**绿着**,
// 而池子从未被拼进 `EventGenFallbackPool`(死代码), 部署之后二进制里连模板字符串都没有。
// 是部署后的 `grep -a` 把它抓出来的, 不是测试。
//
// **「声明了」和「接上了」是两件事, 而只有可达性能失败。**
func TestDrinkEvent_IsReachableFromTheProductionPath(t *testing.T) {
	drinkTexts := map[string]bool{}
	for _, tpl := range eventGenDrinkPool {
		drinkTexts[tpl.Text] = true
	}
	base := time.Date(2026, 10, 15, 12, 0, 0, 0, time.Local)
	persona := &PersonaProfile{Identity: "student"}
	env := EnvDay{City: "测试", Weather: "晴", TempC: 20}
	hits, days := 0, 0
	for i := 0; i < 60; i++ {
		now := base.AddDate(0, 0, i)
		days++
		for _, ev := range EventGenFallbackPool(env, persona, now) {
			if drinkTexts[ev.Text] {
				hits++
			}
		}
	}
	if hits == 0 {
		t.Fatal("六十天里一条饮料事件都排不出来 —— 池子接上了却不可达(死代码)")
	}
	// 反面锚点: 也不许天天有 —— 设计稿给的 p_trigger 是 0.3 量级。
	if hits > days {
		t.Errorf("六十天里 %d 条饮料事件, 太密了(p_trigger 应当是 0.3 量级)", hits)
	}
	// 确定性: 同一天跑两次必须逐字节一样, 否则"重跑就换事件"= 时间线不可修复。
	a1 := EventGenFallbackPool(env, persona, base)
	a2 := EventGenFallbackPool(env, persona, base)
	if len(a1) != len(a2) {
		t.Fatal("同一天两次采样条数不同 —— 那它不是确定性的")
	}
	for i := range a1 {
		if a1[i].Text != a2[i].Text {
			t.Fatalf("同一天两次采样不同: %q vs %q", a1[i].Text, a2[i].Text)
		}
	}
}
