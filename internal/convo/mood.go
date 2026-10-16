package convo

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/tzhnahida/convo/internal/clock"
)

// ——— 世界模型: 事件总线 (§3.2) ———
//
// **任何一层的变化都先变成一条事件, 再汇入心情的成因列表。**
// 这是"世界 → 心情"的**唯一**通道 —— 有了它, 天气/生活事件才不只是注入段里的一句话,
// 而是真的会改变它的状态。
//
// 之前的实测结论 (§1.4 四季 arena): **行为四季同质, 0% 由世界驱动** —— 她主动恒 60、
// 轮次 148-153、预告恒 22, 四个季节完全一样。根因就是没有这条通道:
// env 只喂了注入段 ("你那边下雨了"是一句台词), 没喂任何状态。
type WorldEvent struct {
	TS        string  `json:"ts"`
	Kind      string  `json:"kind"`      // environment | calendar | social | relation | self
	Source    string  `json:"source"`    // 天气源 / 家人 / 同事 / 其他 AI …
	Text      string  `json:"text"`      // 人话描述 (成因展示用)
	Valence   float64 `json:"valence"`   // -1..1 对心情的方向
	Arousal   float64 `json:"arousal"`   // 0..1  唤起度 (烦躁 vs 低落)
	Intensity float64 `json:"intensity"` // 0..1  强度 —— **与关系权重相乘后才生效**
	Weight    float64 `json:"weight"`    // 关系权重 (环境事件 = 1.0)
	Duration  string  `json:"duration"`  // pulse(小时级) | background(周级)
}

// Key 事件的幂等键 —— 同一天同源同文只入账一次 (tick 每分钟都会路过)。
func (e WorldEvent) Key() string {
	day := ""
	if len(e.TS) >= 10 {
		day = e.TS[:10]
	}
	return day + "|" + e.Kind + "|" + e.Source + "|" + e.Text
}

// ——— 心情内核 (§4.1) ———
//
// mood{ valence -1..1 · arousal 0..1 · causes[] · half_life 6-12h · baseline }
//
// ⚠️ **与 A2 的状态同族铁律**: 这个量**由系统持有与更新** (成因 / 半衰期 / 上下限 /
// 变化率上限), 模型只负责**表达** —— 禁止自述与发明。
// 现状修复前 `convo_mood` 只是把日程状态换个表名存了一遍 (note 恒为 "tick"),
// 没人读它 —— 那正是设计稿说的"等于随机数"。
type MoodState struct {
	Valence  float64     `json:"valence"`
	Arousal  float64     `json:"arousal"`
	Causes   []MoodCause `json:"causes"`
	Since    string      `json:"since"`     // 当前心情从何时起 (成因刷新才动)
	LowUntil string      `json:"low_until"` // 低落保护期到何时 (见 Apply)
	Updated  string      `json:"updated"`

	// ——— 背景压力 (§3.2 约束 2) ———
	//
	// "持续性事件不是每小时掷骰子, 而是**一段带衰减的背景压力**, 叠加在脉冲之上。"
	// 天气就是最典型的背景: 它整天都在, 不是早上 07:30 响一下。
	//
	// 没有这一层, 实测的后果是: 心情只被推动 ±0.05, 一天内就衰减回基线 ——
	// 机制跑通了, 但四季的行为仍然一样。**背景不是锦上添花, 它是这条通道能不能 work 的关键。**
	BgValence float64 `json:"bg_valence"`
	BgText    string  `json:"bg_text"`
	BgUntil   string  `json:"bg_until"` // 到这一刻为止有效 (通常 = 当天 23:59:59)
}

type MoodCause struct {
	TS     string  `json:"ts"`
	Kind   string  `json:"kind"`
	Source string  `json:"source"`
	Text   string  `json:"text"`
	Weight float64 `json:"weight"`
	DeltaV float64 `json:"delta_v"` // 这条事件实际推动了多少 (受变化率上限截断后)
	DeltaA float64 `json:"delta_a"`
}

