package convo

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tzhnahida/convo/internal/clock"
	"github.com/tzhnahida/convo/internal/log"
)

// ─────────────────────────────────────────────────────────────
// 会话判断机 (§6.10 线程归属的 30 分钟版本, 2026-10-20 owner 拍板「用 B, 因为准」)
//
// 要解决的问题: **没有 @ 但话题没结束, 它就不回**。
// 线程修复只覆盖"上一条就是它说的"; 真实群聊里话题能来回十几条、中间隔着别人 ——
// 那种延续纯查库判不出来 (时间戳和 uin 都不懂语义)。
//
// 所以花一次**极小的** LLM 调用 (系统提示 ~50 token + 封顶 8 条新消息 ~100 token),
// 每 5 分钟问一句: 这些新消息还在延续同一个会话吗? → {"same":true/false}
//
// ⚠️ 它只判"是不是同一个会话", **不判该不该说话** —— 该不该开口仍走 §6.3 价值判据。
// ⚠️ 失败方向: 判定失败 → **保持原状态** (关掉真会话 = 它又变成不 @ 就不理)。
// ─────────────────────────────────────────────────────────────

// SessionConfig 会话判断机的旋钮。
type SessionConfig struct {
	Enabled   bool `yaml:"enabled"`
	PollMin   int  `yaml:"poll_min"`   // 5
	WindowMin int  `yaml:"window_min"` // 30
	MaxMsgs   int  `yaml:"max_msgs"`   // 每次判定最多几条新消息
}

// Normalized 零值纪律: 缺省 = 内置默认。
func (c SessionConfig) Normalized() SessionConfig {
	if c.PollMin <= 0 {
		c.PollMin = 5
	}
	if c.WindowMin <= 0 {
		c.WindowMin = 30
	}
	if c.MaxMsgs <= 0 {
		c.MaxMsgs = 8
	}
	return c
}

// SessionRow 一个场景的会话状态。
type SessionRow struct {
	SceneKey  string
	Topic     string
	StartedTS string
	LastTS    string
	PollTS    string
	Turns     int
	Open      bool
}

// EnsureSessionSchema 建表 (幂等)。
func EnsureSessionSchema(db *sql.DB) error {
	_, err := db.Exec("CREATE TABLE IF NOT EXISTS convo_session (" +
		"scene_key TEXT PRIMARY KEY, topic TEXT NOT NULL DEFAULT '', " +
		"started_ts TEXT NOT NULL DEFAULT '', last_ts TEXT NOT NULL DEFAULT '', " +
		"poll_ts TEXT NOT NULL DEFAULT '', turns INTEGER NOT NULL DEFAULT 0, " +
		"open INTEGER NOT NULL DEFAULT 0)")
	return err
}

// SessionReplyMinGap 会话中它刚说完话的**缓冲期** —— 这个窗口内不再接下一句。
// 没有它, "会话中降一档"会变成对每条消息都接一句 —— 那是刷屏, 不是聊天。
const SessionReplyMinGap = 90 * time.Second

// SessionAnchorMax 锚点最多留多少字 (分类器的上文预算)。
const SessionAnchorMax = 200

// TouchSession 开一个会话 / 刷新它 (被 @、被点名、线程延续、它开口 都会调)。
// 刷新时**更新锚点**: 话题每轮都在走, 拿 30 分钟前那句当锚点会判出假阳性。
func (s *Store) TouchSession(sceneKey, anchor string, now time.Time) error {
	if strings.TrimSpace(sceneKey) == "" {
		return nil
	}
	anchor = truncateRunes(anchor, SessionAnchorMax)
	ts := now.Format(bodyTSLayout)
	var open int
	var started string
	err := s.DB.QueryRow("SELECT COALESCE(open,0), COALESCE(started_ts,'') FROM convo_session WHERE scene_key = ?", sceneKey).Scan(&open, &started)
	if err == sql.ErrNoRows || (err == nil && started == "") {
		_, ierr := s.DB.Exec("INSERT INTO convo_session (scene_key, topic, started_ts, last_ts, poll_ts, turns, open) "+
			"VALUES (?,?,?,?,?,1,1) ON CONFLICT(scene_key) DO UPDATE SET topic=excluded.topic, "+
			"started_ts=excluded.started_ts, last_ts=excluded.last_ts, poll_ts=excluded.poll_ts, turns=1, open=1",
			sceneKey, anchor, ts, ts, ts)
		return ierr
	}
	if err != nil {
		return err
	}
	if open == 0 {
		_, uerr := s.DB.Exec("UPDATE convo_session SET open=1, started_ts=?, last_ts=?, poll_ts=?, topic=?, turns=1 WHERE scene_key=?",
			ts, ts, ts, anchor, sceneKey)
		return uerr
	}
	_, uerr := s.DB.Exec("UPDATE convo_session SET open=1, last_ts=?, topic=?, turns=turns+1 WHERE scene_key=?", ts, anchor, sceneKey)
	return uerr
}

