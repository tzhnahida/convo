package convo

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/tzhnahida/convo/internal/clock"
	"github.com/tzhnahida/convo/internal/log"
)

// ─────────────────────────────────────────────────────────────
// 发言预算 + 被叫停 (§6.13, 2026-10-21 实测后加)
//
// 现场: 一天里它说了 687 条, 群友合计 675 条 —— 几乎每一条消息它都接。
// 根因是**自激循环**: 它回一句 → 下一条消息的"上一条"就是它 → 算"线程延续" → 再回;
// 会话(30min)开着时更直接: 会话中一律接。
//
// 两条护栏:
//   ① 发言预算: 同一条消息不算，但**间隔**与**单位时间条数**是硬上限;
//   ② 被叫停: 群里说"闭嘴" → 静默 + 关会话, 且**不解释**(解释也是刷屏)。
// ─────────────────────────────────────────────────────────────

// SpeechGuardConfig 发言预算的旋钮。
type SpeechGuardConfig struct {
	Enabled     bool `yaml:"enabled"`
	MinGapSec   int  `yaml:"min_gap_sec"`   // 两条之间最少隔几秒 (45)
	MaxPer10Min int  `yaml:"max_per_10min"` // 10 分钟最多说几条 (4)
	MuteMinutes int  `yaml:"mute_minutes"`  // 被叫停后静默多久 (30)
}

// Normalized 零值纪律: 缺省 = 内置默认。
func (c SpeechGuardConfig) Normalized() SpeechGuardConfig {
	if c.MinGapSec <= 0 {
		c.MinGapSec = 60
	}
	if c.MaxPer10Min <= 0 {
		c.MaxPer10Min = 3
	}
	if c.MuteMinutes <= 0 {
		c.MuteMinutes = 30
	}
	return c
}

// stopMarkers 叫它闭嘴的说法。**只在短句里生效** ——
// 长句里出现"闭嘴"往往是转述别人的话, 不是对它说的。
var stopMarkers = []string{
	"闭嘴", "别说了", "别说话", "别吵", "安静点", "消停",
	"别刷屏", "别刷了", "停一下", "少说两句", "烦死了", "吵死了", "别发了",
}

// hasAtSelf at 段里有没有点它 (@全体不算)。
func hasAtSelf(ats []string, selfUIN string) bool {
	for _, at := range ats {
		if atTouchesSelf(at, selfUIN) {
			return true
		}
	}
	return false
}

// IsStopRequest 这句话是不是在叫停。
func IsStopRequest(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" {
		return false
	}
	if len([]rune(t)) > 20 {
		return false // 长句 = 转述/长文, 不当叫停
	}
	for _, m := range stopMarkers {
		if strings.Contains(t, m) {
			return true
		}
	}
	return false
}

// EnsureSpeechGuardSchema 建表 (幂等)。
func EnsureSpeechGuardSchema(db *sql.DB) error {
	_, err := db.Exec("CREATE TABLE IF NOT EXISTS convo_speech_gate (" +
		"scene_key TEXT PRIMARY KEY, muted_until TEXT NOT NULL DEFAULT '', " +
		"muted_reason TEXT NOT NULL DEFAULT '', muted_ts TEXT NOT NULL DEFAULT '', " +
		"last_speak_ts TEXT NOT NULL DEFAULT '')")
	if err != nil {
		return err
	}
	// 老库补列: 发言预约 (2026-10-23)。
	// 预算原来靠「库里最后一条 AI 消息」, 而群发言在 goroutine 里跑 ——
	// 并发消息在消息入库前全部通过预算, 于是出现「一分钟 11 条」。
	// 现在决策时先记一笔预约, 下一条消息立刻能看见。
	_, _ = db.Exec("ALTER TABLE convo_speech_gate ADD COLUMN last_speak_ts TEXT NOT NULL DEFAULT ''")
	return nil
}

