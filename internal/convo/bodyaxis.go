package convo

import (
	"fmt"
	"strings"
	"time"

	"github.com/tzhnahida/convo/internal/clock"
	"github.com/tzhnahida/convo/internal/log"
)

// ─────────────────────────────────────────────────────────────
// §4.2.2 **身体的三个连续量**: 精力 / 饥饿 / 口渴 (2026-10-15)
//
// ⚠️ **只存事实, 当前值一律现算** —— 与 §4.1「热度现算不落库」同一条纪律。
// 存派生量就必须回答"谁在什么时候重算它", 而这个量是时间的纯函数。
//
// ⚠️ **零值 = 未提供 ≠ 极值**: 这四个量没有"未提供"这一档, 它们全都有明确基线,
// 库里读不到一律折成**基线**, **绝不许折成 0**(折成 0 就是"精力为零"和"饿死了")。
// 这条与 §3 的 WMO code 0 = 晴 是同一个坑。
//
// ⚠️ **变化必须走在 tick 上, 不许挂在对话上**:
// 「它饿不饿、渴不渴, 与有没有人找它说话无关。」
//
// ⚠️ **注入的是档位, 不是数字。** 提示词里写「有点饿」, **绝不写「饥饿度 63%」** ——
// 给了数字模型就会照着报数, 那是最假的写法。数值只对 owner 可见。
// ─────────────────────────────────────────────────────────────

// 三条量的基线 (§4.2.2)。
//
// ⚠️ 这三个数是**基线**, 不是"默认值": 读不到任何事实时, 它就是一个不累不饿不渴的人。
const (
	EnergyBaseline = 0.8
	HungerBaseline = 0.2
	ThirstBaseline = 0.2
)

// 身体事实的种类 —— **只存事实**。
const (
	FactMeal     = "meal"     // 吃了一顿
	FactDrink    = "drink"    // 喝了点什么
	FactSleep    = "sleep"    // 睡了一觉 (Minutes = 时长)
	FactNap      = "nap"      // 打了个盹
	FactExercise = "exercise" // 动了动 (Minutes = 时长)
)

// bodyFactKinds 合法事实种类。
var bodyFactKinds = map[string]bool{
	FactMeal: true, FactDrink: true, FactSleep: true, FactNap: true, FactExercise: true,
}

// BodyAxisFact 一条身体事实。
type BodyAxisFact struct {
	// Src 去重键 —— 同一次"吃了"重跑不许记两次。
	//
	// ⚠️ 靠唯一索引而不是"先查后插": 先查后插在两次运行之间有一个窗口,
	// 而夜里那批会重跑(睡眠状态转换可能触发两次) —— 双击一次就是双倍进食。
	Src     string  `json:"src,omitempty"`
	Day     string  `json:"day"`
	Fact    string  `json:"fact"`
	TS      string  `json:"ts"`
	Minutes float64 `json:"minutes,omitempty"`
	Note    string  `json:"note,omitempty"`
}

// Axes 三个连续量 (0..1)。**全部现算**。
type Axes struct {
	Energy float64 `json:"energy"`
	Hunger float64 `json:"hunger"`
	Thirst float64 `json:"thirst"`
	// 有没有真的事实支撑 (false = 全是基线, 界面上要能看出来)。
	HasFacts bool `json:"has_facts"`
}

// AxesInput 现算要用的**全部**输入。纯函数, 不碰库 —— 这样它可测。
type AxesInput struct {
	Now   time.Time
	Facts []BodyAxisFact
	// Hot 天气热 / 闷 (推动口渴)。
	Hot bool
	// Sick 有活动中的病程 (推动精力)。
	Sick bool
}

