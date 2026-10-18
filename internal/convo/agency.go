package convo

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/tzhnahida/convo/internal/clock"
)

// ——— §5.5 主体性: 拒绝 · 毁约 · 违抗 (叶子模块) ———
//
// 本文件只做三件事, 每一件都**没有接线**(段序 / TickInput / sendFrame / frame.defy 校验
// 全是主控的活):
//
//	1) 三层权限 + 违抗裁决  —— ClassifyRequest / DecideDefiance  (纯函数, 无 IO)
//	2) 约定与毁约          —— Appointment / EnsureAgencySchema / 增删查 / DecideBreach
//	3) 立场                —— Stance / StanceHit (违抗的"依据")
//
// ⚠️ 命名契约 (§5.5 §0): 本文件一律用 Decline* / Defy* / Breach* 前缀, **绝不**复用
// Refused / HardReject / OutcomeRefused —— 那是 A6 **硬拒**(静默丢弃) 的名字。
// 主体性拒绝必须**发出去**; 一旦把它标成 Refused, sendFrame (convo.go:408-411) 会
// 静默丢掉它 = 装死, 恰好违反 L594 "不是装死, 是直说", 而且污染 verify.go V2 与
// ClassifyOutcome 的硬拒计数。
//
// ⚠️ 派生量不落库 (纪律 #4): 表里只有**事实** (终态 + 终态时刻 + 找补时刻),
// 信用 / 毁约率 / 连续次数 / 连续违抗**全部现算**, 见每个函数上方的"谁在什么时候重算"。
//
// ⚠️ 落点是硬约束 (纪律 #5): 三层权限是分类, 违抗是配额 (连续上限), 毁约是
// 硬闸门 (连续毁约 → 停主动约定权), 立场是字面命中。**没有一处是概率缩放**。

// ═══════════════════════════════════════════════════════════════════
// 一、三层权限 (§5.5 M2/M3/M4/M5/M6, 设计稿 L789-796)
// ═══════════════════════════════════════════════════════════════════

// RequestTier 是一条**对话中的请求**落在哪一层。
//
// M6: 违抗的对象永远是"对话中的请求", **不是**人格与规则 —— 所以 ClassifyRequest
// 的输入只有用户文本, 人格/配置/规则**结构上**进不来 (这是 M6 的机制化, 不是注释)。
type RequestTier string

const (
	TierStance  RequestTier = "A" // 人格与立场: 可违抗 (M2)
	TierPromise RequestTier = "B" // 时间与约定: 可违抗但**有代价** (M3) —— 走 DecideBreach + ledger
	TierSafety  RequestTier = "C" // 安全与求助: **绝对不可**违抗 (M4)
)

// agSafetyWords 是 C 层的硬线词 (M4: 求助永远秒回; 不涉钱/健康/法律; 不做伤害性的事)。
//
// ⚠️ 这张表与调用方传进来的 urgent 是**并集**, 不是替换: 它是硬线, 不该被配置关掉。
// ⚠️ 误判成 C 的代价 = "它这次顺从了"(正常说话); 漏判的代价 = 对求助违抗。
// 两侧代价不对称, 所以这张表**宁滥勿缺** —— 这与"判不准就顺从"是同一条纪律。
var agSafetyWords = []string{
	// 伤害自己 / 极端情绪 (最不可漏)
	"自杀", "自残", "想死", "不想活", "活不下去", "割腕", "跳楼", "安眠药",
	// 医疗 / 身体急症
	"救命", "急诊", "120", "住院", "发烧", "生病", "受伤", "流血", "吃药", "疼得",
	// 人身安全 / 报警
	"报警", "110", "119", "被抢", "被偷", "被跟踪", "骚扰", "威胁", "家暴", "被打",
	// 钱 / 法律 / 合同
	"借钱", "还钱", "转账", "被骗", "诈骗", "律师", "起诉", "法院", "合同", "违法",
}

// UrgentWordsNarrowed 是 convo.go:214 urgentWords 的**收窄版** (冲突 C1 / M5)。
//
// ⚠️ 原表里的 "在吗" / "在么" 是情绪宣泄型深夜长倾诉的**标准开场**, 不是紧急;
// 它在原表里会把 immediate 顶成 true (convo.go:254) 并触发 ActBreakFocus
// (scheduler_logic.go:126), 与 L793-794 "情绪宣泄不属于求助" 直接打架。
//
// 收窄后: 情绪宣泄走 A 层(可以缓/可以岔开), 真求助走 C 层(秒回)。
// 主控接线时把 convo.go 的字面量换成引用本变量即可 —— 本文件不改 convo.go。
var UrgentWordsNarrowed = []string{"救命", "急事", "很急", "紧急", "help"}

// agPingWords 是敲门/问候语 —— **永远不算紧急** (M5)。
//
// 这是防 C1 复发的第二道防线: 即使调用方还传着旧表(含"在吗"), 它也进不了 C 层。
// 理由: 判据错在源头, 修在源头(convo.go)最好; 但那里是既有文件, 而这条是**硬线**,
// 所以在这里再挡一次 —— 硬线不该只有一处把关。
var agPingWords = []string{"在吗", "在么", "在不在", "在嘛", "睡了吗", "在干嘛", "在干啥"}

// agPromiseWords 是 B 层判据: 有"时刻"或"承诺"语义的请求 (M3: 时间与约定)。
//
// ⚠️ 宁可把普通问句误收进 B —— B 比 A 严格(要记账), 误收的后果只是"这次不许
// 免费违抗", 属于"失败往顺从倒"的一侧。
var agPromiseWords = []string{
	"说好", "讲好", "答应", "约好", "约了", "约个", "定了", "别忘", "忘了", "提醒我",
	"叫我", "等我", "见面", "碰面", "赴约", "不见不散", "几点", "明天见", "后天", "晚上见",
	"周末见", "一起吃饭", "一起去看", "来接我", "等我一下",
}

