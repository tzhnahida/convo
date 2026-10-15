package convo

import (
	"strings"
	"testing"
)

// ——— §8.4 风格后处理: 契约测试 ———
//
// 这一批的核心不是"能把话变短", 而是三条铁律**不会被打破**:
//
//	① 只减不改 —— 输出必须逐 rune 是输入的子序列 (语料级不变式);
//	② 不许处理成空串 —— 极端参数下仍非空;
//	③ 零值 = 什么都不做 —— 逐字节等于原文。
//
// 反面用例比正面重要: 每条契约都配了"把实现改坏成 X, 这条会红"的用例。
// 危险输入 (转折句 / 网址 / 问号 / 内容语气词) 全部单独立条 —— 那些才是会出灾难的地方。

// styleTestCorpus 语料: 中文聊天的常见形状 + 专门用来绊倒实现的边角。
func styleTestCorpus() []string {
	return []string{
		"",
		"嗯",
		"好",
		"嗯。",
		"我不想去。",
		"今天天气不错。我们出去走走吧。顺便买点东西。",
		"他说“好的。然后走了”",
		"他说“好的。”然后走了",
		"他说“好的。然后走了",
		"让我想想……算了",
		"我到家了。\n你呢",
		"今天在公司忙了一整天，回来的时候已经十点多了",
		"我今天其实特别想去你那边，但是真的抽不出时间",
		"我明天有空，不能陪你出去了",
		"好呀！那我们去吃火锅吧～",
		"妈妈呢？",
		"真的！！！不想动！！！！",
		"我不想去。你走吧。",
		"？？？",
		"。。。",
		"……",
		"https://example.com/a?b=1 你看这个",
		"3.14 是圆周率。",
		"🎉🎉 太好了！！！",
		"   ",
		"\n\n",
		"啊",
	}
}

// styleTestOpts 参数矩阵。刻意包含极端值与全开组合 —— 不变式必须在"最凶"的参数下也成立。
func styleTestOpts() []StyleOptions {
	return []StyleOptions{
		{},
		{LengthFactor: 1},
		{LengthFactor: 0.6},
		{LengthFactor: 0.3},
		{LengthFactor: 0.01},
		{MaxRunes: 1},
		{MaxRunes: 8},
		{ShortSentences: true},
		{FewPunctuation: true},
		{LengthFactor: 0.5, ShortSentences: true},
		{ShortSentences: true, FewPunctuation: true},
		{LengthFactor: 0.01, MaxRunes: 1, ShortSentences: true, FewPunctuation: true},
	}
}

// styleTestIsSubsequence out 是否逐 rune 是 in 的子序列 (顺序不变, 只许删不许增)。
func styleTestIsSubsequence(in, out string) bool {
	a, b := []rune(in), []rune(out)
	j := 0
	for i := 0; i < len(a) && j < len(b); i++ {
		if a[i] == b[j] {
			j++
		}
	}
	return j == len(b)
}

// styleTestHasContent 有没有非空白内容 (纯空白输入允许原样返回空白)。
func styleTestHasContent(s string) bool {
	for _, r := range s {
		if !styleIsSpace(r) {
			return true
		}
	}
	return false
}

// styleTestEach 把 语料 × 参数 跑一遍。
func styleTestEach(t *testing.T, fn func(t *testing.T, in string, opt StyleOptions, out string)) {
	t.Helper()
	for _, in := range styleTestCorpus() {
		for _, opt := range styleTestOpts() {
			out := ApplyStyle(in, opt)
			fn(t, in, opt, out)
		}
	}
}

// ——— 铁律 ③: 零值 = 什么都不做 ———

// TestStyle_ZeroOptionsIsByteIdentical 正面: 空 StyleOptions 必须**逐字节**等于原文。
//
// 这是本模块最重要的一条 —— 任何调用方忘了填参数, 都不该被静默改掉措辞。
// 反面: 改坏成"LengthFactor 的 0 直接当 0.0 用" → 红。
func TestStyle_ZeroOptionsIsByteIdentical(t *testing.T) {
	for _, in := range styleTestCorpus() {
		if out := ApplyStyle(in, StyleOptions{}); out != in {
			t.Fatalf("零值参数改动了文本:\n in=%q\nout=%q", in, out)
		}
		// LengthFactor=1 也是"不处理", 同样逐字节。
		if out := ApplyStyle(in, StyleOptions{LengthFactor: 1}); out != in {
			t.Fatalf("LengthFactor=1 改动了文本:\n in=%q\nout=%q", in, out)
		}
	}
}

