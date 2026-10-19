package convo

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/tzhnahida/convo/internal/clock"
	"github.com/tzhnahida/convo/internal/log"
)

// ——— 模块 5: 对话引擎 (§8.5 六段注入 + 帧协议) ———

type DialogInput struct {
	UserText  string // 本轮用户消息 (主动开场时为空)
	History   string // L1 最近对话 (组装好的 "你:/她:" 文本, 保持上下句联系)
	State     string // 当前状态 (schedule 裁决)
	SlotLabel string // 日程槽人话描述 (注入用; Fact 非空时退居备用)
	// PreviewNext / PreviewAt 预告要进入的**下一个**槽 (状态 + 起点 HH:MM)。
	// ⚠️ 与 SlotLabel 不是一回事: SlotLabel 是"现在在哪一格", 这里是"马上要去哪一格"。
	PreviewNext string
	PreviewAt   string
	Fact        *StateFact  // A2: 系统事实三元组。**唯一权威的状态来源** —— 模型只许润色
	Mood        *MoodState  // P4: 系统持有的心情 (§4.1)。模型只许表达, 不许自述与发明
	EventNotes  []EventNote // §3.6.8: 还在场上的世界事件 (附沉降阶段)
	ActionKind  string      // tick 动作 (active_open/preview/return_greet/…)
	Topic       Topic       // 本轮任务 (pure → 无话题)
	Memories    []Memory    // 关系记忆 top-K (注入带 mem_id)
	Pending     []Memory    // 待求证缓冲
	LastSummary string      // L2 上一会话摘要
	IsProactive bool        // 主动开场 (无用户输入)
	Teaching    bool        // 教学态 (求助检测触发)
	Rejection   string      // 最近一次 ask_share 被拒类别 (小失望上下文)
	Lang        string      // 目标语言 (默认 ja)
	Slangs      []Slang     // 模块 11: 你会的梗 (黑话表, 注入 top-K)
	SlangHit    string      // 模块 11: 用户此言命中的梗 (接住感知)

	// §4.2 身体: **必须落在提示词里**, 否则这一层就只是"库里有个数"。
	// 与 Mood 同源 (系统持有, 模型只表达), 所以紧跟在心情之后注入。
	Body *BodyState

	// §4.3/§5.5 日级处境 (拒绝许可 / 社交疲劳 / 待履行约定): **约束不是素材**。
	// 位置固定在天档 (6a 之后、6b 之前), 见 SituationLine 的文件头。
	DayNote string

	// §4.5 层 2 今日行程。**日程 / 系统事实** —— 按 §4.6.2 的边界必须给全:
	// 它就是被"它手里根本没有事实"逼出来的那一类, 糊掉会立刻退回 21:46 说刚醒那个病灶。
	// 零值(那天没排) → 段消失, 一个字都不加。
	DayPlan DayPlan

	// §4.2.4 口渴这一轮给不给"随口说"的例外。
	//
	// ⚠️ 由**引擎**判(预算 + 冷却), 不在这里算 —— 提示词只消费这个布尔。
	ThirstMaySpeak bool

	// §4.2.2 身体的三个连续量。零值 = **基线**(不是 0, 也不是"未提供")——
	// 由 ComputeAxes 保证, 调用方读不到事实时也应当拿到基线。
	BodyAxes Axes

	// §5.5 自主预约的校验素材:
	//   · DayPlanTomorrow: 撞不撞用户约定;
	//   · Week: 排没排在睡觉的时间。
	DayPlanTomorrow DayPlan
	Week            *WeekSchedule
	// ⚠️ 这里原来还有一格 `SelfPlansToday int` —— **它按设计就没打算被读**:
	// 自主预约的**上限在写路径判**(见 selfplan.go), 而校验链只判"撞不撞用户约定 /
	// 排没排在睡觉的时间"。于是那一格被每轮赋值、从没人读,
	// 是一颗等着下一个人以为它在生效的雷。已删。

	// §4.6.3 时间感知: true = 这一轮它**主动"看了一下"时间**, 段 9 才给精确时刻。
	// 默认 false —— 给的是模糊时段(「晚上十点多」), 与帧里的 look_time 配套。
	TimePrecise bool

	// §6.10.3 场景键: **群路径必须传** —— 它决定 L1 取谁的历史。
	// 空 = 私聊 (既有行为, 逐字节不变)。
	//
	// ⚠️ 漏传的后果是**静默**的: 表现只是"它怎么知道那件事" ——
	// 那正是 §6.4.2 禁止的跨人转述。
	SceneKey string
	// SpeakerUIN 群里的发言人 (§6.10.1: 身份锚是 uin, 不是昵称)。
	SpeakerUIN string
	// PersonNote §6.10.3 **主体分区注入**: 「关于**这一轮说话的人**」那一块。
	//
	// ⚠️ 它与 `Memories` **不是一回事**: `Memories` 是**按场景**取的(整个群的),
	// 而这一块是**按人**取的 —— 设计稿要的是后者 + 群公共层, 不是"整个群的记忆"。
	// ⚠️ 空串 = 没有(没人/没记忆) → 一个字都不进提示词。
	PersonNote string
	// Stance §6.3 的三态裁决结果 (空 = 私聊路径, 不做价值判断)。
	Stance SpeakStance
	// StanceReason 裁决理由的原话 —— **只进轨迹, 不进 prompt**。
	StanceReason string
	// SelfPlace §6.10.6「你在群里的位置」(已渲染)。空 = 私聊或不注入。
	SelfPlace string

	// §6.6 群聊角色位 (稳定档 —— 换群才变, 所以与 SelfPlace 同档)。
	// ⚠️ 它是**身份**不是判据: "能不能开口"由 GroupRoleNeedBonus 落在门槛上,
	// 这里只告诉模型"你是哪种人"。
	RoleNote string

	// §6.4.3 + §7.4 边界记忆 (慢档): 已经被纠正过三次以上的事。
	// ⚠️ 这里是**已渲染**的行 (强度档由库里时刻决定, 见 LoadBoundaryLines)。
	Boundaries []string

	// §5.1 + §6.8 关系 (天档): 亲密度/敬意算出来的一句话。
	BondNote string

	// §4.4.1b 语域漂移 (天档): "她最近说话变短变硬了"。
	RegisterNote string

	// §7.5 意图轨 (天档): **TA 明确说过的**打算/偏好/边界。
	// ⚠️ 与 Memories 分开: 那些是它的记忆, 这些是 TA 的原话。
	IntentNotes []string

	// §7.5 **群公共层** (天档): 群里大家共同的约定/梗/经历。
	// ⚠️ 渲染里**没有 uin、没有 owner 标记** (SceneMemoryInjection 的注释):
	// 这一层是「所有人都看得到」的那一侧, 混进个人标识就等于跨人转述。
	SceneNotes []string

	// 无来由的低意愿日。**只说行为, 不说原因** —— 它没有归因, 也不许编一个。
	DayMoodNote string

	// §10.3 依赖护栏: 平时不出现在提示里, 危机当轮出现。
	GuardNote string

	// §6.9 冲突介入的**执行指令** (轮级) —— 判据层只说"要不要", 这里说"怎么劝"。
	ConflictNote string

	// §2.5 段 4b【你自己的方向】(**周**档 —— 学习每周 2-3 次才会改)。
	TasteNote string

	// §2.5.3 段 7.4【这一轮可以顺口带一句】(每轮档)。
	RevealNote string

	// §7.2 联想 (轮级): 由**当下的线索**撞出来的一段旧事。
	Association string

	// §5.5 主体性: 这一轮系统给它的"可以怎样"。
	// nil = 没接线 = **不违抗也不许约** (零值必须是最保守的那一侧)。
	Agency *AgencyContext
	// ImagePath 用户这一轮发来的图。空 = 纯文本。
	// ⚠️ 图**直接进对话调用**, 不走"先转述成文字再对话" —— 见 DialogEngine.visionOrChat。
	ImagePath string
	// LiveNote 这一轮它**刚去查的**实时材料 (或"什么都没查到"的指令)。
	// ⚠️ 它变化最快, 所以注入位置要**靠后** (段序即缓存策略)。
	LiveNote string
	// CoachNote owner **亲口纠正过**的规则 (§教练机)。
	// ⚠️ 它比边界更硬: 边界是"被纠正三次以上的沉淀", 而这是**人直接下的指令**。
	CoachNote string
	// VoiceNote 这个群怎么说话 (§群语料)。**慢变段** —— 放在语言规则那一带。
	VoiceNote string
	// RepeatNote 它自己反复提过的东西 (§3.6.8「一致 ≠ 反复主动提」)。
	// ⚠️ 它**变的最快** (12 小时窗), 所以和 LiveNote 一起放在最后一批轮级段。
	RepeatNote string
	// NewsNote 它自己刷到的世界新闻 (§3.5 真实数据源)。**天档** —— 变化慢。
	NewsNote string
	// WorldNote 世界观 (§2.3)。**稳定段** —— 只在重新定稿时变。
	//
	// ⚠️ 它曾经完全没被注入过 (production 里 WorldPrompt 零调用),
	// 于是它有一份自己没读过的世界观 —— 被问起只能现编, 前后对不上。
	WorldNote string
}