// MuteScene 让这个场景静默到某个时刻。
func (s *Store) MuteScene(sceneKey, until, reason string) error {
	_, err := s.DB.Exec("INSERT INTO convo_speech_gate (scene_key, muted_until, muted_reason, muted_ts) VALUES (?,?,?,?) "+
		"ON CONFLICT(scene_key) DO UPDATE SET muted_until=excluded.muted_until, "+
		"muted_reason=excluded.muted_reason, muted_ts=excluded.muted_ts",
		sceneKey, until, reason, clock.Now().Format(bodyTSLayout))
	return err
}

// SceneMute 现在是否处于静默期。
func (s *Store) SceneMute(sceneKey string, now time.Time) (time.Time, string, bool) {
	var until, reason string
	err := s.DB.QueryRow("SELECT COALESCE(muted_until,''), COALESCE(muted_reason,'') "+
		"FROM convo_speech_gate WHERE scene_key=?", sceneKey).Scan(&until, &reason)
	if err != nil || strings.TrimSpace(until) == "" {
		return time.Time{}, "", false
	}
	t, perr := time.ParseInLocation(bodyTSLayout, until, time.Local)
	if perr != nil || !now.Before(t) {
		return time.Time{}, "", false
	}
	return t, reason, true
}

// ClearSceneMute 解除静默 (被 @ = 用户想找它了)。
func (s *Store) ClearSceneMute(sceneKey string) error {
	_, err := s.DB.Exec("UPDATE convo_speech_gate SET muted_until='' WHERE scene_key=?", sceneKey)
	return err
}

// MarkSpeakAttempt 决策为「要说」时**立刻**记一笔预约 (发送之前)。
//
// 这是并发下的修复: 预算读的是「上次开口时间」, 而发送是异步的 ——
// 不先记账的话, 同一秒到的多条消息会同时看到「上一条是 1 分钟前」。
func (s *Store) MarkSpeakAttempt(sceneKey string) error {
	_, err := s.DB.Exec("INSERT INTO convo_speech_gate (scene_key, muted_until, muted_reason, muted_ts, last_speak_ts) "+
		"VALUES (?, '', '', '', ?) ON CONFLICT(scene_key) DO UPDATE SET last_speak_ts=excluded.last_speak_ts",
		sceneKey, clock.Now().Format(bodyTSLayout))
	return err
}

// LastSpokeTs 这个场景它最后一次开口的时间 —— 在「真的发出去」与「刚决定要发」之间取较晚者。
func (s *Store) LastSpokeTs(sceneKey string) (time.Time, bool) {
	var gate string
	_ = s.DB.QueryRow("SELECT COALESCE(last_speak_ts,'') FROM convo_speech_gate WHERE scene_key=?", sceneKey).Scan(&gate)
	var best time.Time
	if t, err := time.ParseInLocation(bodyTSLayout, gate, time.Local); err == nil {
		best = t
	}
	if t, ok := s.LastAISpokeAt(sceneKey); ok && t.After(best) {
		best = t
	}
	return best, !best.IsZero()
}

// RecentAITextsSince 这个场景最近说过的文本 (用于「同义句」拦截)。
func (s *Store) RecentAITextsSince(sceneKey string, since time.Time, limit int) []string {
	if s == nil || s.DB == nil {
		return nil
	}
	if limit <= 0 {
		limit = 4
	}
	rows, err := s.DB.Query("SELECT COALESCE(payload_json,'') FROM convo_messages "+
		"WHERE scene_key=? AND dir='ai' AND ts >= ? ORDER BY id DESC LIMIT ?", sceneKey, since.Format(bodyTSLayout), limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if rows.Scan(&t) == nil && strings.TrimSpace(t) != "" {
			out = append(out, t)
		}
	}
	return out
}

// looksAddressedToIt 这句话「像是在对它说」 (会话中直接开口前的最后一道闸)。
//
// 2026-10-23 P2「没话硬插」: 会话开着并不等于该接。没有称呼/问句/第二人称的闲聊,
// 退回价值判断 —— 有话说才开口。
func looksAddressedToIt(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" {
		return false
	}
	if strings.ContainsAny(t, "？?") {
		return true
	}
	for _, w := range []string{"你", "您", "你们", "咱"} {
		if strings.Contains(t, w) {
			return true
		}
	}
	for _, w := range []string{"吗", "呢"} {
		if strings.HasSuffix(t, w) {
			return true
		}
	}
	return false
}

