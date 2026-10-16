package convo

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/tzhnahida/convo/internal/clock"
	"github.com/tzhnahida/convo/internal/log"
)

// 对外自我表达: 群名片 / 头像 / 昵称 / 说说。
//
// ⚠️ 控制权在**它自己**手上 (owner 2026-10-16: 「ai 说行头像就能换, 控制权交给 ai」)。
//
// 所以这一层的形状是:
//
//	① 图先过**视觉闸** (ScreenCandidate, fail-closed) —— 它不可能绕过这道;
//	② 再由**它自己**用一个 YES/NO 判断「这张要不要用」 (askSelfJudgment);
//	③ 答 YES 且限频允许 → 直接做。全程落库, 每一次判断都能回看。
//
// 为什么「控制权给它」不等于「放任」: 限频、每日上限、以及**每一次都留痕** ——
// 一个不给刹车也不留痕的自主权, 出问题时连「它当时怎么想的」都查不到。

// SelfExpressSender 对外输出的能力 (**可选接口**)。
//
// ⚠️ 与 GroupSender 同一个理由: 不往 Sender 上加方法 —— 那会波及所有实现与测试替身。
type SelfExpressSender interface {
	SetGroupCard(groupID int64, card string) error
	SetProfile(nickname, personalNote string) error
	SetAvatar(localPath string) error
	PostQzone(text string, imagePaths []string) (string, error)
	DeleteQzone(msgID string) error
}

// 四种对外动作。
const (
	SelfKindCard     = "card"     // 群名片 (默认文案固定)
	SelfKindAvatar   = "avatar"   // 头像
	SelfKindNickname = "nickname" // 账号昵称 (全局, 默认仍是提议档)
	SelfKindQzone    = "qzone"    // 说说
)

// 动作状态。
const (
	SelfStateApplied  = "applied"
	SelfStateRejected = "rejected" // 它自己判了「不用这张」 / owner 说不
	SelfStatePending  = "pending"  // propose 档: 等 owner 一个字
	SelfStateFailed   = "failed"
)

// SelfAction 一次对外动作 (提议 / 判断 / 执行 / 失败都落这一张表)。
type SelfAction struct {
	ID        int64   `json:"id"`
	Kind      string  `json:"kind"`
	Mode      string  `json:"mode"`
	State     string  `json:"state"`
	Decision  string  `json:"decision"` // 它自己的判断 (要/不要 + 理由)
	Payload   string  `json:"payload"`  // 要给出去的东西 (名片文案 / 说说正文 / 昵称)
	Image     string  `json:"image"`    // 本地图路径 (头像/说说)
	Digest    string  `json:"digest"`
	Score     float64 `json:"score"`
	Note      string  `json:"note"`   // 视觉图意
	Result    string  `json:"result"` // 桥接端返回值 / 失败原因
	CreatedTS string  `json:"created_ts"`
	AppliedTS string  `json:"applied_ts"`
}

// EnsureSelfActionSchema 建表 (老库补建, 幂等)。
func EnsureSelfActionSchema(db *sql.DB) error {
	_, err := db.Exec("CREATE TABLE IF NOT EXISTS convo_selfaction (" +
		"id INTEGER PRIMARY KEY AUTOINCREMENT, " +
		"kind TEXT, mode TEXT, state TEXT, " +
		"decision TEXT, payload TEXT, image_path TEXT, image_digest TEXT, " +
		"score REAL, note TEXT, result TEXT, " +
		"created_ts TEXT, applied_ts TEXT)")
	if err != nil {
		return err
	}
	_, err = db.Exec("CREATE INDEX IF NOT EXISTS ix_selfaction_kind ON convo_selfaction(kind, id)")
	return err
}