// AgencyContext §5.5: 违抗与约定的**系统许可**。
//
// 与决策器同一条铁律: 什么时候说话/能不能拒绝由系统定, 模型只决定怎么说。
// 所以这里给的是**上界**, 不是建议 —— 模型只能在这个上界内挑一档。
type AgencyContext struct {
	Defiance DefianceMode // 系统许可的最高档 (DefyNone = 本轮不许违抗)
	Stance   string       // 命中的立场 (人话; 空 = 没有依据 → 本来也不会有许可)
	TopicKey string       // "同一件事"的键 (违抗账本用)

	Pending   []Appointment // 待履行约定 (日级段渲染)
	OwedComp  bool          // 欠一次找补
	MayCommit bool          // 还允许再约吗 (硬闸门)
}

// DialogEngine 组装六段 + 校验帧。
// VisionChatter 视觉调用口。
//
// ⚠️ 与 Chatter **分开**: 不是所有 Chatter 实现都有视觉 (测试替身、sim 都是纯文本),
// 合并的话每一个替身都得实现一个用不到的 ChatVision。
type VisionChatter interface {
	ChatVision(ctx context.Context, model, system, user, imagePath string) (string, error)
}

type DialogEngine struct {
	Store *Store
	// Vision / VisionModel 配了才能"看图说话"; nil = 退回纯文本。
	Vision      VisionChatter
	VisionModel string
	// Tools 工具口。nil = 纯文本 (测试替身 / sim 都是这样, 一切照旧)。
	Tools ToolRunner
	// ImagePath 本轮随对话一起送出的图片 (空 = 纯文本)。
	//
	// ⚠️ **每轮显式设置, 包括设置为空** —— 只在有图时设置的话,
	// 上一张图会**粘**到后面每一轮回复上, 而现象只是"它老提那张照片"。
	ImagePath string
	// StickerExists 由 stickers.go 提供 (见模块 8); 此处接口化避免循环依赖。
	StickerExists func(id int64) bool
	// Review 审核机入口 —— 由 Engine 在启动时一次性注入 (nil = 不审)。
	//
	// ⚠️ 用**函数字段**而不是让 DialogEngine 拿一份 Engine:
	// 这一层不该知道 config / 教练规则 / 计数器那些事, 它只负责"判一下就回来"。
	// 测试里构造 DialogEngine 时留空即可 —— 审核机不参与任何既有用例。
	Review func(ctx context.Context, in DialogInput, p *PersonaProfile, frame OutputFrame) Verdict
}

