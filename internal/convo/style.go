package convo

import (
	"math"
	"strings"
)

// ——— §8.4 风格后处理 ———
//
// 设计稿 §8.4 原文: "分条节奏 / 标点习惯 / 错字注入 / 打字时长 / 撤回 —— 全部在后处理层,
// 零模型成本, 且全部可调频率、可关闭"。本文件只做其中**动生成文本本身**的那一小块:
// 消息长度 / 句子短 / 标点少。分条节奏、错字注入、撤回不在本文件 (§8.3 那张表里各有归属)。
//
// 为什么这一层非有不可 (§4.2 实测): 设计稿要求 "消息长度 ×0.6", 但**只靠提示词做不到** ——
// 仿真里病中 16.8 字 vs 平时 15.9 字, 反而更长。长度是模型生成的, 系统侧唯一的抓手就是这里。
// 与 §8.3 的 "延迟长 / 少表情" 同一条理由: **凡是能被系统算出来的形式习惯, 都不该交给模型自觉**。
//
// ⚠️ 三条铁律 —— 本文件每个分支都为它们服务, 并由 style_test 逐条钉住:
//
//	① **只减不改**。允许: 丢句 / 去句末语气词 / 把 "！！！" 压成 "！" / 超长截到安全界。
//	   禁止: 改写、替换词、调序、插入、把标点换成另一种意思。
//	   一个后处理把 "我不想聊" 改成 "我想聊" 就是灾难, 所以**输出必须逐 rune 是输入的子序列**
//	   (语料级不变式测试)。
//	② **不许处理成空串**。截断到空就退回原文第一句 —— "少说" 可以, "说不出话" 不行。
//	③ **零值 = 什么都不做**。ApplyStyle(text, StyleOptions{}) 逐字节等于 text。
//	   任何调用方忘了填参数, 都不该被这个模块静默改掉措辞。
//	   与 MoodWillingness / MoodPacingFactor / BodyEffect 的零值约定同源: **失败往"正常说话"倒**。
//
// 本模块是**纯函数**: 无 IO、无随机、无时钟、无持久化 —— 所以没有 EnsureStyleSchema,
// 也没有任何派生量落库 (设计纪律: 派生量不落库, 现算)。
//
// 中文的三个坑 (各有对应测试):
//   - 没有空格分词, **不能按字数硬切** —— 会切出半个词。所有截断只落在标点/句界上。
//   - "……" 、"——" 是**两个字符一个标点**, 必须整体消费, 否则会留下半个省略号。
//   - 引号内的标点不是句界 ("他说"好的。然后走了"" 只有一句话)。

// StyleOptions 风格旋钮。**零值 = 全关 = 原样返回**。
type StyleOptions struct {
	// LengthFactor 目标总长比例 ∈ (0,1]。**0 / 负数 / NaN = 未提供 → 1.0** (不做长度处理)。
	// 只减不增: 传 >1 会被夹到 1.0 —— 这一层没有"把话变长"的能力, 假装有只会掩盖调用方的错。
	LengthFactor float64

	// ShortSentences "句子短": 单句超过 StyleShortSentenceRunes 就收到最近的安全界,
	// 并去掉句末的**纯语气词** ("好呀" → "好")。
	ShortSentences bool

	// FewPunctuation "标点少": 同种标点连打压成一个 ("！！！" → "！"),
	// 并去掉**整条消息末尾**那个句号。不动问号/感叹号/中间句号, 理由见 styleFewPunctuation。
	FewPunctuation bool

	// MaxRunes 整条消息的 rune 上限。**<=0 = 未提供 = 不限制**。
	// 它是"软上限": 找不到安全界时宁可超长也不硬切 (见 styleCutLen 的三级退让)。
	MaxRunes int
}

// StyleShortSentenceRunes 短句档的上限 —— 取设计稿 §2.2 的 "默认 1~15 字像打字"。
//
// 之所以要一个**常量档位**而不是比例: 状态类量对行为的调整要有明确档位表,
// 不做连续漂移。病中/低唤醒只决定"用不用这一档", 不决定"这一档是多少"。
const StyleShortSentenceRunes = 15

