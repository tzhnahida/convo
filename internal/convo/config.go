package convo

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the convo process configuration (configs/convo.yaml).
// The convo process is independent from the main app: its own DB
// (convo-trial.db), its own config, its own HTTP endpoint.
type Config struct {
	Listen  string          `yaml:"listen"`   // e.g. "0.0.0.0:8420" (Pi) or "127.0.0.1:8420" (local)
	DataDir string          `yaml:"data_dir"` // default "data"
	DBFile  string          `yaml:"db_file"`  // default "convo-trial.db"
	Token   string          `yaml:"token"`    // empty → generate & persist to <data_dir>/convo-token.txt
	AI      AIConfig        `yaml:"ai"`
	Channel ChannelConfig   `yaml:"channel"`
	Sched   SchedulerConfig `yaml:"scheduler"`
	// Heat §6.7 热力的旋钮 (cap / 半衰期 / 增益)。
	//
	// ⚠️ 整段缺省 = **全部走内置默认**, 不是"全部归零" ——
	// 零值纪律在 HeatConfig.Normalized() 里兑现, 而 Engine.heatCfg 负责调它。
	Heat     HeatConfig    `yaml:"heat"`
	Stickers StickerConfig `yaml:"stickers"`
	// §6.10 / §9 群聊: 白名单 / 角色 / 线程窗口。
	// ⚠️ 白名单的**唯一真源**就是它 —— convo_scene 不存 enabled (避免双真源)。
	Group GroupConfig `yaml:"group"`
	// §3.3 环境: 它那边的天气与城市。
	//
	// ⚠️ **这个字段是新加的, 而它补的是一个从没接过的线** (2026-10-14):
	// EnvSource 此前只在仿真和测试里被赋值过, 生产一次都没有 ——
	// 于是天气段不存在、世界事件 0 条、心情没有外部成因。
	Env EnvConfig `yaml:"env"`
}

type AIConfig struct {
	BaseURL     string `yaml:"base_url"`
	APIKey      string `yaml:"api_key"` // env CONVO_AI_KEY overrides
	Model       string `yaml:"model"`   // chat model, e.g. deepseek-chat
	VisionModel string `yaml:"vision_model"`
}

type ChannelConfig struct {
	Driver   string `yaml:"driver"`   // "onebot_v11" | "noop"
	APIBase  string `yaml:"api_base"` // NapCat HTTP API, e.g. http://127.0.0.1:3001
	WSListen string `yaml:"ws_listen"`
	// WSMode "" | "server" | "client"。**零值 = server** (既有行为逐字节不变)。
	// server = 反向 WS (桥接方连进来); client = 正向 WS (我们连出去)。
	WSMode string `yaml:"ws_mode"`
	// WSURL client 模式要连的地址, 例: ws://127.0.0.1:3102/ws
	WSURL       string `yaml:"ws_url"` // our reverse-WS endpoint for events, e.g. 127.0.0.1:8421
	AccessToken string `yaml:"access_token"`
	OwnerQQ     string `yaml:"owner_qq"` // 用户的 QQ 号 — 消息裁决只对它生效 (empty → 仅记录)
	SelfQQ      string `yaml:"self_qq"`  // 小夜自己的 QQ 号 (登录后从 get_login_info 回填)
}