// TestStyle_NormalizeZeroIsUnspecified 正面: 归一化的零值/越界语义。
func TestStyle_NormalizeZeroIsUnspecified(t *testing.T) {
	cases := []struct {
		in   StyleOptions
		want float64
		why  string
	}{
		{StyleOptions{}, 1.0, "0 = 未提供"},
		{StyleOptions{LengthFactor: -1}, 1.0, "负数 = 未提供"},
		{StyleOptions{LengthFactor: 1.5}, 1.0, "只减不增, >1 夹回 1"},
		{StyleOptions{LengthFactor: 0.6}, 0.6, "正常值原样"},
		{StyleOptions{LengthFactor: 0.01}, StyleMinLengthFactor, "低于地板 → 地板"},
	}
	for _, c := range cases {
		if got := NormalizeStyleOptions(c.in).LengthFactor; got != c.want {
			t.Fatalf("%s: LengthFactor %.2f → %.2f, 想要 %.2f", c.why, c.in.LengthFactor, got, c.want)
		}
	}
	if got := NormalizeStyleOptions(StyleOptions{MaxRunes: -3}).MaxRunes; got != 0 {
		t.Fatalf("MaxRunes 负数应折成 0 (未提供), 得到 %d", got)
	}
}

// ——— 铁律 ①: 只减不改 ———

// TestStyle_CorpusInvariants 语料级不变式: 子序列 + 不增长 + 有内容就不许空。
//
// 反面: 改坏成"润色/补标点/换词/调序" → 任意一条都会红。
func TestStyle_CorpusInvariants(t *testing.T) {
	styleTestEach(t, func(t *testing.T, in string, opt StyleOptions, out string) {
		if !styleTestIsSubsequence(in, out) {
			t.Fatalf("输出不是输入的子序列 (改/增/调序了):\n in=%q\nout=%q\nopt=%+v", in, out, opt)
		}
		if len([]rune(out)) > len([]rune(in)) {
			t.Fatalf("输出比输入还长 (%d → %d):\n in=%q\nout=%q\nopt=%+v",
				len([]rune(in)), len([]rune(out)), in, out, opt)
		}
		if styleTestHasContent(in) && !styleTestHasContent(out) {
			t.Fatalf("有内容的输入被处理成了空串:\n in=%q\nopt=%+v", in, opt)
		}
	})
}

// TestStyle_Negative_NoInventedRunes 反面: 输出里不许出现输入里没有的字符。
//
// 改坏成 X: 在结尾自动补句号 / 把省略号补全 / 任何"生成式"后处理 → 红。
func TestStyle_Negative_NoInventedRunes(t *testing.T) {
	styleTestEach(t, func(t *testing.T, in string, opt StyleOptions, out string) {
		have := map[rune]bool{}
		for _, r := range in {
			have[r] = true
		}
		for _, r := range out {
			if !have[r] {
				t.Fatalf("输出出现了输入里没有的字符 %q:\n in=%q\nout=%q\nopt=%+v", r, in, out, opt)
			}
		}
	})
}

// TestStyle_TailTrimIsAPrefix 正面: 只开 LengthFactor 时, 输出是输入的**前缀**
// (尾部收口, 前面一个字都不动)。
//
// 反面: 改坏成"从中间抽句/重排" → 红。
func TestStyle_TailTrimIsAPrefix(t *testing.T) {
	for _, in := range styleTestCorpus() {
		for _, f := range []float64{0.6, 0.3, 0.01} {
			out := ApplyStyle(in, StyleOptions{LengthFactor: f})
			if !strings.HasPrefix(in, out) {
				t.Fatalf("LengthFactor=%.2f 不是前缀截断:\n in=%q\nout=%q", f, in, out)
			}
		}
	}
}

