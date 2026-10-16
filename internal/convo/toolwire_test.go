package convo

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tzhnahida/convo/internal/ai"
)

// fakeToolChatter 按脚本依次返回; 记录它收到了什么。
type fakeToolChatter struct {
	replies  []ai.ToolReply
	errs     []error
	calls    int
	lastMsgs []ai.Message
	lastTool int // 最后一轮带的工具数
}

func (f *fakeToolChatter) Chat(ctx context.Context, system, user string) (string, error) {
	return "", errors.New("不该走纯文本")
}

func (f *fakeToolChatter) ChatWithTools(ctx context.Context, messages []ai.Message, temperature float64, tools []ai.ToolSpec) (ai.ToolReply, error) {
	f.lastMsgs = messages
	f.lastTool = len(tools)
	i := f.calls
	f.calls++
	if i < len(f.errs) && f.errs[i] != nil {
		return ai.ToolReply{}, f.errs[i]
	}
	if i < len(f.replies) {
		return f.replies[i], nil
	}
	return ai.ToolReply{Content: "兜底"}, nil
}

func toolCall(id, query string) ai.ToolCall {
	var tc ai.ToolCall
	tc.ID = id
	tc.Type = "function"
	tc.Function.Name = "web_search"
	tc.Function.Arguments = `{"query":"` + query + `"}`
	return tc
}

// ⚠️ **本批的核心主张**: 模型可以**自己决定**去查, 不用等关键词命中。
func TestToolChat_ModelDecidesToSearch(t *testing.T) {
	ch := &fakeToolChatter{replies: []ai.ToolReply{
		{ToolCalls: []ai.ToolCall{toolCall("c1", "尊界v800")}},
		{Content: "{\"reply\":[{\"kind\":\"text\",\"text\":\"我查到了\"}]}"},
	}}
	e, _ := groupSpeakEngine(t, ch)
	e.LearnSearch = &fakeLearnSearch{hits: []WebHit{{Title: "尊界v800 刹车事件", URL: "https://n/1", Desc: "详情"}}}
	d := &DialogEngine{Tools: e}
	out, ok := d.toolChat(context.Background(), "sys", "user", 0.8)
	if !ok {
		t.Fatal("工具路径没走通")
	}
	if !strings.Contains(out, "我查到了") {
		t.Fatalf("没拿到最终回复: %q", out)
	}
	if ch.calls != 2 {
		t.Fatalf("应当是 查→答 两轮, got %d", ch.calls)
	}
	// ⚠️ 第二轮的消息里必须带 tool 角色与 tool_call_id —— 少了 API 会拒。
	found := false
	for _, m := range ch.lastMsgs {
		if m.Role == "tool" && m.ToolCallID == "c1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("没有把工具结果按 id 回灌: %+v", ch.lastMsgs)
	}
}

// ⚠️ **轮数有上限** —— 没有上限它会在"搜→看→再搜"里无限转, 每次都真花钱。
func TestToolChat_RoundCapForcesAnswer(t *testing.T) {
	ch := &fakeToolChatter{replies: []ai.ToolReply{
		{ToolCalls: []ai.ToolCall{toolCall("c1", "a")}},
		{ToolCalls: []ai.ToolCall{toolCall("c2", "b")}},
		// 最后一轮 (不给工具了) 它**必须给话**。
		{Content: "最后一轮必须说话"},
	}}
	e, _ := groupSpeakEngine(t, ch)
	e.LearnSearch = &fakeLearnSearch{hits: []WebHit{{URL: "https://n/x"}}}
	d := &DialogEngine{Tools: e}
	out, ok := d.toolChat(context.Background(), "sys", "user", 0.8)
	if !ok {
		t.Fatal("没走通")
	}
	if out == "" {
		t.Fatal("轮数用尽时必须逼它说话, 不能返回空")
	}
	// ⚠️ **循环必须终止**: 最后一轮不给工具, 它再要也不转。
	if ch.calls != MaxToolRounds+1 {
		t.Fatalf("轮数应当恰好是 %d (最后一轮逼它说话), got %d", MaxToolRounds+1, ch.calls)
	}
	// ⚠️ 最后一轮**不带工具** —— 逼它说话而不是继续要工具。
	if ch.lastTool != 0 {
		t.Fatalf("最后一轮还给了工具, 它不会说话: %d 个", ch.lastTool)
	}
}

