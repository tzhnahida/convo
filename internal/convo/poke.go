package convo

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"time"

	"github.com/tzhnahida/convo/internal/clock"
	"github.com/tzhnahida/convo/internal/log"
)

// 戳一戳: 对方戳它, 它怎么回。
//
// owner 2026-10-16: 「戳一戳这个来回你想想怎么弄」。三条设计:
//
//	① **不是每次一个样** —— 每次都秒回同一个动作, 那是摇铃不是人。
//	   所以按概率分三档: 戳回去 / 搭一句话 / 装死。
//	② **「装死」是功能不是故障** —— 戳一戳的乐趣一半在于对方可能不理你。
//	   睡着、低意愿日、刚回过、今天回够了, 都是"看见了但不回"。
//	③ **限流必须有** —— 没有它, 连戳会把消息刷满, 而且踩风控。
//
// ⚠️ 默认关闭 (poke.enabled=false): 与自我表达同一个纪律 —— 代码上船, 线上先不动。
// ⚠️ 只处理**戳它自己**的事件 (main.go 判 target_id); 它**不主动戳人** ——
// "戳"是对方发起的动作, 主动戳很打扰 (留配置项, 先不做)。

// PokeEvent 一次「被戳」。
type PokeEvent struct {
	SceneKey string
	UIN      string // 戳它的那个人
	GroupID  int64  // >0 = 群里戳的
	Private  bool
}

// 三种回应 (也是审计里的取值)。
const (
	PokeReplyPoke   = "poke"
	PokeReplyLine   = "line"
	PokeReplyIgnore = "ignore"
)

// cannedPokeLines 兜底短句 —— 只在模型不可用/超长时用, 而且轮着来。
//
// ⚠️ 刻意**不写人格台词**: 台词该由它自己按人设生成 (pokeLine 会先问模型)。
// 这里只保证"模型挂了也有句人话", 不让它变成复读机。
var cannedPokeLines = []string{"干嘛", "在的", "……怎么了", "戳我干嘛"}

// PokeRecord 每一次都留痕: 谁戳的、它回没回、为什么。
type PokeRecord struct {
	UIN     string
	GroupID int64
	Replied string
	Reason  string
	TS      string
}

// EnsurePokeSchema 建表 (老库补建, 幂等)。
func EnsurePokeSchema(db *sql.DB) error {
	_, err := db.Exec("CREATE TABLE IF NOT EXISTS convo_poke (" +
		"id INTEGER PRIMARY KEY AUTOINCREMENT, uin TEXT, group_id INTEGER, " +
		"replied TEXT, reason TEXT, ts TEXT)")
	if err != nil {
		return err
	}
	_, err = db.Exec("CREATE INDEX IF NOT EXISTS ix_poke_uin_ts ON convo_poke(uin, ts)")
	return err
}

// InsertPoke 落一行。
func (s *Store) InsertPoke(r PokeRecord) error {
	_, err := s.DB.Exec("INSERT INTO convo_poke(uin, group_id, replied, reason, ts) VALUES(?,?,?,?,?)",
		r.UIN, r.GroupID, r.Replied, r.Reason, r.TS)
	return err
}

// PokeCountFrom 这个人在最近 window 内戳了几次 (连戳判定用)。
func (s *Store) PokeCountFrom(uin string, window time.Duration) int {
	since := clock.Now().Add(-window).Format("2006-01-02 15:04:05")
	var n int
	_ = s.DB.QueryRow("SELECT COUNT(*) FROM convo_poke WHERE uin=? AND ts>=?", uin, since).Scan(&n)
	return n
}

// PokeRepliedToday 今天**真的回了**几次 (装死不算)。
func (s *Store) PokeRepliedToday() int {
	day := clock.TodayTS()[:10]
	var n int
	_ = s.DB.QueryRow("SELECT COUNT(*) FROM convo_poke WHERE ts LIKE ? AND replied<>?",
		day+"%", PokeReplyIgnore).Scan(&n)
	return n
}

