package convo

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/tzhnahida/convo/internal/clock"
	"github.com/tzhnahida/convo/internal/log"
)

// ——— 模块 6: 记忆 (§8.6) ———
//
// L1 会话上下文 (dialog.go 注入近期消息) / L2 会话摘要 (convo_sessions) /
// L3 convo_memory (k/v + label/kind/confidence/src_event)。
//
// 提取链: 会话结束 LLM 输出 memories_delta + summary; confidence≥0.7 直写,
// 否则 pending 缓冲 → 下次会话求证 1 条 (确认升级/纠正修正/2 次无回应丢弃)。
// 注入: top-K 20 (~800 token), 10% 概率温习旧记忆; 30 天降权, 90 天底档。

type Memory struct {
	MemID       string  `json:"mem_id"`
	K           string  `json:"k"`
	V           string  `json:"v"`
	Label       string  `json:"label"`
	Kind        string  `json:"kind"`
	Description string  `json:"description"`
	Confidence  float64 `json:"confidence"`
	State       string  `json:"state"` // active | pending
	CreatedTS   string  `json:"created_ts"`
	UpdatedTS   string  `json:"updated_ts"`
	AccessCount int     `json:"access_count"`
	LastAccess  string  `json:"last_access_ts"`

	// 归属: 只有 user_said 能进「你记得的关于 ta 的事」。
	// ai_said = 它自己说过的(绝不能当成用户的事实); observed = 环境/世界事件; inferred = 推断, 不注入。
	Source       string `json:"source"`
	LastInjected string `json:"last_injected_ts"`
	InjectCount  int    `json:"inject_count"`
	// 计划类记忆的终态
	DueTS  string `json:"due_ts"`
	Status string `json:"status"`

	// ——— §7.3 三个正交维度 ———
	// Sensitivity 敏感度: public 可跨人, relation 只在本场景可用 (private 到不了这里)。
	Sensitivity string `json:"sensitivity"`
	// SceneKey 归属: 这条记忆在哪个场景得到。空 = 不绑定。
	SceneKey string `json:"scene_key"`
	// SlotKey 槽位: **非空才参与"新事实取代旧事实"**。零值 = 不参与, 不是"槽位为空"。
	SlotKey string `json:"slot_key"`
	// SupersededTS 失效区间左端: 被取代的时刻 ("" = 仍然生效)。
	SupersededTS string `json:"superseded_ts"`
	// ValidUntil 失效区间右端 ("" = 无期限)。
	ValidUntil string `json:"valid_until"`
}

// memoryAge decay: 30 天降权 (×0.6), 90 天底档 (×0.3)。
func memoryDecay(lastAccess string, now time.Time) float64 {
	if lastAccess == "" {
		return 0.8
	}
	// Parse**InLocation**(time.Local): time.Parse 返回 UTC, 与本地 now 相减会整体偏移 8 小时,
	// 30/90 天的分档因此全部踩在错误的时刻上。LastCounterTS 早就用的是 InLocation, 这里漏了。
	ts, err := time.ParseInLocation("2006-01-02 15:04:05", lastAccess, time.Local)
	if err != nil {
		return 0.8
	}
	days := now.Sub(ts).Hours() / 24
	switch {
	case days >= 90:
		return 0.3
	case days >= 30:
		return 0.6
	default:
		return 1.0
	}
}

// TopMemories top-K 活跃记忆, 按 confidence×decay 排序 (注入用, 带 mem_id)。
// injectPenalty 检索诱导遗忘: 刚注入过的记忆**降温**, 而不是因为"最近活跃"而升权。
// 这是把记忆系统从正反馈改成负反馈的那一刀 —— 越被用, 短期越不再被用。
func injectPenalty(lastInjected string, now, windowStart time.Time) float64 {
	if lastInjected == "" {
		return 1.0
	}
	// ParseInLocation: 理由同 memoryDecay —— 而且这里更严重: 用 UTC 解析会让
	// "本窗口内注入过"永远成立, 冻结批次于是永不轮换(检索诱导遗忘静默失效)。
	ts, err := time.ParseInLocation("2006-01-02 15:04:05", lastInjected, time.Local)
	if err != nil {
		return 1.0
	}
	// 本冻结窗口内注入过的不算降温 (P1 缓存债)。
	// 没有这一条, 冻结是**假的**: 窗口内任何一次重算(新增一条记忆、会话结束结案)
	// 都会因为上一批刚被 MarkInjected 而整批换成不相交的另一批 —— 前缀照样断。
	// 有了它, 同窗口内的重算是**幂等**的: 得到同一批, 逐字节相同。
	if !ts.Before(windowStart) {
		return 1.0
	}
	switch h := now.Sub(ts).Hours(); {
	case h < 12:
		return 0.25
	case h < 72:
		return 0.6
	default:
		return 1.0
	}
}