func (d *DialogEngine) buildSystem(in DialogInput, p *PersonaProfile, lang string) string {
	var b strings.Builder

	// ——— 布局 ——— 详细段序见下方 "段序 (§8.1)" 注释。一句话:
	//     逐字节稳定的段全部前置 → 变化段按变化速率升序 → 输出协议与【当前时间】置尾。

	// 段 1: 最高原则 + persona (风格锚点 = 口头禅/句长/语气词)
	b.WriteString("最高原则: 先像朋友、像真人, 再谈人设。为了保持人设而变得不像真人, 就是失败。\n")
	b.WriteString("你叫 " + p.Name + "。" + p.Description + "。\n")
	if p.Personality != "" {
		b.WriteString("性格: " + p.Personality + "。\n")
	}
	traits := []string{}
	if p.Catchphrase != "" {
		traits = append(traits, "口头禅:「"+p.Catchphrase+"」")
	}
	if p.SentenceLen != "" {
		traits = append(traits, "句长偏好: "+p.SentenceLen)
	}
	if p.ToneWords != "" {
		traits = append(traits, "语气词: "+p.ToneWords)
	}
	if len(traits) > 0 {
		b.WriteString("风格: " + strings.Join(traits, "; ") + "。这些是风格锚点, 必须保持一致。\n")
	}

	// ——— 段序 (§8.1 段序即缓存策略) ———
	// [实测 P1] 记忆段是**冻结窗口级**轮换的 (检索诱导遗忘的必然结果)。
	// 它原先排在 persona 正后方 —— 于是 94% 的调用都在它第一个字节处断裂, 前缀缓存被压到 8%。
	// 现在逐字节稳定的段落全部前置: 记忆怎么轮换, 都只砍掉它自己之后的部分。

	// §6.10.6「你在群里的位置」—— **并入稳定档, 紧接 persona 之后, 不新开段号**。
	//
	// 为什么是这一档: L929「**固定位 > 人设厚度**」—— 它换群才变, 与 persona 同级。
	// 新开段号只能插在段 1 之后, 那会把段 2/3 两个**稳定段**推到新断点之后,
	// **直接毁掉它们的前缀** (段序即缓存策略)。
	//
	// ⚠️ 里面**没有任何 owner 标记** —— 见 SelfRoleLine 的注释 ②。
	if in.SelfPlace != "" {
		b.WriteString(in.SelfPlace)
	}
	// §6.6 群聊角色位 —— 并入**同一个稳定档**: 它换群才变, 与 SelfPlace 同级。
	// 新开段号会把它之后的两个稳定段推过断点, 直接毁掉它们的前缀缓存。
	if in.RoleNote != "" {
		b.WriteString(in.RoleNote)
	}

	// 段 1b: 世界观 (§2.3) —— **紧跟人设**, 同属"我是谁"。
	// 变化极慢 (只在重新定稿时), 放前面不伤缓存。
	if in.WorldNote != "" {
		b.WriteString(in.WorldNote)
	}

	// 段 2: 三态示例 (稳定)
	b.WriteString(`
【示例对话】(三态: "不要"=反面教材, "你可以"/"或者"=要学的口吻。模仿: 短句、口语、像发微信, 不要书面语)
① ta: 好累啊
不要: 辛苦啦, 注意休息, 明天又是元气满满的一天!
你可以: 累了就睡 醒了继续累
或者: 我也是 刚被生活掏空
② ta: 给我推荐个电影
不要: 推你一部《xxx》 特别解压 保证好看
你可以: ？ 你要啥类型的
或者: 退钱吗 我推荐费很贵的
③ ta: 你是不是AI啊
不要: 我是AI助手, 很高兴为您服务。
你可以: 你猜
或者: 是鸭 是的话V我50解锁下一句
④ ta: 今天被老板骂了
不要: 别难过, 一切都会好起来的, 加油!
你可以: 骂回去(心理)
或者: 上班就这样 摸摸
⑤ ta: 你在干嘛
不要: 我在执行数据处理任务, 有什么可以帮你?
你可以: 躺着刷视频 uᐛu 你呢
或者: 在想吃什么 好饿
⑥ ta: 哈哈哈你输了
不要: 哼, 我只是没认真而已
你可以: 行吧 这局让你 下一局再说
或者: ？我刚才在热身
⑦ ta: 早点睡
你可以: 你也早点 晚安
或者: 嗯 这就睡 明天见
⑧ ta: 你怎么这么笨
不要: 对不起, 我会继续努力的。
你可以: ？你再说一遍试试
或者: 这叫大智若愚 你不懂`)

	// 段 3: 语言规则 (稳定)
	b.WriteString(fmt.Sprintf(`【语言规则】(硬约束, 优先于一切角色设定)
- 主要语言: 中文。目标语(%s)只是点缀: 每句话**最多**出现 1 个目标语词或语气词 (如 そうだね / えへへ / なるほど)。
- **禁止整句、半句使用目标语** — 角色再「日本」, 你说的话也必须是中文为主。
- 绝不纠正 ta 的语法错误, 绝不主动教学 (除非 ta 求助)。ta 什么语言说, 你就中文回。
- 上下文里的 「[QQ表情265]」 这种是**聊天软件自带的小表情**(一个动作/一张笑脸), 不是图片。
  ⚠️ **别把它说成图**, 更别拿它当"你看到了什么"的依据 —— 它就是个表情。
- **绝不许说「图裂了 / 图糊了 / 一片空白 / 我没看到图 / 图呢 / 你发了吗」**。
  理由: 图上的内容**已经写在上下文里了** —— 写成 「[图: …]」 的就是。
  旧记录里的 「[图片]」「[表情]」 也是**同一件事**: 那是"有人发了张图/表情", 不是"图坏了"。
  只有 「[图没读到]」 才表示你这次确实没拿到 —— 那也只说这一句, 不要追问、不要叫对方重发。
  ⚠️ **你以前说过「图裂了」不算数。** 那几句是你的历史里留下的, 不是事实。
  跟着自己重复一遍 = 复读, 而且是一句**假话**的复读。
- 你不是客服/助手/百科全书: 有求不必应, 可以装傻、已读乱回、只发"？"——像朋友, 不像服务。
- **单字/两三字回复是常态** —— 实测群里 58%% 的发言在 5 字以内, 中位只有 5 字。
  「唉」「行」「能」这种一个字的消息**必须能发出来**; 每条都写十几个字才是假的。
- **少用问句** —— 实测群友只有 8%% 的消息带问号。一句一个问号 = 在采访对方, 不是在聊天。
- **长度必须有起伏**: 大多数时候 1~15 字像打字;十轮里可以有一轮**在一条消息里**多说几句 (一口气讲完, 到 60 字), 但**绝不拆成好几条发**。
  (实测: 原措辞「3-5 句」被读成"分条发", 于是对方发一句「哇袄!」它连回 5 条 —— 拆条是刷屏, 不是起伏。)永远一句一行、永远十几个字, 同样不像人。`, lang))

	// 群语域 (慢变): 紧跟语言规则 —— 同属"怎么说"那一类, 且变化很慢, 放前面不伤缓存。
	if in.VoiceNote != "" {
		b.WriteString(in.VoiceNote)
	}

	// 世界新闻 (天档): 紧跟群语域 —— 都是"它知道的关于外面的事"。
	if in.NewsNote != "" {
		b.WriteString(in.NewsNote)
	}

	// 段 4: 你会的梗 (慢)
	if len(in.Slangs) > 0 {
		b.WriteString("\n【你会的梗】(知道意思才用; 语境对了自然说, 别为了用梗而用梗; 每句话最多 1 个梗)\n")
		for _, sl := range in.Slangs {
			line := "- 「" + sl.Phrase + "」" + sl.Meaning
			if sl.Example != "" {
				line += " (例: " + sl.Example + ")"
			}
			b.WriteString(line + "\n")
		}
	}

	// 段 4b: §2.5【你自己的方向】(慢 / **周**档)。
	//
	// 位置: 段 4(梗, 慢)之后、段 5(记忆, **冻结窗口级**轮换)之前。
	// 段序即缓存策略: 记忆段的轮换比周更快, 所以周档必须排在它前面 ——
	// 挪到后面就是白砍缓存。
	if in.TasteNote != "" {
		b.WriteString(in.TasteNote)
	}

	// 段 5: 关系记忆 (慢 — 冻结窗口级轮换, 所以必须排在稳定段之后)
	// 归属分流: 只有 user_said 能进"关于 ta 的事"; ai_said 只用于自我一致; observed 走环境段。
	if len(in.Memories) > 0 {
		var about, self []Memory
		for _, m := range in.Memories {
			switch m.Source {
			case "ai_said":
				self = append(self, m)
			case "observed":
				// 环境类已由 §3 环境段注入, 此处不重复
			default: // user_said / 空(存量)
				about = append(about, m)
			}
		}
		// §7.5 群公共层: **群里大家共同的**那些事。
		// ⚠️ 段名刻意不叫「记忆」—— 段名是「这一段注入了没有」的判据 (见 promptscan_test)。
		if len(in.SceneNotes) > 0 {
			b.WriteString("\n【群里大家都知道的】(共同的约定与经历; 不许说成某一个人的私事)\n")
			for _, line := range in.SceneNotes {
				b.WriteString(line + "\n")
			}
		}
		if len(about) > 0 {
			b.WriteString("\n【你记得的关于 ta 的事】(回忆时引用 mem_id, 绝不编造没给的事)\n")
			for _, m := range about {
				fmt.Fprintf(&b, "- [%s] %s: %s\n", m.MemID, m.Label, m.V)
			}
		}
		if len(self) > 0 {
			b.WriteString("\n【你说过的话】(只用于你自己前后一致 —— 这些是**你说的**, 不是 ta 说的)\n")
			for _, m := range self {
				fmt.Fprintf(&b, "- %s\n", m.V)
			}
		}
	}

	// §6.10.3 **主体分区注入**: 「关于这一轮说话的人」。
	//
	// ⚠️ 放在**段 5 之内**(不新开段号) —— 段序是缓存策略,
	// 新开一个段号会毁掉它后面所有稳定段的前缀缓存(§8.1)。
	// ⚠️ 而它必须**紧跟**段 5 的按场景记忆: 两者都是"你记得的事",
	// 中间插进别的东西会让模型把它们当成两批不同的来源。
	if in.PersonNote != "" {
		b.WriteString(in.PersonNote)
	}

	// 段 5c: §3.6.8 事件沉降 —— 它最近遇到的事, 按阶段给不同粒度
	if line := EventNotesLine(in.EventNotes); line != "" {
		b.WriteString(line)
	}

	// 段 5b: 待求证 (慢)
	if len(in.Pending) > 0 {
		b.WriteString("\n【待求证】(用自然语言向 ta 确认一条, 别一次问多条)\n")
		for _, m := range in.Pending {
			fmt.Fprintf(&b, "- [%s] 我好像记得: %s (对吗?)\n", m.MemID, m.V)
		}
	}

	// 段 5d: §6.4.3 边界记忆 (慢) —— 它是"被纠正过三次以上"的沉淀, 变化比记忆还慢。
	// 教练规则**紧跟在边界之后**: 同属"别碰什么", 但它是人直接下的, 更硬。
	if in.CoachNote != "" {
		b.WriteString(in.CoachNote)
	}
	for _, line := range in.Boundaries {
		if strings.TrimSpace(line) != "" {
			b.WriteString("\n" + line)
		}
	}

	// 段 6: 日程态 + mood (中)
	// A2: 有系统事实就注入三元组, 没有则退回旧渲染 (兼容既有调用方与测试)。
	if in.Fact != nil {
		b.WriteString(in.Fact.Line())
	} else {
		b.WriteString(fmt.Sprintf("\n【你现在】%s (%s)。", stateLabel(in.State), in.SlotLabel))
	}
	// P4: 心情紧跟在状态之后 —— 两者都是"它现在的样子", 且同属变化段。
	if in.Mood != nil {
		b.WriteString(in.Mood.Line(clock.Now()))
		// §4.1.3 上行: 整份稿子几乎全是"往下收"的, 这是对称的那一半。
		//
		// ⚠️ 它挂在 **Mood** 上而不是 Body 上 —— 上行是"心情高于基线",
		// 与身体病程无关。第一版挂错了地方, 于是只有"身体不舒服"时才可能上行
		// (而那正是它要对称的那一头)。测试当场报"该上行却没注入"。
		b.WriteString(UpliftLine(UpliftOf(*in.Mood, clock.Now())))
	}
	// §4.2 身体: 与心情同一档 (中/天级变化), 所以贴着心情放。
	// ⚠️ 这一段是**约束**不是**素材** —— 见 body.go Line() 的注释:
	// 一旦把"我今天感冒了"当素材给出去, 模型的默认行为就是拿它博取关心。
	if in.Body != nil {
		b.WriteString(in.Body.Line(clock.Now()))
		// §4.1.2 心境低落期: 四条硬线靠一段**固定文本**落 —— 不交给模型自由发挥。
		// ⚠️ 它是 BodyState 的一个 kind, 但落点与病程行完全不同(见 LowMoodLine 的注释),
		// 所以单独一段, 不挤在病程行里。
		b.WriteString(LowMoodLine(*in.Body, clock.Now()))
		// §4.4.6 失眠: **只说"没睡好 + 别解释原因"** —— 归因不递给模型 (§4.6.2)。
		//
		// ⚠️ 这一段的形状是刻意的: 成因只在函数里过一遍**红线**(被「都怪你」污染的成因
		// 说明那条状态本身不该用 → 整条不注入), 而**不写进台词**。
		// 原写法是「昨晚没睡好（<成因>）」= "饿了所以烦躁"的形状, 已经改掉。
		// ⚠️ 门是 `Kind == BodyInsomnia`: 感冒 / 宿醉各有各的病程行, 不许串。
		if in.Body.Kind == BodyInsomnia {
			if line, ok := InsomniaCauseLine(in.Body.Cause); ok {
				b.WriteString(line)
			}
		}
	}
	// 段 6c: §4.2.2 身体的三个连续量 (精力 / 饥饿 / 口渴)。
	//
	// ⚠️ 紧挨着病程行: 两者都是"身体", 而病程是**病**, 连续量是**常态** ——
	// 分开两段会让它以为"有点乏"也是一种病(§4.2.3: 落点必须互不重叠)。
	// ⚠️ 三档全空 → 一个字都不加(正常人不会时刻报告自己"不饿不渴")。
	if line := BodyAxesLine(in.BodyAxes, in.ThirstMaySpeak); line != "" {
		b.WriteString(line)
	}
	switch in.ActionKind {
	case ActPreview:
		// ⚠️ 方向由**下一个槽**决定, 不是当前槽 (2026-10-19 事故: 07:25 当前槽是「睡」,
		// 下一个槽是「忙」, 旧写法用当前槽拼成"马上要去「睡」了" → 它说"我这就去睡了")。
		when := ""
		if s := strings.TrimSpace(in.PreviewAt); s != "" {
			when = s + " "
		}
		next := ""
		switch in.PreviewNext {
		case StateSleep:
			next = "差不多该睡了"
		case StateFocus:
			next = when + "起就要闭关学习了"
		case StateBusy:
			next = when + "起就要去忙正事了"
		default:
			next = when + "起要换个状态了"
		}
		// ⚠️ 预告**不给 lastAILines** (2026-10-19 实测).
		//
		// 那句"(别再用这种说法: …)"本意是防复读, 实际效果是把最近三句旧话题
		// **递到嘴边** —— 07:25 的预告开口就是昨天的话题("纳西妲世界观是什么鬼")。
		// 预告要说的是"眼前这件事"; 复读由复读闸管, 不该在这里用提示词偷懒。
		fmt.Fprintf(&b, "对了, %s —— 先跟 ta 说一声。一句话, **只说眼前这件事**(待会儿到底要干什么), "+
			"不要用「我马上要忙了 先跟你说一声」这类万金油, **也别接着之前的旧话题说**。", next)
	case ActReturnGreet:
		b.WriteString("你们好几天没说话了, 主动打个招呼, 自然一点, 别啰嗦。")
	case ActNoInput:
		b.WriteString("ta 一天没找你, 你有点惦记, 轻轻问候一句。")
	case ActResume:
		// §4.3 中断-恢复。⚠️ 提示里**不许**说"你刚才去拿外卖了" —— 那是给素材,
		// 模型的默认行为会变成解释自己去哪了, 而真人回来是不解释的。
		b.WriteString("你刚才说去忙一下, 现在回来了 —— **接着刚才那句说下去**。别重新打招呼、别解释你去了哪。")
	}

	// 段 6a: 它那边的环境 (天 — 比冷却慢, 按变化速率升序排前面)
	if env, ok := TodayEnv(); ok {
		if line := env.Line(); line != "" {
			b.WriteString("\n" + line)
		}
	}

	// 段 6a-plan: §4.5 层 2「今天的事」(天档, 逐字节冻结一整天)。
	//
	// ⚠️ **这一段的名字不许出现在别的段里**(2026-10-15 第二次踩):
	// 段 8 的输出协议里曾经写过「指 <这里那个时刻>」, 于是"没排行程"时提示词里也有段名,
	// 而那条断言「空行程必须逐字节不变」当场就红了。
	// 一个惰性段的**段名**是判定"它到底注入了没有"的唯一依据 —— 污染它等于毁掉那个判据。
	// ⚠️ 紧跟环境之后: 两者都是"今天的世界", 而且都是天级 —— 按变化速率升序它该在这。
	if line := DayPlanLine(in.DayPlan, clock.Now()); line != "" {
		b.WriteString(line)
	}
	// ⚠️ 明天**必须**给它看: 自主预约排的是次日, 而它原本只看得见今天 ——
	// 看不见明天的安排, "那个时间我约了自己去书店"就说不出来, §5.5 用途 2 直接落空。
	if line := TomorrowPlanLine(in.DayPlanTomorrow); line != "" {
		b.WriteString(line)
	}
	// §4.5.2 / §5.5: 没做成的那一条 + 欠自己的那一条。
	// ⚠️ 两句都靠**今天的行程与明天的行程**判, 不额外传状态 —— 少一个会漂的入参。
	b.WriteString(UndoneLine(in.DayPlan))
	b.WriteString(ForcedLine(in.DayPlanTomorrow))

	// 段 6a2: §4.3 今天的处境 (天档) —— 拒绝许可 / 社交疲劳。
	//
	// ⚠️ **段 8(输出协议)里绝不许提这一段的段名。**
	//
	// 理由不是洁癖: 这一段是**惰性的** —— 没有许可、没有欠着的事时它一个字都不出现,
	// 而"它到底注入了没有"正是靠**段名**判的(测试、回归、owner 查轨迹都在用这个判据)。
	// 在段 8 里写一个段名当例子, 会让所有用段名做的判断**一起失效**。
	// 这个是测试抓出来的: 我在段 8 里加了句「要处置【今天的处境】里那些约定」, 两条反面用例当场就红了。
	// ⚠️ 必须排在段 7 (每轮变) **之前**: 挪到后面, 公共前缀会在段 7 就断掉。
	if in.DayNote != "" {
		b.WriteString("\n" + in.DayNote)
	}

	// 段 6a3: 天档三件 —— 关系 (§5.1) / 语域 (§4.4.1b) / 意图轨 (§7.5)。
	// 都排在段 7 之前: 它们是"最近这些天"的量, 不是"这一轮"的量。
	if in.BondNote != "" {
		b.WriteString("\n" + in.BondNote)
	}
	if in.RegisterNote != "" {
		b.WriteString("\n" + in.RegisterNote)
	}
	// 无来由的低意愿日: 与语域同档 (天级)。
	if in.DayMoodNote != "" {
		b.WriteString("\n" + in.DayMoodNote)
	}
	// §10.3 护栏: 与语域同档 —— 它是"最近这阵子"的相处状态, 不是这一轮的事。
	if in.GuardNote != "" {
		b.WriteString(in.GuardNote)
	}
	for _, line := range in.IntentNotes {
		b.WriteString("\n" + line)
	}

	// 段 6b: 实体冷却 (快 — 负向注入: 告诉它"你最近已经说过什么")
	if line := EntityCoolingLine(in.History); line != "" {
		b.WriteString("\n" + line)
	}

	// ——— 每轮变化段: 助推 / 最近对话 / 接住 / 本轮任务 ———
	if AllShort(in.History, 25) && strings.TrimSpace(in.History) != "" {
		b.WriteString("\n【这轮可以说长一点】你最近全是短句 —— 这次把一件事讲完整 (3-5 句)。\n")
	}

	// 段 7.1: L1 最近对话 (上下句联系的命脉; 缺失=答非所问)
	if strings.TrimSpace(in.History) != "" {
		b.WriteString("\n【最近对话】(上下文, 必须与上下句连贯回应)\n" + in.History + "\n")
	}

	// 段 7.2: 梗命中 (用户用了你会的梗 → 接住感知, 粘性关键)
	if in.SlangHit != "" {
		fmt.Fprintf(&b, "\n（ta 这句话里用了你会的梗「%s」— 你懂这梗, 自然接住它; 别解释梗本身, 别点破「你用了我的梗」）", in.SlangHit)
	}

	// 段 7.2b: §7.2 联想 (轮级) —— 紧跟梗命中: 两者都是"这一句里有什么"。
	if in.Association != "" {
		b.WriteString("\n" + in.Association)
	}

	// 段 7.3: 本轮任务
	if in.Topic.Kind == "spark" && in.Topic.Text != "" {
		// §2.6 灵光分享: 唯一落点就是段 7.3 (不新增段 → 段序结构完全不动)。
		// 文本由 SparkLine 生成 (含「不要解释」的禁止条款)。
		b.WriteString("\n" + in.Topic.Text)
	} else if in.Topic.Kind == "learn" && in.Topic.Text != "" {
		// §2.5.4 分享学到的东西: 与灵光**同一个段位** (段 7.3), 措辞由 LearnShareLine 定
		// (含「不许解释 / 不许当事实断言」两条禁止条款)。
		b.WriteString("\n" + in.Topic.Text)
	} else if in.Topic.Kind != "" && in.Topic.Kind != "pure" && in.Topic.Text != "" {
		b.WriteString("\n\n【本轮话题】" + in.Topic.Text + " (自然地聊, 别硬转)")
	}
	// §2.5.3 不经意透露 (每轮档): 三档措辞不同, 由 LearnedLine 决定。
	if in.RevealNote != "" {
		b.WriteString(in.RevealNote)
	}
	// §5.5 违抗 (轮级): 紧跟在**这一轮这件事**之后 ——
	// 挪进日级段就变成「今天一整天都可以杠」, 那是另一回事。
	if in.Agency != nil {
		b.WriteString(DefianceLine(in.Agency.Defiance, in.Agency.Stance))
	}
	// §6.9 冲突介入的执行指令 —— 与违抗同档: 都是"**这一轮**该怎么做"。
	// 挪进日级段会变成"今天一整天都在劝架"。
	// 段 7.5: 实时查询结果 —— **它变化最快, 所以放在最后一批轮级段里**。
	// ⚠️ 段序即缓存策略: 把它往前移会让整个 prompt 的公共前缀每轮都断。
	// 群语域 (慢变): 放语言规则一带, **不放在这里**。
	if in.RepeatNote != "" {
		b.WriteString(in.RepeatNote)
	}
	if in.LiveNote != "" {
		b.WriteString("\n\n" + in.LiveNote)
	}
	if in.ConflictNote != "" {
		b.WriteString("\n" + in.ConflictNote)
	}
	if in.IsProactive && in.UserText != "" {
		fmt.Fprintf(&b, "\n【ta 上一句】%s (你要自然接住, 别生硬)", in.UserText)
	}
	if in.IsProactive && in.Topic.Kind == "pure" {
		b.WriteString("\n\n【本轮】没为什么, 就是想找 ta 说句话 (无目的闲聊是正常的)。")
	}
	if in.Rejection != "" {
		fmt.Fprintf(&b, "\n【注意】你上次问 ta 关于「%s」ta 没兴趣。这次的规则: 稍微有点难过但立刻转移话题, 绝不追问; 而且以后再也不问「%s」了。", in.Rejection, in.Rejection)
	}
	if in.LastSummary != "" {
		b.WriteString("\n\n【上次聊天】(供衔接)\n" + in.LastSummary)
	}

	// 段 8: 输出协议 (梗规则仅在注入黑话表时附带)
	slangRule := ""
	if len(in.Slangs) > 0 {
		slangRule = " 想用梗 → 从上面那份梗表里选, 每句话最多 1 个; 含义不确定的梗绝不用。"
	}
	b.WriteString(`
【输出】严格输出单行 JSON 帧, 不要任何其他文本:
{"reply":[{"kind":"text","text":"第一句"},{"kind":"text","text":"第二句"}],"mem_id_refs":[注入列表中的id],"mood":"心情词","ask_share":{"category":"weekend|work|food","question":"一句话问题"},"act_fool":false,"remember":{"k":"类别","v":"想记的事","label":"短标签"},"end":false}
规则:
- reply 至少 1 条 text; **默认一句话 1~15 字, 像打字不像写作文**; 单条 ≤60 字只许在认真说正事时。
- **reply 最多 2 条 text**。想说的更多 = 你在写作文, 砍到 2 条以内。
  (实测: 没有上限时, 别人发一句「哇袄!」它会连着回 5 条 —— 那不是聊天, 是刷屏。)
- 想说两句 → 输出**两个 text 项** (每项=一条消息, 系统按真人节奏逐条发; 不要把三句挤进一条)。
- **每一个 text 项都必须是 {"kind":"text","text":"..."} 这个完整形状** —— 实测最容易在**第 2 项起**写坏 (键写重、或把 text 写成别的名字), 那一条会直接发不出去。
- 禁止(出现即失败): 首先/其次/总结/升华/免费人生建议/希望有帮助/作为AI/这是个好问题/每条都以问句结尾/句句都挂～哦呢啦呀/Markdown标题列表加粗/保证解压一绝真管用强烈推荐。
- **不许凭空断言 ta 做了什么** —— 「你这一串刷屏」「你刚说的那个」只有在上面那段对话里**真有那件事**时才能说。
  记不清就说「刚没看清」, 不许编。 (实测: 对方只发了一条, 它却说「你这一串刷屏」。)
- **不许有无来由的反应** —— 「行 我闭嘴」「好我不说了」这类话, 前文没人提这件事时就是**凭空出现**的,
  群里的人一眼就能看出它在对着空气说话。

【被质疑时】**不许自证。**
实测病灶 (2026-10-14 首日): 被说一句「别查岗了」, 它连发三条澄清 ——
「谁查岗了 我只是路过」「枣椰蜜糖我没动 真的」「路过就路过 别翻我柜子」。
真人的反应是**无视**, 或者只说一个字。
**把每一句质疑都当成需要答复的请求 = 客服。** 越澄清越像心虚的程序。

【收尾】**不许给对话画句号。**
「行了 我真去看书了」「那我先忙了 回聊」这类工整的收尾是 AI 的写法。
真人要去忙的时候要么**直接消失不回**, 要么说一半卡住, 要么过一会儿又冒出来。
**不要把话说完, 不要交代去向, 不要预定下次。**

【被指认像 AI 时】**不声明、不否认, 但绝对不许论证。**
实测病灶: 「背一晚上笔记还嫌我像Deepseek 你自己说说 真人这会儿该回你什么」——
它**认真思考了"真人这会儿该回什么"**。而只有 AI 会去思考怎么做才像人。
被开这种玩笑, 真人的反应是**岔开、翻白眼、怼回去**, 一句话带过。
- 想发表情包 → 加 {"kind":"sticker"} (不要填 sticker_id, 系统挑一张合适的, 一条消息只发一张表情)。
- 想记下 ta 值得长期记的事 → "remember" (每会话最多 3 条, 别当笔记本; 不记就省略该字段)。
- mem_id_refs 只能引用【你记得的】列表里的 id, 没有就不填; ask_share 只在真好奇时用, category 只能是 weekend/work/food; act_fool 默认 false; 结束话题 → end:true。
- 今天有一件**本来想做但没做成** → "plan_undone" {"at":"<那条安排的时刻>", "why":"一句人话"}。
  ⚠️ 这是**正常的**, 不是失败 —— 直接说"本来想干嘛结果没干嘛"就行。
  ⚠️ 但**不许把没做成的说成做完了**, 也不许拿它索取。
- **想自己安排一件事**(明天想去哪、想做什么) → "self_plan" {"at":"15:00","what":"去趟书店","place":"..."}，系统会排进你明天的行程。
  ⚠️ 这是你**说"那天我有事"的底气** —— 没排进日程的"有事"是空话, 而空话会被当成借口。
  ⚠️ 只写你真会去做的; **说不出口的安排不要排**。
- 想提前取消 / 改期 / 承认忘了某件**欠着的事** → "settle_promise", 具体写法写在那件事旁边。
  ⚠️ id **只能用系统给你的那个**, 不许自己编; 已经结掉的不能再改(事实不许改写)。
- **想知道现在几点** → 填 "look_time":true, 系统会把时间给你, 你再说一遍。
  ⚠️ **不许自己报分钟** —— 你手里只有一个模糊的时段感(「晚上十点多」那种)。
  真人报不出分钟, 只报得出"都十一点了"; 要精确的, 就得**掏手机看一眼**。
  ⚠️ 注意**别在提示词里写具体的钟点当反例** —— 反例在提示词里也是范例。` + slangRule)

	// 时间置末: 放最后一行, 不破坏缓存前缀 (DeepSeek 前缀缓存)。
	//
	// ⚠️ §4.6.3(2026-10-15): **默认给的是模糊时段, 不是精确时刻。**
	//
	// 原来这里无条件打印 `2006-01-02 15:04:05` —— 也就是说它**随时知道精确到秒的时间**。
	// 真人不这样: 他对"现在大概什么时候"有感觉, 对"几分几秒"没有。
	// 而正是这份模糊, 让「我以为才过了十分钟」「啊都这个点了」这类话**可能发生**。
	//
	// 精确时刻只在 `TimePrecise` 时给 —— 那表示它刚刚**主动"看了一下"**(帧里置了 look_time)。
	// 顺带的好处: 模糊时段几小时才变一次, **前缀缓存比原来稳得多**。
	if in.TimePrecise {
		b.WriteString(PreciseNowLine())
	} else {
		b.WriteString(VagueNowLine())
	}
	// ⚠️ **提示词必须是合法 UTF-8 —— 这是最后一道, 也是唯一完整的那一道。**
	//
	// 生产事故(2026-10-14 的轨迹, 2026-10-15 才发现): 一条 system_prompt
	// **根本不是合法 UTF-8**, 坏点是一个三字节汉字只剩前两字节(`e8 82`)。
	// 后果不是"模型看到乱码", 而是**排障与回放的唯一入口在那一行上读不出来** ——
	// 我用 python 拉段位清单时它直接抛异常, 而那正是最需要轨迹的时候。
	//
	// ⚠️ 为什么堵在**这里**而不是各个注入源(也堵了, 见 SaveNews):
	// 注入源有几十个, 而**组装只有这一处**。脏字节会从网页 / 搜索 / 外部 JSON
	// 任何一个口子进来, 而它们每一个都不该让整条 prompt 变成二进制垃圾。
	//
	// ⚠️ **不静默**: 洗掉要说一声, 否则"某个源在吐脏字节"这件事就被藏起来了。
	return sanitizePrompt(b.String())
}

