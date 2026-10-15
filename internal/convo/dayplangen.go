package convo

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tzhnahida/VocabLock/internal/clock"
	"github.com/tzhnahida/VocabLock/internal/log"
)

// ─────────────────────────────────────────────────────────────
// §4.5 层 2 的生成: **每天夜里给明天排一份具体行程**
//
// 「日程太泛了」是这一层存在的全部理由。层 1 给的是 focus/busy/slack/free ——
// 那是**意愿**, 从它推不出"它今天在干什么"。这一层给的是
// **几点、和谁(或者自己)、干什么**。
//
// ⚠️ **生成失败 → 今晚就不排, 不要兜底编一份。**
//
// 这一条与事件池(GenerateNightPool **永远非空**)是**故意相反**的, 理由:
//   · 事件池驱动心情, 空了整条世界→心情的通道就断, 所以它必须有确定性兜底;
//   · 行程是"它今天干了什么"的**事实陈述**。一份编出来的行程, 白天一被追问就穿帮 ——
//     而"今天没排上"的代价只是段消失, 对话退回到今天的样子, **没有任何损失**。
// 所以这里的选择是:**宁可没有, 不可现编**。
// ─────────────────────────────────────────────────────────────

// DayPlanInput 排行程要用的**全部素材**。每一样都能追到来源。
type DayPlanInput struct {
	Day      string // 要排的那一天 YYYY-MM-DD
	Weekday  string // mon..sun
	Profile  *PersonaProfile
	Bible    WorldBible
	HasBible bool
	// Events 事件池 / 还在场上的事 —— from=event 的素材。
	Events []EventNote
	// NightNotes 前一夜 AI 互聊 / 配角群聊的回流 —— from=night 的素材。
	NightNotes []string
	// Pending 还没兑现的约定 —— from=user 的素材。
	Pending []Appointment
	// SleepStart/SleepEnd 睡眠窗, 用来告诉它"别排在这段时间"。
	SleepStart, SleepEnd string
	EnvLine              string
}

