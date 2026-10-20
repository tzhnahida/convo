package convo

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tzhnahida/convo/internal/clock"
	"github.com/tzhnahida/convo/internal/log"
)

// ——— §6.10 用户区分 + §9 群与成员的数据分区 (叶子模块) ———
//
// 本文件只做三件事, 三件都必须是**物理保证**, 而不是"记得过滤":
//
//  1. 群公共层记忆表 (convo_memory_scene) **没有 uin 列** —— 没有列,
//     就不存在"把某个人的东西渲染进群上下文"的代码路径。
//  2. 关系层记忆表 (convo_memory_person) 的 uin **NOT NULL 且非空** ——
//     漏传主体的检索返回**空集**(安静), 不返回所有人(泄漏)。
//  3. 个人私域**没有表** —— 私密内容在**写入口**就被丢弃, 任何读取路径都查不出来。
//
// 为什么用物理保证: §10.1 #1「prompt 是软约束, **按主体分区才是物理保证**」。
// "写进库再 WHERE 掉"(过滤) 与 "根本没查出来"(合规) 是两件事, 本文件只承诺后者。
//
// ⚠️ A3 裁定 (不定向): 全文**没有一处**按群规模分支 ——
// 没有"典型群", 没有 len(members)>K, 没有 isBigGroup, 没有"一般会有 N 个活跃成员"。
// N=2 (只有 owner + 它) 与 N=500 走**同一套公式、同一张表**。
//
// 接线不在这里: 段序 (§8.1) / TickInput 新字段 / convo_messages 的迁移与读取改造
// 由主控统一做。本文件只提供类型、表、分区读取方法与 §6.10 的判据。

// ——— §6.10 问题① 的落点: 身份锚是 uin ———

// 场景形态。只区分"有没有第三方在场", 与人数无关。
const (
	SceneKindGroup   = "group"
	SceneKindPrivate = "private"
)

// 场景阶段 (§5.6)。热力上限只由它决定 —— **不由人数决定**。
const (
	StageStranger = "stranger"
	StageAcquaint = "acquaintance"
	StageFamiliar = "familiar"
	StageIntimate = "intimate"
)

// 平台角色 (§6.10.3 问题③)。
//
// ⚠️ 这与**发言人的**平台角色**同名不同物** (§6.10.1 的 owner|admin|member 说的是说话的人)。
// 规格 §6.10.3 专门点过这条: 代码里必须分开命名, 否则一定串。
// 本文件里的这组常量**只**指 convo 自己在该场景的角色, 库列名是 convo_scene.self_role。
const (
	RoleOwner  = "owner"
	RoleAdmin  = "admin"
	RoleMember = "member"
)

// 敏感度 (§6.4.2 三级记忆)。
//
//	relation = 关系层, 可落 convo_memory_person (中短期 + 衰减)
//	private  = 个人私域, **没有表** —— 只在当前上下文存活, 落库前就丢
//
// 这条硬规则的优先级**高于一切隐私开关**: 没有 if enabled, 没有配置项能关掉它。
const (
	// ⚠️ 三级里**只有两级**曾经存在过。§7.3 的第一维是「公共 / 关系 / 私域」,
	// 而「公共」这一级在代码里从来没有 —— 于是"能不能跨人用"这件事
	// 实际上只有一个答案: 关系级**绑场景**, 公共级**不存在**。
	//
	// 补上它, 但**默认仍然是 relation**: 让它可跨人必须有人**显式声明**。
	// 反过来 (默认 public) 会让每一条记忆都长出腿。
	SensitivityPublic   = "public"   // 公共: 可跨人 / 可跨场景
	SensitivityRelation = "relation" // 关系: 只在本场景可用 (默认)
	SensitivityPrivate  = "private"  // 私域: **没有表**, 写入口就丢
)

// 场景键 (§5.6): 'group:<gid>' | 'private:<uin>'。**私聊也是一个场景**。
func SceneKeyGroup(groupID string) string { return SceneKindGroup + ":" + strings.TrimSpace(groupID) }
func SceneKeyPrivate(uin string) string   { return SceneKindPrivate + ":" + strings.TrimSpace(uin) }

// SceneKindOf 从场景键推断形态。认不出来的键**按群处理** ——
// 群判据的默认落点是沉默 (④), 那是保守的一侧; 反过来把群误判成私聊会直接导致误接话。
func SceneKindOf(sceneKey string) string {
	if strings.HasPrefix(sceneKey, SceneKindPrivate+":") {
		return SceneKindPrivate
	}
	if strings.HasPrefix(sceneKey, SceneKindGroup+":") {
		return SceneKindGroup
	}
	return SceneKindGroup
}

// ScenePeerOf 取场景键的对方标识 (群号 / 对端 uin)。
func ScenePeerOf(sceneKey string) string {
	if i := strings.Index(sceneKey, ":"); i >= 0 {
		return sceneKey[i+1:]
	}
	return ""
}

// ——— 配置项 (§2.1 / §2.3) ———
//
// ⚠️ 这是**新**类型, 不是 Config 的替代: 主控接线时把它嵌进 Config
// (例如 Config.Group GroupConfig `yaml:"group"`)。本文件不认识 Config, 免得两处改同一份代码。
//
// 零值纪律 (照抄 config.go 的既有机制, 规格 §0):
//   - bool 靠 overlay 天然满足 "零值 = 未提供" —— DefaultConfig 先铺底, yaml 再覆盖;
//   - 数值必须显式兜底 (NormalizeGroupConfig 里的 <=0 → 默认)。
type GroupConfig struct {
	// Enabled 群通路总开关。零值 false = 群通路未启用 → 走纯私聊, 与升级前逐字节相同。
	Enabled bool `yaml:"enabled"`
	// Whitelist 群号白名单 (§6.3 硬门槛)。
	//
	// ⚠️ **空 = 没有任何群被启用** (不接群事件, 仅记录)。这是纪律 #2 的一处**有意例外**:
	// 硬门槛的方向必须 fail-closed, "空 = 接所有群" 等于把硬门槛的默认值设成全开。
	// 唯一真源是这里 —— convo_scene 表**不存 enabled 列**, 避免双真源。
	Whitelist []string `yaml:"whitelist"`
	// SelfRole 它在该群的平台角色。"" = 未提供 → 走平台查询 (get_group_member_info);
	// 查询失败 → 按 member 最保守档 (见 EffectiveSelfRole)。
	SelfRole string `yaml:"self_role"`
	// SelfAliases 「被点名」时认的额外名字。空 = 只用平台昵称 + 群名片 + self_qq。
	SelfAliases []string `yaml:"self_aliases"`
	// ThreadWindowMin 无 @ 时, 距上一条消息多少分钟内仍承认线程归属。<=0 → 5。
	ThreadWindowMin int `yaml:"thread_window_min"`
	// ReplyWhenAtOthers @ 别人时是否也接。零值 false = **不接** (§6.10.2 ② 的默认)。
	ReplyWhenAtOthers bool `yaml:"reply_when_at_others"`
	// DefaultAtRecipient 帧未指定 to 时默认是否 @ 对方。零值 false = **默认不 @** (§6.10.4)。
	DefaultAtRecipient bool `yaml:"default_at_recipient"`
	// HistorySubjects 多主体发言流的条数上限。<=0 → 12。
	//
	// ⚠️ **按条数, 不按人数** (R36): 同一个上限在 N=2 与 N=500 下是同一个数。
	HistorySubjects int `yaml:"history_subjects"`
}

// 群配置的默认档位 (§2.3 表)。幅度小、档位化 —— 不做连续公式。
const (
	DefaultThreadWindowMin = 5
	DefaultHistorySubjects = 12
)

// DefaultGroupConfig 与 DefaultConfig() 同一约定: 先铺底, 再由 yaml 覆盖。
func DefaultGroupConfig() GroupConfig {
	return GroupConfig{
		Enabled:         false,
		Whitelist:       nil,
		SelfRole:        "",
		SelfAliases:     nil,
		ThreadWindowMin: DefaultThreadWindowMin,
		HistorySubjects: DefaultHistorySubjects,
	}
}

