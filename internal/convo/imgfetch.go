package convo

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tzhnahida/convo/internal/log"
)

// ─────────────────────────────────────────────────────────────
// 触发桥接方去下载一张图
//
// ⚠️ 桥接方**不会主动下** —— 下载的**唯一入口**是 `get_image(message_id)`。
// 所以 `path_exists == "0"` 不是"这张图坏了", 而是
// **"还没下, 你来叫一声"**。
//
// ⚠️ 这是桥接方 2026-10-14 明确定下来的口径 ——
// 在那之前它的导出函数拿导出目标当源去 Stat, 一次都没成功过,
// 而现象只是"图片段的 path 指向不存在的文件"。
// ─────────────────────────────────────────────────────────────

// ImageFetcher 让桥接方真的去把那张图下下来 (**可选能力**)。
//
// ⚠️ 用可选接口而不是往 Sender 上加方法: 那会波及所有已有实现与测试替身,
// 而它们里没有一个真的会取图。与 GroupSender 同一条思路。
type ImageFetcher interface {
	FetchImage(messageID int32) (string, error)
}

// FetchImage 调桥接方的 get_image, 返回下载到本地的**绝对路径**。
//
// ⚠️ 它是**幂等**的 (同一张图下两次只会得到同一个文件),
// 所以 call 里那两轨重试在这里是安全的 ——
// 而 send_group_msg 那种不幂等的动作, 重试就是重发 (复读机的根源)。
func (c *OneBotClient) FetchImage(messageID int32) (string, error) {
	if messageID == 0 {
		return "", fmt.Errorf("没有 message_id, 无法触发下载")
	}
	// ⚠️ 响应形状**不写死**: 桥接方可能回 data.file / data.path / 裸串。
	// 先把原文收下来, 三种都试一遍 —— 猜错形状的代价是"图永远取不到",
	// 而那会表现成"图片功能整体不工作", 极难定位到是这里。
	var raw json.RawMessage
	if err := c.call("get_image", map[string]interface{}{"message_id": messageID}, &raw); err != nil {
		return "", fmt.Errorf("get_image: %w", err)
	}
	p := extractMediaPath(raw)
	if p == "" {
		return "", fmt.Errorf("get_image 没回路径 (原文 %s)", truncateStr(string(raw), 160))
	}
	log.Info("convo:channel", "get_image(message_id=%d) → %s", messageID, p)
	return p, nil
}

// extractMediaPath 从桥接方的回包里抠出文件路径。
//
// ⚠️ **形状是实测出来的, 不是猜的** (2026-10-14)。get_image 回的是:
//
//	{"data":{"kind":"image","media":[{
//	    "localPath":"/home/tzh/.local/share/qq-nt/QQ/…/nt_data/Pic/2026-10/Ori/….jpg",
//	    "fileName":"0B0CFB231D6AB5AC467978E30E6AC2D1.jpg",
//	    "md5":"0b0cfb231d6ab5ac467978e30e6ac2d1",
//	    "exists":true,"transferStatus":4,"realSize":297053}]}}
//
// ⚠️ 我第一版只认 `file`/`path`/`url` —— 一个都不是它给的名字, 于是
// 每次触发下载都"失败", 而日志里那句「get_image 没回路径」看起来像桥接方的问题。
//
// ⚠️ `localPath` 是 **QQ 客户端的缓存路径** —— 它**会被清理**。
// 所以调用方必须立刻 OwnImage 留一份, 不能拿它当长期存储。
func extractMediaPath(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return ""
	}
	// 裸串 (最简形态)
	if strings.HasPrefix(s, "\"") {
		var str string
		if json.Unmarshal(raw, &str) == nil {
			return strings.TrimSpace(str)
		}
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return ""
	}
	// ① media 数组 —— 实测的真实形状。
	if mv, ok := obj["media"]; ok {
		var arr []map[string]json.RawMessage
		if json.Unmarshal(mv, &arr) == nil {
			for _, m := range arr {
				// ⚠️ exists=false 是"这个元素没有实际文件" —— 跳过它,
				// 否则会把一个不存在的路径当成下载结果。
				if v, ok := m["exists"]; ok {
					var b bool
					if json.Unmarshal(v, &b) == nil && !b {
						continue
					}
				}
				for _, k := range []string{"localPath", "filePath", "path", "fileName", "file"} {
					if p := rawToString(m[k]); p != "" {
						return p
					}
				}
			}
		}
	}
	// ② 平铺的几种 (旧形态 / 别家桥接方)。
	for _, k := range []string{"localPath", "file", "path", "url"} {
		if p := rawToString(obj[k]); p != "" {
			return p
		}
	}
	return ""
}

// rawToString 把一个可能不存在/不是字符串的 JSON 值取成串。
func rawToString(v json.RawMessage) string {
	if len(v) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(v, &s) != nil {
		return ""
	}
	return strings.TrimSpace(s)
}
