package convo

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/tzhnahida/convo/internal/clock"

	"github.com/tzhnahida/convo/internal/log"
)

// ——— §6.3 沉默判据: 三态裁决 ———
//
// 设计稿 L896: **三态都是正常态** —— 沉默 / 表情反应 / 发言。
// 群里多数互动不产生文字; "约九成判否" (L900) 是**群聊形态的成本前提**, 不是优化项。
//
// ⚠️ 三条地基 (来自规格调研, 每条都改变了这一层的形状):
//
//	① **§6.3 是「群形态」的判据表, 不是通用判据表。**
//	   L900 把"约九成判否"明写在群聊形态下; 判据②(被@)、角色一致性(§6.6)、群白名单
//	   三个信号都只在群里成立。→ **私聊沿用今天的"一律接"**, 只借用它的失败倒向边界。
//
//	② **IsAddressedToMe 是「资格」闸门, 这里要造的是「价值」闸门。**
//	   scene.go 回答"这句话算不算对你说的话"; 这里回答"值不值得为它开一次口"。
//	   两者是**合取**: 能接 = IsAddressedToMe ∧ 值得接。
//
//	③ **§6.1 的第二态(表情反应)通路早已通了** —— sendFrame 显式支持纯表情帧
//	   (convo.go 的"纯表情帧丢掉 = 整条消息凭空消失")。第二态**零新增通路**,
//	   只需要一个系统侧的构造器。而它恰好是本层唯一的空缺。

// SpeakStance §6.1 的三态。
//
// ⚠️ 名字里带 Speak 是为了与 agency.go 的 Stance(**立场**: 它喜不喜欢某件事)分开 ——
// 两者都是"它对这个话题的态度", 但一个决定**接不接**, 一个决定**怎么拒绝**, 混用必出事。
type SpeakStance string

const (
	StanceSilent SpeakStance = "silent" // 第一态: 不产生任何消息 —— **0 次 LLM 调用**
	StanceReact  SpeakStance = "react"  // 第二态: 纯表情     —— **0 次 LLM 调用**
	StanceSpeak  SpeakStance = "speak"  // 第三态: 走生成器   —— 1 次 LLM 调用
)

// ValueInput 五条廉价判据的全部输入。
//
// 与 TickInput / HeatQuotaInput / ConflictContext 同构: **引擎侧组装, 纯函数消费**。
// 决策器不读库、不掷骰、不接 LLM、不看时钟 (纪律 #1)。
//
// ⚠️ 每个"可能缺席"的判据都配一个 Has* —— 缺席与"不命中"必须分得开:
// 缺席 = 这个判据还没建/没数据 → **不进分母**; 不命中 = 判据跑完了, 结论是没有。
// 混起来的后果是"因为一个还没建的功能, 四条门槛变成事实上的永不发言" ——
// 那是一个安静的默认值 bug (与 HasImpulse 同一条)。
type ValueInput struct {
	Scene string // "group:<gid>" | "private:<uin>"; **零值按群处理** (见 DecideValue)

	// —— 硬门槛的数据 (引擎读好填进来) ——
	SceneAllowed bool // 白名单; false → 连读 IO 都不该发生

	// —— 判据① 品味 ——
	TasteHits int
	HasTaste  bool

	// —— 判据② 被 @ / 点名 (来自 scene.go, **不许重算**) ——
	Addressed bool

	// —— 判据③ 冲突 ——
	Conflict    bool
	HasConflict bool
	// ConflictWindow 判据③ 实际看到的窗口大小 (**读数, 不进判据**)。
	//
	// ⚠️ 它不是"锦上添花的日志": 没有它, "判据③ 只看了本场景"这条契约
	// **根本断言不了** —— 冲突判定本身有很多信号门槛, 构造一个"换个场景就会误判"的
	// 窗口非常难; 而"窗口里到底有几条"是直接的。
	ConflictWindow int

	// —— 判据④ 冷场 ——
	Gap         time.Duration
	HasGap      bool // false = 这个场景还没有过上一条消息 (**新场景不是冷场**)
	GapValid    bool // 引擎侧已按 ColdGapMin/MaxHours 判过 (纯函数不碰时钟)
	ColdCeiling bool

	// —— 判据⑤ 梗库 ——
	SlangHits int

	// —— 判据⑥ 角色一致性 (L908) ——
	RoleFit bool

	// —— §6.8 偏向 (§5.1 关系量算出来的 Δ) ——
	//
	// ⚠️ HasBias 零值 = **未提供 → 不加不减**。
	// 少了它, 忘了填的调用方会被读成「Δ=0」—— 表面上没事, 但也就永远发现不了
	// 「偏向根本没接上」这件事 (ValueWithBias 曾经整整一批只活在测试里)。
	HasBias    bool
	BiasDelta  int    // ∈ {-1,0,+1}
	BiasFloor  bool   // 保底回应率救回
	BiasReason string // 归因
}

// ValueHits 五条判据的命中明细。
//
// 它同时是**读数** —— L910「让'不说'可解释、可回归」:
// "为什么这次沉默"必须能只从这几个布尔量里读出来。
type ValueHits struct {
	Taste     bool
	Addressed bool
	Conflict  bool
	Cold      bool
	Slang     bool

	Count   int      // 0..5
	Reasons []string // 人话, 进 DecisionTrace
}

// ValueDecision 三态裁决的结果。
type ValueDecision struct {
	Stance SpeakStance
	Hits   int
	Need   int    // 本轮实际要求的发言阈值 (可能因缺席判据被折算)
	Reason string // 归因
}

// ValueConfig 档位表 + 硬门槛常量。**整表导出**, 照抄 BodyEffectTable 的做法。
type ValueConfig struct {
	SpeakMinHits int // 四条以上 (L907); <=0 → 4
	ReactMinHits int // 1..3 → 表情;      <=0 → 1
}

// ValueConfigFrom 从既有 GroupConfig 派生 —— **不新开一份配置树**。
func ValueConfigFrom(g GroupConfig) ValueConfig {
	return ValueConfig{SpeakMinHits: 4, ReactMinHits: 1}
}

// HitValue 数一遍五条判据。**缺席的不进 Count** (见 ValueInput 的注释)。
func HitValue(in ValueInput) ValueHits {
	var h ValueHits
	if in.HasTaste && in.TasteHits > 0 {
		h.Taste = true
		h.Reasons = append(h.Reasons, "品味命中")
	}
	// ⚠️ 判据② 与硬门槛「被 @ 必答」**是同一个信号**, 这里**只计一次**。
	// 两处都算的话, 四条门槛会被一条信号顶掉两条 —— "四条以上"形同虚设。
	if in.Addressed {
		h.Addressed = true
		h.Reasons = append(h.Reasons, "被点名")
	}
	if in.HasConflict && in.Conflict {
		h.Conflict = true
		h.Reasons = append(h.Reasons, "确实在吵")
	}
	// ⚠️ 判据④ 有两个必须写死的边界:
	//   · 没有上一条消息 = **新场景**, 不是冷场 (反过来会把新群第一条判成"该救场");
	//   · 超过上限 = **这个群死了**, 不是冷场 (对它出手是"主动找陌生人说话"的反面)。
	if in.HasGap && in.GapValid && !in.ColdCeiling {
		h.Cold = true
		h.Reasons = append(h.Reasons, "冷场")
	}
	if in.SlangHits > 0 {
		h.Slang = true
		h.Reasons = append(h.Reasons, "梗库命中")
	}
	for _, b := range []bool{h.Taste, h.Addressed, h.Conflict, h.Cold, h.Slang} {
		if b {
			h.Count++
		}
	}
	// 判据⑥ 只在**临界点**生效: hits==3 且有角色依据 → 抬到 4。
	// ⚠️ 它**不进五条计数** —— 加进去会让门槛从 4 悄悄降到 3.x,
	// 那是"已有的契约被一条新判据改写"。
	if in.RoleFit && h.Count == 3 {
		h.Count = 4
		h.Reasons = append(h.Reasons, "角色能给这个发言加值(临界抬一档)")
	}
	return h
}