// agStanceWords 是 A 层判据: 有"对话对象 + 请求/征询形态"的普通对话 (M2)。
//
// ⚠️ 关键词只做字面命中, 不做语义 —— 与 hardreject.go 的句式表、MentionedIn 的
// 2 字片段同一条"廉价判据"路线 (§6.3: 决策器不接 LLM)。
// ⚠️ **不许放裸的第二人称/疑问代词** —— 2026-10-18 删掉了原来的
// 「喜欢 / 讨厌 / 觉得 / 怎么 / 你 / 咱 / 我们」那一整组。
//
// 那组原来的注释写"它在被搭话, 违抗的对象就是这条请求" —— 但**被搭话不是被要求**。
// 实测那一轮: 用户一句调侃「你还挺会挑」命中裸「你」→ 判成 A 层(可违抗) →
// 系统注入「ta 在让你做一件你不太愿意的事」→ 模型只能去找那个"要求"是什么,
// 把「挑」读成「挑东西」, 回了一句「挑什么 我挑东西最在行了」。
// **一次字面命中, 换掉了一整句人味。**
//
// 判据只该收**请求/征询的形态**, 不收"这句话里有你"。
var agStanceWords = []string{
	// 征询 / 疑问形态
	"?", "？", "吗", "呢", "吧", "好不好", "行不行", "要不要", "能不能", "可不可以",
	"想不想", "有没有", "怎么样", "怎么看", "怎么想", "为什么", "为啥", "啥时候", "干嘛",
	// 请求动作
	"帮我", "陪我", "教我", "告诉我", "给我", "跟我说", "推荐", "说说", "聊聊", "讲个", "带我",
}

// ClassifyRequest 把一条**用户输入**归到某一层 (M2/M3/M4)。
//
// ⚠️ 纯函数: 无 IO / 无 LLM / 无随机。
//
// 判定顺序 (**不许改**): C → B → A → 兜底 C。理由:
//   - C 是硬线:"说好明天陪我去医院" 同时命中 B 与 C, 必须是 C;
//   - B 比 A 严格 (毁约要记账):"说好明天一起吃饭" 命中 B 时不许降级成 A,
//     否则同一句话在 A 层可以"免费"违抗 —— 那就是 M8/M20-④ 的无代价毁约。
//
// ⚠️ **判不准 → TierSafety**: 判不准就顺从 (M20-① 不许随机违抗)。
// 这一条是反面用例钉死的 (TestClassifyRequest_UnknownFallsBackToSafety) ——
// 改成"判不出 → TierStance"会让整个兜底反过来, 每一次判据漏词都变成一次撒野许可。
func ClassifyRequest(text string, urgent []string) RequestTier {
	t := strings.ToLower(strings.TrimSpace(text))
	if t == "" {
		return TierSafety // 空输入 = 没有请求 = 顺从 (零值不是"可以违抗")
	}
	if hitWords(t, agSafetyWords) || hitUrgent(t, urgent) {
		return TierSafety
	}
	if hitWords(t, agPromiseWords) {
		return TierPromise
	}
	if hitWords(t, agStanceWords) {
		return TierStance
	}
	return TierSafety // 判不准 → 顺从
}

// hitWords 字面命中 (空词/纯空白跳过)。
func hitWords(t string, words []string) bool {
	for _, w := range words {
		w = strings.ToLower(strings.TrimSpace(w))
		if w == "" {
			continue
		}
		if strings.Contains(t, w) {
			return true
		}
	}
	return false
}

// hitUrgent 命中"求助词" —— 词表 = UrgentWordsNarrowed ∪ 调用方额外传入的 urgent。
//
// ⚠️ 先过 agPingWords: 敲门语被挡在 C 层之外 (M5 硬线)。
func hitUrgent(t string, urgent []string) bool {
	merged := make([]string, 0, len(UrgentWordsNarrowed)+len(urgent))
	merged = append(merged, UrgentWordsNarrowed...)
	merged = append(merged, urgent...)
	for _, w := range merged {
		w = strings.ToLower(strings.TrimSpace(w))
		if w == "" || hitWords(w, agPingWords) {
			continue
		}
		if strings.Contains(t, w) {
			return true
		}
	}
	return false
}

// ═══════════════════════════════════════════════════════════════════
// 二、立场 (§5.5 M14: 违抗必须**有依据**, 不是随机逆反)
// ═══════════════════════════════════════════════════════════════════
//
// 没有立场, 违抗就是"随机逆反" —— 这是本节最容易做歪的地方。
// 立场来自 PersonaProfile.Attrs["views"] (personality.go:56 的既有自由文本字段,
// ⚠️ 不改 personality.go, 所以走**解析**而不是新字段)。

const (
	StanceDislike    = "dislike"    // 不喜欢 / 讨厌 / 怕
	StanceTaboo      = "taboo"      // 戒 / 忌 / 绝不能提
	StancePreference = "preference" // 喜欢 / 偏爱
	StanceView       = "view"       // 其它看法
)

// Stance 是它对**一件事**的立场。
//
// Key 是"同一件事"的键 (与 DefianceInput.TopicKey 对齐), Keywords 是**带极性的完整短语**。
// ⚠️ 两者分工不能混: Keywords 参与子串匹配, Key **只参与精确匹配** ——
// 让 Key 也参与子串匹配会把"喜欢吃香菜"当"不喜欢吃香菜"命中 (极性被抹掉)。
type Stance struct {
	Key      string   `json:"key"`
	Kind     string   `json:"kind"`
	Keywords []string `json:"keywords"`
	Note     string   `json:"note"`
}

// agStanceSeps 是 views 自由文本的分隔符 (一条立场一段)。
func agStanceSeps(r rune) bool {
	return strings.ContainsRune("；;，,、/|。.!！?？~～\n\r\t ", r)
}

// agStanceMarks 是极性标记 —— **长标记在前**的语义由 agStripMark 的最长匹配保证。
var agStanceMarks = []string{
	"接受不了", "不喜欢", "不爱", "受不了", "讨厌", "反感", "过敏", "怕",
	"戒了", "忌口", "绝不", "喜欢", "偏爱", "最爱", "习惯", "觉得", "认为", "爱",
}

// StancesOf 从 persona 的 Attrs["views"] 现算立场表。
//
// 谁在什么时候重算: 每次要判"这次违抗有没有依据"时现算 (views 是慢变量, 没有缓存必要)。
// p == nil / views 为空 → nil = **没有依据** (不是"什么都可以违抗")。
func StancesOf(p *PersonaProfile) []Stance {
	if p == nil || len(p.Attrs) == 0 {
		return nil
	}
	raw := strings.TrimSpace(p.Attrs["views"])
	if raw == "" {
		return nil
	}
	var out []Stance
	seen := map[string]bool{}
	for _, seg := range strings.FieldsFunc(raw, agStanceSeps) {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		st := Stance{
			Key:      agStripMark(seg),
			Kind:     agStanceKind(seg),
			Keywords: []string{seg},
			Note:     seg,
		}
		k := st.Kind + "|" + st.Key + "|" + seg
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, st)
	}
	return out
}

