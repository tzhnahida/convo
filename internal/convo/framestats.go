package convo

import (
	"strings"
	"sync"
)

// ——— 帧协议健康度 (§12 核心指标: 帧失败率) ———
//
// §12 把"帧失败率"列为必须监控的核心指标, 但此前**没有任何埋点** ——
// 于是"降级率 ≈18%"这种数字只能靠人肉翻日志, 也无法回答"这次改动让帧变好还是变坏"。
//
// 为什么它是**决策前置**: 「输出协议」(帧 schema) 是唯一"稳定却留在提示词尾部"的内容,
// 把它上移约能再拿 ~20 点前缀缓存 —— 但它同时是"生成前最近指令"。
// 没有这张表, 动段序就是**无法归因**的行为改动: 缓存涨了、帧坏了, 分不清是谁的锅。
//
// 计数一律走包级状态 + 快照读取, 调用方(DialogEngine / 仿真 / 管理页)不需要持有它。
type FrameStats struct {
	Replies     int            `json:"replies"`    // 对话生成总次数 (一次 Reply = 一次)
	FirstPassOK int            `json:"first_pass"` // 第一次就通过校验 (健康路径)
	RetryUsed   int            `json:"retry_used"` // 触发过"修正重试"
	RetryOK     int            `json:"retry_ok"`   // 重试后通过
	Degraded    int            `json:"degraded"`   // 最终降级放行 (复用原始输出, 标 kind=fallback)
	HardFail    int            `json:"hard_fail"`  // 模型零输出 → 报错, **不编造**
	NoJSON      int            `json:"no_json"`    // 有输出但解析不出 JSON 帧
	Violations  map[string]int `json:"violations"` // 违规类型分布 (按首词归类, 不含具体 id)

	// 失败**原始输出**的形态分布 —— 决定"修复 pass"该怎么设计。
	// 没有这一层, 37% 的失败率只是一个数字: 不知道该修解析器、该改提示词、还是该重试。
	FailureKinds map[string]int `json:"failure_kinds"`
	Samples      []string       `json:"samples"` // 头几条失败的原文样本 (截断), 供人眼判读

	// 修复 pass 的战果 —— 它是"该修解析器, 还是该多打一次模型"这个问题的答案。
	Repairs  map[string]int `json:"repairs"`   // 修复名 → 命中次数
	RepairOK int            `json:"repair_ok"` // 修完真的解析出来了的次数

	// A6 硬拒: **刻意不说**的次数。
	// ⚠️ 它**不进**帧失败率 —— 硬拒是设计内的行为(宁可少说一句), 不是故障。
	// 混进失败率会让"它学会了闭嘴"看起来像"它坏了"。
	HardRefused   int            `json:"hard_refused"`
	RefuseReasons map[string]int `json:"refuse_reasons"`

	// ——— §6.3 三态计数 ———
	//
	// ⚠️ **只统计"说话"的话, 沉默率永远是 100%** —— 那正是这一组存在的理由。
	// L910「让'不说'可解释、可回归」: 沉默是**设计内的一态**, 不是"没有发生的事";
	// 不计数就等于它不存在, 于是"它九成判否"这句话永远没法验证。
	//
	// ⚠️ 与 HardRefused 同规: **沉默不进帧失败率** —— 它是结论, 不是故障。
	StanceSilent int `json:"stance_silent"`
	StanceReact  int `json:"stance_react"`
	StanceSpeak  int `json:"stance_speak"`
}

// SilentRate 群消息里被判沉默的比例 (无样本 → 0, **不是 100%**)。
//
// 零值约定: 一条群消息都没有时返回 0 —— 返回 100% 会让"引擎还没接到群事件"
// 看起来像"它一句话都不说"。
func (f FrameStats) SilentRate() float64 {
	total := f.StanceSilent + f.StanceReact + f.StanceSpeak
	if total == 0 {
		return 0
	}
	return float64(f.StanceSilent) / float64(total)
}

