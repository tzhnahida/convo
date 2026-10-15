package convo

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"sort"
	"strings"

	"github.com/tzhnahida/VocabLock/internal/clock"
)

// ─────────────────────────────────────────────────────────────
// §2.3 世界观挂载
//
// 「它有一个**来处**: 一个作品、一个城市、一群人。」
//
// 三层, 纪律各不相同:
//
//   L1 世界设定   地点 / 组织 / 规则 / 大事时间线   只读、**有出处**; 无来源标存疑
//   L2 关系层     与它关系最深的 2-5 人            关系类型**只允许四类**
//   L3 当下生活   这些人**现在**在忙什么           **这一层才是「它的一天」的背景**
//
// ⚠️ L1/L2 只是布景, **L3 才是它每天真实用到的那一层** —— 只写 L1 的世界是设定集,
// 不是生活。
//
// ⚠️ **现编必须落库再用** (§2.3 明文): 否则下次自相矛盾。
// 与原作设定冲突的现编 = 错 (群里必有懂的人); 与原作无关的现编自由但要与人格自洽。
// ─────────────────────────────────────────────────────────────

// RelationKind 关系类型 —— **只允许四类** (§2.3)。
//
// ⚠️ 这是个**闭集**, 不是自由文本。多一类就会长出「暧昧对象」这种 ——
// 而 §2.3 护栏明确不许恋爱向关系。
type RelationKind string

const (
	RelationCompanion RelationKind = "companion" // 同伴
	RelationRival     RelationKind = "rival"     // 对手
	RelationFamily    RelationKind = "family"    // 家人
	RelationMentor    RelationKind = "mentor"    // 师长
)

// Valid 关系类型是否在四类里。
func (r RelationKind) Valid() bool {
	switch r {
	case RelationCompanion, RelationRival, RelationFamily, RelationMentor:
		return true
	}
	return false
}

// Label 中文名 (注入提示词用)。
func (r RelationKind) Label() string {
	switch r {
	case RelationCompanion:
		return "同伴"
	case RelationRival:
		return "对手"
	case RelationFamily:
		return "家人"
	case RelationMentor:
		return "师长"
	}
	return ""
}

const (
	// CastMinFixed / CastMaxFixed 定稿配额的上下限。
	// ⚠️ **低于 2 人 = 没有关系层** —— 一个谁都不认识的角色讲不出「我的一天」。
	CastMinFixed = 2
	CastMaxFixed = 5
	// CastMaxDynamic 动态配角上限。**不占上面的额** (§2.3 明文)。
	CastMaxDynamic = 8
	// WorldFactMaxRunes L1 单条长度上限 —— 防「逐字复述原作」。
	WorldFactMaxRunes = 120
)

// WorldFact 一条世界设定。
type WorldFact struct {
	Text string `json:"text"`
	// Source 出处。
	// ⚠️ 空 = 现编 → **必须标存疑**, 不许当成原作设定讲出去。
	Source string `json:"source,omitempty"`
}

// Unsure 这条是不是没有出处的现编。
func (f WorldFact) Unsure() bool { return strings.TrimSpace(f.Source) == "" }

// WorldSetting L1 世界设定。
type WorldSetting struct {
	Places   []WorldFact `json:"places,omitempty"`   // 地点
	Orgs     []WorldFact `json:"orgs,omitempty"`     // 组织
	Rules    []WorldFact `json:"rules,omitempty"`    // 规则
	Timeline []WorldFact `json:"timeline,omitempty"` // 大事时间线
}

