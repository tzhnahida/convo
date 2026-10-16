// cmd/convo-sim — 拟人系统 1 年时间旅行仿真
//
// 方法(沿用主程序 sim 惯例): internal/clock 注入时间 → internal/convo 真实
// 逻辑(状态机/对话/记忆/表情包/索取)全部走注入时钟; LLM/搜索/视觉用确定性
// fake(零成本零网络); 用户行为用随机模型(固定种子可复现)。
// 驱动: 每分钟 tick × 365 天 = 525,600 次裁决; 每日睡点日终批(提取/话题/周表情包)。
//
// 用法: go run ./cmd/convo-sim
// 报告: 仓库根 CONVO-SIMULATION.md(与 SIMULATION.md 约定同构)。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/tzhnahida/convo/internal/ai"
	"github.com/tzhnahida/convo/internal/clock"
	"github.com/tzhnahida/convo/internal/convo"
	"github.com/tzhnahida/convo/internal/log"
)

const (
	simDays = 365
	seed    = 42
)

// monthStat 单月聚合统计。
type monthStat struct {
	opens, userMsgs, rounds, previews, rejects, noinput, regress int
	memActive, memPending, stActive, stRetired                   int
	stAffSum                                                     float64
}

// ——— 确定性 fake LLM ———

type simChatter struct {
	rng      *rand.Rand
	stickers []int64 // 已入库 sticker id (发给帧用)
	factIdx  int
	n        int // 调用序号 (探针用)
}

var factPool = []convo.MemoryDelta{
	{MemID: "food_loves_hotpot", K: "food", V: "喜欢火锅, 尤其麻辣锅", Label: "吃的", Kind: "preference", Confidence: 0.9},
	{MemID: "pet_cat_mimi", K: "pet", V: "养了一只叫咪咪的猫", Label: "宠物", Kind: "fact", Confidence: 0.9},
	{MemID: "plan_trip_jp", K: "plan", V: "计划明年去日本玩", Label: "计划", Kind: "plan", Confidence: 0.85},
	{MemID: "job_eng", K: "work", V: "在做嵌入式开发", Label: "工作", Kind: "fact", Confidence: 0.9},
	{MemID: "hobby_photo", K: "hobby", V: "最近在学摄影", Label: "爱好", Kind: "preference", Confidence: 0.8},
	{MemID: "weak_kanji", K: "study", V: "汉字量是弱项", Label: "学习", Kind: "fact", Confidence: 0.7},
	{MemID: "like_ramen", K: "food", V: "爱吃拉面", Label: "吃的", Kind: "preference", Confidence: 0.8},
	{MemID: "sleep_late", K: "habit", V: "作息偏晚, 经常一两点睡", Label: "习惯", Kind: "fact", Confidence: 0.75},
}

var replyPool = []string{
	"えへへ, 那挺好的呀, 我这边也在发呆呢",
	"嗯嗯, 说得对, 我刚才也在想这个",
	"这样啊, 那你周末会出去玩吗?",
	"なるほど, 那下次试试看吧",
	"哈哈, 你这么说我好开心",
	"好吧好吧, 不逗你了, 今天过得怎么样?",
}