// StyleMinLengthFactor 长度比例的地板。
//
// 低于它的值一律按它执行: 后处理不该能把一条消息砍到"只剩一句"以下,
// 那已经从"说得少"变成"说不出话"了 (§8.3 三条边界同源: 保留率有下限)。
const StyleMinLengthFactor = 0.25

// NormalizeStyleOptions 把零值折成明确的默认值, 并夹住越界参数。
//
// ⚠️ **零值 = 未提供 ≠ 极值**: LengthFactor 的 0 读作 "没填" 而不是 "砍到最短"。
// 少了这一步, 任何忘了填这个字段的调用方都会静默变成 "说话只剩一句" ——
// 与 MoodWillingness(0) 会变成 "不理人" 是同一个坑, 只是方向相反。
func NormalizeStyleOptions(opt StyleOptions) StyleOptions {
	if opt.LengthFactor <= 0 || math.IsNaN(opt.LengthFactor) {
		opt.LengthFactor = 1.0
	}
	if opt.LengthFactor > 1 {
		opt.LengthFactor = 1.0
	}
	if opt.LengthFactor < StyleMinLengthFactor {
		opt.LengthFactor = StyleMinLengthFactor
	}
	if opt.MaxRunes < 0 {
		opt.MaxRunes = 0
	}
	return opt
}

// applyStyleNoop 归一化之后与它相等 = 四个旋钮全关 = 短路。
func applyStyleNoop(opt StyleOptions) bool {
	return opt == (StyleOptions{LengthFactor: 1.0})
}

// ApplyStyle §8.4 风格后处理主入口。**纯函数**: 无 IO、无随机、无时钟。
//
// 顺序 (每一步都只删 rune, 所以任一步都不会引入原文没有的字):
//
//	① 长度   LengthFactor 与 MaxRunes 取**更紧**的那个预算, 从尾部按安全界收;
//	② 句长   ShortSentences 逐句压到短句档 + 去句末语气词;
//	③ 标点   FewPunctuation 压重复标点 + 去整条末尾的句号;
//	④ 兜底   任何路径交回空串 → 退回原文第一句 (**宁可正常说话**)。
func ApplyStyle(text string, opt StyleOptions) string {
	opt = NormalizeStyleOptions(opt)
	if text == "" {
		return text
	}
	// 零值短路: 逐字节原样返回, 连 rune 化都不做。
	if applyStyleNoop(opt) {
		return text
	}

	rs := []rune(text)

	// ① 长度。两个旋钮都是"上限", 所以取更紧的那个 —— 先后应用的结果与取 min 等价,
	//    但只切一次, 少一次"在哪断"的争议。
	budget := -1
	if opt.LengthFactor < 1 {
		budget = int(math.Ceil(float64(len(rs)) * opt.LengthFactor))
	}
	if opt.MaxRunes > 0 && (budget < 0 || opt.MaxRunes < budget) {
		budget = opt.MaxRunes
	}
	if budget >= 0 && budget < len(rs) {
		rs = styleTrimTailPunct(rs[:styleCutLen(rs, budget)])
	}

	// ② 句长。
	if opt.ShortSentences {
		rs = styleShortSentences(rs)
	}

	out := string(rs)

	// ③ 标点。
	if opt.FewPunctuation {
		out = styleFewPunctuation(out)
	}

	// ④ 兜底。注意退回的是**原文**的第一句 (不带风格) —— 兜底时宁可恢复完整措辞,
	//    也不要在残缺的文本上再套一层风格。
	if out == "" {
		return styleFirstSentence(text)
	}
	return out
}

// SplitSentences 按句界切句, 返回一个**分区**: 各段顺序拼接逐字节等于原文本。
//
// 分区 (而不是"干净句子"列表) 是刻意的: 上层要能靠"删掉几段再拼回去"来实现丢句,
// 而拼接永不会引入原文没有的字符 —— 铁律 ① 因此是构造上成立的, 不靠自觉。
//
// 句界判据:
//   - 。！？…!? 及其**连续段**算**一个**句界 ("！！！" 不是三个, "……" 不是两个);
//   - 换行算句界 (聊天里的"分条"); 句界后的空白跟着前一句走;
//   - **配对成功**的引号/括号内部的标点不算句界;
//   - 结尾没有标点的残句**也是一句** (中文聊天里极常见)。
func SplitSentences(text string) []string {
	rs := []rune(text)
	if len(rs) == 0 {
		return nil
	}
	spans := styleSpans(rs)
	out := make([]string, 0, len(spans))
	for _, sp := range spans {
		out = append(out, string(rs[sp.lo:sp.hi]))
	}
	return out
}