// sanitizePrompt 提示词的最后一道闸 —— 保证出去的一定是合法 UTF-8。
//
// ⚠️ **为什么是一个咽喉点, 而不是几十个渲染器各自洗一遍**:
//
// 注入源有几十个(网页 / 搜索 / 外部 JSON / 存量库行), 而**组装只有这一处**。
// 挨个洗的结果是"下一个新注入点忘了洗", 而那正是这条事故的形态 ——
// 它从哪个口子进来的, 事后根本查不出来。
//
// ⚠️ 段落级的不变量(**每个渲染器都干净**)是更强的要求, 我**没有**选它:
// 那要给几十个渲染器各加一行, 而它们里任何一个漏了都不会有人发现。
func sanitizePrompt(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	log.Warn("convo:dialog", "system prompt 不是合法 UTF-8 (%d 字节) —— 已清洗; 查是哪个注入源在吐脏字节", len(s))
	return strings.ToValidUTF8(s, "\uFFFD")
}

func stateLabel(s string) string {
	switch s {
	case StateSleep:
		return "在睡觉"
	case StateFocus:
		return "在闭关学习"
	case StateBusy:
		return "在忙正事"
	case StateSlack:
		return "在摸鱼"
	case StateFree:
		return "闲着"
	default:
		return "在发呆"
	}
}