// MaxFailureSamples 报告里保留的失败样本条数 (原文可能很长, 只留够判读的)。
const MaxFailureSamples = 6

var (
	frameMu    sync.Mutex
	frameStats = FrameStats{Violations: map[string]int{}, FailureKinds: map[string]int{}, Repairs: map[string]int{}, RefuseReasons: map[string]int{}}
)

// noteStance 记一次三态裁决 (§6.3)。
func noteStance(st SpeakStance) {
	frameMu.Lock()
	switch st {
	case StanceSilent:
		frameStats.StanceSilent++
	case StanceReact:
		frameStats.StanceReact++
	case StanceSpeak:
		frameStats.StanceSpeak++
	}
	frameMu.Unlock()
}

// ResetFrameStats 清零 (仿真每次运行开始时调用; 正式进程可周期性滚动)。
func ResetFrameStats() {
	frameMu.Lock()
	frameStats = FrameStats{Violations: map[string]int{}, FailureKinds: map[string]int{}, Repairs: map[string]int{}, RefuseReasons: map[string]int{}}
	frameMu.Unlock()
}

// FrameStatsSnapshot 读一份快照 (深拷贝 Violations, 调用方可安全长期持有)。
func FrameStatsSnapshot() FrameStats {
	frameMu.Lock()
	defer frameMu.Unlock()
	s := frameStats
	s.Violations = make(map[string]int, len(frameStats.Violations))
	for k, v := range frameStats.Violations {
		s.Violations[k] = v
	}
	s.FailureKinds = make(map[string]int, len(frameStats.FailureKinds))
	for k, v := range frameStats.FailureKinds {
		s.FailureKinds[k] = v
	}
	s.Samples = append([]string(nil), frameStats.Samples...)
	s.Repairs = make(map[string]int, len(frameStats.Repairs))
	for k, v := range frameStats.Repairs {
		s.Repairs[k] = v
	}
	s.RefuseReasons = make(map[string]int, len(frameStats.RefuseReasons))
	for k, v := range frameStats.RefuseReasons {
		s.RefuseReasons[k] = v
	}
	return s
}

// FrameFailureRate 帧失败率 = (降级 + 硬失败) / 总生成次数。
// **这是 §12 那个指标的定义**, 单调写在这里, 免得每个调用方各算一套。
func (s FrameStats) FailureRate() float64 {
	if s.Replies == 0 {
		return 0
	}
	return float64(s.Degraded+s.HardFail) / float64(s.Replies)
}

// ViolationKey 把违规描述归成一个稳定的短键。
// 违规原串里带具体 mem_id / 类别名 ("mem_id_refs m_abc 不在注入列表"), 直接当 map 键会让
// 分布表爆炸且无法聚合 —— 取首词即可, 这正是我们想看的"哪一类在错"。
func ViolationKey(desc string) string {
	desc = strings.TrimSpace(desc)
	if desc == "" {
		return "(空)"
	}
	if i := strings.IndexAny(desc, " (（"); i > 0 {
		return desc[:i]
	}
	return desc
}

func noteFrame(f func(*FrameStats)) {
	frameMu.Lock()
	if frameStats.Violations == nil {
		frameStats.Violations = map[string]int{}
	}
	if frameStats.FailureKinds == nil {
		frameStats.FailureKinds = map[string]int{}
	}
	if frameStats.Repairs == nil {
		frameStats.Repairs = map[string]int{}
	}
	if frameStats.RefuseReasons == nil {
		frameStats.RefuseReasons = map[string]int{}
	}
	f(&frameStats)
	frameMu.Unlock()
}

func noteReply()     { noteFrame(func(s *FrameStats) { s.Replies++ }) }
func noteFirstPass() { noteFrame(func(s *FrameStats) { s.FirstPassOK++ }) }
func noteRetryUsed() { noteFrame(func(s *FrameStats) { s.RetryUsed++ }) }
func noteRetryOK()   { noteFrame(func(s *FrameStats) { s.RetryOK++ }) }
func noteNoJSON()    { noteFrame(func(s *FrameStats) { s.NoJSON++ }) }