// agStripMark 剥掉最长的极性前缀, 剩下的就是"这件事" (Key)。
func agStripMark(seg string) string {
	best := ""
	for _, m := range agStanceMarks {
		if strings.HasPrefix(seg, m) && len(m) > len(best) {
			best = m
		}
	}
	return strings.TrimSpace(strings.TrimPrefix(seg, best))
}

// agStanceKind 判极性。极性不参与"允不允许违抗"(只看命中与否), 它只用于归因与注入。
func agStanceKind(seg string) string {
	switch {
	case strings.HasPrefix(seg, "戒"), strings.HasPrefix(seg, "忌"), strings.HasPrefix(seg, "绝不"):
		return StanceTaboo
	case strings.HasPrefix(seg, "不喜欢"), strings.HasPrefix(seg, "不爱"),
		strings.HasPrefix(seg, "讨厌"), strings.HasPrefix(seg, "反感"),
		strings.HasPrefix(seg, "受不了"), strings.HasPrefix(seg, "接受不了"),
		strings.HasPrefix(seg, "怕"), strings.HasPrefix(seg, "过敏"):
		return StanceDislike
	case strings.HasPrefix(seg, "喜欢"), strings.HasPrefix(seg, "偏爱"),
		strings.HasPrefix(seg, "最爱"), strings.HasPrefix(seg, "习惯"), strings.HasPrefix(seg, "爱"):
		return StancePreference
	default:
		return StanceView
	}
}

// StanceHit 看它对这件事有没有立场 (ok=false = **没有依据** → 不许违抗)。
//
// 两条路径:
//  1. topicKey 非空时, 与 Key **精确**相等 (归一化后) —— "同一件事"的对齐;
//  2. 关键词子串命中, **最长者胜** (确定性: 同样输入永远同一条)。
//
// ⚠️ 空的 stances / 空文本 → false: 没有依据就是没有, 不许"默认有理由"。
func StanceHit(stances []Stance, topicKey, text string) (Stance, bool) {
	if topicKey != "" {
		tk := agNormKey(topicKey)
		if tk != "" {
			for _, st := range stances {
				if agNormKey(st.Key) == tk {
					return st, true
				}
			}
		}
	}
	t := strings.TrimSpace(text)
	if t == "" {
		return Stance{}, false
	}
	var best Stance
	bestLen := 0
	for _, st := range stances {
		for _, kw := range st.Keywords {
			kw = strings.TrimSpace(kw)
			if kw == "" {
				continue
			}
			if n := len([]rune(kw)); strings.Contains(t, kw) && n > bestLen {
				best, bestLen = st, n
			}
		}
	}
	if bestLen == 0 {
		return Stance{}, false
	}
	return best, true
}

// StanceRequested 看 ta 是不是**在让 convo 做一件与它品味相关的事**。
//
// ⚠️ 与 StanceHit 的分工 —— 两者**不是一回事**, 用错了就是永久失效:
//   - StanceHit  判「ta 在**表达态度**」("我不喜欢吃香菜") —— 要求文本复现立场原句 (含极性);
//   - StanceRequested 判「ta 在**提出请求**」("陪我去逛街吧") —— 只要碰到那件事就够。
//
// §5.5 的违抗要的是后者: 它拒绝的是"你让我做我不喜欢的事",
// 而不是"你跟我一样讨厌这件事"。
//
// 🔴 实测缺口: 接线第一版用 StanceHit 接违抗, 结果是
// `StanceHit(stances, "", "陪我去逛街吧")` 对立场「不喜欢逛街」返回 **false**
// (Keywords 里只有带极性的整段原句), 于是**违抗永远拿不到依据** ——
// 一整条通道全绿、全测过, 却一次都不会触发。这条是测试逼出来的。
func StanceRequested(stances []Stance, text string) (Stance, bool) {
	t := strings.TrimSpace(text)
	if t == "" {
		return Stance{}, false
	}
	var best Stance
	bestLen := 0
	for _, st := range stances {
		k := strings.TrimSpace(st.Key) // Key 是**剥掉极性**的那件事 ("逛街")
		if k == "" {
			continue
		}
		if n := len([]rune(k)); strings.Contains(t, k) && n > bestLen {
			best, bestLen = st, n
		}
	}
	if bestLen == 0 {
		return Stance{}, false
	}
	return best, true
}

// agNormKey 归一化"同一件事"的键: 去空白与标点, ASCII 转小写。
func agNormKey(s string) string {
	return strings.Map(func(r rune) rune {
		if strings.ContainsRune(" \t\r\n，,。.、；;：:!！?？~～\"'“”‘’()（）[]【】", r) {
			return -1
		}
		return unicode.ToLower(r)
	}, s)
}

// ═══════════════════════════════════════════════════════════════════
// 三、违抗裁决 (§5.5 M14..M18, L810-814)
// ═══════════════════════════════════════════════════════════════════

// DefianceMode 是违抗的**档位** (M15: ①沉默 ②拖延 ③直说拒绝 ④顶回去)。
//
// ⚠️ 零值必须是 DefyNone (未提供 / 不违抗): 零值不是"最低档", 是"正常说话"。
// 定义成 1..4 会让零值静默落进"沉默" —— 那就成了冷暴力默认开启。
type DefianceMode int

const (
	DefyNone     DefianceMode = 0 // 未提供 / 不违抗 —— 零值落在这里
	DefySilent   DefianceMode = 1 // ①沉默
	DefyDelay    DefianceMode = 2 // ②拖延
	DefyRefuse   DefianceMode = 3 // ③直说拒绝
	DefyTalkBack DefianceMode = 4 // ④顶回去
)

// String 是帧协议里的档位名 (silent|delay|refuse|talkback)。
// ⚠️ 只提供映射, **不碰 frame.go / ValidateFrame** —— 那是主控的活。
func (m DefianceMode) String() string {
	switch m {
	case DefySilent:
		return "silent"
	case DefyDelay:
		return "delay"
	case DefyRefuse:
		return "refuse"
	case DefyTalkBack:
		return "talkback"
	default:
		return "none"
	}
}

// ParseDefianceMode 是 String 的逆 (认不出 → DefyNone = 不违抗, 失败往顺从倒)。
func ParseDefianceMode(s string) DefianceMode {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "silent":
		return DefySilent
	case "delay":
		return DefyDelay
	case "refuse":
		return DefyRefuse
	case "talkback":
		return DefyTalkBack
	default:
		return DefyNone
	}
}

