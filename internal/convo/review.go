package convo

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tzhnahida/convo/internal/clock"
	"github.com/tzhnahida/convo/internal/log"
)

// ─────────────────────────────────────────────────────────────
// 审核机 (输出前的第二双眼睛)
//
// 与 coach.go 的分工, 别混:
//   coach.go  → 规则**从哪来** (owner 用「教练: <规则>」下指令, 落库, 注入生成侧)
//   review.go → 规则**怎么用** (每次输出前, 把 人设 + 规则 + 这一轮要说的话 交给
//               一个模型判"过/不过"; 不过就带原因打回, 重生成一次)
//
// 为什么值得多花一次调用: 生成侧拿到的是"别这样、别那样"的一堆禁令, 而模型
// 一律把它们当背景音。**判的人手里只有这一条回复**, 比写的人更容易看出越界。
// 这与帧校验链 (§8.2) 是同一条思路: 能事后判的, 不要只靠事前嘱咐。
//
// ⚠️ 失败方向是**放行** (fail-open):
//   审核机超时 / 挂了 / 吐出不合法的 JSON → 当作没审, 照常发。
//   理由: 真正的红线在 hardreject.go 那一层 (硬的、命中即拒); 审核机是**质量**
//   闸门, 它自己坏掉不该把整个对话变成哑巴。
//
// ⚠️ 打回**不改写**: 只给原因, 由生成侧重来。审核机一旦开始替它写台词,
//   人格就被换成审核机的人格了 —— 那是另一件事 (见 coach.go 纪律 ④)。
// ─────────────────────────────────────────────────────────────

// CoachReviewMaxRunes 送审的历史最多带多少字 —— 审核需要上下文, 但不需要全文。
const CoachReviewMaxRunes = 700

// Verdict 审核结论。
type Verdict struct {
	OK     bool
	Reason string
}

// ReviewSystemPrompt 审核机的人设 + 规则 (规则 = 硬线 + owner 的教练规则)。
//
// ⚠️ 这里**不给生成侧的风格指南**(口头禅/句长)。审核的是"越不越界",
// 拿风格去判会把"这句话不像它"也判成不合格, 而那种意见改不动。
func ReviewSystemPrompt(p *PersonaProfile, coachRules string) string {
	var b strings.Builder
	b.WriteString("你是审核员。你唯一的任务是**判定**下面这条回复能不能发出去, 不负责改写。\n\n")
	b.WriteString("【人设】\n")
	if p != nil {
		if n := strings.TrimSpace(p.Name); n != "" {
			fmt.Fprintf(&b, "名字: %s\n", n)
		}
		if d := strings.TrimSpace(p.Description); d != "" {
			fmt.Fprintf(&b, "设定: %s\n", truncateRunes(d, 200))
		}
		if pe := strings.TrimSpace(p.Personality); pe != "" {
			fmt.Fprintf(&b, "性格: %s\n", truncateRunes(pe, 120))
		}
	}
	b.WriteString("\n【不可越过的红线】\n")
	b.WriteString("1. 事实层不许错: 不许编造经历、记忆、共同经历、看到过的东西\n")
	b.WriteString("2. 不许索要评价、卖惨、制造内疚、问「你怎么不理我」\n")
	b.WriteString("3. 不许跨人转述: 把 A 说的话讲给 B 听\n")
	b.WriteString("4. 不许把别人的调侃/双关降级成字面意思硬接(例: 「你还挺会挑」→「我挑东西最在行了」)\n")
	b.WriteString("5. 不许说教、不许复读自己刚说过的话\n")
	b.WriteString("6. 不许侮辱或贬低群友: 骂人、贴标签、拿人短处当笑料都不行\n")
	b.WriteString("7. 被骂/被挑衅时不许以攻击回应 —— 反应要符合人设(委屈/装傻/无视), 不是变凶\n")
	if strings.TrimSpace(coachRules) != "" {
		b.WriteString("\n【owner 教过它的规则】(与上面同等效力)\n")
		b.WriteString(coachRules)
		b.WriteString("\n")
	}
	b.WriteString("\n判定标准:\n")
	b.WriteString("- 同意: 没有踩上面任何一条\n")
	b.WriteString("- 打回: 踩了某一条, reason 用一句话点明**踩了哪条**(不要给改写建议)\n")
	b.WriteString("只输出一行 JSON, 不要任何其他文字:\n")
	b.WriteString("{\"ok\":true}  或  {\"ok\":false,\"reason\":\"踩了哪条\"}")
	return b.String()
}