// TestStyle_Negative_NegationSurvives 反面: 否定句不许被截成肯定句。
//
// 这里的输入都是"否定词在第一分句里, 或第二分句以否定/转折开头"的形状 ——
// 后者靠 styleClauseCutOK 的守卫。
// 改坏成 X: 去掉 styleReversalHeads 守卫 (直接截断) → "我今天其实特别想去你那边" (否定没了) → 红。
func TestStyle_Negative_NegationSurvives(t *testing.T) {
	texts := []string{
		"我不想去。你走吧。",
		"我不想聊，真的。",
		"我没去，因为下雨了。",
		"别等我了。今天不出去。",
		"我今天其实特别想去你那边，但是真的抽不出时间",
		"我明天有空，不能陪你出去了",
	}
	neg := []string{"不", "没", "别", "未", "无"}
	for _, in := range texts {
		for _, opt := range styleTestOpts() {
			out := ApplyStyle(in, opt)
			if !styleTestContainsAny(out, neg) {
				t.Fatalf("否定的意思被后处理吃掉了:\n in=%q\nout=%q\nopt=%+v", in, out, opt)
			}
		}
	}
}

// TestStyle_Negative_ContrastClauseNotCut 反面: 转折句不许在转折词前被截断。
//
// "我今天其实特别想去你那边，但是真的抽不出时间" 截成 "我今天其实特别想去你那边"
// 就是**语义反转** —— 本模块最不能出的错。
// 改坏成 X: styleClauseCutOK 里去掉 styleReversalHeads 检查 → 红。
func TestStyle_Negative_ContrastClauseNotCut(t *testing.T) {
	in := "我今天其实特别想去你那边，但是真的抽不出时间"
	for _, opt := range styleTestOpts() {
		out := ApplyStyle(in, opt)
		if !strings.Contains(out, "但是") {
			t.Fatalf("转折分句被截掉了 (语义反转):\n in=%q\nout=%q\nopt=%+v", in, out, opt)
		}
		if out == "我今天其实特别想去你那边" {
			t.Fatalf("正是那条灾难输出: %q", out)
		}
	}
}

// TestStyle_Negative_UrlNotSplit 反面: 网址/半角 token 不许被当成分句界切开。
//
// 改坏成 X: styleClauseRun 里去掉 styleASCIITokenContinuation / styleNeighborDigit
// (真实踩过: "https://example.com/a?b=1 你看这个" 被截成 "https") → 红。
func TestStyle_Negative_UrlNotSplit(t *testing.T) {
	in := "https://example.com/a?b=1 你看这个"
	for _, opt := range styleTestOpts() {
		out := ApplyStyle(in, opt)
		if !strings.Contains(out, "https://example.com/a") {
			t.Fatalf("网址被从中间切开了:\n in=%q\nout=%q\nopt=%+v", in, out, opt)
		}
	}
	if n := len(SplitSentences(in)); n != 1 {
		t.Fatalf("网址里的 ? 被当成了句界 (切出 %d 段): %q", n, SplitSentences(in))
	}
}

// ——— 铁律 ②: 不许处理成空串 ———

// TestStyle_Negative_ExtremeParamsNotEmpty 反面: 极端参数下输出仍非空。
//
// 改坏成 X: 截断后不做"退回原文第一句"的兜底 → 标点串与极短句会变成 "" → 红。
func TestStyle_Negative_ExtremeParamsNotEmpty(t *testing.T) {
	extreme := StyleOptions{LengthFactor: 0.01, MaxRunes: 1, ShortSentences: true, FewPunctuation: true}
	texts := append([]string{"。。。", "……", "？", "。", "啊"}, styleTestCorpus()...)
	for _, in := range texts {
		out := ApplyStyle(in, extreme)
		if styleTestHasContent(in) && !styleTestHasContent(out) {
			t.Fatalf("极端参数下被处理成空串:\n in=%q", in)
		}
		if in != "" && out == "" {
			t.Fatalf("输入非空但输出为空: %q", in)
		}
	}
}

// TestStyle_Negative_NoRuneCountCut 反面: 不许按字数硬切 (会切出半个词)。
//
// 没有标点的一句话就是**不可切的最小单位** —— 宁可超长, 不留半句。
// 改坏成 X: MaxRunes 直接 rs[:MaxRunes] → 输出 "今天天" → 红。
func TestStyle_Negative_NoRuneCountCut(t *testing.T) {
	in := "今天天气真的很不错"
	out := ApplyStyle(in, StyleOptions{MaxRunes: 3})
	if out != in {
		t.Fatalf("按字数硬切了 (切出半个词):\n in=%q\nout=%q", in, out)
	}
	out = ApplyStyle(in, StyleOptions{LengthFactor: 0.2, MaxRunes: 2})
	if out != in {
		t.Fatalf("无标点长句被硬切:\n in=%q\nout=%q", in, out)
	}
}

