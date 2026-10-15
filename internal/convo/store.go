package convo

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tzhnahida/VocabLock/internal/clock"
	"github.com/tzhnahida/VocabLock/internal/log"
	_ "modernc.org/sqlite"
)

// Store is the convo trial database (convo-trial.db) — deliberately separate
// from the main vocablock.db: trial data is disposable, schema changes freely,
// and the main app's migration lock must never be touched.
type Store struct {
	DB *sql.DB

	// ——— 记忆注入批次缓存 (P1 缓存债) ———
	//
	// §8.1 把"关系记忆"定为**慢变量**, 但 injectPenalty 是按"每一次检索"降温的:
	// 上一轮刚注入的记忆, 下一轮立刻降到 0.25 倍 → 每轮都换一批新的 →
	// 提示词的记忆段逐轮不同 → 从记忆段起的**全部前缀每轮断裂** ([实测] 命中 53%→8%)。
	//
	// 修法: 冻结窗口内复用**同一批**(含顺序), 窗口到期才重算 + MarkInjected。
	// "检索诱导遗忘"的意图保留, 粒度从"轮"对齐到 §8.1 的"慢"(会话级)。
	memMu sync.Mutex
	// ⚠️ 缓存键必须**带上场景**: §5.7 之后不同场景取到的记忆**不是同一批**,
	// 只按时间窗口做键的话, 群里那批会被私聊直接复用 —— 那是跨人转述的读侧入口。
	memScene  string
	memBucket string
	memBatch  []Memory
}

// MemoryFreezeWindow 记忆注入批次的冻结窗口 —— 对齐 §8.1 "记忆 = 慢变量"。
//
// 取值权衡: 太小(轮级) → 前缀每轮断裂, 缓存全废; 太大 → 记忆新鲜度下降
// (刚聊到的事进不了下一轮提示词)。30 分钟 = 一次会话的典型长度。
const MemoryFreezeWindow = 30 * time.Minute

func OpenStore(dataDir, dbFile string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	path := filepath.Join(dataDir, dbFile)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1) // SQLite serialized writes (same as main app)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	s := &Store{DB: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.DB.Close() }