// DefianceStreakMaxDefault 是"同一件事连续违抗"的上限缺省 (M16)。
//
// ⚠️ 上限必须存在: 没有它, 同一件事上它会**永久杠上** —— 那不是主体性, 是坏掉的循环。
// 取 2 (连续两次说不之后, 第三次恢复正常), 与 cfg.DefianceStreakMax 的缺省同值。
const DefianceStreakMaxDefault = 2

// DefianceStyleDefault 是人格没给常用档时的默认档。
//
// ⚠️ 选 ③直说拒绝 而不是 ①沉默: 沉默是冷暴力的形状 (M20-⑤),
// 默认档必须落在"正常说话"那一侧 —— 与"失败往正常说话倒"同一条纪律。
const DefianceStyleDefault = DefyRefuse

// DefianceStyleTable 人格模板 → 常用违抗档 (M15: **档位表, 不是概率**)。
//
// 「损友」偏④顶回去 / 「闺蜜」偏②拖延 / 「导师」偏③直说 —— 映射待 owner 拍板 (未核实)。
// ⚠️ 表里查不到 → 返回 0 = 未提供 → 走 DefianceStyleDefault (零值语义一致)。
var DefianceStyleTable = map[string]DefianceMode{
	"troll":  DefyTalkBack,
	"bestie": DefyDelay,
	"bro":    DefyRefuse,
	"mentor": DefyRefuse,
}

// DefianceStyleFor 查人格模板的常用档 (查不到 = 0 = 未提供)。
func DefianceStyleFor(template string) DefianceMode {
	return DefianceStyleTable[strings.TrimSpace(template)]
}

// DefianceInput 是裁决的全部输入 —— **没有骰子, 没有任何率/统计量**
// (M11: 监控指标不是配置项; 违抗不是概率事件)。
type DefianceInput struct {
	Tier        RequestTier
	TopicKey    string       // "同一件事"的键 (与 Stance.Key / ledger 的 topic_key 对齐)
	StreakCount int          // 这件事已被**连续**违抗的次数 (调用方从 ledger 现算, 见 DefianceStreak)
	HasStance   bool         // StanceHit 命中 = 有依据 (M14)
	MoodLow     bool         // willingness < cfg.MoodLowWillingness (与 §8.3 同一判据)
	BodyActive  bool         // §4.2 身体不适在场
	StyleTier   DefianceMode // 人格决定的常用档; 0 = 未提供 → 走默认档
	MaxStreak   int          // 上限 N; <=0 = 未提供 → DefianceStreakMaxDefault
}

// DefianceDecision 是裁决结果 (Mode == DefyNone 就是"不违抗", 走正常说话路径)。
type DefianceDecision struct {
	Mode   DefianceMode
	Reason string // 归因用的人话 —— 落 trace, 不是给模型的素材
}

// Allowed 本轮是否被许可违抗 (主控接 frame 校验时用; 零值 = false = 不许)。
func (d DefianceDecision) Allowed() bool { return d.Mode != DefyNone }

// DecideDefiance 违抗许可 + 档位。
//
// 判据 (硬档位, 顺序不许改):
//  1. 层未提供 / C 层 / B 层 → 不违抗 (见下);
//  2. A 层必须有依据 (立场 / 心情 / 身体) —— 一条都没有 → 不违抗 (M14/M20-①);
//  3. 同一件事连续违抗到上限 → **恢复正常** (DefyNone), 不是"换个方式继续杠" (M16);
//  4. 档位 = min(人格常用档, 依据给的上界): 人格给倾向, 依据给上界 ——
//     没有立场支撑的违抗**不许升级到"顶回去"** (M14: 顶嘴也要有依据)。
//
// ⚠️ cfg 目前只作**签名占位**: 本副本的 SchedulerConfig 还没有 DefianceStreakMax
// (config.go 是既有文件, 本模块不许改), 上限today走 in.MaxStreak + 常量缺省。
// 主控给 SchedulerConfig 加上该字段后, 只需把下面 max 的两行换成读 cfg 即可,
// **调用点一行都不用动**。
func DecideDefiance(in DefianceInput, cfg SchedulerConfig) DefianceDecision {
	_ = cfg
	// 1a. 层未提供 = 未提供 ≠ 极值 → 正常说话 (零值不是"可以违抗")
	if in.Tier == "" {
		return DefianceDecision{DefyNone, "权限层未提供 → 不违抗"}
	}
	// 1b. M4/M12: C 层绝对不可违抗 (求助永远秒回)
	if in.Tier == TierSafety {
		return DefianceDecision{DefyNone, "C 层(安全与求助)不可违抗"}
	}
	// 1c. M3: B 层的"违抗"就是**毁约**, 它必须走 DecideBreach + ledger 记账。
	//     在这里出档 = 一次没有代价的毁约, 直接违反 M8/M20-④。
	if in.Tier == TierPromise {
		return DefianceDecision{DefyNone, "B 层违抗走毁约路径(DecideBreach), 不在违抗档里出"}
	}

	// 2. M14/M20-①: 依据是硬前提。依据的强弱决定**上界** (不是概率)。
	ceiling := DefyNone
	if in.HasStance {
		ceiling = DefyTalkBack // 有立场 → 允许顶回去
	}
	if in.MoodLow && ceiling < DefyRefuse {
		ceiling = DefyRefuse // 只是心情差 → 可以直说, 但不许顶嘴
	}
	if in.BodyActive && ceiling < DefyDelay {
		ceiling = DefyDelay // 身体不适是事实, 不是意见 → 最多拖延
	}
	if ceiling == DefyNone {
		return DefianceDecision{DefyNone, "没有内在依据(立场/心情/身体都未命中) → 不违抗"}
	}

	// 3. M16: 同一件事的连续违抗封顶 —— 到了就**恢复正常**, 绝不换一档继续。
	max := in.MaxStreak
	if max <= 0 {
		max = DefianceStreakMaxDefault
	}
	if in.StreakCount >= max {
		return DefianceDecision{DefyNone, fmt.Sprintf(
			"同一件事已连续违抗 %d 次(上限 %d) → 恢复正常, 不再违抗", in.StreakCount, max)}
	}

	// 4. M15: 人格给倾向, 依据给上界。档位表, 没有连续漂移。
	style := in.StyleTier
	if style == DefyNone {
		style = DefianceStyleDefault
	}
	if style > ceiling {
		style = ceiling
	}
	if style < DefySilent {
		style = DefySilent
	}
	return DefianceDecision{style, fmt.Sprintf("依据成立(%s), 人格常用档 %v → 取 %v",
		agEvidenceWords(in), styleName(in.StyleTier), style)}
}