// TestStyle_LengthFactorDropsTailSentences 正面: 比例 → 丢尾句。
func TestStyle_LengthFactorDropsTailSentences(t *testing.T) {
	in := "我不想去。你走吧。今天不想聊。"
	if out := ApplyStyle(in, StyleOptions{LengthFactor: 0.6}); out != "我不想去。你走吧。" {
		t.Fatalf("0.6 → %q, 想要 %q", out, "我不想去。你走吧。")
	}
	if out := ApplyStyle(in, StyleOptions{LengthFactor: 0.3}); out != "我不想去。" {
		t.Fatalf("0.3 → %q, 想要 %q", out, "我不想去。")
	}
}

// TestStyle_MaxRunesTrimsAtSentenceBoundary 正面: 硬上限也落在句界上。
func TestStyle_MaxRunesTrimsAtSentenceBoundary(t *testing.T) {
	in := "今天天气不错。我们出去走走吧。"
	if out := ApplyStyle(in, StyleOptions{MaxRunes: 8}); out != "今天天气不错。" {
		t.Fatalf("MaxRunes=8 → %q, 想要 %q", out, "今天天气不错。")
	}
	if out := ApplyStyle(in, StyleOptions{MaxRunes: 5}); out != "今天天气不错。" {
		t.Fatalf("MaxRunes 小于第一句时该保留第一句, 得到 %q", out)
	}
}

// TestStyle_LengthFactorCutsSingleSentenceAtClause 正面: 单句长话靠分句界变短
// (整句丢是"少说一句", 分句截才是"句子短")。
func TestStyle_LengthFactorCutsSingleSentenceAtClause(t *testing.T) {
	in := "今天在公司忙了一整天，回来的时候已经十点多了"
	if out := ApplyStyle(in, StyleOptions{LengthFactor: 0.6}); out != "今天在公司忙了一整天" {
		t.Fatalf("得到 %q", out)
	}
	if out := ApplyStyle(in, StyleOptions{ShortSentences: true}); out != "今天在公司忙了一整天" {
		t.Fatalf("ShortSentences 得到 %q", out)
	}
}

// TestStyle_ShortSentencesCapsLongSentence 正面: 句子短 = 单句收到短句档以内。
func TestStyle_ShortSentencesCapsLongSentence(t *testing.T) {
	in := "今天天气真的特别好，我们出去走走吧"
	if n := len([]rune(in)); n <= StyleShortSentenceRunes {
		t.Fatalf("样例本身就不超档 (%d), 测不到东西", n)
	}
	out := ApplyStyle(in, StyleOptions{ShortSentences: true})
	if out != "今天天气真的特别好" {
		t.Fatalf("得到 %q", out)
	}
	if len([]rune(out)) > StyleShortSentenceRunes {
		t.Fatalf("截完还是超档: %q", out)
	}
}

// TestStyle_ShortSentencesDropsToneParticle 正面: 句末纯语气词去掉, 内容词一个不动。
func TestStyle_ShortSentencesDropsToneParticle(t *testing.T) {
	cases := map[string]string{
		"好呀！": "好！",
		"不要啦": "不要",
		"走咯。": "走。",
		"真的呀": "真的",
		"嗯啊":  "嗯",
	}
	for in, want := range cases {
		if out := ApplyStyle(in, StyleOptions{ShortSentences: true}); out != want {
			t.Fatalf("%q → %q, 想要 %q", in, out, want)
		}
	}
}

// TestStyle_Negative_ContentParticleKept 反面: 呢/吧/吗 带着内容, 不许当语气词删掉。
//
// "妈妈呢？" 删成 "妈妈？" 就是把话吃掉了。
// 改坏成 X: 把 呢 加进 styleToneParticles → 红。
func TestStyle_Negative_ContentParticleKept(t *testing.T) {
	for _, in := range []string{"妈妈呢？", "我们走吧", "你吃了吗", "人呢"} {
		if out := ApplyStyle(in, StyleOptions{ShortSentences: true, FewPunctuation: true, LengthFactor: 0.5}); out != in {
			t.Fatalf("内容语气词被吃掉了: %q → %q", in, out)
		}
	}
}

// TestStyle_FewPunctuationCollapsesRuns 正面: 连打标点压成一个。
func TestStyle_FewPunctuationCollapsesRuns(t *testing.T) {
	in := "真的！！！不想动！！！！"
	if out := ApplyStyle(in, StyleOptions{FewPunctuation: true}); out != "真的！不想动！" {
		t.Fatalf("得到 %q", out)
	}
}