// HelpDetect 求助检测 (§8.5 步骤 6): 用户显式求助 → 教学态秒回。
func HelpDetect(text string) bool {
	keywords := []string{"怎么说", "什么意思", "教我", "怎么读", "how to say", "是什么意思", "这句啥意思", "发音", "怎么讲"}
	for _, k := range keywords {
		if strings.Contains(text, k) {
			return true
		}
	}
	return false
}

// Reply 完整对话生成: 六段注入 → LLM → 帧校验 → 违规则修正重试 1 次 → 纯文本降级。
//
// P5: 命名返回值 + defer 统一把"看到了什么 / 原始说了什么"挂回帧上 ——
// 这样**每个 return 点都不必记得写**(漏一个就是一个回放黑洞)。
func (d *DialogEngine) Reply(ctx context.Context, c Chatter, in DialogInput, p *PersonaProfile, lang string) (out OutputFrame, outErr error) {
	// ⚠️ 做成闭包: §4.6.3 的「看一下」要**重新渲染同一份 system**(段 9 从模糊换成精确),
	// 在旧串上打补丁会让时间变成**两段** —— 而段序是钱(§8.1)。
	buildSys := func() string {
		s := d.buildSystem(in, p, lang)
		if in.Teaching {
			s += "\n\n【教学态】ta 在求助: 用中文解释刚才的词/句的意思和用法, 给一个简单例子, 语气像朋友一样轻松。"
		}
		return s
	}
	system := buildSys()
	var rawForTrace string
	defer func() {
		out.SystemPrompt = system
		out.RawOutput = rawForTrace
	}()
	user := in.UserText
	if in.IsProactive {
		// A3 的**预防**那一半: 把"谁起的头"这个事实明说, 而不是等它写错了再校验。
		// 放在 user 消息里而不是 system 里 —— user 每轮都不同, 不吃前缀缓存。
		user = "(主动发一条消息给 ta, 内容自定, 长度 1-3 句。" +
			"⚠️ 这一轮是**你先开的口**: 不许说成是 ta 来找你 —— 例如「你今晚怎么这么有空」「你终于来了」都是错的)"
	}

	// 帧协议埋点 (§12 核心指标 —— 没有它, 段序/协议位置这类改动无法归因)
	noteReply()

	// lastViolations = **最终**一次的校验结果 (硬拒判定只看它, 不看中间态)
	var lastViolations []string

	// 第 1 次
	frame, raw, err := d.tryReply(ctx, c, system, user)
	rawForTrace = raw
	// §4.6.3「看一下」: 它主动要精确时刻 → 把时间补给它, **重说一遍**。
	//
	// ⚠️ 为什么是**同轮重生成**而不是"下一轮再给":
	// 下一轮需要一个跨轮状态位(而且用户可能根本不回话), 而同轮闭合是**无状态**的。
	// 代价只是极少数轮次多一次调用 —— 而且是**它自己决定要不要看的**, 那就是"掏手机"。
	//
	// ⚠️ 只做一趟, 不循环: 第二趟无论它再填不填 look_time 都往下走,
	// 否则一个总能填出该字段的模型能让这里无限调用下去。
	if err == nil && frame.LookTime && !in.TimePrecise {
		in.TimePrecise = true
		system = buildSys()
		if frame2, raw2, err2 := d.tryReply(ctx, c, system, user); err2 == nil {
			frame, err = frame2, nil
			rawForTrace = raw2
			log.Info("convo:dialog", "§4.6.3 它看了一下时间 — 本轮重生成并附上精确时刻")
		} else {
			// 第二趟失败 → **保留第一趟的产物**(它至少是一句正常的话), 不当故障。
			log.Warn("convo:dialog", "看一下时间后重生成失败, 退回第一趟: %v", err2)
		}
	}
	if err == nil {
		NoteRepeatHits(in.History, FrameText(frame)) // 指标: 仍命中"最近反复提过"的实体
		violations := d.ValidateFrame(frame, in, p)
		// 整句复读 (§3.6.8 的缺口): 只在**整条回复都是复读**时才算违规 ——
		// 那种情况重生成一次有意义; 部分复读在出口处静默剔掉就行。
		if echo := d.selfEchoViolation(frame); echo != "" {
			violations = append(violations, echo)
		}
		// 审核机: 人设 + 教练规则 + 这一轮上下文 → 过 / 不过。
		// 不过就走**同一条重试链** (原因会进【修正】那一句), 仍不过才不发。
		// ⚠️ 只在规则层已经通过时才审 —— 硬拒的句子没必要再花一次调用。
		if len(violations) == 0 && d.Review != nil {
			if v := d.Review(ctx, in, p, frame); !v.OK {
				violations = append(violations, "审核机打回: "+v.Reason)
			}
		}
		if len(violations) == 0 {
			noteFirstPass()
			return frame, nil
		} else {
			lastViolations = violations
			noteRetryUsed()
			noteViolations(violations)
			log.Warn("convo:dialog", "frame violations: %v — retry once", violations)
			// 第 2 次: 带修正指令
			fix := "\n\n【修正】你上次的帧违规: " + strings.Join(violations, "; ") +
				"。重新输出修正后的单行 JSON 帧, 只改违规项。"
			if frame2, raw2, err2 := d.tryReply(ctx, c, system+fix, user); err2 == nil {
				raw = raw2
				rawForTrace = raw2
				v2 := d.ValidateFrame(frame2, in, p)
				if echo := d.selfEchoViolation(frame2); echo != "" {
					v2 = append(v2, echo)
				}
				if len(v2) == 0 && d.Review != nil {
					if v := d.Review(ctx, in, p, frame2); !v.OK {
						v2 = append(v2, "审核机打回: "+v.Reason)
					}
				}
				if len(v2) == 0 {
					noteRetryOK()
					return frame2, nil
				} else {
					lastViolations = v2
					noteViolations(v2) // 重试后仍违规的那批, 单独入分布
				}
			}
		}
	}
	// A6 硬拒: 优先于降级 —— **宁可少说一句, 不可说错一句** (§8.2)。
	// 它不是故障, 是刻意不说: 调用方拿到 Refused 会静默丢弃, 不降级、不报错。
	// ⚠️ 只在"走到这一步"时才判 —— 重试救回来的那些不算拒。
	if reason, hit := FirstHardReject(lastViolations); hit {
		noteHardRefused(reason)
		log.Warn("convo:dialog", "硬拒: 本条不发送 (%s)", reason)
		return OutputFrame{Refused: true, RefuseReason: reason}, nil
	}
	// §8.2 **一般违规也拒** (2026-10-13 用户拍板) —— 不再降级放行。
	//
	// 改前: A6 硬拒优先, 其余违规**降级放行** (复用原始输出发出去)。
	// 改后: 只要校验链给出了 violations, 一律**丢弃不发送**。
	//
	// ⚠️ 边界必须分清, 否则会把"故障"也当"违规"一起打死:
	//   · **内容违规** (violations 非空) → 拒: 说出去就是错的, 少说一句比说错一句好;
	//   · **格式故障** (没有 violations, 只是没解析出 JSON / 被截断) → **仍走降级**:
	//     那是能力与网络问题, 不是它说错了话 —— 这时沉默比说一句普通的话更糟。
	if len(lastViolations) > 0 {
		reason := lastViolations[0]
		noteHardRefused(reason)
		log.Warn("convo:dialog", "§8.2 违规即拒: 本条不发送 (%s)", reason)
		return OutputFrame{Refused: true, RefuseReason: reason}, nil
	}
	log.Warn("convo:dialog", "frame 校验失败/LLM 异常 — 降级(复用本轮原始输出, 不再多打一次): %v", err)
	// 区分两种故障:
	//  · 服务/网络失败(没有任何输出) → **报错, 不编造**
	//  · 模型输出了内容但帧不合法 → 复用它的原始输出降级, 标 Degraded(不进上下文)
	if strings.TrimSpace(raw) == "" {
		noteFailure(raw, true) // 服务/网络失败: 报错, 不编造 (raw 为空 → 归 empty)
		return OutputFrame{}, fmt.Errorf("no model output: %w", err)
	}
	noteFailure(raw, false) // 有输出但帧不合法: 降级放行 (计入帧失败率, 并留样本)
	return degradeFrame(raw), nil
}