// isContentlessText 这句话有没有实际内容 (纯标点/符号 = 没有)。
//
// 2026-10-23 P3: 群里出现过只回一个「？」的发言 —— 那是噪音, 不是聊天。
func isContentlessText(s string) bool {
	rs := []rune(strings.TrimSpace(s))
	if len(rs) == 0 {
		return true
	}
	for _, r := range rs {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Han, r) {
			return false
		}
	}
	return true
}

// normalizeForCompare 去掉空白与标点, 用于「同义句」比较。
func normalizeForCompare(s string) string {
	var b strings.Builder
	for _, r := range []rune(s) {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// sameSpokenRecently 这句话是不是**刚说过** (60 秒内, 归一化后相等)。
func (e *Engine) sameSpokenRecently(sceneKey, text string) bool {
	if e.Store == nil {
		return false
	}
	want := normalizeForCompare(text)
	if want == "" {
		return false
	}
	for _, prev := range e.Store.RecentAITextsSince(sceneKey, clock.Now().Add(-60*time.Second), 4) {
		if normalizeForCompare(prev) == want {
			return true
		}
	}
	return false
}

// AIMsgCountSince 这个场景里它从某时刻起说了几条 (含表情)。
func (s *Store) AIMsgCountSince(sceneKey string, since time.Time) int {
	var n int
	_ = s.DB.QueryRow("SELECT count(*) FROM convo_messages WHERE scene_key=? AND dir='ai' AND ts >= ?",
		sceneKey, since.Format(bodyTSLayout)).Scan(&n)
	return n
}

// speechBudget 发言预算: 静默期 / 间隔 / 单位时间条数。
//
// ⚠️ 它**只管非硬门槛**的发言 (@ 与危机词在上面早退, 不受它影响)。
func (e *Engine) speechBudget(sc SceneMsg) (bool, string) {
	if e.Store == nil {
		return true, ""
	}
	cfg := e.Cfg.Speech.Normalized()
	if !cfg.Enabled {
		return true, ""
	}
	now := clock.Now()
	if until, reason, ok := e.Store.SceneMute(sc.SceneKey, now); ok {
		return false, "被叫停中 (" + reason + ") — 静默到 " + until.Format("15:04")
	}
	if t, ok := e.Store.LastSpokeTs(sc.SceneKey); ok && now.Sub(t) < time.Duration(cfg.MinGapSec)*time.Second {
		return false, fmt.Sprintf("话太密 — 距上一条不到 %d 秒, 缓一下", cfg.MinGapSec)
	}
	if n := e.Store.AIMsgCountSince(sc.SceneKey, now.Add(-10*time.Minute)); n >= cfg.MaxPer10Min {
		return false, fmt.Sprintf("话太密 — 10 分钟内已经说了 %d 条", n)
	}
	return true, ""
}

// noteStopRequest 记一次"被叫停": 静默 + 关会话 (否则 5 分钟后会话又把话接回来)。
func (e *Engine) noteStopRequest(sc SceneMsg) (time.Time, string) {
	cfg := e.Cfg.Speech.Normalized()
	until := clock.Now().Add(time.Duration(cfg.MuteMinutes) * time.Minute)
	reason := truncateRunes(strings.TrimSpace(sc.Text), 20)
	if e.Store != nil {
		if err := e.Store.MuteScene(sc.SceneKey, until.Format(bodyTSLayout), reason); err != nil {
			log.Warn("convo:speech", "静默记录失败: %v", err)
		}
		_ = e.Store.CloseSession(sc.SceneKey)
		_, _ = e.Store.CounterInc("stop_request", clock.Today())
	}
	log.Info("convo:speech", "被叫停 (%s): %q → 静默到 %s", sc.SceneKey, reason, until.Format("15:04"))
	return until, reason
}