// OpenSessions 当前所有会话 (tick 层遍历用)。
func (s *Store) OpenSessions() ([]SessionRow, error) {
	rows, err := s.DB.Query("SELECT scene_key, COALESCE(topic,''), COALESCE(started_ts,''), " +
		"COALESCE(last_ts,''), COALESCE(poll_ts,''), COALESCE(turns,0) FROM convo_session WHERE open = 1")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionRow
	for rows.Next() {
		var r SessionRow
		if err := rows.Scan(&r.SceneKey, &r.Topic, &r.StartedTS, &r.LastTS, &r.PollTS, &r.Turns); err != nil {
			return nil, err
		}
		r.Open = true
		out = append(out, r)
	}
	return out, rows.Err()
}

// SessionActive 这个场景现在有没有一个未过期的会话。
func (s *Store) SessionActive(sceneKey string, window time.Duration, now time.Time) (SessionRow, bool, error) {
	var r SessionRow
	var last string
	err := s.DB.QueryRow("SELECT scene_key, COALESCE(topic,''), COALESCE(started_ts,''), "+
		"COALESCE(last_ts,''), COALESCE(poll_ts,''), COALESCE(turns,0) FROM convo_session "+
		"WHERE scene_key = ? AND open = 1", sceneKey).
		Scan(&r.SceneKey, &r.Topic, &r.StartedTS, &last, &r.PollTS, &r.Turns)
	if err == sql.ErrNoRows {
		return SessionRow{}, false, nil
	}
	if err != nil {
		return SessionRow{}, false, err
	}
	r.LastTS, r.Open = last, true
	t, perr := time.ParseInLocation(bodyTSLayout, last, time.Local)
	if perr != nil {
		return r, true, nil // 时间坏了 → 当作活跃 (失败往"还在聊"倒)
	}
	return r, now.Sub(t) <= window, nil
}

// CloseSession 结束会话。
func (s *Store) CloseSession(sceneKey string) error {
	_, err := s.DB.Exec("UPDATE convo_session SET open=0 WHERE scene_key=?", sceneKey)
	return err
}

// SetSessionAfterPoll 记下轮询时间; same 时同时重置 30 分钟窗口, 否则关闭。
func (s *Store) SetSessionAfterPoll(sceneKey, pollTS, lastTS string, same bool) error {
	if same {
		_, err := s.DB.Exec("UPDATE convo_session SET poll_ts=?, last_ts=? WHERE scene_key=?", pollTS, lastTS, sceneKey)
		return err
	}
	return s.CloseSession(sceneKey)
}

