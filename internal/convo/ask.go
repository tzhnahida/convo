package convo

import (
	"database/sql"
	"strings"
	"time"

	"github.com/tzhnahida/convo/internal/clock"
)

// ——— 模块 7: 双向索取 (§7.6) ———
//
// AI 主动索要生活分享: 每周 1-2 次、free 槽。拒绝 → 一次性小失望 + 立即转移
// 话题 + 被拒类别入黑名单(永不再问)。护栏: 浅→深渐进、隐私开关、敏感类不记。
// 类别只在 profile.Boundaries.ShareCategories 白名单内。

var AskCategories = []string{"weekend", "work", "food"}

const AskPerWeek = 2

// AskDue 判断本周是否该索取: 本周次数 < AskPerWeek && (距上次提问 ≥ 2 天 || 本周 0 次)。
func (s *Store) AskDue(now time.Time) (bool, error) {
	week := now.Format("2006-W02")
	// 用 convo_events 记录 ask_share_intent, 按周计数
	var n int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM convo_events
		WHERE kind='ask_share_intent' AND strftime('%Y-W%W', ts)=?`, week).Scan(&n)
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}
	if n >= AskPerWeek {
		return false, nil
	}
	var last string
	err = s.DB.QueryRow(`SELECT MAX(ts) FROM convo_events WHERE kind='ask_share_intent'`).Scan(&last)
	if err != nil || last == "" {
		return true, nil
	}
	lastTS, err := time.Parse("2006-01-02 15:04:05", last)
	if err != nil {
		return true, nil
	}
	return now.Sub(lastTS) >= 2*24*time.Hour || n == 0, nil
}

// DetectRejection 用户拒绝识别 (heuristic): 不/没空/算了/不想/别问。
func DetectRejection(text string) bool {
	// 排除 "不错/不只/差不多" 等负向误伤: 只认独立否定词
	neg := []string{"没空", "不想", "算了", "别问", "不说", "懒得说", "没什么", "没啥", "不告诉你"}
	if strings.Contains(text, "不错") || strings.Contains(text, "不只") {
		// "不错" 是肯定 — 但 "不怎么" 是否定
		if !strings.Contains(text, "不怎么") && !strings.Contains(text, "不太") {
			return false
		}
	}
	for _, k := range neg {
		if strings.Contains(text, k) {
			return true
		}
	}
	return false
}

// AddAskBlacklist 被拒类别 → 永不再问 (边界), 同时记事件。
func (s *Store) AddAskBlacklist(category string) error {
	_, err := s.DB.Exec(`INSERT INTO convo_events(ts, kind, payload_json)
		VALUES(?, 'ask_share_reject', ?)`, clock.TodayTS(), category)
	return err
}

// AskBlacklist 读当前黑名单 (profile 落盘为准, 事件为辅)。
func (p *PersonaProfile) AskBlacklist() []string {
	if p == nil {
		return nil
	}
	return p.Boundaries.AskBlacklist
}

// RejectionEvent 最近一次被拒类别 (对话注入用) — 从 events 找最新 ask_share_reject。
func (s *Store) RejectionEvent() (string, error) {
	var cat string
	err := s.DB.QueryRow(`SELECT payload_json FROM convo_events
		WHERE kind='ask_share_reject' ORDER BY id DESC LIMIT 1`).Scan(&cat)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return cat, err
}
