package convo

import "time"

// ——— §8.3 每分钟 tick 的裁决逻辑 (纯函数, 时钟/随机数全部注入) ———
//
// 设计原则: "系统骰子"归这里 (延迟种子/概率判定), LLM 只取用不决策 —
// 保证状态机行为确定可测, AI 只负责"说什么"不负责"什么时候说"。

// TickInput 一次 tick 的全部输入。
type TickInput struct {
	Now     time.Time
	Sched   *WeekSchedule
	State   string // 当前槽状态 (CurrentState 结果)
	HadSlot bool   // 是否处于已定义槽内 (schedule 已加载)

	// Counters
	ActiveToday  int // 今日主动开场次数 (≤ 配额)
	NoInputToday int // 今日 noInput 低频消息次数
	PreviewToday int // 今日事件预告次数 (每个切换槽一次)
	PendingReply int // 待发延迟回复数 (busy 30-90min)
	// §4.3 中断-恢复到点。**它是一次"接着说", 不是新的开口** —— 所以不占主动配额,
	// 也不受冲量/抖动闸门拦截 (见 DecideTick 里那一支的注释)。
	InterruptionDue bool
	UserMsgsToday   int // 今日用户消息数
	SilentDays      int // 距最近一次用户交互的天数

	// 主动开场冷却 (病灶 1: 唤醒瞬间把当天配额在两分钟内打光)
	HasLastOpen          bool // 是否有上一条主动开场记录 (false = 无记录, 不设限)
	MinutesSinceLastOpen int  // 距上一条主动开场的分钟数

	// 随机骰子 (外部掷好注入 — 测试可钉死)
	StealRoll      int // 0-99 (slack 偷回判定, < SlackStealPct 则偷)
	DelayMin       int // busy 延迟分钟 (已掷, 30-90)
	OpenJitterRoll int // 0-99 (主动开场抖动, < OpenJitterPct 才发 — 避免"槽一开就发")

	// §8.3 情绪污染: 心情直接进说话决策器 (系统持有, 不是模型的自我报告)。
	// 1.0 = 心情不压制; 越低越不想开口。**永远是正数** —— 心情不是开关。
	MoodWillingness float64

	// B6 **开口冲量** 0..1: 今天有没有值得开口的事 (由世界事件的新鲜度算)。
	// 病灶: 触发条件原本是"配额与冷却都允许" ⇒ 必然贴着**最早可行时刻**发,
	// 实测每天恒为 07:3x + 11:5x 两条 —— 配额从"上限"变成了节拍器。
	OpenImpulse float64

	// HasImpulse 冲量是否**被提供过**。零值 0 的含义是"未提供", 不是"没冲量" ——
	// 少了它, 忘了填的调用方会静默变成"永不开口"。
	HasImpulse bool

	// §4.2 身体: 与心情**同一个落点** —— 必须落在配额上 (同 §8.3 的教训:
	// 缩放抖动概率那种花活看不出效果)。只减不加。
	BodyQuotaDelta int
	// HasBody 身体是否**真的在生效**。只靠 delta 的零值无法与"没病"区分 ——
	// 与 HasImpulse 同一条理由: 少了它, 忘了填的调用方会静默走进身体分支。
	HasBody bool

	// §6.7 热力: 当前热力 (已含衰减) 与**是否真的被提供过**。
	// ⚠️ 零值 0 与"没提供"不可区分 —— 少了 HasHeat, 忘了填的调用方会静默变成
	// "热力为零 = 从不主动开场", 而现象只是"她最近好安静"。
	Heat    float64
	HasHeat bool

	// §4.4.5 睡眠质量给出的配额变化 (≤0; 0 = 没读数/睡得还行)。
	SleepQuotaDelta int

	// Flags
	ConsecutiveUserMsgs int  // 未回复的连续用户消息数 (破功钩子)
	UrgentWordHit       bool // 用户消息命中紧急词 (求助/紧急)
	FunnyMarked         bool // 用户消息好笑标记 (slack 偷回触发)
}

// TickAction 一次 tick 的裁决结果。
type TickAction struct {
	Kind   string // none | preview | active_open | noinput | return_greet | steal_reply | delayed_reply | break_focus
	Reason string
	// 延迟回复参数 (delayed_reply 用)
	DelayMin int
}

const (
	ActNone         = "none"
	ActPreview      = "preview"       // 槽切换前 5 分钟预告
	ActActiveOpen   = "active_open"   // 主动开场 (配额内)
	ActNoInput      = "noinput"       // 静默≥24h + free 槽 低频勾搭 (≤1/日)
	ActReturnGreet  = "return_greet"  // 3 天无交互回归仪式
	ActStealReply   = "steal_reply"   // slack 摸鱼偷回
	ActDelayedReply = "delayed_reply" // busy 延迟回复到点
	ActBreakFocus   = "break_focus"   // focus 破功 (秒回豁免)
	ActImmediate    = "immediate"     // free/slack 秒回
	ActResume       = "resume"        // §4.3 中断-恢复: "我拿个外卖回来了"
)

