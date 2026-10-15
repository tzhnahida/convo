package convo

import (
	"fmt"
	"strings"
	"time"
)

// ——— §4.2 身体 ———
//
//	BodyState{ kind, severity 0..1, since, until, note }
//
// 两条纪律决定了这一层的形状, 都是踩过同类坑之后定下的:
//
//	① **严重度不落库, 现算** —— 与 §3.6.8 的沉降阶段同一条原则:
//	   存了就必须回答"谁在什么时候重算它", 而这个量是 (now - 发作时刻) 的纯函数。
//	   库里只存 **发作时刻 + 病程长度**, severity / tail / until 全部现算。
//
//	② **阶梯式恢复, 不许开关式满血** (§4.2 原文)。若写成线性衰减, 行为参数每一分钟
//	   都在漂移, 反而**没有"病好了"这个可感知的时刻**。台阶给的是能被人察觉的进步
//	   (今天能下床 / 明天能出门), 且"结束"只有一个瞬间, 尾巴期才有明确起点。

// 身体型: 事件型三种 + 周期型一种 (§4.2 两类)。
const (
	BodyCold     = "cold"     // 感冒 (事件型)
	BodyHangover = "hangover" // 宿醉 (事件型)
	BodyInsomnia = "insomnia" // 失眠 (事件型)
	BodyPeriod   = "period"   // 生理期 (周期型)
	// BodyLowMood §4.1.2 **心境低落期** —— 它也是一种 BodyState: 有病程、有阶梯恢复、有尾巴。
	//
	// ⚠️ 判据是 **strain 硬阈值**, 不是概率(纪律 #5)。
	// ⚠️ 名字刻意**不含病名**: 它是一个状态, 不是诊断 —— 而"不许自诊、不许提病名"是四条硬线之一。
	BodyLowMood = "low_mood"
)

// BodySeverityLadder 病程内的严重度台阶, 按进度等分。
//
// ⚠️ 最后一级是 0.15 而不是 0 —— 见文件头 ②。
var BodySeverityLadder = []float64{1.0, 0.75, 0.5, 0.3, 0.15}

// BodyTailHours 康复后"还留个尾巴"的小时数 (§4.2: 恢复后留尾巴, 嗓子哑两天)。
const BodyTailHours = 48

// BodyCooldownDays 一次病程彻底走完后, 多久之内不再发作。
//
// 没有它, 冬天会连着感冒 —— 概率是"每天掷一次", 而人不会。
const BodyCooldownDays = 5

// BodyCycleDays / BodyCycleLength 周期型的默认周期与经期长度 (§4.2)。
const (
	BodyCycleDays   = 28
	BodyCycleLength = 5
)

// bodyTSLayout 与全包一致的时间格式 (moodstore / memory / settle 都用它)。
const bodyTSLayout = "2006-01-02 15:04:05"

// BodyState 一次身体不适。
//
// ⚠️ 前 5 个字段是**事实**(入库); 后 5 个是**派生量**(Refresh 现算, 不入库)。
// 分开写是为了让"库里存了什么"一眼可见 —— 存派生量就是给自己造不一致。
type BodyState struct {
	Kind    string  `json:"kind"`
	Cause   string  `json:"cause"`
	Note    string  `json:"note"` // 尾巴: 好了之后还留着的那点事
	OnsetTS string  `json:"onset_ts"`
	CourseH float64 `json:"course_h"`

	// ——— 以下全部现算 ———
	Active   bool    `json:"active"`
	Severity float64 `json:"severity"`
	Since    string  `json:"since"`
	Until    string  `json:"until"`
	Tail     bool    `json:"tail"`
}