// AvailableCriteria 本轮**实际可用**的判据数 (缺席的不算)。
func AvailableCriteria(in ValueInput) int {
	n := 0
	for _, ok := range []bool{in.HasTaste, true /*② 恒可用*/, in.HasConflict, in.HasGap, true /*⑤ 恒可用*/} {
		if ok {
			n++
		}
	}
	return n
}

// DecideValue §6.3 的核心裁决。**纯函数**。
//
// 落点纪律 (本项目实测过两次的教训):
// **hits 落在档位上 (0 / 1-3 / >=4), 不许乘到任何概率上。**
//
// 三态与设计稿的两条边界严丝合缝:
//
//	hits == 0      → 沉默      (L907「全不命中 → 沉默」)
//	1 <= hits <= 3 → 表情反应  (**本层填的空** —— L907 只给了两条边界, 中间无规定;
//	                            而 §6.1 的三态本来就写着中间那一态, 代码里它一直空着)
//	hits >= 4      → 发言      (L907「四条以上命中 → 值得接」)
//
// ⚠️ **私聊不做价值判断** (地基①): L900 的"约九成判否"限定在群聊形态。
// 私聊只有一个主体, 判据②④在这里没有第三方含义。私聊走"一律接"。
// ⚠️ **Scene 零值按群处理** —— 与 effectiveMultiplier 同规:
// 漏传 scene 不该变成"给私聊开绿灯", 而该变成"按更严的那一侧"。
func DecideValue(in ValueInput, hits ValueHits, cfg ValueConfig) ValueDecision {
	speakMin := cfg.SpeakMinHits
	if speakMin <= 0 {
		speakMin = 4
	}

	// 私聊: 一律接, 不做价值判断。
	if SceneKindOf(in.Scene) == SceneKindPrivate {
		return ValueDecision{Stance: StanceSpeak, Hits: hits.Count, Need: 0,
			Reason: "私聊不做价值判断 (§6.3 是群形态的判据表)"}
	}

	need := ValueNeed(in, speakMin)

	// ⚠️ §6.8 的偏向在这里落到**分数**上, 而且**必须走 ValueWithBias** ——
	// 分档逻辑只许有一份实现。曾经 ValueWithBias 是第二份, 结果它只在测试里活着。
	bias := BiasDecision{}
	if in.HasBias {
		bias = BiasDecision{Delta: in.BiasDelta, Floor: in.BiasFloor, Reason: in.BiasReason}
	}
	scored := ValueWithBias(hits, bias, need, cfg)
	suffix := biasReasonSuffix(bias)

	switch scored.Stance {
	case StanceSpeak:
		return ValueDecision{Stance: StanceSpeak, Hits: hits.Count, Need: need,
			Reason: "命中 " + itoa(hits.Count) + " 条 (门槛 " + itoa(need) + ")" + suffix}
	case StanceReact:
		return ValueDecision{Stance: StanceReact, Hits: hits.Count, Need: need,
			Reason: "命中 " + itoa(hits.Count) + " 条 — 够表情, 不够开口" + suffix}
	default:
		return ValueDecision{Stance: StanceSilent, Hits: hits.Count, Need: need,
			Reason: "五条判据全不命中 — 这是结论, 不是故障" + suffix}
	}
}

// ValueNeed 门槛的**折算**: 缺席判据不进分母, 但下限 2 不许破 ——
// 否则"只有一条判据可用"时会退化成"命中就发言"。
func ValueNeed(in ValueInput, speakMin int) int {
	if speakMin <= 0 {
		speakMin = 4
	}
	avail := AvailableCriteria(in)
	if avail < 5 && avail > 0 {
		need := (speakMin*avail + 4) / 5
		if need < 2 {
			return 2
		}
		return need
	}
	return speakMin
}

func reactMinOf(cfg ValueConfig) int {
	if cfg.ReactMinHits <= 0 {
		return 1
	}
	return cfg.ReactMinHits
}

// itoa 十进制整数转字符串。
//
// ⚠️ **2026-10-15 修: 老实现用一个 `var b [8]byte` 从后往前填 —— 超过 8 位就下标越界 panic,**
// 而它每天经手的正是**QQ 号**(最多 10 位)。也就是说: 名册里放一个 9 位以上的 QQ 号,
// 夜间批会直接崩在 `night:dm:<uin>` 这一行上, 而症状只是"某天夜里没跑"。
//
// 同一天它还以另一种方式咬人: 世界观配角的场景键改用负数 UIN(区分真人)后,
// 负号又多吃一位 —— 于是**连正数都不够用的时候它也没撑住**。
//
// 结论: 手写数字转换在这里没有任何收益, 直接交给 strconv。**能用标准库的别自己写。**
func itoa(n int) string {
	return strconv.Itoa(n)
}

// ValueStanceOf 把 §6.8 的 Willingness 档位映射成三态。
//
// ⚠️ **这是唯一的档位映射口** —— 不许在别处再写一张阈值表。
// 两张表迟早在边界上打架, 而那时"为什么这次不说话"就归因不了了。
//
//	Willingness 0 → 沉默 · 1 → 表情 · >=2 → 发言
func ValueStanceOf(willingness int) SpeakStance {
	switch {
	case willingness <= 0:
		return StanceSilent
	case willingness == 1:
		return StanceReact
	default:
		return StanceSpeak
	}
}

// ——— 引擎侧: 完整裁决链 ———

// GroupGateResult 一条群消息的裁决结果。
//
// 它同时是**读数**: L910「让'不说'可解释、可回归」——
// "为什么这次沉默"必须能只从这个结构里读出来。
type GroupGateResult struct {
	Stance       SpeakStance
	Addressed    bool
	AddressedWhy string // scene.go 的稳定理由串
	Value        ValueDecision
	Reason       string
	// Chimed / ChimeTopic 这一轮是不是**插话** (而不是被叫/主动)。
	// ⚠️ 调用方据此记账 —— 不记的话配额永远用不掉, 它会一直插。
	Chimed     bool
	ChimeTopic bool
}