// InsertSelfAction 落一行, 返回 id。
func (s *Store) InsertSelfAction(a SelfAction) (int64, error) {
	res, err := s.DB.Exec("INSERT INTO convo_selfaction(kind, mode, state, decision, payload, image_path, image_digest, score, note, result, created_ts, applied_ts) "+
		"VALUES(?,?,?,?,?,?,?,?,?,?,?,?)",
		a.Kind, a.Mode, a.State, a.Decision, a.Payload, a.Image, a.Digest, a.Score, a.Note, a.Result,
		clock.Now().Format("2006-01-02 15:04:05"), a.AppliedTS)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// LastSelfAction 某类动作**最近一次真的做了**的记录 (限频用)。
func (s *Store) LastSelfAction(kind string) (SelfAction, bool, error) {
	var a SelfAction
	row := s.DB.QueryRow("SELECT id, kind, mode, state, COALESCE(decision, ''), COALESCE(payload, ''), COALESCE(image_path, ''), "+
		"COALESCE(note, ''), COALESCE(result, ''), COALESCE(created_ts, ''), COALESCE(applied_ts, '') "+
		"FROM convo_selfaction WHERE kind=? AND state=? ORDER BY id DESC LIMIT 1", kind, SelfStateApplied)
	err := row.Scan(&a.ID, &a.Kind, &a.Mode, &a.State, &a.Decision, &a.Payload, &a.Image, &a.Note, &a.Result, &a.CreatedTS, &a.AppliedTS)
	if err == sql.ErrNoRows {
		return a, false, nil
	}
	if err != nil {
		return a, false, err
	}
	return a, true, nil
}

// PendingSelfAction 待 owner 一个字的那条 (propose 档)。
func (s *Store) PendingSelfAction() (SelfAction, bool, error) {
	var a SelfAction
	row := s.DB.QueryRow("SELECT id, kind, mode, payload, COALESCE(image_path, ''), COALESCE(decision, '') "+
		"FROM convo_selfaction WHERE state=? ORDER BY id DESC LIMIT 1", SelfStatePending)
	err := row.Scan(&a.ID, &a.Kind, &a.Mode, &a.Payload, &a.Image, &a.Decision)
	if err == sql.ErrNoRows {
		return a, false, nil
	}
	if err != nil {
		return a, false, err
	}
	a.State = SelfStatePending
	return a, true, nil
}

// ResolveSelfAction 把一条待办改成最终状态。
func (s *Store) ResolveSelfAction(id int64, state, result string) error {
	_, err := s.DB.Exec("UPDATE convo_selfaction SET state=?, result=? WHERE id=?", state, result, id)
	return err
}

// countSelfActionsToday 今天已经做了几次对外动作 (每日上限用)。
func (s *Store) countSelfActionsToday() int {
	day := clock.TodayTS()[:10]
	var n int
	_ = s.DB.QueryRow("SELECT COUNT(*) FROM convo_selfaction WHERE state=? AND created_ts LIKE ?",
		SelfStateApplied, day+"%").Scan(&n)
	return n
}

// LoadSelfActions 最近 N 条 (控制台读数)。
func (s *Store) LoadSelfActions(limit int) ([]SelfAction, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.DB.Query("SELECT id, kind, COALESCE(mode, ''), state, COALESCE(decision, ''), COALESCE(payload, ''), "+
		"COALESCE(image_path, ''), COALESCE(image_digest, ''), COALESCE(score, 0), COALESCE(note, ''), COALESCE(result, ''), "+
		"COALESCE(created_ts, ''), COALESCE(applied_ts, '') FROM convo_selfaction ORDER BY id DESC LIMIT ?", limit)
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

// SelfActionsJSON 控制台读数用。
func SelfActionsJSON(as []SelfAction) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(as))
	for _, a := range as {
		out = append(out, map[string]interface{}{
			"id": a.ID, "kind": a.Kind, "state": a.State, "decision": a.Decision,
			"payload": a.Payload, "image": a.Image, "score": a.Score, "note": a.Note,
			"result": a.Result, "created_ts": a.CreatedTS, "applied_ts": a.AppliedTS,
		})
	}
	return out
}

// selfExpressSender 拿通道的自我表达能力 (可选接口, 拿不到就是没有)。
func (e *Engine) selfExpressSender() (SelfExpressSender, bool) {
	if e.Sender == nil {
		return nil, false
	}
	s, ok := e.Sender.(SelfExpressSender)
	return s, ok
}

