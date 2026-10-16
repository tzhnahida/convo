package convo

import (
	"unicode/utf8"

	"encoding/json"
	"fmt"
	"github.com/tzhnahida/convo/internal/log"
	"strings"

	"github.com/tzhnahida/convo/internal/clock"
)

// ——— P5 决策轨迹回放 (D1) ———
//
// 病灶: 七条问题里有五条是**事后翻 DB 才发现的**。修完几批之后, 「它为什么这次这么说」
// 要横跨五个子系统才答得上 —— 状态事实、心情、注入的记忆、调度动作、帧校验结果。
// 没有这张表, 后面每一次调参都是瞎调。
//
// ⚠️ **它不是日志, 是快照**: 日志记的是"发生了什么", 轨迹记的是"它当时看到的是什么"。
// system prompt 必须**整段存** —— 那一句回复的全部依据都在里面, 只存 hash 等于没存。
type DecisionTrace struct {
	ID  int64  `json:"id"`
	TS  string `json:"ts"`
	Seq int    `json:"seq"` // 进程内序号 (同秒多条也能定序)

	// ——— §6.10 场景与发言人 (护栏 3「偏向可见」的数据源) ———
	//
	// ⚠️ 没有这三列, "它更愿意接谁的话"这个管理页**没有数据源** ——
	// 设计稿 L978: "一个看不见偏向的群友, 比一个没有偏向的群友更危险"。
	SpeakerUIN string `json:"speaker_uin"` // 群里的发言人; 私聊留空
	SceneKey   string `json:"scene_key"`   // 空 = 私聊
	Stance     string `json:"stance"`      // §6.3 三态: silent | react | speak; 空 = 私聊路径
	// Reason 裁决理由 (§6.3 判据表 / §6.8 偏向的原话)。
	// ⚠️ 只有 Stance 的话, 管理页知道"没接", 但不知道**是判据没够还是偏向压了** ——
	// 那正是「看不见偏向」的定义。
	Reason string `json:"reason"`
	// BiasDelta §6.8 的 Δ ∈ {-1,0,+1}。**正数是"更愿意接"**, 与 Reason 一起构成归因。

	ActionKind string `json:"action_kind"` // immediate / active_open / preview / …
	Proactive  bool   `json:"proactive"`
	UserText   string `json:"user_text"`
	Teaching   bool   `json:"teaching"`

	StateFact string  `json:"state_fact"`
	MoodVal   float64 `json:"mood_valence"`
	MoodAro   float64 `json:"mood_arousal"`

	MemIDs   []string `json:"mem_ids"`
	SlangHit string   `json:"slang_hit"`

	SystemPrompt string `json:"system_prompt"`
	PromptBytes  int    `json:"prompt_bytes"`

	RawOutput string   `json:"raw_output"`
	Outcome   string   `json:"outcome"`
	SentTexts []string `json:"sent_texts"`
}

const (
	OutcomeFirstPass = "first_pass"
	OutcomeDegraded  = "degraded"
	OutcomeRefused   = "refused"
	OutcomeError     = "error"
)

// ClassifyOutcome 从帧与错误里判这一轮的结局 —— 只此一处定义, 免得各调用方各算一套。
func ClassifyOutcome(f OutputFrame, err error) string {
	switch {
	case err != nil:
		return OutcomeError
	case f.Refused:
		return OutcomeRefused
	case f.Degraded:
		return OutcomeDegraded
	default:
		return OutcomeFirstPass
	}
}

const traceInsertSQL = `INSERT INTO convo_decision_trace
	(ts, seq, action_kind, proactive, user_text, teaching, state_fact,
	 mood_valence, mood_arousal, mem_ids_json, slang_hit, system_prompt,
	 prompt_bytes, raw_output, outcome, sent_texts_json,
	 speaker_uin, scene_key, stance, reason)
	VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`