// ——— 切句 ———

// styleSpan 半开区间 [lo,hi), 单位是 rune 下标。
type styleSpan struct{ lo, hi int }

// styleSpans 句界分区。SplitSentences 与逐句处理共用它, 保证两边切法永远一致。
func styleSpans(rs []rune) []styleSpan {
	inside := styleQuoteInside(rs)
	out := make([]styleSpan, 0, 4)
	start := 0
	i := 0
	for i < len(rs) {
		if inside[i] {
			i++
			continue
		}
		if n := styleTermRun(rs, i); n > 0 {
			j := i + n
			for j < len(rs) && !inside[j] && styleIsSpace(rs[j]) {
				j++
			}
			out = append(out, styleSpan{start, j})
			start, i = j, j
			continue
		}
		if rs[i] == '\n' {
			j := i
			for j < len(rs) && styleIsSpace(rs[j]) {
				j++
			}
			out = append(out, styleSpan{start, j})
			start, i = j, j
			continue
		}
		i++
	}
	if start < len(rs) {
		out = append(out, styleSpan{start, len(rs)})
	}
	return out
}

// styleTermRun 从 i 起吃掉一段句末标点, 返回消耗的 rune 数 (0 = i 不是句界)。
//
// 必须整体消费: 把 "……" 当两个句界会留下一个只有 "…" 的段, 丢句时就会丢出半个省略号。
func styleTermRun(rs []rune, i int) int {
	j := i
	for j < len(rs) {
		switch rs[j] {
		case '。', '！', '？', '…':
			j++
			continue
		case '!', '?':
			if !styleASCIIEndOK(rs, j) {
				return j - i
			}
			j++
			continue
		case '.':
			if !styleASCIIEndOK(rs, j) || styleNeighborDigit(rs, j) {
				return j - i
			}
			j++
			continue
		}
		return j - i
	}
	return j - i
}

// styleASCIIEndOK 半角 ! ? . 只有在"下一个字符不像网址/小数的一部分"时才算句界。
// 没有这条, "https://a.com?x=1" 会被从问号处劈开。
func styleASCIIEndOK(rs []rune, i int) bool {
	if i+1 >= len(rs) {
		return true
	}
	n := rs[i+1]
	if n >= '0' && n <= '9' {
		return false
	}
	if (n >= 'a' && n <= 'z') || (n >= 'A' && n <= 'Z') {
		return false
	}
	switch n {
	case '/', '.', '=', '&', '%', '_', '-', '#', '~':
		return false
	}
	return true
}

// styleClauseRun 从 i 起吃掉一段**分句标点**, 返回长度 (0 = 不是分句界)。
// 分句界不是句界, 只在 styleBreaks 里作为"截断候选"使用。
func styleClauseRun(rs []rune, i int) int {
	j := i
	for j < len(rs) {
		switch rs[j] {
		case '，', '、', '；', '：', '—', '－':
			// 全角: 安全。
		case ',', ';', ':':
			// 半角: 两侧没有空格的半角标点基本都在**一个 token 内部** ——
			// 数字 (1,000 / 3:14)、网址 (https://…)、英文 (hello,world) 都不是分句界。
			// 没有这条, "https://example.com/a?b=1 你看这个" 会被从冒号处截成 "https"。
			if styleNeighborDigit(rs, j) || styleASCIITokenContinuation(rs, j) {
				return j - i
			}
		default:
			return j - i
		}
		j++
	}
	return j - i
}

// styleASCIITokenContinuation i 后面那个字符是否"还跟它同属一个 token"。
// 半角标点后面紧跟字母/数字/网址符号时, 它是 token 的一部分 (https:// / 1,000 / hello,world),
// 不是分句界。
func styleASCIITokenContinuation(rs []rune, i int) bool {
	if i+1 >= len(rs) {
		return false
	}
	n := rs[i+1]
	if (n >= 'a' && n <= 'z') || (n >= 'A' && n <= 'Z') || (n >= '0' && n <= '9') {
		return true
	}
	switch n {
	case '/', '\\', ':', '.', '-', '_', '#', '?', '&', '=', '%', '@', '~', '+':
		return true
	}
	return false
}