// VisionScorer 取视觉打分器 (没配 = nil, 那闸会 fail-closed)。
func (e *Engine) VisionScorer() VisionScorer {
	if e.Cycle != nil && e.Cycle.Scorer != nil {
		return e.Cycle.Scorer
	}
	return nil
}

// GroupCardText 群名片的固定文案 —— 空配置 → self_aliases 第一个 → 小夜。
func (e *Engine) GroupCardText(cfg SelfExpressConfig) string {
	if t := strings.TrimSpace(cfg.CardText); t != "" {
		return t
	}
	for _, a := range e.Cfg.Group.SelfAliases {
		if s := strings.TrimSpace(a); s != "" {
			return s
		}
	}
	return "小夜"
}

// NicknameText 账号昵称的文案 —— 空配置 → 人格名 → self_aliases 第一个。
//
// ⚠️ 它与群名片分开: 群名片只影响一个群, 昵称影响**所有好友与群**。
func (e *Engine) NicknameText(cfg SelfExpressConfig, p *PersonaProfile) string {
	if t := strings.TrimSpace(cfg.NicknameText); t != "" {
		return t
	}
	if p != nil && strings.TrimSpace(p.Name) != "" {
		return strings.TrimSpace(p.Name)
	}
	return e.GroupCardText(cfg)
}