// NormalizeGroupConfig 数值兜底 (bool 不动 —— overlay 已保证零值语义)。幂等。
func NormalizeGroupConfig(cfg GroupConfig) GroupConfig {
	if cfg.ThreadWindowMin <= 0 {
		cfg.ThreadWindowMin = DefaultThreadWindowMin
	}
	if cfg.HistorySubjects <= 0 {
		cfg.HistorySubjects = DefaultHistorySubjects
	}
	return cfg
}

// WhitelistAllows 群号是否在白名单内。**空白名单 = 一个都不允许** (fail-closed)。
func WhitelistAllows(cfg GroupConfig, groupID string) bool {
	if !cfg.Enabled {
		return false
	}
	gid := strings.TrimSpace(groupID)
	if gid == "" {
		return false
	}
	for _, w := range cfg.Whitelist {
		if strings.TrimSpace(w) == gid {
			return true
		}
	}
	return false
}

// SceneAllowed 场景级启用判定。
//
// 私聊**不受群白名单约束** —— 白名单管的是"接不接某个群的事件",
// 而私聊通路是升级前就有的, 拿它去卡私聊等于给旧路径加了一道新门槛 (纪律 #1 的反面)。
func SceneAllowed(cfg GroupConfig, sceneKey string) bool {
	if SceneKindOf(sceneKey) != SceneKindGroup {
		return true
	}
	return WhitelistAllows(cfg, ScenePeerOf(sceneKey))
}

// EffectiveSelfRole 它在该场景的有效平台角色 (§6.10.3 问题③)。
//
// 优先级: 配置显式 → 库里的 self_role (平台成员信息) → member。
// ⚠️ 取不到一律落 **member 最保守档**: 未取到 ≠ admin, 更 ≠ owner。
func EffectiveSelfRole(cfgRole, sceneRole string) string {
	if r := normalizeRole(cfgRole); r != "" {
		return r
	}
	if r := normalizeRole(sceneRole); r != "" {
		return r
	}
	return RoleMember
}

// normalizeRole 归一化平台角色; 认不出来 → "" (交给下一档, 最终落 member)。
func normalizeRole(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case RoleOwner, "群主":
		return RoleOwner
	case RoleAdmin, "管理员":
		return RoleAdmin
	case RoleMember, "群员", "成员":
		return RoleMember
	default:
		return ""
	}
}

// ——— 场景与成员 ———

// Scene 一个场景 = 群 或 私聊 (§5.6: 私聊 = 一个特殊场景)。
type Scene struct {
	SceneKey    string  `json:"scene_key"`
	Kind        string  `json:"kind"`
	Platform    string  `json:"platform"`
	GroupID     string  `json:"group_id"`
	PeerUIN     string  `json:"peer_uin"`
	SelfRole    string  `json:"self_role"`
	Stage       string  `json:"stage"`
	Intimacy    float64 `json:"intimacy"`
	FirstSeenTS string  `json:"first_seen_ts"`
	LastMsgTS   string  `json:"last_msg_ts"`
	UpdatedTS   string  `json:"updated_ts"`
}

// SceneMember 人 × 场景 (§5.6)。
//
// ⚠️ 主键是 (scene_key, uin) —— **身份锚是 uin**。
// DisplayName / GroupCard **只做显示, 不参与任何身份判定** (§6.10.2):
// 群名片可以随便改, 两个人也可以叫同一个名字; 拿名字当归属键就是 R10 那个 bug。
type SceneMember struct {
	SceneKey    string   `json:"scene_key"`
	UIN         string   `json:"uin"`
	DisplayName string   `json:"display_name"` // 显示用
	GroupCard   string   `json:"group_card"`   // 显示用
	Stage       string   `json:"stage"`
	Affinity    float64  `json:"affinity"`
	AddressAs   string   `json:"address_as"` // 称呼: 按场景各存一份, 两边不许串
	Taboo       []string `json:"taboo"`      // 禁区: 这个场景别拿 X 开玩笑
	FirstSeenTS string   `json:"first_seen_ts"`
	UpdatedTS   string   `json:"updated_ts"`
}

const sceneTSLayout = "2006-01-02 15:04:05"

// ⚠️ 这里原来有一个 `SaveScene`(全字段 upsert) —— **已删**。
//
// ⚠️ 它是一颗**有记录的地雷**: 下面 `EnsureScene` 的注释逐字写着
// 「为什么**不直接用** `SaveScene`: 它的 `ON CONFLICT DO UPDATE` 会把
//   `self_role` 与 `stage` 用**传入值**覆盖, 而它们在函数里被默认成 `""` / `stranger`」
// —— 而群消息那条路是**每一轮**都要登记的。
//
// ⚠️ 它只被**测试**当成播种工具用(4 处)。**而接上生产是危险的** ——
// 那正是 `EnsureScene` 存在的理由。所以: 删掉它, 把播种能力搬进测试文件。

// EnsureScene 登记一个场景 —— **只插入, 绝不覆盖既有字段**。
//
// ⚠️ **为什么不直接用 `SaveScene`**: 它的 `ON CONFLICT DO UPDATE` 会把
// `self_role` 与 `stage` 用**传入值**覆盖, 而它在函数里被默认成 `""` / `stranger`。
// 群消息那条路是**每一轮**都要登记的, 于是:
//
//	· `stage` 会被每轮重置回"陌生人" —— 而 `conflictwire.go` 正读着它;
//	· `self_role` 会被每轮抹成 "" —— 谁将来在管理页设了角色, 下一秒就没了。
//
// ⚠️ 这正是本项目第一条纪律的反面: **零值 = 未提供 ≠ 把它清空**。
// `SaveScene` 是"全量 upsert"(给管理页那种显式写入用),
// 而"每轮路过顺手登记一下"需要的是**另一条更弱的语义** —— 就是这一个。
//
// ⚠️ `last_msg_ts` 是唯一跟着走的字段: 它本来就是"这个场景最近有动静"的事实。
func (s *Store) EnsureScene(sceneKey string) error {
	k := strings.TrimSpace(sceneKey)
	if k == "" {
		return fmt.Errorf("scene: scene_key 为空")
	}
	now := clock.TodayTS()
	// ⚠️ 新行的 stage 落在**最保守档**(陌生人), self_role 留空
	// (留空 → `EffectiveSelfRole` 落 member 档, 见 scene.go 的注释)。
	// ⚠️ `DO UPDATE SET` 里**只有 last_msg_ts 一个字段** ——
	// 别的在冲突时原样留着。这就是它比 `SaveScene` 安全的全部原因。
	_, err := s.DB.Exec(`INSERT INTO convo_scene
		(scene_key, kind, platform, group_id, peer_uin, self_role, stage, intimacy, first_seen_ts, last_msg_ts, updated_ts)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(scene_key) DO UPDATE SET last_msg_ts=excluded.last_msg_ts`,
		k, SceneKindOf(k), "qq", ScenePeerOf(k), "", "", StageStranger, 0, now, now, now)
	return err
}

// LoadScene 读场景行。**没有行 = (nil, nil)** —— 那是"这个场景还没登记", 不是故障。
// 调用方要区分这两者 (纪律 #3 vs §6.10.2 ④), 见 ResolveAddressee。
func (s *Store) LoadScene(sceneKey string) (*Scene, error) {
	var sc Scene
	err := s.DB.QueryRow(`SELECT scene_key, COALESCE(kind,''), COALESCE(platform,''), COALESCE(group_id,''),
		COALESCE(peer_uin,''), COALESCE(self_role,''), COALESCE(stage,'stranger'), COALESCE(intimacy,0),
		COALESCE(first_seen_ts,''), COALESCE(last_msg_ts,''), COALESCE(updated_ts,'')
		FROM convo_scene WHERE scene_key=?`, sceneKey).
		Scan(&sc.SceneKey, &sc.Kind, &sc.Platform, &sc.GroupID, &sc.PeerUIN, &sc.SelfRole,
			&sc.Stage, &sc.Intimacy, &sc.FirstSeenTS, &sc.LastMsgTS, &sc.UpdatedTS)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &sc, nil
}

