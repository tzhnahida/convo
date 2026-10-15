package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// ─────────────────────────────────────────────────────────────
// 工具调用 (function calling)
//
// ⚠️ 与「把搜索结果提前注入提示词」是**两回事**:
//   · 注入: 由**关键词**决定查不查 —— 判错了就查不到, 或者每句都查;
//   · 工具: 由**模型**决定查不查 —— 它知道自己缺什么。
//
// 实测 (2026-10-14 Day-1): 用户连说「知道吗」「你说说细节」「**你去查查**」,
// 它全靠人设躲开、零检索。关键词表再宽也追不上人说话的花样;
// 而"要不要查"本来就是**语义判断**, 该交给模型。
// ─────────────────────────────────────────────────────────────

// ToolSpec 一个可调用的工具 (OpenAI 兼容格式)。
type ToolSpec struct {
	Type     string       `json:"type"` // 恒为 "function"
	Function ToolFuncSpec `json:"function"`
}

// ToolFuncSpec 工具的函数签名。
type ToolFuncSpec struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

// ToolCall 模型要求调用一个工具。
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"` // JSON 字符串, **不是对象**
	} `json:"function"`
}

// ToolReply 一次带工具的返回: 要么给了话, 要么要调工具。
type ToolReply struct {
	Content   string
	ToolCalls []ToolCall
}

// ChatWithTools 带工具发一次。**不循环** —— 循环是调用方的事,
// 因为"最多转几轮"是产品决策, 不是协议细节。
func (c *Client) ChatWithTools(ctx context.Context, messages []Message, temperature float64, tools []ToolSpec) (ToolReply, error) {
	reqBody := chatRequest{
		Model:       c.model,
		Messages:    messages,
		Temperature: temperature,
		Tools:       tools,
	}
	if c.disableThinking {
		reqBody.Thinking = map[string]string{"type": "disabled"}
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return ToolReply{}, fmt.Errorf("marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return ToolReply{}, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return ToolReply{}, fmt.Errorf("http do: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return ToolReply{}, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return ToolReply{}, fmt.Errorf("api error %d: %s", resp.StatusCode, string(respBody))
	}
	var cr chatResponse
	if err := json.Unmarshal(respBody, &cr); err != nil {
		return ToolReply{}, fmt.Errorf("unmarshal response: %w", err)
	}
	if len(cr.Choices) == 0 {
		return ToolReply{}, fmt.Errorf("no choices in response")
	}
	c.mu.Lock()
	c.usage.Calls++
	c.usage.Prompt += cr.Usage.PromptTokens
	c.usage.Completion += cr.Usage.CompletionTokens
	c.usage.Cached += cr.Usage.PromptCacheHit
	c.mu.Unlock()
	return ToolReply{Content: cr.Choices[0].Message.Content, ToolCalls: cr.Choices[0].Message.ToolCalls}, nil
}