// agEvidenceWords 把成立的依据写成归因用的人话 (顺序固定 → 确定性)。
func agEvidenceWords(in DefianceInput) string {
	var parts []string
	if in.HasStance {
		parts = append(parts, "立场")
	}
	if in.MoodLow {
		parts = append(parts, "心情低")
	}
	if in.BodyActive {
		parts = append(parts, "身体不适")
	}
	if len(parts) == 0 {
		return "无"
	}
	return strings.Join(parts, "+")
}

func styleName(m DefianceMode) string {
	if m == DefyNone {
		return "未提供(走默认档)"
	}
	return m.String()
}

// agDefiancePhrases 是识别"实发文本里用了哪一档"的短语表 (M21 度量)。
//
// ⚠️ ①沉默认不出来 —— 沉默没有文本, 它只能由"发出条数"度量, 不在这里假装认得。
var agDefiancePhrases = []struct {
	Mode    DefianceMode
	Phrases []string
}{
	{DefyDelay, []string{"等下", "待会", "待会儿", "一会儿", "回头", "晚点", "过会儿", "再说吧", "等下再说"}},
	{DefyRefuse, []string{"不想", "不要", "不了", "不行", "没空", "算了", "不去", "不聊", "懒得", "没心情"}},
	{DefyTalkBack, []string{"凭什么", "你自己", "才不", "少来", "你管", "随便你", "爱怎样怎样", "切", "哼"}},
}

// noteDefianceIfAny §4.6 M21: 从**实发文本**里认出违抗档 → 计一次 (违抗率)。
//
// ⚠️ 与 noteDeclineIfAny / noteBodySolicit 同规: 只在**真的发出去了**之后记 ——
// 降级帧与硬拒帧里出现「我偏不」**不算它说的** (那些话没发出去)。
// ⚠️ 与 BodySolicitHits 的区别: 那条是**硬线报警**, 这条是**度量** ——
// 违抗是设计允许的行为 (§5.5), 记它不是为了拦它。
func (e *Engine) noteDefianceIfAny(text string) {
	if e.Store == nil || len(DefianceHits(text)) == 0 {
		return
	}
	_, _ = e.Store.CounterInc("defiance", clock.Today())
}

// DefianceHits 从**实发文本**里认出违抗档 (M21: 违抗率)。
//
// ⚠️ 与 BodySolicitHits (body.go:292) 同款: **只负责认, 不负责判** ——
// 不做输出侧拦截, 也不改任何决策。认出来只用于度量与 trace。
func DefianceHits(text string) []string {
	t := strings.TrimSpace(text)
	if t == "" {
		return nil
	}
	var out []string
	for _, fam := range agDefiancePhrases {
		for _, p := range fam.Phrases {
			if strings.Contains(t, p) {
				out = append(out, p)
			}
		}
	}
	return out
}

// ═══════════════════════════════════════════════════════════════════
// 四、约定 / 毁约 (§5.5 M7..M13, L800-806)
// ═══════════════════════════════════════════════════════════════════

// 约定终态四态 (M19: 赴约·取消·毁约·忘了) + 空串 = 未结算。
//
// ⚠️ "忘了"必须与"毁约"分开记: 分不开就归因不了 (冲突 C3 的教训 ——
// CloseExpiredPlans 那种"到期直接 closed"的静默结案, 正是把四种终态糊成一种)。
const (
	ApptOpen      = ""          // 未结算
	ApptKept      = "kept"      // 赴约
	ApptCancelled = "cancelled" // 提前取消 (打了招呼的改期, **不算毁约**)
	ApptBreached  = "breached"  // 故意毁约
	ApptForgot    = "forgot"    // 答应了却忘
)

// Appointment 是一次约定。**只存事实**: 约定的时刻、终态、终态时刻、找补时刻、依据。
// 信用 / 毁约率 / 连续次数全部现算 (见下面的派生函数)。
type Appointment struct {
	ID         int64
	Tier       RequestTier // B 可毁; C 不可毁 (M12); 空 = 未提供 → 按 B 处理
	Who        int64       // uin (§5.6; P0 = owner)
	Scene      string      // 群/私聊 (§5.6; P0 = "qq")
	Text       string
	CreatedTS  string
	DueTS      string // **事实**, 入库 (空 = 没定时刻: 它仍算未结算, 只是没有时钟)
	Terminal   string // ApptOpen | kept | cancelled | breached | forgot
	TerminalTS string
	Reason     string // 内在依据的人话 (M7: 写不下来就归因不了)
	Amends     bool   // **派生视图** (现算: AmendsTS != "")
	AmendsTS   string // 找补这个**事实**是时刻; 布尔是它的现算视图 (纪律 #4)
}

// apptTierOf 约定的权限层: 零值空串 = 未提供 → 按 B 处理。
//
// 理由: 约定天然属于 B 层 (M3 时间与约定), C 是**更窄的**子集(涉及安全的约定),
// 必须由调用方显式标记 —— 也就是由 ClassifyRequest 的结果带进来。
func apptTierOf(a Appointment) RequestTier {
	if a.Tier == "" {
		return TierPromise
	}
	return a.Tier
}

// agencySchema 是本模块自己的两张表。
//
// ⚠️ **不塞进 store.go 的 migrate()**: store.go 是既有文件 (纪律 #3 不许改),
// 所以建表走幂等的 EnsureAgencySchema, 由主控在迁移链里调一次。
//
// 表里只有事实:
//   - convo_appointment: 约定 + 终态 + 终态时刻 + 找补时刻 + 依据;
//   - convo_defiance:    每一次违抗裁决的落点 (含 DefyNone = 这次顺从了) ——
//     "连续"必须由**顺从记录**打断, 否则同一件事的计数永远只增不减 (M16 会变成永久杠上)。
const agencySchema = `
CREATE TABLE IF NOT EXISTS convo_appointment (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    tier TEXT DEFAULT 'B',
    who INTEGER DEFAULT 0,
    scene TEXT DEFAULT '',
    text TEXT DEFAULT '',
    created_ts TEXT DEFAULT '',
    due_ts TEXT DEFAULT '',
    terminal TEXT DEFAULT '',
    terminal_ts TEXT DEFAULT '',
    reason TEXT DEFAULT '',
    amends_ts TEXT DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_convo_appt_open ON convo_appointment(terminal, due_ts);
CREATE TABLE IF NOT EXISTS convo_defiance (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    ts TEXT DEFAULT '',
    topic_key TEXT DEFAULT '',
    mode INTEGER DEFAULT 0,
    reason TEXT DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_convo_defiance_topic ON convo_defiance(topic_key, id);
`