// GateGroupMessage §6.10 + §6.3 的完整裁决链。**顺序不许换** (规格 §6.1)。
//
//  0. 白名单           → 丢弃 (在**任何 IO 之前** —— 这是"九成判否"作为成本前提的落法)
//  1. 资格闸门 IsAddressedToMe (纯函数, 零 IO):
//     @我              → **发言, 短路跳过价值判断** (L906 硬门槛「被 @ 必答」)
//     @别人            → **丢弃, 连表情都不发** (L1026 ②)
//     判不出来          → 丢弃 (L1026 ④)
//     故障 (读库失败等)  → **发言, 跳过价值判断** (纪律: 故障往说话倒)
//     其余 (被点名/线程) → 继续
//  2. 价值判断 → 三态
//
// ⚠️ **否决权先于计分权** —— 三条理由:
//
//	① 设计稿的表格顺序 (L906 硬门槛在 L907 价值判断**之上**);
//	② A @ B 的消息就算品味/梗/冲突全中也不许接 —— 否决是二值的, 计分是多值的;
//	③ 资格闸门是**纯函数零 IO**, 先跑能省掉后面全部 IO。
//
// ⚠️ **故障与"判不出来"必须分开** (规格 V5, 本清单里最容易写错的一条):
//
//	用"判不出来 → 沉默"顺手把读库失败也吞掉, 就是纪律的反面 ——
//	一次读库失败会让它在群里**彻底闭嘴**, 而没人能从表现上看出那是故障。
func (e *Engine) GateGroupMessage(sc SceneMsg, self SelfIdentity, cfg GroupConfig) (out GroupGateResult) {
	// §6.7 **热力入账**: 场景里**任何**一条消息都算一次互动 —— 包括它不打算回的那些。
	//
	// ⚠️ 这是"热力会涨"的唯一来源。它不接, heatNow 永远返回 0 →
	// HeatQuota(0) 返回 0 → **它再也不会主动开口**, 而表现只是"它变安静了"。
	//
	// ⚠️ 放在这里而不是"裁决通过之后": 沉默档也是互动 ——
	// 群里聊得热火朝天而它一直在看, 那也是它参与了那个场景。
	e.NoteSceneHeat(sc.SceneKey, sc.UIN, sc.TS)
	// §7.4 **遗忘权**: 群里**任何人**说"别记我" (设计稿 L1582「任何人说」)。
	//
	// ⚠️ 主体是**说话人那个 uin**, 不是群 —— 遗忘权是**人的**权利,
	// 而 `ForgetSubject` 删的是关于这个人的东西(`convo_memory_person` 按 uin 全覆盖)。
	//
	// ⚠️ 而它**不参与裁决**: 就算这一轮它决定沉默, 遗忘权也已经执行了 ——
	// 挂在裁决之后的话, 沉默那条路会让用户的请求整条失效。
	e.noteForgetRequest(sc.UIN, sc.Text)
	// §12 三态埋点: **每条群消息都记一次**, 沉默也记。
	// 只记"说话"的那些, 沉默率就永远是 100%。
	defer func() {
		noteStance(out.Stance)
		// 读数搬运: 让 SpeakInGroup 能把 reason / Δ 落到轨迹里。
		e.ensureGate()
		e.rememberGate(sc.SceneKey, out)
		e.recordGateOutcome(sc, out)
		e.noteRoleOutcome(sc, out)
		// 插话记账: **只在真的插了才记** —— 判了但没插不该消耗配额。
		if out.Chimed {
			if err := e.Store.BumpChime(e.ownerKey(), sc.SceneKey, out.ChimeTopic); err != nil {
				log.Warn("convo:chime", "插话计数失败 (下次可能多插一次): %v", err)
			}
		}
	}()
	// 0a. ⚠️ **空的 scene_key 是「未提供」, 不是「不在白名单」** ——
	// 前者是**故障** (往说话倒), 后者是**结论** (丢弃)。这两件事混起来的话,
	// "调用方漏传 scene" 这个编码错误会表现成"它在群里忽然不说话",
	// 而没人能从表现上看出那是 bug —— 与 scene.go 的 *_speak 理由同一条约定。
	//
	// 判据用的是**返回值形状**而不是语义: 空串 = 没给, 非空但不在白名单 = 给了且不许。
	if strings.TrimSpace(sc.SceneKey) == "" {
		return GroupGateResult{Stance: StanceSpeak,
			Reason: "scene_key 未提供 — 这是故障, 按「故障往说话倒」处理"}
	}
	// 0b. 白名单: fail-closed, 且在**任何 IO 之前**。
	if !SceneAllowed(cfg, sc.SceneKey) {
		return GroupGateResult{Stance: StanceSilent, Reason: "不在白名单 / 群聊未启用"}
	}

	// §6.10 **场景与成员的写入口**。
	//
	// ⚠️ 它们原来**只有读侧**: `convo.go` 的 SelfRoleLine 段与 `conflictwire.go` 的
	// stage 一直在读一张空表, 而 `IsNewMember` 恒 true(所有人永远是新人)。
	// 那种失效**棘轮扫不到** —— 代码看起来完全正常, 只是没人往里写。
	//
	// ⚠️ 位置在**白名单之后**是有意的: 不服务的群不该在表里留下任何痕迹。
	// ⚠️ 用的是 `EnsureScene`(只插入不覆盖)而不是 `SaveScene`(全量 upsert) ——
	// 这条路**每一轮**都要走, 用全量 upsert 会把 stage/self_role 每轮抹回默认。
	if err := e.Store.EnsureScene(sc.SceneKey); err != nil {
		log.Warn("convo:scene", "场景登记失败(那条注入段会读到空): %v", err)
	}
	if uin := strings.TrimSpace(sc.UIN); uin != "" {
		// ⚠️ 名字从 `sc.SenderName` 来(群名片优先, 见 OneBotSender.DisplayName)。
		// 空串是"**未提供**" —— `ObserveMember` 不会拿它覆盖既有昵称。
		if _, err := e.Store.ObserveMember(sc.SceneKey, uin, sc.SenderName, ""); err != nil {
			log.Warn("convo:scene", "成员登记失败(新人判定会一直为真): %v", err)
		}
	}

	// §6.13 **被叫停** (2026-10-21 实测: 群里说"闭嘴", 它回"？说谁呢", 然后继续刷)。
	// 位置在资格闸门之前: 叫停与"这句话对谁说"无关, 也不该走价值判断。
	// ⚠️ 不解释 —— 解释也是刷屏。
	if IsStopRequest(sc.Text) {
		// 叫停通常就是"它话太多"的抱怨 —— 让插话安全阀也记一笔 (§6.10.5 的 1% 收敛),
		// 否则「别刷屏」这条既有安全阀会被叫停短路掉、永远不落库。
		e.noteChimeComplaint(sc, hasAtSelf(sc.AtUINs, self.UIN))
		until, reason := e.noteStopRequest(sc)
		return GroupGateResult{Stance: StanceSilent,
			Reason: "被叫停 — 静默到 " + until.Format("15:04") + " (" + reason + ")"}
	}
	// 静默期内: 不接、不插话、不发表情。例外只有两个:
	//   · @ 它 = 明确叫它 → 解除静默 (用户想找它了);
	//   · 危机词 → 仍然必答 (与 §6.3 同一条边界)。
	if e.Store != nil {
		if until, reason, muted := e.Store.SceneMute(sc.SceneKey, clock.Now()); muted {
			if hasAtSelf(sc.AtUINs, self.UIN) || CrisisHit(sc.Text) {
				_ = e.Store.ClearSceneMute(sc.SceneKey)
			} else {
				return GroupGateResult{Stance: StanceSilent,
					Reason: "被叫停中 (" + reason + ") — 静默到 " + until.Format("15:04")}
			}
		}
	}

	// 同一句连发 (2026-10-21): 同一条数列题 4 分钟内被 @ 了 4 次, 它答了 4 遍。
	// 长句重复 = 同一道题, 只答一次; 短句重复是聊天常态, 不拦。
	if e.isRepeatedQuestion(sc) {
		return GroupGateResult{Stance: StanceSilent, Reason: "同一句重复 — 刚刚已经答过"}
	}

	// 1. 资格闸门。
	//
	// ⚠️ 有库时走 `ResolveAddressee`(读库版), 没库时才退回纯函数。
	//
	// 差别只有一处, 而那一处是规格 §6.1 专门钉死的:
	//   · **语义判不出来** → `undecidable` → 沉默;
	//   · **系统故障**(读库失败 / 场景未登记 / 没给场景键) → `*_speak` → **接**。
	//
	// ⚠️ 「实现者会用『判不出来 → 沉默』顺手把读库失败也吞掉 —— 那就是纪律 #3 的反面。」
	// 纯函数版本**看不见**这个区别(它不读库), 所以那三个 `*_speak` 理由
	// 在生产路径上恒为 0 次 —— 也就是说"故障与判不出来必须分得开"这条性质,
	// 在此之前**在生产上根本不存在**。
	//
	// ⚠️ 它读库, 所以必须在**白名单之后**(位置正好)。
	// ⚠️ 而它必须在 `EnsureScene` **之后** —— 上面那两行刚把场景行登记出来,
	// 否则每条群消息都会拿到"场景未登记"而短路成发言(它变话痨)。**顺序是承重的。**
	addressed, why := IsAddressedToMe(sc, self, cfg)
	if e.Store != nil {
		addressed, why = e.Store.ResolveAddressee(sc, self, cfg)
	}
	res := GroupGateResult{Addressed: addressed, AddressedWhy: why}

	// §6.x **插话收敛**: 被群友说了就稳定在 1%, 且不自动恢复
	// (chime.go 自己称它是「这个功能**唯一的安全阀**」)。
	//
	// ⚠️ **位置是承重的: 必须在 switch 之前。**
	// 被 @ 的那三条路都会早退, 放进 switch 里就永远轮不到 ——
	// 而"被 @ 着说别刷屏"恰恰是**最常见**的那种抱怨。
	//
	// ⚠️ "是不是在说它"由 `ChimeComplaintHit` 自己判(词 + atSelf **缺一不可**),
	// 这里不重复判 —— 只看场景是不是群。
	// ⚠️ 更要紧的是那个反面: 群友 A 对群友 B 说「你话真多」**绝不许**算到它头上,
	// 那会让它在**没人说它的时候自我禁言**, 而没有任何人知道为什么。
	// ⚠️ 判定收进 noteChimeComplaint 了 —— 它多要一条事实依据:
	// **它最近确实在说话**。只认词表的版本在 2026-10-16 误判过一次
	// (它两小时只说 1 条, 却因为一句「别刷屏闭嘴」被永久降级)。
	e.noteChimeComplaint(sc, addressed)

	switch {
	case why == ReasonAtSelf:
		// 「被 @ 必答」→「被 @ 必接」。
		//
		// ⚠️ **真人被 @ 也会漏、也会「嗯」一句。无条件开口是客服的定义, 不是群友的定义。**
		//
		// 保留: @我 **一定进入价值判断** —— 不会被白名单/资格闸门挡掉,
		//       而判据②已经命中, 所以它至少会「接一下」(表情 = 这个系统里的"嗯")。
		// 去掉: 无条件 speak。所以**只有它真有话可说时才会开口** ——
		//       另有判据命中 (品味/冲突/冷场/梗) 才 speak。
		//
		// ⚠️ 危机词是例外, **必须仍然必答**。少了这一条,
		// 「今天不想说话」会变成「我在群里求救而它不理我」。
		if CrisisHit(sc.Text) {
			res.Stance = StanceSpeak
			res.Reason = "被 @ 且含危机信号 — 硬门槛仍然短路"
			return e.withConflict(sc, res)
		}
		// 低意愿日: @ 了也可能不回 —— 这是「漏」在这个系统里的唯一形态。
		// ⚠️ 它**不是**"没看见", 是"看见了, 今天不想说"。
		if DayMoodSilencesAtReply(e.dayMood()) {
			res.Stance = StanceSilent
			res.Reason = "被 @, 但今天不想说话 (低意愿日)"
			return e.withConflict(sc, res)
		}
		// ⚠️ **2026-10-14 用户实测后恢复**: 「被 @ 必接」在生产里表现为"@ 它没反应" ——
		// 因为"接一下"= 发一张表情, 而表情那张牌还会因为文件缺失而打不出去。
		//
		// 复盘: 「客服味」不在于**答不答**, 而在于**答得像不像人** —— 那由长度和语气决定。
		// 用"少回一句"去换真实感, 换来的是"它不理我" —— 代价远大于收益。
		res.Stance = StanceSpeak
		res.Reason = "被 @ — 硬门槛短路 (2026-10-14 实测后恢复)"
		return e.withConflict(sc, res)
	case why == ReasonAtOthers:
		// 对"别人对别人说的话"贴个表情**也是插话** —— 所以连表情档都不给。
		res.Stance = StanceSilent
		res.Reason = "@ 的是别人 — 不接 (连表情都不发)"
		return e.withConflict(sc, res)
	case why == ReasonUndecidable:
		// 会话中 (5 分钟 mini 分类判定"还在聊同一件事") → **不落随机插话**,
		// 直接走下面的价值判断; 门槛会在 effectiveValueConfig 里降一档。
		// ⚠️ 只降门槛、不绕过判断: 它有话可说才开口 (§6.3)。
		if e.sessionOpen(sc.SceneKey) {
			// ⚠️ 2026-10-21: 这一支原来是"会话中一律接", 结果一天说了 687 条。
			// 现在过**发言预算** (静默期 / 间隔 45s / 10 分钟上限 4 条)。
			if ok, whyNot := e.speechBudget(sc); !ok {
				res.Stance = StanceSilent
				res.Reason = whyNot
				return e.withConflict(sc, res)
			}
			// ⚠️ 2026-10-23 P2「没话硬插」: 会话开着 ≠ 该接。
			// 只有「像在对它说」(称呼/问句/第二人称) 才直接接;
			// 否则退回下面的价值判断 (门槛已降一档) —— 有话说才开口。
			if !looksAddressedToIt(sc.Text) {
				res.Reason = "会话中 — 但没在跟它说, 退回价值判断"
				break
			}
			res.Stance = StanceSpeak
			res.Reason = "会话中 — 这话像是在问它"
			return e.withConflict(sc, res)
		}
		// ⚠️ **2026-10-14: 这里不再是终点。**
		//
		// 原来判不出来就直接沉默 —— 于是它**只有被叫才存在**,
		// 而它的灵光 / 学过的碎片全都烂在库里, 一句都说不出来。
		//
		// 现在过一道插话闸门。**失败方向仍然是沉默** ——
		// 插话是"打扰"那一侧的例外, 例外的失败只能往"不做"倒。
		// §6.10.5 陌生人 / 新人档: 「不接梗、不点名、只给中性反应; **不主动搭话**」。
		//
		// ⚠️ **只挡这一支** —— 被 @ 那几条路在上面就 return 了, 根本走不到这里。
		// 也就是说"被 @ 必答"(§6.3 硬门槛)天然不受它影响, **那正是这个位置的意义**:
		// 2026-10-14 用户实测过一次"@ 它没反应"的代价, 那一次不能重演。
		//
		// ⚠️ 三个"往说话倒"的细节:
		//   · 读库失败 → **不挡**(`err == nil` 是条件的一部分);
		//   · 时间戳解析失败 → `IsNewMember` 已改成返回 false(见它的注释);
		//   · **UIN 为空 → 不判** —— "不知道是谁"不等于"是个新人",
		//     把它当新人会让一条没带 uin 的消息莫名其妙地被静音。
		if uin := strings.TrimSpace(sc.UIN); uin != "" && e.Store != nil {
			if isNew, err := e.Store.IsNewMember(sc.SceneKey, uin, clock.Now(), NewMemberWindow); err == nil && isNew {
				res.Stance = StanceSilent
				res.Reason = "新成员 — 不主动搭话 (§6.10.5)"
				return e.withConflict(sc, res)
			}
		}
		if d := e.chimeAt(sc); d.Speak {
			res.Stance = StanceSpeak
			res.Chimed = true
			res.ChimeTopic = d.Topic
			res.Reason = d.Reason
			return e.withConflict(sc, res)
		}
		res.Stance = StanceSilent
		res.Reason = "判不出对谁说的 — 倾向沉默 (误接话比不接更尴尬)"
		return e.withConflict(sc, res)
	case why == ReasonThreadOurs || why == ReasonThreadToSelf || why == ReasonQuoteSelf:
		// §6.10.2 ③ 线程归属: 这句话就是在跟它说 —— **接**。
		//
		// ⚠️ **被点名(ReasonNamed)不在这里** —— 它本来就该走下面的价值判断:
		// "被叫到"只值一个表情档, 够不上开口。2026-10-20 我把它一起塞进来,
		// 三条既有用例当场红: 被点名 + 无梗从"表情"变成了"开口"。
		//
		// ⚠️ 这一支以前不存在: 判据早就返回了这些理由, 却没有 case 接住,
		// 于是掉到下面"价值判断"里 (要命中 2 个判据才开口), 表现和"判不出来"几乎一样。
		// 用户的原话「有时候没有 @ 但是话题没结束他就不回消息了」—— 就是这个。
		//
		// ⚠️ 2026-10-21: 原来只有"刚说完话"一个 20 秒判断 —— 而它回一句之后,
		// 下一条消息的"上一条"又变成它 → 自激循环, 一天 687 条。
		// 现在与会话分支共用同一份**发言预算**。
		// 先看"上一条是不是刚说的": 3 秒前它自己刚开口, 这条大概率是它自己的余音。
		if !sc.PrevTS.IsZero() && !sc.TS.IsZero() && sc.TS.Sub(sc.PrevTS) < ThreadReplyMinGap {
			res.Stance = StanceSilent
			res.Reason = "线程延续, 但它刚说完话 — 缓一下 (" + why + ")"
			return e.withConflict(sc, res)
		}
		// 再看发言预算 (静默期 / 间隔 60s / 10 分钟 3 条)。
		if ok, whyNot := e.speechBudget(sc); !ok {
			res.Stance = StanceSilent
			res.Reason = whyNot + " (" + why + ")"
			return e.withConflict(sc, res)
		}
		res.Stance = StanceSpeak
		res.Reason = "线程延续 — 上一条在跟它说 (" + why + ")"
		return e.withConflict(sc, res)
	case strings.HasSuffix(why, "_speak"):
		// **系统故障** —— 往说话倒, 与"判不出来"是两件事。
		res.Stance = StanceSpeak
		res.Reason = "系统故障(" + why + ") — 退回升级前的行为(接)"
		return e.withConflict(sc, res)
	}

	// 2. 价值判断。
	in, hits := e.collectValueHits(sc, addressed)
	// §6.8 偏向: **在价值判断之前**算好, 由 DecideValue 落到分数上。
	// ⚠️ 有数据源才带 —— 见 groupBias 的注释 (凭空 Δ=-1 是真事)。
	if bias, ok := e.groupBias(sc); ok && (bias.Delta != 0 || bias.Floor) {
		in.HasBias, in.BiasDelta, in.BiasFloor, in.BiasReason = true, bias.Delta, bias.Floor, bias.Reason
	}
	vcfg := e.effectiveValueConfig(sc, cfg)
	dec := DecideValue(in, hits, vcfg)
	res.Stance, res.Value, res.Reason = dec.Stance, dec, dec.Reason
	// §6.9 **冲突介入的判定侧** (2026-10-15 接线)。
	//
	// ⚠️ 它排在价值判断**之后**: 冲突是一条**覆盖**规则 ——
	// "该不该说话"已经判完了, 而"现在这场合能不能劝"是另一层。
	// 它管两件事: 硬线 4 的沉默, 与"该介入时抬成发言"。
	return e.withConflict(sc, res)
}

