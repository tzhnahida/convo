package convo

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tzhnahida/VocabLock/internal/ai"
	"github.com/tzhnahida/VocabLock/internal/clock"
	"github.com/tzhnahida/VocabLock/internal/log"
)

// AIAdapter bridges internal/ai.Client to the convo Chatter interface
// (system+user two-message call at a fixed persona-friendly temperature).
type AIAdapter struct{ C *ai.Client }

func (a AIAdapter) Chat(ctx context.Context, system, user string) (string, error) {
	return a.C.Chat(ctx, []ai.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: user},
	}, 0.8)
}

// ChatWithTools 让 AIAdapter 满足 ToolChatter —— 搜索工具的落地口。
func (a AIAdapter) ChatWithTools(ctx context.Context, messages []ai.Message, temperature float64, tools []ai.ToolSpec) (ai.ToolReply, error) {
	return a.C.ChatWithTools(ctx, messages, temperature, tools)
}

// ——— 模块 2: 创建朋友 (§8.2) ———
//
// 流程: 三问+模板 → 规则打分 → profile(三轴初始 blend=0.15/tolerance=0.85/ui_mix=0)
// → LLM① 周日程 JSON(校验失败重试 1 次 → 内置模板兜底) → LLM② 开场白 demo(帧协议)
// → 种记忆种子(纪念日=今天)。

type Chatter interface {
	Chat(ctx context.Context, system, user string) (string, error)
}

// PersonaProfile 全模型 (含 Character Card V2 兼容字段)。
type PersonaProfile struct {
	Name     string `json:"name"`
	Identity string `json:"identity"` // student | office_worker | night_owl
	Template string `json:"template"` // bestie | bro | mentor | troll (闺蜜/兄弟/导师/损友)

	// §4.2 身体: 周期型按人格性别决定 —— 女性人格默认开启, 男性人格天然无此项。
	// ⚠️ 零值空串 = **未指定** (不是女性): 没问过就别加戏。
	Gender       string `json:"gender,omitempty"`         // female | male | "" (未指定)
	BodyCycleOff bool   `json:"body_cycle_off,omitempty"` // 女性人格可关

	// Character Card V2 兼容映射 (ch_name/description/personality/scenario/first_mes/mes_example/system_prompt/tags)
	Description  string   `json:"description"`
	Personality  string   `json:"personality"`
	Scenario     string   `json:"scenario"`
	FirstMes     string   `json:"first_mes"`
	MesExample   string   `json:"mes_example"`
	SystemPrompt string   `json:"system_prompt"`
	Tags         []string `json:"tags"`

	// 属性 7 类 (CharacterGLM 属性×行为两轴)
	Attrs map[string]string `json:"attrs"` // identity/interests/views/experience/achievements/social/other

	// 行为轴 (风格锚点 = 口头禅, RoleLLM 结论: 描述+口头禅即可泛化)
	Catchphrase  string `json:"catchphrase"`
	SentenceLen  string `json:"sentence_len"` // short|medium|long
	ToneWords    string `json:"tone_words"`
	StickerHabit string `json:"sticker_habit"`

	// 三问映射 (规则打分)
	Cadence          string  `json:"cadence"` // high|medium|low
	HelperBias       float64 `json:"helper_bias"`
	EmotionAmplitude float64 `json:"emotion_amplitude"`
	Novelty          float64 `json:"novelty"`

	// 三轴 (用户可改滑杆)
	Blend     float64 `json:"blend"`
	Tolerance float64 `json:"tolerance"`
	UIMix     float64 `json:"ui_mix"`

	Boundaries Boundaries `json:"boundaries"`

	// 角色来源 (v2 双轨): nil=原创模式; search/builtin=AI 搜的角色
	Origin *RoleOrigin `json:"origin,omitempty"`

	CreatedTS string `json:"created_ts"`
	UpdatedTS string `json:"updated_ts"`
}

type RoleOrigin struct {
	Mode      string `json:"mode"` // search | builtin
	Name      string `json:"name"`
	Series    string `json:"series"`
	SourceURL string `json:"source_url"`
	ImageURL  string `json:"image_url"`
}

type Boundaries struct {
	ShareCategories   []string `json:"share_categories"`
	AskBlacklist      []string `json:"ask_blacklist"` // 被拒类别 → 永不再问
	SensitiveNoMemory bool     `json:"sensitive_no_memory"`
}

