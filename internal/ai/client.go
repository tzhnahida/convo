package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/tzhnahida/convo/internal/log"
)

type Client struct {
	http            *http.Client
	baseURL         string
	apiKey          string
	model           string
	disableThinking bool

	mu    sync.Mutex
	usage UsageTotals
}

// UsageTotals 累计本次进程内的真实 token 用量 (由 API 的 usage 字段回报)。
type UsageTotals struct {
	Calls      int
	Prompt     int
	Completion int
	Cached     int // prompt_cache_hit_tokens
}

// Usage 返回累计用量 (并发安全)。
func (c *Client) Usage() UsageTotals {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.usage
}

type Config struct {
	BaseURL string `json:"base_url" yaml:"base_url"`
	APIKey  string `json:"api_key" yaml:"api_key"`
	Model   string `json:"model" yaml:"model"`
	Timeout time.Duration
	// DisableThinking 打开非思考模式 ({"thinking":{"type":"disabled"}})。
	// 必要性: 思考模式**默认开启**且 effort=high, 而思考模式下 temperature 直接失效
	// (官方: 设置不报错但不生效) —— 拟人对话的"系统骰子 + 温度"变化全靠 temperature,
	// 不开这个开关等于整套随机性被静默吞掉, 且思维链按输出价计费、延迟秒级。
	DisableThinking bool
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// ⚠️ tool_calls / tool_call_id 是**带工具对话**必需的:
	// 少了它们, 第二轮请求会因为"assistant 说了要调工具但没有这个字段"被 API 拒。
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type chatRequest struct {
	Model       string            `json:"model"`
	Messages    []Message         `json:"messages"`
	Temperature float64           `json:"temperature,omitempty"`
	Thinking    map[string]string `json:"thinking,omitempty"`
	Tools       []ToolSpec        `json:"tools,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content   string     `json:"content"`
			ToolCalls []ToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		PromptCacheHit   int `json:"prompt_cache_hit_tokens"`
	} `json:"usage"`
}

func NewClient(cfg Config) *Client {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
	}
	return &Client{
		http: &http.Client{
			Transport: transport,
			Timeout:   cfg.Timeout,
		},
		baseURL:         cfg.BaseURL,
		apiKey:          cfg.APIKey,
		model:           cfg.Model,
		disableThinking: cfg.DisableThinking,
	}
}

func (c *Client) Chat(ctx context.Context, messages []Message, temperature float64) (string, error) {
	// Debug: log AI input
	var sysPrompt, userPrompt string
	for _, m := range messages {
		switch m.Role {
		case "system":
			sysPrompt = m.Content
		case "user":
			userPrompt = m.Content
		}
	}
	log.Debug("ai", "request model=%s temp=%.2f system_len=%d user_len=%d", c.model, temperature, len(sysPrompt), len(userPrompt))
	if len(sysPrompt) > 0 {
		log.Debug("ai", "system:\n%s", truncateLog(sysPrompt, 2000))
	}
	if len(userPrompt) > 0 {
		log.Debug("ai", "user:\n%s", truncateLog(userPrompt, 500))
	}

	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		reply, err := c.chatOnce(ctx, messages, temperature)
		if err == nil {
			log.Debug("ai", "response len=%d:\n%s", len(reply), truncateLog(reply, 2000))
			return reply, nil
		}
		lastErr = err
		log.Warn("ai", "attempt %d/3 failed: %v", attempt, err)
		if attempt < 3 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
	}
	log.Error("ai", "failed after 3 attempts: %v", lastErr)
	return "", fmt.Errorf("chat failed after 3 attempts: %w", lastErr)
}

func truncateLog(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + fmt.Sprintf("\n... [truncated, total %d chars]", len(s))
}

func (c *Client) chatOnce(ctx context.Context, messages []Message, temperature float64) (string, error) {
	reqBody := chatRequest{
		Model:       c.model,
		Messages:    messages,
		Temperature: temperature,
	}
	if c.disableThinking {
		reqBody.Thinking = map[string]string{"type": "disabled"}
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("http do: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("api error %d: %s", resp.StatusCode, string(respBody))
	}

	var cr chatResponse
	if err := json.Unmarshal(respBody, &cr); err != nil {
		return "", fmt.Errorf("unmarshal response: %w", err)
	}

	if len(cr.Choices) == 0 {
		return "", fmt.Errorf("no choices in response")
	}

	c.mu.Lock()
	c.usage.Calls++
	c.usage.Prompt += cr.Usage.PromptTokens
	c.usage.Completion += cr.Usage.CompletionTokens
	c.usage.Cached += cr.Usage.PromptCacheHit
	c.mu.Unlock()

	return cr.Choices[0].Message.Content, nil
}
