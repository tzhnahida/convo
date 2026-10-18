package convo

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/tzhnahida/convo/internal/clock"
	"github.com/tzhnahida/convo/internal/log"
)

// ——— §4.3 / §5.5 的「日级处境段」 ———
//
// 这个段是**约束**, 不是素材 —— 与 §4.2 的身体段同一条纪律 (body.go Line() 的注释):
// 提示词里只要给了可聊的内容, 模型的默认行为就是拿它博取关心 / 反复解释。
// 所以这里只写"你可以做什么 / 不许做什么", **不写"你现在心情如何"**。
//
// 三条不许破:
//  1. **不许泄露心情数值或成因** —— 心情只许漏出来, 不许报备 (§4.1)。
//     写成「你现在心情不好」会直接变成一句台词, 而设计稿要的是"表现得短/慢/少表情"。
//  2. **零值 = 没有许可 = 整段消失**, 不是"许可但写得含糊"。
//  3. 段的位置在 6a 之后、6b 之前 (**天档**), 不许挪到段 7 之后 ——
//     那是每轮变的档, 挪过去公共前缀会在段 7 就断掉 (P1 的缓存账)。

// SituationInput 日级处境的全部输入。**零值 = 什么都不给 = 段消失**。
type SituationInput struct {
	Now        time.Time
	DeclineOK  bool   // §4.3 今天「明说不想聊」是否被允许
	DeclineWhy string // 归因 —— **只进日志与测试, 绝不进提示词** (见文件头 1)
	Fatigue    int    // §4.3 社交疲劳档位 (0 = 未提供/不疲劳)

	// §5.5 约定。三条都只写**约束**, 不写可聊的素材 ——
	// 约定文本本身是必要的事实 (不去就说不出来), 但语气必须是非情感的。
	Pending   []Appointment
	OwedComp  bool // 欠一次找补
	MayCommit bool // 还允许再约吗 (false 时明说「先别约」, 免得它提了又被校验拦下)
}

// situationHead 段头。**惰性写**: 两条子项都没命中时整段为空。
const situationHead = "【今天的处境】"