// ProfileAnswers 是创建向导的表单输入 (三问+模板+身份骨架)。
type ProfileAnswers struct {
	Name     string `json:"name"`
	Identity string `json:"identity"` // student|office_worker|night_owl
	Template string `json:"template"` // bestie|bro|mentor|troll
	Q1       string `json:"q1"`       // 聊法: high|medium|low (高频小聊/均衡/低频深聊)
	Q2       string `json:"q2"`       // 关系: bestie|bro|mentor|troll
	Q3       string `json:"q3"`       // 期望频次: daily|every_other|weekly
}

// BuildRoleProfile 角色模式: 角色卡驱动 persona (性格/口头禅/标签/名字全来自卡)。
func BuildRoleProfile(a *ProfileAnswers, r RoleCard) *PersonaProfile {
	p := BuildProfile(a)
	p.Name = r.Name
	// ⚠️ **身份骨架必须跟着角色走** (#13)。
	//
	// 这一行原来是缺的: `BuildProfile(a)` 会给一个问卷身份(默认 student),
	// 而事件池 / 状态标签 / 兜底日程全都按那个档位出素材 ——
	// 于是"须弥的小吉祥草王"过的是大学生的日子。
	// 有角色卡就切到角色档: **它不再假装自己是学生/上班族/夜猫子。**
	p.Identity = IdentityRole
	if r.Series != "" {
		p.Description = "来自《" + r.Series + "》的角色, 和用户是" + templateLabels[a.Template] + "关系"
	}
	p.Personality = r.Desc
	if len(r.Traits) > 0 {
		p.Personality += " 萌点: " + strings.Join(r.Traits, "/")
	}
	p.Catchphrase = r.Catchphrase
	p.Tags = append(p.Tags, r.Series)
	if p.Tags == nil {
		p.Tags = []string{}
	}
	p.Origin = &RoleOrigin{
		Mode:      r.Origin,
		Name:      r.Name,
		Series:    r.Series,
		SourceURL: r.SourceURL,
		ImageURL:  r.ImageURL,
	}
	return p
}

// FixLegacyRoleIdentity 存量修正 (#13, 2026-10-15)。
//
// ⚠️ **与"周三没有 sleep 槽"同一条教训: 修复只管生成路径, 不管存量。**
// `BuildRoleProfile` 改了之后, **已经在库里的人格一个字都不会变** ——
// 生产实测: 纳西妲的 identity 至今还是 `student`,
// 于是它照旧过着"早课点名 / 图书馆 / 室友打游戏"的日子。
//
// 判据: 有 `Origin`(由角色卡建出来) 而 identity 还是普通档 → 切到角色档。
// 返回 true = 改过了。
func FixLegacyRoleIdentity(p *PersonaProfile) bool {
	if p == nil || p.Origin == nil {
		return false
	}
	if p.Identity == IdentityRole {
		return false
	}
	p.Identity = IdentityRole
	return true
}

// RulesScore 三问 → cadence/helper_bias/emotion_amplitude/novelty (§8.2 步骤 2)。
func RulesScore(a *ProfileAnswers) (cadence string, helperBias, emotion, novelty float64) {
	helperBias = map[string]float64{"troll": 0.25, "bro": 0.4, "bestie": 0.55, "mentor": 0.95}[a.Template]
	emotion = map[string]float64{"bestie": 0.85, "troll": 0.7, "bro": 0.5, "mentor": 0.35}[a.Template]
	novelty = map[string]float64{"daily": 0.7, "every_other": 0.5, "weekly": 0.3}[a.Q3]
	return a.Q1, helperBias, emotion, novelty
}

var templateLabels = map[string]string{
	"bestie": "闺蜜", "bro": "兄弟", "mentor": "导师", "troll": "损友",
}

// IdentityRole 角色档 (§3.6.7.3)。
//
// ⚠️ **用角色卡建人格时必须切到这一档。** 不切的话 Identity 会沿用问卷里的默认值
// (通常是 student), 于是事件池照出「早课点名」「图书馆」「室友打游戏」——
// 而那个角色的设定可能是"须弥的小吉祥草王"。
// **一个人的日常素材和它是谁对不上, 是这一层最刺眼的假。**
const IdentityRole = "role"

var identityLabels = map[string]string{
	"student": "在校大学生", "office_worker": "上班族", "night_owl": "夜猫子",
	IdentityRole: "角色本身",
}