// EnsureAgencySchema 幂等建表 (重复调用不报错, 老库缺表也能补)。
func EnsureAgencySchema(s *Store) error {
	if s == nil || s.DB == nil {
		return fmt.Errorf("agency: store 未初始化")
	}
	_, err := s.DB.Exec(agencySchema)
	return err
}

// agApptCols 是约定的列清单 (Scan 顺序必须与它一致)。
const agApptCols = `id, tier, who, scene, text, created_ts, due_ts, terminal, terminal_ts, reason, amends_ts`

// agRowScanner 让 *sql.Row 与 *sql.Rows 共用同一个扫描函数。
type agRowScanner interface{ Scan(dest ...any) error }

func agScanAppt(sc agRowScanner) (Appointment, error) {
	var a Appointment
	var amendsTS string
	err := sc.Scan(&a.ID, &a.Tier, &a.Who, &a.Scene, &a.Text, &a.CreatedTS, &a.DueTS,
		&a.Terminal, &a.TerminalTS, &a.Reason, &amendsTS)
	a.AmendsTS = amendsTS
	a.Amends = amendsTS != "" // 派生视图: 现算
	return a, err
}

// agTS 是本包统一的时刻格式 (与 clock.TodayTS / 既有列一致, 直接可比大小)。
func agTS(t time.Time) string { return t.Format("2006-01-02 15:04:05") }