const (
	// MoodHalfLife 不刷新就回基线 —— 防止心情僵化成人设 (§4.1)。取值在 6-12h 中段。
	MoodHalfLife = 9 * time.Hour
	// MoodBaselineValence 人格底色。⚠️ 暂为常量: 接到 persona(乐天/冷淡)是后续工作。
	MoodBaselineValence = 0.10
	MoodBaselineArousal = 0.50
	// MoodMaxDelta 变化率上限 —— **不许被一句话治好** (§4.1)。
	MoodMaxDelta = 0.15
	// MoodLowProtect 坏心情至少持续半个半衰期。
	MoodLowProtect = MoodHalfLife / 2
	// MoodCauseCap 成因列表上限 (只留最近的, 免得无限长)。
	MoodCauseCap = 12
)

func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Decay 按半衰期向基线回归 (不刷新就回)。
func (m *MoodState) Decay(now time.Time) {
	if m.Updated == "" {
		return
	}
	ts, err := time.ParseInLocation("2006-01-02 15:04:05", m.Updated, time.Local)
	if err != nil {
		return
	}
	h := now.Sub(ts).Hours()
	if h <= 0 {
		return
	}
	f := math.Pow(0.5, h/MoodHalfLife.Hours())
	target := m.baselineAt(now)
	m.Valence = target + (m.Valence-target)*f
	m.Arousal = MoodBaselineArousal + (m.Arousal-MoodBaselineArousal)*f
}

// baselineAt 心情回落到哪里 —— 有生效中的背景压力就落在它上面, 否则回人格底色。
func (m MoodState) baselineAt(now time.Time) float64 {
	if m.BgUntil == "" {
		return MoodBaselineValence
	}
	t, err := time.ParseInLocation("2006-01-02 15:04:05", m.BgUntil, time.Local)
	if err != nil || !now.Before(t) {
		return MoodBaselineValence // 背景过期, 回底色
	}
	return clampF(MoodBaselineValence+m.BgValence, -1, 1)
}

// Apply 把一条事件汇入心情; 返回这条事件**实际**推动了多少 (截断后)。
//
// 三条约束按顺序生效, 顺序不能换:
//
//	① 先按半衰期衰减到此刻 ("最新的心情"才谈得上变化率);
//	② **强度 × 关系权重**相乘后才生效 (§3.2 约束 1);
//	③ 变化率上限截断 —— 并额外保证**坏心情不会因为一句好话就翻篇**。
func (m *MoodState) Apply(ev WorldEvent, now time.Time) (dv, da float64) {
	m.Decay(now)
	w := ev.Intensity * ev.Weight
	dv = clampF(ev.Valence*w, -MoodMaxDelta, MoodMaxDelta)
	da = clampF((ev.Arousal-MoodBaselineArousal)*w, -MoodMaxDelta, MoodMaxDelta)

	// 低落保护: 坏心情至少持续半个半衰期 —— 期间正向事件只给一半效果。
	// 没有这一条, "它心情不好"会被用户一句安慰当场治好, 那是假人。
	if dv > 0 && m.inLowProtect(now) {
		dv *= 0.5
	}

	before := m.Valence
	m.Valence = clampF(m.Valence+dv, -1, 1)
	dv = m.Valence - before // 记录**实际**变化 (被上下限夹掉的部分不算)
	m.Arousal = clampF(m.Arousal+da, 0, 1)

	// 跌进负值 → 开一段保护期
	if before >= 0 && m.Valence < 0 {
		m.LowUntil = now.Add(MoodLowProtect).Format("2006-01-02 15:04:05")
	}
	if m.Since == "" || dv != 0 {
		m.Since = clock.TodayTS()
	}
	m.Updated = clock.TodayTS()
	m.Causes = append(m.Causes, MoodCause{
		TS: now.Format("2006-01-02 15:04:05"), Kind: ev.Kind, Source: ev.Source,
		Text: ev.Text, Weight: w, DeltaV: dv, DeltaA: da,
	})
	if len(m.Causes) > MoodCauseCap {
		m.Causes = m.Causes[len(m.Causes)-MoodCauseCap:]
	}
	return dv, da
}

func (m *MoodState) inLowProtect(now time.Time) bool {
	if m.LowUntil == "" {
		return false
	}
	t, err := time.ParseInLocation("2006-01-02 15:04:05", m.LowUntil, time.Local)
	if err != nil {
		return false
	}
	return now.Before(t)
}