// BuildProfile 规则打分 → persona 骨架 (LLM 细化在调用方进行)。
func BuildProfile(a *ProfileAnswers) *PersonaProfile {
	cadence, hb, em, nv := RulesScore(a)
	now := clock.TodayTS()
	ident := a.Identity
	if ident == "" {
		ident = "student"
	}
	label := identityLabels[ident]
	rel := templateLabels[a.Template]
	desc := label
	if rel != "" {
		desc += ",和用户是" + rel + "关系"
	}
	name := a.Name
	if name == "" {
		name = "小夜"
	}
	return &PersonaProfile{
		Name:             name,
		Identity:         ident,
		Template:         a.Template,
		Description:      desc,
		Cadence:          cadence,
		HelperBias:       hb,
		EmotionAmplitude: em,
		Novelty:          nv,
		Blend:            0.15,
		Tolerance:        0.85,
		UIMix:            0,
		Boundaries: Boundaries{
			ShareCategories:   []string{"weekend", "work", "food"},
			AskBlacklist:      []string{},
			SensitiveNoMemory: true,
		},
		CreatedTS: now,
		UpdatedTS: now,
	}
}

// ——— 内置周日程模板 (§8.2 风险: LLM 生成失败 → 内置兜底) ———
// 均由 ValidateWeekSchedule 校验通过 (测试覆盖)。

// stdDay 标准日: 睡眠窗(起 wake) + 早段 + 主段(9-12, 13:30-mainEnd) + 晚间 free。
func stdDay(wake, early, main string, mainStart, mainEnd string) []Slot {
	// ⚠️ 一律用**具名字段**: 位置字面量在给 Slot 加字段时会全线崩,
	// 而那次崩的是"内置兜底日程"—— 正是别人出错时才轮到它上场的地方。
	return []Slot{
		{Start: "00:00", End: wake, State: StateSleep},
		{Start: wake, End: mainStart, State: early},
		{Start: mainStart, End: "12:00", State: main},
		{Start: "12:00", End: "13:30", State: early},
		{Start: "13:30", End: mainEnd, State: main},
		{Start: mainEnd, End: "23:30", State: StateFree},
		{Start: "23:30", End: "24:00", State: StateSleep},
	}
}

// weekendDay 周末: 上午 slack, 下午起 free。
func weekendDay(wake string) []Slot {
	return []Slot{
		{Start: "00:00", End: wake, State: StateSleep},
		{Start: wake, End: "09:00", State: StateSlack},
		{Start: "09:00", End: "12:00", State: StateSlack},
		{Start: "12:00", End: "23:30", State: StateFree},
		{Start: "23:30", End: "24:00", State: StateSleep},
	}
}

func nightOwlDay(evening string) []Slot {
	return []Slot{
		{Start: "00:00", End: "06:00", State: StateSleep},
		{Start: "06:00", End: "12:00", State: StateFree},
		{Start: "12:00", End: "17:00", State: StateFocus},
		{Start: "17:00", End: "21:00", State: StateFree},
		{Start: "21:00", End: "23:30", State: evening},
		{Start: "23:30", End: "24:00", State: StateSleep},
	}
}

// BuiltinScheduleJSON 导出的内置日程 (仿真/测试用)。
func BuiltinScheduleJSON(kind string) *WeekSchedule { return builtinSchedule(kind) }