// CastMember 关系层里的一个人 (L2 + L3)。
type CastMember struct {
	Name     string       `json:"name"`
	Relation RelationKind `json:"relation"`
	History  string       `json:"history,omitempty"` // L2 一段共同历史
	Topics   string       `json:"topics,omitempty"`  // L2 话题偏好
	Status   string       `json:"status,omitempty"`  // L3 现在在忙什么
	// StatusDay 这条 Status 是哪一天的 (YYYY-MM-DD)。
	//
	// ⚠️ 没有它就无法判断"该不该重算" —— 而按"距上次多久"判断会在
	// 跨天那一拍算错, 表现是某天忘了刷新: 静默, 且无从察觉。
	StatusDay string `json:"status_day,omitempty"`
	// Dynamic 动态配角 (§3.6.5b): 陌生人被反复提及后升级而来。
	// **降级版** —— 只有名字 + 关系 + 一个当前状态; 不占定稿配额。
	Dynamic bool   `json:"dynamic,omitempty"`
	Source  string `json:"source,omitempty"` // 空 = 现编
	// ——— §3.6.5c 退出机制 (2026-10-15) ———
	//
	// Retired = **不在当下生活层了**。它转入"过去的人": 可以被回忆提起,
	// 但**不许若无其事地回来**(说过搬家的不能再出现)。
	Retired   bool   `json:"retired,omitempty"`
	RetiredTS string `json:"retired_ts,omitempty"`
	RetireWhy string `json:"retire_why,omitempty"`
	// LastSeenDay 最后一次**被提及**是哪一天 —— 判 60 天沉降的唯一输入。
	//
	// ⚠️ **不是 StatusDay**: 后者只说明状态刷新过, 而刷新是凌晨自动跑的,
	// 与"它有没有在我生活里出现"是两件事。拿 StatusDay 判的话**永远不会有配角退出**。
	// ⚠️ 空 = **未提供** ≠ "六十天没见" —— 空的一律不退出, 否则所有新配角一开始就是过去的人。
	LastSeenDay string `json:"last_seen_day,omitempty"`
}

// WorldBible 一个角色的整个世界。
type WorldBible struct {
	// Self 它自己在这个世界里的位置 (一句话)。
	Self      string       `json:"self,omitempty"`
	Setting   WorldSetting `json:"setting"`
	Cast      []CastMember `json:"cast"`
	UpdatedTS string       `json:"updated_ts,omitempty"`
}

// ——— 配额 ———

// Fixed 定稿配角 (完整版)。
func (b WorldBible) Fixed() []CastMember {
	var out []CastMember
	for _, m := range b.Cast {
		if !m.Dynamic {
			out = append(out, m)
		}
	}
	return out
}

// Dynamic 动态配角 (降级版)。
func (b WorldBible) Dynamic() []CastMember {
	var out []CastMember
	for _, m := range b.Cast {
		if m.Dynamic {
			out = append(out, m)
		}
	}
	return out
}

// NormalizeWorldBible 兜底 + 裁剪。
//
// ⚠️ **动态配角不占定稿配额** (§2.3 明文): 两类各自限额, 不是合起来 5 个。
// 合成一个上限的话, 动态升级会把定稿的人挤出去 —— 而那是它最熟的人。
func NormalizeWorldBible(b WorldBible) WorldBible {
	var fixed, dyn []CastMember
	for _, m := range b.Cast {
		m.Name = strings.TrimSpace(m.Name)
		if m.Name == "" {
			continue
		}
		if m.Dynamic {
			dyn = append(dyn, m)
		} else {
			fixed = append(fixed, m)
		}
	}
	if len(dyn) > CastMaxDynamic {
		dyn = dyn[:CastMaxDynamic]
	}
	b.Cast = append(fixed, dyn...)
	return b
}

// ValidateWorldBible 硬约束。**写入前必须过这一关。**
func ValidateWorldBible(b WorldBible) error {
	fixed := b.Fixed()
	if len(fixed) < CastMinFixed {
		return fmt.Errorf("§2.3 关系层至少要 %d 人 (现在 %d) —— 一个谁都不认识的角色讲不出它的一天", CastMinFixed, len(fixed))
	}
	if len(fixed) > CastMaxFixed {
		return fmt.Errorf("§2.3 定稿配角上限 %d 人 (现在 %d)", CastMaxFixed, len(fixed))
	}
	if len(b.Dynamic()) > CastMaxDynamic {
		return fmt.Errorf("§3.6.5b 动态配角上限 %d 人", CastMaxDynamic)
	}
	seen := map[string]bool{}
	for _, m := range b.Cast {
		if !m.Relation.Valid() {
			return fmt.Errorf("关系类型 %q 不在四类里 (同伴/对手/家人/师长) — %s", m.Relation, m.Name)
		}
		if seen[m.Name] {
			return fmt.Errorf("关系层里 %s 出现了两次", m.Name)
		}
		seen[m.Name] = true
		if err := CastGuard(m); err != nil {
			return err
		}
	}
	return nil
}

// castForbidden 关系层的内容护栏 (§2.3)。
//
// ⚠️ 这几条是**产品红线**, 不是内容偏好: 恋爱向 / 未成年暧昧 / 伤害向 ——
// 它是个住进熟人 QQ 群的东西, 越界的代价由 owner 承担。
var castForbidden = []string{
	"恋人", "女友", "男友", "老婆", "老公", "暧昧", "约会", "接吻", "告白",
	"小学", "初中生", "高中生", "未成年", "萝莉", "幼女",
	"自残", "自杀", "伤害", "虐待", "霸凌",
}