// ShouldOpenSlot 当前槽是否允许主动开口 (free 与 slack 允许, focus/busy/sleep 不允许)。
func ShouldOpenSlot(state string) bool {
	return state == StateFree || state == StateSlack
}

// windowBefore 开: 正在 (start-5min, start) 窗口的判定。
func inPreviewWindow(now time.Time, slotStartMin int) bool {
	if slotStartMin <= 0 {
		return false
	}
	nowMin := now.Hour()*60 + now.Minute()
	d := slotStartMin - nowMin
	return d > 0 && d <= 5
}

// NoInputQuota 把配置折成**条数上限** —— 它是解释这个配置的**唯一一处** ✓。
//
// ⚠️ **≤0 → 1**(内置默认 ✓)。与 `HeatQuota` 的 cost 同规:
// "没配"不等于"配成 0" ✓ —— 后者会让这条机制**静默关掉** ✓。
func NoInputQuota(cfg int) int {
	if cfg <= 0 {
		return 1
	}
	return cfg
}

// DecideTick 每分钟裁决一次。返回唯一动作 (优先级: 预告 > 延迟回复 > 破功 > 回归 > noInput > 偷回 > 主动开场 > 秒回)。
// 用户消息进来的即时回复不走 tick — 由 dialog 触发器直接判断 (见 dialog.go)。
func DecideTick(in TickInput, cfg SchedulerConfig) TickAction {
	// 0. 无日程 (未创建朋友): 不裁决任何主动行为
	if !in.HadSlot {
		return TickAction{Kind: ActNone, Reason: "no schedule"}
	}

	// 1. 槽切换事件预告: 即将进入 focus/busy 的前 5 分钟窗 (查"下一个"槽)
	// 跨午夜边界不可能 (睡眠窗 23:30-07:30 内无 focus/busy 起点), 只查当日。
	ws := in.Sched
	if ws != nil {
		if next := nextSlot(ws, in.Now); next != nil && (next.State == StateFocus || next.State == StateBusy) {
			slotStart, _ := parseHM(next.Start)
			if inPreviewWindow(in.Now, slotStart) && in.PreviewToday < 1 {
				return TickAction{Kind: ActPreview, Reason: "slot " + next.State + " @ " + next.Start}
			}
		}
	}

	// 2. sleep: 静默 (用户紧急走 dialog 破功通道)
	if in.State == StateSleep {
		return TickAction{Kind: ActNone, Reason: "sleep"}
	}

	// 3. 延迟回复到点 (busy 30-90min 后)
	if in.PendingReply > 0 {
		return TickAction{Kind: ActDelayedReply, Reason: "busy delay due"}
	}
	// 3b. §4.3 中断-恢复: 与延迟回复同族 —— 都是**已经在进行的对话的延续**。
	//
	// ⚠️ 必须排在配额与冲量闸门**之前**。冲量要求"4 小时内有新的世界事件"
	// (见 convo.go OpenImpulse), 拿它去拦"我拿个外卖回来了"的后果是:
	// 这句**几乎永远发不出去** —— 而它恰恰是最像人的那句话。
	if in.InterruptionDue {
		return TickAction{Kind: ActResume, Reason: "interruption due"}
	}

	// 4. focus 破功钩子: 连发 3 条 / 紧急词 → 秒回豁免
	if in.State == StateFocus && (in.ConsecutiveUserMsgs >= 3 || in.UrgentWordHit) {
		return TickAction{Kind: ActBreakFocus, Reason: "break hook"}
	}

	// 5. 降频/回归: 3 天无交互 → 1 条回归; 6-9 天 → 再 1 条; ≥10 天暂停
	if in.SilentDays >= 3 && ShouldOpenSlot(in.State) {
		switch {
		case in.SilentDays < 6 && in.ActiveToday < 1:
			return TickAction{Kind: ActReturnGreet, Reason: "3d silence"}
		case in.SilentDays >= 6 && in.SilentDays < 10 && in.ActiveToday < 1:
			return TickAction{Kind: ActReturnGreet, Reason: "6d silence"}
		case in.SilentDays >= 10:
			return TickAction{Kind: ActNone, Reason: "degraded pause"}
		}
	}

	// 6. noInput 低频: 静默≥24h 且 free 槽 → ≤1 次/日
	if in.State == StateFree && in.SilentDays >= 1 && in.NoInputToday < NoInputQuota(cfg.NoInputMaxDay) {
		return TickAction{Kind: ActNoInput, Reason: "24h quiet + free"}
	}

	// 7. slack 偷回: 概率判定 (骰子已注入) + 触发条件
	if in.State == StateSlack && (in.FunnyMarked || in.ConsecutiveUserMsgs >= 3) {
		if in.StealRoll < cfg.SlackStealPct {
			return TickAction{Kind: ActStealReply, Reason: "slack steal"}
		}
	}

	// 8. 主动开场: 配额内 (2 条/天) + 冷却窗 + 抖动
	//    冷却窗治的是"配额没超但像刷屏": 只有日配额时, 引擎会在进入可发言槽后的
	//    相邻两个 tick 连发两条 (真实世界 07:30 / 07:31 逐字重复就是这么来的)。
	// §8.3 情绪污染: 坏心情/低唤醒的日子**少开一次口** (§4.1 坏心情 → 主动变少)。
	//
	// ⚠️ **必须落在配额上, 不能落在抖动概率上** —— 这是实测的教训:
	// 「她主动」恒等于「配额 × 天数」(实测四季恒 60), 说明真正的约束是**配额**而不是那枚
	// 10% 的抖动骰子 —— 一个开放槽里几 tick 就掷过去了, 缩放它对总数几乎没有影响。
	// 把心情乘到抖动上 = 加了一个看不出效果的花活。
	//
	// **幅度上限 = 至多减 1**: 心情不是开关, 差心情不该让它整天不说话。
	// ⚠️ `MoodWillingness > 0` 这个前置条件不能省: 零值 0 的含义是"**未提供**", 不是"心情极差"。
	// 没有它, 任何忘了填这个字段的调用方都会静默变成"低意愿" —— 一个安静的默认值 bug。
	// 与 moodWillingness() 的失败返回 1.0 同一条原则: **失败往"正常说话"倒, 不往"不说话"倒。**
	// ——— 配额: 心情 / 身体 / 热力, **三个减项一次算完, 顺序不许变** ———
	//
	// 先减法 (心情 / 身体), 后与热力预算取 min —— 这一步的顺序就是设计稿那句
	// 「**打扰红线不因热力高而放开**」的实现: 热力拉满也不许把前两个的压制顶回来。
	// (§4.2 身体与心情**叠加是故意的**: 心情差 + 生病 = 更可能一整天不出现, 那是真实的。)
	moodCut := 0
	if in.MoodWillingness > 0 && in.MoodWillingness < cfg.MoodLowWillingness {
		moodCut = 1
	}
	bodyCut := 0
	if in.HasBody {
		bodyCut = in.BodyQuotaDelta
	}
	// §4.4.5 睡眠质量: 与身体**同一个落点** —— 它是"一种身体状态" (设计稿 §4.4.7),
	// 所以两者相加而不是各开一路。
	bodyCut += in.SleepQuotaDelta
	// §6.7 热力: **HasHeat=false → 走常数配额**, 与升级前逐字节相同 (失败路径不变)。
	quota := HeatQuotaFrom(HeatQuotaInput{
		DailyCap:   cfg.ActiveQuotaDay,
		MoodCut:    moodCut,
		BodyCut:    bodyCut,
		Heat:       in.Heat,
		Provided:   in.HasHeat,
		CostPerMsg: cfg.HeatCostPerMsg,
	})
	// B6: **触发来自事件, 配额只做上限** —— 所以冲量闸门必须排在配额**前面**。
	// 反过来的话, "上限允许"仍会自动变成触发, 病根没动。
	if in.HasImpulse && in.OpenImpulse < cfg.OpenImpulseMin {
		return TickAction{Kind: ActNone, Reason: "no impulse"}
	}
	if ShouldOpenSlot(in.State) && in.ActiveToday < quota {
		if in.HasLastOpen && in.MinutesSinceLastOpen < cfg.OpenMinGapMin {
			return TickAction{Kind: ActNone, Reason: "open cooldown"}
		}
		if cfg.OpenJitterPct > 0 && in.OpenJitterRoll >= cfg.OpenJitterPct {
			return TickAction{Kind: ActNone, Reason: "open jitter"}
		}
		return TickAction{Kind: ActActiveOpen, Reason: "quota ok"}
	}

	return TickAction{Kind: ActNone, Reason: "quiet"}
}

// nextSlot 返回 now 之后即将开始的第一个槽 (同日)。
func nextSlot(ws *WeekSchedule, at time.Time) *Slot {
	key := weekdayKey(at)
	min := at.Hour()*60 + at.Minute()
	for _, d := range ws.Days {
		if d.Day != key {
			continue
		}
		for i := range d.Slots {
			sl := &d.Slots[i]
			s, err := parseHM(sl.Start)
			if err != nil {
				continue
			}
			if s > min {
				return sl
			}
		}
	}
	return nil
}

// slotAt 返回 now 所在的槽 (含 start/end 分钟, 供预告窗计算)。
func slotAt(ws *WeekSchedule, at time.Time) *Slot {
	key := weekdayKey(at)
	min := at.Hour()*60 + at.Minute()
	for _, d := range ws.Days {
		if d.Day != key {
			continue
		}
		for i := range d.Slots {
			sl := &d.Slots[i]
			s, err := parseHM(sl.Start)
			if err != nil {
				continue
			}
			e, err2 := parseHM(sl.End)
			if err2 != nil {
				continue
			}
			if min >= s && min < e {
				return sl
			}
		}
	}
	return nil
}