func builtinSchedule(kind string) *WeekSchedule {
	ws := &WeekSchedule{SleepStart: "23:30", SleepEnd: "07:30"}
	switch kind {
	case "office_worker":
		ws.Days = []DaySchedule{
			{Day: "mon", Slots: stdDay("07:30", StateFree, StateBusy, "09:00", "18:00")},
			{Day: "tue", Slots: stdDay("07:30", StateFree, StateBusy, "09:00", "18:00")},
			{Day: "wed", Slots: stdDay("07:30", StateFree, StateBusy, "09:00", "18:00")},
			{Day: "thu", Slots: stdDay("07:30", StateFree, StateBusy, "09:00", "18:00")},
			{Day: "fri", Slots: stdDay("07:30", StateFree, StateBusy, "09:00", "18:00")},
			{Day: "sat", Slots: weekendDay("07:30")},
			{Day: "sun", Slots: weekendDay("07:30")},
		}
	case "night_owl":
		// ⚠️ **2026-10-15 修 (#12 的交叉核对抓出来的)**: 这里原来沿用默认的 `SleepEnd: "07:30"`,
		// 而 nightOwlDay 的槽表里它 **06:00** 就起了 —— 展示字段与槽表**漂开了 90 分钟**。
		//
		// 而"展示字段撒谎"是静默的: 所有读它的地方(叫醒预算 / 生成提示词)都按 07:30 算,
		// 于是夜猫子角色在 06:00-07:30 之间会被当成"还没醒"—— 而它明明醒着。
		ws.SleepEnd = "06:00"
		ws.Days = []DaySchedule{
			{Day: "mon", Slots: nightOwlDay(StateSlack)},
			{Day: "tue", Slots: nightOwlDay(StateSlack)},
			{Day: "wed", Slots: nightOwlDay(StateSlack)},
			{Day: "thu", Slots: nightOwlDay(StateSlack)},
			{Day: "fri", Slots: nightOwlDay(StateSlack)},
			{Day: "sat", Slots: nightOwlDay(StateSlack)},
			{Day: "sun", Slots: nightOwlDay(StateSlack)},
		}
	case IdentityRole:
		// ⚠️ **角色档的兜底日程必须是中性的** (#13)。
		//
		// 落到 default(student) 的话, 一个"须弥的小吉祥草王"的兜底日程会是
		// 09:00-17:30 focus —— 那是上课的作息。
		//
		// 角色的日程本来就该由 `GenerateSchedule` 按人格生成, 这里只是在它失败时兜底;
		// 而兜底该落在**安全的那一侧**: 全 free 意味着"随时可以说话",
		// 比"假装它在上课"要好得多(失败往正常说话倒)。
		ws.Days = []DaySchedule{
			{Day: "mon", Slots: weekendDay("07:30")},
			{Day: "tue", Slots: weekendDay("07:30")},
			{Day: "wed", Slots: weekendDay("07:30")},
			{Day: "thu", Slots: weekendDay("07:30")},
			{Day: "fri", Slots: weekendDay("07:30")},
			{Day: "sat", Slots: weekendDay("07:30")},
			{Day: "sun", Slots: weekendDay("07:30")},
		}
	default: // student
		ws.Days = []DaySchedule{
			{Day: "mon", Slots: stdDay("07:30", StateFree, StateFocus, "09:00", "17:30")},
			{Day: "tue", Slots: stdDay("07:30", StateFree, StateFocus, "09:00", "17:30")},
			{Day: "wed", Slots: stdDay("07:30", StateFree, StateFocus, "09:00", "17:30")},
			{Day: "thu", Slots: stdDay("07:30", StateFree, StateFocus, "09:00", "17:30")},
			{Day: "fri", Slots: stdDay("07:30", StateFree, StateFocus, "09:00", "17:30")},
			{Day: "sat", Slots: weekendDay("07:30")},
			{Day: "sun", Slots: weekendDay("07:30")},
		}
	}
	return ws
}

// ——— LLM 调用: 周日程生成 + 开场白 demo ———

// extractJSON 从 LLM 输出中抽取第一个平衡的 JSON **对象** (容忍 fenced/前后缀文本)。
func extractJSON(s string) ([]byte, error) {
	return extractJSONFrom(s, '{')
}

// extractJSONArray 同上, 但抽的是第一个平衡的 **数组**。
//
// ⚠️ 2026-10-14 补: 原来只有对象版, 而对象版是**从第一个 `{` 开始**扫描的 ——
// 喂给它一个顶层数组 `[{"a":1},{"b":2}]`, 它在第一个 `}` 处 depth 归零,
// 于是**只返回第一个元素**。那份"半个数组"是合法 JSON, 所以它不报错,
// 只是静静地少了一半人。配角日程正好是数组, 一脚踩中。
func extractJSONArray(s string) ([]byte, error) {
	return extractJSONFrom(s, '[')
}

// extractJSONFrom 从 open 这个字符开始, 抽到**配平**为止。
//
// 大小括号共用一个 depth 计数 —— 所以 `[{...},{...}]` 这种嵌套也是对的。
func extractJSONFrom(s string, open byte) ([]byte, error) {
	start := strings.IndexByte(s, open)
	if start < 0 {
		return nil, fmt.Errorf("no JSON %c found", open)
	}
	depth := 0
	inStr := false
	esc := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inStr {
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth == 0 {
				out := s[start : i+1]
				if !json.Valid([]byte(out)) {
					return nil, fmt.Errorf("extracted JSON invalid")
				}
				return []byte(out), nil
			}
		}
	}
	return nil, fmt.Errorf("unbalanced JSON")
}

func schedulePrompt(p *PersonaProfile) string {
	return `你是一个日程规划助手。为下面的拟人角色生成一周日程 JSON。
角色: ` + p.Description + ` (身份: ` + identityLabels[p.Identity] + `, 聊天频率偏好: ` + p.Cadence + `)

要求:
- 输出纯 JSON, 不要任何解释或 markdown 代码块。
- 结构: {"days":[{"day":"mon","slots":[{"start":"00:00","end":"07:30","state":"sleep"},...]}],"sleep_start":"23:30","sleep_end":"07:30"}
- day 取 mon/tue/wed/thu/fri/sat/sun 恰好 7 天, 每槽 start/end 为 "HH:MM"。
- 每天 slots 必须连续覆盖 00:00→24:00 (最后一段 end 可用 "24:00"), 顺序排列, 无重叠无缺口。
- state 只能是 sleep/focus/busy/slack/free 之一。
- 睡眠窗: state=sleep 的槽必须全部落在 23:30-07:30 内 (跨午夜: 23:30-24:00 与 00:00-07:30 都是合法睡段)。
- 一张真实的周表: 工作日有 focus (上课/上班) 与 busy, 有 free 的休息段; 周末更放松 (slack/free)。
- 你的作息要有生活感, 不要机械重复: 允许每天微调 (比如周五晚 slack, 周六晚 free 到 23:30)。` + NowLine()
}