// RecordTrace 落一条轨迹。
//
// ⚠️ 它返回 error, 但**调用方不该因它中断回复** —— 观测失败不能拖垮主流程
// (与 moodWillingness 读失败返回 1.0 同一条原则: 失败往"照常说话"倒)。
func (s *Store) RecordTrace(t DecisionTrace) error {
	if t.TS == "" {
		t.TS = clock.TodayTS()
	}
	memJSON, _ := json.Marshal(t.MemIDs)
	sentJSON, _ := json.Marshal(t.SentTexts)
	// ⚠️ **轨迹里的 prompt 必须是合法 UTF-8。**
	//
	// 不是洁癖: 这条表是排障与回放的**唯一入口**, 而一条非法 UTF-8 的 prompt
	// 会让**按 UTF-8 读它的每一个工具**在那一行上直接报错 ——
	// 2026-10-15 我用 python 拉段位清单时才发现, 而那之前它已经躺了一整天。
	//
	// ⚠️ 洗了要**出声**: 静默洗掉等于把"某个注入源在吐脏字节"这件事藏起来,
	// 而它下一次可能脏在别的地方。
	if !utf8.ValidString(t.SystemPrompt) {
		log.Warn("convo:trace", "system prompt 不是合法 UTF-8 (%d 字节) —— 已清洗, 但要查是哪个注入源在吐脏字节", len(t.SystemPrompt))
		t.SystemPrompt = strings.ToValidUTF8(t.SystemPrompt, "\uFFFD")
	}
	_, err := s.DB.Exec(traceInsertSQL,
		t.TS, t.Seq, t.ActionKind, boolToInt(t.Proactive), t.UserText, boolToInt(t.Teaching),
		t.StateFact, t.MoodVal, t.MoodAro, string(memJSON), t.SlangHit, t.SystemPrompt,
		t.PromptBytes, t.RawOutput, t.Outcome, string(sentJSON),
		t.SpeakerUIN, t.SceneKey, t.Stance, t.Reason) // ⚠️ 20 列 / 20 值 / 20 参 —— 数得出来
	return err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

const traceSelectSQL = `SELECT id, ts, seq, action_kind, proactive, user_text, teaching,
	state_fact, mood_valence, mood_arousal, mem_ids_json, slang_hit, system_prompt,
	prompt_bytes, raw_output, outcome, sent_texts_json,
	speaker_uin, scene_key, stance
	FROM convo_decision_trace ORDER BY id DESC LIMIT ?`

// RecentTraces 最近 n 条轨迹 (回放用, 新的在前)。
func (s *Store) RecentTraces(n int) ([]DecisionTrace, error) {
	rows, err := s.DB.Query(traceSelectSQL, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DecisionTrace
	for rows.Next() {
		var t DecisionTrace
		var proactive, teaching int
		var memJSON, sentJSON string
		if err := rows.Scan(&t.ID, &t.TS, &t.Seq, &t.ActionKind, &proactive, &t.UserText,
			&teaching, &t.StateFact, &t.MoodVal, &t.MoodAro, &memJSON, &t.SlangHit,
			&t.SystemPrompt, &t.PromptBytes, &t.RawOutput, &t.Outcome, &sentJSON,
			&t.SpeakerUIN, &t.SceneKey, &t.Stance); err != nil {
			return nil, err
		}
		t.Proactive = proactive != 0
		t.Teaching = teaching != 0
		_ = json.Unmarshal([]byte(memJSON), &t.MemIDs)
		_ = json.Unmarshal([]byte(sentJSON), &t.SentTexts)
		out = append(out, t)
	}
	return out, rows.Err()
}

// RenderTrace 把一条轨迹渲染成人能读的因果链 —— **回放的产出物就是它**。
func RenderTrace(t DecisionTrace) string {
	var b strings.Builder
	kind := t.ActionKind
	if t.Proactive {
		kind += " (主动)"
	}
	fmt.Fprintf(&b, "── #%d  %s  %s  → %s\n", t.ID, t.TS, kind, strings.ToUpper(t.Outcome))
	if t.UserText != "" {
		fmt.Fprintf(&b, "   用户: %s\n", oneLine(t.UserText, 90))
	} else {
		b.WriteString("   用户: (无输入 —— 它自己开的头)\n")
	}
	if t.StateFact != "" {
		fmt.Fprintf(&b, "   状态: %s\n", oneLine(t.StateFact, 110))
	}
	fmt.Fprintf(&b, "   心情: valence %.3f · arousal %.2f\n", t.MoodVal, t.MoodAro)
	fmt.Fprintf(&b, "   注入记忆 %d 条: %s\n", len(t.MemIDs), strings.Join(t.MemIDs, ", "))
	if t.SlangHit != "" {
		fmt.Fprintf(&b, "   命中梗: %s\n", t.SlangHit)
	}
	fmt.Fprintf(&b, "   提示词 %d 字节 · 原始输出: %s\n", t.PromptBytes, oneLine(t.RawOutput, 120))
	for i, s := range t.SentTexts {
		fmt.Fprintf(&b, "   实发[%d] %s\n", i, oneLine(s, 90))
	}
	return b.String()
}

func oneLine(s string, max int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ⏎ ")
	return truncRunes(s, max)
}