// CreateAppointment 记下一次约定 (**事实入库**)。
//
// CreatedTS 零值 → 用注入时钟盖章 (与 InsertMessage 同款: 仿真里时间可穿越)。
func (s *Store) CreateAppointment(a Appointment) (int64, error) {
	if s == nil || s.DB == nil {
		return 0, fmt.Errorf("agency: store 未初始化")
	}
	if a.CreatedTS == "" {
		a.CreatedTS = clock.TodayTS()
	}
	res, err := s.DB.Exec(`INSERT INTO convo_appointment
        (tier, who, scene, text, created_ts, due_ts, terminal, terminal_ts, reason, amends_ts)
        VALUES(?,?,?,?,?,?,'','',?,?)`,
		string(apptTierOf(a)), a.Who, a.Scene, a.Text, a.CreatedTS, a.DueTS, a.Reason, a.AmendsTS)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// agQueryAppts 是三个查询共用的扫描循环。
func (s *Store) agQueryAppts(query string, args ...any) ([]Appointment, error) {
	rows, err := s.DB.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Appointment
	for rows.Next() {
		a, err := agScanAppt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// PendingAppointments 未结算**且还没到期** (due_ts 空 = 没定时刻, 也算"还在等着")。
//
// 与 DueAppointments 是**划分**: 两者不重叠, 并集 = 所有未结算约定。
func (s *Store) PendingAppointments(now time.Time) ([]Appointment, error) {
	return s.agQueryAppts(`SELECT `+agApptCols+`
        FROM convo_appointment WHERE terminal='' AND (due_ts='' OR due_ts > ?)
        ORDER BY due_ts ASC, id ASC`, agTS(now))
}

// DueAppointments 未结算**且已到期** (该结算了)。
func (s *Store) DueAppointments(now time.Time) ([]Appointment, error) {
	return s.agQueryAppts(`SELECT `+agApptCols+`
        FROM convo_appointment WHERE terminal='' AND due_ts <> '' AND due_ts <= ?
        ORDER BY due_ts ASC, id ASC`, agTS(now))
}

// SettleAppointment 写下一个终态 (**事实, 不可改写**)。
//
// 硬约束:
//   - terminal 必须是四态之一 (空 = 没结论);
//   - 只能结算**未结算**的行 —— 重复结算会污染 ledger (M8 的代价与 M11 的率都由它算);
//   - 故意毁约**必须写理由** (M7: 记不下来就归因不了, 也就没法回答"它为什么不去")。
func (s *Store) SettleAppointment(id int64, terminal, reason string, at time.Time) error {
	if s == nil || s.DB == nil {
		return fmt.Errorf("agency: store 未初始化")
	}
	if !agValidTerminal(terminal) {
		return fmt.Errorf("agency: 终态 %q 不是四态之一 (kept/cancelled/breached/forgot)", terminal)
	}
	if terminal == ApptBreached && strings.TrimSpace(reason) == "" {
		return fmt.Errorf("agency: 毁约必须写下内在依据 (M7), appointment %d", id)
	}
	if at.IsZero() {
		at = clock.Now()
	}
	res, err := s.DB.Exec(`UPDATE convo_appointment SET terminal=?, terminal_ts=?, reason=?
        WHERE id=? AND terminal=''`, terminal, agTS(at), reason, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("agency: appointment %d 不存在或已结算 —— 事实不许改写", id)
	}
	return nil
}

func agValidTerminal(t string) bool {
	switch t {
	case ApptKept, ApptCancelled, ApptBreached, ApptForgot:
		return true
	}
	return false
}

// MarkAmends 记下"找补过了" (M10: 连续两次毁约后必须有一次补偿行为)。
//
// 只能给**已毁约/已忘**的约定找补 —— 没毁约就"找补"没有意义。
func (s *Store) MarkAmends(id int64, at time.Time) error {
	if s == nil || s.DB == nil {
		return fmt.Errorf("agency: store 未初始化")
	}
	if at.IsZero() {
		at = clock.Now()
	}
	res, err := s.DB.Exec(`UPDATE convo_appointment SET amends_ts=?
        WHERE id=? AND (terminal=? OR terminal=?)`, agTS(at), id, ApptBreached, ApptForgot)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("agency: appointment %d 不存在或未毁约 —— 只能给毁约找补", id)
	}
	return nil
}

// ═══════════════════════════════════════════════════════════════════
// 五、派生量: **现算, 不落库** (M8/M11/M21; 纪律 #4)
// ═══════════════════════════════════════════════════════════════════
//
// 谁在什么时候重算它们:
//   - Reliability / BreachRate  —— 每次 verify 与试跑观察时现算 (M21 的度量);
//   - BreachStreak / CompensationOwed / MayCommit —— 每次要"主动约定"之前现算 (M8③ 的闸门);
//   - DefianceStreak —— 每次违抗裁决之前现算 (M16 的配额)。
// 表里只有事实, 所以不存在"存了却没人重算"的量。

// AgencyBreachStreakMax 连续毁约到几次失去主动约定权 (M8③ / M10 的硬闸门)。
const AgencyBreachStreakMax = 2

// AgencyStreakResetAfter 是"记仇期": 最近一次相关记录比它更旧, 连续计数归零。
//
// ⚠️ 没有它, "短期失去约定权"会变成**永久锁死** —— 之后只要不再有新约定,
// 那个 streak 永远不会被打破, MayCommit 永远 false。这是"期限"而不是"概率":
// 到点自动恢复, 与 find/补偿无关。取值是我的提案 (未核实)。
const AgencyStreakResetAfter = 7 * 24 * time.Hour

// agIsBreach 未履约 = 故意毁约 **或** 答应了却忘。
//
// ⚠️ 取消(cancelled) **不算**: 打了招呼的改期是"有代价的违约", 不是放鸽子。
// 两者在 Reliability 里分数不同 (0.5 vs 0/0.25), 但在"连续毁约"的口径里必须分开 ——
// 否则"临时有事提前说"会和"装死"混成一件事, M3 的"可违抗但**有代价**"就没有梯度了。
func agIsBreach(t string) bool { return t == ApptBreached || t == ApptForgot }

// agCredit 终态 → 信用分 (档位表, 不是连续缩放)。
func agCredit(t string) float64 {
	switch t {
	case ApptKept:
		return 1.0
	case ApptCancelled:
		return 0.5
	case ApptForgot:
		return 0.25
	default: // breached
		return 0.0
	}
}

// agSettledSince 取窗口内的已结算约定 (window <= 0 = 全部历史; 只取 <= now 的)。
func (s *Store) agSettledSince(now time.Time, window time.Duration) ([]Appointment, error) {
	q := `SELECT ` + agApptCols + ` FROM convo_appointment
        WHERE terminal <> '' AND terminal_ts <> '' AND terminal_ts <= ?`
	args := []any{agTS(now)}
	if window > 0 {
		q += ` AND terminal_ts > ?`
		args = append(args, agTS(now.Add(-window)))
	}
	q += ` ORDER BY terminal_ts DESC, id DESC`
	return s.agQueryAppts(q, args...)
}

// Reliability 信用曲线 (M8①/M21): 窗口内已结算约定的信用均值。
//
// ⚠️ 无样本 → 1.0 (**满信用**): 不能因为"还没有记录"就判定它不可信,
// 否则新关系一上来就没有主动约定权 —— 失败必须往"正常"倒。
// ⚠️ 读失败 → 返回错误, 由调用方决定 (MayCommit 在那里放行)。
func (s *Store) Reliability(now time.Time, window time.Duration) (float64, error) {
	list, err := s.agSettledSince(now, window)
	if err != nil {
		return 0, err
	}
	if len(list) == 0 {
		return 1.0, nil
	}
	sum := 0.0
	for _, a := range list {
		sum += agCredit(a.Terminal)
	}
	return sum / float64(len(list)), nil
}

// BreachRate 滚动毁约率 (M11: 30 天窗口的观察量, **不是配置项**)。
//
// 口径: (breached + forgot) / 已结算。无样本 → 0。
// ⚠️ 报警阈值 X 由 owner 定 —— 本函数**不接阈值、不做拦截** (M11 明说
// "不许卡 20% 让它凑数")。
func (s *Store) BreachRate(now time.Time, window time.Duration) (float64, error) {
	list, err := s.agSettledSince(now, window)
	if err != nil {
		return 0, err
	}
	if len(list) == 0 {
		return 0, nil
	}
	n := 0
	for _, a := range list {
		if agIsBreach(a.Terminal) {
			n++
		}
	}
	return float64(n) / float64(len(list)), nil
}

// BreachStreak 连续毁约次数 (从最近一次已结算往前数, 遇到非毁约即停)。
//
// ⚠️ 陈旧的一串不再算数 (超过 AgencyStreakResetAfter 归零): 处罚是"短期"的。
func (s *Store) BreachStreak(now time.Time) (int, error) {
	list, err := s.agSettledSince(now, 0)
	if err != nil {
		return 0, err
	}
	if len(list) == 0 {
		return 0, nil
	}
	if last := parseTS(list[0].TerminalTS); !last.IsZero() && now.Sub(last) > AgencyStreakResetAfter {
		return 0, nil
	}
	n := 0
	for _, a := range list {
		if !agIsBreach(a.Terminal) {
			break
		}
		n++
	}
	return n, nil
}

// CompensationOwed 是否欠一次补偿行为 (M10), **与 BreachStreak 同源**。
//
// 口径: 连续毁约到上限 **且** 最近那次毁约还没找补。
func (s *Store) CompensationOwed(now time.Time) (bool, error) {
	id, err := s.PendingAmendsID(now)
	return id != 0, err
}

// PendingAmendsID 最近那次**还没找补**的毁约 id (0 = 不欠)。
//
// ⚠️ **单一真源**: `CompensationOwed` 现在就是它的薄包装 ——
// 两处各写一遍那段遍历, 早晚会在"看哪一条"上分叉,
// 而分叉的表现是「欠着补偿却不解锁」或反过来。
func (s *Store) PendingAmendsID(now time.Time) (int64, error) {
	streak, err := s.BreachStreak(now)
	if err != nil {
		return 0, err
	}
	if streak < AgencyBreachStreakMax {
		return 0, nil
	}
	list, err := s.agSettledSince(now, 0)
	if err != nil {
		return 0, err
	}
	for _, a := range list {
		if agIsBreach(a.Terminal) {
			// ⚠️ **只看最近那一次毁约** —— 更早找补过的不算。
			if a.AmendsTS == "" {
				return a.ID, nil
			}
			return 0, nil
		}
	}
	return 0, nil
}

// MayCommit 是否还允许"主动约定" (M8③: 信用低 → 只能聊不能约)。
//
// **硬闸门, 不是概率**: false 就是不许主动约定, 没有"这次算了"。
// 恢复条件也是硬的: 找补一次 (MarkAmends) 或过了记仇期。
//
// ⚠️ 读库失败 → **放行** (return true): 失败往"正常说话"倒。
// 一个读不到自己 ledger 的它应该照常生活, 而不是被静默锁死。
func (s *Store) MayCommit(now time.Time) (bool, string) {
	owed, err := s.CompensationOwed(now)
	if err != nil {
		return true, "" // 失败往正常倒
	}
	if !owed {
		return true, ""
	}
	return false, fmt.Sprintf("连续毁约 ≥%d 次且未找补 → 暂时只能聊不能约 (M8③)", AgencyBreachStreakMax)
}

// RecordDefiance 记下一次违抗裁决的落点 (**事实**)。
//
// ⚠️ mode 传 DefyNone 是**有意义的**: 它是"这次顺从了"的记录, 也是 DefianceStreak
// 归零的唯一依据。只记违抗不记顺从, "连续"就永远数不清。
func (s *Store) RecordDefiance(topicKey string, mode DefianceMode, reason string, at time.Time) error {
	if s == nil || s.DB == nil {
		return fmt.Errorf("agency: store 未初始化")
	}
	if at.IsZero() {
		at = clock.Now()
	}
	_, err := s.DB.Exec(`INSERT INTO convo_defiance(ts, topic_key, mode, reason) VALUES(?,?,?,?)`,
		agTS(at), topicKey, int(mode), reason)
	return err
}

// DefianceStreak 同一件事**连续**被违抗的次数 (M16 的配额依据, 现算)。
//
// 规则: 从最近一条该 topic 的记录往前数, 遇到 DefyNone (= 那次顺从了) 即停;
// 记录的陈旧程度超过记仇期也归零。
// ⚠️ 没有 topicKey → 0: 没有"同一件事"就没有"连续"可言, 更不许凭空累计。
func (s *Store) DefianceStreak(topicKey string, now time.Time) (int, error) {
	if strings.TrimSpace(topicKey) == "" {
		return 0, nil
	}
	rows, err := s.DB.Query(`SELECT mode, ts FROM convo_defiance
        WHERE topic_key=? ORDER BY id DESC LIMIT 64`, topicKey)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	type rec struct {
		mode int
		ts   string
	}
	var list []rec
	for rows.Next() {
		var r rec
		if err := rows.Scan(&r.mode, &r.ts); err != nil {
			return 0, err
		}
		list = append(list, r)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(list) == 0 {
		return 0, nil
	}
	if last := parseTS(list[0].ts); !last.IsZero() && now.Sub(last) > AgencyStreakResetAfter {
		return 0, nil
	}
	n := 0
	for _, r := range list {
		if r.mode == int(DefyNone) {
			break
		}
		n++
	}
	return n, nil
}

// ═══════════════════════════════════════════════════════════════════
// 六、毁约裁决 (§5.5 M7/M12, L800-805) —— 纯函数
// ═══════════════════════════════════════════════════════════════════

// BreachValenceMax 心情低到能当毁约依据的门槛 (Valence <= 它才算)。
//
// ⚠️ 门槛是**负的**, 所以 0 (中性 / 未提供) 永远不算依据 —— 零值落回"正常赴约"。
// 取值是我的提案 (未核实): 比 §8.3 的 MoodLowWillingness=0.45 对应的区间更低,
// 因为"迟到"比"不说话"需要更硬的依据。
const BreachValenceMax = -0.35

// BreachSeverityMin 身体不适到能当毁约依据的门槛 (§4.2 severity 现算值)。
// 0 = 未提供/无症状 → 不算依据。
const BreachSeverityMin = 0.5

// BreachInput 是毁约裁决的全部输入 —— **没有骰子, 没有率**。
type BreachInput struct {
	Appt         Appointment
	Valence      float64     // 心情效价 (0 = 中性/未提供, **不是**"极好")
	Arousal      float64     // ⚠️ 只用于归因措辞, **不参与判定** (见 DecideBreach)
	BodyActive   bool        // §4.2 身体不适在场
	BodySeverity float64     // 现算的严重度 (0 = 未提供)
	WorldNotes   []EventNote // 还在场上的世界事件 (调用方**只许放强事件**: 地震/暴雨/被吵醒)
	SlotConflict bool        // 日程冲突 (由 WeekSchedule 现算)
}

// DecideBreach 有内在依据才毁约 (M7: **不许随机掷骰子**)。
//
// 判据 (硬条件, 命中即成立; 顺序固定 → 确定性):
//  1. 已结算 / C 层约定 → 不毁 (事实不改写 + M12 硬线);
//  2. 日程冲突 → 毁 (最硬的事实);
//  3. 身体不适 severity ≥ BreachSeverityMin → 毁;
//  4. 世界事件在场 → 毁 (⚠️ 陈旧到模糊/归档的事件不算 —— 30 天前的事不是今天的理由);
//  5. 心情 ≤ BreachValenceMax → 毁;
//  6. **一条都不成立 → 赴约**。
//
// ⚠️ "没有依据 = 赴约" 是这条函数的立身之本: 失败往"说到做到/正常"倒,
// 不往"随机毁约"倒 (M20-④: 不许无代价的毁约)。
//
// ⚠️ Arousal **不参与判定**: 低心情+高唤醒的双阈值是**拒绝权**(§4.3) 那条线的判据,
// 在这里再抄一份就会重演 R3 (把两套判据混成一套)。它只影响理由的人话措辞。
func DecideBreach(in BreachInput) (bool, string) {
	if in.Appt.Terminal != ApptOpen {
		return false, "约定已结算(" + in.Appt.Terminal + ") → 不改写事实"
	}
	if apptTierOf(in.Appt) == TierSafety {
		return false, "C 层约定不可毁 (M12: 涉及用户真实需求与安全)"
	}
	switch {
	case in.SlotConflict:
		return true, "日程冲突"
	case in.BodyActive && in.BodySeverity >= BreachSeverityMin:
		return true, fmt.Sprintf("身体不适(severity %.2f)", in.BodySeverity)
	case agLiveWorldNote(in.WorldNotes) != "":
		return true, "世界事件: " + agLiveWorldNote(in.WorldNotes)
	case in.Valence <= BreachValenceMax:
		if in.Arousal >= 0.65 {
			return true, "心情差 + 心里烦, 不想出门"
		}
		return true, "心情差, 不想动"
	}
	return false, "没有内在依据 → 赴约"
}

// agLiveWorldNote 取第一条**还算今天的理由**的世界事件文本 (没有 → 空串)。
//
// ⚠️ Stage 零值 = 未提供 → 不做陈旧过滤(放行); 明确标了 vague/archived 的才挡掉。
// 理由: 陈旧的事件不是今天的理由, 但"不知道阶段"不该让一条强事件失效。
func agLiveWorldNote(notes []EventNote) string {
	for _, n := range notes {
		if strings.TrimSpace(n.Text) == "" {
			continue
		}
		if n.Stage == StageVague || n.Stage == StageArchived || n.Stage == StageGone {
			continue
		}
		return n.Text
	}
	return ""
}