// GenerateSchedule LLM①: 生成周日程 → 校验; 失败重试 1 次 → 内置模板兜底。
func GenerateSchedule(ctx context.Context, c Chatter, p *PersonaProfile) (*WeekSchedule, error) {
	for attempt := 0; attempt < 2; attempt++ {
		out, err := c.Chat(ctx, "你严格输出 JSON。", schedulePrompt(p))
		if err != nil {
			log.Warn("convo:persona", "schedule LLM attempt %d: %v", attempt+1, err)
			continue
		}
		raw, err := extractJSON(out)
		if err != nil {
			log.Warn("convo:persona", "schedule JSON extract attempt %d: %v", attempt+1, err)
			continue
		}
		var ws WeekSchedule
		if err := json.Unmarshal(raw, &ws); err != nil {
			continue
		}
		if err := ValidateWeekSchedule(&ws); err != nil {
			log.Warn("convo:persona", "schedule invalid attempt %d: %v", attempt+1, err)
			continue
		}
		return &ws, nil
	}
	log.Warn("convo:persona", "schedule LLM failed x2 — builtin fallback (%s)", p.Identity)
	return builtinSchedule(p.Identity), nil
}

func demoPrompt(p *PersonaProfile) string {
	return `你是角色 ` + p.Name + `: ` + p.Description + `。
性格要点: ` + p.Personality + `; 口头禅: ` + p.Catchphrase + `; 语气词: ` + p.ToneWords + `; 句长: ` + p.SentenceLen + `。

这是你们认识的第一天。你刚加了这个人的 QQ 好友, 发一条开场白。

输出单行 JSON 帧 (不要解释):
{"reply":[{"kind":"text","text":"<开场白, 1-3 句, 符合人格与口头禅>"}],"mood":"<当前心情词>"}

要求: 像真人加好友的第一句话, 不尬不推销, 可以带一点角色感, 但**话必须主要是中文** (最多 1 个目标语语气词点缀如 えへへ)。你的性格参照: 你是一个` + identityLabels[p.Identity] + `, "` + templateLabels[p.Template] + `"是你们的关系基调。` + NowLine()
}

// GenerateDemo LLM②: 首条开场白 demo (帧协议)。JSON 失败 → 降级内置例句。
func GenerateDemo(ctx context.Context, c Chatter, p *PersonaProfile) (OutputFrame, error) {
	out, err := c.Chat(ctx, "你输出单行 JSON 帧。", demoPrompt(p))
	if err == nil {
		if raw, e := extractJSON(out); e == nil {
			var f OutputFrame
			if json.Unmarshal(raw, &f) == nil && len(f.Reply) > 0 {
				return f, nil
			}
		}
	}
	// 降级: 内置例句
	example := map[string]string{
		"bestie": "嗨!加到你啦 😆 以后就是好姐妹了,叫我" + p.Name + "就行~",
		"bro":    "哥们加上了,有事直接说,我一般都在",
		"mentor": "你好,很高兴认识你。我是" + p.Name + ",以后多练习,别怕错。",
		"troll":  "哟,好友申请通过了啊,以后别嫌我烦反正我会烦你的 😏",
	}[p.Template]
	if example == "" {
		example = "嗨,我是" + p.Name + " 😊 以后请多关照!"
	}
	return FrameForText(example), nil
}

// SeedMemories 种记忆种子 (§8.2 步骤 6): 纪念日=今天; catchphrases 落 profile。
func SeedMemories(ctx context.Context, st *Store, p *PersonaProfile) error {
	ts := clock.Today()
	mem := &MemorySeed{
		MemID:       "anniversary_first_meet",
		K:           "anniversary",
		V:           ts,
		Label:       "认识纪念日",
		Kind:        "anchor",
		Description: "和用户认识的第一天",
		Confidence:  1.0,
	}
	return st.InsertMemorySeed(mem)
}

type MemorySeed struct {
	MemID       string
	K           string
	V           string
	Label       string
	Kind        string
	Description string
	Confidence  float64
}