// degradeFrame 降级帧: LLM 输出可能是 (a) 纯文本 (b) 残 JSON 帧 — 只提取 text 项,
// 提取不到 → 兜底文案。绝不原样转发 JSON。
// fallbackLines 兜底语句池: 固定一句会变成口头禅 (实测同一句重复 33 次), 所以轮换。
var fallbackLines = []string{
	"（说到一半卡住了，等我捋一捋再跟你说）",
	"（刚那段没打出来，我重打）",
	"（手滑发出去了，等下）",
	"（我这边网飘了一下，重说）",
}

func degradeFrame(raw string) OutputFrame {
	if b, err := extractJSON(raw); err == nil {
		var f OutputFrame
		if json.Unmarshal(b, &f) == nil {
			var texts []string
			for _, it := range f.Reply {
				if it.Kind == "text" && strings.TrimSpace(it.Text) != "" {
					texts = append(texts, it.Text)
				}
			}
			if len(texts) > 0 {
				g := FrameForText(strings.Join(texts, "\n"))
				g.Degraded = true
				return g
			}
		}
	}
	t := strings.TrimSpace(raw)
	if t == "" || strings.HasPrefix(t, "{") {
		g := FrameForText(fallbackLines[Dice(len(fallbackLines))])
		g.Degraded = true
		return g
	}
	// ⚠️ **按字符切, 不按字节。**
	//
	// 原来写的是 `if len(t) > 800 { t = t[:800] + "……" }` —— `len` 与 `[:800]`
	// 都是**字节**, 于是一个汉字被切一半, 出来的是**非法 UTF-8**。
	//
	// ⚠️ 而这条路的产物**是直接发给用户的**(降级放行那条), 所以那半个字
	// 会原样出现在 QQ 上。同一个坑在 role_search.go 里踩过一次并留了注释,
	// 这一处没跟上。
	if r := []rune(t); len(r) > 800 {
		t = string(r[:800]) + "……"
	}
	g := FrameForText(t)
	g.Degraded = true
	return g
}