type SchedulerConfig struct {
	TickEverySec   int `yaml:"tick_every_sec"`       // default 60
	ActiveQuotaDay int `yaml:"active_quota_per_day"` // default 2 — 主动开场配额/天
	// §8.3 情绪污染: 意愿低于此值时当日主动配额 -1 (幅度上限就是这 1)。
	MoodLowWillingness float64 `yaml:"mood_low_willingness"` // default 0.45
	// B6 开口冲量下限: 低于它就不开口 (配额只是上限, 不是触发)。default 0.25
	OpenImpulseMin     float64 `yaml:"open_impulse_min"`
	SleepStart         string  `yaml:"sleep_start"`           // default "23:30"
	SleepEnd           string  `yaml:"sleep_end"`             // default "07:30"
	DegradeDaysNoReply int     `yaml:"degrade_days_no_reply"` // default 3 — 无回复降频门槛
	BusyDelayMin       int     `yaml:"busy_delay_min"`        // default 30
	BusyDelayMax       int     `yaml:"busy_delay_max"`        // default 90
	SlackStealPct      int     `yaml:"slack_steal_pct"`       // default 10 — 摸鱼偷回概率 %
	NoInputMaxDay      int     `yaml:"no_input_max_per_day"`  // default 1 — 静默≥24h 且 free 槽的 noInput 上限
	OpenMinGapMin      int     `yaml:"open_min_gap_min"`      // default 240 — 两条主动开场之间的最小间隔(分钟)
	OpenJitterPct      int     `yaml:"open_jitter_pct"`
	// §6.7 热力: 每条**主动**消息的代价。<=0 → 内置 25 (HeatQuota 内部兜底, 不是免费)
	HeatCostPerMsg float64 `yaml:"heat_cost_per_msg"` // default 25 — 每条**主动**消息扣多少热力 (§6.7)

	// §3.6 事件池生成预算。零值 → 内置默认 (eventgen.go 的 withDefaults 是**唯一**解释它的地方)。
	// ⚠️ 这三项此前**没有任何入口**: SetEventGenBudget 零调用, 于是"预算"永远是内置默认值 ——
	// 想改一次调用数就得改代码重编译。
	EventGenCallsPerDay  int `yaml:"event_gen_calls_per_day"` // default 3
	EventGenCandidates   int `yaml:"event_gen_candidates"`    // default 5 (硬上限 8)
	EventGenLookbackDays int `yaml:"event_gen_lookback_days"` // default 3
}

type StickerConfig struct {
	SearchDriver  string  `yaml:"search_driver"` // "firecrawl" | "tenor" | "builtin" | "none"
	TenorAPIKey   string  `yaml:"tenor_api_key"`
	FirecrawlBase string  `yaml:"firecrawl_base"` // e.g. https://api.firecrawl.dev
	VisionEnabled bool    `yaml:"vision_enabled"`
	Lang          string  `yaml:"lang"`            // 表情包搜索语言, e.g. "ja"
	Dir           string  `yaml:"dir"`             // data/convo/stickers
	ScoreMin      float64 `yaml:"score_min"`       // vision 入选阈值 0-1, default 0.6
	CapPerSticker float64 `yaml:"cap_per_sticker"` // 单张发送概率封顶, default 0.30
	PoolCap       int     `yaml:"pool_cap"`        // 喜欢位上限 (超出按 affinity 退役), default 40
	BuiltinDir    string  `yaml:"builtin_dir"`     // 内置初始集目录
}

func DefaultConfig() Config {
	return Config{
		Listen:  "127.0.0.1:8420",
		DataDir: "data",
		DBFile:  "convo-trial.db",
		AI: AIConfig{
			BaseURL: "https://api.deepseek.com",
			Model:   "deepseek-chat",
		},
		Channel: ChannelConfig{
			Driver:   "noop",
			APIBase:  "http://127.0.0.1:3001",
			WSListen: "127.0.0.1:8421",
		},
		Sched: SchedulerConfig{
			TickEverySec:       60,
			ActiveQuotaDay:     2,
			MoodLowWillingness: 0.45,
			OpenImpulseMin:     0.25,
			SleepStart:         "23:30",
			SleepEnd:           "07:30",
			DegradeDaysNoReply: 3,
			BusyDelayMin:       30,
			BusyDelayMax:       90,
			SlackStealPct:      10,
			NoInputMaxDay:      1,
			OpenMinGapMin:      240,
			OpenJitterPct:      10,
			// 事件池预算: 默认值**引用 eventgen.go 的常量**, 不另抄一份 (抄了必然分叉)。
			EventGenCallsPerDay:  EventGenDefaultCallsPerDay,
			EventGenCandidates:   EventGenDefaultCandidates,
			EventGenLookbackDays: EventGenDefaultLookback,
		},
		Stickers: StickerConfig{
			SearchDriver:  "builtin",
			FirecrawlBase: "https://api.firecrawl.dev",
			VisionEnabled: false,
			Lang:          "ja",
			Dir:           "data/convo/stickers",
			ScoreMin:      0.6,
			CapPerSticker: 0.30,
			PoolCap:       40,
		},
	}
}