// styleNeighborDigit i 的左右是否紧挨着数字。
func styleNeighborDigit(rs []rune, i int) bool {
	isDigit := func(r rune) bool { return r >= '0' && r <= '9' }
	if i > 0 && isDigit(rs[i-1]) {
		return true
	}
	return i+1 < len(rs) && isDigit(rs[i+1])
}

// styleIsSpace 空白 (含全角空格)。
func styleIsSpace(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r', '\v', '\f', '\u00a0', '\u3000':
		return true
	}
	return false
}

// styleIsTermRune 单个句末标点字符 (含省略号)。回看时用, 不做半角语境判断。
func styleIsTermRune(r rune) bool {
	switch r {
	case '。', '！', '？', '…', '!', '?', '.':
		return true
	}
	return false
}

// ——— 引号: 配对成功的才算引号 ———

// styleQuotePairs 引号/括号的**开启 → 关闭**表。
//
// ⚠️ 半角单引号 ' 故意不收: 英文 "don't" 里的那个撇号会跟后面某个 ' 配成一对,
// 把中间真正的句界全吞掉。文本里出现的落单引号同理 —— **配对失败的不算引号**,
// 宁可多切一句, 也不要因为模型多吐了一个引号就让整个后处理失效。
var styleQuotePairs = [][2]rune{
	{'“', '”'}, {'‘', '’'}, {'「', '」'}, {'『', '』'},
	{'《', '》'}, {'〈', '〉'}, {'（', '）'}, {'【', '】'},
	{'(', ')'}, {'[', ']'}, {'{', '}'}, {'"', '"'},
}

func styleQuoteCloseOf(open rune) (rune, bool) {
	for _, p := range styleQuotePairs {
		if p[0] == open {
			return p[1], true
		}
	}
	return 0, false
}

// styleQuoteInside 标记每个 rune 是否落在**配对成功**的引号内部。
//
// 只看配对成功的对: 落单的开启引号与落单的关闭引号都当普通字符。
func styleQuoteInside(rs []rune) []bool {
	type frame struct {
		idx  int
		open rune
	}
	var stack []frame
	var pairs [][2]int
	for i, r := range rs {
		if n := len(stack); n > 0 {
			if want, ok := styleQuoteCloseOf(stack[n-1].open); ok && r == want {
				pairs = append(pairs, [2]int{stack[n-1].idx, i})
				stack = stack[:n-1]
				continue
			}
		}
		if _, ok := styleQuoteCloseOf(r); ok {
			stack = append(stack, frame{i, r})
			continue
		}
		// 落单的关闭符 → 普通字符。
	}
	// 深度差分: 每个配对覆盖 (open, close) 开区间; 嵌套自然相加。
	depth := make([]int, len(rs)+1)
	for _, p := range pairs {
		depth[p[0]+1]++
		depth[p[1]]--
	}
	inside := make([]bool, len(rs))
	cur := 0
	for i := range rs {
		cur += depth[i]
		inside[i] = cur > 0
	}
	return inside
}

// ——— 截断: 只落在安全界上 ———

// styleReversalHeads 截断守卫①: 被**丢掉**的那半句若以这些词开头, 就不许在这里截。
//
// "我想去，但是真的没时间" 截成 "我想去" 就是语义反转 —— 本模块最不能出的错。
// 转折词与否定词都要挡: 前半句成立、后半句推翻, 是中文里最常见的句式之一,
// 而截断恰好会把"推翻"那半句扔掉。**宁可少截, 不可截反。**
var styleReversalHeads = []string{
	"但是", "但", "不过", "可是", "却", "而是", "而", "然而", "只是",
	"反而", "否则", "不然", "要不然", "不", "没", "别", "未", "无", "非", "莫",
	"难道", "何必",
}

// styleDanglingTails 截断守卫②: **留下**的那半句若以这些字结尾, 读起来是半句话。
// 全是虚词/助动词 —— 它们单独收尾不成立, 说明截断点选在了词与词之间。
var styleDanglingTails = "的地得和跟与及或在是就都很太也还又再才只为对从给向往于比把被因所如若虽则且并而要想要会能觉"

