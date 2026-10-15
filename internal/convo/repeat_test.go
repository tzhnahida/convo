package convo

import (
	"strings"
	"testing"
)

// ⚠️ **本批的核心判据** (§3.6.8「一致 ≠ 反复主动提」):
// 实测「枣椰蜜糖」占它当天发言 17.8% —— 真人提一次就过去了,
// 它每次冷场就捡起来再抛一次。
func TestCountSelfRepeats(t *testing.T) {
	msgs := []string{"枣椰蜜糖还我", "对了 枣椰蜜糖还藏着吗", "我想吃枣椰蜜糖", "枣椰蜜糖分你一半"}
	hits := CountSelfRepeats(msgs)
	// ⚠️ 报的是**合并后的完整词**, 不是 2 字切片 —— 见 MergesFragments。
	found := false
	for _, h := range hits {
		if h.Token == "枣椰蜜糖" {
			found = true
		}
	}
	if !found {
		t.Fatalf("反复出现的道具没被统计出来: %+v", hits)
	}
}

// ⚠️ 下限: 说两遍是正常的, 两次不该报警。
func TestCountSelfRepeats_BelowThreshold(t *testing.T) {
	if hits := CountSelfRepeats([]string{"枣椰蜜糖", "枣椰蜜糖"}); len(hits) != 0 {
		t.Fatalf("两次不该算反复: %+v", hits)
	}
}

// ⚠️ **必须分布在多条消息里** —— 否则"一条长消息里同一个词出现 5 次"
// 会被误判成"反复提", 而那只是一句话里的正常重复。
func TestCountSelfRepeats_NeedsMultipleMsgs(t *testing.T) {
	one := strings.Repeat("枣椰蜜糖", 6)
	if hits := CountSelfRepeats([]string{one}); len(hits) != 0 {
		t.Fatalf("同一条里重复不算反复提: %+v", hits)
	}
}

// ⚠️ 功能词反复出现是正常的, 报了就是噪声 —— 而噪声会让它紧张。
func TestCountSelfRepeats_FiltersStopwords(t *testing.T) {
	msgs := []string{"这个我知道", "那个我知道", "什么我知道", "怎么我知道"}
	for _, h := range CountSelfRepeats(msgs) {
		if repeatStopwords[h.Token] {
			t.Fatalf("停用词不该报警: %q", h.Token)
		}
	}
}

// ⚠️ **切片合并** (真实数据抓出来的缺陷): 「枣椰蜜糖」会变成
// 「枣椰」「椰蜜」「蜜糖」「枣椰蜜」「椰蜜糖」五条, 占满 TopN,
// 而真正该报的「闭关」「笔记」一条都进不来。
func TestCountSelfRepeats_MergesFragments(t *testing.T) {
	var msgs []string
	for i := 0; i < 5; i++ {
		msgs = append(msgs, "枣椰蜜糖给你留着呢")
	}
	for i := 0; i < 4; i++ {
		msgs = append(msgs, "我在闭关")
	}
	hits := CountSelfRepeats(msgs)
	for _, h := range hits {
		// 任何一条都不该是另一条的子串 —— 切片必须被合并掉。
		for _, o := range hits {
			if h.Token != o.Token && strings.Contains(o.Token, h.Token) {
				t.Fatalf("切片没合并: %q 是 %q 的子串 (%+v)", h.Token, o.Token, hits)
			}
		}
	}
	// 「闭关」必须能进榜 —— 不然它被道具的切片挤掉了。
	found := false
	for _, h := range hits {
		if h.Token == "闭关" {
			found = true
		}
	}
	if !found {
		t.Fatalf("真该报的「闭关」被切片挤掉了: %+v", hits)
	}
}

// ⚠️ 实测噪声: 「我这」×9 「我闭」×9 「糖都」×8 全是跨词边界的切片。
func TestCountSelfRepeats_FiltersFuncCharEdges(t *testing.T) {
	msgs := []string{"我这边很忙", "我这边还行", "我这边没事", "我这边挺好"}
	for _, h := range CountSelfRepeats(msgs) {
		first := []rune(h.Token)[0]
		last := []rune(h.Token)[len([]rune(h.Token))-1]
		if strings.ContainsRune(repeatFuncChars, first) || strings.ContainsRune(repeatFuncChars, last) {
			t.Fatalf("跨词切片漏出来了: %q (%+v)", h.Token, h)
		}
	}
}

// ⚠️ 治「风格锚点飘移」: 真人不会三种文化符号随机切换。
func TestCountKana(t *testing.T) {
	if n := CountKana([]string{"えへへ", "そうだね"}); n < 6 {
		t.Fatalf("假名计数不对: %d", n)
	}
	if n := CountKana([]string{"纯中文没有假名"}); n != 0 {
		t.Fatalf("中文不该被算成假名: %d", n)
	}
}

func TestRepeatNote(t *testing.T) {
	if RepeatNote(nil, 0, 3) != "" {
		t.Fatal("没东西可报时必须是空串 —— 空段会占位")
	}
	got := RepeatNote([]RepeatHit{{Token: "枣椰蜜糖", Count: 4}}, 0, 3)
	if !strings.Contains(got, "不许再主动提") {
		t.Fatalf("措辞不够硬: %q", got)
	}
	if !strings.Contains(got, "4 次") {
		t.Fatalf("应当报出次数: %q", got)
	}
	// 目标语超量: 单独一条指令。
	got2 := RepeatNote(nil, 9, 3)
	if !strings.Contains(got2, "只说中文") {
		t.Fatalf("目标语超量没被拦: %q", got2)
	}
	if RepeatNote(nil, 3, 3) != "" {
		t.Fatal("刚好到上限不该报警 —— 那是正常用量")
	}
}

// ⚠️ 检测源**只能是它自己说的话** —— 把用户说的也算进去的话,
// 对方提一次某样东西, 它就再也不敢接了 —— 那正好反了。
func TestRepeatNote_OnlyCountsSelf(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	for i := 0; i < 5; i++ {
		if _, err := e.Store.InsertMessageFull("user", "qq", "text", "枣椰蜜糖真好吃", MsgMeta{SceneKey: DefaultSceneKey}); err != nil {
			t.Fatal(err)
		}
	}
	if got := e.repeatNote(DefaultSceneKey); got != "" {
		t.Fatalf("用户说的被算进来了 —— 它会不敢接对方的话: %q", got)
	}
	for i := 0; i < 4; i++ {
		if _, err := e.Store.InsertMessageFull("ai", "qq", "text", "枣椰蜜糖给你留着呢", MsgMeta{SceneKey: DefaultSceneKey}); err != nil {
			t.Fatal(err)
		}
	}
	if got := e.repeatNote(DefaultSceneKey); got == "" {
		t.Fatal("它自己反复说的没被统计到")
	}
}