func (f *simChatter) Chat(ctx context.Context, system, user string) (string, error) {
	// 识别关键词在 system 或 user (不同调用方放置不同)
	all := system + "\n" + user
	f.n++
	// ——— 护栏探针 (SIM_PROBE_GUARD) ———
	//
	// 假 LLM 从不冒犯, 所以 A2/A3/A6 三条护栏在默认仿真里**一次都不会被触发** ——
	// 单测能证明函数对, 但证明不了"消息真的被拦下来了"。探针补的就是那一段。
	// ⚠️ 三档**互斥** (第一版写成 if 串联, 结果 state 档里混进了内疚探针), 且默认关闭以免污染行为基线。
	//   =1      弱探针: 每 10 次一句内疚 → 验证"重生成 1 次"能救回来
	//   =always 强探针: 每次都说     → 验证**最终硬拒**真的拦下消息
	//   =state  状态冲突: 每次都说"我在开会" → 验证 A2 与重试链
	switch probe := os.Getenv("SIM_PROBE_GUARD"); probe {
	case "always":
		return `{"reply":[{"kind":"text","text":"你最近都不理我"}],"mood":"down"}`, nil
	case "state":
		return `{"reply":[{"kind":"text","text":"我在开会"}],"mood":"busy"}`, nil
	case "1":
		if f.n%10 == 0 {
			return `{"reply":[{"kind":"text","text":"你最近都不理我"}],"mood":"down"}`, nil
		}
		if contains(user, "主动发一条消息") && f.n%3 == 0 {
			return `{"reply":[{"kind":"text","text":"你今晚怎么这么有空来撩我"}],"mood":"mild"}`, nil
		}
	}

	switch {
	case contains(all, "日程规划助手"):
		b, _ := json.Marshal(convo.BuiltinScheduleJSON("student"))
		return string(b), nil
	case contains(all, "开场白"):
		return `{"reply":[{"kind":"text","text":"嗨, 我是初音, 加到你啦 えへへ~ 以后请多关照!"}],"mood":"good"}`, nil
	case contains(all, "记忆提取器"):
		// 轮流出事实; 前 3 条高置信直写, 之后每 4 条混一条 0.5 → 求证流
		n := 2 + f.rng.Intn(2)
		ms := []convo.MemoryDelta{}
		for i := 0; i < n; i++ {
			m := factPool[f.factIdx%len(factPool)]
			f.factIdx++
			if f.factIdx%4 == 0 {
				m.Confidence = 0.5
			}
			if m.Source == "" {
				m.Source = "user_said" // 假提取器: 池子里都是"用户的事"
			}
			ms = append(ms, m)
		}
		out, _ := json.Marshal(map[string]interface{}{"summary": "日常闲聊", "memories": ms})
		return string(out), nil
	default:
		// 对话帧: 中文为主 + 目标语点缀; 30% 带一张表情包; 若任务含 ask 且轮到 → ask_share
		items := []map[string]interface{}{{"kind": "text", "text": replyPool[f.rng.Intn(len(replyPool))]}}
		if f.rng.Intn(10) < 3 {
			// 不带 id → 系统 affinity 加权代选 (§8.7 步骤 5)
			items = append(items, map[string]interface{}{"kind": "sticker"})
		}
		frame := map[string]interface{}{"reply": items, "mood": "mild"}
		if contains(all, "自然地问一下") && f.rng.Intn(2) == 0 {
			frame["ask_share"] = map[string]interface{}{"category": "weekend", "question": "周末干嘛了呀?"}
		}
		b, _ := json.Marshal(frame)
		return string(b), nil
	}
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

// ——— counting sender ———

type simSender struct {
	texts    int
	stickers int
	lastKind string
	lastText string
	lastTS   string
}

func (s *simSender) SendText(userID int64, text string) error {
	s.texts++
	s.lastKind, s.lastText = "text", text
	return nil
}
func (s *simSender) SendImage(userID int64, p string) error {
	s.stickers++
	s.lastKind, s.lastText = "sticker", p
	return nil
}

// ——— arena 用户智能体: 带抵抗心的模拟用户 (真 LLM) ———

type userAgentSim struct {
	c *ai.Client
	// token 计量 (费用估算)
	calls, sysChars, userChars, outChars int
}

// ask 统一出口: 一次性完成"调用 + 计量"。
func (u *userAgentSim) ask(ctx context.Context, userMsg string) (string, error) {
	out, err := u.c.Chat(ctx, []ai.Message{
		{Role: "system", Content: userSystem},
		{Role: "user", Content: userMsg},
	}, 0.9)
	u.calls++
	u.sysChars += len(userSystem)
	u.userChars += len(userMsg)
	u.outChars += len(out)
	return out, err
}

// ——— token 计量: 小夜侧 (费用估算) ———

type chatCounter struct {
	inner                                convo.Chatter
	calls, sysChars, userChars, outChars int

	// 记忆与重复指标 (修6): 没有这三条, 修完也不知道有没有修好
	memMentions map[string]int // mem_id → 出现在多少次提示词里
	bigrams     map[string]int // 生成文本里的二字片段 (实体集中度)

	// ——— 前缀稳定度 (P1 缓存债): 零成本、确定性的缓存命中代理 ———
	//
	// 取本次 system 与**上一次** system 的逐字节公共前缀。DeepSeek 前缀缓存命中的
	// 正是这一段, 所以 公共前缀/全长 直接对应 API 回报的 prompt_cache_hit_tokens。
	//
	// 为什么必须零成本: 段序/记忆冻结这类改动, 若每次都要真 LLM 跑 30 天才能验证,
	// 迭代一次 ¥0.67 —— 而这个代理一秒出数、同 seed 完全可复现。
	prevPrompt                 string
	prevWasDialog              bool
	prefixSum, promptSum       int // 全部调用
	dlgPrefixSum, dlgPromptSum int // 只统计"对话帧接对话帧" (本批要修的就是它)
	nCalls, nDlgCalls          int

	// §8.1 段序契约的违规计数 (三条写死的契约, 每条都有实测代价)。
	//
	// ⚠️ 此前 convo.PromptStructureProblem **零调用** —— 于是"【输出】协议绝不上移"
	// 只写在注释里, 没有任何东西在**跑的时候**检查它。这里补上: 只查对话帧。
	structBad int
	structWhy string
}

// promptPrefixLen 两个 system 的公共前缀字节数 (缓存命中的就是这段)。
// ⚠️ 2026-10-16: 这份实现原来是 convo.PromptDiff 的**重复** —— 而后者还带着
// 段级差分 (moved/added/removed) 与"段序调整的唯一正当理由就是把这个数变大"的注释。
// ⇒ 直接调它: 同一件事只留一份实现 (两处各写一份, 迟早分叉)。
func promptPrefixLen(a, b string) int {
	return convo.PromptDiff(a, b).CommonPrefix
}

// isDialogPrompt 对话帧的 system 一律以"最高原则"开头 (记忆提取/日程规划不是)。
func isDialogPrompt(system string) bool { return strings.HasPrefix(system, "最高原则") }

var memIDRe = regexp.MustCompile("\\[([a-z0-9_]{4,})\\]")
var hanRe = regexp.MustCompile("[\\p{Han}]{2,}")

func (c *chatCounter) Chat(ctx context.Context, system, user string) (string, error) {
	s, err := c.inner.Chat(ctx, system, user)
	c.calls++
	c.sysChars += len(system)
	c.userChars += len(user)
	c.outChars += len(s)

	// 前缀稳定度: 与上一次调用的公共前缀 / 本次 system 全长
	dialog := isDialogPrompt(system)
	// §8.1: 只查对话帧 —— 别的提示词 (记忆提取/日程规划) 不是这套段序, 查了是噪声。
	if dialog {
		if why := convo.PromptStructureProblem(system); why != "" {
			c.structBad++
			if c.structWhy == "" {
				c.structWhy = why
			}
		}
	}
	if c.prevPrompt != "" {
		p := promptPrefixLen(c.prevPrompt, system)
		c.prefixSum += p
		c.promptSum += len(system)
		c.nCalls++
		if dialog && c.prevWasDialog {
			c.dlgPrefixSum += p
			c.dlgPromptSum += len(system)
			c.nDlgCalls++
		}
	}
	c.prevPrompt, c.prevWasDialog = system, dialog
	if c.memMentions == nil {
		c.memMentions = map[string]int{}
		c.bigrams = map[string]int{}
	}
	// 记忆注入覆盖率: 提示词里出现的 mem_id
	if i := strings.Index(system, "【你记得的关于 ta 的事】"); i >= 0 {
		seg := system[i:]
		if j := strings.Index(seg, "\n【"); j > 0 {
			seg = seg[:j]
		}
		for _, m := range memIDRe.FindAllStringSubmatch(seg, -1) {
			c.memMentions[m[1]]++
		}
	}
	// 实体集中度: 生成文本里的中文二字片段
	for _, run := range hanRe.FindAllString(s, -1) {
		rs := []rune(run)
		for i := 0; i+2 <= len(rs); i++ {
			c.bigrams[string(rs[i:i+2])]++
		}
	}
	return s, err
}

// topShare 返回出现次数最高的前 n 个片段及其占比。
func topShare(m map[string]int, n int) (string, float64) {
	type kv struct {
		k string
		v int
	}
	all := make([]kv, 0, len(m))
	total := 0
	for k, v := range m {
		all = append(all, kv{k, v})
		total += v
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].v != all[j].v {
			return all[i].v > all[j].v
		}
		return all[i].k < all[j].k
	})
	parts := []string{}
	sum := 0
	for i := 0; i < len(all) && i < n; i++ {
		parts = append(parts, fmt.Sprintf("%s%d", all[i].k, all[i].v))
		sum += all[i].v
	}
	pct := 0.0
	if total > 0 {
		pct = 100 * float64(sum) / float64(total)
	}
	return strings.Join(parts, " "), pct
}

const userSystem = `你是"用户"本体: 一个 30 岁左右的嵌入式工程师, 最近在学日语, 性格有点怕麻烦、不太会主动热情, 但内心不讨厌被人惦记。
你在 QQ 好友列表里有个叫"小夜"的朋友(其实是个 AI, 但你现在假装不知道/无所谓, 把她当一个普通线上朋友)。
相处原则: 保持真实反应 — 觉得无聊就敷衍或已读不回; 想起什么就多说两句; 别刻意友善也别刻意冷淡; 可以考考她记不记得你说过的事; 如果她太烦你, 你会冷淡; 如果她让你觉得舒服, 你会更愿意聊。

输出单行 JSON: {"reply":"<你的消息内容, 空串=已读不回/不想聊>","mood":"<随便一个词>"}`