// effectiveValueConfig 把 §6.6 的角色位落到**门槛**上。
//
// ⚠️ 落门槛而不是概率 —— §6.3 的纪律: 概率缩放实测看不出效果 (B6 的教训)。
// 潜水位 +2 更难开口, 捧哏位 -1 更容易; 下界 2 不许破,
// 再低就退化成"命中一条就开口", 而那正是判据表要防的事。
//
// ⚠️ 抽成独立方法是为了**能被测**: 埋在 GateGroupMessage 里的话,
// "角色位到底有没有改门槛"只能靠肉眼, 而这正是接线失效最常见的形态。
func (e *Engine) effectiveValueConfig(sc SceneMsg, cfg GroupConfig) ValueConfig {
	vcfg := ValueConfigFrom(cfg)
	if e.Store == nil {
		return vcfg
	}
	p, err := e.Store.LoadProfile()
	if err != nil || p == nil {
		return vcfg
	}
	// 会话中: 门槛降一档 —— 话题还没散的时候, "接一句"比"沉默"更像人。
	// ⚠️ 只降门槛, 不绕过判断 (它有话可说才开口, §6.3)。
	if e.sessionOpen(sc.SceneKey) {
		vcfg.SpeakMinHits--
		if vcfg.SpeakMinHits < 1 {
			vcfg.SpeakMinHits = 1
		}
	}
	vcfg.SpeakMinHits += GroupRoleNeedBonus(e.RoleOf(p.Name, sc.SceneKey))
	if vcfg.SpeakMinHits < 2 {
		vcfg.SpeakMinHits = 2
	}
	return vcfg
}