// ComputeAxes 现算三条量。
//
// ⚠️ 读不到任何事实 → **基线**, 不是 0。这是本文件最重要的一条。
func ComputeAxes(in AxesInput) Axes {
	a := Axes{Energy: EnergyBaseline, Hunger: HungerBaseline, Thirst: ThirstBaseline}
	// ⚠️ **一条事实都没有 → 就是基线, 到此为止。**
	//
	// 这条早返回不是优化, 是语义: 「读不到任何线索」和「真的从早到晚没吃东西」是两件事。
	// 让时间在上面继续走, 一个刚装好的库会在傍晚凭空长出一个饿坏了的人 ——
	// 那就是 §4.2.2 那条「零值 = 未提供 ≠ 极值」的告警换了个形状又回来。
	// ⚠️ 判据是「**有没有一条用得上的事实**」, 不是「facts 数组空不空」:
	// 传进来三条但时刻全是坏的, 与一条都没传, 在语义上是同一件事(未提供)。
	usable := false
	for _, f := range in.Facts {
		if _, ok := factMinuteOfDay(f); ok {
			usable = true
			break
		}
	}
	if !usable {
		return a
	}
	cur := in.Now.Hour()*60 + in.Now.Minute()

	var lastMeal, lastDrink *int // 距现在多少分钟
	sleptMin := 0.0
	exercised := false
	for _, f := range in.Facts {
		m, ok := factMinuteOfDay(f)
		if !ok {
			continue
		}
		ago := cur - m
		if ago < 0 {
			ago += 24 * 60 // 昨天的事实
		}
		a.HasFacts = true
		switch f.Fact {
		case FactMeal:
			if lastMeal == nil || ago < *lastMeal {
				v := ago
				lastMeal = &v
			}
		case FactDrink:
			if lastDrink == nil || ago < *lastDrink {
				v := ago
				lastDrink = &v
			}
		case FactSleep:
			sleptMin += f.Minutes
		case FactNap:
			a.Energy += 0.08
		case FactExercise:
			exercised = true
		}
	}

	// ——— 精力 ———
	// 睡不够 → 掉; 睡饱了 → 回一点(但不超过基线)。
	if sleptMin > 0 {
		if sleptMin < 360 {
			a.Energy -= (360 - sleptMin) / 360 * 0.45
		} else if sleptMin > 480 {
			a.Energy += 0.05
		}
	}
	if in.Sick {
		a.Energy -= 0.2
	}
	if exercised {
		a.Energy -= 0.05
	}

	// ——— 饥饿 / 口渴 ———
	// ⚠️ **没记录 ≠ 刚吃过**: 没有进食事实时, 从**当天 00:00** 起算 —— 那是"今天还没吃"。
	// 折成"刚吃过"会让一个整天没吃的角色永远不饿, 折成"饿了很久"则是凭空。
	mealAgo := cur
	if lastMeal != nil {
		mealAgo = *lastMeal
	}
	// ⚠️ 标定: 0.04/小时 → 一整天没吃才接近顶格。
	// 原来写的 0.06 一天能涨 1.44, 也就是**下午必然顶格** —— 那不是"饿了", 那是"永远在饿"。
	a.Hunger += float64(mealAgo) / 60 * 0.04
	drinkAgo := cur
	if lastDrink != nil {
		drinkAgo = *lastDrink
	}
	a.Thirst += float64(drinkAgo) / 60 * 0.04
	if in.Hot {
		a.Thirst += 0.15
	}
	if exercised {
		a.Hunger += 0.08
		a.Thirst += 0.10
	}

	a.Energy = clamp01(a.Energy)
	a.Hunger = clamp01(a.Hunger)
	a.Thirst = clamp01(a.Thirst)
	return a
}

// factMinuteOfDay 事实在一天里的第几分钟。解析不出 → ok=false(**跳过, 不折成 0**:
// 折成 0 等于"凌晨刚吃过", 那会让它永远不饿)。
func factMinuteOfDay(f BodyAxisFact) (int, bool) {
	ts := strings.TrimSpace(f.TS)
	if len(ts) < 5 {
		return 0, false
	}
	// 容忍 "2006-01-02 15:04:05" 与 "15:04" 两种写法。
	if len(ts) >= 16 {
		ts = ts[11:16]
	}
	m, err := parseHM(ts)
	if err != nil {
		return 0, false
	}
	return m, true
}

// ——— 档位 (注入用的**唯一**形态) ———

// EnergyLevel 精力档位。**正常时返回空串** —— 段要"没有就一个字都不加"。
func EnergyLevel(v float64) string {
	switch {
	case v < 0.35:
		return "很困, 反应慢"
	case v < 0.62:
		return "有点乏"
	}
	return ""
}

// HungerLevel 饥饿档位。
func HungerLevel(v float64) string {
	switch {
	case v > 0.65:
		return "很饿"
	case v > 0.38:
		return "有点饿"
	}
	return ""
}

// ThirstLevel 口渴档位。
func ThirstLevel(v float64) string {
	switch {
	case v > 0.65:
		return "很渴"
	case v > 0.38:
		return "有点渴"
	}
	return ""
}