// SessionLines 上次轮询之后的新消息 (只取别人说的)。
func (s *Store) SessionLines(sceneKey, sinceTS string, limit int) []string {
	if limit <= 0 {
		limit = 8
	}
	rows, err := s.DB.Query("SELECT COALESCE(sender_uin,''), COALESCE(payload_json,'') FROM convo_messages "+
		"WHERE scene_key = ? AND dir = 'user' AND ts > ? ORDER BY id ASC LIMIT ?", sceneKey, sinceTS, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var uin, text string
		if rows.Scan(&uin, &text) != nil {
			continue
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		who := uin
		if len(who) > 4 {
			who = who[len(who)-4:]
		}
		out = append(out, who+": "+truncateRunes(text, 60))
	}
	return out
}

// LastAISpokeAt 它在这个场景最后一次说话的时间。
func (s *Store) LastAISpokeAt(sceneKey string) (time.Time, bool) {
	var ts string
	if err := s.DB.QueryRow("SELECT COALESCE(MAX(ts),'') FROM convo_messages WHERE scene_key=? AND dir='ai'", sceneKey).Scan(&ts); err != nil || ts == "" {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation(bodyTSLayout, ts, time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// SessionJudgeSystem 系统提示。**约 50 token** —— owner 定的预算。
const SessionJudgeSystem = "你是群聊会话判定器。判断下面的新消息是否还在延续【上文】那个话题。" +
	"只输出一行 JSON: {\"same\":true} 或 {\"same\":false}。不要解释。"

// SessionJudgeUser 拼用户消息 (上文 + 新消息)。
func SessionJudgeUser(anchor string, lines []string) string {
	var b strings.Builder
	b.WriteString("【上文】\n")
	b.WriteString(truncateRunes(strings.TrimSpace(anchor), SessionAnchorMax))
	b.WriteString("\n【新消息】\n")
	for _, l := range lines {
		b.WriteString("- ")
		b.WriteString(l)
		b.WriteString("\n")
	}
	b.WriteString("\n还在延续同一个话题吗? 只输出 JSON。")
	return b.String()
}

// ParseSessionVerdict 解析 {"same":bool}。宽容找 JSON, 不宽容内容。
func ParseSessionVerdict(raw string) (bool, error) {
	s := strings.TrimSpace(raw)
	i, j := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if i < 0 || j <= i {
		return false, fmt.Errorf("会话判定没有 JSON: %q", truncateRunes(s, 100))
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(s[i:j+1]), &m); err != nil {
		return false, fmt.Errorf("会话判定 JSON 不合法: %w", err)
	}
	v, ok := m["same"]
	if !ok {
		return false, fmt.Errorf("会话判定缺 same 字段: %q", truncateRunes(s, 100))
	}
	b, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("same 不是布尔: %q", truncateRunes(s, 100))
	}
	return b, nil
}

// judgeSession 问一次分类器。失败 → error (调用方保持原状态)。
func (e *Engine) judgeSession(ctx context.Context, anchor string, lines []string) (bool, error) {
	if e.Chatter == nil || len(lines) == 0 {
		return false, fmt.Errorf("没有判定素材")
	}
	raw, err := e.Chatter.Chat(ctx, SessionJudgeSystem, SessionJudgeUser(anchor, lines))
	if err != nil {
		return false, err
	}
	return ParseSessionVerdict(raw)
}

// sessionAnchor 取场景里最近几条原话当锚点。
func (e *Engine) sessionAnchor(sceneKey string) string {
	if e.Store == nil {
		return ""
	}
	rows, err := e.Store.DB.Query("SELECT COALESCE(payload_json,'') FROM convo_messages "+
		"WHERE scene_key=? ORDER BY id DESC LIMIT 4", sceneKey)
	if err != nil {
		return ""
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if rows.Scan(&t) == nil && strings.TrimSpace(t) != "" {
			out = append([]string{strings.TrimSpace(t)}, out...)
		}
	}
	return strings.Join(out, " / ")
}

// TouchSession 引擎层入口: 刷新会话并记锚点。
func (e *Engine) TouchSession(sceneKey string) {
	if e.Store == nil || !e.Cfg.Session.Enabled {
		return
	}
	if err := e.Store.TouchSession(sceneKey, e.sessionAnchor(sceneKey), clock.Now()); err != nil {
		log.Warn("convo:session", "会话刷新失败: %v", err)
	}
}

// sessionOpen 这个场景现在是否处于一个未过期的会话里。
func (e *Engine) sessionOpen(sceneKey string) bool {
	if e.Store == nil || !e.Cfg.Session.Enabled {
		return false
	}
	cfg := e.Cfg.Session.Normalized()
	_, ok, err := e.Store.SessionActive(sceneKey, time.Duration(cfg.WindowMin)*time.Minute, clock.Now())
	return err == nil && ok
}

// sessionPollTick 每 5 分钟问一次"还在聊同一件事吗"。
// ⚠️ 挂在 stepTick (每分钟跑), 自己按 poll_min 节流 —— 与 maybeLearn/maybeNews 同一条纪律。
func (e *Engine) sessionPollTick(ctx context.Context) {
	if e.Store == nil {
		return
	}
	cfg := e.Cfg.Session
	if !cfg.Enabled {
		return
	}
	cfg = cfg.Normalized()
	now := clock.Now()
	rows, err := e.Store.OpenSessions()
	if err != nil {
		log.Warn("convo:session", "读会话失败: %v", err)
		return
	}
	for _, s := range rows {
		last, lerr := time.ParseInLocation(bodyTSLayout, s.LastTS, time.Local)
		if lerr == nil && now.Sub(last) > time.Duration(cfg.WindowMin)*time.Minute {
			_ = e.Store.CloseSession(s.SceneKey)
			_, _ = e.Store.CounterInc("session_closed", clock.Today())
			log.Info("convo:session", "会话结束 (%s): %d 分钟没有同话题内容", s.SceneKey, cfg.WindowMin)
			continue
		}
		poll, perr := time.ParseInLocation(bodyTSLayout, s.PollTS, time.Local)
		if perr == nil && now.Sub(poll) < time.Duration(cfg.PollMin)*time.Minute {
			continue
		}
		lines := e.Store.SessionLines(s.SceneKey, s.PollTS, cfg.MaxMsgs)
		pollTS := now.Format(bodyTSLayout)
		if len(lines) == 0 {
			_ = e.Store.SetSessionAfterPoll(s.SceneKey, pollTS, s.LastTS, true)
			continue
		}
		same, jerr := e.judgeSession(ctx, s.Topic, lines)
		if jerr != nil {
			log.Warn("convo:session", "判定失败 → 保持原状态 (%s): %v", s.SceneKey, jerr)
			_, _ = e.Store.CounterInc("session_poll_fail", clock.Today())
			_ = e.Store.SetSessionAfterPoll(s.SceneKey, pollTS, s.LastTS, true)
			continue
		}
		if same {
			_ = e.Store.SetSessionAfterPoll(s.SceneKey, pollTS, pollTS, true)
			_, _ = e.Store.CounterInc("session_poll_same", clock.Today())
			log.Info("convo:session", "会话延续 (%s): 窗口重置 %d 分钟", s.SceneKey, cfg.WindowMin)
			continue
		}
		_ = e.Store.SetSessionAfterPoll(s.SceneKey, pollTS, s.LastTS, false)
		_, _ = e.Store.CounterInc("session_poll_new", clock.Today())
		log.Info("convo:session", "会话结束 (%s): 新消息已经在聊别的事", s.SceneKey)
	}
}