// recordGateOutcome 群消息的**闭环落库**: 品味轨迹 / 负反馈 / 关系量 / 意图轨。
//
// ⚠️ 它必须挂在**三态之前的那一个 defer** 上, 理由和 noteStance 完全一样:
// 只记"它说了话"的那些, 判据① 的接话率就恒等于 100%, 于是判据① 恒命中 ——
// 那不是"它有了品味", 那是一条永远为真的判据。
//
// ⚠️ 落库失败必须出声 (P7: 静默的不是数据库, 是调用方)。
func (e *Engine) recordGateOutcome(sc SceneMsg, out GroupGateResult) {
	if e.Store == nil {
		return
	}
	owner := e.ownerKey()
	topic := e.topicOf(sc.Text)
	// ① 品味轨迹: 这个话题它接没接。沉默也记。
	if topic != "" {
		if err := RecordTaste(e.Store.DB, owner, topic, out.Stance == StanceSpeak); err != nil {
			log.Warn("convo:value", "品味轨迹落库失败: %v", err)
		}
	}
	// §6.4.3 负反馈: 骂 / 纠正。
	// ⚠️ gotReply 传 true —— 冷场**不在这一层判**: 它要等"它说了话之后没人接",
	// 那是另一个时刻。在这里判会把每条群消息都变成一次冷场。
	kind, hit := DetectFeedback(sc.Text, true, 0)
	if hit {
		if n, err := RecordFeedback(e.Store.DB, owner, sc.SceneKey, kind, topic); err != nil {
			log.Warn("convo:value", "负反馈落库失败: %v", err)
		} else if n >= FeedbackPromoteThreshold {
			log.Info("convo:value", "边界升格 (%s): 第 %d 次", kind, n)
		}
	}
	// §5.1 关系量: 熟悉度**每条都涨**, 亲密度对好事坏事都反应。
	//
	// ⚠️ 这里原来是 `BondSignal{Msgs, Rebuffed, Corrected}` —— **只有负向信号**。
	// 而四个正向字段(说了私事 / 向它请教 / 道谢 / 深夜还在聊)全库没有生产者,
	// 于是 `closeness` 从 0 出发只可能被扣下去 → **永远是 stranger**
	// → 热力 cap 20 < 每条主动消息的代价 25 → **主动配额恒为 0**。
	// 现在正向的那四项从消息文本里读 (`BondSignalsFrom`)。
	if br, err := LoadBond(e.Store.DB, owner, sc.SceneKey); err == nil {
		sig := BondSignalsFrom(sc.Text, sc.TS)
		sig.Msgs = 1
		sig.Rebuffed = kind == FeedbackScold
		sig.Corrected = kind == FeedbackCorrect
		b := ApplyBond(br.Bond, sig)
		if err := SaveBond(e.Store.DB, owner, sc.SceneKey, b); err != nil {
			log.Warn("convo:value", "关系量落库失败: %v", err)
		}
		// §5.1 **人物层**: 群里的这一笔也要记到**这个人**头上 (owner 定规 2026-10-16)。
		//
		// ⚠️ 键是**发言人 uin**(sc.UIN), 不是群号 —— 群号是场景, 不是人。
		// 方向规则由此是结构性的: 群那一行只有本群读 (`loadBondForScene`), 人物层只有私聊读。
		e.noteBondPerson(sc.UIN, sig, e.OwnerMultForScene(sc.SceneKey))
		// §5.6 **关系阶段落到场景行** —— 群这条路原来缺的正是这一步。
		//
		// ⚠️ 私聊走 `NoteSceneBond`, 它里面自带这句; 而群这条路是**直接** `SaveBond`,
		// 于是 `convo_scene.stage` 只有 `EnsureScene` 插入那一次的 `stranger`:
		//   · `SelfRoleLine`(群里"能不能怼")永远按陌生档说话;
		//   · `conflictwire` A1(阶段=陌生 → 有冲突也不掺和)永远触发;
		//   · 热力 `CapOf(stage)` 永远拿 20 → **群内主动配额恒为 0**。
		// 生产实测 (2026-10-16): 群 closeness 0.37, 而 stage 仍是 stranger。
		//
		// ⚠️ 传**刚落库的** `b`(不是 `br.Bond`): 库里的值已经是它 —— 用旧值会让阶段
		// 永远慢一条消息, 而"慢一条"这种偏差在别处已经坑过一次。
		e.NoteSceneStage(sc.SceneKey, BondRecord{Loaded: true, Bond: b})
	}
	// §7.5 意图轨: **只收 plan/preference/boundary** —— fact 已经走记忆那条路,
	// 两边都收会让同一句话在提示里出现两次。
	if k := ClassifyIntentKind(sc.Text); k == IntentPlan || k == IntentPreference || k == IntentBoundary {
		if c, ok := CanonicalIntent(sc.Text, k); ok {
			if err := SaveIntent(e.Store.DB, owner, sc.SceneKey, IntentTrack{Raw: sc.Text, Canonical: c, Kind: k}); err != nil {
				log.Warn("convo:value", "意图轨落库失败: %v", err)
			}
		}
	}
}