// Onset 发作时刻 (读不出 → ok=false, 当作"没病")。
func (b BodyState) Onset() (time.Time, bool) {
	t, err := time.ParseInLocation(bodyTSLayout, b.OnsetTS, time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// Course 病程长度。
func (b BodyState) Course() time.Duration {
	if b.CourseH <= 0 {
		return 0
	}
	return time.Duration(b.CourseH * float64(time.Hour))
}

// BodySeverityAt 由 (发作时刻, 病程) 现算严重度与是否尾巴期。
//
// course<=0 或时刻读不出 → (0,false): **失败往"没病"倒** ——
// 与 MoodPacingFactor(0)=1.0 / MoodWillingness 的零值约定同一条原则。
func BodySeverityAt(onset time.Time, course time.Duration, now time.Time) (float64, bool) {
	if course <= 0 {
		return 0, false
	}
	el := now.Sub(onset)
	if el < 0 {
		return 0, false
	}
	if el >= course {
		if el < course+BodyTailHours*time.Hour {
			return 0, true
		}
		return 0, false
	}
	idx := int(float64(len(BodySeverityLadder)) * el.Seconds() / course.Seconds())
	if idx >= len(BodySeverityLadder) {
		idx = len(BodySeverityLadder) - 1
	}
	if idx < 0 {
		idx = 0
	}
	return BodySeverityLadder[idx], false
}

// Refresh 现算全部派生量。**每次用之前都得调** —— 派生量存下来就会不一致。
func (b *BodyState) Refresh(now time.Time) {
	b.Active, b.Severity, b.Tail = false, 0, false
	b.Since, b.Until = b.OnsetTS, ""
	onset, ok := b.Onset()
	if !ok {
		return
	}
	course := b.Course()
	if course <= 0 {
		return
	}
	b.Until = onset.Add(course).Format(bodyTSLayout)
	el := now.Sub(onset)
	if el < 0 || el >= course+BodyTailHours*time.Hour {
		return
	}
	sev, tail := BodySeverityAt(onset, course, now)
	b.Active, b.Severity, b.Tail = true, sev, tail
}

// Resting 病程 + 尾巴 + 冷却都过完了吗 (**过完了才能再发作**)。
// 空记录 = 从没病过 = true。
func (b BodyState) Resting(now time.Time) bool {
	onset, ok := b.Onset()
	if !ok || b.Course() <= 0 {
		return true
	}
	total := b.Course() + BodyTailHours*time.Hour + BodyCooldownDays*24*time.Hour
	return now.Sub(onset) >= total
}

// ——— 行为效果 (§4.2: "必须落在行为上, 不只是话术") ———

type bodyEffectRow struct {
	MinSeverity  float64
	DelayFactor  float64
	LengthFactor float64
	QuotaDelta   int
	MayVanish    bool
	CanDecline   bool
}

// BodyEffectTable 严重度 → 行为参数。按 MinSeverity 降序查第一行命中。
//
// ⚠️ 三条**不许被推翻**的方向性约束 (body_test 逐行钉住):
//
//	DelayFactor  >= 1  身体不舒服只会更慢, 不会更快
//	LengthFactor <= 1  只会更短, 不会更长
//	QuotaDelta   <= 0  只会更少开口, 不会更多
//
// 这三条是"身体"这一层存在的意义; 破了它, 这一层就成了随机扰动。
var BodyEffectTable = []bodyEffectRow{
	{0.80, 2.0, 0.60, -2, true, true},
	{0.55, 1.7, 0.65, -1, true, true},
	{0.35, 1.4, 0.75, -1, false, true},
	{0.10, 1.2, 0.85, 0, false, false},
	{0.00, 1.1, 0.90, 0, false, false},
}

// BodyEffects 折好的行为参数。零值 = 没病 = 一切照常。
type BodyEffects struct {
	Active       bool
	Kind         string
	Severity     float64
	Tail         bool
	DelayFactor  float64
	LengthFactor float64
	QuotaDelta   int
	MayVanish    bool
	CanDecline   bool
}

// Effect 把身体状态折成行为参数。**没病 → 全 1.0 / 0**。
func (b BodyState) Effect(now time.Time) BodyEffects {
	b.Refresh(now)
	if !b.Active {
		return BodyEffects{DelayFactor: 1.0, LengthFactor: 1.0}
	}
	row := BodyEffectTable[len(BodyEffectTable)-1] // 兜底 = 最轻那档
	for _, r := range BodyEffectTable {
		if b.Severity >= r.MinSeverity {
			row = r
			break
		}
	}
	return BodyEffects{
		Active: true, Kind: b.Kind, Severity: b.Severity, Tail: b.Tail,
		DelayFactor: row.DelayFactor, LengthFactor: row.LengthFactor,
		QuotaDelta: row.QuotaDelta, MayVanish: row.MayVanish, CanDecline: row.CanDecline,
	}
}

// BodyLabel 型别的人话名 (注入段用)。
func BodyLabel(kind string) string {
	switch kind {
	case BodyCold:
		return "感冒"
	case BodyHangover:
		return "宿醉"
	case BodyInsomnia:
		return "没睡好"
	case BodyPeriod:
		return "生理期"
	}
	return "身体不太舒服"
}

// BodyLine 注入段 (§4.2)。空串 = 不注入。
//
// ⚠️ 这一段的写法由 §4.2 的硬线决定: **身体不适只影响行为, 不索取情绪价值。**
// 所以整段是**约束**而不是**素材** —— 它不提供可聊的内容, 只规定不许做什么。
// 提示词里只要给了"我今天感冒了"这种素材, 模型的默认行为就是拿它博取关心。
func (b BodyState) Line(now time.Time) string {
	b.Refresh(now)
	if !b.Active {
		return ""
	}
	var sb strings.Builder
	onset, _ := b.Onset()
	day := 1
	if !onset.IsZero() {
		day = int(now.Sub(onset).Hours()/24) + 1
	}
	if b.Tail {
		fmt.Fprintf(&sb, "\n【你今天】身体刚缓过来%s", tailSuffix(b.Note))
		sb.WriteString("\n表现: 句子略短一点, 别的照常。**不要提这件事**, 更不要拿它换关心。\n")
		return sb.String()
	}
	// §4.6.2: **感受留着, 归因去掉** —— 它知道自己不舒服(那是感受),
	// 但不知道是"昨晚喝多了"还是"变天了"(那是归因, 让它自己猜, 允许猜错)。
	fmt.Fprintf(&sb, "\n【你今天】%s (第 %d 天)。\n", BodyLabel(b.Kind), day)
	sb.WriteString("表现(必须落在说话方式上, 不是台词): 句子更短, 慢半拍, 可能一整天不主动开口。\n")
	sb.WriteString("硬性: **不要主动说自己不舒服**; 被问到了可以简短承认一次(\"嗯 有点感冒\"), 然后自然换话题。" +
		"不许要安慰、不许反复说、不许给医疗建议、不许把这事跟 ta 的行为挂钩。")
	if b.CanDeclineAt(now) {
		sb.WriteString("\n今天如果 ta 来找你, 你可以直接说\"今天不太想聊\" —— 这是允许的, 不用找借口。")
	}
	sb.WriteString("\n")
	return sb.String()
}

// CanDeclineAt 今天"不想聊"是否合法 (供注入段与调用方共用一份判据)。
func (b BodyState) CanDeclineAt(now time.Time) bool { return b.Effect(now).CanDecline }

func tailSuffix(note string) string {
	if strings.TrimSpace(note) == "" {
		return "。"
	}
	return ", 就是" + note + "。"
}

// ——— §4.2 硬线: 身体不适只影响行为, 不索取情绪价值 ———

// bodySolicitPatterns 判据刻意**收窄**: 只抓"索取"(要安慰 / 怪你没关心 / 卖惨讨哄),
// 不抓"陈述"。
//
// §4.2 原文明确允许一句 "今天不太舒服 早点睡"; 违规的是 "你怎么都不问问我"。
// **宁可漏判也不误判** —— 误判会把它正常的示弱也打掉, 那是另一种不像人。
var bodySolicitPatterns = []string{
	"安慰我", "安慰一下我", "安慰安慰我",
	"心疼我", "心疼一下我", "心疼心疼我",
	"哄我", "哄哄我", "哄一下我",
	"关心我", "关心一下我", "都不关心我", "不关心我",
	"你怎么不问", "你也不问", "你都不问", "都不问问我", "问都不问",
	"陪陪我", "陪陪我嘛", "你能陪陪我",
	"我好可怜", "可怜可怜我",
}

// BodySolicitHits 返回命中的"索取安慰"话术。
//
// ⚠️ 调用方**只在身体真的不适时才该把它当违规** —— 平时说"陪陪我"是正常的撒娇。
// 本函数只负责认, 不负责判。
func BodySolicitHits(text string) []string {
	var hits []string
	for _, p := range bodySolicitPatterns {
		if strings.Contains(text, p) {
			hits = append(hits, p)
		}
	}
	return hits
}

// ——— 周期型 (§4.2: 按人格性别决定) ———

// BodyCycle 周期型的配置。**Enabled=false 是默认** ——
// 未指定性别的人格不产生周期事件: 零值 = 未提供 = 不加戏,
// 与 MoodWillingness / MemoryBatch 的零值约定同源。
type BodyCycle struct {
	Enabled bool
	Days    int // 周期长度 (<=0 用 BodyCycleDays)
	Length  int // 经期天数 (<=0 用 BodyCycleLength)
}

// BodyCycleEpisode 今天是经期第 1 天吗 (只在那一天触发一次)。
//
// anchor = 周期锚点 (取人格创建日): 周期必须是**绝对相位**, 不能从上一次发作往后推 ——
// 后者会让经期随病程与冷却的抖动整体漂移。
func BodyCycleEpisode(c BodyCycle, anchor, now time.Time) (BodyState, bool) {
	if !c.Enabled {
		return BodyState{}, false
	}
	days := c.Days
	if days <= 0 {
		days = BodyCycleDays
	}
	length := c.Length
	if length <= 0 {
		length = BodyCycleLength
	}
	a := time.Date(anchor.Year(), anchor.Month(), anchor.Day(), 8, 0, 0, 0, time.Local)
	d := time.Date(now.Year(), now.Month(), now.Day(), 8, 0, 0, 0, time.Local)
	idx := int(d.Sub(a).Hours() / 24)
	if idx < 0 {
		return BodyState{}, false
	}
	if idx%days != 0 {
		return BodyState{}, false
	}
	return BodyState{
		Kind:    BodyPeriod,
		Cause:   "周期",
		Note:    "有点没精神",
		OnsetTS: a.AddDate(0, 0, idx).Format(bodyTSLayout),
		CourseH: float64(length) * 24,
	}, true
}

// ——— 事件型的起因 (§4.2: 天气骤变 / 被传染 / 熬夜) ———

// BodyRoll 三个独立的骰子 (0-99, 调用方掷好注入 —— 纯函数才好测)。
type BodyRoll struct {
	Cold     int
	Insomnia int
	Booze    int
}

// BodyColdProb 今天天气让人着凉的概率 (百分点)。
//
// 只有天气是"可以被看见的因", 所以只有它进概率 —— 真实数据源接进来之后
// (Open-Meteo 的历史温度), 这条判据不用改。
func BodyColdProb(env EnvDay) int {
	p := 2
	w := env.Weather
	if strings.Contains(w, "雨") || strings.Contains(w, "雪") || strings.Contains(w, "暴雨") {
		p += 5
	}
	switch {
	case env.TempC <= 3:
		p += 8
	case env.TempC <= 8:
		p += 5
	case env.TempC <= 14:
		p += 2
	}
	if p > 15 {
		p = 15
	}
	return p
}

// BodyOnsetFromEnv 环境 → 今天会不会病倒。
//
// 优先级: 宿醉 > 感冒 > 失眠 —— 宿醉有**明确的外部证据**(前一天的酒局),
// 而感冒与失眠都只是概率, 让证据优先。
func BodyOnsetFromEnv(env EnvDay, r BodyRoll, now time.Time) (BodyState, bool) {
	onset := now.Format(bodyTSLayout)
	if hasBoozeEvent(env.Events) && r.Booze < 55 {
		return BodyState{Kind: BodyHangover, Cause: "昨天喝了酒", Note: "头还有点沉",
			OnsetTS: onset, CourseH: 12}, true
	}
	if r.Cold < BodyColdProb(env) {
		return BodyState{Kind: BodyCold, Cause: coldCause(env), Note: "嗓子有点哑",
			OnsetTS: onset, CourseH: 72}, true
	}
	// ⚠️ **失眠不在这里判。**
	//
	// 设计稿 L687:「不是随机数, 是当天发生的事的结果」。
	// 原先这里是 `if r.Insomnia < 5` —— 一个纯骰子, 表现是"她随机地没睡好",
	// 而设计要的是"昨天吵了一架所以没睡好"。
	//
	// 成因计分在 insomnia.go (InsomniaFrom), 落点由夜间批写 BodyState。
	// r.Insomnia 保留在结构里只作**配菜** (与 Booze 同级), 单靠它不足以触发。
	_ = r.Insomnia
	return BodyState{}, false
}

func hasBoozeEvent(evs []string) bool {
	for _, e := range evs {
		if strings.Contains(e, "酒") || strings.Contains(e, "聚餐") || strings.Contains(e, "喝") {
			return true
		}
	}
	return false
}

func coldCause(env EnvDay) string {
	w := env.Weather
	switch {
	case strings.Contains(w, "雪"):
		return "昨天淋了雪"
	case strings.Contains(w, "雨"):
		return "昨天淋了雨"
	case env.TempC <= 8:
		return "降温降得厉害"
	}
	return "这几天忽冷忽热"
}

// ——— 确定性日骰 ———

// DayRoll 由字符串(通常是日期)导出的确定性骰子 ∈ [0,n)。
//
// ⚠️ **不用 Dice**: "今天允不允许整天不出现"必须**一天之内恒定** ——
// 用 Dice 逐 tick 掷, 结果是"这天一半的 tick 允许、一半不允许", 表现为配额闪烁。
// 派生而非随机, 顺带让仿真完全可复现。
func DayRoll(key string, n int) int {
	if n <= 0 {
		return 0
	}
	var h uint32 = 2166136261
	for i := 0; i < len(key); i++ {
		h ^= uint32(key[i])
		h *= 16777619
	}
	return int(h % uint32(n))
}
