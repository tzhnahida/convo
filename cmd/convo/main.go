// cmd/convo — 拟人试跑独立进程 (P0)
//
// 与主程序完全独立: 自己的 DB (convo-trial.db)、自己的配置 (configs/convo.yaml)、
// 自己的 HTTP 端点 (管理 Web + /api/v1/convo/*)。不共享 vocablock.db, 不碰主程序迁移锁。
//
// 部署: Pi systemd 常驻 (GOOS=linux GOARCH=arm64 交叉编译, 纯 Go 无 CGO)。
// 通道: QQ 第三方 OneBot v11 (NapCat), 聊天零 UI — 详见 wiki §6.3 v2.3。
package main

import (
	"context"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/tzhnahida/convo/internal/ai"
	"github.com/tzhnahida/convo/internal/clock"
	"github.com/tzhnahida/convo/internal/convo"
	"github.com/tzhnahida/convo/internal/log"
)

func parseQQ(s string) int64 {
	if s == "" {
		return 0
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func main() {
	cfgPath := flag.String("config", "configs/convo.yaml", "convo config file")
	adminDir := flag.String("admin-dir", "", "serve admin UI from this directory (dev, hot reload)")
	// 一次性运维: 收拢表情包池里的重复。dry|run, 默认关。
	// ⚠️ run 是**破坏性**的 (删行 + 删重复文件) —— 所以必须显式点名, 不许顺手跑。
	dedupeStickers := flag.String("dedupe-stickers", "", "one-shot: collapse duplicate stickers by content + prune download leftovers (dry|run; default off)")
	// 说说的回滚入口。msg id 在 convo_selfaction.result 里 (「说说已发 (msg …)」)。
	// ⚠️ 这是**人**的刹车, 不是它的: 对外发出去的东西要有办法收回。
	delQzone := flag.String("delete-qzone", "", "one-shot: delete one Qzone post by message id (default off)")
	flag.Parse()

	cfg, err := convo.Load(*cfgPath)
	if err != nil {
		log.Error("convo", "config: %v", err)
		os.Exit(1)
	}

	store, err := convo.OpenStore(cfg.DataDir, cfg.DBFile)
	if err != nil {
		log.Error("convo", "store: %v", err)
		os.Exit(1)
	}
	defer store.Close()

	// 表情包池一次性收拢 (2026-10-16 owner 报「表情包都是一模一样的」)。
	// 起因: 爬虫每轮重下同一批 URL + InsertSticker 裸 INSERT → 473 行其实只有 21 张图。
	if *delQzone != "" {
		client := convo.NewOneBotClient(cfg.Channel.APIBase, cfg.Channel.AccessToken)
		if err := client.DeleteQzone(*delQzone); err != nil {
			log.Error("convo:self", "删说说失败: %v", err)
			os.Exit(1)
		}
		log.Info("convo:self", "说说 %s 已删", *delQzone)
		return
	}
	if *dedupeStickers != "" {
		dry := *dedupeStickers != "run"
		rep, err := store.DedupeStickers(dry)
		if err != nil {
			log.Error("convo", "表情包池去重失败: %v", err)
			os.Exit(1)
		}
		mode := "预演 (什么都没改)"
		if !dry {
			mode = "已执行"
		}
		log.Info("convo:sticker", "池去重 %s: %d 行 → %d 行 (合并 %d 行, 删重复文件 %d 个, 无文件 %d 行, 剩 %d 张不同的图)",
			mode, rep.RowsBefore, rep.RowsAfter, rep.Dropped, rep.FilesGone, rep.NoFile, rep.Groups)
		// 顺手扫掉「下载了但没入池」的残留 (分数没过门槛时留下的半成品)。
		// 池目录默认就是内置包目录 → 那里只认下载器的命名形状, 用户放的图一律不碰。
		poolDir := filepath.Join(cfg.Stickers.Dir, cfg.Stickers.Lang)
		if n, perr := store.PruneOrphanDownloads(poolDir, dry); perr != nil {
			log.Warn("convo:sticker", "下载残留扫描失败: %v", perr)
		} else {
			log.Info("convo:sticker", "下载残留 %s: %d 个 (%s)", mode, n, poolDir)
		}
		return
	}

	token, err := convo.LoadOrCreateToken(cfg.DataDir, cfg.Token)
	if err != nil {
		log.Error("convo", "token: %v", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 模块 5 前置: AI client — 未配置 key 时 Chatter=nil, 对话接口降级。
	//
	// ⚠️ **两个 client, 两种思考模式** (2026-10-18), 不是一个 client 两用:
	//   chatter: 用户在等 → 关思考。思考模式下 temperature 静默失效,
	//            而拟人化的抖动全押在 temperature 上 (系统骰子只决定"说不说")。
	//   thinker: 没人等 (做梦 / 日计划 / 世界事件 / 学习摘要) → 开思考换质量,
	//            多几秒无所谓, 而且这些任务不需要"随机感"。
	var chatter, thinker convo.Chatter
	if cfg.AI.APIKey != "" {
		c := ai.NewClient(ai.Config{
			BaseURL:         cfg.AI.BaseURL,
			APIKey:          cfg.AI.APIKey,
			Model:           cfg.AI.Model,
			Timeout:         120 * time.Second,
			DisableThinking: true,
		})
		chatter = convo.AIAdapter{C: c}
		tc := ai.NewClient(ai.Config{
			BaseURL: cfg.AI.BaseURL,
			APIKey:  cfg.AI.APIKey,
			Model:   cfg.AI.ThinkModel,
			Timeout: 180 * time.Second, // 思考模式慢, 但它是后台任务, 给足
		})
		thinker = convo.AIAdapter{C: tc}
		log.Info("convo", "ai: chat=%s (思考关, 保 temperature) · think=%s (思考开)", cfg.AI.Model, cfg.AI.ThinkModel)
	} else {
		log.Warn("convo", "ai: 未配置 API key (env CONVO_AI_KEY) — 试跑将无法生成对话")
	}

	// 模块 3: 状态机 (回调先注册, tick 后启动)
	sched := convo.NewScheduler(cfg.Sched, store)

	// 对话引擎 (帧校验链 + sticker 存在性校验)
	dialog := &convo.DialogEngine{Store: store, StickerExists: store.StickerExists}

	// 模块 3+5 编排器
	engine := &convo.Engine{
		Store:    store,
		Cfg:      cfg,
		Sched:    sched,
		Dialog:   dialog,
		Chatter:  chatter,
		Thinker:  thinker,
		UserID:   parseQQ(cfg.Channel.OwnerQQ),
		Lang:     cfg.Stickers.Lang,
		Reviewer: thinker, // 审核用开思考的那一档 (判"踩没踩红线"是推理活)
	}
	// 审核机: 每条输出在发出去之前过一遍 (人设 + 教练规则 + 这一轮上下文)。
	// ⚠️ 一次性注入 —— 对话是并发 goroutine, 每轮赋值会是数据竞争。
	dialog.Review = engine.ReviewFrame
	if cfg.Review.Enabled {
		log.Info("convo", "review: 已开 (每条输出过审; 打回带原因重生成一次; 失败放行)")
	} else {
		log.Info("convo", "review: 关闭 (config review.enabled=false)")
	}
	// §5.1 人物层回填 (带真实 uin): OpenStore 那次拿不到 owner_qq,
	// 于是默认私聊那一行的历史只能在这里补 —— 同一个人不许变成两笔账。
	if n, err := convo.BackfillBondPerson(store.DB, store.OwnerKey(), cfg.Channel.OwnerQQ); err != nil {
		log.Warn("convo:value", "人物层回填失败(私聊退回读场景行): %v", err)
	} else if n > 0 {
		log.Info("convo:value", "§5.1 人物层回填 %d 人 (含默认私聊)", n)
	}
	// §3.6 事件池预算: 主控从 convo.yaml 读一次 (零值 = 内置默认, 与「不设置」完全等价)。
	// ⚠️ 只设一次: 这个闸门是**进程级**的 (eventgen.go 的 eventGenBud), 不是每个引擎一份。
	convo.SetEventGenBudget(convo.EventGenBudget{
		CallsPerDay:  cfg.Sched.EventGenCallsPerDay,
		Candidates:   cfg.Sched.EventGenCandidates,
		LookbackDays: cfg.Sched.EventGenLookbackDays,
	})
	// Vision: 用户发图 → 看图说话 (v4-flash-vision-exp, 同 key)
	if cfg.AI.APIKey != "" && cfg.AI.VisionModel != "" {
		engine.VisionClient = ai.NewClient(ai.Config{
			BaseURL: cfg.AI.BaseURL, APIKey: cfg.AI.APIKey,
			Model: cfg.AI.VisionModel, Timeout: 60 * time.Second,
		})
		engine.VisionModel = cfg.AI.VisionModel
		log.Info("convo", "vision: configured (%s, 用户发图可看)", cfg.AI.VisionModel)
	}

	// ——— §3.3 环境: 接**真**天气与地震 ———
	//
	// ⚠️ **这根线此前从没接过** (2026-10-14 查出来)。
	//
	// `convo.EnvSource` 只在 cmd/convo-sim (仿真) 与测试里被赋值,
	// 生产进程一次都没有 —— 于是 TodayEnv() 永远 ok=false, 而它下游是:
	//
	//   【你那边】天气/城市段  → 从来没注入过
	//   ingestWorld()          → 第一行就 return, Pi 上世界事件**至今 0 条**
	//   mood 的外部成因        → 从来没有
	//
	// 用户的原话是「角色从来不说自己的生活」。
	// **它不是不愿意说, 是它没有生活可说。** 提示词治不了这个。
	//
	// 未配 (enabled: false) → 整段照旧消失, 行为与从前逐字节一致。
	if cfg.Env.Enabled {
		convo.EnvSource = convo.CachedEnvSource(convo.HTTPEnvSource(cfg.Env), 30*time.Minute)
		log.Info("convo", "env: 已接 (%s %.3f,%.3f) — 天气 Open-Meteo, 地震 USGS",
			cfg.Env.City, cfg.Env.Lat, cfg.Env.Lon)
	} else {
		log.Info("convo", "env: 未配置 — 没有天气/城市/生活事件 (它的一天是空的)")
	}

	// 通道: QQ 第三方 OneBot v11 / NapCat
	var bot *convo.OneBotClient
	if cfg.Channel.Driver == "onebot_v11" {
		bot = convo.NewOneBotClient(cfg.Channel.APIBase, cfg.Channel.AccessToken)
		engine.Sender = bot
		// ⚠️ 图片下载的唯一入口在桥接方那边 (get_image(message_id)) ——
		// 它**不会主动下**, 得我们叫。不接这条线, path_exists=="0" 的图永远不存在。
		engine.ImageFetch = bot
		ws := &convo.OneBotWS{
			AccessToken: cfg.Channel.AccessToken,
			OnEvent: func(ev convo.OneBotEvent) {
				// 戳一戳: post_type=notice + notice_type=notify + sub_type=poke。
				//
				// ⚠️ 只管**戳它自己**的那些 (target_id == self): 群里别人互相戳,
				// 它不该有任何反应 —— 那是别人的互动, 不是它的。
				// 之前这里对非 message 事件一律 return, 于是"被戳"它根本不知道。
				if ev.PostType == "notice" && ev.NoticeType == "notify" && ev.SubType == "poke" {
					self := parseQQ(cfg.Channel.SelfQQ)
					if ev.TargetID == self && self > 0 {
						uin := strconv.FormatInt(ev.UserID, 10)
						scene := "private:" + uin
						if ev.GroupID > 0 {
							scene = "group:" + strconv.FormatInt(ev.GroupID, 10)
						}
						go func() {
							if err := engine.HandlePoke(context.Background(), convo.PokeEvent{
								SceneKey: scene, UIN: uin, GroupID: ev.GroupID, Private: ev.GroupID == 0,
							}); err != nil {
								log.Warn("convo:poke", "处理被戳失败: %v", err)
							}
						}()
					}
					return
				}
				if ev.PostType != "message" {
					return
				}
				// ——— §6.10 / §9 **群消息**: 白名单是第一道门 ———
				//
				// ⚠️ 这一步只做两件事: **判白名单** 与 **按分区记下来**。
				// 它**不回复** —— 因为"该不该接"的决策器 (§6.3) 还没有,
				// 没有它就没有接话判据, 而"进群就抢话"比不说话糟得多。
				//
				// 但**记下来是必须的**: origin + scene_key 现在不写,
				// 等决策器落地时就没有任何群历史可用, 而那段历史补不回来。
				if ev.MessageType == "group" {
					g := convo.DecideGroupIngest(cfg.Group, ev.MessageType, ev.GroupID)
					if !g.Record {
						log.Debug("convo:channel", "群消息不记 (%s) group=%d", g.Reason, ev.GroupID)
						return
					}
					// ⚠️ **图片消息的 PlainText() 是空串** —— 直接存会留下一条空行。
					//
					// 实测 (2026-10-14): 420 条群消息里 **115 条是空的 (27%)**,
					// 拿桥接方历史逐条对过 —— 它们 **segments 全是 image**。
					// 那些空行进上下文之后, 模型看到的是一串空行夹着几句话,
					// 于是接的话驴头不对马嘴。
					//
					// 而且群路径**完全没走图片管线** —— 不看图、不判重、不分流。
					refs := ev.ImageRefs()
					payload := ev.PlainText()
					var lookPath string
					if len(refs) > 0 {
						// ⚠️ **图片一律走完整 IngestImage, 在裁决之前** (2026-10-14 纠正)。
						//
						// 我第一版把它放在"裁决说要说话"之后, 理由是省一次 vision 调用。
						// **那个判断是错的**, 而且错得很隐蔽:
						//
						//   19:08:39  图片消息 → 存 [图片] → 裁决 silent → 不下载、不标注
						//   19:09:20  用户: 你看得见图吗?
						//   19:09:25  它:   看得见              ← 上下文里有个 [图片]
						//   19:09:32  它:   就是刚才那张一片空白  ← 但里面没有内容, 只能编
						//
						// **「一片空白」不是它看到了空白, 是它没东西可看。**
						// 图会留在上下文里被反复引用, 所以它必须**现在就有内容** ——
						// 省下的那一次调用, 换来的是它在群里编。
						//
						// ⚠️ 代价可控: 标注按 md5 缓存, 同一张图只看一次。
						// 群里一天十几张图 = 十几次调用, 而不是每条消息一次。
						//
						// ⚠️ 它也负责**触发下载** (path_exists=="0" 时调 get_image) ——
						// 桥接方不会主动下, 不叫它那张图永远不存在。
						mark, look := engine.IngestImage(ctx, refs[0])
						// ⚠️ **图文同时存在时不许丢掉文字** (2026-10-14 发现)。
						//
						// 原来是无条件 `payload = mark` —— 用户打的那句话被整个盖掉。
						// 「@我 这是谁?」+ 图 会变成只有「[图: …]」, 于是那轮对话里
						// **问题本身消失了**, 它只能对着图自说自话。
						//
						// 顺序是**文字在前** —— 人也是先说话再指图。
						if t := strings.TrimSpace(payload); t != "" {
							payload = t + " " + mark
						} else {
							payload = mark
						}
						// 图交给这一轮的对话 (只有照片给 look; 表情包的标签就够了)。
						lookPath = look
					}
					if strings.TrimSpace(payload) == "" {
						// 没文本也没图 = 撤回 / 桥接方没给内容 —— 什么都没发生, 不记。
						return
					}
					// ⚠️ **这一行被我自己删过一次** (2026-10-14)。
					//
					// 重写图片那段时, 我的替换区间多吃了四行, 把入库整个带走了。
					// 后果不是"图片坏了", 而是**用户消息从此都不入库** ——
					// 图片处理照跑、标注照存, 但消息本身从上下文里消失了。
					// 于是它被问起时没有任何依据, 只能编「图在我这儿裂了」。
					//
					// 症状藏在**提示词长度**里: 12000+ → 6273 字节。
					// 那是"历史少了一半"的读数, 而不是"图片有问题"。
					// ⚠️ **必须带上发言人 uin** (2026-10-14 定案)。
					//
					// 原来走的是 InsertMessageEx —— 它把 sender_uin 硬编码成 "".
					// 后果不是"少个字段", 而是**群里所有人都是同一个「群友」**:
					// recentDialogIn 拼出来的发言流是
					//
					//     群友: 这茶渍为什么这么重
					//     群友: 不好
					//     群友: 给我杯子都干黄了
					//
					// 三个人先后说话, 在它眼里是**同一个人**在自言自语。
					// 而多主体发言流 (§6.10.3) 的全部意义就是"谁说的分得开" ——
					// 没有 uin 它就是白建的, 它接的话只能对着空气。
					uin := strconv.FormatInt(ev.UserID, 10)
					// 2026-10-22 桥修好后: id/seq/text 全是真的; text 就是被引用原话。
					quoteID, quoteText, _ := ev.ReplyQuote()
					if _, err := store.InsertMessageFull("user", "qq", convo.KindForMark(payload), payload,
						convo.MsgMeta{Origin: convo.OriginUser, SceneKey: g.SceneKey, SenderUIN: uin,
							// §6.9 信号 ②「互相 @」的唯一数据来源。
							//
							// ⚠️ 不写它, 冲突判定就只剩 ①③④ 三个信号, 而门槛要 3 个 ——
							// 也就是说**三个必须全中**, 判出冲突会难得多。
							AtUINs: ev.AtUINs(),
							// §6.10.2 ③ **引用回复**: 存住平台 id 与"这条在回哪一条" ✓。
							//
							// ⚠️ `msg_id` 是**回查的钥匙** ✓ —— OneBot 的 reply 段里通常
							// 只有 id 没有正文 ✗, 所以"把被引用的原话附在后面"这件事
							// 只能靠拿这个 id 回查我们自己存过的那条 ✓。
							MsgID: strconv.FormatInt(int64(ev.MessageID), 10),
							// ⚠️ 2026-10-22: 桥的 reply 段现在自带被引用原话 (text) 与真 id。
							// 存 text 是为了「引用那条不在库里/窗口外」时还能附原话, 不必回查。
							ReplyTo:   quoteID,
							ReplyText: quoteText,
						}); err != nil {
						log.Warn("convo:channel", "store group msg: %v", err)
					} else {
						// ⚠️ **成功也要出声** (2026-10-14)。
						//
						// 这一行原本不存在, 而"入库失败"与"入库被删掉"在日志里**长得一模一样**:
						// 两者都是一片安静。我那次误删就是这么潜伏了两轮的 ——
						// 图片链路全绿, 而用户消息其实一条都没进去。
						log.Debug("convo:channel", "群消息入库 kind=%s len=%d: %s",
							convo.KindForMark(payload), len([]rune(payload)), truncStr(payload, 40))
					}
					// ——— §6.3 三态裁决 ———
					//
					// ⚠️ **发言档暂不执行**: 群里的"发言"要走多主体发言流
					// (§6.10.3: history 不再是「你:/她:」二元), 那条路还没建。
					// 现在接上去会让它用**私聊的口吻**在群里说话 —— 那比不说话糟得多。
					//
					// 但**沉默档与表情档现在就是完整的**: 两者都是 0 次 LLM 调用, 不需要多主体流。
					// 所以这一批交付的是"它能在群里**接梗**、且**不抢话**"。
					sc := convo.SceneMsg{
						SceneKey: g.SceneKey,
						UIN:      strconv.FormatInt(ev.UserID, 10),
						// §6.10.4 称呼的原料: 群名片优先, 退回账号昵称 (见 OneBotSender)。
						SenderName: ev.Sender.DisplayName(),
						Text:       payload, // ⚠️ 不是 PlainText() —— 图片消息那边是空串
						AtUINs:     ev.AtUINs(),
						TS:         clock.Now(),
					}
					// §6.10.2 ③ 线程归属的原料: 上一条是谁说的、多久以前。
					// ⚠️ 2026-10-20 修: 这几个字段**从来没被填过**, 于是
					// "无 @ 时按线程归属"在生产上是死代码 (用户: 话题没结束它就不回)。
					if dir, puin, pts, pok, perr := store.PrevSceneMessage(g.SceneKey); perr != nil {
						log.Warn("convo:channel", "取上一条群消息失败(本轮按无线程处理): %v", perr)
					} else if pok {
						sc.HasPrev = true
						if dir == "ai" {
							sc.PrevUIN = cfg.Channel.SelfQQ // 它自己说的 → 这条大概率是在回它
						} else {
							sc.PrevUIN = puin
						}
						if t, terr := time.ParseInLocation("2006-01-02 15:04:05", pts, time.Local); terr == nil {
							sc.PrevTS = t
						}
					}
					// 引用的是**它自己**吗 —— 与 @ 同级 (§6.10.2 ③)。
					// 用库里的方向判 (dir=ai = 它说的), 因为桥的 sender 是 uid 形态、不是数字号。
					if quoteID != "" {
						if dir, _, ok := store.SenderOfMsgID(quoteID); ok && dir == "ai" {
							sc.QuoteToSelf = true
						}
					}
					self := convo.SelfIdentity{UIN: cfg.Channel.SelfQQ}
					verdict := engine.GateGroupMessage(sc, self, cfg.Group)
					store.CounterInc("group_gate_"+string(verdict.Stance), clock.Today()) // §12 三态读数
					// 会话判断机: 被搭话 / 它要开口 → 开或刷新会话 (30 分钟窗口)。
					if verdict.Addressed || verdict.Stance == convo.StanceSpeak {
						engine.TouchSession(g.SceneKey)
					}
					log.Info("convo:channel", "群裁决 %s (%s) — %s",
						verdict.Stance, verdict.AddressedWhy, verdict.Reason)
					// 三态的后两态都是**已经能跑的** —— 第二态 0 次 LLM 调用,
					// 第三态走多主体发言流 (L1 按场景取)。
					switch verdict.Stance {
					case convo.StanceReact:
						go func() {
							if err := engine.ReactInGroup(g.SceneKey, ev.GroupID); err != nil {
								log.Warn("convo:channel", "群表情失败: %v", err)
							}
						}()
					case convo.StanceSpeak:
						// 图片已经在**裁决之前**处理过了 (下载 + 标注 + 判重分流) ——
						// 这里只负责把结果交给这一轮对话。
						//
						// ⚠️ 我第一版把 IngestImage 放在这里, 理由是"只在真要说话时才花 vision"。
						// 那个判断是错的: 裁决 silent 的图**不会**被处理, 但它的 [图片]
						// 仍然留在上下文里 —— 于是之后被问起时, 它面对一个空壳只能编
						// (「就是刚才那张一片空白」)。
						ask := ev.PlainText()
						if strings.TrimSpace(ask) == "" {
							// ⚠️ 图上的字往往才是问题本身, 而图片消息的 PlainText() 是空的。
							ask = payload
						}
						go func() {
							if err := engine.SpeakInGroupSeeing(ctx, g.SceneKey, ev.GroupID,
								strconv.FormatInt(ev.UserID, 10), ask, lookPath); err != nil {
								log.Warn("convo:channel", "群发言失败: %v", err)
							}
						}()
					}
					return
				}
				if ev.MessageType != "private" {
					return
				}
				if owner := parseQQ(cfg.Channel.OwnerQQ); owner != 0 && ev.UserID != owner {
					log.Debug("convo:channel", "ignore non-owner msg user_id=%d", ev.UserID)
					return
				}
				text := ev.PlainText()
				// ⚠️ 用 ImageRefs 而不是 ImageFiles —— 前者多带一个 md5,
				// 而 **md5 是"照片 vs 表情包"唯一的判据** (重复度高的就是表情包)。
				refs := ev.ImageRefs()
				// v2.4.3 诊断: 含 CQ 码或图片的每帧打一条 (一条日志定案 message 形态)
				if len(refs) > 0 || strings.Contains(text, "[CQ:") {
					msg := string(ev.Message)
					if len(msg) > 140 {
						msg = msg[:140] + "…"
					}
					t := text
					if len(t) > 60 {
						t = t[:60] + "…"
					}
					log.Info("convo:channel", "msg-diag: user=%d images=%d message=%q text=%q",
						ev.UserID, len(refs), msg, t)
				}
				// ⚠️ **空文本别存** —— 图片消息的 text 是空的, 存下去就是一条空行。
				// 图本身由 engine.OnUserMessageRefs 按 md5 分流成 [图片]/[表情] 落库。
				if strings.TrimSpace(text) != "" {
					if _, err := store.InsertMessage("user", "qq", "text", text); err != nil {
						log.Warn("convo:channel", "store user msg: %v", err)
					}
				}
				if _, err := store.CounterInc("user_msgs", clock.Today()); err != nil {
					log.Warn("convo:channel", "counter: %v", err)
				}
				go func() { _ = engine.OnUserMessageRefs(ctx, text, refs) }()
			},
		}
		// WS 的**朝向**由配置决定 —— 同一份协议, 两种形态:
		//   server (默认): 桥接方连进来 (NapCat / LLBot 系);
		//   client:        我们连出去 (nimbus 的 BRIDGE_WS_SERVER=1)。
		if cfg.Channel.WSMode == "client" {
			go func() { _ = convo.StartOneBotWSClient(ctx, cfg.Channel.WSURLWithToken(), ws) }()
		} else {
			go func() { _ = convo.StartOneBotWS(ctx, cfg.Channel.WSListen, ws) }()
		}
		if id, name, err := bot.LoginInfo(); err == nil {
			log.Info("convo:channel", "qq login: user_id=%d nickname=%q (建议回填配置 self_qq=%d)", id, name, id)
		} else {
			log.Error("convo:channel", "get_login_info 失败(发送将不可用): %v — 检查 NapCat HTTP API (3001) 与 access_token", err)
		}
	} else {
		engine.Sender = convo.LogSender{UserID: parseQQ(cfg.Channel.OwnerQQ)}
		log.Info("convo", "channel: driver=noop (仅落库/日志, 不接 QQ)")
	}

	// 模块 8: 表情包自搜自选链 (启动即搜一轮 + 每周日增量)
	engine.Cycle = &convo.StickerCycle{
		Cfg:      cfg.Stickers,
		Searcher: convo.NewSearcher(cfg.Stickers),
		Mem:      "和用户是朋友, 聊天会发可爱表情包的 AI 朋友",
	}
	if cfg.Stickers.VisionEnabled && cfg.AI.APIKey != "" {
		engine.Cycle.Scorer = &convo.DeepSeekScorer{
			Client: ai.NewClient(ai.Config{BaseURL: cfg.AI.BaseURL, APIKey: cfg.AI.APIKey, Model: cfg.AI.Model, Timeout: 30 * time.Second}),
			Model:  cfg.AI.VisionModel,
			Mem:    engine.Cycle.Mem,
		}
	}
	go func() {
		cy, c2 := context.WithTimeout(context.Background(), 3*time.Minute)
		defer c2()
		if err := engine.Cycle.Run(cy, store); err != nil {
			log.Warn("convo:sticker", "boot cycle: %v", err)
		}
	}()

	// 模块 11: 主动学梗 (每周日热榜链; 释义必须来自萌娘百科, 无出处不学)
	if chatter != nil {
		engine.SlangCycle = &convo.HotMemeCycle{
			Chatter: chatter,
			Store:   store,
			Moegirl: &convo.MoegirlWeb{Base: "https://zh.moegirl.org.cn", HTTP: &http.Client{Timeout: 15 * time.Second}},
		}
	}

	// 模块 3: 状态机 — 回调 = 睡点日终批 (先注册, tick 后启动)
	sched.OnStateChange = func(state string, at time.Time) {
		engine.OnStateChange(state, at)
	}
	sched.Start(ctx)
	engine.StartTicker(ctx)

	// 模块 9: 管理 Web + API
	srv := &convo.Server{
		Token:         token,
		Store:         store,
		Sched:         sched,
		Chatter:       chatter,
		DataDir:       cfg.DataDir,
		LangCode:      cfg.Stickers.Lang,
		FirecrawlBase: cfg.Stickers.FirecrawlBase,
		AdminDisk:     *adminDir,
	}
	if err := srv.Start(cfg.Listen); err != nil {
		log.Error("convo", "server: %v", err)
		os.Exit(1)
	}
	log.Info("convo", "http://%s (token: %s)", cfg.Listen, token)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Info("convo", "shutting down…")
	srv.Shutdown()
}

// truncStr 截断日志用的短串 (按 rune 截, 不会把汉字切成半个)。
func truncStr(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