// dayPlanPrompt 生成提示词。
//
// ⚠️ 「五种来源」在这里是**给模型的规则**, 而在 SaveDayPlan 里是**硬校验** ——
// 两道都要有: 只写规则, 模型会偶尔越界; 只写校验, 它会白跑一趟才发现被拒。
func dayPlanPrompt(in DayPlanInput) string {
	var b strings.Builder
	b.WriteString("你是这个角色本人。现在是夜里, 你要给**明天**排一份具体的行程 ——\n")
	b.WriteString("不是「几点到几点忙」这种抽象档, 而是**具体到几点、和谁(或者自己)、干什么**。\n\n")
	b.WriteString("【你是谁】\n")
	if in.Profile != nil {
		fmt.Fprintf(&b, "%s: %s\n", in.Profile.Name, in.Profile.Description)
		if in.Profile.Personality != "" {
			fmt.Fprintf(&b, "性格: %s\n", in.Profile.Personality)
		}
	}
	if in.HasBible && in.Bible.Self != "" {
		fmt.Fprintf(&b, "你在这个世界里的位置: %s\n", in.Bible.Self)
	}
	b.WriteString("\n【你生活里的人】(只能从这些人里挑「和谁」)\n")
	wrote := false
	// ⚠️ §3.6.5c: **只用当下生活层** —— 排明天的行程时把"已经搬走的人"排进去,
	// 就是让它在明天若无其事地复活(而不许复活正是那一条的正文)。
	for _, m := range ActiveCast(in.Bible) {
		if strings.TrimSpace(m.Name) == "" {
			continue
		}
		wrote = true
		extra := m.Status
		if extra == "" {
			extra = m.History
		}
		fmt.Fprintf(&b, "- %s (%s): %s\n", m.Name, relLabel(m.Relation), extra)
	}
	if !wrote {
		b.WriteString("(名单是空的 —— 那明天就多排一点自己一个人的事)\n")
	}
	if len(in.Events) > 0 {
		b.WriteString("\n【明天可能发生的事】(可用可不用)\n")
		for _, e := range in.Events {
			fmt.Fprintf(&b, "- %s\n", e.Text)
		}
	}
	if len(in.NightNotes) > 0 {
		b.WriteString("\n【昨晚跟别的朋友聊到的事】(可用可不用)\n")
		for _, n := range in.NightNotes {
			fmt.Fprintf(&b, "- %s\n", n)
		}
	}
	if len(in.Pending) > 0 {
		b.WriteString("\n【你还欠着的事】(有就应当排进去)\n")
		for _, a := range in.Pending {
			fmt.Fprintf(&b, "- %s\n", a.Text)
		}
	}
	if in.EnvLine != "" {
		fmt.Fprintf(&b, "\n【明天的天气】%s\n", in.EnvLine)
	}
	b.WriteString("\n要求:\n")
	b.WriteString("1. 输出纯 JSON, 不要解释、不要 markdown 代码块:\n")
	b.WriteString("   {\"day\":\"" + in.Day + "\",\"items\":[{\"at\":\"15:30\",\"dur\":\"40m\",\"with\":\"可颂\",\"what\":\"去分拣仓帮可颂搬箱子, 她欠我一顿\",\"place\":\"3 号仓\",\"from\":\"cast\"}]}\n")
	b.WriteString("2. **4-7 条**。太少没有生活感, 太多就变成连续剧了。\n")
	b.WriteString("3. \"with\" 留空 = **你自己一个人**。至少每 3 条里有 1 条是自己 ——\n")
	b.WriteString("   **一个人待着也是生活**, 不许排成一张社交任务表。\n")
	b.WriteString("4. \"from\" **只能**是这五个之一:\n")
	b.WriteString("   cast = 上面【你生活里的人】里的某个人; event = 上面【明天可能发生的事】里的某件;\n")
	b.WriteString("   self = 你自己想干的; night = 昨晚聊到的事; user = 跟人有约。\n")
	b.WriteString("   ⚠️ **不许出现这五个之外的来源, 也不许编造上面没给你的人和事。**\n")
	b.WriteString("5. \"at\" 用 \"HH:MM\"(24 小时制)。")
	if in.SleepStart != "" && in.SleepEnd != "" {
		fmt.Fprintf(&b, "**不许排在你睡觉的时间段**: %s - %s。\n", in.SleepStart, in.SleepEnd)
	} else {
		b.WriteString("(那天日程上没有固定睡眠段。)\n")
	}
	b.WriteString("6. \"what\" 要是**一句人话**, 具体到能接着聊(\"去分拣仓帮可颂搬箱子, 她欠我一顿\"),\n")
	b.WriteString("   不是「处理事务」「忙工作」这种空话。\n")
	b.WriteString("7. 别排满 —— 真实的一天**大部分时间是什么都没安排的**。\n")
	b.WriteString("8. 允许「**本来想 X 结果没做**」—— 这是真过日子的常态, 不用每条都完成。\n")
	b.WriteString("   ⚠️ 但**不许每条都没做**(那叫废人叙事, 不叫生活); 也**不许把它说成做完了**。\n")
	return b.String()
}

// GenerateDayPlan 给 in.Day 排一份行程。
//
// ⚠️ **失败不兜底**: 返回的 error 不是"故障", 而是"今晚不排"这个**正常结局**。
// 调用方只该记一条日志, 不该重试、更不该编一份填空。
func GenerateDayPlan(ctx context.Context, c Chatter, in DayPlanInput, ws *WeekSchedule) (DayPlan, error) {
	if c == nil {
		return DayPlan{}, fmt.Errorf("没有模型")
	}
	prompt := dayPlanPrompt(in)
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		user := "给明天排一份具体行程, 只输出 JSON。"
		if attempt > 0 && lastErr != nil {
			user = "上次的输出不合规: " + lastErr.Error() + "。请重新输出修正后的 JSON, 只改违规的地方。"
		}
		out, err := c.Chat(ctx, user, prompt)
		if err != nil {
			lastErr = err
			continue
		}
		raw, err := extractJSON(out)
		if err != nil {
			lastErr = fmt.Errorf("抽不出 JSON: %w", err)
			continue
		}
		var p DayPlan
		if err := json.Unmarshal(raw, &p); err != nil {
			lastErr = fmt.Errorf("JSON 解析失败: %w", err)
			continue
		}
		if strings.TrimSpace(p.Day) == "" {
			p.Day = in.Day // 忘了填 day 不算错: 我们本来就知道是哪一天。
		}
		if p.Day != in.Day {
			// ⚠️ 排错日子必须拦: 一份"明天"的行程写到后天, 明天就没有行程, 而日志一切正常。
			lastErr = fmt.Errorf("day 写成了 %q, 应当是 %q", p.Day, in.Day)
			continue
		}
		p = NormalizeDayPlan(p, ws)
		if err := ValidateDayPlan(p, ws); err != nil {
			lastErr = err
			continue
		}
		if len(p.Items) < DayPlanMinItems {
			log.Warn("convo:dayplan", "§4.5 行程只有 %d 条 (目标 %d-%d) —— 仍然采用, 不为了凑数编",
				len(p.Items), DayPlanMinItems, DayPlanMaxItems)
		}
		return p, nil
	}
	return DayPlan{}, fmt.Errorf("两次都没排出合规行程 (%v)", lastErr)
}

