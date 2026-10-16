package convo

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tzhnahida/convo/internal/clock"
	"github.com/tzhnahida/convo/internal/log"
)

// ─────────────────────────────────────────────────────────────
// §2.3 的 L3 层**每天重算**。
//
// ⚠️ 这个文件补的是「**角色从来不说自己的生活**」的最后一块。
//
// 世界观有三层: L1 世界设定 / L2 关系 / L3「这些人现在在忙什么」。
// L3 才是它每天真的会用到的那一层 —— 只有它能让「今天」有内容。
//
// 但 L3 原来**只在生成世界观的那一次**写过一次, 之后永久定格。
// 于是它能说的话只有一句「德克萨斯在送快递」, 说完就没了:
// 第二次问它今天怎么样, 它只能回「就那样」。
//
// **配角是服务世界观的, 不是摆着看的。** 让他们的"现在"每天都动,
// 它才有取之不尽的、属于自己生活的话可说。
//
// 与 §3.6 事件池的分工: 事件池是「世界今天发生了什么」(天气/地震),
// 这里是「我的人今天在干什么」。前者是背景, 后者是关系。
// ─────────────────────────────────────────────────────────────

// CastStatusMaxRunes 单条状态的长度上限。
//
// ⚠️ 它同时是**质量闸门**: 「在忙工作」四个字放得进来, 而它是没用的。
// 上限管不住空洞, 所以真正的约束写在提示词里 (要具体到能接话)。
const CastStatusMaxRunes = 30

// CastStatusStale 这些人的"现在"是不是该重算了。
//
// ⚠️ 判据是**日期串**, 不是"距上次多久" —— 后者在跨天的那一拍会算错,
// 而错的表现是"某天忘了刷新", 静默且无从察觉。
func CastStatusStale(b WorldBible, day string) bool {
	if len(b.Cast) == 0 {
		return false
	}
	for _, m := range b.Cast {
		if strings.TrimSpace(m.StatusDay) != day {
			return true
		}
	}
	return false
}

// castStatusPrompt 「他们今天在忙什么」。
//
// ⚠️ 提示词里的三条要求对应三个真实故障, 不是修辞:
//
//	① **要具体到能接话** —— 「在忙」这种答不出"然后呢";
//	② **要跟今天的世界对得上** —— 外面下暴雨而所有人都"在外面跑", 那是一戳就破;
//	③ **不许把人写没了** —— 返回的人必须和给的人一一对应, 少一个就少一个话题。
func castStatusPrompt(p *PersonaProfile, b WorldBible, envLine string) string {
	var sb strings.Builder
	if p != nil {
		sb.WriteString("你是 " + p.Name + "。\n")
	}
	if s := strings.TrimSpace(b.Self); s != "" {
		sb.WriteString("你在这个世界里的位置: " + s + "\n")
	}
	if e := strings.TrimSpace(envLine); e != "" {
		sb.WriteString("你那边今天: " + e + "\n")
	}
	sb.WriteString("\n你生活里的人:\n")
	for _, m := range b.Cast {
		sb.WriteString("- " + m.Name + " (" + m.Relation.Label() + ")")
		if h := strings.TrimSpace(m.History); h != "" {
			sb.WriteString(": " + h)
		}
		sb.WriteString("\n")
	}
	sb.WriteString("\n写**他们今天各自在干什么**。这是给你自己用的日常素材 —— ")
	sb.WriteString("别人问你今天怎么样, 你要答的就是这些具体的事。\n\n")
	sb.WriteString("严格输出单行 JSON 数组, **一个人一条, 一个都不能少**:\n")
	sb.WriteString("[{\"name\":\"名字\",\"status\":\"今天在干什么\",\"left\":\"\"}]\n\n")
	sb.WriteString("硬性要求:\n")
	sb.WriteString("① 每条 **10-25 字**, 要**具体到能接着说下一句** —— ")
	sb.WriteString("「在忙工作」不合格, 「在三号仓库盘货, 手都冻僵了」合格;\n")
	sb.WriteString("② 要和你那边的天气/时节**对得上** (下暴雨就别让人都在外面跑);\n")
	sb.WriteString("③ 写法是**日常琐事**, 不要大事件、不要战斗、不要戏剧化;\n")
	sb.WriteString("④ 不要写你, 只写他们;\n")
	sb.WriteString("⑤ 不许写成恋人/暧昧关系, 不许涉及未成年, 不许有伤害内容;\n")
	// §3.6.5c 「明确退出」的**信号源**就是这个键 —— 没有它, 退出只剩自然沉降那一条,
	// 而「他搬家了」的角色会一直留在当下生活层里 (一年后她有 40 个还在原地等她的旧人)。
	sb.WriteString("⑥ `left` **默认留空**。只有当这个人的人生**真的翻篇了** (搬家/离职/店关门/关系断了) ")
	sb.WriteString("才填一句理由 —— 填了就等于**他退出你的日常生活**: 从此只能被回忆提起, ")
	sb.WriteString("不能再若无其事地出现, 也**不许反悔**。")
	return sb.String()
}

// CastStatusUpdate 一个人的一条日程更新。
//
// ⚠️ `Left` 只在**他叙述了结局**时非空 (§3.6.5c「明确退出」) —— 空 = 他还在当下生活里。
type CastStatusUpdate struct {
	Status string
	Left   string
}