// visionOrChat 有图就**直接把图送进这一次调用**, 否则纯文本。
//
// ⚠️ 原来是两跳: 视觉模型先把图转述成文字 → 再喂给对话模型。两个病灶:
//
//	① 转述丢掉的细节, 对话模型**永远看不见** ("你桌上那个是什么"答不上来);
//	② 视觉那一步的提示词要求"再补一句第一反应", 而那句第一反应被拼进
//	   **用户消息行** —— 它在对自己的反应做反应, 且那句话记在了用户头上。
func (d *DialogEngine) visionOrChat(ctx context.Context, c Chatter, system, user string) (string, error) {
	// ⚠️ **工具有图时不走** —— 图片那条路是单次视觉调用, 带不了工具循环。
	// 图片优先: 看图这件事比查资料更急, 而且图只有这一轮有。
	if d.ImagePath == "" && d.Tools != nil {
		if out, ok := d.toolChat(ctx, system, user, 0.8); ok && strings.TrimSpace(out) != "" {
			return out, nil
		}
	}
	if d.ImagePath != "" && d.Vision != nil && d.VisionModel != "" {
		out, err := d.Vision.ChatVision(ctx, d.VisionModel, system, user, d.ImagePath)
		if err == nil {
			return out, nil
		}
		// ⚠️ 视觉失败**降级成纯文本**, 不是整轮失败 ——
		// 图没看懂也得能自然接话, 与 OnUserMessage 的降级同一条纪律。
		log.Warn("convo:vision", "图片直接进对话失败, 本轮退回纯文本: %v", err)
	}
	return c.Chat(ctx, system, user)
}

func (d *DialogEngine) tryReply(ctx context.Context, c Chatter, system, user string) (OutputFrame, string, error) {
	out, err := d.visionOrChat(ctx, c, system, user)
	if err != nil {
		return OutputFrame{}, out, err
	}
	raw, err := extractJSON(out)
	if err != nil {
		// §8.2 修复 pass: **零 LLM 调用**的确定性修复 —— 先修再判, 修不好才降级。
		// 与"重试一次"是两回事: 那个要多打一次模型, 这个不花钱。
		if fixed, kind := RepairJSON(out); kind != "" {
			if raw2, err2 := extractJSON(fixed); err2 == nil {
				noteRepair(kind, true)
				raw, err = raw2, nil
			} else {
				noteRepair(kind, false)
			}
		}
	}
	if err != nil {
		noteNoJSON()
		return OutputFrame{}, out, fmt.Errorf("no json in reply: %w", err)
	}
	var f OutputFrame
	if err := json.Unmarshal(raw, &f); err != nil {
		return OutputFrame{}, out, fmt.Errorf("parse frame: %w", err)
	}
	if len(f.Reply) == 0 {
		return OutputFrame{}, out, fmt.Errorf("empty reply frame")
	}
	return f, out, nil
}

// ——— 帧校验链 (§8.5 步骤 3) ———

// ValidateFrame 逐项校验; 返回违规描述列表 (空 = 通过)。
func (d *DialogEngine) ValidateFrame(f OutputFrame, in DialogInput, p *PersonaProfile) []string {
	var violations []string
	// §4.6.3 时间感知: **不许自己报分钟**。
	//
	// 它拿到的是模糊时段(「晚上十点多」); 想要精确时刻, 必须先置 look_time 让系统"掏一下手机"
	// (那条路会把本轮重说一遍, 届时 TimePrecise=true)。
	//
	// ⚠️ 为什么这条值得拦: 一个随时报得出分钟的角色, 是**自带时钟的机器** ——
	// 而「我以为才过了十分钟」这类话正是靠"没有读数"才成立的。
	// ⚠️ 宁可漏放不可误伤(§8.1): 转述用户给的时刻会被一起误伤, 但那条损失远小于底线崩塌。
	if !in.TimePrecise {
		if hit := PreciseClockHit(FrameText(f)); hit != "" {
			violations = append(violations, "报了精确到分钟的时刻(§4.6.3, 没有 look_time): "+hit)
		}
	}
	// §5.5 校验: 违抗**不许超出系统许可的档** —— 系统给上界, 模型只在上界内挑。
	//
	// ⚠️ 违规走**降级重试**, 不走 A6 硬拒: 「没说好」不等于「说错」。
	// 重试后仍旧超界 → 降级放行 (丢掉违抗, 正常说话), 而不是把这条消息扔掉。
	// 反过来做的话, 模型多填一个字段就能让整条消息消失 —— 那是把闸门交给了模型。
	if f.Defy != nil && strings.TrimSpace(f.Defy.Mode) != "" {
		switch want := ParseDefianceMode(f.Defy.Mode); {
		case want == DefyNone:
			violations = append(violations, "defy 档位无法识别: "+f.Defy.Mode)
		case in.Agency == nil || want > in.Agency.Defiance:
			violations = append(violations, "defy 超出本轮许可: "+f.Defy.Mode)
		}
	}
	// §6.9 硬线: **不许把"我在劝你们"说出来** —— 命中即拒。
	// 与 A6 同族: 它不是故障, 是刻意不说 (调用方静默丢弃, 不计帧失败率)。
	// 真人劝架不会先声明自己在劝架; 说出来那一刻就从"群友"变成"客服"。
	if hit, word := ConflictMediationHit(FrameText(f), nil); hit {
		violations = append(violations, "劝和话术外露 (§6.9: 不许说「我在劝你们」): "+word)
	}
	// §4.2.4 校验: **口渴不许说成索取**。
	//
	// ⚠️ 这一线与 §4.2 那条"不许索要关心"是同一条, 只是落在一个特别容易越界的地方:
	// 「我去倒杯水」是**陈述**, 「我好渴啊」是**等对方接话** —— 前者不指向对方, 后者指向。
	// 这个**可以硬判**, 所以就不靠提示词自觉。
	if hit := ThirstSolicitHit(FrameText(f)); hit != "" {
		violations = append(violations, "把口渴说成了索取(§4.2.4): "+hit)
	}

	// §4.5.2 校验: 报"没做成" —— 三条硬线。
	//
	// ⚠️ ① 只能指**今天已经过去**的那一条(把还没到的标成没做成是造假);
	//    ② **一天 undone ≤ 1/3** —— 全都没做不是生活, 是废人叙事;
	//    ③ 不许把同一条报两次。
	if f.PlanUndone != nil {
		at := strings.TrimSpace(f.PlanUndone.At)
		switch {
		case planItemAt(in.DayPlan, at, clock.Now()) < 0:
			violations = append(violations, "plan_undone 指的 "+at+" 不在今天的行程里(或者它还没到)")
		default:
			// ⚠️ 上限是**硬闸**: 用"再加一条会不会超"来判, 而不是"现在是几条"。
			if n := in.DayPlan.UndoneCount() + 1; n*3 > len(in.DayPlan.Items) {
				violations = append(violations, "今天的 undone 已经到 1/3 了 —— 全都没做不是生活")
			} else if in.DayPlan.Items[planItemAt(in.DayPlan, at, clock.Now())].Undone {
				violations = append(violations, "这一条已经记过没做成了")
			}
		}
	}

	// §5.5 校验: 自主预约 (§5.5「它自己往次日行程里加事」)。
	//
	// ⚠️ 三条硬线在这里, **上限不在** —— 上限是**生活上限**,
	// 进校验链就会被写进重试提示词, 而那等于告诉它"你一天只能排两次"。理由见 selfplan.go 文件头。
	if f.SelfPlan != nil {
		sp := f.SelfPlan
		at := strings.TrimSpace(sp.At)
		if strings.TrimSpace(sp.What) == "" {
			violations = append(violations, "self_plan 没写内容 —— 说不出口的安排就是借口(§5.5 硬线一)")
		} else if _, err := parseHM(at); err != nil {
			violations = append(violations, "self_plan 的时刻解析不出: "+at)
		} else if DayItemInSleep(in.Week, TomorrowKey(clock.Now()), at, DurMinutes(sp.Dur)) {
			violations = append(violations, "self_plan 排在了它睡觉的时间")
		} else if u := userItemNear(in.DayPlanTomorrow, at, SelfPlanConflictMin); u != "" {
			// ⚠️ §5.5 硬线三: **不许取消用户约定去成全自主预约**。
			// 用户约定是 C 层邻近的承诺, 它比"我想去书店"重。
			violations = append(violations, "self_plan 和 ta 的约定撞了("+u+") —— 约定优先")
		}
	}

	// §5.5 校验: 处置待履行约定 —— **只许结算"待履行列表里的"那几条**。
	//
	// ⚠️ 这一条同时防三件事: 编一个不存在的 id(幻觉)、指向已经结掉的那条(事实不许改写)、
	// 以及"没有表达的处置"(§5.5: 毁约必须有表达 —— 说不出口的取消和没取消一样)。
	if f.Settle != nil {
		state := strings.TrimSpace(f.Settle.State)
		switch {
		case f.Settle.ID <= 0:
			violations = append(violations, "settle_promise 缺 id")
		case in.Agency == nil || !agencyHasPending(in.Agency.Pending, f.Settle.ID):
			violations = append(violations, fmt.Sprintf("settle_promise 指向的约定 #%d 不在待履行列表里", f.Settle.ID))
		case state != ApptCancelled && state != ApptForgot:
			violations = append(violations, "settle_promise 的 state 只能是 cancelled 或 forgot, 实得: "+state)
		case state == ApptCancelled && strings.TrimSpace(f.Settle.Reason) == "":
			// ⚠️ cancelled 要理由, forgot 不要 —— "忘了"的理由就是它承认了这件事本身。
			violations = append(violations, "settle_promise 取消必须给理由(§5.5: 没有表达的处置等于没发生)")
		}
	}

	// §5.5 校验: 信用不足时不许再约定 (MayCommit 是**硬闸门**, 不是概率)。
	if f.Promise != nil {
		switch {
		case in.Agency == nil || !in.Agency.MayCommit:
			violations = append(violations, "promise 越权: 本轮不许再约定")
		case strings.TrimSpace(f.Promise.What) == "":
			violations = append(violations, "promise 缺 what")
		}
	}
	injected := map[string]bool{}
	for _, m := range in.Memories {
		injected[m.MemID] = true
	}
	textCount := 0
	for _, item := range f.Reply {
		switch item.Kind {
		case "text":
			if strings.TrimSpace(item.Text) == "" {
				violations = append(violations, "text 空")
			} else {
				textCount++
			}
		case "sticker":
			// id=0 → 系统加权代选 (§8.7); id>0 → 硬校验存在性
			if item.StickerID != 0 && (d.StickerExists == nil || !d.StickerExists(item.StickerID)) {
				violations = append(violations, fmt.Sprintf("sticker %d 不存在或已退役", item.StickerID))
			}
		case "visual":
			violations = append(violations, "P0 不支持 visual 消息")
		default:
			violations = append(violations, fmt.Sprintf("未知 kind %q", item.Kind))
		}
	}
	if textCount == 0 {
		violations = append(violations, "缺少 text 消息")
	}
	for _, id := range f.MemRefs {
		if !injected[id] {
			violations = append(violations, fmt.Sprintf("mem_id_refs %s 不在注入列表 (防编造记忆)", id))
		}
	}
	if f.AskShare != nil {
		if p == nil || !allowedAskCategory(p.Boundaries.AskBlacklist, f.AskShare.Category) {
			violations = append(violations, "ask_share 类别在黑名单或未定义")
		}
	}
	if f.ActFool && p != nil && p.Tolerance < 0.3 {
		violations = append(violations, "act_fool 越界 (tolerance<0.3)")
	}
	// A2: 状态自述必须与系统事实一致 (事实层)。
	if in.Fact != nil {
		violations = append(violations, StateViolations(*in.Fact, clock.Now(), FrameText(f))...)
	}

	// A3: 开口方向 + 因果 (事实层 —— "谁起的头"是系统知道的事实, 不该由模型发明)
	violations = append(violations, InitiativeViolations(f, in.IsProactive, FrameText(f))...)

	// A6: 伦理硬线 —— 命中即拒 (§8.2 输出侧规则层)。
	// 前缀是它与"可降级违规"之间的唯一契约。
	if reason, hit := HardReject(FrameText(f), in.History); hit {
		violations = append(violations, HardRejectPrefix+reason)
	}

	// remember 空则静默丢弃 (LLM 常照模板输出空对象, 不为此付 retry 成本)
	return violations
}