// styleClauseCutOK 判断"在分句标点处截断"安不安全。
//
// ⚠️ 这是本文件最需要解释的一处。整句丢只是"少说一句", 真正能让**单句**变短的手段
// 只有分句截断 —— 而它最大的风险就是语义反转 (见 styleReversalHeads)。两条守卫:
//
//	① 丢掉的部分不许以转折/否定词开头;
//	② 留下的部分不许以悬空虚词结尾。
//
// 两条都不满足就**不截**: 一句长话 > 一句变味的话 (§8.3 三条边界的同一取向)。
func styleClauseCutOK(rs []rune, i, j int) bool {
	head := rs[:i]
	for len(head) > 0 && styleIsSpace(head[len(head)-1]) {
		head = head[:len(head)-1]
	}
	if len(head) == 0 || strings.ContainsRune(styleDanglingTails, head[len(head)-1]) {
		return false
	}
	tail := rs[j:]
	for len(tail) > 0 && styleIsSpace(tail[0]) {
		tail = tail[1:]
	}
	if len(tail) == 0 {
		return false
	}
	s := string(tail)
	for _, p := range styleReversalHeads {
		if strings.HasPrefix(s, p) {
			return false
		}
	}
	return true
}

// styleBreaks 所有**安全的截断位置** (rune 下标, 升序, 不含 0 与 len)。
//
// 两类候选: 句界 (永远安全) 与通过守卫的分句界。
func styleBreaks(rs []rune) []int {
	inside := styleQuoteInside(rs)
	var out []int
	i := 0
	for i < len(rs) {
		if inside[i] {
			i++
			continue
		}
		if n := styleTermRun(rs, i); n > 0 {
			j := i + n
			if j < len(rs) {
				out = append(out, j)
			}
			i = j
			continue
		}
		if rs[i] == '\n' {
			if i+1 < len(rs) {
				out = append(out, i+1)
			}
			i++
			continue
		}
		if n := styleClauseRun(rs, i); n > 0 {
			j := i + n
			if j < len(rs) && styleClauseCutOK(rs, i, j) {
				out = append(out, j)
			}
			i = j
			continue
		}
		i++
	}
	return out
}

// styleFirstSentenceEnd 第一句的结束位置 (整条只有一句时就是整条的末尾)。
func styleFirstSentenceEnd(rs []rune) int {
	spans := styleSpans(rs)
	if len(spans) == 0 {
		return len(rs)
	}
	return spans[0].hi
}

// styleCutLen 挑一个"安全截断长度": 不超过 budget, 但绝不切在半个词/半句话上。
//
// 三级退让, 每一级都是往"正常说话"倒:
//
//	① 预算内有安全界 → 取**最靠后**的那个 (尽量把话说完);
//	② 界都落在预算之外 → 退到**第一句** (宁可超预算, 不留半句);
//	③ 连句界都没有 (整条就是一句不带标点的话) → 原样返回, **绝不按字数硬切**。
func styleCutLen(rs []rune, budget int) int {
	if budget >= len(rs) {
		return len(rs)
	}
	best := 0
	for _, p := range styleBreaks(rs) {
		if p > budget {
			break
		}
		best = p
	}
	if best > 0 {
		return best
	}
	if floor := styleFirstSentenceEnd(rs); floor > 0 {
		return floor
	}
	return len(rs)
}

// styleTrimTailPunct 去掉尾部残留的空白与分句标点。
// 截断点落在分句界上时, 留下一个孤零零的 "，" 看起来像话被打断了 —— 它是断点本身, 不是内容。
// 句末标点 (。！？…) **不动**: 那是句子的正常收尾。
func styleTrimTailPunct(rs []rune) []rune {
	end := len(rs)
	for end > 0 && styleIsSpace(rs[end-1]) {
		end--
	}
	for end > 0 && styleIsClausePunct(rs[end-1]) {
		end--
	}
	for end > 0 && styleIsSpace(rs[end-1]) {
		end--
	}
	return rs[:end]
}

// styleIsClausePunct 单个分句标点字符 (与 styleClauseRun 的表保持一致)。
func styleIsClausePunct(r rune) bool {
	switch r {
	case '，', '、', '；', '：', '—', '－', ',', ';', ':':
		return true
	}
	return false
}

