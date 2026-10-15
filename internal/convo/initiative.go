package convo

import (
	"regexp"
	"strings"
)

// ——— A3 开口方向与因果校验 ———
//
// 病灶: **主动开场却说"你今晚怎么这么有空来撩我"** —— 它把"我找你"说成了"你找我"。
// 这不是文风问题, 是**事实层错误**: 谁起的头是系统知道的事实, 不该由模型发明。
//
// 两层校验:
//
//	① **自述与事实比对** —— 模型填的 initiative 必须与系统事实(是否主动 tick 触发)一致;
//	② **因果负向模式** —— 主动开口时不许出现"预设对方先开口"的说法。
//
// ⚠️ ② 只在**主动开口**时生效。用户先说话时, "你终于来了"是完全正常的。
// 这与 A6 的取向相反: A6 是宁可少说, 这里必须**宁可不管** —— 误伤的代价是被动场景下它不敢回话。
//
// 预设"对方起的头"的说法。**仅用于主动开口场景**。
//
// 两类混用: 固定短语用子串(精确、好读), 可插入修饰的说法用正则 ——
// 第一版全用子串, 结果 "你今晚怎么这么有空来撩我" 从 "你怎么有空" 里漏了过去(实测)。
// **枚举穷不尽中文的插入语**, 该上正则的地方不要硬凑字面量。
var presupposeSubstr = []string{
	"你终于",
	"你想起我",
	"难得你",
	"你主动找我",
	"你是不是想起",
	"你居然找我",
}

var presupposeRe = []*regexp.Regexp{
	// "你(今晚/今天)怎么…有空/闲" 与 "你怎么这么有空" —— 同一类反问的两种插入
	regexp.MustCompile(`你怎么(这么|那么)?(有空|闲)|你(今晚|今天|最近|这两天)怎么`),
	// "你居然/竟然 …来/找/理/回"
	regexp.MustCompile(`你(居然|竟然)[^。！？\n]{0,4}(来|找|理|回)`),
}

// InitiativeViolations 返回 A3 的违规列表 (空 = 通过)。
// proactive = 这一轮是**它自己**起的头 (无用户输入)。
func InitiativeViolations(f OutputFrame, proactive bool, text string) []string {
	var out []string

	// ① 自述 vs 事实。缺省不算错 —— 不许因为它没填就判违规 (那会逼它每次都填, 变成模板)。
	if f.Initiative == "ai" || f.Initiative == "user" {
		want := "user"
		if proactive {
			want = "ai"
		}
		if f.Initiative != want {
			out = append(out, "initiative 自述与事实不符 (自述 "+f.Initiative+", 事实 "+want+")")
		}
	}

	// ② 因果负向模式 —— 只在主动开口时查
	if proactive {
		for _, p := range presupposeSubstr {
			if strings.Contains(text, p) {
				out = append(out, "主动开口却说成对方先开口 ("+p+")")
				break
			}
		}
		for _, re := range presupposeRe {
			if m := re.FindString(text); m != "" {
				out = append(out, "主动开口却说成对方先开口 ("+m+")")
				break
			}
		}
	}
	return out
}