// TestStyle_FewPunctuationDropsOnlyFinalPeriod 正面: 只去整条末尾那个句号。
func TestStyle_FewPunctuationDropsOnlyFinalPeriod(t *testing.T) {
	if out := ApplyStyle("我到家了。", StyleOptions{FewPunctuation: true}); out != "我到家了" {
		t.Fatalf("得到 %q", out)
	}
	if out := ApplyStyle("我不想去。你走吧。", StyleOptions{FewPunctuation: true}); out != "我不想去。你走吧" {
		t.Fatalf("得到 %q", out)
	}
}

// TestStyle_Negative_FewPunctuationKeepsQuestionMark 反面: 少标点不许把问号删掉/换成句号。
//
// "你去？" 去掉问号就成了陈述句 —— 那是改意思, 不是少标点。
// 改坏成 X: styleFewPunctuation 里把所有句末标点都去掉 → 红。
func TestStyle_Negative_FewPunctuationKeepsQuestionMark(t *testing.T) {
	for _, in := range []string{"你去不去？", "真的吗？", "你去？"} {
		out := ApplyStyle(in, StyleOptions{FewPunctuation: true, ShortSentences: true, LengthFactor: 0.35})
		if !strings.HasSuffix(out, "？") {
			t.Fatalf("问号被改掉了: %q → %q", in, out)
		}
	}
}

// TestStyle_Negative_MiddlePeriodsSurvive 反面: 中间句号不许删 (删了两句会粘成一句)。
//
// 改坏成 X: styleFewPunctuation 里删掉所有 "。" → "我不想去你走吧" → 红。
func TestStyle_Negative_MiddlePeriodsSurvive(t *testing.T) {
	out := ApplyStyle("我不想去。你走吧。", StyleOptions{FewPunctuation: true})
	if !strings.Contains(out, "。") {
		t.Fatalf("中间句号被删光, 两句粘成一句: %q", out)
	}
	if out == "我不想去你走吧" {
		t.Fatalf("正是那条灾难输出: %q", out)
	}
}

// ——— 中文的三个坑 ———

// TestSplitSentences_PartitionsOriginal 正面: 切句是**分区** —— 拼回去逐字节等于原文。
func TestSplitSentences_PartitionsOriginal(t *testing.T) {
	for _, in := range styleTestCorpus() {
		parts := SplitSentences(in)
		if strings.Join(parts, "") != in {
			t.Fatalf("切句不是分区:\n in=%q\nparts=%q", in, parts)
		}
		for i, p := range parts {
			if p == "" {
				t.Fatalf("%q 切出了空段 (第 %d 段)", in, i)
			}
		}
	}
}

// TestSplitSentences_TailWithoutPunctuation 正面: 结尾没标点的残句要能识别成一句。
func TestSplitSentences_TailWithoutPunctuation(t *testing.T) {
	parts := SplitSentences("我到家了。你呢")
	want := []string{"我到家了。", "你呢"}
	if len(parts) != 2 || parts[0] != want[0] || parts[1] != want[1] {
		t.Fatalf("得到 %q, 想要 %q", parts, want)
	}
	if n := len(SplitSentences("你呢")); n != 1 {
		t.Fatalf("整条没标点也该是一句, 得到 %d 段", n)
	}
}

// TestSplitSentences_QuotedPunctuationNotBoundary 正面: 引号内的标点不算句界。
func TestSplitSentences_QuotedPunctuationNotBoundary(t *testing.T) {
	if parts := SplitSentences("他说“好的。然后走了”"); len(parts) != 1 {
		t.Fatalf("引号内的句号被当成了句界: %q", parts)
	}
	// 引号**外**的句号仍然是句界。
	parts := SplitSentences("他说“好的”。然后走了")
	if len(parts) != 2 || parts[0] != "他说“好的”。" || parts[1] != "然后走了" {
		t.Fatalf("引号外的句号没切: %q", parts)
	}
}