// ObserveMember 记一次出现, 返回**这是不是本场景第一次见到这个 uin**。
//
// §6.6 新人识别**按 uin, 不按昵称** (R31): 判定只看 (scene_key, uin) 这一对在不在,
// 老成员改了名依然是老成员 —— 否则改名就会重走一遍"不接梗不点名"的新人待遇。
//
// 已存在时**只更新显示字段**: 称呼/禁区/阶段是沉淀下来的, 不能被一次改名冲掉。
func (s *Store) ObserveMember(sceneKey, uin, displayName, groupCard string) (bool, error) {
	sceneKey, uin = strings.TrimSpace(sceneKey), strings.TrimSpace(uin)
	if sceneKey == "" || uin == "" {
		return false, fmt.Errorf("scene member: scene_key/uin 不能为空 (身份锚是 uin, §6.10.2)")
	}
	now := clock.TodayTS()
	res, err := s.DB.Exec(`INSERT OR IGNORE INTO convo_scene_member
		(scene_key, uin, display_name, group_card, stage, affinity, address_as, taboo_json, first_seen_ts, updated_ts)
		VALUES(?,?,?,?,?,0,'','[]',?,?)`,
		sceneKey, uin, displayName, groupCard, StageStranger, now, now)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return true, nil
	}
	// ⚠️ **零值 = 未提供, 不是"把它清空"。**
	//
	// 原来是无条件 `SET display_name=?, group_card=?` —— 于是**任何一次没带名字的调用
	// 都会把这个人的昵称抹掉**。而这条路是**每一轮**都要走的,
	// 也就是说: 谁在某一轮带上了名字, 下一轮就被清掉了。
	//
	// ⚠️ 与 §2.44 的 `EnsureScene` 是**同一条纪律的两处**:
	// 那里是"别把 stage/self_role 抹回默认", 这里是"别把昵称抹掉"。
	_, err = s.DB.Exec(`UPDATE convo_scene_member SET
		display_name = CASE WHEN ? <> '' THEN ? ELSE display_name END,
		group_card   = CASE WHEN ? <> '' THEN ? ELSE group_card END,
		updated_ts=?
		WHERE scene_key=? AND uin=?`,
		displayName, displayName, groupCard, groupCard, now, sceneKey, uin)
	return false, err
}

// rowScanner *sql.Row 与 *sql.Rows 共用的最小接口。
type rowScanner interface {
	Scan(dest ...interface{}) error
}

