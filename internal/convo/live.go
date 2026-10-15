package convo

import (
	"context"
	"strings"

	"github.com/tzhnahida/VocabLock/internal/log"
)

// ─────────────────────────────────────────────────────────────
// 对话内搜索: 「问它实时热点, 它会自己去找」
//
// 在这之前, 对话路径**完全没有搜索** —— 你问它最近出了什么事, 它要么说不知道,
// 要么**编一个**。后者更糟: 它会把编的说得跟真的一样。
//
// ⚠️ 三条硬纪律:
//   ① **搜不到就不编** —— 而是**反问** (2026-10-14 定)。
//      真人搜不到时的反应是「没搜到诶, 你说的是哪个?」, 不是硬答。
//   ② **只给搜到的内容**, 材料外的一律不许加。
//   ③ **不是每句都搜** —— 只有"被 @ + 带实时性"才搜。
//      每句都搜会把成本打穿, 而且"我看看"会变成口头禅。
// ─────────────────────────────────────────────────────────────

// liveQuestionMarkers 实时性信号。
//
// ⚠️ 这张表要**窄**: 宽了每一句闲聊都会触发搜索,
// 而现象是"它回什么都慢半拍" —— 没人会想到是关键词表太宽。
// ⚠️ 分两档 —— 它们的**可信度差一个数量级**:
//
//	① 显式指令: 对方直接说"你去查查" —— 这是最强的信号, 没有歧义;
//	② 实时性词: 对方问的事**可能**有时效性 —— 有误判空间。
//
// 2026-10-14 生产事故: 对方连说「知道吗」「你说说细节」「**你去查查**」,
// 它全靠人设躲开, 零检索 —— 因为这张表里根本没有"查/搜"这类词。
var liveExplicitMarkers = []string{
	"你查查", "你去查", "查一下", "查查", "搜一下", "搜搜", "帮我查", "帮我搜",
	"上网查", "网上查", "去搜", "百度", "搜索一下",
}

var liveQuestionMarkers = []string{
	"最近有什么", "最近出了", "最近怎么样", "最近咋样",
	"最新", "热点", "新闻", "今天发生", "昨天发生", "这几天",
	"多少钱", "什么价", "涨了", "跌了", "发布了", "上市了",
	"什么时候出", "出了吗", "是不是真的", "属实吗",
}

// LiveQuestion 这句话是不是在问一件**有时效性**的事。
func LiveQuestion(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" {
		return false
	}
	// ① 显式指令 —— 对方要它去查, 没有歧义。
	for _, m := range liveExplicitMarkers {
		if strings.Contains(t, m) {
			return true
		}
	}
	// ② 实时性词。
	for _, m := range liveQuestionMarkers {
		if strings.Contains(t, m) {
			return true
		}
	}
	// ③ 「知道吗」「听说过」—— 问的是一件**它未必亲历**的事。
	//    这类问题它没有素材就会编, 所以宁可多查一次。
	for _, m := range []string{"知道吗", "听说过", "是不是真的", "有没有听说", "真的假的"} {
		if strings.Contains(t, m) {
			return true
		}
	}
	return false
}

// liveQueryPrompt 把口语问题收成一个**能被搜到**的查询词。
//
// ⚠️ 这一步不能省: 直接拿「你最近有没有听说那个事」去搜, 什么都搜不到。
func liveQueryPrompt() string {
	return "把下面这句话收成一个**搜索关键词**, 4-15 字。\n" +
		"严格输出单行 JSON: {\"query\":\"关键词\"}\n" +
		"要求: 只保留能搜到的实体与主题, 去掉语气词、称呼、以及「你知不知道」这类框架。\n\n"
}

// liveResultNote 搜到东西时注入的材料。
func liveResultNote(query, hits string) string {
	return "【你刚去查了一下】\n你搜的是「" + query + "」, 查到这些:\n" + hits + "\n" +
		"⚠️ 硬线: 只许用上面材料里有的内容回答; 不许加材料外的;" +
		"材料里没有的就说不知道。不要把材料里的话说成你自己的经历。"
}

// liveNoResultNote 什么都没搜到时注入的话。
//
// ⚠️ **不是"老实说不知道", 是"反问"** (2026-10-14 定)。
// 真人搜不到的本能反应是问清楚对方指什么 —— 那既诚实, 又比"我不知道"更像人。
func liveNoResultNote(query string) string {
	q := strings.TrimSpace(query)
	if q == "" {
		q = "他问的那件事"
	}
	return "【你刚去查了一下】\n你搜了「" + q + "」, **什么都没搜到**。\n" +
		"⚠️ 硬线: ①**绝对不许编** —— 你不知道就是不知道;" +
		"②**别只说不知道**, 反问他一句: 他指的是哪件事/哪个东西, 问清楚;" +
		"③一句话, 短, 像微信。"
}

// liveLookup 为这一轮做一次实时查询。
//
// 返回 (注入文本, 要不要注入)。第二个返回值为 false 时**一个字都不注入** ——
// 不是"注入空串": 空串会占位, 而占位段会让提示词长度无意义地波动。
func (e *Engine) liveLookup(ctx context.Context, text string) (string, bool) {
	if e.Chatter == nil || !LiveQuestion(text) {
		return "", false
	}
	// ① 收成搜索词。
	out, err := e.Chatter.Chat(ctx, liveQueryPrompt(), text)
	if err != nil {
		log.Warn("convo:live", "实时问题收词失败, 本轮不查: %v", err)
		return "", false
	}
	query, ok := ParseLearnQuery(out)
	if !ok {
		log.Warn("convo:live", "实时问题没收敛出搜索词, 本轮不查")
		return "", false
	}
	// ② 真搜。⚠️ 搜索**挂了**和"没搜到"是两件事, 但对它来说都是"查不到",
	// 所以走同一条反问路径 —— 区别只在日志里。
	hits, serr := e.learnSearcher().SearchWeb(ctx, query, 5)
	if serr != nil {
		log.Warn("convo:live", "实时搜索失败 (%v) —— 走反问", serr)
		return liveNoResultNote(query), true
	}
	if len(hits) == 0 {
		log.Info("convo:live", "「%s」没搜到东西 —— 走反问", query)
		return liveNoResultNote(query), true
	}
	log.Info("convo:live", "实时查询「%s」命中 %d 条", query, len(hits))
	return liveResultNote(query, FormatHits(hits, 5)), true
}
