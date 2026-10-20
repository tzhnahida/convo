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
	// 戳一戳: 对方戳它, 它怎么回。默认关 (同自我表达: 代码上船, 线上先不动)。
	Poke PokeConfig `yaml:"poke"`
	// 对外自我表达: 群名片 / 头像 / 昵称 / 说说。
	//
	// ⚠️ 默认 `enabled: false` —— 代码先上船, 线上一个动作都不发生。
	// 开了之后: 头像/说说/群名片**由它自己判** (视觉闸 + 它自己的 YES/NO), 决策理由全部落库。
	SelfExpress SelfExpressConfig `yaml:"self_express"`
	// 审核机: 每条输出在发出去之前过一遍"人设 + 规则"的判定。
	// ⚠️ 与 coach.go 的「教练: 规则」不是一回事 —— 那是**规则从哪来**, 这是**规则怎么用**。
	Review ReviewConfig `yaml:"review"`
	// 会话判断机: 每 5 分钟问一次「还在聊同一件事吗」(一次极小 LLM 调用), 用来维持
	// 30 分钟的会话窗口 —— 没有 @ 但话题没结束的时候, 它不该装死。
	Session SessionConfig `yaml:"session"`
}

// ReviewConfig 审核机 (输出前的第二双眼睛)。
//
// 一次审核 = 一次模型调用。默认**开** (owner 2026-10-18 要的), 但它永远 fail-open:
// 超时/挂了/输出看不懂 → 放行。真红线在 hardreject.go, 这里是质量闸门。
type ReviewConfig struct {
	Enabled    bool `yaml:"enabled"`
	TimeoutSec int  `yaml:"timeout_sec"` // 默认 20
}

// Normalized 零值纪律: 缺省 = 内置默认。
func (c ReviewConfig) Normalized() ReviewConfig {
	if c.TimeoutSec <= 0 {
		c.TimeoutSec = 20
	}
	return c
}

// PokeConfig 戳一戳。
//
// 三条设计 (owner 2026-10-16「戳一戳这个来回你想想怎么弄」):
//
//	① **不是每次一个样** —— 每次都秒回同一个动作, 那是摇铃不是人;
//	② **「装死」是功能不是故障** —— 戳一戳的乐趣一半在于对方可能不理你;
//	③ **限流必须有** —— 没有它, 连戳会把消息刷满, 而且踩风控。
type PokeConfig struct {
	Enabled     bool `yaml:"enabled"`
	CooldownMin int  `yaml:"cooldown_min"` // 同一个人多久内只回一次
	DailyCap    int  `yaml:"daily_cap"`    // 每天最多回几次
	PokePct     int  `yaml:"poke_pct"`     // 戳回去的概率 %
	LinePct     int  `yaml:"line_pct"`     // 搭一句话的概率 % (剩下的是装死)
	// SleepUnlessTimes 睡着时被同一个人连戳几次才算急事 (0 = 睡着就一律不理)。
	SleepUnlessTimes int `yaml:"sleep_unless_times"`
	// DelayMaxSec 回之前随机等几秒 (真人不会零延迟; 测试里设 0 就是立刻)。
	DelayMaxSec int `yaml:"delay_max_sec"`
}

// Normalized 零值纪律: 整段缺省 = 内置默认, 不是「全部归零」。
func (c PokeConfig) Normalized() PokeConfig {
	if c.CooldownMin <= 0 {
		c.CooldownMin = 10
	}
	if c.DailyCap <= 0 {
		c.DailyCap = 20
	}
	if c.PokePct <= 0 {
		c.PokePct = 70
	}
	if c.LinePct <= 0 {
		c.LinePct = 25
	}
	if c.SleepUnlessTimes <= 0 {
		c.SleepUnlessTimes = 3
	}
	return c
}

