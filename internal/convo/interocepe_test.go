package convo

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

// numRE 形如 0.42 / -0.31 的数值。
var numRE = regexp.MustCompile(`-?\d\.\d`)

// TestMoodLine_GivesFeelingNotQuantity
//
// ⚠️ §4.6.2 内感受模糊: 注入的是**感觉**不是**量**。
// 病灶是原实现里的 `【你现在的心情】有点烦 (-0.42, 精神 0.31)。` ——
// 给了两个小数, 模型就会去**报数**("我今天心情大概negative")。
// 真人读自己是「有点烦」, 不是「valence -0.42」。
func TestMoodLine_GivesFeelingNotQuantity(t *testing.T) {
	now := time.Now()
	m := MoodState{Valence: -0.42, Arousal: 0.31,
		Causes: []MoodCause{{Text: "下班路上被雨淋了", TS: now.Format(bodyTSLayout)}}}
	// ⚠️ 反面锚点: 成因真的在 state 里。不在的话, 下面那条"不许出现成因"就是恒真的。
	if len(m.Causes) == 0 || m.Causes[0].Text == "" {
		t.Fatal("用例没有成因 —— 这条测试会变成恒真")
	}
	line := m.Line(now)
	if !strings.Contains(line, "【你现在的心情】") {
		t.Fatalf("心情段本身不见了: %q", line)
	}
	if numRE.MatchString(line) {
		t.Errorf("注入里出现了数值 —— §4.6.2 要求给感觉不给量: %q", line)
	}
	for _, bad := range []string{"起因", "淋"} {
		if strings.Contains(line, bad) {
			t.Errorf("注入里出现了成因(%q) —— 而同一段还写着「不许解释原因」: %q", bad, line)
		}
	}
	// 感受本身必须还在。
	if !strings.Contains(line, m.Label()) {
		t.Errorf("心情标签不见了 —— 不能把糊当成删: %q", line)
	}
}

// TestBodyLine_KeepsFeelingDropsCause
//
// ⚠️ 边界(设计稿 §4.6.2「哪一类事实可以糊」): **感受留着, 归因去掉**。
// 它知道自己不舒服(BodyLabel = 感受); 不知道是"昨晚喝多了"还是"变天了"(归因)。
// 与 §4.2 的硬线配合: "被问到了可以简短承认一次" —— 承认的是**感受**, 不是**归因**。
func TestBodyLine_KeepsFeelingDropsCause(t *testing.T) {
	now := time.Now()
	b := BodyState{
		Kind:    BodyHangover,
		Cause:   "昨天喝了酒",
		Note:    "头还有点沉",
		OnsetTS: now.Add(-6 * time.Hour).Format(bodyTSLayout),
		CourseH: 72,
	}
	b.Refresh(now)
	if !b.Active {
		t.Fatalf("用例构造有误: 6 小时 / 72 小时病程应当是 Active 的")
	}
	if b.Cause == "" {
		t.Fatal("用例没有起因 —— 这条测试会变成恒真")
	}
	line := b.Line(now)
	if strings.Contains(line, "起因") || strings.Contains(line, "喝了酒") {
		t.Errorf("身体注入里出现了归因: %q", line)
	}
	if !strings.Contains(line, BodyLabel(BodyHangover)) {
		t.Errorf("身体的**感受**不见了 —— 它得知道自己不舒服才能被问到时承认一次: %q", line)
	}
}

// TestInsomniaLine_DropsCauseKeepsSafetyFilter
//
// ⚠️ 两件事要同时成立:
//
//	① §4.6.2: 成因**不再注入**;
//	② §10 归因红线**不能被顺手删掉** —— 那是安全线, 不是措辞问题。
func TestInsomniaLine_DropsCauseKeepsSafetyFilter(t *testing.T) {
	line, ok := InsomniaCauseLine("闷热 心里有点事")
	if !ok {
		t.Fatal("干净的成因应当能出线")
	}
	if strings.Contains(line, "闷热") || strings.Contains(line, "（") {
		t.Errorf("失眠注入里还带着成因: %q", line)
	}
	if !strings.Contains(line, "昨晚没睡好") {
		t.Errorf("感受本身不见了: %q", line)
	}
	// 安全线: 归因指向用户的, 整条丢弃。
	for _, blame := range []string{"都怪你", "因为你", "你害的"} {
		if _, ok := InsomniaCauseLine("昨晚没睡好 " + blame + " 不让我睡"); ok {
			t.Errorf("归因红线的成因仍然出线了: %q", blame)
		}
	}
}