func (s *Store) migrate() error {
	schema := `
CREATE TABLE IF NOT EXISTS convo_meta (
	k TEXT PRIMARY KEY, v TEXT
);
CREATE TABLE IF NOT EXISTS convo_profile (
	id INTEGER PRIMARY KEY,
	name TEXT, persona_json TEXT, boundaries_json TEXT,
	created_ts TEXT, updated_ts TEXT
);
CREATE TABLE IF NOT EXISTS convo_schedule (
	id INTEGER PRIMARY KEY,
	week_json TEXT, revision_history TEXT DEFAULT '[]', updated_ts TEXT
);
CREATE TABLE IF NOT EXISTS convo_mood (
	id INTEGER PRIMARY KEY,
	state TEXT, since_ts TEXT, note TEXT
);
CREATE TABLE IF NOT EXISTS convo_messages (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	ts TEXT, dir TEXT, channel TEXT, kind TEXT, payload_json TEXT, session_id TEXT,
	-- §9/§3.7 分区: **谁说的** (user | ai_to_user | ai_to_ai) 与**在哪个场景**。
	-- 没有这两列, AI↔AI 的夜间批算产物会和真人对话无法区分地混进 L1 上下文。
	origin TEXT DEFAULT '', scene_key TEXT DEFAULT '',
	-- §6.10.3 多主体: 群里的发言人。私聊恒空 —— 那里只有一个主体, 存了是噪声。
	sender_uin TEXT DEFAULT '',
	-- §6.9 冲突四信号里的 **② 互相 @** 只能从这里读。
	--
	-- ⚠️ 没有它, 信号 ② 就**永远算不出来**, 而后果不是"判不出冲突",
	-- 而是**门槛实际是 3/3**: 能算的只剩 ①连续对呛 ③负面密度 ④间隔骤短,
	-- 三个必须全中。
	at_uins TEXT DEFAULT ''
);
CREATE TABLE IF NOT EXISTS convo_topics (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	kind TEXT, payload_json TEXT, used INTEGER DEFAULT 0, created_ts TEXT
);
CREATE TABLE IF NOT EXISTS convo_events (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	ts TEXT, kind TEXT, payload_json TEXT
);
CREATE TABLE IF NOT EXISTS convo_memory (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	mem_id TEXT UNIQUE, k TEXT, v TEXT, label TEXT, kind TEXT, description TEXT,
	confidence REAL, src_event_id TEXT, state TEXT DEFAULT 'active',
	created_ts TEXT, updated_ts TEXT, access_count INTEGER DEFAULT 0, last_access_ts TEXT,
	-- 归属: user_said | ai_said | observed | inferred (只有 user_said 能进"记得 ta 的事")
	source TEXT DEFAULT 'user_said',
	-- 检索诱导遗忘: 被注入过就降温, 而不是升权
	last_injected_ts TEXT DEFAULT '', inject_count INTEGER DEFAULT 0,
	-- 计划类记忆的终态 (治"执念": 没有终态的约定会一直被注入)
	due_ts TEXT DEFAULT '', status TEXT DEFAULT 'open',
	-- §7.3 维一 敏感度: 决定**能不能跨人用**。'private' 在写入口就被丢弃, 不落这里。
	-- 'relation' = 只在该场景可用; 'public' = 任何场景可用。
	sensitivity TEXT DEFAULT 'relation',
	-- §7.3 维一 归属: 这条记忆是**在哪个场景**得到的。空 = 全局 (不绑定场景)。
	scene_key TEXT DEFAULT '',
	-- §7.3 维三 时间: 槽位键 + 生效区间。**空 slot_key = 不参与取代** (不是"槽位为空")。
	slot_key TEXT DEFAULT '',
	superseded_ts TEXT DEFAULT '',
	valid_until TEXT DEFAULT ''
);
CREATE TABLE IF NOT EXISTS convo_memory_pending (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	mem_id TEXT, prompt TEXT, status TEXT DEFAULT 'pending', created_ts TEXT
);
CREATE TABLE IF NOT EXISTS convo_sessions (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	session_id TEXT UNIQUE, started_ts TEXT, ended_ts TEXT, summary TEXT
);
CREATE TABLE IF NOT EXISTS stickers (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	src_url TEXT, local_path TEXT, lang TEXT,
	affinity REAL DEFAULT 0.5, use_count INTEGER DEFAULT 0, score REAL,
	tags TEXT, source TEXT, born_ts TEXT, retired_ts TEXT,
	note TEXT DEFAULT ''
);
CREATE TABLE IF NOT EXISTS convo_counters (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT, day TEXT, n INTEGER DEFAULT 0, updated_ts TEXT,
	UNIQUE(name, day)
);
CREATE TABLE IF NOT EXISTS convo_mood_state (
	id INTEGER PRIMARY KEY,
	valence REAL DEFAULT 0, arousal REAL DEFAULT 0.5,
	causes_json TEXT DEFAULT '[]', since_ts TEXT DEFAULT '',
	low_until TEXT DEFAULT '', updated_ts TEXT DEFAULT '',
	-- 背景压力 (§3.2 约束 2): 天气这类"整天都在"的事件不是脉冲, 是底色
	bg_valence REAL DEFAULT 0, bg_text TEXT DEFAULT '', bg_until TEXT DEFAULT ''
);
CREATE TABLE IF NOT EXISTS convo_world_event (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	event_key TEXT UNIQUE, ts TEXT, kind TEXT, source TEXT, text TEXT,
	valence REAL, arousal REAL, intensity REAL, weight REAL, duration TEXT,
	-- §3.6.8 沉降: 时刻以**最后一次被提及**为准, 不是生成时刻
	last_mentioned_ts TEXT DEFAULT '', mention_count INTEGER DEFAULT 0
);
CREATE TABLE IF NOT EXISTS convo_decision_trace (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	ts TEXT, seq INTEGER, action_kind TEXT, proactive INTEGER, user_text TEXT, teaching INTEGER,
	state_fact TEXT, mood_valence REAL, mood_arousal REAL,
	mem_ids_json TEXT, slang_hit TEXT,
	system_prompt TEXT, prompt_bytes INTEGER,
	raw_output TEXT, outcome TEXT, sent_texts_json TEXT,
	-- §6.10 护栏 3 的三个数据源 (老库 ALTER 补)
	speaker_uin TEXT DEFAULT '', scene_key TEXT DEFAULT '', stance TEXT DEFAULT '',
	-- §6.8 归因: 光有 stance 只能知道「没接」, 不知道**是判据没够还是偏向压了**
	reason TEXT DEFAULT ''
);
CREATE TABLE IF NOT EXISTS convo_world_event_draft (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	event_key TEXT UNIQUE, fire_ts TEXT,
	kind TEXT, source TEXT, text TEXT,
	valence REAL, arousal REAL, intensity REAL, weight REAL, duration TEXT,
	fired INTEGER DEFAULT 0
);
CREATE TABLE IF NOT EXISTS convo_mood_log (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	ts TEXT, kind TEXT, source TEXT, text TEXT, weight REAL,
	delta_v REAL, delta_a REAL, v_after REAL, a_after REAL
);
CREATE TABLE IF NOT EXISTS convo_slang (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	phrase TEXT UNIQUE, meaning TEXT, example TEXT,
	source_quote TEXT, source TEXT DEFAULT 'user', state TEXT DEFAULT 'active',
	confidence REAL DEFAULT 0.8, use_count INTEGER DEFAULT 0,
	last_used_ts TEXT, created_ts TEXT, updated_ts TEXT
);
CREATE TABLE IF NOT EXISTS convo_body_state (
	id INTEGER PRIMARY KEY,
	kind TEXT, cause TEXT, note TEXT,
	-- §4.2: **只存事实** (发作时刻 + 病程)。severity / until / tail 全部现算 ——
	-- 存派生量就必须回答"谁在什么时候重算它", 而这个量是时间的纯函数。
	onset_ts TEXT DEFAULT '', course_h REAL DEFAULT 0, updated_ts TEXT DEFAULT ''
);
`
	_, err := s.DB.Exec(schema)
	if err != nil {
		return err
	}
	// v2.4: stickers.note (含义/适用语境标注) — 老库 ALTER 补列, 新库 CREATE 已带
	var hasNote int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('stickers') WHERE name='note'`).Scan(&hasNote); err == nil && hasNote == 0 {
		_, _ = s.DB.Exec(`ALTER TABLE stickers ADD COLUMN note TEXT DEFAULT ''`)
	}
	// ⚠️ **这 8 列不许再挂回上面那个 if 里面。**
	//
	// 它们原先跟 `stickers ADD COLUMN note` 挤在同一个 `if hasNote == 0` 分支里。
	// 后果: 库里**已经有 stickers.note** 时整个分支不进 →
	// convo_memory 的 source 永远加不上 → 每次 UpsertMemory / dailyExtract 都报
	//   `table convo_memory has no column named source`
	// 而现象是「它不记事」「夜里什么都没发生」—— 完全看不出是迁移的问题。
	//
	// 实测代价: Pi 上这套迁移挂了整整一个月, 记忆写入全废, 夜间批第 1 步就死。
	// **一组迁移只许挂在自己的检查下面。**
	// 记忆归属 / 注入反馈 / 计划终态 (§7.3 三维同理)
	_, _ = s.DB.Exec(`ALTER TABLE convo_memory ADD COLUMN source TEXT DEFAULT 'user_said'`)
	_, _ = s.DB.Exec(`ALTER TABLE convo_memory ADD COLUMN last_injected_ts TEXT DEFAULT ''`)
	_, _ = s.DB.Exec(`ALTER TABLE convo_memory ADD COLUMN inject_count INTEGER DEFAULT 0`)
	_, _ = s.DB.Exec(`ALTER TABLE convo_memory ADD COLUMN due_ts TEXT DEFAULT ''`)
	_, _ = s.DB.Exec(`ALTER TABLE convo_memory ADD COLUMN status TEXT DEFAULT 'open'`)
	_, _ = s.DB.Exec(`ALTER TABLE convo_memory ADD COLUMN sensitivity TEXT DEFAULT 'relation'`)
	_, _ = s.DB.Exec(`ALTER TABLE convo_memory ADD COLUMN scene_key TEXT DEFAULT ''`)
	_, _ = s.DB.Exec(`ALTER TABLE convo_memory ADD COLUMN slot_key TEXT DEFAULT ''`)
	_, _ = s.DB.Exec(`ALTER TABLE convo_memory ADD COLUMN superseded_ts TEXT DEFAULT ''`)
	_, _ = s.DB.Exec(`ALTER TABLE convo_memory ADD COLUMN valid_until TEXT DEFAULT ''`)
	// P4: 背景压力三列 (老库升级; 重复列报错忽略)
	_, _ = s.DB.Exec(`ALTER TABLE convo_mood_state ADD COLUMN bg_valence REAL DEFAULT 0`)
	_, _ = s.DB.Exec(`ALTER TABLE convo_mood_state ADD COLUMN bg_text TEXT DEFAULT ''`)
	_, _ = s.DB.Exec(`ALTER TABLE convo_mood_state ADD COLUMN bg_until TEXT DEFAULT ''`)
	// §9 消息分区两列 (老库升级; 重复列报错忽略)
	_, _ = s.DB.Exec(`ALTER TABLE convo_messages ADD COLUMN origin TEXT DEFAULT ''`)
	_, _ = s.DB.Exec(`ALTER TABLE convo_messages ADD COLUMN scene_key TEXT DEFAULT ''`)
	_, _ = s.DB.Exec(`ALTER TABLE convo_messages ADD COLUMN sender_uin TEXT DEFAULT ''`)
	// §6.9: @ 目标 (逗号分隔的 uin)。与上面三列同规 —— **幂等, 失败不拦启动**。
	_, _ = s.DB.Exec(`ALTER TABLE convo_messages ADD COLUMN at_uins TEXT DEFAULT ''`)
	// §6.10 护栏 3: 决策轨迹的场景/发言人/三态
	_, _ = s.DB.Exec(`ALTER TABLE convo_decision_trace ADD COLUMN speaker_uin TEXT DEFAULT ''`)
	_, _ = s.DB.Exec(`ALTER TABLE convo_decision_trace ADD COLUMN scene_key TEXT DEFAULT ''`)
	_, _ = s.DB.Exec(`ALTER TABLE convo_decision_trace ADD COLUMN stance TEXT DEFAULT ''`)
	_, _ = s.DB.Exec(`ALTER TABLE convo_decision_trace ADD COLUMN reason TEXT DEFAULT ''`)
	_, _ = s.DB.Exec(`ALTER TABLE convo_decision_trace ADD COLUMN reason TEXT DEFAULT ''`)
	_, _ = s.DB.Exec(`ALTER TABLE convo_decision_trace ADD COLUMN bias_delta INTEGER DEFAULT 0`)
	// §3.6.8 事件沉降两列 (老库升级; 重复列报错忽略)
	_, _ = s.DB.Exec(`ALTER TABLE convo_world_event ADD COLUMN last_mentioned_ts TEXT DEFAULT ''`)
	_, _ = s.DB.Exec(`ALTER TABLE convo_world_event ADD COLUMN mention_count INTEGER DEFAULT 0`)

	// §5.5 主体性 / §4.3 中断-恢复 的表由各自的模块自带 (幂等建表)。
	//
	// ⚠️ **这两个调用不能省, 也不能忽略错误** —— 少了它们, 表永远不会被建,
	// 而 LoadInterruption / PendingAppointments 会**静默返回空**:
	// 表现是"它从不记得约过什么""从不中断", 看不出是坏了。
	// (P7 的教训: 静默的不是数据库, 是调用方。)
	if err := EnsureAgencySchema(s); err != nil {
		return fmt.Errorf("agency schema: %w", err)
	}
	if err := EnsureFatigueSchema(s); err != nil {
		return fmt.Errorf("fatigue schema: %w", err)
	}
	// §6.7 热力
	if err := EnsureHeatSchema(s); err != nil {
		return fmt.Errorf("heat schema: %w", err)
	}
	// §4.4.4 梦 (内部素材, 与记忆不是一个池子)
	if err := EnsureDreamSchema(s); err != nil {
		return fmt.Errorf("dream schema: %w", err)
	}
	// #13 存量修正: 角色卡建出来的人格, identity 还停在 student/office_worker 的, 切到角色档。
	//
	// ⚠️ 修复只管生成路径、不管存量 —— 这条已经在"周三没有 sleep 槽"上吃过一次。
	if prof, perr := s.LoadProfile(); perr == nil && prof != nil {
		if FixLegacyRoleIdentity(prof) {
			if serr := s.SaveProfile(prof); serr != nil {
				log.Warn("convo:world", "存量身份修正落库失败: %v", serr)
			} else {
				log.Info("convo:world", "#13 存量人格的身份已切到角色档: %s", prof.Name)
			}
		}
	}
	// §4.1.1 慢性压力: convo_mood_state 补 strain 列。
	if err := EnsureStrainSchema(s); err != nil {
		return fmt.Errorf("strain schema: %w", err)
	}
	// §5.7 owner 的手动关系倍率 (设计稿 L1301)。
	if err := EnsureOwnerMultSchema(s); err != nil {
		return fmt.Errorf("owner mult schema: %w", err)
	}
	// §4.4.6 日级读数 (冲突次数 / 热力峰值 —— 这两个**不可重算**)。
	if err := EnsureDayMetricSchema(s); err != nil {
		return fmt.Errorf("day metric schema: %w", err)
	}
	// §4.2.4 口渴句式记账 (给 owner 看的那一面 + 反口头禅的判据)。
	if err := EnsureThirstSchema(s); err != nil {
		return fmt.Errorf("thirst schema: %w", err)
	}
	// §4.2.2 身体三量的**事实**表 (当前值现算, 不落库)。
	if err := EnsureBodyAxisSchema(s); err != nil {
		return fmt.Errorf("body axis schema: %w", err)
	}
	// §4.5 层 2 行程 (次日具体行程)。⚠️ 同样**不能省**: 少了它 LoadDayPlan 静默返回空,
	// 表现只是"它今天没有安排" —— 看不出是表没建。
	if err := EnsureDayPlanSchema(s); err != nil {
		return fmt.Errorf("day plan schema: %w", err)
	}
	// §4.4.5 睡眠质量 (一夜一行)
	if err := EnsureNightSchema(s); err != nil {
		return fmt.Errorf("night schema: %w", err)
	}
	// §3.6 事件池生成 (配额账本)
	if err := EnsureEventGenSchema(s); err != nil {
		return fmt.Errorf("eventgen schema: %w", err)
	}
	// §6.10 / §9 场景与三级记忆分区
	if err := EnsureSceneSchema(s); err != nil {
		return fmt.Errorf("scene schema: %w", err)
	}
	// §6.9 冲突介入 (只落"介入过"这一事实)
	if err := EnsureConflictSchema(s); err != nil {
		return fmt.Errorf("conflict schema: %w", err)
	}
	// §4.4.1b 语域漂移 (一天一行读数)
	if err := EnsureRegisterSchema(s.DB); err != nil {
		return fmt.Errorf("register schema: %w", err)
	}
	// §7.1 群消息日终折叠 (一天一条, 幂等由主键承载)
	if err := EnsureFoldSchema(s.DB); err != nil {
		return fmt.Errorf("fold schema: %w", err)
	}
	// §5.1 三个量 (亲密度/敬意/熟悉度)
	if err := EnsureBondSchema(s.DB); err != nil {
		return fmt.Errorf("bond schema: %w", err)
	}
	// §6.3 判据① 品味轨迹
	if err := EnsureTasteSchema(s.DB); err != nil {
		return fmt.Errorf("taste schema: %w", err)
	}
	// §6.4.3 社交反馈闭环 (计数与边界同表)
	if err := EnsureFeedbackSchema(s.DB); err != nil {
		return fmt.Errorf("feedback schema: %w", err)
	}
	// §7.5 意图轨 (**刻意与 convo_memory 分表**)
	if err := EnsureIntentSchema(s.DB); err != nil {
		return fmt.Errorf("intent schema: %w", err)
	}
	// §4.4.6 作息偏移状态 (护栏的**持久化承载者**)
	if err := EnsureSleepPointSchema(s.DB); err != nil {
		return fmt.Errorf("sleeppoint schema: %w", err)
	}
	// §2.5 学习产物 (**独立表**: 不进 convo_topics, 否则会被 SeedTopics 全表清空)
	if err := EnsureLearnSchema(s.DB); err != nil {
		return fmt.Errorf("learn schema: %w", err)
	}
	// §2.6 灵光 (**status 不在表里** —— 它是 heat + 时间的纯函数)
	if err := EnsureSparkSchema(s.DB); err != nil {
		return fmt.Errorf("spark schema: %w", err)
	}
	// §6.3/§2.5 话题词表 (夜间抽一次, 白天只做字符串匹配)
	if err := EnsureTopicKeySchema(s.DB); err != nil {
		return fmt.Errorf("topic key schema: %w", err)
	}
	// §2.6.3 被顶素材
	if err := EnsureChallengeSchema(s.DB); err != nil {
		return fmt.Errorf("challenge schema: %w", err)
	}
	// §3.7 同伴名册 (手填, 落库以便管理页写)
	if err := EnsureNightPeerSchema(s.DB); err != nil {
		return fmt.Errorf("night peer schema: %w", err)
	}
	// §7.4 遗忘权的**墓碑** (删除之后不许再学回来)
	if err := EnsureForgetSchema(s.DB); err != nil {
		return fmt.Errorf("forget schema: %w", err)
	}
	// 角色位**长出来**的账本 (取代 DayRoll 分配)
	if err := EnsureRoleSchema(s.DB); err != nil {
		return fmt.Errorf("role schema: %w", err)
	}
	// §2.3 世界观挂载
	if err := EnsureWorldBibleSchema(s.DB); err != nil {
		return fmt.Errorf("world bible schema: %w", err)
	}
	// 图片去重与照片/表情包分流
	if err := EnsureImageSchema(s.DB); err != nil {
		return fmt.Errorf("image schema: %w", err)
	}
	// 它自己去看世界 (§3.5 真实数据源)
	if err := EnsureNewsSchema(s.DB); err != nil {
		return fmt.Errorf("news schema: %w", err)
	}
	// 教练机 (owner 直接教它)
	if err := EnsureCoachSchema(s.DB); err != nil {
		return fmt.Errorf("coach schema: %w", err)
	}
	// §6.10 插话状态 (被群友说过 → 收敛)
	if err := EnsureChimeSchema(s.DB); err != nil {
		return fmt.Errorf("chime schema: %w", err)
	}
	return nil
}

// ——— meta k/v ———

func (s *Store) MetaGet(k string) (string, bool, error) {
	var v string
	err := s.DB.QueryRow(`SELECT v FROM convo_meta WHERE k=?`, k).Scan(&v)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

func (s *Store) MetaSet(k, v string) error {
	_, err := s.DB.Exec(`INSERT INTO convo_meta(k,v) VALUES(?,?)
		ON CONFLICT(k) DO UPDATE SET v=excluded.v`, k, v)
	return err
}

// ——— schedule ———

func (s *Store) SaveSchedule(week *WeekSchedule) error {
	b, err := json.Marshal(week)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(`INSERT INTO convo_schedule(id, week_json, updated_ts)
		VALUES(1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET week_json=excluded.week_json, updated_ts=excluded.updated_ts`,
		string(b), clock.TodayTS())
	return err
}

func (s *Store) LoadSchedule() (*WeekSchedule, error) {
	var raw string
	err := s.DB.QueryRow(`SELECT week_json FROM convo_schedule WHERE id=1`).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ws := &WeekSchedule{}
	if err := json.Unmarshal([]byte(raw), ws); err != nil {
		return nil, fmt.Errorf("parse schedule: %w", err)
	}
	return ws, nil
}

// ——— mood ———

func (s *Store) SaveMood(state, note string) error {
	_, err := s.DB.Exec(`INSERT INTO convo_mood(id, state, since_ts, note)
		VALUES(1, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET state=excluded.state, since_ts=excluded.since_ts, note=excluded.note`,
		state, clock.TodayTS(), note)
	return err
}

// ——— profile ———

func (s *Store) SaveProfile(p *PersonaProfile) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(`INSERT INTO convo_profile(id, name, persona_json, created_ts, updated_ts)
		VALUES(1, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, persona_json=excluded.persona_json,
			updated_ts=excluded.updated_ts`,
		p.Name, string(b), p.CreatedTS, clock.TodayTS())
	return err
}

func (s *Store) LoadProfile() (*PersonaProfile, error) {
	var raw, name string
	err := s.DB.QueryRow(`SELECT name, persona_json FROM convo_profile WHERE id=1`).Scan(&name, &raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p := &PersonaProfile{}
	if err := json.Unmarshal([]byte(raw), p); err != nil {
		return nil, fmt.Errorf("parse profile: %w", err)
	}
	return p, nil
}

// ——— memory seed (§8.2 步骤 6: 纪念日=今天) ———

func (s *Store) InsertMemorySeed(m *MemorySeed) error {
	_, err := s.DB.Exec(`INSERT OR IGNORE INTO convo_memory
		(mem_id, k, v, label, kind, description, confidence, created_ts, updated_ts)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.MemID, m.K, m.V, m.Label, m.Kind, m.Description, m.Confidence,
		clock.TodayTS(), clock.TodayTS())
	return err
}

// ——— messages ———

type MessageRecord struct {
	// §6.10.3 多主体发言流的三件套。**私聊里它们可以全为空** ——
	// 那时 recentDialog 退回"你:/她:"二元 (只有一个主体, 加 uin 是噪声)。
	Origin    string
	SceneKey  string
	SenderUIN string
	// AtUINs 这条消息 @ 了谁 (§6.9 信号 ②)。空 = 没 @ 任何人。
	AtUINs []string

	ID      int64  `json:"id"`
	TS      string `json:"ts"`
	Dir     string `json:"dir"` // "ai" | "user"
	Channel string `json:"channel"`
	Kind    string `json:"kind"` // text | sticker | image | status
	Payload string `json:"payload"`
}

// 消息来源 (§9 分区)。**它是"这句话是谁说的"的唯一权威** ——
// 少了它, 夜间批算出来的 AI↔AI 对话会被 L1 上下文当成真人说过的话。
const (
	OriginUser     = "user"       // 真人说的
	OriginAIToUser = "ai_to_user" // 它对真人说的 (含降级产物)
	OriginAIToAI   = "ai_to_ai"   // §3.7 夜间批算出来的 AI↔AI 对话
)

// DefaultSceneKey 单场景期的场景键 (群层落地后由调用方传真实的)。
const DefaultSceneKey = "private:owner"

// DefaultOriginFor 按 dir 推来源 —— **只为不改现有调用点**, 新代码请显式传。
func DefaultOriginFor(dir string) string {
	if dir == "user" {
		return OriginUser
	}
	return OriginAIToUser
}

func (s *Store) InsertMessage(dir, channel, kind, payload string) (int64, error) {
	return s.InsertMessageEx(dir, channel, kind, payload, DefaultOriginFor(dir), DefaultSceneKey)
}

// MsgMeta 一条消息的分区元信息 (§9)。
//
// 打包成一个结构而不是继续堆参数: InsertMessageEx 已经有 6 个位置参数,
// 再加第七个的话, 每个调用点都要重新数一遍 —— 而 P7 那次事故正是"数错了"。
type MsgMeta struct {
	Origin    string // user | ai_to_user | ai_to_ai (空 → 按 dir 推)
	SceneKey  string // 空 → 默认场景
	SenderUIN string // §6.10.3 群里的发言人; 私聊留空
	// AtUINs §6.9: 这条消息 @ 了谁。
	//
	// ⚠️ **它只服务于冲突四信号里的 ② 互相 @** —— 而那个信号原来是**算不出来**的,
	// 因为库里根本没存过 @ 目标。缺了它, 冲突门槛实际是 3/3。
	AtUINs []string
}

// InsertMessageFull 带完整分区信息的写入 (群路径与 §3.7 夜间批用这个)。
func (s *Store) InsertMessageFull(dir, channel, kind, payload string, meta MsgMeta) (int64, error) {
	return s.insertMessage(dir, channel, kind, payload, meta.Origin, meta.SceneKey, meta.SenderUIN, JoinAtUINs(meta.AtUINs))
}

// InsertMessageEx 带分区信息的写入。§3.7 的夜间批算**必须**用这个入口,
// 且 origin 恒为 OriginAIToAI —— 走默认那条会把 AI 之间的对话标成"它对真人说的"。
func (s *Store) InsertMessageEx(dir, channel, kind, payload, origin, sceneKey string) (int64, error) {
	return s.insertMessage(dir, channel, kind, payload, origin, sceneKey, "", "")
}

func (s *Store) insertMessage(dir, channel, kind, payload, origin, sceneKey, senderUIN, atUINs string) (int64, error) {
	if origin == "" {
		origin = DefaultOriginFor(dir) // 零值 = 未提供 → 按 dir 推, 不是"空来源"
	}
	if sceneKey == "" {
		sceneKey = DefaultSceneKey
	}

	res, err := s.DB.Exec(`INSERT INTO convo_messages(ts, dir, channel, kind, payload_json, origin, scene_key, sender_uin, at_uins)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`, clock.TodayTS(), dir, channel, kind, payload, origin, sceneKey, senderUIN, atUINs)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) RecentMessages(limit int) ([]MessageRecord, error) {
	rows, err := s.DB.Query(`SELECT id, ts, dir, channel, kind, payload_json,
			COALESCE(origin,''), COALESCE(scene_key,''), COALESCE(sender_uin,''), COALESCE(at_uins,'')
		FROM convo_messages WHERE COALESCE(origin,'') <> ? ORDER BY id DESC LIMIT ?`, OriginAIToAI, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MessageRecord
	for rows.Next() {
		var m MessageRecord
		var atRaw string
		if err := rows.Scan(&m.ID, &m.TS, &m.Dir, &m.Channel, &m.Kind, &m.Payload,
			&m.Origin, &m.SceneKey, &m.SenderUIN, &atRaw); err != nil {
			return nil, err
		}
		m.AtUINs = SplitAtUINs(atRaw)
		out = append(out, m)
	}
	return out, rows.Err()
}

// RecentMessagesIn 按场景取最近消息 —— **群路径必须用这个**。
//
// ⚠️ RecentMessages 是**全局**的 (没有 scene_key 过滤), 用它构造群里的 L1
// 会把 A 群的话、甚至私聊的话读进 B 群的上下文 —— 那正是 §6.4.2 禁止的跨人转述,
// 而且是**静默**的: 表现只是"它怎么知道那件事"。
func (s *Store) RecentMessagesIn(sceneKey string, limit int) ([]MessageRecord, error) {
	rows, err := s.DB.Query(`SELECT id, ts, dir, channel, kind, payload_json,
			COALESCE(origin,''), COALESCE(scene_key,''), COALESCE(sender_uin,''), COALESCE(at_uins,'')
		FROM convo_messages
		WHERE COALESCE(origin,'') <> ? AND COALESCE(scene_key,'') = ?
		ORDER BY id DESC LIMIT ?`, OriginAIToAI, sceneKey, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MessageRecord
	for rows.Next() {
		var m MessageRecord
		var atRaw string
		if err := rows.Scan(&m.ID, &m.TS, &m.Dir, &m.Channel, &m.Kind, &m.Payload,
			&m.Origin, &m.SceneKey, &m.SenderUIN, &atRaw); err != nil {
			return nil, err
		}
		m.AtUINs = SplitAtUINs(atRaw)
		out = append(out, m)
	}
	return out, rows.Err()
}

// ——— counters (试跑观察, 轻量计数) ———

func (s *Store) CounterInc(name, day string) (int, error) {
	_, err := s.DB.Exec(`INSERT INTO convo_counters(name, day, n, updated_ts)
		VALUES(?, ?, 1, ?)
		ON CONFLICT(name, day) DO UPDATE SET n=n+1, updated_ts=excluded.updated_ts`,
		name, day, clock.TodayTS())
	if err != nil {
		return 0, err
	}
	var n int
	err = s.DB.QueryRow(`SELECT n FROM convo_counters WHERE name=? AND day=?`, name, day).Scan(&n)
	return n, err
}

// SetCounter 直接写一个计数值 (用于**跨天的连续量**, 比如"连续几次自我失信")。
//
// ⚠️ 与 CounterInc 分开: 那个是"一天里第几次", 这个是"连着几天/几次"。
// 需要重置的地方(出口触发之后)必须有 setter, 否则那个计数器只会单调涨。
func (s *Store) SetCounter(name, day string, n int) error {
	if n < 0 {
		n = 0
	}
	_, err := s.DB.Exec(`INSERT INTO convo_counters(name, day, n, updated_ts)
		VALUES(?, ?, ?, ?)
		ON CONFLICT(name, day) DO UPDATE SET n=excluded.n, updated_ts=excluded.updated_ts`,
		name, day, n, clock.TodayTS())
	return err
}

// LastCounterTS 返回某计数器最近一次自增的时间 (ok=false = 从未记过)。
// updated_ts 由注入时钟盖章, 所以时间旅行下取到的也是仿真时间。
func (s *Store) LastCounterTS(name string) (time.Time, bool, error) {
	var ts sql.NullString
	if err := s.DB.QueryRow(`SELECT MAX(updated_ts) FROM convo_counters WHERE name=?`, name).Scan(&ts); err != nil {
		return time.Time{}, false, err
	}
	if !ts.Valid || ts.String == "" {
		return time.Time{}, false, nil
	}
	t, err := time.ParseInLocation("2006-01-02 15:04:05", ts.String, time.Local)
	if err != nil {
		return time.Time{}, false, err
	}
	return t, true, nil
}

func (s *Store) CounterGet(name, day string) (int, error) {
	var n int
	err := s.DB.QueryRow(`SELECT n FROM convo_counters WHERE name=? AND day=?`, name, day).Scan(&n)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return n, err
}

// OwnerKey 数据分区键 (§9) 的**单一来源**。
//
// ⚠️ 引擎按 Cfg.Channel.SelfQQ 算, 后台服务没有 Cfg —— 两边各算一遍,
// 表现是「管理页永远是空的」而引擎一切正常。所以落库一份, 双方都读它。
func (s *Store) OwnerKey() string {
	var v string
	if err := s.DB.QueryRow("SELECT v FROM convo_meta WHERE k = 'owner_key'").Scan(&v); err == nil {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return "default"
}

// SetOwnerKey 写分区键 (幂等)。
func (s *Store) SetOwnerKey(k string) error {
	_, err := s.DB.Exec("INSERT OR REPLACE INTO convo_meta (k, v) VALUES ('owner_key', ?)", k)
	return err
}