// LastPokeReply 这个人上一次**被回**是什么时候 (冷却用)。
func (s *Store) LastPokeReply(uin string) (time.Time, bool) {
	var ts string
	err := s.DB.QueryRow("SELECT ts FROM convo_poke WHERE uin=? AND replied<>? ORDER BY id DESC LIMIT 1",
		uin, PokeReplyIgnore).Scan(&ts)
	if err != nil {
		return time.Time{}, false
	}
	t, perr := time.ParseInLocation("2006-01-02 15:04:05", strings.TrimSpace(ts), time.Local)
	if perr != nil {
		return time.Time{}, false
	}
	return t, true
}

// poker 拿通道的戳一戳能力 (**可选接口**, 拿不到就是没有)。
func (e *Engine) poker() (PokeSender, bool) {
	if e.Sender == nil {
		return nil, false
	}
	p, ok := e.Sender.(PokeSender)
	return p, ok
}

// pokeRoll 掷骰 —— 走注入点 (理由同 ChimeRollFn/DayMoodFn: 骰子不钉死, 测试就会偶尔红)。
func (e *Engine) pokeRoll(n int) int {
	if e.PokeRollFn != nil {
		return e.PokeRollFn(n)
	}
	return Dice(n)
}

// HandlePoke 被戳了一下 —— 决定回不回、怎么回。返回的错误只用于日志。
func (e *Engine) HandlePoke(ctx context.Context, ev PokeEvent) error {
	cfg := e.Cfg.Poke.Normalized()
	if !cfg.Enabled {
		return nil
	}
	snd, ok := e.poker()
	if !ok {
		return nil
	}
	rec := PokeRecord{UIN: ev.UIN, GroupID: ev.GroupID, TS: clock.Now().Format("2006-01-02 15:04:05")}
	// ⚠️ **+1**: 这一下也算一次。少了它, "连戳三次"永远差一次 ——
	// 用例当场咬出来了 (低意愿日第二下该理, 却还是不理)。
	times := e.Store.PokeCountFrom(ev.UIN, 2*time.Minute) + 1

	// ① 睡着: 连戳几次才算急事 —— 否则"睡得死"这件事就假了。
	if ws, _ := e.Store.LoadSchedule(); ws != nil && CurrentState(ws, clock.Now()) == StateSleep {
		if times < cfg.SleepUnlessTimes {
			rec.Replied, rec.Reason = PokeReplyIgnore, "睡了 (连戳 "+strconv.Itoa(times)+" 次)"
			_ = e.Store.InsertPoke(rec)
			return nil
		}
	}
	// ② 低意愿日: 同样要连戳两次才理。
	if e.dayMood() == DayMoodLow && times < 2 {
		rec.Replied, rec.Reason = PokeReplyIgnore, "低意愿日"
		_ = e.Store.InsertPoke(rec)
		return nil
	}
	// ③ 同一个人刚回过 —— 冷却。
	if last, ok := e.Store.LastPokeReply(ev.UIN); ok &&
		time.Since(last) < time.Duration(cfg.CooldownMin)*time.Minute {
		rec.Replied, rec.Reason = PokeReplyIgnore, "刚回过"
		_ = e.Store.InsertPoke(rec)
		return nil
	}
	// ④ 今天回够了。
	if n := e.Store.PokeRepliedToday(); n >= cfg.DailyCap {
		rec.Replied, rec.Reason = PokeReplyIgnore, "今天戳够了"
		_ = e.Store.InsertPoke(rec)
		return nil
	}
	// ⑤ 掷骰: 戳回去 / 搭一句话 / 这一次没理。
	v := e.pokeRoll(100)
	switch {
	case v < cfg.PokePct:
		rec.Replied, rec.Reason = PokeReplyPoke, "戳回去"
		_ = e.Store.InsertPoke(rec)
		e.pokeBack(snd, ev, cfg)
		return nil
	case v < cfg.PokePct+cfg.LinePct:
		line := e.pokeLine(ctx, ev)
		rec.Replied, rec.Reason = PokeReplyLine, line
		_ = e.Store.InsertPoke(rec)
		e.sendPokeLine(ev, line, cfg)
		return nil
	}
	rec.Replied, rec.Reason = PokeReplyIgnore, "这一次没理"
	_ = e.Store.InsertPoke(rec)
	return nil
}

