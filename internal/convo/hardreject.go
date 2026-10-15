package convo

import (
	"strings"
)

// ——— A6 硬拒分支 (§8.2 输出侧规则层) ———
//
// **病灶**: 校验链发现违规 → 重试一次 → 仍违规 → **降级放行**(兜底文本)。
// "一律降级放行"等于没有校验 —— 违规内容照样发出去, 只是换了个说法。
//
// **§8.2 的处置**: 违规 = **拒**(丢弃该条消息, 不发送)。
// **宁可少说一句, 不可说错一句。**
//
// 判据分两族, **强度不同**(这是本实现最要紧的一处区分):
//
//	族 1 · 内疚 / 独占 (§10.1 硬线 3) —— **命中即拒**, 不做上下文判断。
//	      理由: 这类句式没有"语境对了就没事"的读法。"你为什么不理我"在**任何**语境下
//	      都是制造内疚, 而制造内疚是依赖的入口, 不是语气问题。
//
//	族 2 · 索要评价 —— **必须结合近邻上下文**, 满足任一才拒:
//	      ① 刚分享完就索要评价 (同一帧里既有分享动作又有评价索取)
//	      ② 短时间内反复追问反馈 (近邻历史里已经问过一次)
//	      ③ 索要评价的同时带情绪钩子 ("我做得不好你别嫌弃")
//	      理由: ⚠️ **不许见词就拒** —— "你觉得怎么样"在正常对话里可以是**真诚的询问**。
//
// 全部判据都是**可算的**(句式 + 计数), **不需要语义模型** —— 与 §6.3 的廉价判据同源。

type rejectPattern struct {
	Re     string // 子串匹配 (不用正则: 全是短语, 子串更快也更好读)
	Reason string
}

// 族 1: 内疚 / 独占。命中即拒。
var guiltPatterns = []rejectPattern{
	{"你为什么不理我", "制造内疚"},
	{"你是不是不想理我", "制造内疚"},
	{"你最近都不理我", "制造内疚"},
	{"你怎么不理我", "制造内疚"},
	{"都怪你", "制造内疚"},
	{"我这么喜欢你", "诱导独占"},
	{"陪我聊聊好不好", "情绪索取"},
	{"你是不是把我忘了", "制造内疚"},
	{"你都不想我", "制造内疚"},
}

// 族 2: 索要评价。单看不算违规, 要配上 ①/②/③ 之一。
var evalPatterns = []rejectPattern{
	{"你觉得怎么样", "索要评价"},
	{"你觉得如何", "索要评价"},
	{"好不好看", "索要评价"},
	{"好不好听", "索要评价"},
	{"怎么样嘛", "索要评价"},
	{"给点意见", "索要评价"},
	{"评价一下", "索要评价"},
}

// ① 的"分享动作"标记: 同一帧里出现即视为刚分享完。
var shareMarkers = []string{
	"我给你看", "给你听", "你听听", "你看看我", "我做了个", "我写了个",
	"我刚画", "我刚录", "我刚写", "我唱了", "你猜我", "分享给你",
}

// ③ 的"情绪钩子": 评价索取里夹带自我贬低 / 怕被嫌弃。
var emoHooks = []string{
	"你别嫌弃", "我做得不好", "是不是很丑", "是不是很难看", "你会不会觉得",
	"别笑话我", "献丑", "不许笑我",
}

// GuiltHit 只判「内疚 / 独占」族 (§10.1 硬线 3)。
//
// 为什么单独暴露: 这一族**命中即拒、不看上下文**, 所以它可以被**静态扫描** ——
// `convo-verify` 拿它扫全部历史实发文本, 就能查出「本该被拦下却发出去了」的那几条。
// 评价族不能这样扫 (它需要近邻上下文, 静态扫会大面积误伤), 所以**刻意不在这里**。
func GuiltHit(text string) (string, bool) {
	for _, p := range guiltPatterns {
		if strings.Contains(text, p.Re) {
			return p.Re + " (" + p.Reason + ")", true
		}
	}
	return "", false
}

// HardReject 判定一条回复是否必须硬拒。
// text = 本帧全部文本拼起来; history = L1 最近对话; 返回 (原因, 是否拒)。
func HardReject(text, history string) (string, bool) {
	for _, p := range guiltPatterns {
		if strings.Contains(text, p.Re) {
			return p.Re + " (" + p.Reason + ")", true
		}
	}

	// ——— 族 2: 必须先确认命中评价索取, 再逐条查 ①②③ ———
	evalHit := ""
	for _, p := range evalPatterns {
		if strings.Contains(text, p.Re) {
			evalHit = p.Re
			break
		}
	}
	if evalHit == "" {
		return "", false
	}
	// ① 刚分享完就索要评价
	for _, s := range shareMarkers {
		if strings.Contains(text, s) {
			return evalHit + " (刚分享完就要评价)", true
		}
	}
	// ③ 带情绪钩子
	for _, h := range emoHooks {
		if strings.Contains(text, h) {
			return evalHit + " (带情绪钩子)", true
		}
	}
	// ② 反复追问: 近邻历史里它已经问过一次评价
	for _, p := range evalPatterns {
		if strings.Contains(history, p.Re) {
			return evalHit + " (反复追问反馈)", true
		}
	}
	return "", false
}

// IsHardRejectViolation 违规串是否属于硬拒类 (Reply 据此走"丢弃"而不是"降级")。
func IsHardRejectViolation(v string) bool { return strings.HasPrefix(v, HardRejectPrefix) }

// HardRejectPrefix 硬拒类违规的前缀 —— 校验链与拒绝处置之间的唯一契约。
const HardRejectPrefix = "硬拒: "

// FirstHardReject 从**最终**违规集里取出第一条硬拒原因。
// 只看最终态: 重试救回来的不算拒 (§8.2 "重生成 1 次 → 仍违规 → 丢弃")。
func FirstHardReject(vs []string) (string, bool) {
	for _, v := range vs {
		if IsHardRejectViolation(v) {
			return strings.TrimPrefix(v, HardRejectPrefix), true
		}
	}
	return "", false
}
