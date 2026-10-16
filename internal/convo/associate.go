package convo

import (
	"strings"
	"unicode"

	"github.com/tzhnahida/convo/internal/clock"
)

// ─────────────────────────────────────────────────────────────
// §7.2 联想式检索
//
// 存档式 = 「我需要回忆」→ 按权重取 top-K。（已有）
// 联想式 = 「我现在看到/听到 X」→ 撞上某段记忆的表层线索，于是想起它。
// 设计稿：「联想式才是人味的来源」。
//
// ⚠️ 两种检索的**入口不同**，这是本模块的全部要点：
//   存档式的入口是需求，所以排序键是权重；
//   联想式的入口是**当下的刺激**，所以排序键是线索的具体度。
// 一个"按权重挑一段记忆硬塞进对话"的实现，穿上联想的皮也是存档式 ——
// 而且比存档式更糟，因为它会在完全无关的时候突然提起旧事。
//
// 因此本模块的头号反面用例是：**没有线索就不许联想** (TestAssoc_NoCueNoAssociation)。
// ─────────────────────────────────────────────────────────────

// AssocCue 一条表层线索。Token 必须是**会出现在当下文本里的字面量**。
type AssocCue struct {
	Token string `json:"token"`
	Kind  string `json:"kind"` // entity | place | slang | person
}

// AssocItem 一段可被联想起来的记忆。
type AssocItem struct {
	Key    string
	Text   string
	Cues   []AssocCue
	Weight float64 // ⚠️ 只做**同分裁决**，绝不是入口
}

// assocStopChars 单字功能词表。
//
// ⚠️ 判据是**停用词表**，不是「长度 < 2」。
// 中文里大量实词只有一个字 —— 猫、狗、妈、车、钱。用长度当判据会把它们一并杀掉，
// 而现象只是「它从来想不起你那只猫」，看不出是坏了。这是本模块最容易写错的一行。
var assocStopChars = map[string]bool{
	"我": true, "你": true, "他": true, "她": true, "它": true, "的": true,
	"了": true, "是": true, "在": true, "有": true, "和": true, "就": true,
	"都": true, "也": true, "还": true, "很": true, "不": true, "没": true,
	"好": true, "吧": true, "吗": true, "呢": true, "啊": true, "呀": true,
	"哦": true, "嗯": true, "这": true, "那": true, "个": true, "把": true,
}

// assocCueRune 这个字能不能进线索。
//
// ⚠️ **只要字母与数字** —— 标点与空白不许当线索。
//
// 生产实测(2026-10-15 的决策轨迹)抓到一个真实的反例:
//
//	【联想】刚才提到了「:」，你由此想起：纳西妲: 明天见，记得给我留一颗呀。。
//
// 线索是一个**冒号** —— 而它匹配上的那条记忆里也恰好有个冒号。
// **那不是联想, 是巧合**, 而冒号在对话里到处都是, 于是它每一轮都可能发生。
//
// ⚠️ 这个洞只有对着**生产数据**才看得见: 单字停用词表挡得住「我」「的」,
// 却挡不住一个两字标点对。
func assocCueRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// 而硬扯比不提更伤 —— 它暴露了"她在翻档案"。
func AssociationLine(item AssocItem, cue AssocCue) (string, bool) {
	tok := strings.TrimSpace(cue.Token)
	if tok == "" || strings.TrimSpace(item.Text) == "" {
		return "", false
	}
	return "【联想】刚才提到了「" + tok + "」，你由此想起：" + item.Text +
		"。**只有在话题接得上时才自然带一句**；不搭就别提。", true
}

// BigramCues 从一段文本里抽 2-gram 线索。
//
// ⚠️ 为什么是 2-gram 而不是分词: 这里没有分词器, 而 2-gram 是**零依赖**的,
// 且「宠物店」这种三字词一定会被「宠物」+「物店」两个 gram 同时覆盖 ——
// 共享的子串越长, 命中的 gram 越多, 于是"更具体的线索胜出"是**自然涌现**的,
// 不需要额外写一个长度权重。
func BigramCues(text string, max int) []AssocCue {
	r := []rune(text)
	if len(r) < 2 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]AssocCue, 0, len(r)-1)
	for i := 0; i+1 < len(r); i++ {
		a, b := string(r[i]), string(r[i+1])
		if assocStopChars[a] || assocStopChars[b] {
			continue
		}
		// ⚠️ 标点与空白**不许**当线索 —— 见 assocCueRune 的实测反例。
		if !assocCueRune(r[i]) || !assocCueRune(r[i+1]) {
			continue
		}
		pair := a + b
		if seen[pair] {
			continue
		}
		seen[pair] = true
		out = append(out, AssocCue{Token: pair, Kind: "bigram"})
	}
	if max > 0 && len(out) > max {
		out = out[:max]
	}
	return out
}

// assocNote §7.2 **联想式检索**的接线 (§8.1 段 7 的唯一落点)。
//
// ⚠️ 设计稿把它排得很高:「两者都要, 但**联想式才是人味的来源**」——
// 而它原来**只接在群路径上** (`SpeakInGroupSeeing` 里那 6 行)。
//
// ⚠️ 而设计稿举的例子恰恰是私聊的那个:
//
//	> "刚路过宠物店 想起**你那只猫**"
//
// 所以它现在放在 `dialogReply` 里 —— **群与私聊都过这一处, 只有一份实现**。
//
// ⚠️ §7.3 维一: 按**本场景**取记忆 —— 全局取会把别处的关系记忆带进这个场景。
// ⚠️ 零值: 没有共享线索就**不联想**(`AssociateMemories` 自己保证)。
func (e *Engine) assocNote(sceneKey, text string) string {
	if e.Store == nil || strings.TrimSpace(text) == "" {
		return ""
	}
	mems, merr := e.Store.TopMemoriesFor(sceneKey, 12, clock.Now())
	if merr != nil {
		return ""
	}
	m, cue, ok := AssociateMemories(mems, text, nil)
	if !ok {
		return ""
	}
	line, ok := AssociationLine(AssocItem{Key: m.MemID, Text: m.V}, cue)
	if !ok {
		return ""
	}
	return line
}

// AssociateMemories 从记忆池里做一次联想。
//
// 判据: 记忆文本与当下文本**共享的表层线索** —— 共享得越多, 越可能是同一件事。
// 完全不共享 → false。**这就是「没有线索不许联想」在记忆上的形态。**
//
// ⚠️ 排序是确定的: (共享 gram 数降序, MemID 升序)。少了第二项,
// 同分记忆的先后会随 map 迭代变化 —— 同一句话每轮想起不同的旧事。
func AssociateMemories(mems []Memory, text string, skip map[string]bool) (Memory, AssocCue, bool) {
	nowCues := BigramCues(text, 0)
	if len(nowCues) == 0 {
		return Memory{}, AssocCue{}, false
	}
	set := make(map[string]bool, len(nowCues))
	for _, c := range nowCues {
		set[c.Token] = true
	}
	best := -1
	bestScore := 0
	var bestCue AssocCue
	for i, m := range mems {
		if skip != nil && skip[m.MemID] {
			continue
		}
		cues := BigramCues(m.Label+" "+m.V, 0)
		score := 0
		var lastCue AssocCue
		for _, c := range cues {
			if set[c.Token] {
				score++
				lastCue = c
			}
		}
		if score == 0 {
			continue
		}
		if score > bestScore || (score == bestScore && best >= 0 && m.MemID < mems[best].MemID) {
			best, bestScore, bestCue = i, score, lastCue
		}
	}
	if best < 0 {
		return Memory{}, AssocCue{}, false
	}
	return mems[best], bestCue, true
}