// SelfExpressConfig 对外自我表达: 群名片 / 头像 / 昵称 / 说说。
//
// 三种模式 (每个动作各自可配):
//
//	auto    它自己判 —— 过视觉闸 + 它自己的 YES/NO 判断 → 直接做
//	propose 先问 owner: 在私聊里说「我想换这个」, 回一个字才做
//	off     这个动作完全不碰
//
// 默认 (owner 2026-10-16 定): 头像 / 说说 / 群名片 = auto (「控制权交给 ai」),
// 昵称 = propose —— 昵称是**全局**的, 会出现在所有好友和所有群里。
//
// ⚠️ 群名片默认文案**固定**(不挂状态后缀) —— owner 明确选的 plain。
type SelfExpressConfig struct {
	Enabled  bool   `yaml:"enabled"`
	Avatar   string `yaml:"avatar"`
	Nickname string `yaml:"nickname"`
	Qzone    string `yaml:"qzone"`
	Card     string `yaml:"card"`
	// CardText 群名片固定文案。空 = 用 group.self_aliases 的第一个 (通常是「小夜」)。
	CardText string `yaml:"card_text"`
	// NicknameText 账号昵称文案 (空 = 人格名 → self_aliases 第一个)。
	//
	// ⚠️ 昵称是**全局**的 (所有好友与群都看得见), 所以它的限频最长 (默认 90 天):
	// 换人格时它该跟着换, 但「隔三差五改名」是机器行为, 不是人。
	NicknameText string `yaml:"nickname_text"`
	// 限频。三条理由: 风控 (账号级动作比发消息显眼)、人味 (频繁换头像/天天发说说
	// 是典型的机器行为)、以及"每天都做同一件事"会让它变成一台发布机。
	AvatarMinDays   int `yaml:"avatar_min_days"`
	QzoneMinDays    int `yaml:"qzone_min_days"`
	CardMinHours    int `yaml:"card_min_hours"`
	NicknameMinDays int `yaml:"nickname_min_days"`
	// DailyCap 每天最多几次对外动作 (所有 kind 合计)。
	DailyCap int `yaml:"daily_cap"`
	// Images 图从哪来 —— 见 ImageSourceConfig。
	Images ImageSourceConfig `yaml:"images"`
}

// Mode 归一化一个动作档位 (空 = 调用方给的默认)。
func (c SelfExpressConfig) Mode(kind, def string) string {
	var v string
	switch kind {
	case "avatar":
		v = c.Avatar
	case "nickname":
		v = c.Nickname
	case "qzone":
		v = c.Qzone
	case "card":
		v = c.Card
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "auto", "propose", "off":
		return strings.ToLower(strings.TrimSpace(v))
	}
	return def
}

// ImageSourceConfig 图片来源 —— **可插拔**: 换站点 = 改配置, 不碰代码。
//
// source 的四个取值:
//
//	none   不抓图 (默认)
//	pixiv  官方 ajax 搜索 (安全模式: mode=safe + xRestrict==0 + R18 词表)
//	json   任何站点的 JSON 接口 (URL 模板 + 点路径 + 字段名)
//	html   任何站点的页面 (正则抓图; 纯 stdlib, 零第三方依赖)
type ImageSourceConfig struct {
	Source string `yaml:"source"`
	// Dir 图存哪 (默认 <data_dir>/convo/self)。
	Dir string `yaml:"dir"`
	// Proxy 抓图走的代理, 例如 http://127.0.0.1:2026 (pixiv 在国内必须走它)。
	// ⚠️ 只作用于**图片抓取**这一个 http.Client —— 不碰 AI 调用与通道。
	Proxy     string `yaml:"proxy"`
	UserAgent string `yaml:"user_agent"`
	// PerRun 每轮最多抓几张 (每张都要过视觉闸, 所以这也是花销上限)。
	PerRun int `yaml:"per_run"`
	// RoleQuery / Keyword: owner 选了「角色名 + 关键词都要」→ 两个都搜, 结果合并去重。
	RoleQuery string `yaml:"role_query"`
	Keyword   string `yaml:"keyword"`
	// R18 默认 false。关着的时候: pixiv 搜索走安全模式 + 结果里 xRestrict!=0 一律丢 +
	// 标签/标题命中 R18Words 一律丢。开着 = 只在源头放宽搜索模式, **视觉闸仍然照拦**。
	R18      bool     `yaml:"r18"`
	R18Words []string `yaml:"r18_words"`
	// VisionRequired 默认 true。**关了它等于放弃那道闸** —— 启动时会大声警告一行。
	VisionRequired *bool            `yaml:"vision_required"`
	VisionMin      float64          `yaml:"vision_min"`
	Pixiv          PixivConfig      `yaml:"pixiv"`
	JSON           JSONSourceConfig `yaml:"json"`
	HTML           HTMLSourceConfig `yaml:"html"`
}

// PixivConfig pixiv 搜索与下载的细节。
type PixivConfig struct {
	// Base 站根 (默认 https://www.pixiv.net)。换成镜像/反代就改这里。
	Base string `yaml:"base"`
	// Cookie 可选。公开搜索多数情况不用登录; 被要求登录时填 `PHPSESSID=...`。
	Cookie string `yaml:"cookie"`
	// Order: date_d (最新) | date (旧→新) | popular_d (热度) | popular_male_d …
	Order string `yaml:"order"`
}