// CastGuard 单个人的内容护栏。
func CastGuard(m CastMember) error {
	blob := m.Name + " " + m.History + " " + m.Status + " " + m.Topics
	for _, bad := range castForbidden {
		if strings.Contains(blob, bad) {
			return fmt.Errorf("§2.3 护栏: %s 的关系描述命中禁词 %q", m.Name, bad)
		}
	}
	// 逐字复述原作: 超长 = 抄的。
	if n := len([]rune(m.History)); n > WorldFactMaxRunes {
		return fmt.Errorf("§2.3 护栏: %s 的共同历史 %d 字, 超过 %d —— 不要逐字复述原作文本", m.Name, n, WorldFactMaxRunes)
	}
	return nil
}

// ——— 落库 ———

// EnsureWorldBibleSchema 建表。
func EnsureWorldBibleSchema(db *sql.DB) error {
	_, err := db.Exec("CREATE TABLE IF NOT EXISTS convo_world_bible (" +
		"owner TEXT PRIMARY KEY, json TEXT NOT NULL DEFAULT '', updated_ts TEXT NOT NULL DEFAULT '')")
	return err
}

// SaveWorldBible 落库。**校验不过不许写** —— 半个世界观比没有更糟。
func (s *Store) SaveWorldBible(owner string, b WorldBible) error {
	b = NormalizeWorldBible(b)
	if err := ValidateWorldBible(b); err != nil {
		return err
	}
	b.UpdatedTS = clock.TodayTS()
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec("INSERT INTO convo_world_bible (owner, json, updated_ts) VALUES (?,?,?) "+
		"ON CONFLICT(owner) DO UPDATE SET json = excluded.json, updated_ts = excluded.updated_ts",
		owner, string(raw), b.UpdatedTS)
	return err
}

// LoadWorldBible 读。第二个返回值 = **有没有** (零值 ≠ 空世界观)。
func (s *Store) LoadWorldBible(owner string) (WorldBible, bool, error) {
	var raw string
	err := s.DB.QueryRow("SELECT json FROM convo_world_bible WHERE owner = ?", owner).Scan(&raw)
	if err == sql.ErrNoRows {
		return WorldBible{}, false, nil
	}
	if err != nil {
		return WorldBible{}, false, err
	}
	var b WorldBible
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		return WorldBible{}, false, err
	}
	return b, true, nil
}

// ——— 注入 ———

// WorldPrompt 把世界观渲染成注入文本。
//
// ⚠️ **L3 单独成段且放在最后** —— L1/L2 是布景, 只有 L3 是「现在」。
// 混在一起写, 模型会拿布景当当下说 (「我现在在净善宫」), 而它其实在家。
func WorldPrompt(b WorldBible) string {
	var sb strings.Builder
	sb.WriteString("【你的来处】\n")
	if s := strings.TrimSpace(b.Self); s != "" {
		sb.WriteString("你是: ")
		sb.WriteString(s)
		sb.WriteString("\n")
	}
	for _, grp := range []struct {
		name string
		list []WorldFact
	}{
		{"地点", b.Setting.Places},
		{"组织", b.Setting.Orgs},
		{"规则", b.Setting.Rules},
		{"大事", b.Setting.Timeline},
	} {
		var parts []string
		for _, f := range grp.list {
			t := strings.TrimSpace(f.Text)
			if t == "" {
				continue
			}
			if f.Unsure() {
				// ⚠️ 现编的必须**自曝**, 否则模型会把它当原作设定讲出去,
				// 而群里必有懂的人 (§2.3)。
				t += " (这条是你自己编的, 别人问起别当成原作设定)"
			}
			parts = append(parts, t)
		}
		if len(parts) > 0 {
			sb.WriteString(grp.name)
			sb.WriteString(": ")
			sb.WriteString(strings.Join(parts, "; "))
			sb.WriteString("\n")
		}
	}
	if len(b.Cast) > 0 {
		sb.WriteString("\n【你生活里的人】\n")
		for _, m := range b.Cast {
			sb.WriteString("- ")
			sb.WriteString(m.Name)
			sb.WriteString(" (")
			sb.WriteString(m.Relation.Label())
			sb.WriteString(")")
			if m.History != "" {
				sb.WriteString(": ")
				sb.WriteString(m.History)
			}
			sb.WriteString("\n")
		}
		// ─────────────────────────────────────────────────────────
		// 护栏的措辞 —— **这一版是被实测纠正过的**
		//
		// 上一版写的是「也别声称他们在线」。那句话被读成了**"不许提他们"** ——
		// 结果它从来不说自己的生活: 问它今天干嘛, 它说"闭关看书";
		// 问它最近怎么样, 它说"就那样"。**配角全成了摆设。**
		//
		// ⚠️ 而配角的**全部意义**就是让它的生活有内容 ——
		// "定稿配角"与"动态配角"是**服务世界观**的, 不是需要被保护起来的。
		//
		// 真正该拦的只有两件:
		//   ① 别说成**群友** (那会让它在群里认错人);
		//   ② 别拿他们去**评价群里的人** (那会变成借刀伤人)。
		// **提他们本身不但不该拦, 还该鼓励。**
		// ─────────────────────────────────────────────────────────
		sb.WriteString("⚠️ 这些人**不在这个群里** —— 别把他们说成群友, 也别拿他们去评价群里的人。\n")
		sb.WriteString("**但你随时可以提他们** —— 他们就是你自己的生活。\n")
		sb.WriteString("别人问你今天干嘛了、最近怎么样, 你答的本来就该是你**跟他们之间的事**, 不用绕开。\n")
		sb.WriteString("⚠️ 只报「我在看书」「还行」「就那样」= **你没有生活** —— 那比说错话假得多。\n")
	}
	var now []string
	for _, m := range b.Cast {
		if s := strings.TrimSpace(m.Status); s != "" {
			now = append(now, m.Name+" 现在"+s)
		}
	}
	if len(now) > 0 {
		sb.WriteString("\n【他们现在】\n")
		sb.WriteString(strings.Join(now, "; "))
		sb.WriteString("\n")
	}
	return sb.String()
}