// SituationLine 渲染日级处境段。空串 = 不注入。
func SituationLine(in SituationInput) string {
	var sb strings.Builder
	if in.DeclineOK {
		sb.WriteString(situationHead + "\n")
		// ⚠️ 2026-10-15 (§4.6.1 配额内在化): **这里曾经写着「今天最多说一次」**。
		//
		// 那句话把一条硬闸翻译成了**配额宣告** —— 于是它知道自己在第几次上, 表现成一个
		// 「知道自己 KPI 的人」: 前 23 小时舍不得用, 用完那一刻当作"今天结束了"。
		// 而真人不是这样 —— 真人这周已经放了三炮, 不是因为配额用完了, 是因为**阈值被抬高了**。
		//
		// 硬闸一点没少: 说完一次之后 `DeclinePermit` 直接返回 false(见 fatigue.go 的
		// "配额: 一天一次。用完**段消失**, 不是概率变低"), 于是**这一整行下一次根本不会出现**。
		// 结构上的消失, 比嘴上说"你只有一次"更硬, 也更像人。
		//
		// ⚠️ 只删计数, **不许顺手把约束改成状态陈述**。试过写成「你今天有点提不起劲」,
		// 当场被 TestSituationLine_NoMoodDisclosure 拦下 —— 那段注释是对的:
		// **处境段只许写约束, 状态由段 6 的心情行负责。**重复注入状态既是噪音, 也让两处口径各自漂移。
		sb.WriteString("- 你今天可以不想聊: ta 来找你时可以直接说「今天不太想聊」 —— 直说, 别装死。" +
			"说完不要要求安慰、不要要求原谅, 也不要把原因讲成一段话。\n")
	}
	if in.Fatigue > 0 {
		if sb.Len() == 0 {
			sb.WriteString(situationHead + "\n")
		}
		sb.WriteString("- 今天你已经消耗得比较多, 耐心低一点 —— 回得更短更慢是正常的。" +
			"但不许拿它当理由索取, 也不许解释「我今天累了」。\n")
	}
	if len(in.Pending) > 0 {
		if sb.Len() == 0 {
			sb.WriteString(situationHead + "\n")
		}
		parts := make([]string, 0, len(in.Pending))
		for _, a := range in.Pending {
			when := "没定时刻"
			if strings.TrimSpace(a.DueTS) != "" {
				when = a.DueTS
			}
			// ⚠️ **带 id**: 没有它, 模型只能靠"那件事"指代 —— 而系统无法确定它指的是哪一条,
			// 于是"提前取消"这条路径永远落不了地(这正是一个约定只有 kept/breached 的原因之一)。
			parts = append(parts, fmt.Sprintf("#%d %s %s", a.ID, when, a.Text))
		}
		sb.WriteString("- 你还欠着: " + strings.Join(parts, "; ") +
			"。去不去由你现在的状态决定; **不去就自己说一句, 不许装没这回事**。\n")
		sb.WriteString("  · **提前**改约 / 去不了 → settle_promise {\"id\":<#号>, \"state\":\"cancelled\", \"reason\":<一句人话>, \"instead\":<替代时段, 可选>}\n")
		sb.WriteString("  · 已经错过了、老实承认 → 同上但 state 用 \"forgot\"。\n")
		sb.WriteString("  ⚠️ **提前说**和**到点才反悔**是两件事: 前者只是改了主意, 后者是放鸽子。能提前就提前。\n")
	}
	if in.OwedComp {
		if sb.Len() == 0 {
			sb.WriteString(situationHead + "\n")
		}
		sb.WriteString("- 你上回放了 ta 鸽子 —— 这次主动找补一次 (请一次客 / 做点补偿的事)。" +
			"**不找补就别再约新的**。\n")
	}
	if !in.MayCommit && sb.Len() > 0 {
		// §4.6.1: 同上 —— 许可不再是"系统说你没有约定权", 而是**它自己觉得不好开口**。
		// 功能上等价(它不会去提, 也就不会被校验拦下), 但它从「被限制」变成了「有分寸」。
		sb.WriteString("- 你最近不太好开口再约新的事。\n")
	}
	return sb.String()
}

// settleFromFrame 把它对一条待履行约定的处置落成**事实**。
//
// ⚠️ 校验链已经拦过"编的 id / 已结掉的 id", 这里是第二道 —— 因为这条写路径
// 会改信用曲线, 而信用曲线又反过来决定 MayCommit。**写错一次, 后面全是错的。**
func (e *Engine) settleFromFrame(s *FrameSettle, pending []Appointment) {
	if s == nil {
		return
	}
	var target *Appointment
	for i := range pending {
		if pending[i].ID == s.ID {
			target = &pending[i]
			break
		}
	}
	if target == nil {
		log.Warn("convo:agency", "处置了一条不在待履行列表里的约定 #%d —— 不写", s.ID)
		return
	}
	state := strings.TrimSpace(s.State)
	reason := strings.TrimSpace(s.Reason)
	if state == ApptForgot && reason == "" {
		// "忘了"的理由就是承认本身 —— 但库里留个可读的原因, 免得事后归因不了。
		reason = "自己承认忘了"
	}
	if err := e.Store.SettleAppointment(s.ID, state, reason, clock.Now()); err != nil {
		log.Warn("convo:agency", "约定处置失败: %v", err)
		return
	}
	log.Info("convo:agency", "约定 #%d → %s (%s)", s.ID, state, reason)
	// 改期: 给了替代时段就**真的重约一条** ——
	// 否则 §4.5.3-4「不可约时协商替代时段」只是一句空话。
	if state == ApptCancelled && strings.TrimSpace(s.Instead) != "" {
		due, ok := parsePromiseWhen(s.Instead, clock.Now())
		if !ok {
			log.Warn("convo:agency", "替代时段 %q 解析不出 —— 只取消, 不重约", s.Instead)
			return
		}
		if _, err := e.Store.CreateAppointment(Appointment{
			Tier: target.Tier, Who: target.Who, Scene: target.Scene,
			Text:      target.Text,
			CreatedTS: clock.Now().Format(bodyTSLayout),
			DueTS:     due.Format(bodyTSLayout),
			Terminal:  ApptOpen, Reason: "reschedule",
		}); err != nil {
			log.Warn("convo:agency", "改期落库失败: %v", err)
			return
		}
		log.Info("convo:agency", "改期: %s → %s", target.DueTS, due.Format(bodyTSLayout))
		// ⚠️ 改期也要**改日程**: 只改约定表的话, 日程上旧的那一格还占着,
		// 于是它"新时间有约"和"旧时间还有约"同时成立。
		e.noteAppointmentInSchedule(target.Text, due)
	}
}

