package ai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// ——— 多模态 (vision) 调用 — 附加式扩展, 主程序原行为不受影响 ———
// OpenAI-compatible content parts: [{type:"text"},{type:"image_url"}]
// 用途: convo 表情包 vision 打分 (deepseek-flash 等, 原生多模态)。

// mimeFromPath 取真实 MIME —— **内容优先, 扩展名只做兜底**。
//
// ⚠️ 原来纯按扩展名: 一张名叫 .jpg 的 GIF 会以 image/jpeg 送出去,
// 而一张名叫 .gif 的 HTML 会以 image/gif 送出去 —— 后者的报错
// 是「请确保是 webp/png/jpeg/gif」, 看起来像 API 不支持, 其实是文件不对。
func mimeFromPath(path string) string { return MimeByContentOrExt(path) }

// ChatVision 单图多模态调用。imagePath 为本地文件 (Base64 内联)。
// 返回模型文本回答。3 次重试与 Chat 同构。
// ChatVisionMulti 一次传**多张图** —— 动图抽帧要走这条。
//
// ⚠️ 与"拼成一张"是两回事: 分开传时**顺序本身就是时间轴**,
// 而拼起来的话模型要靠提示词才知道哪边在前 —— 实测它会直接读成"左右对比梗图"。
//
// prompt 里应当写明这是**按时间顺序**的第 1..N 帧 (与 API 的排列一致)。
func (c *Client) ChatVisionMulti(ctx context.Context, model, system, prompt string, images []string) (string, error) {
	if len(images) == 0 {
		return "", errNoImages
	}
	var parts []map[string]interface{}
	if prompt != "" {
		parts = append(parts, map[string]interface{}{"type": "text", "text": prompt})
	}
	for _, p := range images {
		mime := MimeOfFile(p)
		if mime == "" {
			// ⚠️ 内容不是图片 → 跳过, 而不是发一个坏 data URL 让 API 回 400。
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		parts = append(parts, map[string]interface{}{
			"type": "image_url",
			"image_url": map[string]string{
				"url": "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(b),
			},
		})
	}
	if len(parts) <= 1 {
		return "", errNoImages
	}
	reqBody := map[string]interface{}{
		"model": model,
		"messages": []map[string]interface{}{
			{"role": "system", "content": system},
			{"role": "user", "content": parts},
		},
	}
	return c.chatVisionOnce(ctx, reqBody)
}

var errNoImages = errors.New("没有可用的图片")

func (c *Client) ChatVision(ctx context.Context, model, system, prompt, imagePath string) (string, error) {
	img, err := os.ReadFile(imagePath)
	if err != nil {
		return "", fmt.Errorf("read image: %w", err)
	}
	dataURL := "data:" + mimeFromPath(imagePath) + ";base64," + base64.StdEncoding.EncodeToString(img)

	messages := []interface{}{
		map[string]interface{}{"role": "system", "content": system},
		map[string]interface{}{"role": "user", "content": []interface{}{
			map[string]interface{}{"type": "text", "text": prompt},
			map[string]interface{}{"type": "image_url", "image_url": map[string]string{"url": dataURL}},
		}},
	}
	reqBody := map[string]interface{}{
		"model":       model,
		"messages":    messages,
		"temperature": 0.2,
	}

	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		reply, err := c.chatVisionOnce(ctx, reqBody)
		if err == nil {
			return reply, nil
		}
		lastErr = err
		if attempt < 3 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
	}
	return "", fmt.Errorf("chat vision failed after 3 attempts: %w", lastErr)
}

func (c *Client) chatVisionOnce(ctx context.Context, reqBody map[string]interface{}) (string, error) {
	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("vision api error %d: %s", resp.StatusCode, string(raw))
	}
	var cr chatResponse
	if err := json.Unmarshal(raw, &cr); err != nil {
		return "", err
	}
	if len(cr.Choices) == 0 {
		return "", fmt.Errorf("no choices")
	}
	return cr.Choices[0].Message.Content, nil
}
