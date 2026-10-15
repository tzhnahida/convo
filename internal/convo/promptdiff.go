package convo

import "strings"

// ─────────────────────────────────────────────────────────────
// §8.1 段序的**机器可检**版本 —— 提示词差分尺子
//
// 它测不了"她这句话像不像人" (那需要真模型或人)。它测的是另一类事故,
// 而这一整个项目里抓到的真 bug **几乎全部属于这一类**:
//   · 某一段悄悄没被注入 (BodyLine / ApplyStyle 死了整整一轮);
//   · 段序被挪错 (输出协议上移过一次, 帧失败率 37% → 75%);
//   · 前缀缓存被砍 (记忆段曾经排在 persona 正后方, 前缀命中掉到 8%)。
//
// ⚠️ 这三类**都不需要模型**就能判。所以这把尺子是便宜的、可以每次改动都跑的 ——
// 这正是它的价值, 而不是"它更准"。
// ─────────────────────────────────────────────────────────────

// PromptSegment 一个段在整段提示词里的字节区间。
type PromptSegment struct {
	Name string `json:"name"`
	// ⚠️ 两个字段**各用各的 tag** —— 原来合写成一个 `json:"start_end"`,
	// 序列化时两个字段会抢同一个键 (vet 报 "repeats json tag")。
	Start int `json:"start"`
	End   int `json:"end"`
}

// promptMarkers 段位锚。改动 dialog.go 里的段标题就必须同步这里 ——
// 漏同步的表现是「这一段突然报缺失」, 而那是个**真信号**: 标题变了就是结构变了。
var promptMarkers = []struct{ Name, Marker string }{
	{"示例", "【示例对话】"},
	{"语言规则", "【语言规则】"},
	{"梗", "【你会的梗】"},
	{"方向", "【你自己最近在琢磨的方向】"},
	{"记忆", "【你记得的关于 ta 的事】"},
	{"说过的话", "【你说过的话】"},
	{"待求证", "【待求证】"},
	{"状态", "【你现在】"},
	{"最近对话", "【最近对话】"},
	{"本轮话题", "【本轮话题】"},
	{"输出", "【输出】"},
	{"时间", "【当前时间】"},
}

// SegmentPrompt 找出每个段位的字节区间 (按出现位置排序)。
func SegmentPrompt(prompt string) []PromptSegment {
	var out []PromptSegment
	for _, m := range promptMarkers {
		i := strings.Index(prompt, m.Marker)
		if i < 0 {
			continue
		}
		out = append(out, PromptSegment{Name: m.Name, Start: i, End: i + len(m.Marker)})
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Start < out[j-1].Start; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// PromptDiffResult 两份提示词的差分。
type PromptDiffResult struct {
	BaseBytes    int      `json:"base_bytes"`
	CurBytes     int      `json:"cur_bytes"`
	CommonPrefix int      `json:"common_prefix"`
	Moved        []string `json:"moved"`
	Added        []string `json:"added"`
	Removed      []string `json:"removed"`
}

// PromptDiff 比较两份提示词。
//
// ⚠️ CommonPrefix 是**前缀缓存**的直接读数: 它决定从第几个字节起缓存全部失效。
// 段序调整的唯一正当理由就是把这个数**变大**。
func PromptDiff(base, cur string) PromptDiffResult {
	r := PromptDiffResult{BaseBytes: len(base), CurBytes: len(cur)}
	n := len(base)
	if len(cur) < n {
		n = len(cur)
	}
	for i := 0; i < n; i++ {
		if base[i] != cur[i] {
			break
		}
		r.CommonPrefix++
	}
	bs, cs := SegmentPrompt(base), SegmentPrompt(cur)
	bm := map[string]int{}
	for _, s := range bs {
		bm[s.Name] = s.Start
	}
	cm := map[string]int{}
	for _, s := range cs {
		cm[s.Name] = s.Start
	}
	for _, s := range cs {
		if old, ok := bm[s.Name]; ok {
			if old != s.Start {
				r.Moved = append(r.Moved, s.Name)
			}
		} else {
			r.Added = append(r.Added, s.Name)
		}
	}
	for _, s := range bs {
		if _, ok := cm[s.Name]; !ok {
			r.Removed = append(r.Removed, s.Name)
		}
	}
	return r
}

// PromptStructureProblem 结构性违规检查。返回 "" = 没问题。
//
// 三条**写死的**契约 (每一条都有实测代价):
//  1. 【输出】协议**绝不上移** —— 上移过一次, 前缀缓存 +19pt 但帧失败率 37%→75%;
//  2. 【当前时间】置尾 —— 它每分钟变, 放前面等于每分钟砍一次缓存;
//  3. 稳定段 (示例/语言规则) 在**每次轮换**的段 (最近对话/本轮话题) 之前。
func PromptStructureProblem(prompt string) string {
	segs := SegmentPrompt(prompt)
	if len(segs) == 0 {
		return "一个段位都没识别出来 —— 段标题全变了?"
	}
	pos := map[string]int{}
	for i, s := range segs {
		pos[s.Name] = i
	}
	if _, ok := pos["输出"]; !ok {
		return "缺【输出】段 —— 帧协议没注入"
	}
	if _, ok := pos["时间"]; ok {
		if pos["时间"] != len(segs)-1 {
			return "【当前时间】不在最后 —— 它每分钟变, 放前面等于每分钟砍一次缓存"
		}
		if pos["输出"] > pos["时间"] {
			return "【输出】协议被挪到了时间之后"
		}
	}
	// ⚠️ 【输出】必须排在**所有内容段之后** —— 这是「绝不上移」的可判定形式。
	// 实测: 上移过一次, 前缀缓存 +19pt 但帧失败率 37% → 75%。尾部位置是承重的。
	for _, late := range []string{"最近对话", "本轮话题", "记忆"} {
		if li, ok := pos[late]; ok && pos["输出"] < li {
			return "【输出】协议被挪到了内容段「" + late + "」之前 —— 尾部位置是承重的"
		}
	}
	for _, stable := range []string{"示例", "语言规则"} {
		si, ok := pos[stable]
		if !ok {
			continue
		}
		for _, fast := range []string{"最近对话", "本轮话题"} {
			fi, ok2 := pos[fast]
			if !ok2 {
				continue
			}
			if si > fi {
				return "稳定段「" + stable + "」排在了每轮变化段「" + fast + "」之后 —— 段序即缓存策略"
			}
		}
	}
	return ""
}