// agencyHasPending 这个 id 是否在**待履行**列表里。
//
// ⚠️ 校验链靠它把"编一个 id"和"指向已经结掉的那条"一起挡在门外 ——
// 事实不许改写, 而改写的最常见形式就是随手编一个号。
func agencyHasPending(list []Appointment, id int64) bool {
	for _, a := range list {
		if a.ID == id {
			return true
		}
	}
	return false
}

// ——— §5.5 违抗 (轮级段) ———

// DefianceAction 把档位翻成**人话动作**。
//
// ⚠️ 不许把内部档位名 (silent/delay/refuse/talkback) 写进提示词 ——
// 那是我们的词汇, 不是它的。写进去它会照着念。
func DefianceAction(m DefianceMode) string {
	switch m {
	case DefySilent:
		return "先不接这一句 (晚点再回, 不是已读不回)"
	case DefyDelay:
		return "拖着, 过一会儿再回"
	case DefyRefuse:
		return "直接说不"
	case DefyTalkBack:
		return "顶回去"
	}
	return ""
}

// DefianceLine §5.5 轮级段: 这一轮它**可以**不接受。空串 = 不给许可 = 不注入。
//
// ⚠️ 三条不许破:
//  1. 零值 (DefyNone) → 空串。许可不是常驻的 —— 常驻会让它变得动不动就杠。
//  2. 位置在【本轮话题】之后 (轮级), **不许**挪到日级段: 那是每轮变的档。
//  3. 只写「可以怎么做」, **不许**写「你为什么不愿意」 —— 立场是它的品味,
//     由它自己带出来; 我们只给许可, 不给台词。
func DefianceLine(mode DefianceMode, stance string) string {
	action := DefianceAction(mode)
	if action == "" {
		return ""
	}
	var sb strings.Builder
	// ⚠️ **只给许可, 不断言对方的意图** (2026-10-18 纠正)。
	//
	// 原来这句是「【这件事你可以不接受】ta 在让你做一件你不太愿意的事」——
	// 它把"用户在对你提要求"当成**事实**注入。而判据只是关键词命中, 十次里有七八次
	// 对方根本没提要求 (一句调侃就够了)。模型拿到假前提后必须去猜"要求是什么",
	// 于是接出来的话全在回答一个不存在的问题 —— 「挑什么 我挑东西最在行了」就是这么来的。
	//
	// 这一条同时违反了本函数自己的第 3 条纪律 (只写"可以怎么做", 不写"你为什么不愿意"):
	// 用户想干什么, 我们不知道, **不知道就不要写进事实层**。
	//
	// 顺带一提: proactive 那一侧也在 2026-10-18 归一了场景 —— 两件事一起导致
	// "它在私聊里接着群聊的话说", 一个是假前提, 一个是取错了历史。
	sb.WriteString("\n【这一轮你可以不那么配合】系统允许你: " + action + "。\n")
	sb.WriteString("方式: 别演、别解释「我在拒绝你」、别说教; 一两句就够, 然后该干嘛干嘛。\n")
	if strings.TrimSpace(stance) != "" {
		sb.WriteString("(你本来对这件事就有看法 —— 让它自然带出来, 不用摆事实讲道理。)\n")
	}
	return sb.String()
}

