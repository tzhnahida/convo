package convo

import (
	"strings"
)

// ─────────────────────────────────────────────────────────────
// 事实溯源 (生成侧的硬规则, 2026-10-19)
//
// 审核机两天的打回里, 一半以上是同一件事: **编造自己做过/看过什么**。
// 例: 「我玩过RTS」「体育课没了是真的爽」「你资料卡上写着舞、活跃才半小时」。
// 提示词里早就写着"事实层不许错", 但模型把它当背景音 —— 这类错误**可算**,
// 就不该只靠嘱咐 (设计稿 §8.2b: 能机制化的不许只靠 prompt)。
//
// 判据刻意保守 (两个方向都会误伤, 取"宁可不拦"的一侧):
//   ① **经历类**: 只认那几个明确的动词短语 (我玩过/我去过/…), 且
//      宾语必须**不在**它自己的事实里 (记忆 / 今日行程 / 世界事件);
//      ⚠️ **不算上聊天记录** —— 群友聊到 RTS, 不等于它玩过 RTS (就是这次的事故)。
//   ② **图片细节类**: 这一轮**没有图**却说图里有什么 (资料卡/图上/截图里) —— 必错。
//   ③ 带「好像/不记得/瞎猜/不确定」的句子直接放行 —— 我们要的就是这种写法。
// ─────────────────────────────────────────────────────────────

// claimVerbs "我做过什么"的动词短语。只收明确的经历, 不收「我觉得/我想要」。
var claimVerbs = []string{
	"我玩过", "我打过", "我去过", "我看过", "我见过", "我吃过", "我喝过",
	"我学过", "我练过", "我做过", "我上过", "我逛过", "我试过", "我买过",
	"我读过", "我参加过", "我考过", "我住过",
	// 2026-10-23 P4: 更宽的「自己的过去」说法 —— 审核机打回里最常见的编造就是这类。
	"我高中", "我初中", "我们学校", "我们班", "我当年", "我前阵子", "我前几天",
}

// imageClaimPhrases 声称"从图里读到了什么"。
var imageClaimPhrases = []string{
	"资料卡", "图上", "图片里", "照片里", "截图里", "画面里", "你发的图",
	"上面写着", "上面写的是", "头像里", "背景里",
}

// hedgeWords 带这些词 = 已经是不确定语气, 放行。
var hedgeWords = []string{
	"好像", "大概", "可能", "不记得", "记不清", "没印象", "瞎猜", "不确定",
	"也许", "似乎", "猜的", "随口", "胡说",
}

// hasImageEvidence 这一轮是否有图 (群路径把图的说明塞进 UserText)。
func hasImageEvidence(in DialogInput) bool {
	if strings.TrimSpace(in.ImagePath) != "" {
		return true
	}
	return strings.Contains(in.UserText, "[图") || strings.Contains(in.History, "[图") && strings.Contains(in.UserText, "图")
}

// ownFacts 它**自己的**事实: 记忆 + 今日行程 + 世界事件。
//
// ⚠️ 故意**不含聊天记录**: 对方聊过的事不等于它经历过 (RTS 事故的根因)。
func ownFacts(in DialogInput) string {
	var b strings.Builder
	for _, m := range in.Memories {
		b.WriteString(m.V)
		b.WriteString(" ")
	}
	for _, it := range in.DayPlan.Items {
		b.WriteString(it.What)
		b.WriteString(" ")
	}
	for _, ev := range in.EventNotes {
		b.WriteString(ev.Text)
		b.WriteString(" ")
	}
	return b.String()
}

// clauseAround 取命中位置前后的**一句** (以标点为界), 用来判"有没有带不确定语气"。
func clauseAround(s string, at int) string {
	start, end := at, at
	for start > 0 && !isClauseBreak(rune(s[start-1])) {
		start--
	}
	for end < len(s) && !isClauseBreak(rune(s[end])) {
		end++
	}
	return s[start:end]
}

func isClauseBreak(r rune) bool {
	switch r {
	case '，', '。', '！', '？', '；', '…', ',', '.', '!', '?', ';', '\n', ' ':
		return true
	}
	return false
}

// claimObject 取动词后面的宾语 (最多 6 个字, 遇到标点/虚词就停)。
func claimObject(s string, from int) string {
	r := []rune(s[from:])
	var out []rune
	for _, c := range r {
		if len(out) >= 6 || isClauseBreak(c) || strings.ContainsRune("的了着过吗呢吧啊", c) {
			break
		}
		out = append(out, c)
	}
	return strings.TrimSpace(string(out))
}

// GroundingViolations 检查这条回复有没有"编造的自身经历 / 图里细节"。
//
// 返回人话违规 (进【修正】), 空 = 没算到问题。
func GroundingViolations(text string, in DialogInput) []string {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	var out []string
	haveImage := hasImageEvidence(in)

	// ① 图片细节: 这一轮没有图, 却说图里有什么。
	if !haveImage {
		for _, p := range imageClaimPhrases {
			if i := strings.Index(text, p); i >= 0 {
				if hasHedge(clauseAround(text, i)) {
					continue
				}
				out = append(out, "你在说图里的细节("+p+"), 但这一轮没有图 —— 别编, 改成「没看清」或不说")
				break
			}
		}
	}

	// ② 经历: 动词短语 + 宾语不在自己的事实里。
	facts := ownFacts(in)
	for _, v := range claimVerbs {
		i := strings.Index(text, v)
		if i < 0 {
			continue
		}
		if hasHedge(clauseAround(text, i)) {
			continue
		}
		obj := claimObject(text, i+len(v))
		if len([]rune(obj)) < 2 {
			continue // 「我玩过」后面没宾语 → 不判 (交给审核机)
		}
		if strings.Contains(facts, obj) {
			continue // 有出处
		}
		out = append(out, "你在编自己的经历(「"+v+obj+"」), 记忆和日程里都没有这件事 —— 说「我不记得了」或者别接这句")
	}
	return out
}

func hasHedge(clause string) bool {
	for _, h := range hedgeWords {
		if strings.Contains(clause, h) {
			return true
		}
	}
	return false
}