// MoodWillingnessFloor 意愿下限 —— **心情不是开关**。
// 人不会因为心情差就彻底不理人; 心情只让它「回得慢一点、少开一次口」。
const MoodWillingnessFloor = 0.25

// Willingness 说话意愿 0..1 —— §8.3「心情直接进说话决策器」。
//
// 两个分量相乘: 坏心情压低它, 低唤醒再压低一次 (§4.1 低唤醒表现为句子短/延迟长/少表情)。
// ⚠️ 它**不是随机**: 同一个心情值给出的意愿是确定的, 掷骰子发生在别处。
// 「是自身状态导致的波动、不是随机」这句设计约束, 就靠这条区分守住。
func (m MoodState) Willingness(now time.Time) float64 {
	m.Decay(now)
	w := 0.5 + 0.5*m.Valence // 坏心情 → 低
	w *= 0.6 + 0.6*m.Arousal // 低唤醒 → 再低一次
	return clampF(w, MoodWillingnessFloor, 1.0)
}

// Label 把心情折成一个词 (给注入用, 不是给模型报备用)。
func (m MoodState) Label() string {
	switch {
	case m.Valence >= 0.35:
		return "挺好的"
	case m.Valence >= 0.05:
		return "还行"
	case m.Valence >= -0.25:
		return "有点低"
	default:
		return "挺低的"
	}
}

// Line 渲染注入段。
//
// ⚠️ **表达要节制 (§4.1)**: 它描写的是"漏出来"的方式, 不是让模型去报备。
// "我今天心情不好"是最假的写法 —— 所以这句话必须写进注入里, 否则模型一定会去解释原因。
func (m MoodState) Line(now time.Time) string {
	var b strings.Builder
	m.Decay(now)
	// ⚠️ 2026-10-15 (§4.6.2 内感受模糊): **这里原来打印 valence / arousal 的数值, 还附了「最近的起因」。**
	//
	// 那一段同时犯了两件事: ① 给的是**量**不是**感觉**; ② 给的是**归因** ——
	// 而紧接着的下一行写着「**不许解释原因**」。**它当然会解释: 你已经把原因告诉它了。**
	//
	// 真人不是这样读自己的: 低血糖引起的易怒不会被体验成"我饿了", 会被体验成"你怎么这么烦"。
	// **允许它猜错, 才像人。** 它说不清的时候那句「我也不知道我怎么了」是合法输出, 不是错误。
	//
	// ⚠️ 数值与成因**照样落库** —— 那是给 owner 的仪表盘, 不是给它的剧本(与 §4.2.3 同一条纪律)。
	fmt.Fprintf(&b, "\n【你现在的心情】%s。", m.Label())
	b.WriteString("⚠️ 心情只许**漏出来**, 不许报备: 不许说「我今天心情不好」, **不许解释原因**。")
	if m.Arousal < MoodBaselineArousal {
		b.WriteString("低唤醒的表现是: 句子更短、标点更少、回得更慢、少发表情。")
	}
	if m.Valence < 0 {
		b.WriteString("坏心情的表现是: 更容易抬杠, 或者干脆不想说话 —— **都不是说出来, 是漏出来**。")
	}
	return b.String()
}