// roleSpokeToday 今天在这个位上一共开过几次口 (杠精的每日上限用)。
func (e *Engine) roleSpokeToday() int {
	n, _ := e.Store.CounterGet("grouprole_spoke", clock.Today())
	return n
}

// collectValueHits 组装五条判据。
//
// ⚠️ **只读, 绝不写** —— 尤其是判据⑤: 现有调用点紧跟 TouchSlang (那是"这个词条还活着"),
// 而裁决器若顺手 Touch, **一次不接的沉默也会重置梗的衰减** → 梗库永不退役。
//
// v1 只接了判据②(来自资格闸门)与⑤(梗库); ①③④ 标为**缺席**(Has*=false)
// —— 它们**不进分母**, 门槛会按可用判据数折算 (见 AvailableCriteria)。
// 这不是"先不做", 是"没数据时不许假装有数据": 把缺席当成"不命中"会让
// 四条门槛变成事实上的永不发言, 那是个安静的默认值 bug。
func (e *Engine) collectValueHits(sc SceneMsg, addressed bool) (ValueInput, ValueHits) {
	in := ValueInput{
		Scene:       sc.SceneKey,
		Addressed:   addressed, // 由调用方填入 —— 见 GateGroupMessage
		HasTaste:    false,     // ① 品味轨迹: 无设施 (需先裁定是否允许裁决路径上做 embedding)
		HasConflict: false,     // ③ 冲突: 需要该场景的消息窗口
		HasGap:      false,     // ④ 冷场: 需要该场景的上一条消息时刻
	}
	// ⑤ 梗库命中: **只读**。
	var topic string
	if hits, err := e.Store.FindSlangHit(sc.Text); err == nil {
		in.SlangHits = len(hits)
	}
	// 话题键优先, 梗命中兜底。
	// ⚠️ 词表来自**当天聊了什么** (夜间抽取), 梗表来自**外来的黑话** ——
	// 拿梗表当话题, 判据① 就退化成「它对哪些梗有话想说」。
	if keys, kerr := e.Store.RecentTopicKeys(e.ownerKey(), 7); kerr == nil {
		if k, ok := MatchTopicKey(sc.Text, keys); ok {
			topic = k
		}
	}
	if topic == "" {
		if hits, err := e.Store.FindSlangHit(sc.Text); err == nil && len(hits) > 0 {
			topic = hits[0].Phrase
		}
	}
	// ① 品味轨迹 (§6.3)。
	//
	// ⑤ 量"这一句命中了没有"; ① 量"历史上它对这类话题接话的比例" —— 同一张词表, 两个量。
	//
	// ⚠️ 没样本 → HasTaste **保持 false** (缺席, 不进分母);
	// 有样本但接话率不够 → HasTaste=true 且 TasteHits=0 (判过了, 结论是不命中)。
	// 把这两件事混起来, 就是判据表最典型的"安静的默认值 bug"。
	if topic != "" {
		if th, terr := LoadTaste(e.Store.DB, e.ownerKey()); terr == nil {
			if rate, ok := TasteOf(th, topic); ok {
				in.HasTaste = true
				if rate >= TasteHitThreshold {
					in.TasteHits = 1
				}
			}
		}
	}
	// ④ 冷场 (§6.3)。
	//
	// ⚠️ 必须**排除刚刚那条**: 调用方先落库再裁决, 直接取最新一条差值恒为 0,
	// 判据④会**永不触发** —— 而它看起来完全正常。
	if sc.SceneKey != "" {
		if last, has, lerr := LastSceneMessageAtExcluding(e.Store.DB, sc.SceneKey, sc.Text, clock.Now()); lerr == nil {
			gap, h, valid, ceil := SceneColdGap(last, has, clock.Now(), ColdGapMin, ColdGapMax)
			in.HasGap, in.Gap, in.GapValid, in.ColdCeiling = h, gap, valid, ceil
		}
	}
	// ③ 冲突: 用 **DetectConflict 的四信号**, **不是裸负面词表**。
	//
	// 设计稿的括注写的是「冲突关键词命中(**吵架劝和**, §6.9)」——
	// 语义是"确实在吵", 不是"出现了负面词"。
	// 裸词表会让它在别人吐槽"这个 bug 真烦"的时候插嘴 —— 那不是劝架, 是听不懂人话。
	//
	// ⚠️ 这也是**唯一**一处它需要读"这个场景最近发生了什么"的地方 ——
	// 所以必须用 RecentMessagesIn (按场景), 否则会拿别的群的争吵来判断这个群。
	if sc.SceneKey != "" {
		if msgs, err := e.Store.RecentMessagesIn(sc.SceneKey, 30); err == nil && len(msgs) > 1 {
			in.ConflictWindow = len(msgs)
			pol := DefaultConflictPolicy()
			selfUIN := e.Cfg.Channel.SelfQQ
			cmsgs := make([]ConflictMsg, 0, len(msgs))
			for i := len(msgs) - 1; i >= 0; i-- { // 按**时间升序** (判据要求)
				m := msgs[i]
				ts, _ := time.ParseInLocation(bodyTSLayout, m.TS, time.Local)
				cmsgs = append(cmsgs, ConflictMsgFrom(m.SenderUIN, selfUIN, m.Payload, nil, ts))
			}
			base := ConflictBaselineFrom(cmsgs, pol)
			if hit, _ := DetectConflict(cmsgs, base, pol); hit {
				in.HasConflict = true
				in.Conflict = true
			} else {
				// ⚠️ **判过了, 结论是没冲突** → HasConflict=true (它进了分母)。
				// 与"读不到消息窗口"是两件事: 后者 HasConflict=false (缺席, 不进分母)。
				in.HasConflict = true
				in.Conflict = false
			}
		}
	}
	return in, HitValue(in)
}