// ——— 接线: L2 关系层 → §3.7 夜伴名册 ———

// NightPeersFromCast 把关系层变成夜间多 AI 社交的名册 (§3.4.1: 群友是「别人家的 AI」)。
//
// ⚠️ 这是 §2.3 与 §3.7 之间**唯一**的那根线。
// 没有它, 夜伴名册只能手工填 —— 而手工填的名册和角色没有任何关系,
// 于是「多 AI 社交」变成一群陌生人陪它演戏。
//
// ⚠️ 关系类型映射到权重: 同伴/家人最亲 (常聊), 对手次之 (有戏), 师长最少 (不常打扰)。
// castUIN 给一个世界观配角一个**稳定的负数 UIN**。
//
// ⚠️ 2026-10-15 修: 原来 NightPeersFromCast **不设 UIN**(零值 0), 而 nightSessions 拿
// "night:dm:" + itoa(int(pr.UIN)) 当场景键 —— 于是**所有配角的私聊塌成同一个 night:dm:0**。
// 生产库里的实证: 4 条私聊记忆的 label 全是 night:dm:0(§3.7.2「多个私聊, 成对私密」在存储层已经失效)。
//
// 取**负数**不是随手: QQ 号是正数, 负数一眼就能看出「这不是真人, 是世界观的配角」。
// 用 FNV-1a 而不是 map 遍历 —— 场景键必须在**重启之后仍然一致**,
// 否则同一段私聊会被切成两半, 而那是静默的。
func castUIN(name string) int64 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(strings.TrimSpace(name)))
	return -int64(1 + h.Sum32()%1_000_000_000)
}

func NightPeersFromCast(b WorldBible) []NightPeer {
	// ⚠️ §3.6.5c: 只用**当下生活层** —— 已经搬走的人不该出现在今晚的群里。
	var out []NightPeer
	for _, m := range ActiveCast(b) {
		if strings.TrimSpace(m.Name) == "" {
			continue
		}
		brief := m.Status
		if brief == "" {
			brief = m.History
		}
		out = append(out, NightPeer{
			UIN:    castUIN(m.Name),
			Name:   m.Name,
			Brief:  brief,
			Weight: relationWeight(m.Relation),
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Weight > out[j].Weight })
	return out
}

// relationWeight 关系类型 → 夜间社交权重。
func relationWeight(r RelationKind) float64 {
	switch r {
	case RelationCompanion:
		return 1.0
	case RelationFamily:
		return 0.9
	case RelationRival:
		return 0.7
	case RelationMentor:
		return 0.4
	}
	return 0.0 // fail-closed: 不认识的关系不参与夜间社交
}
