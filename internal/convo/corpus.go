package convo

import (
	"fmt"
	"sort"
	"strings"
)

// ─────────────────────────────────────────────────────────────
// 群语料: 拿**真实的人**怎么说话, 教它怎么说
//
// ⚠️ **最大的坑: 群里大半消息不是人话, 是指令。**
// 实测语料: `st 棍母` `st otto` `music 人生啊能不能放过我这一次` —— 那是**别的 bot**
// 的命令格式。拿它们统计, 学出来的是"说 st 开头的词", 而且句长基线被拉高。
// 所以进语料之前必须过一道**人话闸**。
//
// ⚠️ 第二件事: 原来的 RegisterDriftLine **只在漂移时**注入 ——
// 也就是说平时它根本不知道这个群怎么说话。漂移检测是"变了没有", 不是"该怎么说"。
// ─────────────────────────────────────────────────────────────

// botCommandHeads 别的 bot 的指令头。
//
// ⚠️ 这张表只收**明确是命令**的 —— 收宽了会把正常说话剔掉,
// 而"语料变少"是看不出来的 (统计数小一点而已)。
var botCommandHeads = []string{
	"st ", "st:", "music ", "music:", "help", "点歌", "来点",
	"#", "/", "！", "!!", ">>",
}

// IsNaturalUtterance 这一条是**人话**吗 (能进语料)。
func IsNaturalUtterance(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" {
		return false
	}
	r := []rune(t)
	// ⚠️ **一个字也是人话**: 「唉」「草」「就」在群里比比皆是 ——
	// 而它们正是最能代表"这个群什么调子"的东西。原来卡 len>=2 把「唉」剔了。
	if len(r) == 0 || len(r) > 40 {
		return false // 40 字以上是转发/粘贴, 不是说话
	}
	low := strings.ToLower(t)
	for _, h := range botCommandHeads {
		if strings.HasPrefix(low, h) {
			return false
		}
	}
	// ⚠️ 判据是**必须含汉字或字母**:
	//   「？」「！！」纯符号 → 剔;  「1」「12」纯数字 → 剔 (那是投票/接龙, 不是说话);
	//   「唉」「草」汉字 → 留;  「nb」「ok」字母 → 留。
	var hasHan, hasLetter bool
	for _, c := range r {
		switch {
		case c >= 0x4E00 && c <= 0x9FFF:
			hasHan = true
		case (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
			hasLetter = true
		}
	}
	return hasHan || hasLetter
}

// GroupVoice 这个群怎么说话。
//
// ⚠️ **中位数比均值更该用**: 实测均值 7.7 而被少数长消息拉高, 中位只有 5。
// 拿均值当尺子会让它写得比群里长一半。
type GroupVoice struct {
	N         int
	AvgRunes  float64
	Median    float64 // 中位句长
	ShortRate float64 // ≤5 字占比 (实测 58%)
	P90       float64 // 90 分位
	Samples   []string
}

// GroupVoiceFrom 从一堆消息里统计 (内部已过**人话闸**)。
func GroupVoiceFrom(msgs []string) GroupVoice {
	var v GroupVoice
	var lens []int
	var short []string
	for _, m := range msgs {
		if !IsNaturalUtterance(m) {
			continue
		}
		t := strings.TrimSpace(m)
		n := len([]rune(t))
		lens = append(lens, n)
		v.N++
		if n <= 4 {
			short = append(short, t)
		}
	}
	if v.N == 0 {
		return v
	}
	// ⚠️ **零值 = 未提供 ≠ 0 字** —— 没有样本时全套保持零值, 由调用方判断。
	var total int
	for _, n := range lens {
		total += n
	}
	v.AvgRunes = float64(total) / float64(v.N)
	sort.Ints(lens)
	v.Median = float64(lens[len(lens)/2])
	v.P90 = float64(lens[int(float64(len(lens)-1)*0.9)])
	cnt := 0
	for _, n := range lens {
		if n <= 5 {
			cnt++
		}
	}
	v.ShortRate = float64(cnt) / float64(v.N)
	if len(short) > 6 {
		short = short[:6]
	}
	v.Samples = short
	return v
}

// GroupVoiceLine 渲染成注入文本。第二个返回 false = **样本不足, 一个字都不注入**。
//
// ⚠️ 样本不足时不许注入 —— 3 条消息算出来的"平均 6 个字"会让它以为自己在一个
// 只说短句的群里, 于是把自己憋成复读机。**门槛是硬约束, 不是建议。**
func GroupVoiceLine(v GroupVoice) (string, bool) {
	if v.N < GroupVoiceMinSamples {
		return "", false
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("\n\n【这个群怎么说话】(从 %d 条真实发言里看的)\n", v.N))
	// ⚠️ 报**中位数**而不是均值 —— 均值被少数长消息拉高 (实测 7.7 vs 5)。
	b.WriteString(fmt.Sprintf("- 中位一句 **%.0f 字**;%.0f%% 的发言在 5 字以内;九成在 %.0f 字以内。\n",
		v.Median, v.ShortRate*100, v.P90))
	if v.ShortRate >= 0.3 {
		b.WriteString("- ⚠️ **单字/两三字回复在这个群是常态** (「唉」「行」「能」) —— 你也该这么短。\n")
	}
	if len(v.Samples) > 0 {
		b.WriteString("- 他们平时就这么说: ")
		for i, s := range v.Samples {
			if i > 0 {
				b.WriteString(" / ")
			}
			b.WriteString("「" + s + "」")
		}
		b.WriteString("\n")
	}
	b.WriteString("- **别写得比他们卷** —— 群聊不是写文章。")
	return b.String(), true
}

// voiceNoteOrEmpty 渲染; 样本不足时返回空串。
//
// ⚠️ 空串和"有内容"必须是两种可区分的结果 —— 调用方往 DialogInput 里塞空串是安全的
// (buildSystem 判空), 但**别在这里返回一句"样本不足"**: 那会进提示词, 而且是个噪声段。
func voiceNoteOrEmpty(v GroupVoice) string {
	line, ok := GroupVoiceLine(v)
	if !ok {
		return ""
	}
	return line
}

// GroupVoiceMinSamples 语料门槛。
//
// ⚠️ 12 条以下不谈语域: 少于这个数, 一条长消息就能把均值拉歪。
const GroupVoiceMinSamples = 12

// groupVoice 取这个场景最近的真人发言并统计。
func (e *Engine) groupVoice(sceneKey string, n int) GroupVoice {
	if e.Store == nil || sceneKey == "" {
		return GroupVoice{}
	}
	if n <= 0 {
		n = 200
	}
	rows, err := e.Store.DB.Query("SELECT payload_json FROM convo_messages "+
		"WHERE scene_key = ? AND dir = 'user' AND kind = 'text' ORDER BY id DESC LIMIT ?", sceneKey, n)
	if err != nil {
		return GroupVoice{}
	}
	defer rows.Close()
	var msgs []string
	for rows.Next() {
		var raw string
		if rows.Scan(&raw) == nil {
			msgs = append(msgs, raw)
		}
	}
	return GroupVoiceFrom(msgs)
}
