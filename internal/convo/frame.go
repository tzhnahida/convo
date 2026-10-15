package convo

// ——— 输出帧协议 (§8.5): LLM 每次回复输出单行 JSON 帧 ———
//
// 字段按 §8.5 校验链逐项校验: sticker 存在性 / mem_id ∈ 注入列表(硬校验,
// 防编造记忆) / ask_share 黑名单 / act_fool 门槛(tolerance≥0.3) /
// recast 只记隔日卡候选不符 P0(学习闭环砍→仅丢弃) / end 会话收尾。

type OutputFrame struct {
	Reply    []FrameItem    `json:"reply"`
	MemRefs  []string       `json:"mem_id_refs,omitempty"`
	Mood     string         `json:"mood,omitempty"`
	AskShare *AskShareFrame `json:"ask_share,omitempty"`
	ActFool  bool           `json:"act_fool,omitempty"`
	Recast   *RecastFrame   `json:"recast,omitempty"`
	End      bool           `json:"end,omitempty"`
	Remember *FrameRemember `json:"remember,omitempty"` // v2.4.1: AI 主动记忆 (每会话 ≤3, 系统侧强制)
	// §5.5 主体性 (约定): 它主动跟 ta 约一件事 —— 系统落 convo_appointment。
	// ⚠️ **只有系统给了许可才许提** (信用低 → MayCommit=false, 硬闸门不是概率)。
	Promise *FramePromise `json:"promise,omitempty"`
	// §4.5.2 报一件事**没做成** —— 「本来想看书, 结果刷了一晚上手机」。
	//
	// ⚠️ 这是**一等公民**, 不是失败态: 真人报告自己一天时最高频的句式就是未完成,
	// 而"计划与执行的落差"是整个日常质感里最厚的一层。
	PlanUndone *FramePlanUndone `json:"plan_undone,omitempty"`

	// §5.5 自主预约: **它自己往次日行程里加一件事** —— "我明天想去趟书店"。
	//
	// ⚠️ 这不是锦上添花, 它是**拒绝的依据**:
	// 用户临时约它, 它能答"那个时间我约了自己去书店" ——
	// **没有自主预约, 拒绝就是无依据的**, 而无依据的拒绝 = 噪声(违反 M7: 不许随机掷骰子)。
	SelfPlan *FrameSelfPlan `json:"self_plan,omitempty"`

	// §5.5 Settle: 对一条**待履行约定**的主动处置 —— 提前取消 / 改期 / 承认忘了。
	//
	// ⚠️ 这是「**它能说不行**」的落点。在它之前, 一条约定只有两种结局:
	// 赴约, 或者**到点了才反悔**(breached)。而"提前打招呼"与"放鸽子"在信用上是
	// 两件完全不同的事(0.5 vs 0.0)—— 分不开就归因不了, 也就没有"有分寸"可言。
	Settle *FrameSettle `json:"settle_promise,omitempty"`

	// §5.5 主体性 (违抗): 这一轮它选择不接受。
	// ⚠️ **必须 ≤ 系统许可的档** —— 系统说"最多拖一下", 它不许顶回去 (校验链会拦)。
	Defy    *FrameDefy `json:"defy,omitempty"`
	Explain string     `json:"explain,omitempty"` // 约定: 教学态附词卡说明 (求助检测→教学态)
	// A3: 这次开口**是谁起的头**。系统知道事实(它是不是主动 tick 触发的), 模型必须自述一致 ——
	// 不一致 = 事实层错误, 不是文风问题。典型病灶: 主动开场却说"你今晚怎么这么有空来撩我"。
	Initiative string `json:"initiative,omitempty"` // ai | user

	// §4.6.3 时间感知: 它**想知道现在几点**。
	// 置位 → 系统把精确时刻补给这一轮并让它重说一遍(见 Reply 的 pre-pass)。
	// ⚠️ 不给它一个绝对时钟是**故意**的: 真人不随身带着秒表, 他"掏手机看一眼"。
	LookTime bool `json:"look_time,omitempty"`

	// A6 硬拒(§8.2): 命中伦理硬线 → **本条不发送**。
	// 它不是故障, 是刻意不说 —— 调用方据此静默丢弃, 不降级、不报错。
	Refused      bool   `json:"-"`
	RefuseReason string `json:"-"`

	// P5 回放: 这一轮**它看到了什么** 与 **它原始说了什么**。
	// 只走内存与轨迹表, 永不进上下文 (json:"-" 保证它不会被序列化进任何帧)。
	SystemPrompt string `json:"-"`
	RawOutput    string `json:"-"`

	// Degraded 标记这是降级产物 (解析失败后的兜底/纯文本)。
	// 降级文本**入库但不进上下文** —— 否则它会接自己的故障, 把兜底句发展成口头禅。
	Degraded bool `json:"-"`
}