// ——— §6.10 群里的执行路径 (三态的后两态) ———

// ReactInGroup §6.1 **第二态**: 只发一张表情, **0 次 LLM 调用**。
//
// 这是"约九成判否"最便宜的落法 —— 接住了梗, 但没说一个字。
func (e *Engine) ReactInGroup(sceneKey string, groupID int64) error {
	gs, ok := e.Sender.(GroupSender)
	if !ok {
		return fmt.Errorf("sender 不支持群发送 (未实现 GroupSender)")
	}
	// ⚠️ **只挑文件真的在的那张。**
	//
	// 实测 (2026-10-14 生产): stickers 表里 9 条、ja 目录里 9 个文件, 但**文件名对不上** ——
	// 表是从 PC 同步过来的, .gif 没跟着过来。挑中的那张在磁盘上不存在,
	// 桥接方回 "image 文件不存在", 于是"接一下"变成了"什么都没发生"。
	//
	// 表与文件是**两份真相**, 而牌打不出去的时候没有任何人知道 —— 除了日志。
	localPath := ""
	for try := 0; try < 5; try++ {
		cand, err := e.Store.WeightedPick(e.Cfg.Stickers.CapPerSticker)
		if err != nil || cand == nil || cand.LocalPath == "" {
			break
		}
		if _, statErr := os.Stat(cand.LocalPath); statErr == nil {
			localPath = cand.LocalPath
			break
		}
		log.Warn("convo:sticker", "贴纸文件不在, 换一张: %s", cand.LocalPath)
	}
	if localPath == "" {
		// 一张能用的都没有 → **什么都不做**。不许退化成"发一句文本":
		// 那会把第二态悄悄变成第三态, 而第三态要花一次 LLM 调用。
		return nil
	}
	if err := gs.SendGroupImage(groupID, localPath); err != nil {
		return err
	}
	_, _ = e.Store.InsertMessageFull("ai", "qq", "sticker", "[表情]",
		MsgMeta{Origin: OriginAIToUser, SceneKey: sceneKey})
	return nil
}

// SceneMemoryInjectMax 群公共层一次最多注入几行。
//
// ⚠️ 与别的段一样是**预算**: 群共识再重要也不该把这一轮的话挤没。
const SceneMemoryInjectMax = 6

// sceneNotes §7.5 群公共层的注入行。读不到 → nil (= **不注入**, 失败往「少说」倒)。
func (e *Engine) sceneNotes(sceneKey string) []string {
	if e.Store == nil || strings.TrimSpace(sceneKey) == "" {
		return nil
	}
	lines, err := e.Store.SceneMemoryInjection(sceneKey, SceneMemoryInjectMax)
	if err != nil {
		log.Warn("convo:scene", "群公共层读取失败(这一轮不注入): %v", err)
		return nil
	}
	return lines
}

// SpeakInGroup §6.10 **第三态**: 在群里开一次口。
//
// ⚠️ 与私聊路径的三处不同 (每一处漏了都会出事):
//  1. **L1 按场景取** (in.SceneKey) —— 漏了会把别处的话读进来 (跨人转述);
//  2. **发到群** (GroupSender) 而不是发给人 —— 写成 SendText 会让它私聊给 owner;
//  3. **入库标 origin/scene** —— 否则这条 AI 发言会被当成私聊消息进别人的上下文。
//
// ⚠️ 这里原来有一个 `SpeakInGroup`(不看图的便捷包装) —— **已删**。
//
// ⚠️ 它不是"没人用", 而是**只有测试在用**(6 处) —— 而生产路径走的是
// `SpeakInGroupSeeing`(`cmd/convo/main.go` 调它, 并把 lookPath 一起传)。
//
// ⚠️ **为什么删一个"用起来更顺手"的包装**:
//
//	· 它让**测试走了一条生产不会走的路** —— 而夹具该走真入口
//	  (与前面几轮同一条规矩: 手搓捷径会掩盖真路径上的问题);
//	· 而它上面对那三处差异的说明, **本来就是 `SpeakInGroupSeeing` 的性质**,
//	  写在一个只有测试会经过的壳子上, 谁也不会在那里读到它。
//
// ⚠️ 那三处差异的说明**没有丢** —— 它们现在挂在下面对的地方。

// SpeakInGroupSeeing 在群里说话, 并且**看着一张图说**。
//
// ⚠️ 这个方法是补一个**线上可见的缺口** (2026-10-14 用户截图):
//
//	群友: [图] @nahida 这是谁呀？
//	它:   ? 图里那个?
//	它:   我这没看清 你截的谁啊
//
// 它**知道有张图** (群路径会记一笔 [图片]), 但**看不到内容** ——
// 因为群路径当初根本没接 vision, 只有私聊接了。
//
// ⚠️ 与"直接塞路径"的区别: lookPath 是**已经判过重、打过标签**之后的决定 ——
// 表情包只给标签 (标签就是它的行为), 照片才真的把图给它看。
// GroupMaxTextPerTurn 群聊一轮最多发几条文本 —— 拆条是刷屏, 一条就够。
const GroupMaxTextPerTurn = 1

