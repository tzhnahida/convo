package convo

import (
	"fmt"
	"strings"
	"time"

	"github.com/tzhnahida/convo/internal/clock"
	"github.com/tzhnahida/convo/internal/log"
)

// ─────────────────────────────────────────────────────────────
// 它自己的对外动作 (§自我表达) 也要进它的**事实层**
//
// 2026-10-20 20:48 生产实录:
//   群主: @海梦/まりん 你怎么还换头像了
//   它:   换头像？我这边没动过啊 / 是不是缓存没刷
//
// 而 20:11 系统刚**用它自己的判断**换过头像:
//   convo_selfaction: 2026-10-20 20:11:11 | avatar | applied | 它说行: 要 | 头像已换
//
// 也就是说它不是撒谎 —— 它**真的不知道**系统替它做了什么。
// 这与"编造经历"是同一枚硬币的两面: 事实层缺了它自己那一半。
//
// 设计结论: 自我表达既然把控制权给了它, 那这些动作就必须**回灌给它**,
// 否则它会在群里否认自己刚做过的事 —— 那比不换头像更像故障。
// ─────────────────────────────────────────────────────────────

const (
	// SelfNoteLookback 回灌多久以内的动作。
	SelfNoteLookback = 48 * time.Hour
	// SelfNoteLimit 最多回灌几条 (说太多会变成念流水账)。
	SelfNoteLimit = 3
)

// selfActionLabels 动作 → 人话。
var selfActionLabels = map[string]string{
	SelfKindAvatar:   "换了头像",
	SelfKindNickname: "改了账号昵称",
	SelfKindCard:     "换了群名片",
	SelfKindQzone:    "发了一条说说",
}

// formatSelfActionTS 时间的人话。**今天的事情必须写"今天"** —— 2026-10-20 实测:
// 注入的是 "10-20 20:11 你换了头像", 它回出来却是"昨晚换头像"(时间是错的)。
// 一个裸日期它读不出来"这是几小时前", 而"今天/昨天"是它真能用的锚点。
func formatSelfActionTS(created, today string) string {
	ts := created
	if len(ts) >= 16 {
		clockPart := ts[11:16]
		if len(today) == 10 && len(ts) >= 10 && ts[:10] == today {
			return "今天 " + clockPart
		}
		return ts[5:10] + " " + clockPart // "10-17 00:52"
	}
	return ts
}

// RenderSelfActionNote 把最近**真的做过**的对外动作渲染成"你自己的事实"。
// 空输入 → 空串 (不占位, 没做过就不该有这一段)。
func RenderSelfActionNote(actions []SelfAction) string {
	today := clock.Today()
	var lines []string
	for _, a := range actions {
		label, ok := selfActionLabels[a.Kind]
		if !ok {
			continue
		}
		lines = append(lines, fmt.Sprintf("- %s 你%s", formatSelfActionTS(a.CreatedTS, today), label))
	}
	if len(lines) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n【你最近对外做过的事】(系统记录, 是真的 —— 别人问起别否认)\n")
	b.WriteString(strings.Join(lines, "\n"))
	b.WriteString("\n这些是你自己做的: 不是缓存、也不是别人动的手。有人问就照实说。\n")
	// ⚠️ 2026-10-20 实测: 给了真事实之后, 它开始给事实**加戏** ——
	// "昨晚换头像" / "睡不着翻相册"。系统只知道"做了什么", 不知道"当时在干嘛";
	// 不写明这一点, 它会用编出来的细节把一句真话撑成一句假话。
	b.WriteString("⚠️ 你只知道「做了什么」, 不知道「当时在干嘛、为什么」 —— 别为它编细节 (几点翻的相册、当时在想什么), 那是编。\n")
	return b.String()
}

// selfActionNote 取最近的动作并渲染。读不到 → 空串 (不注入, 不炸)。
func (e *Engine) selfActionNote() string {
	if e.Store == nil {
		return ""
	}
	as, err := e.Store.RecentAppliedSelfActions(SelfNoteLookback, SelfNoteLimit)
	if err != nil {
		log.Warn("convo:self", "对外动作读取失败(这一轮不注入): %v", err)
		return ""
	}
	return RenderSelfActionNote(as)
}

// RecentAppliedSelfActions 最近的**已生效**对外动作, 新的在前。
func (s *Store) RecentAppliedSelfActions(since time.Duration, limit int) ([]SelfAction, error) {
	if s == nil || s.DB == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = SelfNoteLimit
	}
	cutoff := clock.Now().Add(-since).Format("2006-01-02 15:04:05")
	rows, err := s.DB.Query("SELECT id, kind, COALESCE(mode, ''), state, COALESCE(decision, ''), COALESCE(payload, ''), "+
		"COALESCE(image_path, ''), COALESCE(image_digest, ''), COALESCE(score, 0), COALESCE(note, ''), COALESCE(result, ''), "+
		"COALESCE(created_ts, ''), COALESCE(applied_ts, '') FROM convo_selfaction "+
		"WHERE state = ? AND created_ts >= ? ORDER BY id DESC LIMIT ?", SelfStateApplied, cutoff, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SelfAction
	for rows.Next() {
		var a SelfAction
		if err := rows.Scan(&a.ID, &a.Kind, &a.Mode, &a.State, &a.Decision, &a.Payload, &a.Image, &a.Digest,
			&a.Score, &a.Note, &a.Result, &a.CreatedTS, &a.AppliedTS); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