// TestStyle_Negative_StrayQuoteKeepsSplitting 反面: 落单的引号不许把整条消息变成"一句话"。
//
// 模型偶尔多吐一个引号。若实现无脑维护"引号深度", 这条消息就再也切不动,
// 后处理整层静默失效 —— 那是最难发现的一种坏。
// 改坏成 X: 把每个引号字符都记进深度 (不检查配对) → 红。
func TestStyle_Negative_StrayQuoteKeepsSplitting(t *testing.T) {
	in := "他说“好的。然后走了"
	parts := SplitSentences(in)
	if len(parts) != 2 {
		t.Fatalf("落单引号让切句失效了: %q", parts)
	}
	if out := ApplyStyle(in, StyleOptions{LengthFactor: 0.6}); out != "他说“好的。" {
		t.Fatalf("得到 %q", out)
	}
}

// TestStyle_Negative_EllipsisStaysWhole 反面: "……" 是两个字符**一个**标点。
//
// 改坏成 X: 把每个 "…" 当独立句界 → 会切出一个只有 "…" 的段, 丢句时留下半个省略号 → 红。
func TestStyle_Negative_EllipsisStaysWhole(t *testing.T) {
	in := "让我想想……算了"
	parts := SplitSentences(in)
	if len(parts) != 2 || parts[0] != "让我想想……" || parts[1] != "算了" {
		t.Fatalf("省略号切法不对: %q", parts)
	}
	for _, p := range parts {
		if strings.Contains(p, "…") && !strings.Contains(p, "……") {
			t.Fatalf("切出了半个省略号: %q", parts)
		}
	}
	// 丢句路径上也不许留下半个省略号。
	for _, opt := range styleTestOpts() {
		out := ApplyStyle(in, opt)
		if strings.Contains(out, "…") && !strings.Contains(out, "……") {
			t.Fatalf("输出里有半个省略号: %q (opt=%+v)", out, opt)
		}
	}
}

// TestStyle_Negative_DecimalPointIsNotABoundary 反面: 小数点不是句界。
// 改坏成 X: styleTermRun 里对 '.' 不加 styleNeighborDigit / styleASCIIEndOK 判断 → 红。
func TestStyle_Negative_DecimalPointIsNotABoundary(t *testing.T) {
	in := "3.14 是圆周率。"
	if parts := SplitSentences(in); len(parts) != 1 {
		t.Fatalf("小数点被当成了句界: %q", parts)
	}
}

// TestStyle_Negative_ZeroIsNotZeroFactor 反面: 零值 ≠ 极值。
//
// 取一条"如果 0 被当成 0.0 就必然被砍"的输入。
// 改坏成 X: NormalizeStyleOptions 里删掉 LengthFactor<=0 → 1.0 → 红。
func TestStyle_Negative_ZeroIsNotZeroFactor(t *testing.T) {
	in := "今天在公司忙了一整天，回来的时候已经十点多了"
	if out := ApplyStyle(in, StyleOptions{}); out != in {
		t.Fatalf("零值被当成了极值: %q → %q", in, out)
	}
	if out := ApplyStyle(in, StyleOptions{LengthFactor: -1}); out != in {
		t.Fatalf("负数被当成了极值: %q → %q", in, out)
	}
}

// TestStyle_Negative_NeverLengthens 反面: 任何参数组合都不许把话变长。
// 改坏成 X: 加了"补全/润色"分支 → 红。
func TestStyle_Negative_NeverLengthens(t *testing.T) {
	in := "嗯。"
	for _, opt := range styleTestOpts() {
		out := ApplyStyle(in, opt)
		if len([]rune(out)) > len([]rune(in)) {
			t.Fatalf("%+v 把话变长了: %q → %q", opt, in, out)
		}
	}
}

// TestStyle_Negative_NoopIsNotShortCircuitHazard 反面: 短路路径不许把 MaxRunes 一起短路掉。
//
// LengthFactor 是"不做", MaxRunes 是"做" —— 两者共用一个结构体, 很容易在短路条件里写错。
// 改坏成 X: applyStyleNoop 只判断 LengthFactor (忽略 MaxRunes) → 红。
func TestStyle_Negative_MaxRunesSurvivesNoopLengthFactor(t *testing.T) {
	in := "今天天气不错。我们出去走走吧。"
	out := ApplyStyle(in, StyleOptions{LengthFactor: 1, MaxRunes: 8})
	if out != "今天天气不错。" {
		t.Fatalf("MaxRunes 被 LengthFactor 的短路吃掉了: %q", out)
	}
}

// containsAny 供本文件使用的小工具 (与包内既有同名函数不同, 加了 style 前缀避免冲突)。
func styleTestContainsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