// MarkInjected 记录本批记忆已注入 (下次检索降温; 只增不减的权重是"道具依赖"的成因)。
func (s *Store) MarkInjected(memIDs []string) {
	for _, id := range memIDs {
		_, _ = s.DB.Exec(`UPDATE convo_memory SET last_injected_ts=?, inject_count=COALESCE(inject_count,0)+1 WHERE mem_id=?`,
			clock.TodayTS(), id)
	}
}

// CloseExpiredPlans 计划类记忆的终态: 到期未兑现 → 结案, 不再注入。
// 没有终态的约定会永久 pending, 被反复引用 —— 那就是"执念"。
func (s *Store) CloseExpiredPlans(now time.Time) (int, error) {
	res, err := s.DB.Exec(`UPDATE convo_memory SET status='closed', updated_ts=?
		WHERE kind='plan' AND COALESCE(status,'open')='open'
		  AND COALESCE(due_ts,'')<>'' AND due_ts < ?`,
		clock.TodayTS(), now.Format("2006-01-02 15:04:05"))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		s.InvalidateMemoryBatch() // 结案的计划必须立刻从注入批次里消失
	}
	return int(n), nil
}

// TopMemories 全局取用 (不绑场景)。
func (s *Store) TopMemories(limit int, now time.Time) ([]Memory, error) {
	return s.TopMemoriesFor("", limit, now)
}