// FrameRemember v2.4.1: LLM 主动请记 (想记住的事, 系统落库 convo_memory)。
type FrameRemember struct {
	K     string `json:"k"`     // 类别 (food/work/plan/…)
	V     string `json:"v"`     // 事实
	Label string `json:"label"` // 短标签
}

// FrameSelfPlan §5.5: 它给自己排的一件事 (**次日**)。
//
// ⚠️ 硬线之一: 「自主预约的条目必须**能说出口**」—— 所以 What 非空是校验项,
// 不是"补个默认值"。说不出口的安排 = 借口。
type FrameSelfPlan struct {
	At    string `json:"at"`   // "HH:MM"
	What  string `json:"what"` // 必须能说出口
	With  string `json:"with,omitempty"`
	Place string `json:"place,omitempty"`
	Dur   string `json:"dur,omitempty"`
}

// FrameSettle §5.5: 处置一条**待履行**约定。
//
// ⚠️ ID 必须来自注入段里那个 `#号` —— 系统只认**待履行列表里**的 id,
// 编一个、或者指向已经结掉的那条, 会被校验链拦下(事实不许改写)。
type FrameSettle struct {
	ID     int64  `json:"id"`
	State  string `json:"state"`            // cancelled | forgot
	Reason string `json:"reason,omitempty"` // cancelled 必填; forgot 可空(承认本身就是表达)
	// Instead 替代时段("明天 19:00")。给了就**真的重约一条** ——
	// 否则 §4.5.3-4「不可约时协商替代时段」只是一句空话。
	Instead string `json:"instead,omitempty"`
}

// FramePromise §5.5: 约定的提案。
//
// ⚠️ `When` **只收两种形式**: "HH:MM" (今天该时刻) 或 "明天 HH:MM"。
// 解析不出 → **静默丢弃, 不重试** —— 与 remember 空对象同一条纪律:
// 让模型自由发挥时间表述, 换来的是"约了但系统不知道约在几点", 那比不约更糟。
type FramePromise struct {
	What string `json:"what"` // 约什么 (人话)
	When string `json:"when"` // "HH:MM" | "明天 HH:MM"
}

// FrameDefy §5.5: 违抗的档位。取值 silent|delay|refuse|talkback。
type FrameDefy struct {
	Mode string `json:"mode"`
}

type FrameItem struct {
	Kind      string `json:"kind"` // text | sticker | visual
	Text      string `json:"text,omitempty"`
	StickerID int64  `json:"sticker_id,omitempty"` // 0 = 系统 affinity 加权代选 (§8.7 步骤 5)
	URL       string `json:"url,omitempty"`        // ⚠️ P0 仅文本/表情包, visual 保留字段
}

type AskShareFrame struct {
	Category string `json:"category"`
	Question string `json:"question"`
}

type RecastFrame struct {
	Original string `json:"original"`
	Correct  string `json:"correct"`
}

// FrameForText builds a minimal text-only frame (降级纯文本回用)。
func FrameForText(text string) OutputFrame {
	return OutputFrame{Reply: []FrameItem{{Kind: "text", Text: text}}}
}