func (e *Engine) SpeakInGroupSeeing(ctx context.Context, sceneKey string, groupID int64, uin, text, lookPath string) error {
	gs, ok := e.Sender.(GroupSender)
	if !ok {
		return fmt.Errorf("sender 不支持群发送 (未实现 GroupSender)")
	}
	p, err := e.Store.LoadProfile()
	if err != nil || p == nil {
		return fmt.Errorf("no profile: %w", err)
	}
	owner := e.ownerKey()
	din := DialogInput{
		UserText:   text,
		CoachNote:  e.coachNote(sceneKey),
		VoiceNote:  voiceNoteOrEmpty(e.groupVoice(sceneKey, 200)),
		RepeatNote: e.repeatNote(sceneKey),
		NewsNote:   e.newsNote(),
		WorldNote:  e.worldNote(),
		SceneNotes: e.sceneNotes(sceneKey), // §7.5 群公共层
		State:      e.currentState(),
		ActionKind: ActImmediate,
		SceneKey:   sceneKey,           // ← 决定 L1 取谁的历史
		SelfNote:   e.selfActionNote(), // ← 它自己的对外动作也要进事实层
		MoralsNote: MoralsNote(p),      // ← 道德底线 (随人设: 被骂的反应由角色决定)
		SpeakerUIN: uin,                // ← 护栏 3「偏向可见」的数据源
		Stance:     StanceSpeak,
		// ⚠️ 只有当这张图**值得看**时才有值 (照片)。表情包走标签, 不走这里。
		//
		// ⚠️ **没带图但它提了图 → 去看最近那张** (2026-10-14 实测补的)。
		//
		// 桥接方不输出 reply 段 (段类型统计里没有它), 所以
		// 「引用一张图问」到 convo 这边是**纯文本**, 图完全不在手上。
		// 现象就是它对着上下文里一条 [QQ表情265] 硬说"我看到一张脸"。
		//
		// 近似的补法见 imgrecall.go: 只有真的提到图、且 5 分钟内的照片才用。
		ImagePath: e.lookOrRecent(lookPath, text),
	}
	// 实时问题: **被 @ 且带时效性信号**才查一次。
	// ⚠️ 放在这里 (构造 din 时) 而不是更早: 只有真的要说的时候才值得花那两次调用。
	// 查不到时注入的是**反问指令**, 不是"你不知道" —— 见 liveNoResultNote。
	// ⚠️ **工具有了就别再走关键词那条** —— 否则同一次提问会搜两遍:
	// 关键词注入一次 (搜给模型看), 模型自己再调一次工具。
	// 工具是**模型自己判断**该不该查, 那条路更准, 所以它优先。
	if _, toolCapable := e.Chatter.(ToolChatter); !toolCapable {
		if note, ok := e.liveLookup(ctx, text); ok {
			din.LiveNote = note
		}
	}
	// §6.8 归因: 把这次裁决的 reason 与 Δ 带上 (读不到就留空, 不是错误)。
	if g, ok := e.lastGate(sceneKey); ok {
		din.StanceReason = g.Reason
	}
	// §6.6 角色位 (稳定档)。
	if line, ok := GroupRoleLine(e.RoleOf(p.Name, sceneKey), e.roleSpokeToday()); ok {
		din.RoleNote = line
	}
	// §5.1 关系 (天档) —— **Loaded=false 就不注入**: 没有记录不等于陌生人。
	// ⚠️ 走**读侧唯一出口**: 群只读本群那一行 (私聊不许进群, 群之间不许串)。
	din.BondNote = e.bondNoteForScene(sceneKey)
	// §4.4.1b 语域漂移 (天档): 基线 = 近 7 天, 最近 = 昨天那一行。
	// 两个都是**小查询**, 不在热路径上扫消息。
	if base, e1 := LoadRegisterBaseline(e.Store.DB, owner, 7); e1 == nil {
		if recent, e2 := LoadRegisterBaseline(e.Store.DB, owner, 1); e2 == nil {
			if line, ok := RegisterDriftLine(DetectRegisterDrift(base, recent)); ok {
				din.RegisterNote = line
			}
		}
	}
	// §6.4.3 边界记忆 (慢档)。
	// §7.4: 边界**降权而非永久禁止** —— 强度由升格时刻现算。
	din.Boundaries = e.Store.LoadBoundaryLines(owner, sceneKey, clock.Now())
	// §7.5 意图轨 (天档)。
	if its, ierr := LoadIntents(e.Store.DB, owner, sceneKey, 4); ierr == nil {
		for _, it := range its {
			if line, ok := IntentLine(it); ok {
				din.IntentNotes = append(din.IntentNotes, line)
			}
		}
	}
	// §6.9 冲突介入的执行 (§6.9 判据层只回答"要不要", 这里回答"怎么劝")。
	if note, ok := e.conflictActNote(sceneKey); ok {
		din.ConflictNote = note
	}
	// §7.2 联想 (轮级): 由**当下的线索**撞出一段旧事 —— 没有共享线索就不联想。
	//
	// ⚠️ 这 6 行原来**只在这里**(群路径), 而设计稿举的例子是私聊那只猫。
	// 已抽成 `assocNote`; 群路径也改成调它 —— **一份实现, 两处调用**。
	din.Association = e.assocNote(sceneKey, text)
	frame, err := e.dialogReply(ctx, din, p)
	if err != nil {
		return err
	}
	// A6 硬拒: 刻意不说 —— 静默丢弃, 不降级、不报错。
	if frame.Refused {
		log.Warn("convo:engine", "群硬拒: 本条未发送 (%s)", frame.RefuseReason)
		return nil
	}
	// 2026-10-23 P2: 群聊一轮**只说一句** (表情另算) —— 拆条是刷屏。
	textSent := 0
	for _, item := range frame.Reply {
		switch item.Kind {
		case "text":
			txt := strings.TrimSpace(item.Text)
			if txt == "" {
				continue
			}
			if textSent >= GroupMaxTextPerTurn {
				continue
			}
			// P3: 纯标点 (如只回一个「？」) 是噪音, 不是聊天。
			if isContentlessText(txt) {
				log.Info("convo:engine", "群发拦截: 空内容 %q", txt)
				continue
			}
			// P3: 60 秒内说过一模一样的话 (归一化后) → 不重复发。
			if e.sameSpokenRecently(sceneKey, txt) {
				log.Info("convo:engine", "群发拦截: 刚说过同样的话 %q", txt)
				continue
			}
			// 打字节奏: **打完了才发** —— 延迟按这条消息的字数算。
			if d := e.groupTypingDelay(txt); d > 0 {
				if serr := clock.Sleep(ctx, d); serr != nil {
					return serr
				}
			}
			if serr := gs.SendGroupText(groupID, txt); serr != nil {
				log.Warn("convo:engine", "群发文本失败: %v", serr)
				continue
			}
			textSent++
			kind := "text"
			if frame.Degraded {
				kind = "fallback" // 降级产物: 留痕但不进上下文
			}
			_, _ = e.Store.InsertMessageFull("ai", "qq", kind, txt,
				MsgMeta{Origin: OriginAIToUser, SceneKey: sceneKey})
		case "sticker":
			st, serr := e.Store.WeightedPick(e.Cfg.Stickers.CapPerSticker)
			if serr != nil || st == nil || st.LocalPath == "" {
				continue
			}
			if serr := gs.SendGroupImage(groupID, st.LocalPath); serr != nil {
				log.Warn("convo:engine", "群发表情失败: %v", serr)
				continue
			}
			_, _ = e.Store.InsertMessageFull("ai", "qq", "sticker", "[表情]",
				MsgMeta{Origin: OriginAIToUser, SceneKey: sceneKey})
		}
	}
	return nil
}

// ——— §6.10.6 「你在群里的位置」 ———

// SelfRoleLine 它在**这个群**里的位置。空串 = 不注入。
//
// ⚠️ 三条不许破:
//
//  1. **它是身份, 不是判据。** "我的位是什么"是稳定档 (换群才变) → 进 persona;
//     "能不能给这个发言加值"是**决策层**的事 (ValueInput.RoleFit), **绝不注入** ——
//     把判据注入进去等于让模型自己决定接不接, 那是"决策器不接 LLM"的反面。
//
//  2. **不输出任何 owner 标记** (§6.10.7 / §5.6 护栏 2)。
//     这里只说"我在这个群是什么角色", **不说"谁是主人"** ——
//     把它写进 prompt 等于在群里公示关系。
//
//  3. **关系阶段决定"能不能怼"** (§6.10.5 三档)。这是它在群里的社交默认,
//     比人设厚度重要 (L929「固定位 > 人设厚度」)。
func SelfRoleLine(sc *Scene, cfg GroupConfig) string {
	if sc == nil {
		return ""
	}
	role := EffectiveSelfRole(cfg.SelfRole, sc.SelfRole)
	if strings.TrimSpace(role) == "" {
		role = RoleMember // 取不到 → 最保守的一档
	}
	stage := strings.TrimSpace(sc.Stage)
	if stage == "" {
		stage = StageStranger // 零值 = 未知 → 按最陌生处理
	}

	var roleCN, stageCN, behavior string
	switch role {
	case RoleOwner:
		roleCN = "群主"
	case RoleAdmin:
		roleCN = "管理员"
	default:
		roleCN = "普通群友"
	}
	switch stage {
	case StageIntimate:
		stageCN, behavior = "自己人", "可以怼、可以起外号、可以接任何梗。"
	case StageFamiliar:
		stageCN, behavior = "熟悉", "可以接话、可以捧哏; **但别怼**, 也别乱起外号。"
	default:
		stageCN, behavior = "陌生", "**不接梗、不点名, 只给中性反应**; 多听少说。"
	}

	var b strings.Builder
	b.WriteString("\n【你在群里的位置】你在「群 " + sc.GroupID + "」是" + roleCN +
		", 跟这个群的关系是" + stageCN + "。\n")
	b.WriteString("表现: " + behavior + "\n")
	return b.String()
}