// Load reads convo.yaml (if present) over defaults. API key falls back to
// env CONVO_AI_KEY (mirrors the main app's VOCABLOCK_AI_KEY convention —
// never commit real keys).
func Load(path string) (Config, error) {
	cfg := DefaultConfig()
	// §6.10 群配置**先铺底再 overlay** —— `DefaultGroupConfig()` 存在的理由就是这一行。
	// ⚠️ 在它之前那个函数是**零调用**的 (它的注释写着"先铺底", 而没有人在铺):
	// 于是一旦 DefaultGroupConfig 里加了新的非零默认, 它不会生效 —— 而看起来一切正常。
	// ⚠️ 数值项的兜底仍然在 overlay **之后** (见下面 NormalizeGroupConfig 那段)。
	cfg.Group = DefaultGroupConfig()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil // defaults only
		}
		return cfg, fmt.Errorf("read config: %w", err)
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config: %w", err)
	}
	// §6.10 群配置的数值兜底**必须在 overlay 之后** ——
	// 放进 DefaultConfig 的话, yaml 里显式写 0 的项会被"默认值 + yaml 的 0"覆盖成 0,
	// 于是"配错了"和"没配"长得一模一样。
	cfg.Group = NormalizeGroupConfig(cfg.Group)
	if v := os.Getenv("CONVO_AI_KEY"); v != "" {
		cfg.AI.APIKey = v
	} else if v := os.Getenv("VOCABLOCK_AI_KEY"); v != "" { // 主程序的既有约定, 复用同一把 key
		cfg.AI.APIKey = v
	}
	if cfg.AI.BaseURL == "" {
		cfg.AI.BaseURL = "https://api.deepseek.com"
	}
	if cfg.AI.Model == "" {
		cfg.AI.Model = "deepseek-chat"
	}
	if cfg.Sched.TickEverySec <= 0 {
		cfg.Sched.TickEverySec = 60
	}
	if cfg.Sched.ActiveQuotaDay <= 0 {
		cfg.Sched.ActiveQuotaDay = 2
	}
	if cfg.Sched.MoodLowWillingness <= 0 {
		cfg.Sched.MoodLowWillingness = 0.45
	}
	if cfg.Sched.OpenImpulseMin <= 0 {
		cfg.Sched.OpenImpulseMin = 0.25
	}
	if cfg.Stickers.ScoreMin <= 0 {
		cfg.Stickers.ScoreMin = 0.6
	}
	if cfg.Stickers.CapPerSticker <= 0 || cfg.Stickers.CapPerSticker > 1 {
		cfg.Stickers.CapPerSticker = 0.30
	}
	return cfg, nil
}

// WSURLWithToken 给 client 模式的连接地址补上 access_token。
//
// ⚠️ 只在**没有**时才补 —— 重复拼一个会让服务端读到错的那个
// (dashboard 上踩过同一个坑: 双问号 → token 丢失 → 401), 而现象是连不上。
func (c ChannelConfig) WSURLWithToken() string {
	u := strings.TrimSpace(c.WSURL)
	if u == "" {
		return ""
	}
	if c.AccessToken == "" || strings.Contains(u, "access_token=") {
		return u
	}
	sep := "?"
	if strings.Contains(u, "?") {
		sep = "&"
	}
	return u + sep + "access_token=" + c.AccessToken
}
