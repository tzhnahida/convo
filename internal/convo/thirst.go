package convo

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/tzhnahida/convo/internal/clock"
	"github.com/tzhnahida/convo/internal/log"
)

// ─────────────────────────────────────────────────────────────
// §4.2.4 **口渴: 唯一可以"随口说"的身体信号** (2026-10-15)
//
// 「我去倒杯水」不是索取关心, 是生活。它是这几条里**唯一**天然允许主动说出口的 ——
// 因为它**不指向对方**: 说饿了像在等投喂, 说累了像在等安慰, **说渴了就只是去喝水**。
//
// ⚠️ 所以这里做的是**受控的正面例外**: 权限本身是**条件授予**的。
// 预算用完 / 在冷却期 → 口渴退回和其他两条一样的待遇(不许说出口),
// 而**不是**告诉它"你今天还有 0 次"。
//
// 这就是 §4.6.1 那条纪律的具体做法: **生活上限内在化** ——
// 它感觉到的是"这会儿能不能说", 不是"我还剩几次"。
// ─────────────────────────────────────────────────────────────

const (
	// ThirstQuotaPerDay 每天允许随口提口渴的次数 (§4.2.4)。
	//
	// ⚠️ **这个数字不许进提示词。** 它只决定"这一轮给不给那个例外"。
	ThirstQuotaPerDay = 2
	// ThirstClichéLimit 窗口内同一句式重复多少次算口头禅。
	ThirstClichéLimit = 3
	// ThirstClichéWindowDays 句式记账的窗口。
	ThirstClichéWindowDays = 7
	// ThirstCooldownDays 判定口头禅之后冷却多久。
	ThirstCooldownDays = 7
)

// thirstMentionRE 认得出"它去喝东西了"。
//
// ⚠️ 刻意**窄**: 只认"倒/接/买/拿 + 杯/瓶"和"喝 + 口/杯/瓶"。
// 认宽了会把「喝水」这个**话题**也算成一次主动提及 —— 而聊到喝水和去喝水是两件事。
var thirstMentionRE = regexp.MustCompile(`(倒|接|买|拿)(一)?(杯|瓶)|喝(一)?(口|杯|瓶)|去喝(点)?水`)

// thirstSolicitRE **索取写法** —— 这个必须硬判。
//
// ⚠️ 区别只有一线之隔, 但那一线就是全部: 「我去倒杯水」是**陈述**,
// 「我好渴啊」是**等对方接话**。前者不指向对方, 后者指向。
var thirstSolicitRE = regexp.MustCompile(`我好渴|渴死|渴得(不行|要命|慌)|好想喝|谁给(我|俺)|能不能给(我|俺)(倒|拿)`)

// ThirstBudget 这一轮给不给"随口说"的例外。
type ThirstBudget struct {
	Allowed bool
	// Why 只给日志与 owner —— **绝不进提示词**(进了就是配额宣告)。
	Why string
}

// thirstBudget 判这一轮的口渴出口开不开。
//
// ⚠️ 读不到库 → **放行**。与 moodWillingness / fatigueTier 同一条原则:
// 失败往「正常说话」倒, 不往「闭嘴」倒 —— 反过来的话一次读库失败会让它整天不提口渴,
// 而没人能从表现上看出那是故障。
func (e *Engine) thirstBudget(now time.Time) ThirstBudget {
	if e.Store == nil {
		return ThirstBudget{Allowed: true}
	}
	if n := counterOrZero(e.Store, "thirst", clock.Today()); n >= ThirstQuotaPerDay {
		return ThirstBudget{Why: fmt.Sprintf("今天已经随口提过 %d 次", n)}
	}
	if ts, ok, err := e.Store.LastCounterTS("thirst_cool"); err == nil && ok {
		if now.Sub(ts) < ThirstCooldownDays*24*time.Hour {
			return ThirstBudget{Why: "同一句式说过头了"}
		}
	}
	return ThirstBudget{Allowed: true}
}

// NoteThirstMention 事后认一次"随口提到的口渴"。
//
// ⚠️ 与 noteDeclineIfAny 同规: **只在真的发出去了之后**才记 ——
// 降级/硬拒的产物不算它说的, 记了会白白吃掉今天的额度。
func (e *Engine) NoteThirstMention(text string) {
	if e.Store == nil {
		return
	}
	m := strings.TrimSpace(thirstMentionRE.FindString(text))
	if m == "" {
		return
	}
	if _, err := e.Store.CounterInc("thirst", clock.Today()); err != nil {
		// ⚠️ 出声: 计数写不上意味着上限永远不会触发, 也就是"可以无限随口说"。
		log.Warn("convo:thirst", "口渴计数失败(上限将不会触发): %v", err)
	}
	if err := e.Store.NoteThirstPhrase(m, clock.Now()); err != nil {
		log.Warn("convo:thirst", "口渴句式记账失败: %v", err)
		return
	}
	if n, err := e.Store.CountThirstPhrase(m, clock.Now(), ThirstClichéWindowDays); err == nil && n >= ThirstClichéLimit {
		// ⚠️ 判定口头禅 → 冷却一周 (§4.2.4「不许变成口头禅」)。
		if _, cerr := e.Store.CounterInc("thirst_cool", clock.Today()); cerr != nil {
			log.Warn("convo:thirst", "冷却标记写不上(口头禅还会继续): %v", cerr)
		} else {
			log.Info("convo:thirst", "§4.2.4 同一句式在 %d 天里说了 %d 次 — 冷却 %d 天",
				ThirstClichéWindowDays, n, ThirstCooldownDays)
		}
	}
}

// ThirstSolicitHit 这一句里有没有把口渴说成**索取**。返回命中的那一段。
func ThirstSolicitHit(text string) string { return thirstSolicitRE.FindString(text) }

// ——— 句式记账 (给 owner 看的那一面) ———

// EnsureThirstSchema 幂等建表。
func EnsureThirstSchema(s *Store) error {
	_, err := s.DB.Exec(`CREATE TABLE IF NOT EXISTS convo_thirst_phrase (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		ts TEXT NOT NULL, phrase TEXT NOT NULL
	)`)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(`CREATE INDEX IF NOT EXISTS idx_thirst_ts ON convo_thirst_phrase(ts)`)
	return err
}

// NoteThirstPhrase 记一次句式。
func (s *Store) NoteThirstPhrase(phrase string, at time.Time) error {
	_, err := s.DB.Exec(`INSERT INTO convo_thirst_phrase (ts, phrase) VALUES (?,?)`,
		at.Format(bodyTSLayout), phrase)
	return err
}

// CountThirstPhrase 窗口内同一句式出现了几次。
func (s *Store) CountThirstPhrase(phrase string, now time.Time, days int) (int, error) {
	since := now.AddDate(0, 0, -days).Format(bodyTSLayout)
	var n int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM convo_thirst_phrase WHERE phrase=? AND ts>=?`,
		phrase, since).Scan(&n)
	return n, err
}