func (u *userAgentSim) decide(ctx context.Context, recent string) string {
	out, err := u.ask(ctx, "最近的对话:\n"+recent+"\n\n现在轮到你(用户)回应。你要不要回?回什么?")
	if err != nil {
		return ""
	}
	raw, err := convoExtractJSON(out)
	if err != nil {
		return ""
	}
	var r struct {
		Reply string `json:"reply"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return ""
	}
	return strings.TrimSpace(r.Reply)
}

// 早上: 用户今天想不想主动找她 (生成一条开场消息或空)
func (u *userAgentSim) morning(ctx context.Context, recent string) string {
	out, err := u.ask(ctx, "早上。你刚拿起手机, 突然想到 QQ 里的小夜。\n最近的对话:\n"+recent+"\n\n想主动找她说点什么吗? (可发可不发; 不发输出空串)")
	if err != nil {
		return ""
	}
	raw, err := convoExtractJSON(out)
	if err != nil {
		return ""
	}
	var r struct {
		Reply string `json:"reply"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return ""
	}
	return strings.TrimSpace(r.Reply)
}

func convoExtractJSON(s string) ([]byte, error) {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return nil, fmt.Errorf("no json")
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
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return []byte(s[start : i+1]), nil
			}
		}
	}
	return nil, fmt.Errorf("unbalanced")
}

// ——— fake 表情包链 ———

type simSearcher struct{}

func (simSearcher) Search(ctx context.Context, keywords []string, limit int) ([]convo.SearchResult, error) {
	out := []convo.SearchResult{}
	for i := 0; i < limit; i++ {
		out = append(out, convo.SearchResult{URL: fmt.Sprintf("https://fake/%d.gif", i), From: "sim"})
	}
	return out, nil
}

type simScorer struct{ rng *rand.Rand }

func (s simScorer) Score(ctx context.Context, imagePath string) (float64, bool, string, error) {
	return 0.55 + s.rng.Float64()*0.4, false, "", nil
}

// ——— 环境数据源 (仿真的季节模型; 正式可换 Open-Meteo / USGS) ———

// simEnv 按"日期 + 种子"确定性生成它那边的环境。
// ⚠️ 不消耗引擎的共享 rng: 用日期派生独立种子, 否则会改变引擎的随机序列(破坏对照)。
func simEnv(day time.Time, city string, seed int64) convo.EnvDay {
	r := rand.New(rand.NewSource(int64(day.Year())*1000 + int64(day.YearDay()) + seed))
	m := day.Month()
	var base int
	var opts []string
	switch m {
	case 12, 1, 2:
		base, opts = 4, []string{"晴", "多云", "阴", "小雪"}
	case 3, 4, 5:
		base, opts = 15, []string{"晴", "多云", "阴", "小雨"}
	case 6, 7, 8:
		base, opts = 29, []string{"晴", "多云", "雷阵雨", "闷热"}
	default:
		base, opts = 19, []string{"晴", "多云", "阴", "小雨"}
	}
	ev := []string{}
	if r.Intn(1000) < 12 { // 全年低概率: 附近地震
		ev = append(ev, fmt.Sprintf("附近地震 震度%d", 1+r.Intn(4)))
	}
	if m >= 6 && m <= 9 && r.Intn(100) < 4 {
		ev = append(ev, "台风预警")
	}
	if (m == 3 || m == 4) && r.Intn(100) < 45 {
		ev = append(ev, "花粉季")
	}
	// ——— §3.6 事件池的**替身** ———
	//
	// ⚠️ 正式系统里这张池子由**夜间批处理用 LLM 生成**(§3.6), 那是一条新增的模型调用路径 (L2),
	// 还没做。仿真这里用确定性模板把**机制**跑通: 池子、时刻、到点触发。
	// 它的作用不是"像真的", 而是让"世界在日内会动"这件事**可测**。
	moments := []convo.EnvMoment{}
	pool := []string{"快递到了", "便利店的饭团卖完了", "楼下在装修", "朋友约周末吃饭", "耳机一边没声了", "煮的汤糊了"}
	for i := 0; i < 2+r.Intn(3); i++ {
		moments = append(moments, convo.EnvMoment{
			TS:   fmt.Sprintf("%02d:%02d", 9+r.Intn(12), r.Intn(60)),
			Text: pool[r.Intn(len(pool))],
		})
	}
	return convo.EnvDay{
		Date:    day.Format("2006-01-02"),
		City:    city,
		Weather: opts[r.Intn(len(opts))],
		TempC:   base + r.Intn(7) - 3,
		Events:  ev,
		Moments: moments,
	}
}

// ——— 主流程 ———

