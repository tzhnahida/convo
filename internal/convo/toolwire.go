package convo

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/tzhnahida/VocabLock/internal/ai"
	"github.com/tzhnahida/VocabLock/internal/log"
)

// ─────────────────────────────────────────────────────────────
// 搜索变成**它自己的工具**
//
// ⚠️ 与「关键词命中就提前注入搜索结果」是两回事:
//   · 注入: 由**关键词表**决定查不查 —— 表窄了查不到, 表宽了每句都查;
//   · 工具: 由**模型**决定查不查 —— 它自己知道缺什么。
//
// 实测 (Day-1): 用户连说「知道吗」「你说说细节」「**你去查查**」, 它全靠人设躲开。
// 关键词表再宽也追不上人说话的花样 —— **"要不要查"本来就是语义判断**。
//
// ⚠️ 三条纪律:
//   ① **轮数有上限** —— 没有上限它会在"搜→看→再搜"里无限转, 每次都真花钱;
//   ② **最后一轮不带工具** —— 逼它说话, 而不是继续要工具;
//   ③ **工具失败要如实告诉它** —— 否则它会把"没查到"编成一个答案。
// ─────────────────────────────────────────────────────────────

// MaxToolRounds 最多转几轮。
//
// ⚠️ 2 轮足够: 第一轮决定查, 第二轮看到结果说话。再多就是在反复查同一件事。
const MaxToolRounds = 2

// ToolRunner 对话引擎调工具的接口。
//
// ⚠️ 抽成接口而不是直接用 Engine: DialogEngine 不该依赖整个 Engine,
// 而且**测试替身要能不实现它** (纯文本场景下 Tools 为 nil, 一切照旧)。
type ToolRunner interface {
	ChatWithTools(ctx context.Context, messages []ai.Message, temperature float64, tools []ai.ToolSpec) (ai.ToolReply, error)
	RunTool(ctx context.Context, call ai.ToolCall) string
}

// WebSearchTools 它会的工具。
//
// ⚠️ 描述里必须写清**什么时候别用** —— 只写"能搜"的话, 它会拿它查"今天天气"
// 这种一句话就能答的事, 每次多花两秒和一次调用。
func WebSearchTools() []ai.ToolSpec {
	return []ai.ToolSpec{{
		Type: "function",
		Function: ai.ToolFuncSpec{
			Name: "web_search",
			Description: "上网查一件你**不知道**或**有时效性**的事 (新闻/热点/事件/价格/某样东西是什么)。" +
				"想知道就去查, 不用等对方让你查。" +
				"**别用它查你已经确定的事**, 也别用它闲聊 —— 每次查都要花时间, 对方在等你回话。",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query": map[string]interface{}{
						"type":        "string",
						"description": "搜索关键词, 4-20 字, 名词性问题 (不要把整句话塞进去)",
					},
				},
				"required": []string{"query"},
			},
		},
	}}
}

// ChatWithTools 转发给底层客户端。Engine 实现 ToolRunner。
func (e *Engine) ChatWithTools(ctx context.Context, messages []ai.Message, temperature float64, tools []ai.ToolSpec) (ai.ToolReply, error) {
	tc, ok := e.Chatter.(ToolChatter)
	if !ok {
		return ai.ToolReply{}, errNoToolSupport
	}
	return tc.ChatWithTools(ctx, messages, temperature, tools)
}

// ToolChatter 能调工具的对话口。
type ToolChatter interface {
	ChatWithTools(ctx context.Context, messages []ai.Message, temperature float64, tools []ai.ToolSpec) (ai.ToolReply, error)
}

var errNoToolSupport = errStr("这个 Chatter 不支持工具")

type errStr string

func (e errStr) Error() string { return string(e) }

// RunTool 执行一次工具调用。**返回的是给模型看的文本**, 不是给用户的。
//
// ⚠️ 失败时返回的是**人话原因**, 不是 error: 模型要能读到"搜索挂了",
// 才会老实说"我这边查不到"。回一个空串的话它会当成"搜到了但没有内容"。
func (e *Engine) RunTool(ctx context.Context, call ai.ToolCall) string {
	name := strings.TrimSpace(call.Function.Name)
	if name != "web_search" {
		return "没有这个工具: " + name
	}
	var args struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
		return "参数解析失败, 换个写法再试。"
	}
	q := strings.TrimSpace(args.Query)
	if q == "" {
		return "搜索词是空的 —— 你得先想清楚要查什么。"
	}
	hits, err := e.learnSearcher().SearchWeb(ctx, q, 5)
	if err != nil {
		log.Warn("convo:tool", "web_search(%q) 失败: %v", q, err)
		return "搜索出错了 (" + err.Error() + ")。**别编**, 就说你这边查不到。"
	}
	if len(hits) == 0 {
		return "「" + q + "」什么都没搜到。**别编** —— 要么换个词再查一次, 要么问清楚对方指的是什么。"
	}
	log.Info("convo:tool", "web_search(%q) → %d 条", q, len(hits))
	return "搜到这些:\n" + FormatHits(hits, 5) + "\n⚠️ 只许用上面的内容回答, 材料外的别说。"
}

// toolChat 带工具的一轮对话。第二个返回值 = **这次走没走工具路径**。
//
// ⚠️ 没走 (Chatter 不支持工具) 时要能**干净地退回纯文本**, 而不是报错 ——
// 测试替身和 sim 都是纯文本的, 让它们全挂掉没有意义。
func (d *DialogEngine) toolChat(ctx context.Context, system, user string, temp float64) (string, bool) {
	if d.Tools == nil {
		return "", false
	}
	msgs := []ai.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: user},
	}
	for round := 0; round <= MaxToolRounds; round++ {
		// ⚠️ 最后一轮**不带工具** —— 逼它说话。
		last := round == MaxToolRounds
		var tools []ai.ToolSpec
		if !last {
			tools = WebSearchTools()
		}
		rep, err := d.Tools.ChatWithTools(ctx, msgs, temp, tools)
		if err != nil {
			log.Warn("convo:tool", "带工具对话失败, 本轮退回纯文本: %v", err)
			return "", false
		}
		// ⚠️ **循环必须保证终止, 不管模型返回什么。**
		//
		// 不给工具了却**仍然**返回 tool_calls 是可能的 (模型没跟上, 或者端上实现有偏差)。
		// 那时如果还继续转, 就是无限循环 —— 每次都真花钱、真花时间, 而对方在等回话。
		if last || len(rep.ToolCalls) == 0 {
			if strings.TrimSpace(rep.Content) == "" {
				// 最后一轮它没给出话 → 把这一轮当失败, 让调用方退回纯文本再问一次。
				return "", false
			}
			return rep.Content, true
		}
		msgs = append(msgs, ai.Message{Role: "assistant", Content: rep.Content, ToolCalls: rep.ToolCalls})
		for _, call := range rep.ToolCalls {
			msgs = append(msgs, ai.Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    d.Tools.RunTool(ctx, call),
			})
		}
	}

	// 到不了这里 (最后一轮必 return), 但 Go 要一个兜底。
	return "", false
}
