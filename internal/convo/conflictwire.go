package convo

import (
	"strings"

	"github.com/tzhnahida/convo/internal/log"
)

// ─────────────────────────────────────────────────────────────
// §6.9 **冲突介入: 判定侧的接线** (2026-10-15)
//
// 这一节原来只接了一半:
//
//   · `conflictActNote` (**怎么劝**) —— 已接 (value.go:805), 它甚至自己算了一遍冲突信号;
//   · `GateConflict` (**要不要劝**) —— **生产调用者为零**。
//
// ⚠️ 于是三条硬约束全都不生效, 而它们各自都是**硬线**:
//
//   1. **A5 冷却 (120 分钟)** —— 没有决策落库, `ConflictCooldownActive` 永远读不到东西,
//      同一对可以**无限次**被劝;
//   2. **硬线 4** —— 「涉及人身攻击 / 歧视 / 安全 → **不劝和**, 沉默或上报 owner」,
//      原来它照样会开口;
//   3. **C3 抬门槛 / A3 不熟不劝** —— 都要那份决策才谈得上。
//
// ⚠️ 而"怎么劝"接上了、"要不要劝"没接, 是最难看出来的一种半接线:
// **它会劝架, 只是劝得毫无节制** —— 而那看起来像"它很热心"。
// ─────────────────────────────────────────────────────────────

// ——— §6.9 信号 ② 的存储层: @ 目标 ———

// JoinAtUINs 把一条消息的 @ 目标拼成库里那一列 (逗号分隔)。
//
// ⚠️ 去重 + 保持出现顺序: 顺序不影响判定(信号只问"有没有互相 @"),
// 但**去重是必须的** —— 同一个人被 @ 两次不该在库里留两遍。
func JoinAtUINs(ats []string) string {
	if len(ats) == 0 {
		return ""
	}
	seen := make(map[string]bool, len(ats))
	out := make([]string, 0, len(ats))
	for _, a := range ats {
		a = strings.TrimSpace(a)
		if a == "" || seen[a] {
			continue
		}
		seen[a] = true
		out = append(out, a)
	}
	return strings.Join(out, ",")
}