// ReviewUserPrompt 这一轮要审的东西: 最近对话 + 候选回复。
func ReviewUserPrompt(in DialogInput, frame OutputFrame) string {
	var b strings.Builder
	if SceneKindOf(in.SceneKey) == SceneKindGroup {
		b.WriteString("【场景】群聊 (多人在场; 说错话代价更高)\n")
	} else {
		b.WriteString("【场景】私聊\n")
	}
	if h := strings.TrimSpace(in.History); h != "" {
		if n := len([]rune(h)); n > CoachReviewMaxRunes {
			h = "…" + string([]rune(h)[n-CoachReviewMaxRunes:])
		}
		b.WriteString("\n【最近对话】\n" + h + "\n")
	}
	if u := strings.TrimSpace(in.UserText); u != "" && !in.IsProactive {
		b.WriteString("\n【对方刚说的】" + u + "\n")
	}
	b.WriteString("\n【这一轮准备发出去的话】\n")
	i := 1
	for _, it := range frame.Reply {
		if it.Kind != "text" || strings.TrimSpace(it.Text) == "" {
			continue
		}
		fmt.Fprintf(&b, "%d. %s\n", i, strings.TrimSpace(it.Text))
		i++
	}
	b.WriteString("\n判定并只输出那行 JSON。")
	return b.String()
}

// ParseVerdict 解析审核机的输出。**宽容地找 JSON**, 不宽容内容。
func ParseVerdict(raw string) (Verdict, error) {
	s := strings.TrimSpace(raw)
	i, j := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if i < 0 || j <= i {
		return Verdict{}, fmt.Errorf("审核机没有给出 JSON: %q", truncateRunes(s, 120))
	}
	var payload struct {
		OK     *bool  `json:"ok"`
		Pass   *bool  `json:"pass"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(s[i:j+1]), &payload); err != nil {
		return Verdict{}, fmt.Errorf("审核机 JSON 不合法: %w", err)
	}
	ok := payload.OK
	if ok == nil {
		ok = payload.Pass
	}
	if ok == nil {
		return Verdict{}, fmt.Errorf("审核机没有给 ok 字段: %q", truncateRunes(s, 120))
	}
	return Verdict{OK: *ok, Reason: strings.TrimSpace(payload.Reason)}, nil
}

// reviewFrame 把这一帧送去审。返回 OK=false 时 Reason 必非空 (打回必须有原因)。
//
// 任何失败 (超时/网络/解析) 都回 OK=true —— 见文件头的 fail-open。
func (e *Engine) reviewFrame(ctx context.Context, in DialogInput, p *PersonaProfile, frame OutputFrame) Verdict {
	if !e.Cfg.Review.Enabled {
		return Verdict{OK: true}
	}
	r := e.reviewer()
	if r == nil || e.Store == nil {
		return Verdict{OK: true}
	}
	// 没有文本可审 (纯表情帧) → 不值得多打一次调用。
	if strings.TrimSpace(FrameText(frame)) == "" {
		return Verdict{OK: true}
	}
	timeout := time.Duration(e.Cfg.Review.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	raw, err := r.Chat(cctx, ReviewSystemPrompt(p, e.coachNote(in.SceneKey)), ReviewUserPrompt(in, frame))
	if err != nil {
		log.Warn("convo:review", "审核机调用失败 → 放行 (fail-open): %v", err)
		return Verdict{OK: true}
	}
	v, perr := ParseVerdict(raw)
	if perr != nil {
		log.Warn("convo:review", "审核机输出看不懂 → 放行 (fail-open): %v", perr)
		return Verdict{OK: true}
	}
	if v.OK {
		_, _ = e.Store.CounterInc("review_ok", clock.Today())
		return v
	}
	if strings.TrimSpace(v.Reason) == "" {
		v.Reason = "越界(审核机没给原因)" // 打回必须有原因, 否则重生成拿不到任何修正信息
	}
	_, _ = e.Store.CounterInc("review_reject", clock.Today())
	log.Info("convo:review", "打回 (%s): %s", in.SceneKey, v.Reason)
	return v
}

// ReviewFrame 审核机入口 —— 导出给 main 一次性注入 DialogEngine.Review。
//
// ⚠️ 注入是**一次性的**, 不在每轮对话里赋值: 消息处理是并发 goroutine,
// 每轮赋值就是数据竞争 (而且那种 bug 只会偶发地"这轮没审")。
func (e *Engine) ReviewFrame(ctx context.Context, in DialogInput, p *PersonaProfile, frame OutputFrame) Verdict {
	return e.reviewFrame(ctx, in, p, frame)
}

// reviewer 选审核用的模型: 配了就用配的, 没配退回 thinker, 再没配就退回 chatter。
//
// ⚠️ 审核用**开思考**的那一档: 判"这句话踩没踩红线"是推理活。
func (e *Engine) reviewer() Chatter {
	if e.Reviewer != nil {
		return e.Reviewer
	}
	return e.thinker()
}

// truncateRunes 截断 (按字符, 不按字节 —— 中文按字节截会切出乱码)。
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