// SceneMemberByUIN 按 uin 取成员 —— 唯一合法的身份查询入口 (**没有"按名字查"的入口**)。
func (s *Store) SceneMemberByUIN(sceneKey, uin string) (*SceneMember, error) {
	m, err := scanSceneMember(s.DB.QueryRow(`SELECT scene_key, uin, COALESCE(display_name,''), COALESCE(group_card,''),
		COALESCE(stage,'stranger'), COALESCE(affinity,0), COALESCE(address_as,''), COALESCE(taboo_json,'[]'),
		COALESCE(first_seen_ts,''), COALESCE(updated_ts,'')
		FROM convo_scene_member WHERE scene_key=? AND uin=?`, sceneKey, uin))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// SceneMembers 本场景的成员名单 (确定性全序: first_seen_ts, uin —— 提示词前缀逐字节稳定)。
func (s *Store) SceneMembers(sceneKey string) ([]SceneMember, error) {
	rows, err := s.DB.Query(`SELECT scene_key, uin, COALESCE(display_name,''), COALESCE(group_card,''),
		COALESCE(stage,'stranger'), COALESCE(affinity,0), COALESCE(address_as,''), COALESCE(taboo_json,'[]'),
		COALESCE(first_seen_ts,''), COALESCE(updated_ts,'')
		FROM convo_scene_member WHERE scene_key=? ORDER BY first_seen_ts ASC, uin ASC`, sceneKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SceneMember
	for rows.Next() {
		m, err := scanSceneMember(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func scanSceneMember(r rowScanner) (SceneMember, error) {
	var m SceneMember
	var taboo string
	if err := r.Scan(&m.SceneKey, &m.UIN, &m.DisplayName, &m.GroupCard, &m.Stage, &m.Affinity,
		&m.AddressAs, &taboo, &m.FirstSeenTS, &m.UpdatedTS); err != nil {
		return m, err
	}
	m.Taboo = decodeTaboo(taboo)
	return m, nil
}

func decodeTaboo(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[]" || raw == "null" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil // 脏数据 → 当没有禁区, 不炸
	}
	return out
}

func encodeTaboo(t []string) string {
	if len(t) == 0 {
		return "[]"
	}
	b, err := json.Marshal(t)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// SetMemberAddress 写称呼。**按 (scene_key, uin) 各存一份** (§5.6:875):
// A 群叫外号、B 群必须叫全名, 两边不许串 (R32)。
//
// ⚠️ 成员不存在时**报错, 不静默成功** —— P7 的教训: 静默的不是数据库, 是调用方。
func (s *Store) SetMemberAddress(sceneKey, uin, addressAs string) error {
	return s.updateMember(sceneKey, uin, "address_as=?", addressAs)
}

// SetMemberTaboo 写禁区 (这个场景别拿 X 开玩笑)。
func (s *Store) SetMemberTaboo(sceneKey, uin string, taboo []string) error {
	return s.updateMember(sceneKey, uin, "taboo_json=?", encodeTaboo(taboo))
}

// SetMemberStage 写 person×scene 阶段 (表达与偏向用)。
func (s *Store) SetMemberStage(sceneKey, uin, stage string) error {
	return s.updateMember(sceneKey, uin, "stage=?", stage)
}

func (s *Store) updateMember(sceneKey, uin, setClause string, arg interface{}) error {
	sceneKey, uin = strings.TrimSpace(sceneKey), strings.TrimSpace(uin)
	if sceneKey == "" || uin == "" {
		return fmt.Errorf("scene member: scene_key/uin 不能为空 (身份锚是 uin)")
	}
	res, err := s.DB.Exec("UPDATE convo_scene_member SET "+setClause+", updated_ts=? WHERE scene_key=? AND uin=?",
		arg, clock.TodayTS(), sceneKey, uin)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("scene member: (%s, %s) 不存在 —— 先 ObserveMember 再写沉淀字段", sceneKey, uin)
	}
	return nil
}

// IsNewMember 该 uin 在本场景是否仍在"新人窗口"内。**现算, 不落库**。
//
// 规格 §3 的 DDL 里有一列 is_new, 这里**故意不设**: 一个粘住的布尔量,
// 谁在什么时候把它清掉? 它其实是 first_seen_ts 与 now 的纯函数
// (与 convo_body_state 的 severity/until 同族 —— 纪律 #4: 派生量不落库)。
//
// 从没见过的 uin → 新人。时间戳读不出来 → 也当新人 (保守: 不拿老成员的架子;
// 这一条不在"说话/不说话"的通路上, 不适用纪律 #2 的倒向)。
// NewMemberWindow 一个人到多新还算"新人" (§6.10.5 陌生人档)。
//
// ⚠️ 设计稿没给数字 —— 它给的是行为:「不接梗、不点名、只给中性反应; **不主动搭话**」。
// 取一天: 短了第一句话刚说完就"熟"了, 长了它会对一个天天见的人一直端着。
//
// ⚠️ 这是**生活上限那一类**, 只判在代码里, **绝不告诉模型**(§4.6.1)。
const NewMemberWindow = 24 * time.Hour

func (s *Store) IsNewMember(sceneKey, uin string, now time.Time, window time.Duration) (bool, error) {
	// ⚠️⚠️ **先问一个更前面的问题: 这个场景本身是不是刚建立?**
	//
	// `first_seen_ts` 是**它自己第一次见到这个人**的时刻 ✓ —— 而不是"这个人什么时候
	// 进群的" ✗。两者在**它刚上线 / 刚换到一个群**时会完全分开:
	// 那一刻它第一次见到**所有人** ✓, 于是**每个人都落在 24h 窗口内** ✗,
	// 于是整群沉默一整天 ✗✗。
	//
	// 2026-10-16 生产实测: 群里 12:17 之后**四小时一条都没回** ✓,
	// 而每一个裁决都是同一句「新成员 — 不主动搭话」 ✓ ——
	// 成员表里 7 个人的 `first_seen_ts` **全都在当天** ✓。
	//
	// ⚠️ 而判据不是"缩短窗口"(那是调数 ✓), 是**这个词在这里没有区分力**:
	// 如果**整个场景**比窗口还年轻, 那"新人"与"老人"的分别根本不存在 ✓ ——
	// 那不是"来了一群新人", 是"**我们还没认识过任何人**" ✓。
	//
	// ⚠️ 而这条修法不是我发明的 —— 它**就是下面那段注释自己写的理由**:
	//
	//   「返回 true 就意味着『数据坏了 → 它忽然不说话』,
	//     而那是最难查的一类现象(没人会想到去怀疑一个时间戳格式)」
	//
	// ⇒ 作者把这条理由用在了**时间戳解析失败**上 ✓, 而漏了**同样的一个洞** ✓。
	// ⇒ 所以这里同样**往"正常说话"倒** ✓。
	if sceneFirst := s.sceneFirstSeenTS(sceneKey); !sceneFirst.IsZero() {
		if now.Sub(sceneFirst) <= window {
			return false, nil
		}
	}

	var first string
	err := s.DB.QueryRow(`SELECT COALESCE(first_seen_ts,'') FROM convo_scene_member
		WHERE scene_key=? AND uin=?`, sceneKey, uin).Scan(&first)
	if err == sql.ErrNoRows {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	t, perr := time.ParseInLocation(sceneTSLayout, first, time.Local)
	if perr != nil {
		// ⚠️ **解析失败 → 算"不是新人"**, 也就是**往正常说话那一侧倒**。
		//
		// 原来这里返回 true。在它只当读数的时候无所谓, 但它现在是
		// **「不主动搭话」那道闸门** —— 返回 true 就意味着"数据坏了 → 它忽然不说话",
		// 而那是最难查的一类现象(没人会想到去怀疑一个时间戳格式)。
		//
		// ⚠️ 与全包同一条纪律: **失败往"正常说话"倒**。
		return false, nil
	}
	return now.Sub(t) <= window, nil
}

// ——— 三级记忆: 类型与写入口 ———

// SceneMemory **群公共层**记忆 (梗 / 公共事件 / 群玩笑 / 群共识)。
//
// ⚠️ 这张表**没有 uin 列**。OriginUIN 是**血缘**, 只给审计与撤回用,
// 注入渲染器的固定列清单里永远没有它 —— 没有列/没有查, 就不存在渲染出来的可能。
type SceneMemory struct {
	MemID       string  `json:"mem_id"`
	SceneKey    string  `json:"scene_key"`
	K           string  `json:"k"`
	V           string  `json:"v"`
	Label       string  `json:"label"`
	Kind        string  `json:"kind"` // meme | event | fact | relation
	Confidence  float64 `json:"confidence"`
	OriginUIN   string  `json:"origin_uin"` // 血缘: 审计/撤回; **注入不读**
	State       string  `json:"state"`
	CreatedTS   string  `json:"created_ts"`
	UpdatedTS   string  `json:"updated_ts"`
	LastInject  string  `json:"last_injected_ts"`
	InjectCount int     `json:"inject_count"`
	LastAccess  string  `json:"last_access_ts"`
	AccessCount int     `json:"access_count"`
}

// PersonMemory **关系层**记忆 (挂在人身上)。
//
// uin 是归属主体; scene_key 既是来源场景, 也是"不许跨场景讲"的依据。
// GlobalFact=1 = 对该人的**稳定事实**: 只在他**本人在场**时可用 (R7)。
type PersonMemory struct {
	MemID       string  `json:"mem_id"`
	UIN         string  `json:"uin"`
	SceneKey    string  `json:"scene_key"`
	GlobalFact  bool    `json:"global_fact"`
	K           string  `json:"k"`
	V           string  `json:"v"`
	Label       string  `json:"label"`
	Kind        string  `json:"kind"`
	Source      string  `json:"source"`
	Sensitivity string  `json:"sensitivity"`
	Confidence  float64 `json:"confidence"`
	State       string  `json:"state"`
	DueTS       string  `json:"due_ts"`
	Status      string  `json:"status"`
	CreatedTS   string  `json:"created_ts"`
	UpdatedTS   string  `json:"updated_ts"`
	LastInject  string  `json:"last_injected_ts"`
	InjectCount int     `json:"inject_count"`
	LastAccess  string  `json:"last_access_ts"`
	AccessCount int     `json:"access_count"`
}

// scenePrivateMarkers 兜底词表 (高敏类别: 精神/医疗/性/财务/凭据/自伤)。
//
// ⚠️ 词表必须**小而准**: 覆盖不到的交给主判据 (declared), 宁可漏在模型那一侧,
// 也不许把日常闲聊整片判成私密 —— 那会让它连"我今天吃药了"都不敢记, 是另一头的事故。
var scenePrivateMarkers = []string{
	"抗抑郁", "抑郁症", "焦虑症", "精神科", "心理医生", "自残", "自杀",
	"化疗", "确诊", "性病", "艾滋", "流产", "堕胎",
	"密码", "验证码", "身份证", "银行卡", "裸聊", "出轨", "约炮",
	"月薪", "工资条", "存款", "欠债", "网贷",
}

// ClassifySensitivity 敏感度判定 —— 判据**无 LLM 可算** (纪律 #7)。
//
// 主判据是提取器逐条给出的 declared (与 source 字段同一约定: 模型必须明说);
// 关键词表是**兜底网**: 模型漏标时, 高敏内容依然不许落库。两网**取严**。
//
// 认不出来的 declared 值 → relation (零值 = 未提供, 不是极值);
// 但已知的 private 别名 (大小写/空格/中文) 一律归 private —— fail-closed。
func ClassifySensitivity(declared, text string) string {
	switch strings.ToLower(strings.TrimSpace(declared)) {
	case SensitivityPrivate, "secret", "sensitive", "私密", "隐私":
		return SensitivityPrivate
	}
	low := strings.ToLower(text)
	for _, w := range scenePrivateMarkers {
		if strings.Contains(low, strings.ToLower(w)) {
			return SensitivityPrivate
		}
	}
	// ⚠️ 顺序是**承载的**: 私域词表必须**先于**这里说话。
	// 把 public 分支提到 switch 的第一位 (我第一版就是这么写的) 会让
	// 「显式声明 public」压过词表 —— 模型一旦把私事标成 public, 它就漏出去了。
	// 保守的一方**最后说话**。
	switch strings.ToLower(strings.TrimSpace(declared)) {
	case SensitivityPublic, "公共", "公开":
		return SensitivityPublic
	}
	return SensitivityRelation
}

// SaveSceneMemory 群公共层写入口。
//
// 返回 (false, nil) = **刻意丢弃**(私密内容), 不是故障 —— 与 A6 硬拒同族:
// 调用方静默处理, 不计入失败率。返回 error 才是真的坏了。
func (s *Store) SaveSceneMemory(m SceneMemory) (bool, error) {
	if strings.TrimSpace(m.MemID) == "" {
		return false, fmt.Errorf("scene memory: mem_id 为空")
	}
	if strings.TrimSpace(m.SceneKey) == "" {
		return false, fmt.Errorf("scene memory: scene_key 为空 (群公共层必须知道是哪个群)")
	}
	if ClassifySensitivity("", m.K+" "+m.V+" "+m.Label) == SensitivityPrivate {
		return false, nil // 个人私域**没有表**: 连群公共层也不许落
	}
	if m.State == "" {
		m.State = "active"
	}
	if m.Kind == "" {
		m.Kind = "meme"
	}
	if m.Confidence == 0 {
		m.Confidence = 0.8
	}
	now := clock.TodayTS()
	if m.CreatedTS == "" {
		m.CreatedTS = now
	}
	if m.UpdatedTS == "" {
		m.UpdatedTS = now
	}
	// origin_uin 只在插入时写: 血缘记的是**谁先说的**, 后续同一 mem_id 的更新不许改写它。
	_, err := s.DB.Exec(`INSERT INTO convo_memory_scene
		(mem_id, scene_key, k, v, label, kind, confidence, origin_uin, state, created_ts, updated_ts)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)
		-- ★ 冲突维度是 **(场景, mem_id)** —— 同一个 id 出现在两个群里是**两条记忆**,
		--   不是一条被改写的记忆。写成 mem_id 会让 B 群改写 A 群那行却保留 A 的 scene_key。
		ON CONFLICT(scene_key, mem_id) DO UPDATE SET
			k=excluded.k, v=excluded.v, label=excluded.label, kind=excluded.kind,
			confidence=excluded.confidence, state=excluded.state, updated_ts=excluded.updated_ts`,
		m.MemID, m.SceneKey, m.K, m.V, m.Label, m.Kind, m.Confidence, m.OriginUIN, m.State, m.CreatedTS, m.UpdatedTS)
	if err != nil {
		return false, err
	}
	return true, nil
}

// SavePersonMemory 关系层写入口 —— **禁跨人转述的第一道闸**。
//
//   - uin 为空 → 不进库 (关系层表 uin NOT NULL): 没有主体就没有归属。
//   - 私密内容 → (false, nil) 丢弃, 不进任何表。
func (s *Store) SavePersonMemory(m PersonMemory) (bool, error) {
	if strings.TrimSpace(m.MemID) == "" {
		return false, fmt.Errorf("person memory: mem_id 为空")
	}
	if strings.TrimSpace(m.UIN) == "" {
		return false, fmt.Errorf("person memory: uin 为空 (关系层记忆必须挂在人身上, §5.4)")
	}
	if strings.TrimSpace(m.SceneKey) == "" {
		return false, fmt.Errorf("person memory: scene_key 为空 (来源场景既是检索键, 也是不许跨场景讲的依据)")
	}
	if ClassifySensitivity(m.Sensitivity, m.K+" "+m.V+" "+m.Label) == SensitivityPrivate {
		return false, nil // 个人私域**没有表** —— 在写入口就丢弃
	}
	if m.State == "" {
		m.State = "active"
	}
	if m.Kind == "" {
		m.Kind = "fact"
	}
	if m.Source == "" {
		m.Source = "user_said"
	}
	if m.Status == "" {
		m.Status = "open"
	}
	if m.Confidence == 0 {
		m.Confidence = 0.8
	}
	gf := 0
	if m.GlobalFact {
		gf = 1
	}
	now := clock.TodayTS()
	if m.CreatedTS == "" {
		m.CreatedTS = now
	}
	if m.UpdatedTS == "" {
		m.UpdatedTS = now
	}
	_, err := s.DB.Exec(`INSERT INTO convo_memory_person
		(mem_id, uin, scene_key, global_fact, k, v, label, kind, source, sensitivity, confidence,
		 state, due_ts, status, created_ts, updated_ts)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		-- ★ 冲突维度是 **(人, 场景, mem_id)** ——
		--   原来是 mem_id, 而 DO UPDATE 里还带着 uin=excluded.uin,
		--   于是同一个 id 写两次不同的 uin 会让**一条记忆从甲改判给乙**。
		--   (§5.4: 跨人转述是绝对红线, 优先级高于一切隐私开关。)
		--   ⚠️ 现在 uin/scene_key 是键的一部分, **不许**再出现在 SET 里。
		ON CONFLICT(uin, scene_key, mem_id) DO UPDATE SET
			global_fact=excluded.global_fact,
			k=excluded.k, v=excluded.v, label=excluded.label, kind=excluded.kind,
			source=excluded.source, confidence=excluded.confidence, state=excluded.state,
			due_ts=excluded.due_ts, status=excluded.status, updated_ts=excluded.updated_ts`,
		m.MemID, m.UIN, m.SceneKey, gf, m.K, m.V, m.Label, m.Kind, m.Source, SensitivityRelation,
		m.Confidence, m.State, m.DueTS, m.Status, m.CreatedTS, m.UpdatedTS)
	if err != nil {
		return false, err
	}
	return true, nil
}

// ——— 分区读取 (第二道闸: 注入白名单) ———

// sceneMemoryInjectCols 是**注入读取路径唯一的列清单**。
//
// origin_uin 不在这里 —— 这不是"记得别 SELECT 它", 是**查不出这个人**。
// (与 convo.go 用 kind 过滤把 vision_note 挡在上下文外是同一手法。)
const sceneMemoryInjectCols = `mem_id, scene_key, k, v, label, kind, confidence, state,
		COALESCE(created_ts,''), COALESCE(updated_ts,''), COALESCE(last_injected_ts,''),
		COALESCE(inject_count,0), COALESCE(last_access_ts,''), COALESCE(access_count,0)`

// SceneMemories 群公共层检索: 只看本场景的 active 行。
//
// 确定性全序 (confidence DESC, mem_id ASC): 同分时顺序不保证 = 提示词前缀逐字节不同 = 缓存断裂
// (承 memory.go TopMemories 的同一条教训: sort.Slice 不是稳定排序)。
func (s *Store) SceneMemories(sceneKey string, limit int) ([]SceneMemory, error) {
	if limit <= 0 {
		limit = 8
	}
	rows, err := s.DB.Query(`SELECT `+sceneMemoryInjectCols+`
		FROM convo_memory_scene WHERE scene_key=? AND state='active'
		ORDER BY confidence DESC, mem_id ASC LIMIT ?`, sceneKey, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SceneMemory
	for rows.Next() {
		var m SceneMemory
		if err := rows.Scan(&m.MemID, &m.SceneKey, &m.K, &m.V, &m.Label, &m.Kind, &m.Confidence,
			&m.State, &m.CreatedTS, &m.UpdatedTS, &m.LastInject, &m.InjectCount,
			&m.LastAccess, &m.AccessCount); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// SceneMemoryOrigin 血缘读取 (**审计 / 撤回专用**)。
//
// 它单独成一个方法, 是为了让"注入路径"里根本没有这个名字 —— 没得调, 也就没得漏。
func (s *Store) SceneMemoryOrigin(memID string) (string, error) {
	var uin string
	err := s.DB.QueryRow(`SELECT COALESCE(origin_uin,'') FROM convo_memory_scene WHERE mem_id=?`, memID).Scan(&uin)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return uin, err
}

// SceneMemoryInjection 渲染群公共层注入行。
//
// 它存在是因为 "注入里不许出现任何 uin" 这条契约必须**有代码可测**,
// 否则它只是一句口号 (R5)。渲染只用 kind / label / k / v ——
// 没有 uin, 没有场景标识, 没有 owner 标记 (§6.10.7)。
func (s *Store) SceneMemoryInjection(sceneKey string, limit int) ([]string, error) {
	ms, err := s.SceneMemories(sceneKey, limit)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		head := strings.TrimSpace(m.Label)
		if head == "" {
			head = strings.TrimSpace(m.K)
		}
		if head == "" {
			head = "群里的共识"
		}
		out = append(out, fmt.Sprintf("- [%s] %s: %s", sceneMemoryKindLabel(m.Kind), head, m.V))
	}
	return out, nil
}

func sceneMemoryKindLabel(kind string) string {
	switch kind {
	case "meme":
		return "群里的梗"
	case "event":
		return "公共事件"
	case "fact":
		return "群共识"
	case "relation":
		return "群里的关系"
	default:
		return "群公共记忆"
	}
}

// personMemoryCols 是关系层检索的列清单 (含 scene_key: 来源场景是检索键的一部分)。
const personMemoryCols = `mem_id, uin, scene_key, COALESCE(global_fact,0), k, v, label, kind,
	COALESCE(source,'user_said'), COALESCE(sensitivity,'relation'), confidence, state,
	COALESCE(due_ts,''), COALESCE(status,'open'), COALESCE(created_ts,''), COALESCE(updated_ts,''),
	COALESCE(last_injected_ts,''), COALESCE(inject_count,0), COALESCE(last_access_ts,''), COALESCE(access_count,0)`

// PersonMemories 关系层检索: **必须同时给场景与主体**。
//
// ⚠️ uin 为空 → 返回**空集** (安静), 不返回所有人 (泄漏)。
// 这条刻意与纪律 #3 相反: 纪律 #3 管的是"系统故障往说话倒", 这里是**事实层错误** ——
// 漏传主体是调用方的 bug, 它的正确退化方向是"什么也查不到", 不是"把所有人的事都倒出来"。
func (s *Store) PersonMemories(sceneKey, uin string) ([]PersonMemory, error) {
	uin = strings.TrimSpace(uin)
	if uin == "" {
		return nil, nil
	}
	rows, err := s.DB.Query(`SELECT `+personMemoryCols+`
		FROM convo_memory_person WHERE scene_key=? AND uin=? AND state='active'
		ORDER BY confidence DESC, mem_id ASC`, sceneKey, uin)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPersonMemories(rows)
}

// PersonGlobalFacts 该人的**稳定事实** (global_fact=1)。
//
// ⚠️ 两道约束, 缺一不可:
//   - **必须传 uin**: 空 → 空集 (与 PersonMemories 同规);
//   - **投影里没有 scene_key**: 场景标识在**结构上**进不了注入文本 (R7)。
//
// 它的正确用法是"对这个人" (他本人在场), 不是"讲那个场景" (§5.6:866)。
func (s *Store) PersonGlobalFacts(uin string) ([]PersonMemory, error) {
	uin = strings.TrimSpace(uin)
	if uin == "" {
		return nil, nil
	}
	rows, err := s.DB.Query(`SELECT mem_id, uin, COALESCE(global_fact,0), k, v, label, kind,
		COALESCE(source,'user_said'), COALESCE(sensitivity,'relation'), confidence, state,
		COALESCE(due_ts,''), COALESCE(status,'open'), COALESCE(created_ts,''), COALESCE(updated_ts,''),
		COALESCE(last_injected_ts,''), COALESCE(inject_count,0), COALESCE(last_access_ts,''), COALESCE(access_count,0)
		FROM convo_memory_person WHERE uin=? AND COALESCE(global_fact,0)=1 AND state='active'
		ORDER BY confidence DESC, mem_id ASC`, uin)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PersonMemory
	for rows.Next() {
		var m PersonMemory
		var gf int
		if err := rows.Scan(&m.MemID, &m.UIN, &gf, &m.K, &m.V, &m.Label, &m.Kind, &m.Source,
			&m.Sensitivity, &m.Confidence, &m.State, &m.DueTS, &m.Status, &m.CreatedTS,
			&m.UpdatedTS, &m.LastInject, &m.InjectCount, &m.LastAccess, &m.AccessCount); err != nil {
			return nil, err
		}
		m.GlobalFact = gf != 0
		// m.SceneKey 保持空串: 这个投影里根本没有来源场景。
		out = append(out, m)
	}
	return out, rows.Err()
}

func scanPersonMemories(rows *sql.Rows) ([]PersonMemory, error) {
	var out []PersonMemory
	for rows.Next() {
		var m PersonMemory
		var gf int
		if err := rows.Scan(&m.MemID, &m.UIN, &m.SceneKey, &gf, &m.K, &m.V, &m.Label, &m.Kind,
			&m.Source, &m.Sensitivity, &m.Confidence, &m.State, &m.DueTS, &m.Status,
			&m.CreatedTS, &m.UpdatedTS, &m.LastInject, &m.InjectCount, &m.LastAccess, &m.AccessCount); err != nil {
			return nil, err
		}
		m.GlobalFact = gf != 0
		out = append(out, m)
	}
	return out, rows.Err()
}

// ——— §6.10 用户区分: 三个问题 ———

// SceneMsg 一条进站消息 (§6.10 问题① 的输入)。
//
// ⚠️ UIN 是**身份锚**; 昵称/群名片根本不在这张结构里 ——
// 判据拿不到名字, 也就不可能拿名字当身份。
type SceneMsg struct {
	SceneKey  string // 场景键; 空 = 未提供 → 按故障往说话倒 (见 ResolveAddressee)
	SceneKind string // 可留空 → 由 SceneKey 前缀推断; ResolveAddressee 会用库里的 kind 覆盖
	UIN       string // 发言人 (§6.10.2: OneBot 事件的 user_id)
	// SenderName 发言人的**显示名**(群名片优先, 退回昵称)。
	//
	// ⚠️ 零值 = **未提供**, 与"他叫空名字"是两件事 ——
	// `ObserveMember` 据此决定要不要覆盖既有昵称。
	SenderName string
	Text       string
	AtUINs     []string // at 段解析出的被 @ 的 uin ('all' = @全体)
	TS         time.Time

	// 线程线索 (§6.10.2 ③): 上一条消息的发言人与收件人。
	HasPrev   bool   // false = 没有上一条 (零值 = 未提供)
	PrevUIN   string // 上一条是谁说的
	PrevTS    time.Time
	PrevToUIN string // 上一条帧的 to 字段 ('' = 未指定收件人)
}

// SelfIdentity convo 自己在该场景的身份。名字只用于"被点名"这一个用途。
type SelfIdentity struct {
	UIN      string
	Nickname string   // 平台昵称 (显示用; 也是"被点名"的信号之一)
	Card     string   // 它在该群的群名片 (同上)
	Aliases  []string // 额外别名 (与 cfg.SelfAliases 取并集)
}

// §6.10.2 四个分支的判据结果。必须是**稳定标识**: 日志与反例测试都要断言它。
const (
	ReasonAtSelf          = "at_self"                      // ① @我 → 接
	ReasonAtOthers        = "at_others"                    // ② @别人 → 不接
	ReasonAtOthersNamed   = "at_others_but_named"          // ② 例外: @了别人但文本点了我的名
	ReasonAtOthersAllowed = "at_others_allowed_by_config"  // ② 例外: 配置显式打开
	ReasonNamed           = "named"                        // 无 @ 但被点名
	ReasonThreadOurs      = "thread_ours"                  // ③ 上一条是我说的
	ReasonThreadToSelf    = "thread_addressed_to_self"     // ③ 上一条的收件人是我
	ReasonPrivateScene    = "private_scene_single_subject" // 私聊只有一个主体
	ReasonSelfEcho        = "self_echo"                    // 自己的回声
	ReasonUndecidable     = "undecidable"                  // ④ 判不出来 → 沉默

	// 系统故障 (§6.1 的边界)。**与 ④ 是两件事, 必须能分开观测**:
	// 语义判不出来 → 沉默; 读库失败/场景未登记 → 往说话倒 (纪律 #3)。
	ReasonNoSceneKey      = "no_scene_key_speak"
	ReasonSceneLoadFailed = "scene_load_failed_speak"
	ReasonSceneMissing    = "scene_missing_speak"
)

// IsAddressedToMe §6.10 问题②「这句话是对谁说的」。
//
// 四个分支 (§6.10.2):
//
//	① @我      → 接 (硬门槛: §6.3 "被 @ 必答", 任何开关都不许把它关掉)
//	② @别人    → 不接, 除非被点名 / 除非配置显式打开
//	③ 无 @     → 按对话线程归属 (距上一条 ≤ thread_window_min · 上一条是谁 · 上一条的收件人)
//	④ 判不出来 → 沉默 (§6.10:1026「群聊里误接话比不接更尴尬」)
//
// 纯函数: 不读库、不掷骰、不接 LLM (纪律 #7)。读库与故障倒向在 ResolveAddressee 里。
func IsAddressedToMe(m SceneMsg, self SelfIdentity, cfg GroupConfig) (bool, string) {
	cfg = NormalizeGroupConfig(cfg)

	// 自己的回声不接 —— OneBot 会把 convo 自己发出的消息也推回事件流,
	// 接了就成自问自答的死循环。
	if self.UIN != "" && strings.TrimSpace(m.UIN) == strings.TrimSpace(self.UIN) {
		return false, ReasonSelfEcho
	}

	// 私聊只有一个主体 (§6.10:1003): 不存在"接错人", 一律接。
	// 场景键未提供 → 按群处理: 群判据的默认落点是沉默, 那是保守的一侧。
	if sceneKindOf(m) == SceneKindPrivate {
		return true, ReasonPrivateScene
	}

	// ① @我 → 接
	for _, at := range m.AtUINs {
		if atTouchesSelf(at, self.UIN) {
			return true, ReasonAtSelf
		}
	}
	// ② @别人 → 不接 (两个例外都是**显式**的; 零值一律落在"不接")
	if atOthersPresent(m.AtUINs, self.UIN) {
		if cfg.ReplyWhenAtOthers {
			return true, ReasonAtOthersAllowed
		}
		if calledByName(m.Text, self, cfg) {
			return true, ReasonAtOthersNamed
		}
		return false, ReasonAtOthers
	}
	// 无 @: 被点名 = 叫到了我
	if calledByName(m.Text, self, cfg) {
		return true, ReasonNamed
	}
	// ③ 无 @ → 按线程归属
	if !m.HasPrev || !threadWithinWindow(m, cfg) {
		return false, ReasonUndecidable // ④
	}
	if pu := strings.TrimSpace(m.PrevUIN); pu != "" && pu == strings.TrimSpace(self.UIN) {
		return true, ReasonThreadOurs
	}
	if pt := strings.TrimSpace(m.PrevToUIN); pt != "" && pt == strings.TrimSpace(self.UIN) {
		return true, ReasonThreadToSelf
	}
	return false, ReasonUndecidable // ④
}

// ResolveAddressee 是 IsAddressedToMe 的**读库版本**: 它把两种"不接"分开。
//
//   - 语义判不出来 (无 @ / 无线程 / 话题接不上)  → (false, undecidable) : 沉默
//   - 系统故障 (读场景失败 / 场景未登记 / 没给场景键) → (true, *_speak) : 接
//
// ⚠️ 规格 §6.1 把这条边界钉死了: "这两条不冲突, 是两件事。实现者会用'判不出来 → 沉默'
// 顺手把读库失败也吞掉 —— 那就是纪律 #3 的反面。" 所以故障倒向在**这一层**就成为返回值,
// 不留给每个调用方各自解释 (P7: 静默的不是数据库, 是调用方)。
func (s *Store) ResolveAddressee(m SceneMsg, self SelfIdentity, cfg GroupConfig) (bool, string) {
	if strings.TrimSpace(m.SceneKey) == "" {
		return true, ReasonNoSceneKey
	}
	sc, err := s.LoadScene(m.SceneKey)
	if err != nil {
		return true, ReasonSceneLoadFailed
	}
	if sc == nil {
		// 未登记的场景: 规格 §6.1 把它与"读失败"同列 → 往说话倒。
		// 实际上白名单 (§2.1) 是上游硬门槛, 没登记的群根本进不到这里。
		return true, ReasonSceneMissing
	}
	if k := strings.TrimSpace(sc.Kind); k != "" {
		m.SceneKind = k // 库里的 kind 是权威: 把私聊判成群 = 变哑巴
	}
	return IsAddressedToMe(m, self, cfg)
}

// sceneKindOf 优先用显式 kind, 否则从场景键前缀推断。
func sceneKindOf(m SceneMsg) string {
	if k := strings.TrimSpace(m.SceneKind); k != "" {
		return k
	}
	return SceneKindOf(m.SceneKey)
}

// atTouchesSelf at 段里这一项是不是我。'all' (@全体) **不算叫我** —— 那是叫所有人。
func atTouchesSelf(at, selfUIN string) bool {
	a := strings.TrimSpace(at)
	if a == "" || selfUIN == "" || atIsAll(a) {
		return false
	}
	return a == strings.TrimSpace(selfUIN)
}

// atIsAll OneBot 的 @全体 在 at 段里是 qq="all"。
func atIsAll(at string) bool { return strings.EqualFold(strings.TrimSpace(at), "all") }

// atOthersPresent 存在"@了别人"的段 (@全体也算别人 —— 它不是单独叫我)。
func atOthersPresent(ats []string, selfUIN string) bool {
	for _, at := range ats {
		a := strings.TrimSpace(at)
		if a == "" {
			continue
		}
		if !atTouchesSelf(a, selfUIN) {
			return true
		}
	}
	return false
}

// calledByName 「被点名」(§6.10.2 ② 的例外)。判据是**文本包含**我的某个名字。
//
// 名字集合 = cfg.SelfAliases ∪ self.Aliases ∪ {平台昵称, 群名片}。
// 取并集而不是取其一: 少认一个名字只是漏接, 不会泄漏什么。
func calledByName(text string, self SelfIdentity, cfg GroupConfig) bool {
	if strings.TrimSpace(text) == "" {
		return false
	}
	for _, n := range selfNames(self, cfg) {
		if strings.Contains(text, n) {
			return true
		}
	}
	return false
}

func selfNames(self SelfIdentity, cfg GroupConfig) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	for _, a := range cfg.SelfAliases {
		add(a)
	}
	for _, a := range self.Aliases {
		add(a)
	}
	add(self.Nickname)
	add(self.Card)
	return out
}

// ThreadReplyMinGap 线程延续时的最小间隔 —— 它刚说完话, 下一条别马上又接。
//
// 没有这一条, "上一条是它说的"会把一次连续对话变成刷屏
// (2026-10-20 实测过同一秒冒出两条)。
const ThreadReplyMinGap = 10 * time.Second

// threadWithinWindow 距上一条是否还在线程窗口内。
//
// ⚠️ 时间戳缺失 → **视为窗口成立**: 零值 = 未提供 ≠ 超时 (纪律 #1)。
// 把"没给时间"当成"过期线程"会静默地把线程判据整个关掉。
func threadWithinWindow(m SceneMsg, cfg GroupConfig) bool {
	if m.TS.IsZero() || m.PrevTS.IsZero() {
		return true
	}
	return m.TS.Sub(m.PrevTS) <= time.Duration(cfg.ThreadWindowMin)*time.Minute
}

// ——— §6.10.3 多主体发言流 ———

// ⚠️ 这里原来有一个 `SceneHistoryLine`(拼「名字(尾号): 文本」) —— **已删**。
//
// ⚠️ 2026-10-16 行首格式换成了群主定的「**序号 编号: 原话**」
// (对照表在表头只印一次 ✓), 它就没人调了 ✗。

// UinTail uin 后四位 (不足四位原样; 空 → "????")。够区分人, 又不必把完整号写进提示词。
func UinTail(uin string) string {
	rs := []rune(strings.TrimSpace(uin))
	switch {
	case len(rs) == 0:
		return "????"
	case len(rs) <= 4:
		return string(rs)
	default:
		return string(rs[len(rs)-4:])
	}
}

// ——— 建表 (§3 表结构提案的叶子部分) ———
//
// ⚠️ 这里**只建自己的四张表**。convo_messages / convo_slang 的分区列补齐是主控的迁移活,
// 本文件一个字都不碰它们 —— 两处改同一张表的 DDL 是双真源, 合并时必炸。
const sceneSchemaScene = `
CREATE TABLE IF NOT EXISTS convo_scene (
  scene_key     TEXT PRIMARY KEY,                  -- 'group:<gid>' | 'private:<uin>'
  kind          TEXT NOT NULL,                     -- 'group' | 'private'
  platform      TEXT NOT NULL DEFAULT 'qq',
  group_id      TEXT NOT NULL DEFAULT '',
  peer_uin      TEXT NOT NULL DEFAULT '',
  self_role     TEXT NOT NULL DEFAULT '',          -- 它自己在该群的平台角色 (未取到 = '')
  stage         TEXT NOT NULL DEFAULT 'stranger',  -- 场景阶段 = 热力 cap 的来源
  intimacy      REAL NOT NULL DEFAULT 0,
  first_seen_ts TEXT NOT NULL DEFAULT '',
  last_msg_ts   TEXT NOT NULL DEFAULT '',
  updated_ts    TEXT NOT NULL DEFAULT ''
);`

const sceneSchemaMember = `
CREATE TABLE IF NOT EXISTS convo_scene_member (
  scene_key     TEXT NOT NULL,
  uin           TEXT NOT NULL,                     -- ★ 身份锚。昵称/群名片**只做显示**
  display_name  TEXT NOT NULL DEFAULT '',
  group_card    TEXT NOT NULL DEFAULT '',
  stage         TEXT NOT NULL DEFAULT 'stranger',
  affinity      REAL NOT NULL DEFAULT 0,
  address_as    TEXT NOT NULL DEFAULT '',          -- 称呼: 按场景各存一份, 两边不许串
  taboo_json    TEXT NOT NULL DEFAULT '[]',
  first_seen_ts TEXT NOT NULL DEFAULT '',          -- ★ is_new 由它现算, 不落布尔量 (纪律 #4)
  updated_ts    TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (scene_key, uin)
);`

const sceneSchemaMemScene = `
CREATE TABLE IF NOT EXISTS convo_memory_scene (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  mem_id        TEXT NOT NULL,
  scene_key     TEXT NOT NULL,
  k             TEXT NOT NULL,
  v             TEXT NOT NULL,
  label         TEXT NOT NULL DEFAULT '',
  kind          TEXT NOT NULL DEFAULT 'meme',
  -- ★ 一条群公共记忆的身份是 **(场景, mem_id)**, 不是 mem_id ——
  --   写成 mem_id 全局唯一时, B 群写同一个 id 会**改写 A 群那行的内容却保留 A 的 scene_key**:
  --   B 永远看不到自己的梗, 而 A 的梗被顶掉。静默跨群串。
  confidence    REAL NOT NULL DEFAULT 0.8,
  -- ★ 血缘列, **不是 uin 列**: 注入渲染器的固定列清单永不 SELECT 它。
  --   本表没有任何名为 uin 的列 = 结构上不可能把某个人的东西渲染进群上下文。
  origin_uin    TEXT NOT NULL DEFAULT '',
  state         TEXT NOT NULL DEFAULT 'active',
  created_ts    TEXT NOT NULL DEFAULT '',
  updated_ts    TEXT NOT NULL DEFAULT '',
  last_injected_ts TEXT NOT NULL DEFAULT '',
  inject_count  INTEGER NOT NULL DEFAULT 0,
  last_access_ts   TEXT NOT NULL DEFAULT '',
  access_count  INTEGER NOT NULL DEFAULT 0,
  -- ★ 身份 = **(场景, mem_id)**: 见上面 mem_id 那条注释。
  UNIQUE(scene_key, mem_id)
);
CREATE INDEX IF NOT EXISTS ix_mem_scene ON convo_memory_scene(scene_key, state);`

const sceneSchemaMemPerson = `
CREATE TABLE IF NOT EXISTS convo_memory_person (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  mem_id        TEXT NOT NULL,
  -- ★ uin NOT NULL 且非空: 漏传主体的检索返回**空集**(安静), 不返回所有人(泄漏)。
  uin           TEXT NOT NULL CHECK (uin <> ''),
  scene_key     TEXT NOT NULL,
  -- ★ 关系层记忆的身份是 **(人, 场景, mem_id)** ——
  --   写成 mem_id 全局唯一 + DO UPDATE SET uin=excluded.uin,
  --   会让**一条记忆从甲改判给乙**, 而 §5.4 说跨人转述是绝对红线。
  global_fact   INTEGER NOT NULL DEFAULT 0,
  k             TEXT NOT NULL,
  v             TEXT NOT NULL,
  label         TEXT NOT NULL DEFAULT '',
  kind          TEXT NOT NULL DEFAULT 'fact',
  source        TEXT NOT NULL DEFAULT 'user_said',
  sensitivity   TEXT NOT NULL DEFAULT 'relation',  -- 只允许 'relation'; 'private' 在写入口就被丢弃
  confidence    REAL NOT NULL DEFAULT 0.8,
  state         TEXT NOT NULL DEFAULT 'active',
  due_ts        TEXT NOT NULL DEFAULT '',
  status        TEXT NOT NULL DEFAULT 'open',
  created_ts    TEXT NOT NULL DEFAULT '',
  updated_ts    TEXT NOT NULL DEFAULT '',
  last_injected_ts TEXT NOT NULL DEFAULT '',
  inject_count  INTEGER NOT NULL DEFAULT 0,
  last_access_ts   TEXT NOT NULL DEFAULT '',
  access_count  INTEGER NOT NULL DEFAULT 0,
  -- ★ 身份 = **(人, 场景, mem_id)**: 见上面 mem_id 那条注释。
  UNIQUE(uin, scene_key, mem_id)
);
CREATE INDEX IF NOT EXISTS ix_mem_person ON convo_memory_person(scene_key, uin, state);
CREATE INDEX IF NOT EXISTS ix_mem_person_global ON convo_memory_person(uin, global_fact);`

const sceneSchema = sceneSchemaScene + sceneSchemaMember + sceneSchemaMemScene + sceneSchemaMemPerson

// EnsureSceneSchema 建/迁移四张场景表。
//
// ⚠️ 它同时负责**存量库的形状迁移** —— 见 migetMemoryDims 的注释。
func EnsureSceneSchema(s *Store) error {
	if s == nil || s.DB == nil {
		return fmt.Errorf("scene schema: store 未初始化")
	}
	if _, err := s.DB.Exec(sceneSchema); err != nil {
		return err
	}
	// ⚠️ `CREATE TABLE IF NOT EXISTS` **不会改已经建好的表** ——
	// 上面那段 DDL 只对**新库**生效, 存量库要靠这一步。
	return migrateMemoryDims(s.DB)
}

// migrateMemoryDims 把两张记忆表的唯一性从 mem_id 改成正确的维度:
//
//	convo_memory_scene  : mem_id → (scene_key, mem_id)
//	convo_memory_person : mem_id → (uin, scene_key, mem_id)
//
// ⚠️ **SQLite 不能改约束, 只能重建表** —— 标准的四步(建新/搬/删旧/改名)。
//
// ⚠️ 生产上这两张表在改的时候**是空的**, 但仍然按"保数据"写:
// 万一将来有行, 这一步不该吃掉它们。(也就是说不许偷懒直接 DROP。)
//
// ⚠️ **只在检测到旧形状时才动** —— 否则每次启动都会重建一遍表。
func migrateMemoryDims(db *sql.DB) error {
	type job struct {
		table, loneCol, newDDL, cols string
	}
	jobs := []job{
		{
			table:   "convo_memory_scene",
			loneCol: "mem_id",
			cols:    "id, mem_id, scene_key, k, v, label, kind, confidence, origin_uin, state, created_ts, updated_ts, last_injected_ts, inject_count, last_access_ts, access_count",
			newDDL:  sceneSchemaMemScene,
		},
		{
			table:   "convo_memory_person",
			loneCol: "mem_id",
			cols:    "id, mem_id, uin, scene_key, global_fact, k, v, label, kind, source, sensitivity, confidence, state, due_ts, status, created_ts, updated_ts, last_injected_ts, inject_count, last_access_ts, access_count",
			newDDL:  sceneSchemaMemPerson,
		},
	}
	for _, j := range jobs {
		if !hasLoneUniqueIndex(db, j.table, j.loneCol) {
			continue
		}
		tmp := j.table + "_new"
		ddl := strings.Replace(j.newDDL, "CREATE TABLE IF NOT EXISTS "+j.table, "CREATE TABLE "+tmp, 1)
		if _, err := db.Exec(ddl); err != nil {
			return fmt.Errorf("重建 %s 失败(建新表): %w", j.table, err)
		}
		if _, err := db.Exec("INSERT INTO " + tmp + " (" + j.cols + ") SELECT " + j.cols + " FROM " + j.table); err != nil {
			return fmt.Errorf("重建 %s 失败(搬数据): %w", j.table, err)
		}
		if _, err := db.Exec("DROP TABLE " + j.table); err != nil {
			return fmt.Errorf("重建 %s 失败(删旧表): %w", j.table, err)
		}
		if _, err := db.Exec("ALTER TABLE " + tmp + " RENAME TO " + j.table); err != nil {
			return fmt.Errorf("重建 %s 失败(改名): %w", j.table, err)
		}
		log.Info("convo:store", "§6.10 记忆表 %s 的主键维度已从 mem_id 改为正确的组合", j.table)
	}
	return nil
}

// hasLoneUniqueIndex 表上有没有一个"只含某一列"的唯一索引。
//
// ⚠️ `TEXT UNIQUE` 建出来的隐式索引(`sqlite_autoindex_...`)也会出现在 pragma 里,
// 所以这一条能认出旧形状 —— 不需要在别处再记一个 schema 版本号。
func hasLoneUniqueIndex(db *sql.DB, table, col string) bool {
	rows, err := db.Query(`SELECT name FROM pragma_index_list(?) WHERE "unique" = 1`, table)
	if err != nil {
		return false
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err == nil {
			names = append(names, n)
		}
	}
	for _, n := range names {
		irs, err := db.Query(`SELECT name FROM pragma_index_info(?)`, n)
		if err != nil {
			continue
		}
		var cols []string
		for irs.Next() {
			var c string
			if err := irs.Scan(&c); err == nil {
				cols = append(cols, c)
			}
		}
		irs.Close()
		if len(cols) == 1 && cols[0] == col {
			return true
		}
	}
	return false
}
