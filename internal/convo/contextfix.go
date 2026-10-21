package convo

import (
	"strings"
	"time"

	"github.com/tzhnahida/convo/internal/clock"
)

// ─────────────────────────────────────────────────────────────
// 上下文修复 (2026-10-21)
//
// 现场: 模型收到的【最近对话】12 行里有 9 行是它自己说的 ——
// 用户真正的问题被自己的回复挤出了窗口, 于是它开始自言自语。
// 用户的原话: "很多都胡言乱语, 根本没有上下文, 跟傻子一样"。
//
// 两条修法:
//   ① 连续的"我:"在注入时**合并成一行** (内容不丢, 只是不再占 9 个窗口位);
//   ② 同一句用户原话 2 分钟内被重复发送 → 只答一次
//      (实测同一条数列题被 @ 了 4 次, 它答了 4 遍, 每遍都不一样)。
// ─────────────────────────────────────────────────────────────

// OwnBurstLimit 合并后一条"我"行最多保留几段。
const OwnBurstLimit = 4

// collapseOwnBursts 把连续的自己发言合并成一行。
//
// 输入是渲染好的行: "1390 我: 文本" (群) / "12 她: 文本" (私聊)。
// 合并后: "1390 我: 文本A / 文本B / 文本C"。
func collapseOwnBursts(lines []string) []string {
	if len(lines) == 0 {
		return lines
	}
	out := make([]string, 0, len(lines))
	merged := 0
	for _, ln := range lines {
		isAI := isOwnLine(ln)
		if isAI && merged > 0 && len(out) > 0 && isOwnLine(out[len(out)-1]) {
			if merged >= OwnBurstLimit {
				continue
			}
			body := ln
			if i := strings.Index(body, ": "); i >= 0 {
				body = body[i+2:]
			}
			out[len(out)-1] += " / " + body
			merged++
			continue
		}
		out = append(out, ln)
		if isAI {
			merged = 1
		} else {
			merged = 0
		}
	}
	return out
}

// isOwnLine 这一行是不是它自己说的 (群里的"我" / 私聊的"她")。
func isOwnLine(ln string) bool {
	return strings.Contains(ln, " 我: ") || strings.Contains(ln, " 她: ")
}

// SameUserTextSince 同一句用户原话在窗口内出现了几次 (含刚刚入库的这条)。
func (s *Store) SameUserTextSince(sceneKey, text string, since time.Time) int {
	if s == nil || s.DB == nil {
		return 0
	}
	var n int
	_ = s.DB.QueryRow("SELECT count(*) FROM convo_messages WHERE scene_key=? AND dir='user' AND payload_json=? AND ts >= ?",
		sceneKey, text, since.Format(bodyTSLayout)).Scan(&n)
	return n
}

// RepeatedQuestionWindow 同一句话重复发送的判定窗口 (2 分钟)。
const RepeatedQuestionWindow = 2 * time.Minute

// isRepeatedQuestion 同一条问题刚刚已经问过 (它的答案大概率已经发过了)。
func (e *Engine) isRepeatedQuestion(sc SceneMsg) bool {
	if e.Store == nil {
		return false
	}
	text := strings.TrimSpace(sc.Text)
	// 太短的话重复是聊天常态 ("哈哈"), 不是同一道题; 长句重复才是真重复。
	if len([]rune(text)) < 6 {
		return false
	}
	return e.Store.SameUserTextSince(sc.SceneKey, text, clock.Now().Add(-RepeatedQuestionWindow)) >= 2
}