// SplitAtUINs 把库里那一列拆回来。空 → nil (零值 = 没 @ 任何人)。
func SplitAtUINs(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var out []string
	for _, a := range strings.Split(raw, ",") {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	return out
}

// conflictWindowOf 把最近的消息窗口归一化成判据输入 (§6.9)。
//
// ⚠️ 抽成一个函数是因为**两处都要用**: `conflictActNote` 判"怎么劝",
// `applyConflictGate` 判"要不要劝"。各写一遍的下场这一轮已经见过一次(睡眠窗那个公式),
// 而这次两处都在同一个包里、用的是同一份窗口 —— 让它们从同一个函数出来。
func (e *Engine) conflictWindowOf(sceneKey string, n int) ([]ConflictMsg, ConflictPolicy, bool) {
	pol := DefaultConflictPolicy()
	if e.Store == nil || strings.TrimSpace(sceneKey) == "" {
		return nil, pol, false
	}
	if n <= 0 {
		n = 20
	}
	msgs, err := e.Store.RecentMessagesIn(sceneKey, n)
	if err != nil || len(msgs) < 2 {
		return nil, pol, false
	}
	selfUIN := e.Cfg.Channel.SelfQQ
	cmsgs := make([]ConflictMsg, 0, len(msgs))
	for i := len(msgs) - 1; i >= 0; i-- { // 时间升序
		m := msgs[i]
		ts, _ := ParseMsgTS(m.TS)
		// §6.9 信号 ② 的数据来源 —— 这些 @ 目标由入库时写进 at_uins 列。
		//
		// ⚠️ 这条列是 2026-10-15 才加的。在那之前 `MsgMeta` / `MessageRecord`
		// **都不带 @ 目标**, 于是信号 ② 在生产窗口上**永远算不出来** ——
		// 后果不是"判不出冲突", 而是**门槛实际是 3/3**(能算的只剩 ①③④, 三个必须全中)。
		// ⚠️ 存量行没有这一列的值(空) → 那些窗口仍然只有三个信号, 这是**正确的**:
		// 历史消息本来就没记过 @ 给谁, 补不出来。
		cmsgs = append(cmsgs, ConflictMsgFrom(m.SenderUIN, selfUIN, m.Payload, m.AtUINs, ts))
	}
	return cmsgs, pol, true
}

// conflictContextOf §6.9 判定要的上下文。
//
// ⚠️ **失败方向**: 读不到的场景信息一律**留空**(= 未提供), 由判定内部决定不生效 ——
// 而不是在这里填一个默认值把某条硬线打开。
func (e *Engine) conflictContextOf(sceneKey string, cmsgs []ConflictMsg, selfUIN string) (ConflictContext, error) {
	ctx := ConflictContext{SceneKey: sceneKey}
	if e.Store == nil {
		return ctx, nil
	}
	// ⚠️ **读失败必须报出来, 不许吞。**
	//
	// 原来这里是 `if sc, err := ...; err == nil && sc != nil` —— 错误被静默丢掉。
	// 后果不是"少了一点信息", 而是**方向错了**:
	//
	//   · `ctx.Stage` 留空 → A1「阶段=陌生 → 不掺和」**不触发**
	//     → 一次读库失败把它推向**更容易介入**的那一侧;
	//   · 而"故障"与"未提供"在日志里**长得一模一样**。
	//
	// ⚠️ 而 `ResolveConflict` 的注释把这条界线钉死了:
	// 「`ctxErr != nil`(读库失败) → ConflictNormal; `ctx.Undecidable` → ConflictSilent;
	//   **把两者并成一条, 就是 R28 写错的样子。**」
	//
	// ⚠️ **只有"读失败"算故障**: 场景行**不存在**是"未提供"(留空, 照常走判据) ——
	// 两者在这一行上分开, 而不是在调用方各自解释(P7: 静默的不是数据库, 是调用方)。
	sc, err := e.Store.LoadScene(sceneKey)
	if err != nil {
		return ctx, err
	}
	if sc != nil {
		ctx.Stage = sc.Stage
		ctx.SelfRole = sc.SelfRole
	}
	// A3 的"跟双方都不熟": 熟悉度是**场景级**的(一个场景一行关系量) ——
	// 拿不到就留 nil (= 未提供 → A3 不生效), 而不是填 0 把"不熟"坐实。
	if e.Store.DB != nil {
		if br, err := LoadBond(e.Store.DB, e.ownerKey(), sceneKey); err == nil && br.Loaded {
			aff := map[string]float64{}
			for _, m := range cmsgs {
				u := strings.TrimSpace(m.Uin)
				if u == "" || u == ConflictSelfUin {
					continue
				}
				aff[u] = br.Bond.Familiarity
			}
			if len(aff) > 0 {
				ctx.Affinity = aff
			}
		}
	}
	_ = selfUIN
	return ctx, nil
}

// withConflict §6.9 的**覆盖规则出口** —— 每一条出口都必须过它。
//
// ⚠️ **为什么是一个出口函数, 而不是在末尾调一次**:
//
// `GateGroupMessage` 有**四条早退** (被 @ / @ 别人 / 判不出对谁说的 / 系统故障),
// 而其中「判不出对谁说的」与「@ 的是别人」**恰恰是冲突最常见的两种形态** ——
// 一群人互相呛起来的时候, 谁也没在跟它说话。
//
// 第一版把冲突闸门挂在函数**末尾**, 于是那两条路直接 return, 永远走不到它。
// 现象是"测试说没有抬成发言", 而真正的原因是**它根本没被调用**。
//
// ⚠️ **为什么不干脆放进 defer**: §6.1 要求白名单检查在**任何 IO 之前**,
// 而冲突判定要读一次消息窗口。放进 defer 会让每条非白名单消息都付一次查询 ——
// 那正好把"九成判否"的成本前提拆了。
func (e *Engine) withConflict(sc SceneMsg, res GroupGateResult) GroupGateResult {
	e.applyConflictGate(sc, &res)
	return res
}

// applyConflictGate §6.9 **判定侧**的接线 —— 它是价值判断之后的**覆盖**规则。
//
// ⚠️ 顺序不能换: 先判"该不该说话"(§6.3 三态), 再判"这场合能不能劝"。
// 反过来的话, 一次冲突会把一条本来不该接的消息也变成发言。
func (e *Engine) applyConflictGate(sc SceneMsg, res *GroupGateResult) {
	if e.Store == nil || res == nil {
		return
	}
	if SceneKindOf(sc.SceneKey) != SceneKindGroup {
		return // 私聊里没有"劝架"这回事
	}
	cmsgs, pol, ok := e.conflictWindowOf(sc.SceneKey, 20)
	if !ok {
		return
	}
	ctx, ctxErr := e.conflictContextOf(sc.SceneKey, cmsgs, e.Cfg.Channel.SelfQQ)
	// §6.1 **故障与判不出来必须分得开** —— 冲突那一侧。
	//
	// ⚠️ 读库失败 → `ConflictNormal`: **不沉默也不介入**, 照常走正常说话路径。
	// 这一支是**硬要求**(纪律 #3「失败往正常说话倒」), 不是"少一点信息":
	// 吞掉它的话, `Stage` 留空会让 A1 不触发, 于是故障把它推向**更容易介入**的一侧。
	//
	// ⚠️ 而"场景没那行"(未提供)**不走这一支** —— 它由 `GateConflict` 照常判。
	var dec ConflictDecision
	if ctxErr != nil {
		dec = ResolveConflict(cmsgs, ConflictBaselineFrom(cmsgs, pol), ctx, ctxErr, pol)
		log.Warn("convo:conflict", "场景状态读取失败 → 冲突判定让路: %s", dec.Why)
	} else {
		dec = e.Store.GateConflict(sc.SceneKey, cmsgs, ctx, pol, sc.TS)
	}
	// ConflictNormal = 不介入, **但照常走正常说话路径** —— 它不是沉默, 什么都不用改。
	if dec.Verdict == ConflictNormal {
		return
	}
	// ⚠️ 决策必须落库, 否则 **A5 冷却永远读不到东西**, 同一对可以被无限次劝。
	if err := e.Store.RecordConflictDecision(sc.SceneKey, dec, sc.TS, pol); err != nil {
		log.Warn("convo:conflict", "冲突决策落库失败(冷却将失效, 可能反复劝同一对): %v", err)
	}
	switch dec.Verdict {
	case ConflictSilent, ConflictEscalate:
		// ⚠️ **`ConflictEscalate` 是硬线 4 的裁定, 它 != `ConflictSilent`。**
		//
		// 我第一版只写了 `ConflictSilent`, 而那个裁定**只**来自 `ctx.Undecidable`
		// (语义判不出)。也就是说: 硬线 4「涉及人身攻击 / 歧视 / 安全 → **不劝和**」
		// 差一点又变成一条写了但不生效的闸门 —— 而这一节整轮讲的就是这件事。
		//
		// 两个裁定在这里合流成同一个动作(沉默), 但它们的原因不同, 所以日志分开。
		// 硬线 4: 涉及人身攻击 / 歧视 / 安全 → **不劝和**, 沉默。
		//
		// ⚠️ 它盖掉价值判断的 speak —— 那正是这一条存在的全部意义。
		// ⚠️ 但它**不是系统故障**: 故障走的是 ConflictNormal(往说话倒),
		// 两者在这个 switch 里分得开。
		res.Stance = StanceSilent
		res.Reason = "§6.9 不劝和 (" + dec.Why + ")"
		log.Info("convo:conflict", "§6.9 不劝和 (%s): %s", dec.Verdict, dec.Why)
	case ConflictIntervene:
		// 介入 → 抬成发言。**怎么劝**由 conflictActNote 在提示词那一侧给。
		//
		// ⚠️ 这里**不记 Chime 配额**: 冲突介入的节制由 §6.9 自己的 A5 冷却 (120 分钟)
		// 承担, 而不是插话配额 —— 两套配额叠在一起会让"劝架"和"随口接一句"互相挤占,
		// 而那两件事的重要性差得远。
		res.Stance = StanceSpeak
		res.Reason = "§6.9 冲突介入 " + dec.Mode + " (" + dec.Why + ")"
		log.Info("convo:conflict", "§6.9 介入: mode=%s hits=%d/%d pair=%s",
			dec.Mode, dec.Hits, dec.Need, dec.Pair)
	}
}