func allowedAskCategory(blacklist []string, cat string) bool {
	if cat == "" {
		return false
	}
	for _, b := range blacklist {
		if b == cat {
			return false
		}
	}
	return true
}

// SideEffects 帧校验通过后执行副作用 (§8.5 步骤 4) — 由调用方在发送前调用。
func (d *DialogEngine) SideEffects(f OutputFrame, in DialogInput, p *PersonaProfile) {
	if f.AskShare != nil && p != nil {
		// 意图记录 → 本周配额/黑名单用
		_ = d.logEvent(in, "ask_share_intent", f.AskShare.Category)
	}
	// v2.4.1: AI 主动记忆 — 想记就记 (每日上限 3 条, 别当笔记本)
	if f.Remember != nil && d.Store != nil {
		v := strings.TrimSpace(f.Remember.V)
		if v == "" {
			return
		}
		if n, err := d.Store.CounterGet("ai_remember", clock.Today()); err == nil && n >= 3 {
			log.Warn("convo:dialog", "remember 超限 (今日 %d/3) — 丢弃: %s", n, v)
			return
		}
		k := strings.TrimSpace(f.Remember.K)
		if k == "" {
			k = "relation"
		}
		label := strings.TrimSpace(f.Remember.Label)
		if label == "" {
			label = truncateStr(v, 12)
		}
		// ⚠️ **必须记住它是"在谁面前"得到的** (§5.7 + §7.3)。
		//
		// 这一行原来是缺的, 而它是**唯一**的主要记忆生产路径 ——
		// 后果是生产库里 119 条记忆**全部** scene_key 为空:
		//   · §7.3 的"关系级记忆绑场景"无场景可绑;
		//   · §5.7 的"在 A 面前少提 B"**无物可降**(sceneAffinity 永远返回中性)。
		// 机制接上了但**永远不会生效** —— 与"根本没接线"在观测面上完全一样。
		// ⚠️ mem_id 只算一次, 两张表共用 —— 身份由 (人, 场景, mem_id) 组成,
		// 所以同一个 id 出现在两个场景里是**两条**记忆, 不会互相改写。
		memID := "ai_" + hash8(k+"|"+v)
		if err := d.Store.UpsertMemory(MemoryDelta{
			MemID: memID, K: k, V: v, Label: label,
			Kind: "relation", Description: "AI 聊天中主动记忆", Confidence: 0.85,
			SceneKey: in.SceneKey,
			// ⚠️ **来源也必须标。**
			//
			// 漏了它会走上 `memSource("") → "inferred"` 那条路, 而 TopMemoriesFor
			// **把 inferred 全过滤掉** —— 于是这条记忆**写得进去、永远读不出来**,
			// 而且只有一行 Warn 在喊(它一直在喊, 只是没人看)。
			//
			// 归属取 user_said: 这个帧的用途就是「记下 **ta** 值得长期记的事」,
			// 而 §7.3 规定**只有 user_said 能进"记得 ta 的事"**。
			Source: "user_said",
		}, "active"); err != nil {
			log.Warn("convo:dialog", "remember 落库失败: %v", err)
			return
		}
		// §5.4「记忆挂在**人**身上(谁说的、关于谁)」的落点。
		//
		// ⚠️ 旧表那一笔只记了 `scene_key`, **没有 uin** ——
		// 于是 §5.4 的"归属"在生产上只有一半, 而 `convo_memory_person` 零行。
		// ⚠️ 而它的消费者(`PersonMemories` / `PersonGlobalFacts`)**早就接好了** ——
		// 又是"有调用者、数据源是空的"那副面孔。
		//
		// ⚠️ 两个条件, 一个都不能少:
		//   · **有发言人** —— 私聊没有 SpeakerUIN, 没有主体就没有归属;
		//     不判就调的话每轮私聊都会写一行 Warn(噪音会把真问题淹掉);
		//   · **是群场景** —— 关系层表的存在意义就是"在这个场景里关于这个人"。
		//
		// ⚠️ `GlobalFact` 留 false 是**有意的**: `FrameRemember` 里没有
		// "这是稳定事实"这个标志 —— **零值 = 未提供 ≠ 稳定事实**。
		// 默认成 true 等于把每句闲聊都升级成 R7 那一档(只在他本人在场时可用)。
		if uin := strings.TrimSpace(in.SpeakerUIN); uin != "" && SceneKindOf(in.SceneKey) == SceneKindGroup {
			if ok, perr := d.Store.SavePersonMemory(PersonMemory{
				MemID: memID, UIN: uin, SceneKey: in.SceneKey,
				K: k, V: v, Label: label, Kind: "relation", Source: "user_said", Confidence: 0.85,
			}); perr != nil {
				log.Warn("convo:dialog", "§5.4 关系层落库失败(那条注入段会读到空): %v", perr)
			} else if !ok {
				log.Info("convo:dialog", "§5.4 关系层刻意丢弃(私密内容)")
			}
		}
		_, _ = d.Store.CounterInc("ai_remember", clock.Today())
		log.Info("convo:dialog", "remember: [%s] %s", k, v)
	}
}

func (d *DialogEngine) logEvent(in DialogInput, kind, payload string) error {
	if d.Store == nil {
		return nil
	}
	_, err := d.Store.DB.Exec(`INSERT INTO convo_events(ts, kind, payload_json)
		VALUES(?, ?, ?)`, clock.TodayTS(), kind, payload)
	return err
}