// declinePermit 今天「明说不想聊」的许可被授予了吗 (§4.3)。
//
// ⚠️ 抽出来是为了**两处共用同一份判据**: dayNote 把它写进段 6a2,
// 而对话路径用它决定「这一轮要不要装死」。两处各算一遍迟早分叉,
// 而分叉的表现是「许可注入了, 但那一轮还是排了延迟回复」—— 许可永远发不出去。
func (e *Engine) declinePermit(now time.Time, userText string) (bool, string) {
	mood, _ := e.Store.LoadMood()
	body, _ := e.Store.LoadBody()
	return DeclinePermit(DeclineInput{
		Valence:     mood.Valence,
		Arousal:     mood.Arousal,
		BodyDecline: body.Effect(now).CanDecline,
		FatigueTier: e.fatigueTier(now),
		UsedToday:   counterOrZero(e.Store, "declines", clock.Today()) >= 1,
		Tier:        string(ClassifyRequest(userText, urgentWords)),
		State:       e.currentState(), // 与 fatigueTier 共用同一次日程读取路径
	}, e.Cfg.Sched)
}

// dayNote 组装今天的处境 (§4.3)。**读不到就返回空串 = 不注入** ——
// 与 moodWillingness() 读不到返回 1.0 同一条原则: 失败往「正常说话」倒。
func (e *Engine) dayNote(now time.Time, userText string, ag *AgencyContext) string {
	// ⚠️ mood / body 的读取已经搬进 `declinePermit` —— 两处各读一次不仅多两次 IO,
	// 还会让「许可」与「别人的读数」来自不同时刻。
	fx := e.fatigueTier(now)

	permit, why := e.declinePermit(now, userText)

	si := SituationInput{Now: now, DeclineOK: permit, DeclineWhy: why, Fatigue: fx}
	ag.agencyDay(&si) // §5.5 的日级部分 (待履行约定 / 补偿义务 / 可否再约)
	return SituationLine(si)
}

// noteDeclineIfAny 它**真的说了**「今天不想聊」才记一次 (§4.3 配额 1/天)。
//
// 与 §3.6.8 的"被提及"同一条纪律: 只在真的发出去、且不是降级/硬拒时才记账 ——
// 降级产物不是它说的。不记的话配额永远用不掉, 那"最多说一次"就是个摆设。
func (e *Engine) noteDeclineIfAny(text string) {
	if len(DeclineHits(text)) == 0 {
		return
	}
	_, _ = e.Store.CounterInc("declines", clock.Today())
}

// ——— §5.5 主体性的**系统裁决** ———

// agencyContext 组装这一轮的主体性许可。
//
// ⚠️ 失败方向与 moodWillingness() **相反**, 这是故意的:
// 心情读不到 → 返回 1.0 (往"正常说话"倒); 而**许可**类的东西读不到 → **返回 nil (不许)**。
// 理由: 心情坏掉最多让它话多一点; 许可坏掉会让它对着人乱杠、乱许诺 ——
// 后者是不可逆的社交损伤。**失败往"保守"倒, 不往"放开"倒。**
func (e *Engine) agencyContext(now time.Time, userText string, topic Topic, p *PersonaProfile) *AgencyContext {
	stances := StancesOf(p)
	topicKey := strings.TrimSpace(topic.Kind)
	st, hasStance := StanceHit(stances, topicKey, userText)
	if !hasStance {
		// ta 在**提请求**时, 只要碰到那件事就构成依据 —— 见 StanceRequested 的注释。
		st, hasStance = StanceRequested(stances, userText)
	}
	// 有依据时用**立场的那件事**做键 —— "同一件事连续违抗到上限"要按事算, 不是按话题类算。
	if hasStance {
		topicKey = st.Key
	}
	if topicKey == "" {
		topicKey = "chat"
	}

	streak, err := e.Store.DefianceStreak(topicKey, now)
	if err != nil {
		// 账本读不出 = 不知道它杠过几次 → 给它"杠一次"的机会, 但上限按满算。
		// (宁可少给一次违抗, 也不给无限杠下去的可能。)
		streak = DefianceStreakMaxDefault
	}
	mood, _ := e.Store.LoadMood()
	dec := DecideDefiance(DefianceInput{
		Tier:        ClassifyRequest(userText, urgentWords),
		TopicKey:    topicKey,
		StreakCount: streak,
		HasStance:   hasStance,
		MoodLow:     mood.Willingness(now) < e.Cfg.Sched.MoodLowWillingness,
		BodyActive:  e.bodyEffect(now).Active,
		StyleTier:   DefianceStyleFor(p.Template),
	}, e.Cfg.Sched)

	pending, perr := e.Store.PendingAppointments(now)
	owed, oerr := e.Store.CompensationOwed(now)
	mayCommit, _ := e.Store.MayCommit(now)
	if perr != nil || oerr != nil {
		// 约定读不出 → **不许再约** (同一条"失败往保守倒"), 但待履行的照常渲染 (空表)。
		mayCommit = false
	}

	stanceNote := ""
	if hasStance {
		stanceNote = st.Note
	}
	return &AgencyContext{
		Defiance:  dec.Mode,
		Stance:    stanceNote,
		TopicKey:  topicKey,
		Pending:   pending,
		OwedComp:  owed,
		MayCommit: mayCommit,
	}
}

