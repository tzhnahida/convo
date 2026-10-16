package convo

import (
	"fmt"
	"strings"

	"github.com/tzhnahida/convo/internal/clock"
	"github.com/tzhnahida/convo/internal/log"
)

// ─────────────────────────────────────────────────────────────
// §5.5 **自主预约**: 它自己往次日行程里加一件事
//
// 设计稿那段的第一句是重点: 「一个只能被约、从不主动安排自己生活的角色,
// 和"永远答应"是同一个东西的另一面。」
//
// ⚠️ 它还是**拒绝的依据**。用户临时约它, 它能答"那个时间我约了自己去书店" ——
// **没有自主预约, 拒绝就是无依据的**, 而无依据的拒绝 = 噪声(违反 M7: 不许随机掷骰子)。
//
// ⚠️ **上限 ≤2 条/天, 判在写路径上, 不判在校验链里。**
//
// 理由是 §4.6.1 那条区分: 这是**生活上限**(「不许为了显得忙而堆」), 要**内在化**。
// 校验链的违规文本会被塞进重试提示词 —— 判在那里, 就等于亲口告诉它"你一天只能排两次",
// 于是它会在边界上表现异常: 那不是人, 是一个知道自己 KPI 的人。
// 超限的处理是**丢掉这一帧字段 + 出声记日志**, 不是"训它一顿让它改"。
// ─────────────────────────────────────────────────────────────

// SelfPlanDailyCap 每天的自主预约硬上限 (§5.5)。
//
// ⚠️ **这个数字不许进提示词。** 见文件头。
const SelfPlanDailyCap = 2

// SelfPlanConflictMin 与用户约定相距多少分钟算"撞了"。
//
// ⚠️ 取 30 而不是 0: 一个人不能在同一个小时内出现在两个地方。
// 也不能取太大 —— 误判会把它正常的安排拒掉, 而拒绝的代价是整轮重说。
const SelfPlanConflictMin = 30

// userItemNear 次日行程里有没有一条**用户约定**离 at 太近; 有就返回那条的内容。
func userItemNear(plan DayPlan, at string, withinMin int) string {
	m, err := parseHM(at)
	if err != nil {
		return ""
	}
	for _, it := range plan.Items {
		if it.From != DayFromUser {
			continue
		}
		u, uerr := parseHM(it.At)
		if uerr != nil {
			continue
		}
		d := u - m
		if d < 0 {
			d = -d
		}
		if d < withinMin {
			return it.At + " " + it.What
		}
	}
	return ""
}

// AddSelfPlan 把一条自主预约**并进次日行程**。
//
// 返回 (加进去没有, 说明)。失败**不阻断回复** —— 它话已经说出去了,
// 只是那件事没被记成"安排"; 上限与撞车都在这里判。
func (e *Engine) AddSelfPlan(sp *FrameSelfPlan, tomorrow DayPlan) (bool, string) {
	if e.Store == nil || sp == nil {
		return false, "没有 store"
	}
	if strings.TrimSpace(sp.What) == "" {
		return false, "没写内容"
	}
	day := TomorrowKey(clock.Now())
	// 上限: 生活上限, 内在化 —— 超了就**不加**, 并出声。
	if n := counterOrZero(e.Store, "selfplan", clock.Today()); n >= SelfPlanDailyCap {
		log.Info("convo:selfplan", "自主预约已到今天的上限, 这一条不并入日程: %s", sp.What)
		return false, "今天已经排过了"
	}
	ws, _ := e.Store.LoadSchedule()
	if DayItemInSleep(ws, day, sp.At, DurMinutes(sp.Dur)) {
		return false, "那个时间它在睡觉"
	}
	if u := userItemNear(tomorrow, sp.At, SelfPlanConflictMin); u != "" {
		return false, "和 ta 的约定撞了: " + u
	}
	item := DayItem{
		At: sp.At, What: strings.TrimSpace(sp.What), Dur: strings.TrimSpace(sp.Dur),
		With: strings.TrimSpace(sp.With), Place: strings.TrimSpace(sp.Place),
		From: DayFromSelf,
	}
	plan := tomorrow
	plan.Day = day
	plan.Items = append(append([]DayItem(nil), plan.Items...), item)
	plan = NormalizeDayPlan(plan, ws)
	// ⚠️ 条数封顶之后可能把**刚加的那条**挤掉 —— 那就等于没加成, 如实说。
	if !dayPlanHasWhat(plan, item.What) {
		return false, fmt.Sprintf("明天的行程已经满了 (%d 条)", DayPlanMaxItems)
	}
	if err := e.Store.SaveDayPlan(e.ownerKey(), plan, ws); err != nil {
		return false, "落库失败: " + err.Error()
	}
	if _, err := e.Store.CounterInc("selfplan", clock.Today()); err != nil {
		// ⚠️ 计数失败**要出声**: 它意味着上限永远不会触发, 也就是"可以无限排"。
		log.Warn("convo:selfplan", "自主预约计数失败(上限将不会触发): %v", err)
	}
	log.Info("convo:selfplan", "§5.5 自主预约: 明天 %s %s", item.At, item.What)
	return true, fmt.Sprintf("明天 %s %s", item.At, item.What)
}

func dayPlanHasWhat(p DayPlan, what string) bool {
	for _, it := range p.Items {
		if it.What == what && it.From == DayFromSelf {
			return true
		}
	}
	return false
}

// TomorrowPlanLine 渲染「明天的事」段。空行程 → 空串(一个字都不加)。
//
// ⚠️ **明天必须给它看。** 自主预约排的是**次日**, 而它的提示词里原本只有**今天** ——
// 看不见明天的安排, "那个时间我约了自己去书店"就说不出来, 用途 2 直接落空。
func TomorrowPlanLine(p DayPlan) string {
	if len(p.Items) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n【明天的事】(系统事实 —— 你明天已经排了的; 有人约你就照着这个答)\n")
	for _, it := range p.Items {
		with := ""
		if it.With != "" {
			with = " (和 " + it.With + ")"
		}
		place := ""
		if it.Place != "" {
			place = " @" + it.Place
		}
		fmt.Fprintf(&sb, "- %s %s%s%s\n", it.At, it.What, with, place)
	}
	return sb.String()
}
