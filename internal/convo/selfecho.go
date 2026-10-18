package convo

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/tzhnahida/convo/internal/log"
)

// ─────────────────────────────────────────────────────────────
// 整句复读闸 (§3.6.8 的缺口)
//
// 现有的两道闸都治不了它:
//   - repeat.go 统计**片段**(2-4 字)出现 ≥3 次 —— 治的是"冷场就把同一样东西
//     捡起来再抛一次"; 整句照抄自己时, 每个片段的计数才 1-2, 它一声不响。
//   - 提示词里的"别重复"是软约束。它看得见自己刚才说了什么, 但"照抄"的诱惑
//     比"改写成新的一句"小得多。
//
// 实测 (2026-10-18): 08:30:00 与 08:34:25 两句一模一样的「我脑子也还没转过来」。
// 用户看到的就是"它卡带了"。
//
// 判据刻意**窄** —— 不做相似度, 只做两种:
//   ① 归一化后**整句相等**;
//   ② 把某条**较长的**最近发言**整段包含** (实测那句其实是"原句 + 补一句":
//      "我脑子也还没转过来 咱俩半斤八两" —— 只比相等会漏掉真正被看见的那一次)。
// 短回应("嗯"/"好"/"真的假的")不参与判断: 它们重复是正常的, 而且是群聊主力。
// ─────────────────────────────────────────────────────────────

const (
	// SelfEchoLookback 回看多少条它自己的发言。
	SelfEchoLookback = 24
	// SelfEchoMinRunes 参与判断的最短长度 (归一化后)。
	SelfEchoMinRunes = 4
	// SelfEchoContainRunes 被"整段包含"时, 那条旧发言至少要多长 —— 太短的
	// ("我知道了")一旦被包含进来, 正常的句子会被误杀。
	SelfEchoContainRunes = 8
)

// EchoKey 归一化一行用于整句比较: 只留汉字/字母/数字, 其余一律丢
// (标点、空白、表情、CQ 码都不该影响"是不是同一句")。
// ⚠️ 方括号段 ([QQ表情38] / [CQ:...] / 图片占位) **整段先剥掉** ——
// 里面的汉字不是"它说的话", 留着会让同一句话因为带了个表情就不相等。
var echoBracket = regexp.MustCompile(`\[[^\]]*\]`)

func EchoKey(s string) string {
	s = echoBracket.ReplaceAllString(s, "")
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return strings.ToLower(b.String())
}

// isSelfEcho 这条 (已归一化的) 话是不是在复读 recent 里的某一条。
func isSelfEcho(key string, recent map[string]bool) bool {
	if len([]rune(key)) < SelfEchoMinRunes {
		return false
	}
	if recent[key] {
		return true
	}
	for rk := range recent {
		if len([]rune(rk)) >= SelfEchoContainRunes && strings.Contains(key, rk) {
			return true
		}
	}
	return false
}

// selfEchoKeysOf 取最近说过的话 (归一化后的集合)。
//
// ⚠️ **跨场景**: 复读在哪都是复读 —— 它在群里说过的话, 转头在私聊再说一遍,
// 一样是卡带。所以这里**不加** scene 过滤 (与 repeat.go 同一条口径)。
func selfEchoKeysOf(s *Store) map[string]bool {
	if s == nil || s.DB == nil {
		return nil
	}
	rows, err := s.DB.Query(
		"SELECT payload_json FROM convo_messages WHERE dir = 'ai' AND kind = 'text' ORDER BY id DESC LIMIT ?",
		SelfEchoLookback)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var raw string
		if rows.Scan(&raw) != nil {
			continue
		}
		if k := EchoKey(raw); len([]rune(k)) >= SelfEchoMinRunes {
			out[k] = true
		}
	}
	return out
}

// selfEchoViolation 只在**整条回复都是复读**时给出违规 —— 那种情况值得重生成一次。
//
// 部分复读不进违规: 为了一行把整条回复重来一遍, 代价太大 (而且重试本身
// 有 17-24% 的帧失败率)。那种走 dropSelfEcho 静默剔掉就行。
func selfEchoViolationOf(s *Store, frame OutputFrame) string {
	recent := selfEchoKeysOf(s)
	if len(recent) == 0 {
		return ""
	}
	texts, echoes := 0, 0
	var sample string
	for _, it := range frame.Reply {
		if it.Kind != "text" || strings.TrimSpace(it.Text) == "" {
			continue
		}
		texts++
		if isSelfEcho(EchoKey(it.Text), recent) {
			echoes++
			if sample == "" {
				sample = strings.TrimSpace(it.Text)
			}
		}
	}
	if texts == 0 || echoes < texts {
		return ""
	}
	return "你整条回复都在重复自己刚才说过的话(「" + sample + "」) — 换一个说法, 别复读"
}

// dropSelfEcho 出口兜底: 把**部分**复读的行剔掉, 剩下的照常发。
//
// 剔光了不放行空帧 —— 那种情况已经被 selfEchoViolation 拦在重生成那一步了。
func dropSelfEchoOf(s *Store, frame OutputFrame) OutputFrame {
	recent := selfEchoKeysOf(s)
	if len(recent) == 0 || len(frame.Reply) == 0 {
		return frame
	}
	kept := make([]FrameItem, 0, len(frame.Reply))
	var dropped []string
	for _, it := range frame.Reply {
		if it.Kind == "text" && isSelfEcho(EchoKey(it.Text), recent) {
			dropped = append(dropped, strings.TrimSpace(it.Text))
			continue
		}
		kept = append(kept, it)
	}
	if len(dropped) == 0 {
		return frame
	}
	frame.Reply = kept
	log.Info("convo:dialog", "整句复读剔掉 %d 行 (剩 %d 行): %v", len(dropped), len(kept), dropped)
	return frame
}

// 两个薄入口: 帧校验在 DialogEngine 上, 出口在 Engine 上 —— 核心逻辑只有一份。
func (d *DialogEngine) selfEchoViolation(frame OutputFrame) string {
	return selfEchoViolationOf(d.Store, frame)
}

func (e *Engine) dropSelfEcho(frame OutputFrame) OutputFrame {
	return dropSelfEchoOf(e.Store, frame)
}