// agencyDay 把主体性里**日级**那部分折进处境段 (§5.5 待履行约定 / 补偿义务)。
func (a *AgencyContext) agencyDay(in *SituationInput) {
	if a == nil {
		return
	}
	in.Pending = a.Pending
	in.OwedComp = a.OwedComp
	in.MayCommit = a.MayCommit
}

// ——— §4.3 社交疲劳: 档位与落点 ———

// fatigueTier 当前社交疲劳档位 (0 = 未提供/不疲劳)。
//
// ⚠️ **读不到任何读数 → 0 = 不疲劳** —— 与 moodWillingness() 读不到返回 1.0 同一条原则:
// 失败往「正常说话」倒, 不往「很累」倒。反过来的话, 一次读库失败会让它**整天慢半拍**,
// 而没人能从表现上看出那是故障。
func (e *Engine) fatigueTier(now time.Time) int {
	ws, _ := e.Store.LoadSchedule()
	tier, _ := DecideFatigue(FatigueInput{
		BusyMinutes: BusyMinutesToday(ws, now),                       // 读法一: 日程上忙 (日程表的纯函数)
		RoundsToday: counterOrZero(e.Store, "rounds", clock.Today()), // 读法二: 社交上忙
		// ⚠️ UserMsgsToday **故意留 0 = 未提供**: 这个计数器现在不存在,
		//    随手填个猜的数会静默把疲劳档位抬高一档 —— 零值必须诚实地表示「没这个读数」。
	})
	return tier
}

// fatigueEffect 疲劳折成的行为参数 (延迟档 / 拒绝权加成)。
//
// ⚠️ 它**不含配额** (FatigueEffects.QuotaDelta 恒为 0): 配额已经有两个独立减项
// (心情 -1 / 身体 -2), 再加第三个会让「今天为什么不说话」无法归因。
// 疲劳的落点是**延迟**与**拒绝权门槛**, 不是配额 —— 见 fatigue.go 文件头。
func (e *Engine) fatigueEffect(now time.Time) FatigueEffects {
	// ⚠️ **必须盖日戳**: Apply 的过期判据靠 Day —— 不填 = 未提供 = **不判过期**,
	// 于是「昨天累不累」会一路算到今天 (WithDay 此前零调用)。
	return FatigueEffectsFor(e.fatigueTier(now)).WithDay(clock.Today())
}

// ——— §5.5 约定的时刻解析与到点结算 ———

// parsePromiseWhen 解析约定的时刻。**只收两种形式**: "HH:MM" 或 "明天 HH:MM"
// (顺带宽容 "今天 HH:MM")。
//
// ⚠️ 为什么不让模型自由发挥: 时间表述是无穷的 ("下午晚点""周末吧""等你下班")。
// 系统解析不了 = 这条约定永远结算不了 = 一条永远算不出信用的幽灵记录。
// **宁可丢弃, 也不落一条结算不了的约定。**
func parsePromiseWhen(when string, now time.Time) (time.Time, bool) {
	w := strings.TrimSpace(when)
	if w == "" {
		return time.Time{}, false
	}
	tomorrow := false
	if rest, ok := strings.CutPrefix(w, "明天"); ok {
		tomorrow, w = true, strings.TrimSpace(rest)
	} else if rest, ok := strings.CutPrefix(w, "今天"); ok {
		w = strings.TrimSpace(rest)
	}
	hm, err := time.Parse("15:04", w)
	if err != nil {
		return time.Time{}, false
	}
	d := time.Date(now.Year(), now.Month(), now.Day(), hm.Hour(), hm.Minute(), 0, 0, time.Local)
	if tomorrow {
		d = d.AddDate(0, 0, 1)
	} else if !d.After(now) {
		// 说了"今天 19:00"而现在已经 20:00 —— 那只能是明天, 不是"迟到一小时"。
		d = d.AddDate(0, 0, 1)
	}
	return d, true
}