// relLabel 关系类型的人话。
func relLabel(r RelationKind) string {
	switch r {
	case RelationCompanion:
		return "同伴"
	case RelationFamily:
		return "家人"
	case RelationRival:
		return "对手"
	case RelationMentor:
		return "师长"
	}
	return "认识的人"
}

// buildDayPlanInput 攒齐排一份行程要用的全部素材。
//
// ⚠️ **夜间批与 HTTP 口共用这一份** —— 两处各攒一遍的话, 它们会慢慢漂开,
// 而症状是"手动补排的行程和夜里排的不一样", 归因不了。
func buildDayPlanInput(st *Store, owner, day string, p *PersonaProfile, nightNotes []string, ws *WeekSchedule) DayPlanInput {
	in := DayPlanInput{Day: day, Profile: p, NightNotes: nightNotes}
	if ws != nil {
		// ⚠️ 给模型的睡眠窗必须是**那一天**的, 从槽表解 (#12)。
		// 读展示字段的话, 一个"周三没有 sleep 槽"的日程会让它以为周三也要 23:30 睡 ——
		// 而它排出来的行程会被 ValidateDayPlan 按**槽表**判, 两边对不上。
		if sleepMin, wakeMin, ok := SleepWindowOf(ws, in.Weekday); ok {
			in.SleepStart, in.SleepEnd = fmtHM(sleepMin), fmtHM(wakeMin)
		} else {
			// 那天真的没有睡眠段 → 明说"没有", 不拿展示字段冒充。
			log.Warn("convo:dayplan", "§4.5 那天没有睡眠段(槽表里找不到), 生成提示词不写睡眠窗: %s", day)
		}
	}
	if t, ok := ParseDay(day); ok {
		in.Weekday = weekdayKey(t)
	}
	now := clock.Now()
	if b, ok, err := st.LoadWorldBible(owner); err == nil && ok {
		in.Bible, in.HasBible = b, true
	}
	if notes, err := st.InPlayEvents(now, 6); err == nil {
		in.Events = notes
	}
	if appts, err := st.PendingAppointments(now); err == nil {
		in.Pending = appts
	}
	if env, ok := TodayEnv(); ok {
		in.EnvLine = fmt.Sprintf("%s %s %d°C", env.City, env.Weather, env.TempC)
	}
	return in
}

// planTomorrow 夜间批第 10 步: 给**明天**排一份行程 (层 2)。
//
// ⚠️ 失败**要出声**。静默的话, "今天没排上"和"这个功能根本没接线"长得一模一样 ——
// 而这个项目已经在"线接了一半"上栽过很多次了。
func (e *Engine) planTomorrow(ctx context.Context, p *PersonaProfile, nightNotes []string) {
	if e.Store == nil || e.Chatter == nil || p == nil {
		return
	}
	ws, err := e.Store.LoadSchedule()
	if err != nil || ws == nil {
		log.Warn("convo:dayplan", "读不到周日程, 明天不排行程: %v", err)
		return
	}
	now := clock.Now()
	day := TomorrowKey(now)
	in := buildDayPlanInput(e.Store, e.ownerKey(), day, p, nightNotes, ws)
	plan, gerr := GenerateDayPlan(ctx, e.Chatter, in, ws)
	if gerr != nil {
		log.Warn("convo:dayplan", "§4.5 明天(%s)不排行程: %v", day, gerr)
		return
	}
	if serr := e.Store.SaveDayPlan(e.ownerKey(), plan, ws); serr != nil {
		log.Warn("convo:dayplan", "§4.5 行程落库失败: %v", serr)
		return
	}
	log.Info("convo:dayplan", "§4.5 次日行程已排: %s · %d 条 (独自 %d)", day, len(plan.Items), plan.SoloCount())
}

// TomorrowKey 明天是哪一天 (行程按"次日"排, 睡前算)。
//
// ⚠️ 与 NightKeyOf 同源但不相同: NightKeyOf 是"刚过去的那一夜", 这个是"天亮之后那天"。
func TomorrowKey(at time.Time) string {
	return at.AddDate(0, 0, 1).Format("2006-01-02")
}