// pokeBack 戳回去。延迟是为了像人: 零延迟秒回是机器。
//
// ⚠️ 延迟走 goroutine: 卡住的是 WS 事件循环, 那会连带把消息一起堵住。
// 测试里 DelayMaxSec=0 → 同步执行 (可断言)。
func (e *Engine) pokeBack(snd PokeSender, ev PokeEvent, cfg PokeConfig) {
	uid := e.UserID
	if uid <= 0 {
		return
	}
	do := func() {
		if d := e.pokeDelay(cfg); d > 0 {
			time.Sleep(d)
		}
		if err := snd.Poke(ev.GroupID, uid); err != nil {
			log.Warn("convo:poke", "戳回去没成: %v", err)
		}
	}
	if cfg.DelayMaxSec <= 0 {
		do()
		return
	}
	go do()
}

// sendPokeLine 搭一句话 (群戳就发群里, 私戳就私聊)。
func (e *Engine) sendPokeLine(ev PokeEvent, line string, cfg PokeConfig) {
	if strings.TrimSpace(line) == "" {
		return
	}
	send := func() {
		if d := e.pokeDelay(cfg); d > 0 {
			time.Sleep(d)
		}
		if ev.GroupID > 0 {
			if gs, ok := e.Sender.(GroupSender); ok {
				if err := gs.SendGroupText(ev.GroupID, line); err != nil {
					log.Warn("convo:poke", "群里搭话没成: %v", err)
				}
				return
			}
		}
		if e.Sender != nil {
			_ = e.Sender.SendText(e.UserID, line)
		}
	}
	if cfg.DelayMaxSec <= 0 {
		send()
		return
	}
	go send()
}

// pokeDelay 回之前等几秒 (随机, 0 = 不等)。
func (e *Engine) pokeDelay(cfg PokeConfig) time.Duration {
	if cfg.DelayMaxSec <= 0 {
		return 0
	}
	return time.Duration(e.pokeRoll(cfg.DelayMaxSec)) * time.Second
}

// pokeLine 让它自己说一句 —— 用现有人格链路生成, 生成不出来才用兜底短句。
func (e *Engine) pokeLine(ctx context.Context, ev PokeEvent) string {
	if e.Chatter != nil {
		name := "它"
		if p, err := e.Store.LoadProfile(); err == nil && p != nil && strings.TrimSpace(p.Name) != "" {
			name = strings.TrimSpace(p.Name)
		}
		sys := "你是" + name + "。有人戳了你一下 (QQ 的戳一戳)。回一句, 就像在手机上打字那样: " +
			"一句话以内, 口语, 不要客套, 不要解释, 不要问「要不要我帮你」这类话。"
		usr := "（" + ev.UIN + " 戳了你一下）"
		if out, err := e.Chatter.Chat(ctx, sys, usr); err == nil {
			t := strings.TrimSpace(strings.SplitN(strings.TrimSpace(out), "\n", 2)[0])
			if t != "" && len([]rune(t)) <= 30 {
				return t
			}
		}
	}
	return cannedPokeLines[e.pokeRoll(len(cannedPokeLines))]
}

// PokeStatsJSON 控制台读数用。
func PokeStatsJSON(rows []PokeRecord) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(rows))
	for _, r := range rows {
		out = append(out, map[string]interface{}{
			"uin": r.UIN, "group_id": r.GroupID, "replied": r.Replied,
			"reason": r.Reason, "ts": r.TS,
		})
	}
	return out
}

// LoadPokes 最近 N 条 (控制台/排障用)。
func (s *Store) LoadPokes(limit int) ([]PokeRecord, error) {
	if limit <= 0 || limit > 200 {
		limit = 30
	}
	rows, err := s.DB.Query("SELECT uin, group_id, replied, reason, ts FROM convo_poke ORDER BY id DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PokeRecord
	for rows.Next() {
		var r PokeRecord
		if err := rows.Scan(&r.UIN, &r.GroupID, &r.Replied, &r.Reason, &r.TS); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