// maybeNickname 账号昵称: 与群名片同一套形状 (文案没变就不动 + 限频),
// 但限频长得多 —— 它是全局的, 「隔三差五改名」是机器行为。
func (e *Engine) maybeNickname(ctx context.Context, sender SelfExpressSender, cfg SelfExpressConfig, p *PersonaProfile) error {
	want := e.NicknameText(cfg, p)
	if want == "" {
		return nil
	}
	last, ok, _ := e.Store.LastSelfAction(SelfKindNickname)
	if ok && strings.TrimSpace(last.Payload) == want {
		return nil // 已经是这个名字了
	}
	if ok {
		ts, perr := time.ParseInLocation("2006-01-02 15:04:05", strings.TrimSpace(last.AppliedTS), time.Local)
		if perr == nil && time.Since(ts) < time.Duration(cfg.NicknameMinDays)*24*time.Hour {
			return nil
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	a := SelfAction{Kind: SelfKindNickname, Mode: "auto", Payload: want,
		Decision: fmt.Sprintf("auto: 昵称跟人格名同步 (限频 %d 天)", cfg.NicknameMinDays)}
	if err := sender.SetProfile(want, ""); err != nil {
		a.State, a.Result = SelfStateFailed, err.Error()
		_, _ = e.Store.InsertSelfAction(a)
		return fmt.Errorf("set_qq_profile: %w", err)
	}
	a.State, a.Result = SelfStateApplied, "昵称 → "+want
	a.AppliedTS = clock.Now().Format("2006-01-02 15:04:05")
	if _, err := e.Store.InsertSelfAction(a); err != nil {
		return err
	}
	log.Info("convo:self", "昵称改了: %q", want)
	return nil
}

// selfExpressTick 每 tick 看一眼「要不要对外做点什么」。
//
// ⚠️ 它**不进 DecideTick**: 那边的裁决是「要不要跟 owner 说话」, 这里是「要不要动」,
// 两件事的节律与理由都不一样, 混进一个 switch 会让两边都难读。
func (e *Engine) selfExpressTick(ctx context.Context) error {
	cfg := e.Cfg.SelfExpress.Normalized()
	if !cfg.Enabled {
		return nil
	}
	sender, ok := e.selfExpressSender()
	if !ok {
		return nil // 通道不支持 (测试替身 / noop) —— 不是错误
	}
	if e.Store.countSelfActionsToday() >= cfg.DailyCap {
		return nil
	}
	p, err := e.Store.LoadProfile()
	if err != nil || p == nil {
		return nil
	}
	// ① 群名片 (默认 auto): 只在**文案与上次不同**且过了限频时才动。
	if cfg.Mode(SelfKindCard, "auto") == "auto" {
		if err := e.maybeGroupCard(ctx, sender, cfg); err != nil {
			log.Warn("convo:self", "群名片: %v", err)
		}
	}
	// ①b 账号昵称 (默认 propose; 换人格时设 auto): 同一套形状, 限频最长。
	if cfg.Mode(SelfKindNickname, "propose") == "auto" {
		if err := e.maybeNickname(ctx, sender, cfg, p); err != nil {
			log.Warn("convo:self", "昵称: %v", err)
		}
	}
	// ② 头像 / 说说 (默认 auto): 抓图 → 视觉闸 → 它自己判 → 做。
	for _, kind := range []string{SelfKindAvatar, SelfKindQzone} {
		mode := cfg.Mode(kind, "auto")
		if mode == "off" {
			continue
		}
		if !e.selfKindDue(kind, cfg) {
			continue
		}
		if err := e.trySelfImageAction(ctx, sender, cfg, p, kind, mode); err != nil {
			log.Warn("convo:self", "%s: %v", kind, err)
		}
	}
	return nil
}

// selfKindDue 限频: 这一类动作够久没做了吗。
func (e *Engine) selfKindDue(kind string, cfg SelfExpressConfig) bool {
	last, ok, err := e.Store.LastSelfAction(kind)
	if err != nil || !ok {
		return true
	}
	ts, perr := time.ParseInLocation("2006-01-02 15:04:05", strings.TrimSpace(last.AppliedTS), time.Local)
	if perr != nil {
		return true
	}
	gap := time.Duration(cfg.AvatarMinDays) * 24 * time.Hour
	if kind == SelfKindQzone {
		gap = time.Duration(cfg.QzoneMinDays) * 24 * time.Hour
	}
	return time.Since(ts) >= gap
}

// maybeGroupCard 群名片: 低风险, 所以给了它自主 (但限频 + 只在文案变了才动)。
func (e *Engine) maybeGroupCard(ctx context.Context, sender SelfExpressSender, cfg SelfExpressConfig) error {
	gids := e.Cfg.Group.Whitelist
	if len(gids) == 0 {
		return nil
	}
	want := e.GroupCardText(cfg)
	last, ok, _ := e.Store.LastSelfAction(SelfKindCard)
	if ok && strings.TrimSpace(last.Payload) == want {
		return nil // 已经是这个名字了 —— 不做无意义的改动 (人也不会天天改)
	}
	if ok {
		ts, perr := time.ParseInLocation("2006-01-02 15:04:05", strings.TrimSpace(last.AppliedTS), time.Local)
		if perr == nil && time.Since(ts) < time.Duration(cfg.CardMinHours)*time.Hour {
			return nil
		}
	}
	for _, gidStr := range gids {
		gid, err := strconv.ParseInt(strings.TrimSpace(gidStr), 10, 64)
		if err != nil || gid <= 0 {
			continue
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		a := SelfAction{Kind: SelfKindCard, Mode: "auto", Payload: want,
			Decision: "auto: 群名片按配置固定文案", AppliedTS: clock.Now().Format("2006-01-02 15:04:05")}
		if err := sender.SetGroupCard(gid, want); err != nil {
			a.State, a.Result = SelfStateFailed, err.Error()
			_, _ = e.Store.InsertSelfAction(a)
			return fmt.Errorf("set_group_card(%d): %w", gid, err)
		}
		a.State, a.Result = SelfStateApplied, fmt.Sprintf("group %d → %s", gid, want)
		if _, err := e.Store.InsertSelfAction(a); err != nil {
			return err
		}
		log.Info("convo:self", "群名片改了: 群 %d → %q", gid, want)
	}
	return nil
}

// imageQueries 抓图用的关键词 (owner 选了「角色名 + 关键词都要」→ 两个都搜)。
func imageQueries(cfg ImageSourceConfig) []string {
	out := []string{}
	for _, q := range []string{cfg.RoleQuery, cfg.Keyword} {
		if s := strings.TrimSpace(q); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// trySelfImageAction 抓图 → 视觉闸 → 它自己判 → (auto) 做 / (propose) 问。
func (e *Engine) trySelfImageAction(ctx context.Context, sender SelfExpressSender, cfg SelfExpressConfig, p *PersonaProfile, kind, mode string) error {
	src, err := NewImageSource(cfg.Images)
	if err != nil {
		return err
	}
	if src == nil {
		return nil // 没配图源 —— 这一类动作自然不发生
	}
	queries := imageQueries(cfg.Images)
	if len(queries) == 0 {
		return nil
	}
	hc := newImageHTTPClient(cfg.Images)
	// 抓 + 下 + 过闸: **第一张通过的**就用它 (每张都要过一次视觉, 所以不做批量囤图)。
	var picked *ImageCandidate
	lastReason := ""
	fail := 0
	for _, q := range queries {
		cands, ferr := src.Fetch(ctx, q, cfg.Images.PerRun)
		if ferr != nil {
			log.Warn("convo:self", "图源 %s 抓 %q 失败: %v", src.Name(), q, ferr)
			continue
		}
		for i := range cands {
			c := cands[i]
			if derr := DownloadCandidate(ctx, cfg.Images, hc, &c); derr != nil {
				log.Debug("convo:self", "下载跳过 %s: %v", c.URL, derr)
				continue
			}
			okScreen, why := ScreenCandidate(ctx, e.VisionScorer(), cfg.Images.VisionMin, cfg.Images.VisionRequiredOn(), &c)
			if !okScreen {
				fail++
				lastReason = why
				log.Info("convo:self", "视觉闸拦下一张: %s", why)
				continue
			}
			picked = &c
			break
		}
		if picked != nil {
			break
		}
	}
	if picked == nil {
		if fail > 0 {
			return fmt.Errorf("本轮 %d 张图都没过视觉闸 (最后一条: %s)", fail, lastReason)
		}
		return nil
	}
	base := SelfAction{Kind: kind, Mode: mode, Image: picked.LocalPath, Digest: picked.Digest,
		Score: picked.Score, Note: picked.Note}
	// ② 它自己判 —— 控制权在它手上。
	okAction, text, reason := e.askSelfJudgment(ctx, kind, picked, p)
	base.Decision = reason
	if !okAction {
		base.State = SelfStateRejected
		_, _ = e.Store.InsertSelfAction(base)
		log.Info("convo:self", "它自己判了不用这张 (%s): %s", kind, reason)
		return nil
	}
	if mode == "propose" {
		base.State = SelfStatePending
		base.Payload = text
		id, err := e.Store.InsertSelfAction(base)
		if err != nil {
			return err
		}
		if e.Sender != nil {
			_ = e.Sender.SendImage(e.UserID, picked.LocalPath)
			_ = e.Sender.SendText(e.UserID, SelfProposalLine(kind, reason, text))
		}
		log.Info("convo:self", "%s 提议已发出 (#%d)", kind, id)
		return nil
	}
	// auto: 直接做。
	switch kind {
	case SelfKindAvatar:
		base.Payload = picked.LocalPath
		if err := sender.SetAvatar(picked.LocalPath); err != nil {
			base.State, base.Result = SelfStateFailed, err.Error()
			_, _ = e.Store.InsertSelfAction(base)
			return fmt.Errorf("set_qq_avatar: %w", err)
		}
		base.Result = "头像已换"
	case SelfKindQzone:
		base.Payload = text
		if strings.TrimSpace(text) == "" {
			base.State = SelfStateRejected
			base.Decision = reason + " (但它没写出正文, 所以没发)"
			_, _ = e.Store.InsertSelfAction(base)
			return nil
		}
		msgID, err := sender.PostQzone(text, []string{picked.LocalPath})
		if err != nil {
			base.State, base.Result = SelfStateFailed, err.Error()
			_, _ = e.Store.InsertSelfAction(base)
			return fmt.Errorf("send_qzone_msg: %w", err)
		}
		base.Result = "说说已发 (msg " + msgID + ")"
	default:
		return nil
	}
	base.State = SelfStateApplied
	base.AppliedTS = clock.Now().Format("2006-01-02 15:04:05")
	if _, err := e.Store.InsertSelfAction(base); err != nil {
		return err
	}
	log.Info("convo:self", "%s 做了: %s | 依据: %s", kind, base.Result, reason)
	return nil
}

// askSelfJudgment 让它自己判断「这张要不要用」。
//
// 返回 (要不要, 正文 [说说用], 理由)。**判断必须留痕** —— reason 会落库。
//
// ⚠️ 解析必须保守: 只有首行明确以 要/不要 (或 YES/NO) 开头才认; 含糊 = 不用。
// 含糊时倾向「不做」是刻意的 —— 对外动作做错比没做贵得多。
func (e *Engine) askSelfJudgment(ctx context.Context, kind string, c *ImageCandidate, p *PersonaProfile) (bool, string, string) {
	if e.Chatter == nil {
		return false, "", "没有可用的模型, 不敢替它决定"
	}
	name := "它"
	if p != nil && strings.TrimSpace(p.Name) != "" {
		name = strings.TrimSpace(p.Name)
	}
	what := map[string]string{
		SelfKindAvatar: "把 QQ 头像换成这张图",
		SelfKindQzone:  "发一条说说 (会被好友看见), 配这张图",
	}[kind]
	if what == "" {
		what = kind
	}
	var sys strings.Builder
	fmt.Fprintf(&sys, "你是 %s。现在有一个真实的动作要你自己决定: %s。\n", name, what)
	sys.WriteString("换头像/说说不是别人要求你做的任务, 是你想不想。不想就说不。\n")
	sys.WriteString("只输出两行, 不要客套, 不要解释格式:\n")
	sys.WriteString("第一行: 要 或 不要\n")
	if kind == SelfKindQzone {
		sys.WriteString("第二行: 如果要, 写出你要发的那句话 (一到两句, 你自己的语气); 如果不要, 写一句为什么不要。\n")
	} else {
		sys.WriteString("第二行: 一句话理由 (为什么想换 / 为什么不要)。\n")
	}
	var usr strings.Builder
	fmt.Fprintf(&usr, "这张图: 来源 %s, 作者 %s, 标题 %s\n", c.Source, c.Author, c.Title)
	if u := strings.TrimSpace(c.PageURL); u != "" {
		fmt.Fprintf(&usr, "出处: %s\n", u)
	}
	if len(c.Tags) > 0 {
		fmt.Fprintf(&usr, "标签: %s\n", strings.Join(c.Tags, "、"))
	}
	if n := strings.TrimSpace(c.Note); n != "" {
		fmt.Fprintf(&usr, "你看过这张图之后记下的: %s\n", n)
	}
	fmt.Fprintf(&usr, "图的好感分: %.2f\n", c.Score)
	if ws, err := e.Store.LoadSchedule(); err == nil {
		fmt.Fprintf(&usr, "现在是 %s, 你的状态: %s\n", clock.Now().Format("2006-01-02 15:04"), CurrentState(ws, clock.Now()))
	}
	out, err := e.Chatter.Chat(ctx, sys.String(), usr.String())
	if err != nil {
		return false, "", fmt.Sprintf("问它的时候失败: %v", err)
	}
	yes, body := parseSelfJudgment(out)
	head := strings.TrimSpace(strings.SplitN(strings.TrimSpace(out), "\n", 2)[0])
	if yes {
		return true, body, "它说行: " + head
	}
	return false, body, "它说不要: " + head
}

// parseSelfJudgment 解析「要/不要 + 正文」。含糊一律当作不要。
func parseSelfJudgment(out string) (bool, string) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) == 0 {
		return false, ""
	}
	head := strings.TrimSpace(lines[0])
	head = strings.Trim(head, "【】[]*#- 　")
	yes := false
	switch {
	case strings.HasPrefix(head, "要") && !strings.HasPrefix(head, "不要"),
		strings.HasPrefix(head, "YES"), strings.HasPrefix(head, "yes"),
		strings.HasPrefix(head, "Yes"), strings.HasPrefix(head, "换"),
		strings.HasPrefix(head, "发"):
		yes = true
	case strings.HasPrefix(head, "不要"), strings.HasPrefix(head, "NO"),
		strings.HasPrefix(head, "No"), strings.HasPrefix(head, "no"),
		strings.HasPrefix(head, "不"):
		yes = false
	default:
		return false, "" // 含糊 = 不做
	}
	body := ""
	if len(lines) > 1 {
		body = strings.TrimSpace(strings.Join(lines[1:], " "))
	}
	return yes, body
}

// SelfProposalLine 提议档发给 owner 的那句话 (先说清「会被别人看见」)。
func SelfProposalLine(kind, reason, text string) string {
	switch kind {
	case SelfKindAvatar:
		return "我想把头像换成这张。" + reason + "\n要换就说个「准」, 不想就直接说不要。"
	case SelfKindQzone:
		if strings.TrimSpace(text) == "" {
			return "我想发条说说配这张图。" + reason + "\n要发就说个「准」。"
		}
		return "我想发条说说, 会被好友看见:「" + text + "」配这张图。" + reason + "\n要发就说个「准」。"
	}
	return "我想做一件事: " + kind + "。" + reason
}

// SelfActionApproval 认出 owner 的「准 / 不要」。
//
// ⚠️ 保守: 只认很短的回复, 且只认明确词 —— 避免把「好吧那就这样」误判成批准。
// 返回 (是不是批准, 有没有认出这是对提议的回应)。
func SelfActionApproval(text string) (approve bool, ok bool) {
	t := strings.TrimSpace(text)
	t = strings.Trim(t, "。.!！~ 　")
	if t == "" || len([]rune(t)) > 8 {
		return false, false
	}
	for _, w := range []string{"准", "准了", "可以", "行", "好", "好啊", "换吧", "发吧", "同意", "那就换", "那就发", "ok", "OK", "yes"} {
		if t == w {
			return true, true
		}
	}
	for _, w := range []string{"不要", "不换", "不发", "算了", "别", "不行", "no", "NO"} {
		if t == w {
			return false, true
		}
	}
	return false, false
}

// trySelfActionReply owner 回了「准」→ 把那条待办做掉。
// 返回 true = 已经处理完, 不必再当闲聊走一遍。
func (e *Engine) trySelfActionReply(ctx context.Context, text string) bool {
	cfg := e.Cfg.SelfExpress
	if !cfg.Enabled {
		return false
	}
	approve, ok := SelfActionApproval(text)
	if !ok {
		return false
	}
	a, has, err := e.Store.PendingSelfAction()
	if err != nil || !has {
		return false
	}
	sender, sok := e.selfExpressSender()
	if !sok {
		return false
	}
	if !approve {
		_ = e.Store.ResolveSelfAction(a.ID, SelfStateRejected, "owner 说不要")
		if e.Sender != nil {
			_ = e.Sender.SendText(e.UserID, "好, 那就不动。")
		}
		return true
	}
	var applyErr error
	result := ""
	switch a.Kind {
	case SelfKindAvatar:
		applyErr = sender.SetAvatar(a.Image)
		result = "owner 批准 → 头像已换"
	case SelfKindQzone:
		var msgID string
		msgID, applyErr = sender.PostQzone(a.Payload, []string{a.Image})
		result = "owner 批准 → 说说已发 (msg " + msgID + ")"
	case SelfKindNickname:
		applyErr = sender.SetProfile(a.Payload, "")
		result = "owner 批准 → 昵称已改"
	default:
		return false
	}
	if applyErr != nil {
		_ = e.Store.ResolveSelfAction(a.ID, SelfStateFailed, applyErr.Error())
		if e.Sender != nil {
			_ = e.Sender.SendText(e.UserID, "刚才那下没成 ("+applyErr.Error()+"), 我没硬来。")
		}
		return true
	}
	_ = e.Store.ResolveSelfAction(a.ID, SelfStateApplied, result)
	if e.Sender != nil {
		_ = e.Sender.SendText(e.UserID, "好, 换好了。")
	}
	log.Info("convo:self", "%s: %s", a.Kind, result)
	return true
}