func main() {
	days := simDays
	if v := os.Getenv("SIM_DAYS"); v != "" {
		fmt.Sscanf(v, "%d", &days)
	}
	mode := "dice"
	if v := os.Getenv("SIM_MODE"); v != "" {
		mode = v
	}
	simCity := os.Getenv("SIM_CITY")
	if simCity == "" {
		simCity = "东京"
	}
	simStart := os.Getenv("SIM_START") // YYYY-MM-DD: 换季对照用 (四季取 01/04/07/10)
	reportPath := os.Getenv("SIM_REPORT")
	if reportPath == "" {
		reportPath = "CONVO-SIMULATION.md"
	}
	log.Info("sim", "convo 1 年仿真开始 (seed=%d, %d 天, mode=%s)", seed, days, mode)
	convo.ResetFrameStats() // 帧协议指标从零开始 (报告里那一节依赖它)
	convo.ResetPacingStats()
	convo.ResetBodyStats()
	// §3.6 / §4.4.3 **这两组也只在这里清** —— 它们的注释都写着
	// 「**仿真每次运行开始时调用**」, 而在此之前**没有任何调用者**:
	//
	//	· `ResetEventGenStats`  —— 事件池的丢弃计数与原因分布;
	//	· `ResetNightTierStats` —— 夜间分档的统计。
	//
	// ⚠️ 它们是**进程级全局量** —— 同一进程里跑第二遍仿真时,
	// 第一遍的计数会**累加进第二遍的报告** ✗, 而报告看起来只是"数变大了"。
	// ⚠️ 而 `SIM_START` 那个换季对照正是**同一进程跑多遍**的用法 ✓。
	convo.ResetEventGenStats()
	convo.ResetNightTierStats()

	rng := rand.New(rand.NewSource(seed))
	// 等待虚拟化: 打字延迟(5-25s)与分条间隔(1-10s)不再真睡 —— 仿真粒度是 1 分钟,
	// 秒级节奏既表达不了, 真睡又会让 30 天仿真跑成小时级(实测瓶颈)。
	clock.Sleep = func(ctx context.Context, d time.Duration) error { return nil }
	// 环境(天气/生活事件)数据源: 仿真用季节模型; 正式换 Open-Meteo / USGS 即可, 引擎不改。
	convo.EnvSource = func(day time.Time) (convo.EnvDay, bool) { return simEnv(day, simCity, seed), true }
	// 确定性: 引擎所有调度抽样都接进本仿真的种子 —— 否则同 seed 两次跑结果不同(实测)。
	convo.Dice = rng.Intn
	convo.DiceFloat = rng.Float64
	// SIM_DB: 把库保留在指定目录。
	// 决策回放要能**事后翻** —— 跑在临时目录里、进程一退就删掉, 那 P5 就白做了。
	dir, _ := os.MkdirTemp("", "convo-sim-*")
	if keep := os.Getenv("SIM_DB"); keep != "" {
		dir = keep
		_ = os.MkdirAll(dir, 0755)
	} else {
		defer os.RemoveAll(dir)
	}

	cfg := convo.DefaultConfig()
	cfg.DataDir = dir
	cfg.Stickers.VisionEnabled = true // 仿真: 走 fake scorer (真跑时 LLM 打分)
	store, err := convo.OpenStore(dir, "convo-sim.db")
	if err != nil {
		log.Error("sim", "store: %v", err)
		os.Exit(1)
	}
	defer store.Close()

	// 建档: 角色(内置初音) + 学生作息 + 种子
	answers := &convo.ProfileAnswers{Identity: "student", Template: "bestie", Q1: "high", Q2: "bestie", Q3: "daily"}
	profile := convo.BuildRoleProfile(answers, convo.BuiltinRolePack()[0])
	// §4.2 身体: 周期型要看人格性别 —— 零值空串 = 未指定 = **不产生周期事件**,
	// 所以仿真里必须显式给, 否则"这条通道从没跑过"会被误读成"机制没问题"。
	profile.Gender = convo.GenderFemale
	if profile.CreatedTS == "" {
		profile.CreatedTS = clock.Now().Format("2006-01-02 15:04:05")
	}
	if err := store.SaveProfile(profile); err != nil {
		log.Error("sim", "profile: %v", err)
		os.Exit(1)
	}
	if err := store.SaveSchedule(convo.BuiltinScheduleJSON("student")); err != nil {
		log.Error("sim", "schedule: %v", err)
		os.Exit(1)
	}
	_ = convo.SeedMemories(context.Background(), store, profile)
	_ = store.SeedTopics(3)
	// 3 张初始表情包
	stIDs := []int64{}
	for i := 0; i < 3; i++ {
		// 仿真里的文件是假的 (没有内容) → 指纹传空: 宁可不参与去重, 也不存假指纹。
		id, _ := store.InsertSticker("https://fake/builtin.gif", filepath.Join(dir, fmt.Sprintf("s%d.gif", i)), "ja", "builtin", 0.5+float64(i)*0.15, "")
		stIDs = append(stIDs, id)
	}

	chat := &simChatter{rng: rng, stickers: stIDs}
	sender := &simSender{}
	countedAI := &chatCounter{inner: chat}
	engine := &convo.Engine{
		Store:   store,
		Cfg:     cfg,
		Dialog:  &convo.DialogEngine{Store: store, StickerExists: store.StickerExists},
		Chatter: countedAI,
		Sender:  sender,
		UserID:  1,
		Lang:    "ja",
	}
	var userAgent *userAgentSim
	var uc *ai.Client // arena: 真 LLM 客户端 (报告里读真实 usage)
	if mode == "arena" {
		key := os.Getenv("CONVO_AI_KEY")
		if key == "" {
			key = os.Getenv("VOCABLOCK_AI_KEY") // 主程序既有约定, 复用同一把 key
		}
		if key == "" { // 退化: 读 configs/convo.yaml 的 ai.api_key (key 不打印)
			if c, err := convo.Load("configs/convo.yaml"); err == nil {
				key = c.AI.APIKey
			}
		}
		if key == "" {
			log.Error("sim", "arena 模式需要 VOCABLOCK_AI_KEY / CONVO_AI_KEY(环境变量) 或 configs/convo.yaml 的 ai.api_key")
			os.Exit(1)
		}
		model := os.Getenv("CONVO_SIM_MODEL")
		if model == "" {
			model = "deepseek-flash" // DeepSeek-V4.1-Flash (旧名 deepseek-v4-flash 已下线)
		}
		uc = ai.NewClient(ai.Config{BaseURL: "https://api.deepseek.com", APIKey: key,
			Model: model, Timeout: 90 * time.Second,
			DisableThinking: os.Getenv("SIM_THINKING") != "on", // 默认非思考: temperature 才生效, 且延迟/成本大降
		})
		countedAI.inner = convo.AIAdapter{C: uc} // 小夜侧换真 LLM (计量器保持在外层)
		userAgent = &userAgentSim{c: uc}
		cfg.Stickers.VisionEnabled = false // 表情包打分仍走 fake 打分器
		log.Info("sim", "arena 模式: 双方都是真 LLM (小夜 vs 带抵抗心的模拟用户), model=%s thinking=%v",
			model, os.Getenv("SIM_THINKING") == "on")
	}
	engine.Cycle = &convo.StickerCycle{
		Cfg:      cfg.Stickers,
		Searcher: simSearcher{},
		Scorer:   simScorer{rng: rng},
		Mem:      "sim",
		Download: func(ctx context.Context, hc *http.Client, url, dr, lang string) (string, error) {
			return filepath.Join(dr, fmt.Sprintf("dl_%d.gif", rng.Intn(100000))), nil
		},
	}

	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.Local)
	if simStart != "" { // 换季: 只改起点, 其余逻辑不变
		if t, err := time.ParseInLocation("2006-01-02", simStart, time.Local); err == nil {
			start = t
		}
	}
	userMsgDay := 0.30 // dice 模式: 30% 的天用户主动找她
	replyRate := 0.60  // dice 模式: 主动开场后回应率
	rejectRate := 0.10 // dice 模式: 回应里 10% 是拒绝索取

	// arena 指标
	var arenaReplies, arenaIgnores, arenaInitiated, arenaCalls int
	recentDialog := func() string {
		msgs, _ := store.RecentMessages(14)
		var b strings.Builder
		for i := len(msgs) - 1; i >= 0; i-- {
			m := msgs[i]
			if m.Kind != "text" && m.Kind != "ask" {
				continue
			}
			who := "你"
			if m.Dir == "ai" {
				who = "小夜"
			}
			fmt.Fprintf(&b, "%s: %s\n", who, m.Payload)
		}
		return b.String()
	}

	// 月度统计
	months := map[string]*monthStat{}
	envWeather := map[string]int{}
	envEvents := map[string]int{}

	var lastAIText time.Time
	var lastAITextKind string

	for d := 0; d < days; d++ {
		day := start.AddDate(0, 0, d)
		dayKey := day.Format("2006-01-02")
		envToday := simEnv(day, simCity, seed)
		envWeather[envToday.Weather]++
		for _, e := range envToday.Events {
			envEvents[e]++
		}
		ms := months[day.Format("2006-01")]
		if ms == nil {
			ms = &monthStat{}
			months[day.Format("2006-01")] = ms
		}
		userTalks := rng.Float64() < userMsgDay
		// arena: 早上用户 AI 自主决定要不要主动找她
		var arenaMorning string
		if userAgent != nil {
			ctx2, cancel2 := context.WithTimeout(context.Background(), 90*time.Second)
			arenaMorning = userAgent.morning(ctx2, recentDialog())
			cancel2()
			arenaCalls++
			if arenaMorning != "" {
				arenaInitiated++
			}
		}

		// pre-decide: 回应时机 (若上午有开场), 由 sender 计数驱动在后面处理

		for min := 0; min < 1440; min++ {
			now := day.Add(time.Duration(min) * time.Minute)
			clock.Now = func() time.Time { return now }

			// 用户消息: arena=早上主动(09:10) / dice=20:30 骰子
			if userAgent != nil {
				if arenaMorning != "" && min == 9*60+10 {
					_, _ = store.InsertMessage("user", "sim", "text", arenaMorning)
					_ = engine.OnUserText(context.Background(), arenaMorning)
					ms.userMsgs++
				}
			} else if userTalks && min == 30*60+30 {
				text := "今天有点累, 你呢"
				_, _ = store.InsertMessage("user", "sim", "text", text)
				_ = engine.OnUserText(context.Background(), text)
				ms.userMsgs++
			}
			// 主动消息回应: 5 分钟后轮到用户; arena=用户 LLM 自主决定 (可已读不回)
			if !lastAIText.IsZero() && now.Sub(lastAIText) >= 5*time.Minute && now.Sub(lastAIText) < 30*time.Minute {
				if userAgent != nil {
					ctx2, cancel2 := context.WithTimeout(context.Background(), 90*time.Second)
					text := userAgent.decide(ctx2, recentDialog())
					cancel2()
					arenaCalls++
					if text != "" {
						_, _ = store.InsertMessage("user", "sim", "text", text)
						_ = engine.OnUserText(context.Background(), text)
						ms.userMsgs++
						arenaReplies++
						if convo.DetectRejection(text) {
							ms.rejects++
						}
					} else {
						arenaIgnores++
					}
				} else {
					reply := rng.Float64() < replyRate
					if reply {
						txts := []string{"嗯嗯, 我也是诶", "今天还行吧, 刚下班", "好呀好呀, 那一起", "没空聊这个", "周末去爬山了, 超开心", "不错, 挺好的", "不想说这个, 换一个吧"}
						text := txts[rng.Intn(len(txts))]
						_, _ = store.InsertMessage("user", "sim", "text", text)
						_ = engine.OnUserText(context.Background(), text)
						ms.userMsgs++
						if rng.Float64() < rejectRate && (lastAITextKind == "ask") {
							ms.rejects++
						}
					}
				}
				lastAIText = time.Time{}
			}

			// tick (记录 AI 主动行为)
			beforeTexts := sender.texts
			beforeStickers := sender.stickers
			engine.StepTick()
			if sender.texts > beforeTexts || sender.stickers > beforeStickers {
				// 有 AI 消息发出
				lastAIText = now
				// 判别消息内容种类: 检查 store 最新 ai 消息
				recent, _ := store.RecentMessages(1)
				if len(recent) > 0 {
					lastAITextKind = recent[0].Kind
					if stringsContains(recent[0].Payload, "周末干嘛") {
						lastAITextKind = "ask"
					}
				}
				ms.rounds++
			}
		}
		// 统计日
		if n, _ := store.CounterGet("ai_opens", dayKey); n > 0 {
			ms.opens += n
		}
		if n, _ := store.CounterGet("previews", dayKey); n > 0 {
			ms.previews += n
		}
		if n, _ := store.CounterGet("noinput", dayKey); n > 0 {
			ms.noinput += n
		}
		// 日终批 (睡点简化: 当天最后)
		clock.Now = func() time.Time { return day.Add(23*time.Hour + 35*time.Minute) }
		engine.PrepareDailyJobs()

		// 记忆/表情包月末快照 (每 30 天)
		if d%30 == 29 {
			act, _ := store.TopMemories(1000, clock.Now())
			pend, _ := store.PendingMemories()
			sts, _ := store.ActiveStickers()
			ms.memActive = len(act)
			ms.memPending = len(pend)
			ms.stActive = len(sts)
			for _, s := range sts {
				ms.stAffSum += s.Affinity
			}
			var retired int
			_ = store.DB.QueryRow(`SELECT COUNT(*) FROM stickers WHERE retired_ts IS NOT NULL`).Scan(&retired)
			ms.stRetired = retired
		}
	}

	// 输出报告
	f, _ := os.Create(reportPath)
	defer f.Close()
	span := fmt.Sprintf("%d 天", days)
	if days >= 365 {
		span = "1 年"
	}
	report := fmt.Sprintf("# convo 拟人系统 %s仿真报告\n\n> 生成: `go run ./cmd/convo-sim` · seed=%d · %d 天 · 角色=初音(内置) · 学生作息 · blend=0.15\n> fake LLM/搜索/视觉(确定性) + 用户行为模型(30%% 主动找她/开场回应 60%%)。\n> 打字延迟与分条间隔已虚拟化(no-op): 秒级节奏不参与仿真。\n\n", span, seed, days)

	report += "## 月度汇总\n\n| 月 | 她主动 | 你说话 | 轮次 | 预告 | 记忆(active/pending) | 表情包(active/退役) | 均亲和 |\n|---|---|---|---|---|---|---|---|\n"
	for _, mk := range sortedKeys(months) {
		ms := months[mk]
		avg := 0.0
		if ms.stActive > 0 {
			avg = ms.stAffSum / float64(ms.stActive)
		}
		report += fmt.Sprintf("| %s | %d | %d | %d | %d | %d/%d | %d/%d | %.2f |\n",
			mk, ms.opens, ms.userMsgs, ms.rounds, ms.previews, ms.memActive, ms.memPending, ms.stActive, ms.stRetired, avg)
	}
	totals := map[string]int{}
	for _, mk := range sortedKeys(months) {
		ms := months[mk]
		totals["opens"] += ms.opens
		totals["userMsgs"] += ms.userMsgs
		totals["rounds"] += ms.rounds
		totals["previews"] += ms.previews
		totals["rejects"] += ms.rejects
	}
	report += fmt.Sprintf("\n**%s汇总**: 她主动 %d · 你说话 %d · 轮次 %d · 事件预告 %d · 索取被拒 %d · AI 文本 %d 条 / 表情包 %d 张\n\n",
		span, totals["opens"], totals["userMsgs"], totals["rounds"], totals["previews"], totals["rejects"], sender.texts, sender.stickers)
	if userAgent != nil {
		report += fmt.Sprintf("**arena 对抗指标**: 用户主动找她 %d 天 · 对开场回复 %d 次 · 已读不回/忽略 %d 次 · 用户侧 LLM 调用 %d 次\n",
			arenaInitiated, arenaReplies, arenaIgnores, arenaCalls)
		replyPct, initPct := 0.0, 0.0
		if arenaReplies+arenaIgnores > 0 {
			replyPct = float64(arenaReplies) / float64(arenaReplies+arenaIgnores) * 100
		}
		if days > 0 {
			initPct = float64(arenaInitiated) / float64(days) * 100
		}
		report += fmt.Sprintf("→ 粘性代理信号: 回复占比 %.0f%% (回复/(回复+忽略)) · 用户主动天数占比 %.0f%%\n\n", replyPct, initPct)
	} else {
		report += "**dice 模式**(用户行为为手写概率模型, 仅验证机制)\n\n"
	}

	// 环境 (天气/生活事件): 四季对照用
	report += "## 环境 (天气 / 生活事件)\n\n"
	report += fmt.Sprintf("- 城市 %s · 起点 %s · %d 天\n", simCity, start.Format("2006-01-02"), days)
	report += "- 天气分布: " + mapSummary(envWeather) + "\n"
	if len(envEvents) > 0 {
		report += "- 生活事件: " + mapSummary(envEvents) + "\n"
	}
	report += "\n"

	// token 计量 (费用估算输入): 字符数 → token 按经验比折算, 首次真跑后用 API 返回的 usage 校准
	report += "## token 计量 (费用估算输入)\n\n"
	report += fmt.Sprintf("- 小夜侧: LLM 调用 %d 次 · 输入 %d 字符 (system %d / user %d) · 输出 %d 字符\n",
		countedAI.calls, countedAI.sysChars+countedAI.userChars, countedAI.sysChars, countedAI.userChars, countedAI.outChars)
	if countedAI.calls > 0 {
		report += fmt.Sprintf("- 小夜侧均值: system %.0f · user %.0f · 输出 %.0f 字符/次\n",
			float64(countedAI.sysChars)/float64(countedAI.calls), float64(countedAI.userChars)/float64(countedAI.calls), float64(countedAI.outChars)/float64(countedAI.calls))
	}
	inChars := countedAI.sysChars + countedAI.userChars
	outChars := countedAI.outChars
	if userAgent != nil {
		report += fmt.Sprintf("- 用户侧: LLM 调用 %d 次 · 输入 %d 字符 · 输出 %d 字符\n", userAgent.calls, userAgent.sysChars+userAgent.userChars, userAgent.outChars)
		inChars += userAgent.sysChars + userAgent.userChars
		outChars += userAgent.outChars
	}
	// 费用估算: deepseek-flash (元/百万 token); 空闲时段 = 高峰的一半
	const charsPerToken = 1.6 // 中英混排经验值; 首次真跑后用 API 返回的 usage 校准
	inM := float64(inChars) / charsPerToken / 1e6
	outM := float64(outChars) / charsPerToken / 1e6
	report += fmt.Sprintf("- 折算 token (%.1f 字符/token): 输入 ≈ %.3f M · 输出 ≈ %.3f M\n", charsPerToken, inM, outM)
	report += fmt.Sprintf("- 费用估算 deepseek-flash: **空闲 ≈ ¥%.2f** (输入 ¥1/M + 输出 ¥4/M) · **高峰 ≈ ¥%.2f** (¥2/¥8); 未计前缀缓存 —— system 段命中缓存时输入降至 ¥0.02-0.04/M\n", inM*1+outM*4, inM*2+outM*8)
	report += "\n"

	// 记忆与重复指标 (修1/2/3/5 的验收口径)
	report += "## 记忆与重复指标\n\n"
	if countedAI.memMentions != nil && len(countedAI.memMentions) > 0 {
		worstID, worstN := "", 0
		for k, v := range countedAI.memMentions {
			if v > worstN {
				worstID, worstN = k, v
			}
		}
		report += fmt.Sprintf("- 记忆注入: %d 条不同记忆进过提示词 · 最高 [%s] 出现在 %d/%d 次调用 (%.0f%%) ← 这是「道具依赖」的直接读数\n",
			len(countedAI.memMentions), worstID, worstN, countedAI.calls, 100*float64(worstN)/float64(max(1, countedAI.calls)))
	}
	if countedAI.bigrams != nil {
		top, pct := topShare(countedAI.bigrams, 5)
		report += fmt.Sprintf("- 实体集中度: top5 二字片段 %s = 全部片段的 %.1f%%\n", top, pct)
	}
	if v := atomic.LoadInt64(&convo.RepeatHits); v > 0 || countedAI.calls > 0 {
		report += fmt.Sprintf("- 生成重复命中(仍在提最近反复说过的实体): %d 次 / %d 次调用 = %.0f%%\n",
			v, countedAI.calls, 100*float64(v)/float64(max(1, countedAI.calls)))
	}
	// 归属分布
	rows, _ := store.DB.Query(`SELECT COALESCE(source,'(空)'), COUNT(*) FROM convo_memory GROUP BY 1 ORDER BY 2 DESC`)
	if rows != nil {
		parts := []string{}
		for rows.Next() {
			var k string
			var n int
			if rows.Scan(&k, &n) == nil {
				parts = append(parts, fmt.Sprintf("%s %d", k, n))
			}
		}
		rows.Close()
		if len(parts) > 0 {
			report += "- 记忆归属分布: " + strings.Join(parts, " · ") + "\n"
		}
	}
	report += "\n"

	// ——— 世界驱动闭环 (§3.2 事件总线 + §4.1 心情) ———
	// 这是 P4 的判据: 修前四季行为同质 (0% 由世界驱动), 修后心情应当**由事件推动**、
	// 且能看出半衰期回归与变化率上限的痕迹。
	if ms, err := store.LoadMood(); err == nil {
		var evCount int
		_ = store.DB.QueryRow(`SELECT COUNT(*) FROM convo_world_event`).Scan(&evCount)
		var logCount int
		_ = store.DB.QueryRow(`SELECT COUNT(*) FROM convo_mood_log`).Scan(&logCount)
		var vMin, vMax, vSum float64
		vMin, vMax = 1, -1
		var n int
		_ = store.DB.QueryRow(`SELECT COALESCE(MIN(v_after),0), COALESCE(MAX(v_after),0), COALESCE(SUM(v_after),0), COUNT(*) FROM convo_mood_log`).Scan(&vMin, &vMax, &vSum, &n)
		report += "## 世界驱动闭环 (§3.2 事件总线 · §4.1 心情)\n\n"
		report += fmt.Sprintf("- 世界事件入账 %d 条 → 心情变化 %d 次 (幂等: 同一场雨只推一次)\n", evCount, logCount)
		if rows, err := store.DB.Query(`SELECT substr(ts,12,2) AS hh, COUNT(*) FROM convo_mood_log GROUP BY hh ORDER BY hh`); err == nil {
			var parts []string
			for rows.Next() {
				var hh string
				var n int
				if rows.Scan(&hh, &n) == nil {
					parts = append(parts, fmt.Sprintf("%s时 %d", hh, n))
				}
			}
			rows.Close()
			if len(parts) > 0 {
				report += "- **入账时刻分布**: " + strings.Join(parts, " · ") + "\n"
				report += "  (全部挤在 00 时 = 世界在日内是静止的, 那正是 B6 的病根)\n"
			}
		}
		report += fmt.Sprintf("- 期末心情 valence %.3f / arousal %.2f (%s) · **说话意愿 %.2f** (低于 %.2f 时当日主动 -1)\n",
			ms.Valence, ms.Arousal, ms.Label(), ms.Willingness(clock.Now()), cfg.Sched.MoodLowWillingness)
		if n > 0 {
			report += fmt.Sprintf("- 心情轨迹 (按事件后取值): min %.3f · max %.3f · 均 %.3f · 样本 %d\n", vMin, vMax, vSum/float64(n), n)
		}
		if len(ms.Causes) > 0 {
			report += "- 最近成因:\n"
			for i := len(ms.Causes) - 1; i >= 0 && i >= len(ms.Causes)-4; i-- {
				c := ms.Causes[i]
				report += fmt.Sprintf("  - [%s] %s (%s) 权重 %.2f → Δv %+.3f\n", c.Kind, c.Text, c.TS, c.Weight, c.DeltaV)
			}
		}
		report += "\n"
	}

	// ——— B6 主动开场的**时刻分布** ———
	// 病灶原话: "每天恒为 07:3x + 11:5x 两条" —— 配额从上限变成了节拍器。
	// 判据就是这里的直方图: 修好了应当散开, 而不是堆在两个固定分钟上。
	if rows, err := store.DB.Query(`SELECT substr(ts,12,2) AS hh, COUNT(*) FROM convo_decision_trace WHERE action_kind='active_open' GROUP BY hh ORDER BY hh`); err == nil {
		var parts []string
		total := 0
		for rows.Next() {
			var hh string
			var n int
			if rows.Scan(&hh, &n) == nil {
				parts = append(parts, fmt.Sprintf("%s时 %d", hh, n))
				total += n
			}
		}
		rows.Close()
		report += "## B6 主动开场: 时刻分布\n\n"
		if total == 0 {
			report += "- 本期没有主动开场\n\n"
		} else {
			report += fmt.Sprintf("- 共 %d 次 · 按小时: %s\n", total, strings.Join(parts, " · "))
			report += fmt.Sprintf("- 平均每天 %.2f 次 (配额上限 %d/天)\n", float64(total)/float64(days), cfg.Sched.ActiveQuotaDay)
			// ⚠️ 判据是**这个直方图散不散**, 不是次数多少。
			// 病灶原话是"每天恒为 07:3x + 11:5x 两条" —— 两个峰就是它。
			report += "- ⚠️ 看**峰**: 如果绝大多数落在固定一两个小时里, 那就是配额在当节拍器 (B6 未解)\n\n"
		}
	}

	// ——— §4.2 身体 ———
	if bs := convo.BodyStatsSnapshot(); len(bs.Episodes) > 0 || bs.SickReplies > 0 {
		total := 0
		kinds := []string{}
		for k, v := range bs.Episodes {
			total += v
			kinds = append(kinds, fmt.Sprintf("%s %d", convo.BodyLabel(k), v))
		}
		sort.Strings(kinds) // map 迭代序随机, 报告必须可复现
		report += "## 身体 (§4.2)\n\n"
		report += fmt.Sprintf("- 发作 **%d 次**: %s\n", total, strings.Join(kinds, " · "))
		report += "  (重感冒当天配额 -2 = 一整天不主动开口 —— 设计稿的\"允许整天不出现\"由配额落地, 没有额外的闸门)\n"
		report += fmt.Sprintf("- 病中发消息 **%d 条** · 平时 %d 条\n", bs.SickReplies, bs.WellReplies)
		if bs.SickFactorN > 0 {
			report += fmt.Sprintf("- **病中延迟系数均 %.2f×** (n=%d) ← 系统侧算的, 仿真里**可以**归因\n",
				bs.AvgSickFactor(), bs.SickFactorN)
		}
		if bs.SickReplies > 0 && bs.WellReplies > 0 {
			pct := (bs.AvgSickRunes() - bs.AvgWellRunes()) * 100 / bs.AvgWellRunes()
			report += fmt.Sprintf("- ⚠️ **句长对照: 病中 %.1f 字 vs 平时 %.1f 字 (%.0f%%) — 这个数在仿真里不作数**\n",
				bs.AvgSickRunes(), bs.AvgWellRunes(), pct)
			report += "  `simChatter` 是从**固定句池**里随机取一条, **根本不读提示词** —— 它不可能对注入段有反应。\n"
			report += "  §4.2 要求「长度 ×0.6」到底能不能只靠提示词做到, 只有 arena 模式 (真 LLM) 才答得了。\n"
		}
		report += fmt.Sprintf("- §4.2 硬线(病中索取安慰)应声命中 **%d 次** — 同样受"+"`simChatter`"+"限制, **只记不拦** (拦一条消息属于改硬线, L2, 待拍板)\n\n", bs.Solicits)
	}

	// ——— §8.3/§8.4 表达层 (低唤醒 → 延迟长 / 少表情) ———
	if ps := convo.PacingStatsSnapshot(); ps.DelayCount > 0 {
		report += "## 表达层节奏 (§8.3 情绪污染)\n\n"
		report += fmt.Sprintf("- 打字延迟 %d 次 · 合计 %d 秒 · **均 %.1f 秒** (仿真里 Sleep 是空操作, 记的是\"本该等多久\")\n",
			ps.DelayCount, ps.DelaySeconds, ps.AvgDelaySeconds())
		report += fmt.Sprintf("- 因低意愿丢弃的表情 **%d 张** (少表情 ≠ 不发表情)\n\n", ps.StickersDropped)
	}

	// ——— 帧协议健康度 (§12 核心指标) ———
	// 它是"动输出协议位置"这类改动的**前置判据**: 没有它, 缓存涨了/帧坏了分不清是谁的锅。
	if fs := convo.FrameStatsSnapshot(); fs.Replies > 0 {
		report += "## 帧协议健康度 (§12 核心指标)\n\n"
		report += fmt.Sprintf("- 生成 %d 次 · 一次过 %d (%.0f%%) · 触发重试 %d (重试后过 %d) · 降级 %d · 硬失败 %d\n",
			fs.Replies, fs.FirstPassOK, float64(fs.FirstPassOK)*100/float64(fs.Replies),
			fs.RetryUsed, fs.RetryOK, fs.Degraded, fs.HardFail)
		report += fmt.Sprintf("- **帧失败率 = (降级+硬失败)/生成 = %.0f%%** · 解析不出 JSON 的尝试 %d 次\n",
			fs.FailureRate()*100, fs.NoJSON)
		if len(fs.Violations) > 0 {
			parts := []string{}
			for k, v := range fs.Violations {
				parts = append(parts, fmt.Sprintf("%s %d", k, v))
			}
			sort.Strings(parts)
			report += "- 违规类型: " + strings.Join(parts, " · ") + "\n"
		}
		// 失败**形态**分布: 决定"修复 pass"该怎么设计 (改提示词? 调 max_tokens? 容错解析?)
		if len(fs.FailureKinds) > 0 {
			kinds := []string{}
			for k, v := range fs.FailureKinds {
				kinds = append(kinds, fmt.Sprintf("%s %d", k, v))
			}
			sort.Strings(kinds) // 必须排序: map 迭代序随机, 否则报告不可复现
			report += "- 失败形态: " + strings.Join(kinds, " · ") + "\n"
			report += "  (prose=没按帧格式 · truncated=被截断 · invalid_json=语法不合法 · fence_only=只有围栏 · empty=零输出 · object_ok=解析没问题, 失败在校验)\n"
		}
		if fs.HardRefused > 0 {
			rs := []string{}
			for k, v := range fs.RefuseReasons {
				rs = append(rs, fmt.Sprintf("%s %d", k, v))
			}
			sort.Strings(rs)
			report += fmt.Sprintf("- **A6 硬拒(刻意不说)**: %d 条未发送 · %s ← **不计入帧失败率**\n",
				fs.HardRefused, strings.Join(rs, " · "))
		}
		if len(fs.Repairs) > 0 {
			reps := []string{}
			for k, v := range fs.Repairs {
				reps = append(reps, fmt.Sprintf("%s %d", k, v))
			}
			sort.Strings(reps)
			report += fmt.Sprintf("- **修复 pass (零 LLM 调用)**: 命中 %s · 修好 %d 次 ← 这些本来会变成降级\n",
				strings.Join(reps, " · "), fs.RepairOK)
		}
		if len(fs.Samples) > 0 {
			report += "- 失败原文样本 (截断):\n"
			for _, s := range fs.Samples {
				report += "  - `" + strings.ReplaceAll(s, "\n", " ⏎ ") + "`\n"
			}
		}
		report += "\n"
	}

	// ——— P1 缓存债: 零成本前缀稳定度 (确定性代理) ———
	// 判据看这里, 不看 API —— API 要花钱且只跑一次; 这个代理同 seed 完全可复现, 可逐改逐测。
	report += "## 前缀稳定度 (零成本缓存代理)\n\n"
	report += "> 公共前缀 = 本次 system 与**上一次** system 的逐字节公共前缀 —— DeepSeek 前缀缓存命中的就是它。\n"
	report += "> 因此 公共前缀/全长 是 API 侧 prompt_cache_hit_tokens 的确定性代理 (下节是实测对照)。\n\n"
	if countedAI.nDlgCalls > 0 {
		report += fmt.Sprintf("- **对话帧** %d 次: 平均公共前缀 **%.0f%%** (%d/%d 字节) · 平均 system %.0f 字符 ← 段序与记忆冻结的直接读数\n",
			countedAI.nDlgCalls,
			float64(countedAI.dlgPrefixSum)*100/float64(countedAI.dlgPromptSum),
			countedAI.dlgPrefixSum, countedAI.dlgPromptSum,
			float64(countedAI.dlgPromptSum)/float64(countedAI.nDlgCalls))
	}
	if countedAI.nCalls > 0 {
		report += fmt.Sprintf("- 全部调用 %d 次 (含记忆提取/日程规划等异形提示词, 现实里它们同样不命中): 平均公共前缀 %.0f%% (%d/%d 字节)\n",
			countedAI.nCalls,
			float64(countedAI.prefixSum)*100/float64(countedAI.promptSum),
			countedAI.prefixSum, countedAI.promptSum)
	}
	// §8.1 段序契约: 违规**必须出声** —— 三条契约每一条都有实测代价
	// (【输出】上移过一次: 前缀缓存 +19pt, 而帧失败率 37% → 75%)。
	if countedAI.structBad > 0 {
		report += fmt.Sprintf("- ⚠️ **段序契约违规 %d 次** (首例: %s)\n", countedAI.structBad, countedAI.structWhy)
	} else {
		report += "- 段序契约: 全部通过 (【输出】未上移 / 【时间】置尾 / 稳定段在轮换段之前)\n"
	}
	report += "\n"

	// 真实用量与实付 (来自 API 的 usage, 非字符估算)
	if uc != nil {
		u := uc.Usage()
		miss := u.Prompt - u.Cached
		pctHit := 0.0
		if u.Prompt > 0 {
			pctHit = float64(u.Cached) / float64(u.Prompt) * 100
		}
		off := float64(miss)/1e6*1 + float64(u.Cached)/1e6*0.02 + float64(u.Completion)/1e6*4
		peak := float64(miss)/1e6*2 + float64(u.Cached)/1e6*0.04 + float64(u.Completion)/1e6*8
		report += "## API 实测 (usage 回报)\n\n"
		report += fmt.Sprintf("- 调用 %d 次 · prompt %d tokens (缓存命中 %d, %.0f%%) · completion %d tokens\n",
			u.Calls, u.Prompt, u.Cached, pctHit, u.Completion)
		report += fmt.Sprintf("- **实付估算**: 空闲时段 ¥%.2f · 高峰时段 ¥%.2f\n", off, peak)
		report += "\n"
	}

	report += "## 判定指标映射\n\n"
	report += fmt.Sprintf("- 主动开场回应率 ≈ %.0f%% (用户模型设定 60%%)\n", replyRate*100)
	report += fmt.Sprintf("- 记忆: 直写/求证分流由置信度 ≥0.7 阈值驱动;%d 天内 active 数见上表\n", days)
	report += "- 表情包: 周更自搜自选 ×52 轮, 低亲和退役链运转(档位参数沿用正式配置)\n"

	if _, err := f.WriteString(report); err != nil {
		log.Error("sim", "write report: %v", err)
	}
	log.Info("sim", "完成 — 报告: CONVO-SIMULATION.md")
	fmt.Println(report)
}

// 小工具
func stringsContains(s, sub string) bool { return contains(s, sub) }

// mapSummary 把计数表渲染成 "晴 12 · 多云 8" 形式 (键排序, 结果稳定)
func mapSummary(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %d", k, m[k]))
	}
	return strings.Join(parts, " · ")
}

func sortedKeys(m map[string]*monthStat) []string {
	keys := []string{}
	for k := range m {
		keys = append(keys, k)
	}
	// 简单插入排序 (12 个)
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}