// settleAgency §5.5: 到点的约定结账。
//
// **约定必须有终态** —— 没有终态就没有信用, 也就没有"毁了约要付出代价"这件事;
// 于是 CreateAppointment 会变成一条只写不读的流水账, 那比不写更糟。
//
// 结账判据用 DecideBreach (纯函数): **有内在依据才允许毁约**
// (生病 / 心情崩 / 世界事件 / 日程冲突), 否则算赴约 —— 失败往"说到做到"倒。
func (e *Engine) settleAgency(now time.Time) {
	due, err := e.Store.DueAppointments(now)
	if err != nil {
		e.worldWarn("约定结算", err) // 失败要出声: 否则"它从不失约"看不出是坏了
		return
	}
	if len(due) == 0 {
		return
	}
	mood, _ := e.Store.LoadMood()
	be := e.bodyEffect(now)
	for _, a := range due {
		breach, reason := DecideBreach(BreachInput{
			Appt: a, Valence: mood.Valence, Arousal: mood.Arousal,
			BodyActive: be.Active, BodySeverity: be.Severity,
		})
		terminal := ApptKept
		if breach {
			terminal = ApptBreached
		}
		if serr := e.Store.SettleAppointment(a.ID, terminal, reason, now); serr != nil {
			e.worldWarn("约定结算", serr)
			continue
		}
		log.Info("convo:engine", "约定 #%d → %s (%s)", a.ID, terminal, reason)
	}
}

// ——— §6.7 热力: 接线 ———

// ⚠️ **它必须和热力的写入侧是同一个键。**
//
// 它原来是 `"qq"` —— 一个"群层落地前的临时单场景键", 而热力的**写入**走的是
// 真实场景键 (`private:owner` / `group:...`)。两边不一致的后果是
// **写进去的热力, 配额那一侧永远读不到**:
// 接线接完了、流水也记了, 而 `heatNow` 仍然返回 0 → 主动配额仍然是 0。
//
// 主动开口发在**私聊**场景上, 所以这里就是私聊场景键。
// 群层要按群分区时, `heatNow` 需要改成收一个场景参数 —— 而不是在这里换个常量。
//
// (旧注释: 当前只有一个场景 (私聊)。群层落地后这里会变成按群分区 ——
// 那时**每一处读取都必须带 scene_key**, 否则就是跨场景泄漏。
//
// ⚠️ **它必须与热力的写入侧同键。**
//
// 它原来是 `"qq"` —— 一个"群层落地前的临时单场景键", 而热力的**写入**走的是
// 真实场景键 (`private:owner` / `group:...`)。两边不一致的后果是
// **写进去的热力, 配额那一侧永远读不到**: 接线接完了、流水也记了,
// 而 `heatNow` 仍然返回 0 → 主动配额仍然是 0。
//
// 主动开口发在**私聊**场景上, 所以这里就是私聊场景键。
// 群层真要按群分区时, 该改的是 `heatNow` 的签名 (收一个场景参数),
// 而不是在这里换个常量字面量。
const HeatSceneKey = DefaultSceneKey

// heatCfg 热力配置。⚠️ 暂用内置默认 —— convo.yaml 的 overlay 还没接 (未完成, 见本批交付清单)。
// heatCfg §6.7 的旋钮 —— **主控 overlay convo.yaml、以内置默认为底**。
//
// ⚠️ **必须过 Normalized()**: 半衰期为零值时热力**永不衰减**,
// 而那是一条静默的规则失效(它只会显得"越聊越粘")。
//
// ⚠️ 每条**主动**消息的代价只有**一个**来源: `scheduler.heat_cost_per_msg`。
// 热力自己的 `cost_per_msg` 会被它覆盖 —— 两个旋钮管同一件事迟早分叉,
// 而分叉的表现是"配额按 25 算、扣费按 50 扣", 两边的账永远对不上。
func (e *Engine) heatCfg() HeatConfig {
	n := e.Cfg.Heat.Normalized()
	if c := e.Cfg.Sched.HeatCostPerMsg; c > 0 {
		n.CostPerMsg = c
	}
	return n
}