// TopMemoriesFor 按场景取用。
//
// ⚠️ §7.3 维一: 关系级记忆**绑场景** —— 在 A 群学到的事不许出现在 B 群。
// 走全局 TopMemories 就正好绕过了这条 (那是「禁止跨人转述」的读侧入口)。
func (s *Store) TopMemoriesFor(sceneKey string, limit int, now time.Time) ([]Memory, error) {
	rows, err := s.DB.Query(`SELECT mem_id, k, v, label, kind, description, confidence, state,
		created_ts, updated_ts, access_count, COALESCE(last_access_ts,''),
		COALESCE(source,'user_said'), COALESCE(last_injected_ts,''), COALESCE(inject_count,0),
		COALESCE(due_ts,''), COALESCE(status,'open'),
		COALESCE(sensitivity,''), COALESCE(scene_key,''), COALESCE(slot_key,''),
		COALESCE(superseded_ts,''), COALESCE(valid_until,'')
		FROM convo_memory
		WHERE state='active'
		  AND COALESCE(status,'open') <> 'closed'
		  AND COALESCE(source,'user_said') <> 'inferred'
		ORDER BY confidence DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type scored struct {
		m     Memory
		score float64
	}
	var all []scored
	for rows.Next() {
		var m Memory
		if err := rows.Scan(&m.MemID, &m.K, &m.V, &m.Label, &m.Kind, &m.Description,
			&m.Confidence, &m.State, &m.CreatedTS, &m.UpdatedTS, &m.AccessCount, &m.LastAccess,
			&m.Source, &m.LastInjected, &m.InjectCount, &m.DueTS, &m.Status,
			&m.Sensitivity, &m.SceneKey, &m.SlotKey, &m.SupersededTS, &m.ValidUntil); err != nil {
			return nil, err
		}
		// §7.3 三维闸门 (22 列 / 22 扫描值 —— 数得出来)。
		if !MemoryUsableIn(m, sceneKey, now) {
			continue
		}
		// §5.7 关系不对称: **同一个场景的加权, 别的场景降权** ——
		// 这就是"在 A 面前少提 B"的落点, 而它落在**排序**上不是过滤上。
		// §7.4 L1580「边界类记忆**降权而非永久禁止**」的落点。
		//
		// ⚠️ **只给边界类加地板, 不给所有 kind** ——
		// 给所有 kind 加地板的话, "久不用就降权"这条对谁都失效了。
		//
		// ⚠️ **是地板(取大)不是系数(相乘)**: 相乘会把边界压到 0.3×0.5=0.15,
		// 比普通记忆**更**禁止 —— 正好与设计稿相反。
		decay := memoryDecay(m.LastAccess, now)
		if isBoundaryKind(m.Kind) && decay < BoundaryDecayFloor {
			decay = BoundaryDecayFloor
		}
		all = append(all, scored{m: m,
			score: m.Confidence * decay *
				injectPenalty(m.LastInjected, now, now.Truncate(MemoryFreezeWindow)) *
				sceneAffinity(m, sceneKey)})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 确定性全序 (P1 缓存债): 同分时按 mem_id 升序。
	// sort.Slice **不是稳定排序** —— 只按 score 比较时, 同分条目的相对顺序不保证,
	// 同一份数据两次组装可能得到不同的记忆段 → 提示词前缀逐字节不同 → 缓存断裂。
	sort.Slice(all, func(i, j int) bool {
		if all[i].score != all[j].score {
			return all[i].score > all[j].score
		}
		return all[i].m.MemID < all[j].m.MemID
	})

	// 多样性配额: 同一 kind 最多 2 条 (纯按权重取会出现"5 条全是吃的")
	out := make([]Memory, 0, limit)
	perKind := map[string]int{}
	for _, sc := range all {
		if len(out) >= limit {
			break
		}
		if perKind[sc.m.Kind] >= 2 {
			continue
		}
		perKind[sc.m.Kind]++
		out = append(out, sc.m)
	}
	return out, nil
}

// 提取输出 (LLM 会话结束帧)
type MemoryDelta struct {
	MemID  string `json:"mem_id"`
	Source string `json:"source"`
	DueTS  string `json:"due_ts"`
	// Scope §7.5 双轨: 空/person = 关系层 (挂在场景里的人身上); scene = **群公共层**
	// (convo_memory_scene, 没有 uin 列)。⚠️ 缺省 person: 群公共层是「所有人看得到」
	// 的那一侧, 零值必须落在保守的一边。
	Scope       string  `json:"scope"`
	K           string  `json:"k"`
	V           string  `json:"v"`
	Label       string  `json:"label"`
	Kind        string  `json:"kind"`
	Description string  `json:"description"`
	Confidence  float64 `json:"confidence"`

	// §7.3: 写入口可显式声明这三样; 零值 = 未提供 → 走保守默认。
	Sensitivity string `json:"sensitivity"`
	SceneKey    string `json:"scene_key"`
	SlotKey     string `json:"slot_key"`
	ValidUntil  string `json:"valid_until"`
}

type SessionDelta struct {
	Summary  string        `json:"summary"`
	Memories []MemoryDelta `json:"memories"`
	Memes    []SlangDelta  `json:"memes"` // 模块 11: 用户说过的梗/口头禅

	// ⚠️ 下面两个由**调用方**填, 不是模型输出 (json:"-")。
	// 群公共层必须知道「是哪个群」与「谁先说的」(§7.5 血缘), 而抽取器看不见这两件事。
	SceneKey  string `json:"-"`
	OriginUIN string `json:"-"`
}

// SessionSceneOf 这一批消息里占多数的**群**场景 + 该场景第一条真人消息的 uin。
//
// ⚠️ 只认群场景: 私聊没有「大家」, 群公共层也就无从谈起。
// ⚠️ 返回空串 = **不写**群公共层 (失败往「不写」倒 —— 公共层的错是「所有人看得到」)。
func SessionSceneOf(msgs []MessageRecord) (sceneKey, originUIN string) {
	counts := map[string]int{}
	first := map[string]string{}
	var order []string
	for _, m := range msgs {
		sk := strings.TrimSpace(m.SceneKey)
		if SceneKindOf(sk) != SceneKindGroup {
			continue
		}
		if _, seen := counts[sk]; !seen {
			order = append(order, sk)
		}
		counts[sk]++
		if _, ok := first[sk]; !ok && m.Dir == "user" && strings.TrimSpace(m.SenderUIN) != "" {
			first[sk] = m.SenderUIN // 血缘: **谁先说的** (后续同 mem_id 的更新不改写它)
		}
	}
	best, bestN := "", 0
	for _, sk := range order {
		if counts[sk] > bestN {
			best, bestN = sk, counts[sk]
		}
	}
	return best, first[best]
}

// ApplySessionDelta 会话结束处理: L2 摘要落库; L3 ≥0.7 直写, 否则 pending。
func (s *Store) ApplySessionDelta(sessionID string, d SessionDelta) error {
	if d.Summary != "" {
		_, err := s.DB.Exec(`INSERT INTO convo_sessions(session_id, started_ts, ended_ts, summary)
			VALUES(?, ?, ?, ?)
			ON CONFLICT(session_id) DO UPDATE SET summary=excluded.summary, ended_ts=excluded.ended_ts`,
			sessionID, clock.TodayTS(), clock.TodayTS(), d.Summary)
		if err != nil {
			return err
		}
	}
	for _, m := range d.Memories {
		if m.MemID == "" || m.V == "" {
			continue
		}
		// §4.4.3 四档: **forget 与 habit 都不进记忆表**。
		//
		// ⚠️ 这一步必须在**写入口** —— "遗忘"的诚实做法是根本不写,
		// 而不是写进去再删 (两次 IO, 还留痕)。
		//
		// habit 档则要去改日程 (§4.5.3)。写进记忆表会变成
		// "它记得你熬夜, 但作息一动不动" —— 那正是第四档要防的事。
		switch DeriveTier(m) {
		case TierForget, TierHabit:
			tier := DeriveTier(m)
			noteTierDropped(tier)
			// ⚠️ 只记档位等于没说: forget 与 habit 是**两种完全不同的处置**
			// (前者当天就丢, 后者要去改日程 §4.5.3), 而 TierReason 给的是人话归因。
			// 它此前零调用 —— 于是"这条为什么没留下来"在日志里只剩一个字符串。
			log.Info("convo:memory", "§4.4.3 这一条不进记忆表 (%s): %s", tier, TierReason(m))
			continue
		}
		// §7.5 **双轨存储**: scope=scene 的记忆进**群公共层**, 不进人身上那一层。
		//
		// ⚠️ 两轨混在一起会让「群里大家共同知道的」变成某个人的私记 ——
		// 而群公共层**没有 uin 列** (scene.go 开头第 1 条), 它天然不能被跨人转述。
		// ⚠️ SaveSceneMemory 自己还会再过一遍私域词表, 返回 (false,nil) = **刻意丢弃**。
		if strings.EqualFold(strings.TrimSpace(m.Scope), "scene") {
			if strings.TrimSpace(d.SceneKey) == "" {
				// 不知道是哪个群 → **不写**: 群公共层必须知道自己的场景。
				continue
			}
			ok, serr := s.SaveSceneMemory(SceneMemory{
				MemID: m.MemID, SceneKey: d.SceneKey,
				K: m.K, V: m.V, Label: m.Label, Kind: m.Kind,
				Confidence: m.Confidence, OriginUIN: d.OriginUIN,
			})
			if serr != nil {
				return serr
			}
			if !ok {
				log.Info("convo:memory", "§7.5 群公共层刻意丢弃 (命中私域词表): %s", m.MemID)
			}
			continue
		}
		if m.Confidence >= 0.7 {
			if err := s.UpsertMemory(m, "active"); err != nil {
				return err
			}
		} else {
			if err := s.PendingMemory(m); err != nil {
				return err
			}
		}
	}
	// 模块 11: 用户说过的梗 → 梗库 (source=user; 高置信直转 active, 否则试用)
	for _, m := range d.Memes {
		if strings.TrimSpace(m.Phrase) == "" {
			continue
		}
		if _, err := s.UpsertSlang(m, "user"); err != nil {
			return err
		}
	}
	return nil
}

// memSource 归一化归属: 未知一律 inferred (宁缺勿假 —— 不注入)。
func memSource(s string) string {
	switch strings.TrimSpace(s) {
	case "user_said", "ai_said", "observed":
		return s
	default:
		return "inferred"
	}
}

func (s *Store) UpsertMemory(m MemoryDelta, state string) error {
	// ——— §6.4.2 私域防线: **写入口就丢弃** ———
	//
	// 不是"存下来再加个标记": 只要落过库, 它就进了备份、进了检索、进了模型的上下文,
	// 而"事后删除"永远追不上已经泄露出去的那一次。
	//
	// 放在这里而不是各个调用方, 是因为记忆的写入口**只有这一个**
	// (会话提取 / remember / SeedMemories 全都汇到这里) ——
	// 一处生效、全部路径受保护; 分散到调用方就一定会漏一处。
	//
	// ⚠️ **返回 nil 而不是 error**: 刻意丢弃不是故障。
	// 报错会把"用户说了句敏感的话"变成一次系统异常, 而它本来是一次正常的对话。
	// (与 A6 硬拒、scene.go 的 private 分支同一条约定。)
	if ClassifySensitivity("", m.K+" "+m.V+" "+m.Label) == SensitivityPrivate {
		log.Info("convo:store", "§6.4.2 私域内容未落库 (写入口丢弃): %s", m.MemID)
		return nil
	}
	// §7.4: 已行使遗忘权的主体, **写入口就拒收** —— 这是"不可被逆转"的落点。
	if s.ForgetScene(m.SceneKey) {
		log.Info("convo:forget", "§7.4 该主体已行使遗忘权, 记忆写入被拒: %s", m.SceneKey)
		return nil
	}
	if strings.TrimSpace(m.Source) == "" {
		// ⚠️ 零值被 memSource 归到 **inferred**, 而 TopMemoriesFor 会过滤掉 inferred ——
		// 于是这条记忆**写得进去、永远读不出来**, 且没有任何报错。
		// 保守默认是对的 (来源不明的记忆不该注入), 但**必须出声**。
		log.Warn("convo:store", "记忆未标来源 (将按 inferred 处理, 落库后不会被注入): %s", m.MemID)
	}
	slot := strings.TrimSpace(m.SlotKey)
	if slot == "" {
		// §7.3 维三: **只有显式槽位才取代**。自动按 k 派生会出真事故 ——
		// 「下周三体检」和「月底交房租」都是 k=plan, 却会被判成同一个槽,
		// 于是后一条把前一条**静默删掉**。计划类天然是并存的。
		slot = MemorySlot(m.Kind, m.K, m.Label)
	}
	sens := strings.TrimSpace(m.Sensitivity)
	if sens == "" {
		sens = SensitivityRelation // 零值 → **保守**: 绑场景, 不跨人
	}
	_, err := s.DB.Exec(`INSERT INTO convo_memory
		(mem_id, k, v, label, kind, description, confidence, state, created_ts, updated_ts, source, due_ts, status, sensitivity, scene_key, slot_key, valid_until)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?, 'open', ?,?,?,?)
		ON CONFLICT(mem_id) DO UPDATE SET v=excluded.v, label=excluded.label,
			description=excluded.description, confidence=excluded.confidence,
			state=excluded.state, updated_ts=excluded.updated_ts,
			source=excluded.source, due_ts=excluded.due_ts,
			sensitivity=excluded.sensitivity, scene_key=excluded.scene_key,
			slot_key=excluded.slot_key, valid_until=excluded.valid_until`,
		m.MemID, m.K, m.V, m.Label, m.Kind, m.Description, m.Confidence, state,
		clock.TodayTS(), clock.TodayTS(), memSource(m.Source), m.DueTS,
		sens, strings.TrimSpace(m.SceneKey), slot, strings.TrimSpace(m.ValidUntil))
	if err != nil {
		return err
	}
	// §7.3 维三: 新事实取代旧事实 —— **同一槽位的其它活跃行失效**。
	if slot != "" {
		if err := s.SupersedeSlot(slot, m.MemID, clock.TodayTS()); err != nil {
			log.Warn("convo:store", "槽位取代失败 (旧事实会继续被注入): %v", err)
		}
	}
	s.InvalidateMemoryBatch() // 刚写下的记忆立刻可见, 不压到冻结窗口到期
	return nil
}

func (s *Store) PendingMemory(m MemoryDelta) error {
	_, err := s.DB.Exec(`INSERT INTO convo_memory
		(mem_id, k, v, label, kind, description, confidence, state, created_ts, updated_ts, source, due_ts, status)
		VALUES(?,?,?,?,?,?,?, 'pending', ?, ?, ?, ?, 'open')
		ON CONFLICT(mem_id) DO NOTHING`,
		m.MemID, m.K, m.V, m.Label, m.Kind, m.Description, m.Confidence,
		clock.TodayTS(), clock.TodayTS(), memSource(m.Source), m.DueTS)
	if err == nil {
		s.InvalidateMemoryBatch()
	}
	return err
}

// PendingMemories 待求证缓冲 (最多 1 条/会话)。
func (s *Store) PendingMemories() ([]Memory, error) {
	rows, err := s.DB.Query(`SELECT mem_id, k, v, label, kind, description, confidence, state,
		created_ts, updated_ts, access_count, COALESCE(last_access_ts,'')
		FROM convo_memory WHERE state='pending' ORDER BY updated_ts DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Memory
	for rows.Next() {
		var m Memory
		if err := rows.Scan(&m.MemID, &m.K, &m.V, &m.Label, &m.Kind, &m.Description,
			&m.Confidence, &m.State, &m.CreatedTS, &m.UpdatedTS, &m.AccessCount, &m.LastAccess); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ResolvePending 处理求证结果: confirm→active; correct→修正; else 无回应计数。
func (s *Store) ResolvePending(memID, verdict, corrected string) error {
	var err error
	switch verdict {
	case "confirm":
		_, err = s.DB.Exec(`UPDATE convo_memory SET state='active', confidence=MAX(confidence,0.9), updated_ts=? WHERE mem_id=?`,
			clock.TodayTS(), memID)
	case "correct":
		_, err = s.DB.Exec(`UPDATE convo_memory SET v=?, state='active', confidence=0.9, updated_ts=? WHERE mem_id=?`,
			corrected, clock.TodayTS(), memID)
	default: // drop (2 次无回应)
		_, err = s.DB.Exec(`DELETE FROM convo_memory WHERE mem_id=? AND state='pending'`, memID)
	}
	if err == nil {
		s.InvalidateMemoryBatch()
	}
	return err
}

// TouchMemory 注入时计访问 (衰减重置信号)。
func (s *Store) TouchMemory(memID string) error {
	_, err := s.DB.Exec(`UPDATE convo_memory SET access_count=access_count+1, last_access_ts=? WHERE mem_id=?`,
		clock.TodayTS(), memID)
	return err
}

// ——— 会话级 L1/L2 辅助 ———

// TouchAndTopMemories 取本窗口的注入批次并记账。
//
// 冻结语义 (P1 缓存债): **同一冻结窗口内返回同一批**(含顺序与逐字节内容),
// 窗口到期才重算。原因见 Store.memMu 的注释 —— 轮级轮换 = 缓存每轮断裂。
//
// injectPenalty 与 MarkInjected 因此回到**批次级**(每窗口一次)而非轮次级:
// "检索诱导遗忘"的设计意图完整保留, 只是粒度对齐 §8.1 给记忆定的"慢"。
// ⚠️ 这里原来有一个 `TouchAndTopMemories(limit)` —— **已删**。
//
// 它是一行包装: `return s.TouchAndTopMemoriesFor("", limit)` ✓ ——
// 而**生产路径一律走带场景的那个** (§5.7: 缓存键是 (场景, 窗口) 二元组 ✓)。
//
// ⚠️ 而它多一个坏处: 它让**测试**走了一条生产不走的路 ✗ ——
// `cache_p1_test.go` 那四条钉的是「冻结窗口缓存」这条性质 ✓,
// 而那与场景无关 ⇒ 改走真函数, 性质一分不减 ✓。

// TouchAndTopMemoriesFor 与 TouchAndTopMemories 同款, 但**按场景取** (§5.7)。
//
// ⚠️ 缓存键是 (场景, 窗口) 二元组 —— 只按窗口做键的话, 群里那批会被私聊复用。
func (s *Store) TouchAndTopMemoriesFor(scene string, limit int) ([]Memory, error) {
	now := clock.Now()
	bucket := now.Truncate(MemoryFreezeWindow).Format("2006-01-02 15:04")

	s.memMu.Lock()
	defer s.memMu.Unlock()
	if bucket == s.memBucket && scene == s.memScene && s.memBatch != nil {
		return s.memBatch, nil // 窗口内复用: 记忆段逐字节相同 → 前缀缓存不断
	}

	ms, err := s.TopMemoriesFor(scene, limit, now)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(ms))
	for _, m := range ms {
		_ = s.TouchMemory(m.MemID)
		ids = append(ids, m.MemID)
	}
	s.MarkInjected(ids) // 检索诱导遗忘: 用过的降温
	s.memBucket, s.memScene, s.memBatch = bucket, scene, ms
	return ms, nil
}

// InvalidateMemoryBatch 丢弃冻结批次 (记忆被写入/删除后调用, 让下一轮立刻看到新记忆)。
//
// 没有这一刀, 刚说"记住这个"要等最多 30 分钟才进提示词 —— 那是 bug 不是缓存优化。
func (s *Store) InvalidateMemoryBatch() {
	s.memMu.Lock()
	s.memBucket, s.memScene, s.memBatch = "", "", nil
	s.memMu.Unlock()
}

// LastSessionSummary 最近一次会话摘要 (L2, 供新会话续接)。
func (s *Store) LastSessionSummary() (string, error) {
	var summary string
	err := s.DB.QueryRow(`SELECT summary FROM convo_sessions ORDER BY started_ts DESC LIMIT 1`).Scan(&summary)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return summary, err
}

// ——— 会话结束提取 (LLM: memories_delta + summary) ———

const extractPrompt = `你是记忆提取器。阅读下面的对话, 提取值得长期记住的关系记忆、用户说过的梗、一句话会话摘要。

输出单行 JSON:
{"summary":"<20 字内会话提要>","memories":[{"mem_id":"<snake_case_id>","k":"<类别, 如 food/work/pet/plan/anniversary>","v":"<事实, 按 source 对应的视角陈述>","label":"<给用户看的短标签>","kind":"fact|preference|plan|relation","description":"<一句话说明它的价值>","source":"user_said|ai_said|observed|inferred","due_ts":"<kind=plan 时的兑现时间 YYYY-MM-DD HH:MM:SS, 否则空串>","confidence":0.0,"scope":"person|scene"}],"memes":[{"phrase":"<原词>","meaning":"<一句话含义>","example":"<用户原话场景里的用法>","source_quote":"<用户原话, 几个词即可>","confidence":0.0}]}

规则:
- 只提取**长期有价值**的事实 (用户的喜好/计划/重要事件/你们的梗), 不要提取闲聊细节。
- **source 必须逐条判定; 判不出来就写 inferred**:
  · user_said = **用户明说的** (例: 用户在焊板子)
  · ai_said   = **你自己说过的** (例: 你说"我这边下雪了" → ai_said。**绝不能记成用户说的**)
  · observed  = 环境与世界事件 (天气/地震/节日/发生在别人身上的事)
  · inferred  = 你推断出来的
- **只有 user_said 会以"你记得 ta 的事"注入**; ai_said 只用来保持你自己前后一致; inferred 不注入。
- **最严重的一类错误是把你自己说过的话写成"用户说"** —— 那会变成关于 ta 的假事实, 一经发现必须写 inferred。
- kind=plan 的条目**必须**给 due_ts (兑现时间); 到期未兑现会被结案, 不再提起。
- 每段对话最多 4 条 memories; 没有值得记的 → "memories":[]。
- confidence: 用户明确说过=0.9+, 你在对话中推断=0.6, 猜的=0.4。
- **scope 只判一件事**: 这条是「关于某一个人的」还是「这个群大家共同的」——
  · person (**缺省**) = ta 的事 (喜好/计划/重要事件/你俩的梗) —— 绝大多数都是它;
  · scene  = **群里大家共同的** (群里的约定/共同的梗/一起经历过的事), 且**不是任何一个人的私事**;
  · ⚠️ 拿不准就写 person —— scene 会进**群公共层**、被群里所有人看到, 写错等于把私事挂上公共墙。
- **只有群聊里才可能有 scope=scene**; 私聊一律 person。
- 之前已经澄清/求证过的 (按下面需求说明) 不要重复。

memes 规则 (用户说过的梗/网络用语/口头禅):
- 只提取**用户实际说过**的 (source_quote 必须抄用户原话, 绝不造梗); phrase ≤12 字。
- 这梗要有"被记住的价值" (他反复说/你会想接他/你们之间形成的梗), 路人甲式的随口词不要。
- 每段对话最多 3 条; 没有 → "memes":[]。`

func ExtractSession(ctx context.Context, c Chatter, convoText string, pending []Memory) (SessionDelta, error) {
	user := convoText
	if len(pending) > 0 {
		user += "\n\n待向用户求证的记忆:" + pendingJSON(pending)
	}
	out, err := c.Chat(ctx, "你输出单行 JSON。", extractPrompt+"\n\n对话:\n"+user)
	if err != nil {
		return SessionDelta{}, err
	}
	raw, err := extractJSON(out)
	if err != nil {
		return SessionDelta{}, fmt.Errorf("extract session json: %w", err)
	}
	var d SessionDelta
	if err := json.Unmarshal(raw, &d); err != nil {
		return SessionDelta{}, fmt.Errorf("parse session delta: %w", err)
	}
	return d, nil
}

func pendingJSON(pending []Memory) string {
	b, _ := json.Marshal(pending)
	return string(b)
}