// BodyAxesLine 渲染身体段。三档全空 → 空串(**一个字都不加**)。
//
// ⚠️ 三条量的落点**必须互不重叠**(§4.2.3), 否则它们会退化成同一个疲劳因子的四个名字:
//
//	· 精力 → 回得慢一点、短一点(§4.3 疲劳已经在管延迟与拒绝权门槛);
//	· 饥饿 → 耐心低一点;
//	· 口渴 → **唯一允许说出口的那一个**(§4.2.4)。
//
// ⚠️ 这条边界必须写进提示词: 不写的话它会把"有点乏"说成"我好累", 而那是**索取**。
func BodyAxesLine(a Axes, maySayThirst bool) string {
	// ⚠️ **零值 = 未提供 ≠ 极值。**
	//
	// `Axes{}`(三个都是 0)只可能来自"调用方没设这个字段" —— 真正的现算永远至少给到基线。
	// 不挡的话, 一个没接线的新调用方会注入「精力: 很困」, 而它其实一切正常。
	// 这与 §4.2.2 那条告警是同一件事, 只是发生在注入这一侧。
	if a.Energy == 0 && a.Hunger == 0 && a.Thirst == 0 {
		return ""
	}
	e, h, t := EnergyLevel(a.Energy), HungerLevel(a.Hunger), ThirstLevel(a.Thirst)
	if e == "" && h == "" && t == "" {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n【身体】你自己的状态 (**不要说出口**, 让它从别的地方漏出来)\n")
	if e != "" {
		fmt.Fprintf(&sb, "- 精力: %s —— 回得慢一点、句子短一点就够, **不许说「我好累」**(那是索取)\n", e)
	}
	if h != "" {
		fmt.Fprintf(&sb, "- 饥饿: %s —— 耐心低一点, **不许说「我饿了」**(同上)\n", h)
	}
	if t != "" {
		// §4.2.4: 口渴是**唯一**的正面例外 —— 因为它不指向对方。
		//
		// ⚠️ 但这个例外是**条件授予**的: 预算用完 / 在冷却期 → 退回和其他两条一样的待遇。
		// 做法是**不给许可**, 而不是告诉它"你今天还有 0 次" ——
		// 后者就是 §4.6.1 说的"一个知道自己 KPI 的人"。
		if maySayThirst {
			fmt.Fprintf(&sb, "- 口渴: %s —— **这一个可以说出口**(「等下 我去倒杯水」), 因为它不指向对方;\n"+
				"  但**不许说成「我好渴啊」** —— 那是在等对方接话, 就变成索取了;\n"+
				"  也**不许在刚说完一件正经事之后马上接这一句** —— 那会被读成「我不想聊了」\n", t)
		} else {
			fmt.Fprintf(&sb, "- 口渴: %s —— **不要说出口**, 喝口水这事你自己知道就行\n", t)
		}
	}
	return sb.String()
}

// ——— 存储 (只有事实) ———

// EnsureBodyAxisSchema 幂等建表。
func EnsureBodyAxisSchema(s *Store) error {
	_, err := s.DB.Exec(`CREATE TABLE IF NOT EXISTS convo_body_axis (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		src TEXT DEFAULT '',
		day TEXT NOT NULL, fact TEXT NOT NULL, ts TEXT NOT NULL,
		minutes REAL DEFAULT 0, note TEXT DEFAULT '', created_ts TEXT DEFAULT ''
	)`)
	if err != nil {
		return err
	}
	// 老库补列 (新库 CREATE 已带)。
	s.DB.Exec(`ALTER TABLE convo_body_axis ADD COLUMN src TEXT DEFAULT ''`)
	if _, err = s.DB.Exec(`CREATE INDEX IF NOT EXISTS idx_body_axis_day ON convo_body_axis(day)`); err != nil {
		return err
	}
	// ⚠️ 去重靠**唯一索引**, 不靠"先查后插"。
	_, err = s.DB.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_body_axis_src ON convo_body_axis(src) WHERE src <> ''`)
	return err
}

// RecordBodyFact 记一条身体**事实**。
//
// ⚠️ 种类非法 → 拒写。放进去的话, 一个拼错的 fact 会**永远不被任何分支读到**,
// 而症状是"吃了却没回血"—— 静默且极难查。
func (s *Store) RecordBodyFact(f BodyAxisFact) error {
	if s == nil || s.DB == nil {
		return fmt.Errorf("bodyaxis: store 未初始化")
	}
	f.Fact = strings.TrimSpace(strings.ToLower(f.Fact))
	if !bodyFactKinds[f.Fact] {
		return fmt.Errorf("bodyaxis: 未知的事实种类 %q", f.Fact)
	}
	if strings.TrimSpace(f.Day) == "" {
		f.Day = clock.Today()
	}
	if strings.TrimSpace(f.TS) == "" {
		f.TS = clock.Now().Format(bodyTSLayout)
	}
	// ⚠️ `INSERT OR IGNORE` + 唯一索引: 同一个 src 重跑一次**不会**变成吃了两顿。
	// 而如果是"先查后插", 中间那个窗口照样能双击进去。
	_, err := s.DB.Exec(`INSERT OR IGNORE INTO convo_body_axis (src, day, fact, ts, minutes, note, created_ts)
		VALUES (?,?,?,?,?,?,?)`, f.Src, f.Day, f.Fact, f.TS, f.Minutes, f.Note, clock.TodayTS())
	return err
}

// RecentBodyFacts 取最近 window 小时内的事实 (现算的输入)。
func (s *Store) RecentBodyFacts(now time.Time, hours int) ([]BodyAxisFact, error) {
	if hours <= 0 {
		hours = 24
	}
	since := now.Add(-time.Duration(hours) * time.Hour).Format(bodyTSLayout)
	rows, err := s.DB.Query(`SELECT src, day, fact, ts, minutes, note FROM convo_body_axis
		WHERE ts >= ? ORDER BY ts ASC`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BodyAxisFact
	for rows.Next() {
		var f BodyAxisFact
		if err := rows.Scan(&f.Src, &f.Day, &f.Fact, &f.TS, &f.Minutes, &f.Note); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// ——— 从行程里认出"吃了 / 喝了" ———

// 关键词表。⚠️ 这是**粗**的 —— 但粗得诚实: 认不出就是没有这一顿, 而不是猜一顿。
var mealWords = []string{"吃", "饭", "餐", "外卖", "食堂", "夜宵", "烧烤", "火锅", "米线", "面"}
var drinkWords = []string{"喝", "咖啡", "奶茶", "拿铁", "美式", "茶", "可乐", "果汁", "汽水"}

// IsMealText 这条行程是不是"吃"。
func IsMealText(s string) bool {
	for _, w := range mealWords {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

// IsDrinkText 这条行程是不是"喝"。
func IsDrinkText(s string) bool {
	for _, w := range drinkWords {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

// RecordPlanFacts 把**已经过去**的行程条目落成身体事实。
//
// ⚠️ 只记**已经发生**的(At <= now): 把晚上的安排当成"已经吃了", 会让它在下午就不饿。
// ⚠️ 去重键 `plan:<day>:<at>` —— 夜里那批会重跑, 没有它就是吃了两顿。
func (e *Engine) RecordPlanFacts(day string, plan DayPlan, now time.Time) (meals, drinks int) {
	if e.Store == nil {
		return 0, 0
	}
	cur := now.Hour()*60 + now.Minute()
	for _, it := range plan.Items {
		m, err := parseHM(it.At)
		if err != nil || m > cur {
			continue
		}
		ts := day + " " + it.At + ":00"
		src := "plan:" + day + ":" + it.At
		switch {
		case IsMealText(it.What):
			if err := e.Store.RecordBodyFact(BodyAxisFact{Src: src, Day: day, Fact: FactMeal, TS: ts, Note: it.What}); err == nil {
				meals++
			}
		case IsDrinkText(it.What):
			if err := e.Store.RecordBodyFact(BodyAxisFact{Src: src, Day: day, Fact: FactDrink, TS: ts, Note: it.What}); err == nil {
				drinks++
			}
		}
	}
	if meals > 0 || drinks > 0 {
		log.Info("convo:bodyaxis", "§4.2.2 从行程落成身体事实: 吃 %d / 喝 %d (%s)", meals, drinks, day)
	}
	return meals, drinks
}

// ——— 引擎侧 ———

// bodyAxes 现算当前三条量 (读事实 + 天气 + 病程)。
//
// ⚠️ 任何一步读不到 → **跳过那一步**, 保留基线。绝不因为一次读库失败就把它变成"精疲力尽"。
func (e *Engine) bodyAxes() Axes {
	if e.Store == nil {
		return Axes{Energy: EnergyBaseline, Hunger: HungerBaseline, Thirst: ThirstBaseline}
	}
	now := clock.Now()
	in := AxesInput{Now: now}
	if facts, err := e.Store.RecentBodyFacts(now, 24); err == nil {
		in.Facts = facts
	}
	if env, ok := TodayEnv(); ok {
		in.Hot = env.TempC >= 28 || strings.Contains(env.Weather, "闷")
	}
	// ⚠️ `convo_body_state` 是**一行**(id=1): 它记的是"当前那一次发作"。
	// 活动与否**现算**(BodyState.Active), 不落库 —— 与 severity 同一条纪律。
	if b, err := e.Store.LoadBody(); err == nil {
		in.Sick = b.Effect(now).Active
	}
	return ComputeAxes(in)
}