// styleFirstSentence 原文的第一句 —— 兜底专用。
//
// 它保证两件事: 输出非空, 且仍是原文的前缀 (铁律 ① 在兜底路径上同样成立)。
func styleFirstSentence(text string) string {
	for _, c := range SplitSentences(text) {
		if c != "" {
			return c
		}
	}
	return text
}

// ——— 句子短 ———

// styleShortSentences 逐句压到短句档, 并去掉句末语气词。
//
// 逐段处理后按原顺序拼接: 每段都是自身的子序列, 拼接自然也是全文的子序列 (铁律 ①)。
func styleShortSentences(rs []rune) []rune {
	spans := styleSpans(rs)
	out := make([]rune, 0, len(rs))
	for _, sp := range spans {
		seg := rs[sp.lo:sp.hi]
		if len(seg) > StyleShortSentenceRunes {
			seg = styleTrimTailPunct(seg[:styleCutLen(seg, StyleShortSentenceRunes)])
		}
		out = append(out, styleDropToneParticle(seg)...)
	}
	return out
}

// styleToneParticles 可以安全去掉的**纯语气尾词**。
//
// ⚠️ 呢 / 吧 / 吗 **故意不在表里**, 它们带着内容:
//
//	"人呢？"   去掉 "呢" 只剩 "人？"  —— 那是把话吃掉, 不是把句子变短;
//	"我们走吧" 去掉 "吧" 从"商量"变成"命令"。
//
// 表内这几个去掉只减语气、不减事实 ("好呀" → "好", "不要啦" → "不要")。
var styleToneParticles = "啊呀哇啦嘞哦噢嘛呐哟呦咯"

// styleDropToneParticle 去掉段末的一个纯语气词 (标点与尾部空白原样保留)。
//
// 只在"去掉之后还剩内容"时才动: 整段就是 "呀！" 的话, 去掉就成了空消息。
func styleDropToneParticle(s []rune) []rune {
	end := len(s)
	for end > 0 && styleIsSpace(s[end-1]) {
		end--
	}
	tailWS := end
	for end > 0 && styleIsTermRune(s[end-1]) {
		end--
	}
	if end < 2 || !strings.ContainsRune(styleToneParticles, s[end-1]) {
		return s
	}
	out := make([]rune, 0, len(s)-1)
	out = append(out, s[:end-1]...)
	out = append(out, s[end:tailWS]...)
	out = append(out, s[tailWS:]...)
	return out
}

// ——— 标点少 ———

// styleCollapsible 连打时压成一个的标点。
//
// ⚠️ "…" 不在表里: "……" 是中文省略号的规范写法 (两个字符 = 一个标点),
// 压成 "…" 是**换了一个标点**, 属于"改"不属于"减"。
func styleCollapsible(r rune) bool {
	switch r {
	case '！', '？', '。', '!', '?', '~', '～':
		return true
	}
	return false
}

// styleFewPunctuation 少标点。只做两件**不会改变句式**的事:
//
//	① 同种标点连打压成一个 ("！！！" → "！", "。。。" → "。");
//	② 去掉**整条消息末尾**那一个句号 (网聊里本来就不打句号)。
//
// ⚠️ 三条刻意的"不做":
//   - 不问号/感叹号: "你去？" 去掉问号就成了陈述句 —— 那是改意思, 不是少标点;
//   - 不动**中间**的句号: "我不想去。你走吧。" 去掉中间那个会把两句粘成
//     "我不想去你走吧", 语义糊在一起 —— 同样是"改";
//   - 不动 "……": 见 styleCollapsible。
func styleFewPunctuation(s string) string {
	rs := []rune(s)
	out := make([]rune, 0, len(rs))
	for i := 0; i < len(rs); {
		r := rs[i]
		out = append(out, r)
		i++
		if styleCollapsible(r) {
			for i < len(rs) && rs[i] == r {
				i++
			}
		}
	}
	end := len(out)
	for end > 0 && styleIsSpace(out[end-1]) {
		end--
	}
	if end > 0 && out[end-1] == '。' {
		out = append(out[:end-1], out[end:]...)
	}
	return string(out)
}