// noteHardRefused 记一次 A6 硬拒 (刻意不说)。**它不计入帧失败率** —— 见 FrameStats 注释。
func noteHardRefused(reason string) {
	noteFrame(func(s *FrameStats) {
		if s.RefuseReasons == nil {
			s.RefuseReasons = map[string]int{}
		}
		s.HardRefused++
		s.RefuseReasons[reason]++
	})
}

// noteRepair 记一次修复 pass: 命中哪个修复、以及修完是否真的解析出来了。
func noteRepair(kind string, ok bool) {
	noteFrame(func(s *FrameStats) {
		if s.Repairs == nil {
			s.Repairs = map[string]int{}
		}
		s.Repairs[kind]++
		if ok {
			s.RepairOK++
		}
	})
}

// ClassifyTerminalFailure 是终态失败用的分类: 先按原始形态判, 但**修得好就不算解析问题**。
//
// 为什么必须分开: 修复 pass 上线后, 剩下的失败里有一部分是"原文确实畸形、但修复器一修就好
// —— 它之所以仍然失败, 是因为**修好之后的帧内容违规**"(典型: 空的 text 项)。
// 把这种记成 invalid_json 会把下一步导错方向: 你会去继续加强解析器, 而真正该改的是提示词。
func ClassifyTerminalFailure(raw string) string {
	kind := ClassifyRaw(raw)
	switch kind {
	case "invalid_json", "truncated", "fence_only":
		if fixed, rk := RepairJSON(raw); rk != "" {
			if _, err := extractJSON(fixed); err == nil {
				return "repaired_then_invalid"
			}
		}
	}
	return kind
}

// noteFailure 记一次**终态失败**并留样本 (raw = 模型这一轮的真实输出, 可能为空)。
func noteFailure(raw string, hard bool) {
	kind := ClassifyTerminalFailure(raw)
	noteFrame(func(s *FrameStats) {
		if hard {
			s.HardFail++
		} else {
			s.Degraded++
		}
		if s.FailureKinds == nil {
			s.FailureKinds = map[string]int{}
		}
		s.FailureKinds[kind]++
		if len(s.Samples) < MaxFailureSamples {
			s.Samples = append(s.Samples, kind+": "+truncRunes(raw, 160))
		}
	})
}

// ClassifyRaw 把"帧失败时的原始输出"归成一类。分档依据是**修复手法不同**:
//
//	prose        → 模型压根没按帧格式输出  → 改提示词 / 加强输出协议
//	truncated    → 有 { 无 } (被 max_tokens 截断) → 提高上限 或 让它说短点
//	invalid_json → 花括号配平但语法不合法   → 解析器容错 (去尾逗号/单引号/裸键)
//	fence_only   → 只有 ``` 围栏没有对象  → 剥围栏后重试
//	empty        → 零输出                 → 服务/网络, 重试即可
//	object_ok    → 能抽出合法对象         → 失败在上层(校验), 不是解析
func ClassifyRaw(raw string) string {
	t := strings.TrimSpace(raw)
	switch {
	case t == "":
		return "empty"
	case func() bool { _, err := extractJSON(raw); return err == nil }():
		return "object_ok"
	case !strings.Contains(t, "{"):
		if strings.Contains(t, "```") {
			return "fence_only"
		}
		return "prose"
	case !strings.Contains(t, "}"):
		return "truncated"
	default:
		return "invalid_json"
	}
}

// truncRunes 按**字符**截断 (不是字节 —— 中文按字节切会切出半个字)。
func truncRunes(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}
func noteViolations(vs []string) {
	noteFrame(func(s *FrameStats) {
		for _, v := range vs {
			s.Violations[ViolationKey(v)]++
		}
	})
}