// ⚠️ **循环必须保证终止, 不管模型返回什么。**
//
// 不给工具了却**仍然**返回 tool_calls 是可能的 (模型没跟上, 或端上实现有偏差)。
// 那时如果还继续转, 就是无限循环 —— 每次都真花钱、真花时间, 而对方在等回话。
func TestToolChat_LoopTerminatesEvenIfModelKeepsAsking(t *testing.T) {
	ch := &fakeToolChatter{replies: []ai.ToolReply{
		{ToolCalls: []ai.ToolCall{toolCall("c1", "a")}},
		{ToolCalls: []ai.ToolCall{toolCall("c2", "b")}},
		{ToolCalls: []ai.ToolCall{toolCall("c3", "c")}}, // 不给工具了它还要
		{ToolCalls: []ai.ToolCall{toolCall("c4", "d")}},
	}}
	e, _ := groupSpeakEngine(t, ch)
	e.LearnSearch = &fakeLearnSearch{hits: []WebHit{{URL: "https://n/x"}}}
	d := &DialogEngine{Tools: e}
	if _, ok := d.toolChat(context.Background(), "s", "u", 0.8); ok {
		t.Fatal("最后一轮它没给话, 不该声称走通 —— 调用方会拿空串去解析")
	}
	if ch.calls != MaxToolRounds+1 {
		t.Fatalf("必须在 %d 轮后停下, got %d", MaxToolRounds+1, ch.calls)
	}
}

// ⚠️ 工具失败要**如实告诉它** —— 否则它会把"没查到"编成一个答案。
func TestRunTool_TellsTheModelWhenItFails(t *testing.T) {
	e, _ := groupSpeakEngine(t, &fakeChatter{outputs: []string{"{}"}})
	e.LearnSearch = &fakeLearnSearch{err: errors.New("网络挂了")}
	got := e.RunTool(context.Background(), toolCall("c1", "某件事"))
	if !strings.Contains(got, "别编") {
		t.Fatalf("搜索失败时没告诉它别编: %q", got)
	}
	e.LearnSearch = &fakeLearnSearch{}
	got = e.RunTool(context.Background(), toolCall("c1", "某件事"))
	if !strings.Contains(got, "别编") {
		t.Fatalf("搜不到时没告诉它别编: %q", got)
	}
}

func TestRunTool_BadArgs(t *testing.T) {
	e, _ := groupSpeakEngine(t, &fakeChatter{outputs: []string{"{}"}})
	var bad ai.ToolCall
	bad.Function.Name = "web_search"
	bad.Function.Arguments = "not json"
	if got := e.RunTool(context.Background(), bad); !strings.Contains(got, "解析失败") {
		t.Fatalf("坏参数没被拦住: %q", got)
	}
	var unknown ai.ToolCall
	unknown.Function.Name = "rm_rf"
	if got := e.RunTool(context.Background(), unknown); !strings.Contains(got, "没有这个工具") {
		t.Fatalf("未知工具没被拦住: %q", got)
	}
}

// ⚠️ 不支持工具的 Chatter 要能**干净地退回纯文本**, 而不是报错。
func TestToolChat_NoSupportFallsBack(t *testing.T) {
	d := &DialogEngine{} // Tools 为 nil
	if _, ok := d.toolChat(context.Background(), "s", "u", 0.8); ok {
		t.Fatal("没有工具口时不该声称走通了")
	}
}

func TestToolChat_APIFailureFallsBack(t *testing.T) {
	ch := &fakeToolChatter{errs: []error{errors.New("api 500")}}
	e, _ := groupSpeakEngine(t, ch)
	d := &DialogEngine{Tools: e}
	if _, ok := d.toolChat(context.Background(), "s", "u", 0.8); ok {
		t.Fatal("API 挂了却声称走通了 —— 调用方会拿着空串去解析")
	}
}

// ⚠️ 工具描述里必须写清**什么时候别用** —— 只写"能搜"它会拿它查"今天天气"。
func TestWebSearchTools_HasNegativeGuidance(t *testing.T) {
	ts := WebSearchTools()
	if len(ts) != 1 || ts[0].Function.Name != "web_search" {
		t.Fatalf("工具定义不对: %+v", ts)
	}
	desc := ts[0].Function.Description
	if !strings.Contains(desc, "别用它查你已经确定的事") {
		t.Fatalf("描述里缺「什么时候别用」: %q", desc)
	}
}