// clampCastRunes 按**字符**截断 —— 按字节切会把汉字切坏, 而那是要进提示词的。
func clampCastRunes(s string) string {
	if len([]rune(s)) > CastStatusMaxRunes {
		return string([]rune(s)[:CastStatusMaxRunes])
	}
	return s
}

// ParseCastStatus 解析 → name → status。
//
// ⚠️ 键按**名字**而不是下标: 模型偶尔会调换顺序或漏人,
// 按下标合并会把张三的日程安到李四头上 —— 而那是**静默的**, 看不出来。
func ParseCastStatus(raw string) (map[string]CastStatusUpdate, bool) {
	// ⚠️ **必须用数组版抽取器** —— 对象版从第一个 `{` 扫描,
	// 遇到顶层数组只会返回第一个元素, 而那是合法 JSON, 不报错。
	// 后果是"只刷新了一个人", 静静地少一半人 (见 extractJSONArray 的注释)。
	b, err := extractJSONArray(raw)
	if err != nil {
		return nil, false
	}
	var arr []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
		Left   string `json:"left"`
	}
	if err := json.Unmarshal(b, &arr); err != nil {
		// 有的模型会回 {"cast":[...]} —— 认一下, 不认就失败。
		var wrap struct {
			Cast []struct {
				Name   string `json:"name"`
				Status string `json:"status"`
				Left   string `json:"left"`
			} `json:"cast"`
		}
		if err2 := json.Unmarshal(b, &wrap); err2 != nil || len(wrap.Cast) == 0 {
			return nil, false
		}
		arr = wrap.Cast
	}
	if len(arr) == 0 {
		return nil, false
	}
	out := make(map[string]CastStatusUpdate, len(arr))
	for _, it := range arr {
		n := strings.TrimSpace(it.Name)
		s := strings.TrimSpace(it.Status)
		if n == "" || s == "" {
			continue
		}
		s = clampCastRunes(s)
		out[n] = CastStatusUpdate{Status: s, Left: clampCastRunes(strings.TrimSpace(it.Left))}
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// ApplyCastStatus 把新状态并进世界观。**只动命中的那些人。**
//
// ⚠️ 没被命中的**保留旧状态、也保留旧的 StatusDay** —— 于是明天它还会
// 被算作 stale, 再试一次。把没更新的人也标成"今天已更新"的话,
// 一次模型抽风就会让那个人**永久停在旧日程上**, 而且没人会发现。
func ApplyCastStatus(b WorldBible, updates map[string]CastStatusUpdate, day string) WorldBible {
	for i := range b.Cast {
		u, ok := updates[b.Cast[i].Name]
		if !ok {
			continue
		}
		if u.Status != "" {
			b.Cast[i].Status = u.Status
			b.Cast[i].StatusDay = day
		}
		// §3.6.5c **明确退出**: 模型在叙述里给这个人写了结局 (「搬家了」)。
		//
		// ⚠️ 这就是 RetireCastMember 的生产者 —— 没有它, 退出只剩「60 天没被提起」
		// 那一条自然沉降, 而「他搬家了」的角色会一直留在当下生活层里。
		// ⚠️ 理由为空**不退出**: 没有理由的退出与「忘了这个人」长得一模一样。
		if u.Left != "" && RetireCastMember(&b, b.Cast[i].Name, u.Left, clock.Now()) {
			log.Info("convo:cast", "§3.6.5c %s 叙述了结局 (%s) —— 转入「过去的人」", b.Cast[i].Name, u.Left)
		}
	}
	return b
}

// RefreshCastStatus 跑一次刷新。**失败不动库** —— 旧日程比没有日程好。
func RefreshCastStatus(ctx context.Context, c Chatter, p *PersonaProfile, b WorldBible, day, envLine string) (WorldBible, bool) {
	if c == nil || len(b.Cast) == 0 {
		return b, false
	}
	out, err := c.Chat(ctx, castStatusPrompt(p, b, envLine), "开始。")
	if err != nil {
		log.Warn("convo:world", "配角日程生成失败 (保留旧日程): %v", err)
		return b, false
	}
	updates, ok := ParseCastStatus(out)
	if !ok {
		log.Warn("convo:world", "配角日程解析失败 (保留旧日程): %.200s", out)
		return b, false
	}
	return ApplyCastStatus(b, updates, day), true
}

// maybeRefreshCastStatus tick 里的每日一次刷新。
//
// ⚠️ 放在 tick 而不是对话里, 与 §3.6 世界事件同一条理由:
// "没人跟它说话的日子, 它的人也在过日子"。
func (e *Engine) maybeRefreshCastStatus(ctx context.Context, p *PersonaProfile) {
	if e.Store == nil || e.Chatter == nil || p == nil {
		return
	}
	day := clock.Now().Format("2006-01-02")
	b, ok, err := e.Store.LoadWorldBible(e.ownerKey())
	if err != nil || !ok {
		return
	}
	if !CastStatusStale(b, day) {
		return
	}
	envLine := ""
	if env, eok := TodayEnv(); eok {
		envLine = fmt.Sprintf("%s %s %d°C", env.City, env.Weather, env.TempC)
	}
	nb, gen := RefreshCastStatus(ctx, e.Chatter, p, b, day, envLine)
	if !gen {
		return
	}
	if serr := e.Store.SaveWorldBible(e.ownerKey(), nb); serr != nil {
		log.Warn("convo:world", "配角日程落库失败 (下次再试): %v", serr)
		return
	}
	log.Info("convo:world", "配角日程已刷新 (%d 人)", len(nb.Cast))
}