// JSONSourceConfig 任何站点的 JSON 接口。
//
// URL 里的 {query} / {n} 会被替换; ArrayPath 是点路径 (例如 data.items);
// 其余字段填该数组里元素的键名。
type JSONSourceConfig struct {
	URL         string            `yaml:"url"`
	ArrayPath   string            `yaml:"array_path"`
	URLField    string            `yaml:"url_field"`
	PageField   string            `yaml:"page_field"`
	TitleField  string            `yaml:"title_field"`
	AuthorField string            `yaml:"author_field"`
	TagsField   string            `yaml:"tags_field"`
	R18Field    string            `yaml:"r18_field"`
	Headers     map[string]string `yaml:"headers"`
}

// HTMLSourceConfig 任何站点的页面 (零依赖正则抓图)。
type HTMLSourceConfig struct {
	URL string `yaml:"url"`
	// Pattern 必须含**一个捕获组** = 图片地址。相对地址会按 Base 补全。
	Pattern string `yaml:"pattern"`
	// R18Pattern 命中的整段直接丢弃 (可选)。
	R18Pattern string            `yaml:"r18_pattern"`
	Base       string            `yaml:"base"`
	Headers    map[string]string `yaml:"headers"`
}

// Normalized 零值纪律: 整段缺省 = 走内置默认, 不是「全部归零」。
func (c SelfExpressConfig) Normalized() SelfExpressConfig {
	if c.AvatarMinDays <= 0 {
		c.AvatarMinDays = 30
	}
	if c.QzoneMinDays <= 0 {
		c.QzoneMinDays = 7
	}
	if c.CardMinHours <= 0 {
		c.CardMinHours = 20
	}
	if c.NicknameMinDays <= 0 {
		c.NicknameMinDays = 90
	}
	if c.DailyCap <= 0 {
		c.DailyCap = 2
	}
	c.Images = c.Images.Normalized()
	return c
}

// Normalized 图源默认值。
func (c ImageSourceConfig) Normalized() ImageSourceConfig {
	if c.PerRun <= 0 {
		c.PerRun = 6
	}
	if c.VisionMin <= 0 {
		c.VisionMin = 0.6
	}
	if c.UserAgent == "" {
		c.UserAgent = "Mozilla/5.0 (compatible; convo/1.0)"
	}
	if len(c.R18Words) == 0 {
		c.R18Words = []string{"R-18", "R18", "R-18G", "18禁", "成人向", "エロ", "成人漫画"}
	}
	if c.Pixiv.Base == "" {
		c.Pixiv.Base = "https://www.pixiv.net"
	}
	if c.Pixiv.Order == "" {
		c.Pixiv.Order = "date_d"
	}
	return c
}

// VisionRequiredOn 默认 true —— 只有显式写 false 才关得掉。
func (c ImageSourceConfig) VisionRequiredOn() bool {
	return c.VisionRequired == nil || *c.VisionRequired
}

type AIConfig struct {
	BaseURL string `yaml:"base_url"`
	APIKey  string `yaml:"api_key"` // env CONVO_AI_KEY overrides

	// Model 是**交互级**模型 (用户正在等)。思考关闭 —— 只有关掉 thinking,
	// temperature 才真的生效, 而那正是拟人化抖动的来源 (2026-10-18 定)。
	Model string `yaml:"model"`

	// ThinkModel 是**后台级**模型 (做梦 / 日计划 / 世界事件 / 学习摘要):
	// 没有人在等, 允许开思考换质量。空 = 与 Model 相同。
	ThinkModel string `yaml:"think_model"`

	// VisionModel 看图用的模型。flash 系列原生支持图像输入, 默认与 Model 相同。
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
			BaseURL:     "https://api.deepseek.com",
			Model:       "deepseek-flash", // V4.1-Flash; deepseek-chat 已下线
			ThinkModel:  "deepseek-flash", // 后台生成: 同模型, 开思考
			VisionModel: "deepseek-flash", // 原生多模态, 不再需要 experimental 视觉模型
		},
		// 审核机默认开 —— 它只在有文本可审时花一次调用, 且永远 fail-open。
		Review:  ReviewConfig{Enabled: true},
		Session: SessionConfig{Enabled: true},
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
	// 模型档位 (2026-10-18 更新; `deepseek-chat` 已从 /models 下线):
	//   deepseek-flash  = V4.1-Flash, 1M 上下文, **原生支持图像输入**
	//   deepseek-v4-pro = V4-Pro, 1M 上下文, 纯文本
	// 对话用 flash: 快、便宜、自带多模态 —— 词表外的 experimental 视觉模型可以退役。
	if cfg.AI.Model == "" {
		cfg.AI.Model = "deepseek-flash"
	}
	if cfg.AI.ThinkModel == "" {
		cfg.AI.ThinkModel = cfg.AI.Model
	}
	if cfg.AI.VisionModel == "" {
		cfg.AI.VisionModel = cfg.AI.Model
	}
	cfg.Review = cfg.Review.Normalized()
	cfg.Session = cfg.Session.Normalized()
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