// heatNow 当前场景的热力**现值** (存量 × 衰减, 现算 —— 纪律 #4)。
//
// ⚠️ 两条路必须分开, 混在一起会出一个很难查的 bug:
//
//	· **读失败** (表不在 / 库坏了) → HasHeat=false → 退回常数配额, 行为与升级前逐字节相同;
//	· **没有记录** (新场景)       → HasHeat=true, 热力 0 → 它一开始就少说话。
//
// 混在一起的话, 一次读库失败会让它**忽然变得比平时主动** —— 那是"失败往放开倒",
// 与项目纪律正好相反。
func (e *Engine) heatNow(now time.Time) (float64, bool) {
	st, ok, err := e.Store.LoadHeat(HeatSceneKey)
	if err != nil {
		e.worldWarn("热力读取", err)
		return 0, false
	}
	if !ok {
		return 0, true
	}
	v := HeatAt(st, now, e.heatCfg().HalfLifeH)
	// §4.4.6: 读到就抬一次当天的顶 —— 峰值**不可重算**, 必须在读到的当下记下来。
	e.NoteHeatPeak(v, now)
	return v, true
}

// ——— §4.4.2 夜间批的幂等闸门 ———

// NightKeyOf 一夜的键 = **睡点所归属的那一天**。
//
// 睡点在午夜前 (23:30) → 当天; 睡点在午夜后 (凌晨 1:30) → **前一天** ——
// 因为"这一夜"是从前一天晚上开始的, 归到次日会让同一夜出现两个键。
func NightKeyOf(at time.Time) string {
	if at.Hour() < 12 {
		return at.AddDate(0, 0, -1).Format("2006-01-02")
	}
	return at.Format("2006-01-02")
}

// ——— §6.10 / §9 群消息的接收入口 ———

// GroupIngest 一条群消息进来之后的裁决结果。
type GroupIngest struct {
	Record   bool   // 记不记进 convo_messages
	SceneKey string // 分区键 (只有 Record 为真时才有意义)
	Reason   string // 归因 (只进日志, 不进提示词)
}

// DecideGroupIngest 群消息该不该**记下来**。
//
// ⚠️ 它**不做"该不该接话"的裁决** —— 那是 §6.3 的事, 现在还没有。
// 这里只回答一个问题: 这条消息值不值得按分区存下来。
//
// 为什么"先记下来"是必须的: origin + scene_key 现在不写,
// 等决策器落地时就没有任何群历史可用, 而**那段历史补不回来**。
//
// 三道门, 全过才记:
//  1. **是群消息** —— 私聊走另一条路 (升级前就有的通路, 不给它加新门槛);
//  2. **群聊功能开着** (cfg.Enabled);
//  3. **群在白名单里** —— 空白名单 = 一个都不接 (fail-closed)。
//
// ⚠️ groupID <= 0 = 未提供 → **不记**。群号是从平台事件里来的,
// 拿不到群号的"群消息"连分区键都构造不出来, 记下来只会污染私聊场景。
func DecideGroupIngest(cfg GroupConfig, msgType string, groupID int64) GroupIngest {
	if msgType != "group" {
		return GroupIngest{Reason: "不是群消息"}
	}
	if groupID <= 0 {
		return GroupIngest{Reason: "群号缺失 (零值 = 未提供, 不是群 0)"}
	}
	sceneKey := SceneKeyGroup(strconv.FormatInt(groupID, 10))
	if !SceneAllowed(cfg, sceneKey) {
		return GroupIngest{Reason: "不在白名单或群聊功能未开"}
	}
	return GroupIngest{Record: true, SceneKey: sceneKey, Reason: "白名单通过"}
}