// EnvEvents 把当天环境折成世界事件 (§3.3 事件目录的最小可用集)。
//
// 这是 env → 心情的**适配器**: 正式接真实气象 (Open-Meteo) 与地震 (USGS) 时,
// 只需换 EnvSource, 这一层一行不改。
func EnvEvents(e EnvDay) []WorldEvent {
	if e.Weather == "" && len(e.Events) == 0 {
		return nil
	}
	var out []WorldEvent
	base := WorldEvent{TS: clock.TodayTS(), Kind: "environment", Weight: 1.0, Duration: "pulse"}
	add := func(source, text string, val, aro, inten float64) {
		ev := base
		ev.Source, ev.Text, ev.Valence, ev.Arousal, ev.Intensity = source, text, val, aro, inten
		out = append(out, ev)
	}

	// 天气是**背景**而不是脉冲: 它整天都在。所以合成**一条**当天底色事件,
	// 由 IngestWorldEvents 走背景通道 (设 BgValence/BgUntil), 不经逐条 Apply。
	// Valence 直接就是"今天的心情底色偏移", Intensity/Weight 恒为 1 (不再二次缩放)。
	w := e.Weather
	bg, bgText := 0.0, ""
	switch {
	case strings.Contains(w, "暴雨") || strings.Contains(w, "雷"):
		bg, bgText = -0.35, "今天下暴雨"
	case strings.Contains(w, "雪"):
		bg, bgText = 0.20, "今天下雪了"
	case strings.Contains(w, "雨"):
		bg, bgText = -0.20, "今天下雨"
	case strings.Contains(w, "阴"):
		bg, bgText = -0.10, "今天阴天"
	case strings.Contains(w, "多云"):
		bg, bgText = 0.05, "今天多云"
	case strings.Contains(w, "闷热"):
		bg, bgText = -0.15, "今天闷热"
	case strings.Contains(w, "雾"):
		bg, bgText = -0.10, "今天有雾"
	case strings.Contains(w, "晴"):
		bg, bgText = 0.15, "今天晴天"
	}
	if e.TempC >= 33 {
		bg -= 0.15
		bgText += fmt.Sprintf(", %d 度热得不行", e.TempC)
	} else if e.TempC <= 0 {
		bg -= 0.10
		bgText += fmt.Sprintf(", %d 度", e.TempC)
	}
	// ⚠️ **兜底: 认不出的天气也要入账, 只是中性**。
	//
	// 实测踩过: 词表只覆盖了晴/阴/雨/雪, 而天气池里还有"多云"与"闷热" ——
	// 于是约 1/4 的日子**一条事件都不产生**, 心情完全不受那些天影响。
	// 更坏的是它**静默**: 报告只显示"30 天入账 18 条", 看不出少了 12 天。
	//
	// 所以认不出 → 照样建事件、照样进成因链, 只是强度为 0。
	// **看得见的中性, 好过看不见的消失。**
	if bgText == "" && w != "" {
		bgText = "今天" + w
	}
	if bgText != "" {
		ev := base
		ev.Source, ev.Text, ev.Valence, ev.Arousal, ev.Intensity = "天气", bgText, bg, 0.5, 1.0
		ev.Duration = "background"
		out = append(out, ev)
	}
	for _, ev := range e.Events {
		add("生活", ev, envEventValence(ev), 0.5, envEventIntensity(ev))
	}
	return out
}

// DayDrafts 把当天**带时刻**的生活事件变成事件池草稿 (§3.6)。
//
// 与 EnvEvents 的分工: EnvEvents 是"今天一整天都在的底色"(天气), 立刻入账;
// DayDrafts 是"今天某个时刻会发生的事", **到点才入账** —— 世界因此第一次在日内会动。
//
// ⚠️ 时刻缺失的事件退回"当天开始" (与旧行为一致, 不制造新的静默差异)。
func DayDrafts(e EnvDay, day string) []WorldEvent {
	var out []WorldEvent
	for _, m := range e.Moments {
		ts := day + " 00:00:00"
		if m.TS != "" {
			ts = day + " " + m.TS + ":00"
		}
		out = append(out, WorldEvent{
			TS: ts, Kind: "environment", Source: "生活", Text: m.Text,
			Valence: envEventValence(m.Text), Arousal: 0.5,
			Intensity: envEventIntensity(m.Text), Weight: 1.0, Duration: "pulse",
		})
	}
	return out
}

// envEventValence / envEventIntensity 给生活事件定方向与强度。
// ⚠️ 只覆盖已知词; 认不出的一律**中性** —— 宁可没效果, 不可乱给情绪。
func envEventValence(s string) float64 {
	switch {
	case strings.Contains(s, "地震") || strings.Contains(s, "台风"):
		return -0.6
	case strings.Contains(s, "花粉"):
		return -0.25
	case strings.Contains(s, "祭") || strings.Contains(s, "节"):
		return 0.25
	default:
		return 0
	}
}

func envEventIntensity(s string) float64 {
	switch {
	case strings.Contains(s, "地震") || strings.Contains(s, "台风"):
		return 0.9
	case strings.Contains(s, "花粉"):
		return 0.5
	default:
		return 0.3
	}
}

// NewMoodState 初始心情 = 人格底色。
func NewMoodState() MoodState {
	return MoodState{Valence: MoodBaselineValence, Arousal: MoodBaselineArousal}
}
