package convo

import (
	"context"
	"database/sql"
	"fmt"
	"html"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/tzhnahida/VocabLock/internal/clock"
	"github.com/tzhnahida/VocabLock/internal/log"
)

// Dice / DiceFloat 是本包唯一的随机接缝 (与 clock 同构)。
//
// 所有调度类抽样 —— 打字延迟、分条间隔、抖动、偷回、话题骰子、表情包加权 ——
// 都必须走这里: 否则"固定种子的仿真"并不可复现 (实测同 seed=42 两次结果不同,
// 因为引擎混用了全局 math/rand 与挂钟播种的 rand)。仿真侧覆盖这两个变量即可。
var (
	Dice      = rand.Intn
	DiceFloat = rand.Float64
)

// ——— 运行时编排: 用户消息 + tick 动作 → 对话引擎 → 发送 ———
//
// 原则: 一切"什么时候说"由 scheduler 裁决 (纯函数), 这里只执行;
// 随机数 (延迟种子/偷回骰子/话题骰子) 均由系统掷, LLM 只取用。

// Sender 通道发送抽象 (OneBotClient 实现; QQ 零 UI 聊天全部出口)。
type Sender interface {
	SendText(userID int64, text string) error
	SendImage(userID int64, localPath string) error
}

// GroupSender 群发送能力 (**可选接口**)。
//
// ⚠️ 为什么不直接往 Sender 上加两个方法:
// 那会波及**所有**已有实现与测试替身 (LogSender / simSender / styleCapSender …),
// 每个都要补两个空方法 —— 而它们里没有一个真的会往群里发。
// 用可选接口: 能力是**声明式**的, 不支持群发送的实现在编译期就看得出来
// (类型断言失败), 而不是运行时静默什么都不发。
type GroupSender interface {
	SendGroupText(groupID int64, text string) error
	SendGroupImage(groupID int64, localPath string) error
}

// Engine 编排器。
type Engine struct {
	// gate 群裁决的读数搬运 (见 gatememo.go) —— 不进任何判断。
	gate *gateMemo

	// §3.7 夜间多 AI 社会模拟的**同伴名册**。
	//
	// ⚠️ 空 = 没有别的 AI 可聊 → 夜间社交整批跳过, 而且**要出声** (log.Warn)。
	// 静默跳过会让「同伴没配」和「模拟坏了」长得一模一样 (P7 的教训)。
	NightPeers []NightPeer

	// LearnSearch 自主学习用的搜索口。nil = 按配置现造一个 Firecrawl。
	LearnSearch LearnSearcher

	// ChimeRollFn 插话掷骰, nil = Dice。**测试注入用**。
	ChimeRollFn func(n int) int

	Store   *Store
	Cfg     Config
	Sched   *Scheduler
	Dialog  *DialogEngine
	Sender  Sender
	Chatter Chatter
	UserID  int64  // owner QQ (消息对象)
	Lang    string // 目标语言 (默认 ja)
	Cycle   *StickerCycle

	// ImageFetch 触发桥接方下载一张图 (可选)。nil = 不主动取, 只认已在盘上的。
	//
	// ⚠️ 桥接方**不会主动下** —— 下载的唯一入口是 get_image(message_id)。
	// 所以 path_exists == "0" 时我们必须叫它一声, 否则那张图永远不存在。
	ImageFetch ImageFetcher

	// Vision: 用户发图 → 描述+拟人评论 (nil=未配置, 降级文案)
	//
	// ⚠️ 类型是**接口**不是 *ai.Client —— 具体类型没法替身,
	// 而"图片这条路"恰恰是最需要能测的 (它踩过一串 bug: 字段没接对、
	// 扩展名骗人、群路径根本没接、动图只认第一帧)。
	// *ai.Client 天然满足 VisionChatter, 赋值处一行都不用改。
	VisionClient VisionChatter
	VisionModel  string

	// 模块 11: 主动学梗 (每周日热榜链; nil=未配置)
	SlangCycle *HotMemeCycle

	// worldWarn 的每日限流 (见 worldWarn 注释)。
	worldWarned string
}

// OnUserMessage 统一入口: 用户消息 (文本 + 图片列表, 可同时)。
// OnUserMessage 兼容入口 (只有路径, 没有 md5)。
//
// ⚠️ 没 md5 就判不了重 —— 那会当成照片 (保守: 宁可多看一次)。
// 真实路径走 OnUserMessageRefs (桥接方给了 md5)。
func (e *Engine) OnUserMessage(ctx context.Context, text string, images []string) error {
	refs := make([]ImageRef, 0, len(images))
	for _, p := range images {
		refs = append(refs, ImageRef{Path: p})
	}
	return e.OnUserMessageRefs(ctx, text, refs)
}

func (e *Engine) OnUserMessageRefs(ctx context.Context, text string, refs []ImageRef) error {
	// ⚠️ **既没文本也没图 = 真的什么都没发生** (撤回 / 桥接方没给内容)。
	//
	// ⚠️ 注意这与"空文本但**有图**"是**两回事**: 后者是图片消息的正常形态,
	// 绝不能在这里挡掉 —— 那正是"它看不见图"的第一个原因。
	if strings.TrimSpace(text) == "" && len(refs) == 0 {
		log.Debug("convo:engine", "空消息 (无文本无图) — 忽略")
		return nil
	}
	if len(refs) == 0 {
		return e.OnUserText(ctx, text)
	}
	// ——— 图片: 走**与群路径同一个** IngestImage ———
	//
	// ⚠️ 这里原本是一大段内联逻辑 (prepareImage → describeImage → 分流 → 落库),
	// 而它与群路径**长得像但不完全一样** —— 那种"两份实现"迟早分叉,
	// 而分叉的表现是"群里看得见、私聊看不见"(或者反过来), 极难查。
	//
	// ⚠️ 内联那版还有个具体的病: 它直接用 refs[0].Path, **不验文件在不在**。
	// 而桥接方的导出曾经一次都没成功过 (exportOne 拿导出目标当源去 Stat),
	// 于是一堆图片段的 path 指向不存在的文件 —— 那时它会存一个 [图片],
	// 然后编出「图太糊了 看半天」。
	mark, look := e.IngestImage(ctx, refs[0])
	_, _ = e.Store.InsertMessage("user", "qq", KindForMark(mark), mark)
	if look != "" {
		// 图交给对话 —— 这一轮它**看着图**说话。
		return e.OnUserTextImage(ctx, text, look)
	}
	// 没图可看 (表情包 / 没读到): 用标记兜底, 别让它空着问。
	if strings.TrimSpace(text) == "" {
		text = mark
	}
	return e.OnUserText(ctx, text)
}

// KindForMark 标记 → 消息 kind (**导出给 cmd 用**: 群路径也要按标记定 kind)。
//
// ⚠️ kind 不只是分类: **recentDialogIn 会按它过滤**, 用错就等于那条消息不存在。
func KindForMark(mark string) string {
	switch {
	case strings.HasPrefix(mark, "[表情"):
		return "sticker"
	case strings.HasPrefix(mark, "[图片"), strings.HasPrefix(mark, "[照片"):
		return "image"
	}
	return "text"
}

// prepareImage 图片就位: URL → 下载临时文件; 本地路径 → 直接用。
// v2.4.2: URL 先 HTML 反转义 (CQ 字符串里 &amp; 是编码态, 不转义 → 404)。
func (e *Engine) prepareImage(ctx context.Context, file string) (string, error) {
	file = html.UnescapeString(file) // 幂等: 无转义时原样返回
	if strings.HasPrefix(file, "http://") || strings.HasPrefix(file, "https://") {
		dir := filepath.Join(e.Cfg.DataDir, "convo", "inbox")
		if err := os.MkdirAll(dir, 0755); err != nil {
			return "", err
		}
		hc := &http.Client{Timeout: 15 * time.Second}
		path, err := DownloadImage(ctx, hc, file, dir, "")
		log.Info("convo:engine", "image prepare: url → %s (%v)", path, err)
		return path, err
	}
	// ⚠️ **本地文件也要留一份** (2026-10-14)。
	//
	// 桥接方回的路径**两类都不能依赖**:
	//   · `~/.nimbus/media/<md5>.<ext>` —— 桥的导出目录 (它那边还在修);
	//   · `<QQ>/nt_data/Pic/...`        —— **QQ 客户端的缓存, 会被清理**。
	//
	// `get_image` 下完回的就是后者。拿它当长期存储
	// = 把"这张图我看过"建在一个随时会被删的文件上 ——
	// 而现象是"标签库里的图过几天全没了", 且不会有任何报错。
	owned := e.OwnImage(file)
	log.Info("convo:engine", "image prepare: local %s → %s", file, owned)
	return owned, nil
}

// describeImage 视觉模型: 描述图片 + 小夜口吻的一句第一反应。
// 通道 D: 若是表情包/梗图, 输出尾部带 【梗图】<≤15 字含义> 行。
func (e *Engine) describeImage(ctx context.Context, path string) (string, error) {
	if e.VisionClient == nil || e.VisionModel == "" {
		return "", fmt.Errorf("vision not configured")
	}
	system := "你是" + e.Lang + "语境里的说话人。看图后: 用一两句口语描述图里发生了什么/有什么细节, 再补一句像真人收到朋友照片时的第一反应 (可以吐槽/夸/好奇)。总共不超过 3 句, 口语化, 不要书面语。另外: 如果这张图是表情包/梗图/沙雕图 (不是普通照片), 在最后单独输出一行: 【梗图】<≤15 字>它表达的意思。"
	// ⚠️ 动图抽多帧分开传 —— 与 LabelImage 同一条 (GIF 只认第一帧)。
	paths, animated := visionReadyPaths(path)
	q := "朋友发来这张图, 你看到了什么? 自然地说。"
	if animated {
		q += animatedNote
	}
	out, err := visionChat(ctx, e.VisionClient, e.VisionModel, system, q, paths)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// splitMemeNote 把看图描述拆成 (**能进用户消息行的口语部分**, **只给系统看的标注**)。
//
// A5 的病灶不是"标注不该存在", 而是"标注和口语走同一条路" —— 于是 `【梗图】<含义>`
// 变成了用户说的话。拆开之后: 口语进上下文, 标注走 side-channel (表情包库 + vision_note 行,
// 两者都被 recentDialog 的 kind 过滤挡在上下文之外)。
//
// ⚠️ 返回的 spoken **必须**先 TrimSpace: 否则照片描述后面会留一堆空行。
func splitMemeNote(comment string) (spoken, note string) {
	i := strings.Index(comment, "【梗图】")
	if i < 0 {
		return strings.TrimSpace(comment), ""
	}
	note = strings.TrimSpace(strings.TrimPrefix(comment[i:], "【梗图】"))
	return strings.TrimSpace(comment[:i]), note
}

// extractMemeNote 从看图描述里抽【梗图】标注行 (没有=普通照片, 不收录)。
func extractMemeNote(comment string) string {
	if i := strings.Index(comment, "【梗图】"); i >= 0 {
		note := strings.TrimSpace(strings.TrimPrefix(comment[i:], "【梗图】"))
		note = strings.Trim(note, "】")
		note = strings.TrimSpace(note)
		if runes := []rune(note); len(runes) > 15 {
			note = string(runes[:15]) + "…"
		}
		return note
	}
	return ""
}

// captureUserSticker 用户发的梗图 → 复制进表情包库 (affinity 0.55 起, 带 note)。
func (e *Engine) captureUserSticker(ctx context.Context, imgPath, note string) (string, error) {
	dir := filepath.Join(e.Cfg.Stickers.Dir, e.Cfg.Stickers.Lang)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	ext := strings.ToLower(filepath.Ext(imgPath))
	if ext == "" {
		ext = ".gif"
	}
	dst := filepath.Join(dir, fmt.Sprintf("user_%d%s", clock.Now().UnixNano(), ext))
	if err := copyFile(imgPath, dst); err != nil {
		return "", err
	}
	id, err := e.Store.InsertSticker("user-sent://"+note, dst, e.Cfg.Stickers.Lang, "user", 0.55)
	if err != nil {
		return "", err
	}
	if note != "" {
		_ = e.Store.SetStickerNote(id, note)
	}
	return dst, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

var urgentWords = []string{"救命", "急事", "很急", "紧急", "在吗", "在么", "help"}

// ——— 用户消息路径 ———

// OnUserText 处理用户消息 (已在通道层落库; 此处裁决与回复)。
// OnUserText 纯文本的一轮。
func (e *Engine) OnUserText(ctx context.Context, text string) error {
	return e.onUserText(ctx, text, "")
}

// OnUserTextImage 带图的一轮 —— 图**直接进对话调用**。
func (e *Engine) OnUserTextImage(ctx context.Context, text, imagePath string) error {
	return e.onUserText(ctx, text, imagePath)
}

func (e *Engine) onUserText(ctx context.Context, text, imagePath string) error {
	if e.Chatter == nil {
		log.Warn("convo:engine", "chat 未配置, 不回复: %s", text)
		return nil
	}
	now := clock.Now()
	p, _ := e.Store.LoadProfile()
	ws, _ := e.Store.LoadSchedule()
	state := CurrentState(ws, now)
	if p == nil {
		log.Info("convo:engine", "未创建朋友, 用户消息仅记录: %q", text)
		return nil
	}

	// §教练机: owner 用「教练: <规则>」直接下指令。
	//
	// ⚠️ **在这里短路**: 不让它走正常对话 —— 那会花一次调用, 而且模型会把指令
	// 当聊天内容解读, 于是"教它"变成了"跟它讨论要不要听"。
	if rule, ok := ParseCoachCommand(text); ok {
		if _, err := e.Store.AddCoachRule(e.ownerKey(), rule, ""); err != nil {
			log.Warn("convo:coach", "教练指令落库失败: %v", err)
		} else {
			log.Info("convo:coach", "§教练机 owner 纠正了一条: %s", rule)
		}
		// 回一句人话确认。**不是"已记录"** —— 那三个字立刻暴露这是个系统。
		if uid, perr := strconv.ParseInt(strings.TrimSpace(e.Cfg.Channel.OwnerQQ), 10, 64); perr == nil && uid > 0 {
			if err := e.Sender.SendText(uid, CoachAck(rule)); err != nil {
				log.Warn("convo:coach", "确认语发送失败: %v", err)
			}
		}
		return nil
	}
	// 1. 上次 ask 被拒? → 黑名单 + 小失望注入
	rejection, _ := e.Store.RejectionEvent()
	if rejection != "" && DetectRejection(text) {
		_ = e.Store.AddAskBlacklist(boundaryCategory(rejection))
		// p 边界黑名单同步落盘
		p.Boundaries.AskBlacklist = append(p.Boundaries.AskBlacklist, boundaryCategory(rejection))
		p.Boundaries.AskBlacklist = uniqueStrings(p.Boundaries.AskBlacklist)
		_ = e.Store.SaveProfile(p)
		_, _ = e.Store.CounterInc("ask_share_reject", clock.Today())
	}

	// 1b. 求证缓冲 (§8.6): 用户回复 pending 记忆 → 确认升级/纠正修正/无回应丢弃
	e.resolvePending(text)

	// 2. 表情包反馈 (§8.7 步骤 6): 上次发过表情包 → 长回复+0.1 / 无视-0.05
	e.computeStickerFeedback(text)

	// 3. 教学态秒回 (求助 → 永远秒回)
	teaching := HelpDetect(text)

	// 4. 状态裁决的即时性
	immediate := teaching || state == StateFree || state == StateSlack
	consec := e.consecutiveUserMsgs()
	urgent := containsAny(text, urgentWords)
	if state == StateFocus && (consec >= 3 || urgent) {
		immediate = true // 破功钩子
	}
	if state == StateSleep && urgent {
		immediate = true // 紧急容忍
	}
	if state == StateBusy {
		immediate = false // 30-90 分钟延迟
	}
	if !immediate {
		// busy: 排延迟回复 (骰子在系统: 30-90min)
		e.scheduleDelayedReply()
		return nil
	}

	// 5. 即时回复 — 系统骰子: 5-25s 打字延迟 (真人节奏; 教学/紧急/破功除外)
	// §8.3 强制破功: **求助与紧急不看心情** —— 它心情再差, 你求助它也秒回。
	// ⚠️ 危机词也要绕过: 「今天不想说话」不能变成「我最需要的时候它不理我」。
	if !DayMoodBypass(text, teaching, urgent) {
		beNow := e.bodyEffect(now)
		// §4.3 疲劳: 与身体**同一条乘法链** (至多抬一档, 见 FatigueEffectTable 的注释)。
		// §4.4.5 睡眠质量: 与心情/身体/疲劳**同一条乘法链** ——
		// 分开算的话, 四者会各乘一次并叠成"常驻慢速", 那时延迟不再能归因于任何一个状态。
		// ⚠️ 链**只有一份实现** (typing.go pacingFactor)。
		// 原来这里手抄了一份, 而 groupTypingDelay 用的是另一份 —— 两份已经分叉过
		// (抄的那份有身体效应, 另一份漏了), 现象是"群里和私聊快慢不一样"。
		factor := e.pacingFactor()
		noteBodyDelay(beNow.Active, factor)
		delay := scaleSeconds(5+Dice(21), factor)
		notePacingDelay(delay)
		if err := clock.Sleep(ctx, time.Duration(delay)*time.Second); err != nil {
			return err
		}
		log.Debug("convo:engine", "reply delayed %ds (human pace, mood x%.2f)", delay, factor)
	}
	// §2.6.3 被顶: 记素材, 不在热路径生成灵光。
	// §5.1 **私聊也有关系量。**
	//
	// ⚠️ 关系量原来**只有群路径**在更新 (GateGroupMessage 的 defer), 而
	// 主动开口恰恰发在私聊场景上、用的是 `heatStageOf(DefaultSceneKey)` ——
	// 于是私聊永远是 stranger, cap 20 < 代价 25, 主动配额恒为 0。
	// §2.6.5 **被接住之后**: 这一句有没有接住刚才那条灵光。
	//
	// ⚠️ 排在关系量之前不是随意的: 它是"这一轮说了什么"的处置,
	// 而关系量是"这一轮的互动质量"。两者互不依赖, 但顺序固定了好读。
	e.noteSparkPickup(text, now)
	e.NoteSceneBond(DefaultSceneKey, text, now)
	e.noteChallenge(text, e.lastAIText(3))
	dayMoodNote := ""
	if line, ok := DayMoodLine(DayMoodOf(clock.Today())); ok {
		dayMoodNote = line
	}
	frame, err := e.dialogReply(ctx, DialogInput{
		UserText:  text,
		ImagePath: imagePath,
		// §5.7 / §7.3: **私聊也要有场景键**。
		//
		// ⚠️ 这里原来是空的, 而取记忆走的是全局口径 —— 后果不是"少一点上下文",
		// 而是"关系级记忆绑场景"(§7.3)在私聊里退化成**全排除**:
		// 带场景标签的记忆一条都取不到, 而它静默得看不出来。
		SceneKey:   DefaultSceneKey,
		CoachNote:  e.coachNote(""),
		RepeatNote: e.repeatNote(DefaultSceneKey),
		NewsNote:   e.newsNote(),
		WorldNote:  e.worldNote(),
		State:      state,
		ActionKind: ActImmediate,
		Teaching:   teaching,
		Rejection:  e.rejectionFor(p, text),
		// §10.3 依赖护栏: 每一条**用户消息**都过一遍 (危机必须当轮就响)。
		GuardNote:   e.guardNote(text),
		DayMoodNote: dayMoodNote,
		// §2.5 段 4b (周档) 与段 7.4 (轮级)。
		TasteNote:  e.tasteNote(p),
		RevealNote: e.revealNote(text),
	}, p)
	if err != nil {
		log.Error("convo:engine", "reply: %v", err)
		return err
	}
	return e.sendFrame(ctx, frame)
}

// recentDialog 取最近 N 条对话组装 L1 历史文本 ("你:/她:" 前缀, 保持上下句联系)。
func (e *Engine) recentDialog(n int) string { return e.recentDialogIn("", n) }

// recentDialogIn 按场景构造 L1 发言流。sceneKey 为空 → 全局 (私聊的既有行为)。
//
// ⚠️ 群路径**必须**传 sceneKey —— 见 Store.RecentMessagesIn 的注释。
func (e *Engine) recentDialogIn(sceneKey string, n int) string {
	var msgs []MessageRecord
	var err error
	if strings.TrimSpace(sceneKey) == "" {
		msgs, err = e.Store.RecentMessages(n)
	} else {
		msgs, err = e.Store.RecentMessagesIn(sceneKey, n)
	}
	if err != nil || len(msgs) == 0 {
		return ""
	}
	// §6.10.3 **多主体发言流**: 群场景里每一行要带"是谁说的"。
	//
	// ⚠️ **一次查询, 后面全程用 map** —— 不要在循环里逐条查(那是 N 次 IO)。
	// ⚠️ 取不到名字时传空串给 `SceneHistoryLine`, 它会落"某人"而**仍然带 uin 尾号** ——
	// 也就是说"没名字"**不会退化成二元**(二元下三个人说话会被接成同一个人)。
	nameByUIN := map[string]string{}
	if SceneKindOf(sceneKey) == SceneKindGroup {
		if members, merr := e.Store.SceneMembers(sceneKey); merr == nil {
			for _, mb := range members {
				if n := strings.TrimSpace(mb.DisplayName); n != "" {
					nameByUIN[mb.UIN] = n
				}
			}
		}
	}

	// ⚠️ **先收成一行一条, 最后再统一去重** (2026-10-14)。
	//
	// 重复图片的「引用式」标记必须在**这一刻**决定 —— 只有这时才知道
	// 那份完整内容还在不在窗口里。入库时决定过一次, 结果是线上出现
	// 两条互相指向的「往上找」, 指针悬空 (详见 imgcollapse.go)。
	var out []string
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		// ⚠️ **`image` / `sticker` 必须放行** —— 它们不是噪声, 是"群里发生了什么"。
		//
		// 原来只放行 text/ask, 于是图片消息被整个滤掉: 它看不到"有人发了张图",
		// 只能看到前后两句话, 而那两句话往往正是在说那张图。
		if m.Kind != "text" && m.Kind != "ask" && m.Kind != "image" && m.Kind != "sticker" {
			continue
		}
		// ⚠️ **空文本 + kind=text, 在群里 = 一张图。**
		//
		// 2026-10-14 实测: 420 条群消息里有 115 条是这样 (27%)。
		// 当时我差点把它们当噪声删掉 —— 那是错的: 拿 nimbus 的历史逐条对过,
		// 它们 **segments 全是 image**。删掉等于把"群里有人发了张图"
		// 从它记忆里抹掉, 而它之后接的话会完全不搭。
		//
		// 根因是 `ImageFiles` 不认桥接方的 `path` 字段 (已修);
		// 但**存量那些已经这么躺着了**, 所以这里也要认。
		line := m.Payload
		if strings.TrimSpace(line) == "" {
			line = "[图片]"
		}
		if len([]rune(line)) > 300 {
			line = string([]rune(line)[:300]) + "…"
		}
		// ——— §6.10.3 **多主体发言流** ———
		//
		// 私聊只有一个主体 → 保持「你:/她:」二元 (加 uin 是噪声, 而且**会改动每一轮的
		// system prompt**, 那是拿缓存换没有的信息)。
		//
		// 群里**必须**多主体: 二元格式下"你"指谁是不确定的 ——
		// 三个人先后说话, 模型会把它接成同一个人说的, 于是**第一句话就接错人**。
		// 每行带发言人标识, uin 后四位做锚 (两个人同名也分得开)。
		//
		// ⚠️ **owner 标记绝不进注入** (§6.10.7 / §5.6 护栏 2):
		// 这里只输出 uin 尾号, 没有任何特权标记 —— 把 owner 标进发言流
		// 等于在群里公示主人。
		if SceneKindOf(m.SceneKey) == SceneKindGroup {
			// ⚠️ **uin 为空时不许拼出「群友????」** —— UinTail("") 返回的就是 "????"。
			// 那既难看又**丢了信息**: "有个人说的"和"uin 是问号"是两件事。
			// (这个 bug 是变异测试的输出意外暴露的 —— 我本来只在测私聊格式。)
			if m.Dir == "ai" {
				out = append(out, "她: "+line) // 它自己在群里也是"她", 不必再标 uin
				continue
			}
			// ⚠️ **空 uin 走更早的回退, 不许进 SceneHistoryLine** ——
			// `UinTail("")` 返回的就是 `????`, 而"有个人说的"和"uin 是问号"是两件事。
			// 这一条有专门用例钉着(它当初是**变异测试**意外暴露的)。
			if strings.TrimSpace(m.SenderUIN) == "" {
				out = append(out, "群友: "+line)
				continue
			}
			// ⚠️ §6.10.3 要的格式是 **`昵称(uin 后四位): 内容`**, 不是"群友+尾号"。
			// 昵称让人读得顺, 尾号保证**两个人同名也分得开**。
			out = append(out, SceneHistoryLine(nameByUIN[m.SenderUIN], m.SenderUIN, line))
			continue
		}
		who := "你"
		if m.Dir == "ai" {
			who = "她"
		}
		out = append(out, who+": "+line)
	}
	// 同一张图的第 1 次留完整内容, 第 2 次起换成「内容往上找」(短标对得上)。
	out = CollapseRepeatedImageMarks(out)
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, "\n") + "\n"
}

// dialogReply 组装输入 → 对话引擎。
func (e *Engine) dialogReply(ctx context.Context, in DialogInput, p *PersonaProfile) (OutputFrame, error) {
	// ⚠️ **每轮显式设置, 包括清空** —— 见 DialogEngine.ImagePath 的注释。
	if e.Dialog != nil {
		e.Dialog.Vision, e.Dialog.VisionModel = e.VisionClient, e.VisionModel
		// 工具口: 只有 Chatter 真的支持工具时才给 —— 否则每次调用都要先失败一次。
		if _, ok := e.Chatter.(ToolChatter); ok {
			e.Dialog.Tools = e
		} else {
			e.Dialog.Tools = nil
		}
		e.Dialog.ImagePath = in.ImagePath
	}
	ws, _ := e.Store.LoadSchedule()
	in.SlotLabel = slotLabel(ws, clock.Now())
	// A2: 状态是**系统事实** —— 三元组在这里定, 之后模型只负责润色成话。
	fact := BuildStateFact(ws, clock.Now())
	in.Fact = &fact

	// P4: 心情由系统持有 (成因/半衰期/变化率上限), 这里只取快照注入。
	// 事件入账在 stepTick 里, 与有没有人跟它说话无关。
	if mood, err := e.Store.LoadMood(); err == nil {
		in.Mood = &mood
	}
	// §4.2 身体: 与心情同一条路 (系统持有, 模型只表达)。
	// ⚠️ 读不到就**不注入** —— 与 moodWillingness() 读不到返回 1.0 同一条原则:
	// 失败往"正常说话"倒, 变成"没病"而不是变成"病了"。
	if body, err := e.Store.LoadBody(); err == nil {
		body.Refresh(clock.Now())
		if body.Active {
			in.Body = &body
		}
	}
	// §6.10.6 它在**这个群**里的位置 (身份, 不是判据)。
	// 读不到 → 不注入 (失败往"正常说话"倒, 不退化成"它在群里没有位置")。
	if in.SceneKey != "" && SceneKindOf(in.SceneKey) == SceneKindGroup {
		if sc, serr := e.Store.LoadScene(in.SceneKey); serr == nil && sc != nil {
			in.SelfPlace = SelfRoleLine(sc, e.Cfg.Group)
		}
	}
	// §5.5 主体性: 违抗许可 + 约定状况 (系统裁决, 模型只在许可内挑一档)。
	// ⚠️ 必须排在 dayNote **之前** —— 日级处境段要从它取「待履行/补偿/可否再约」。
	in.Agency = e.agencyContext(clock.Now(), in.UserText, in.Topic, p)
	// §4.3 今天的处境: 拒绝许可 + 社交疲劳 (系统裁决, 模型只管照着说)。
	in.DayNote = e.dayNote(clock.Now(), in.UserText, in.Agency)
	// §3.6.8: 还在场上的世界事件 (阶段算出来的, 不落库)
	in.EventNotes, _ = e.Store.InPlayEvents(clock.Now(), 6)
	// §4.5 层 2: 今天的行程 (夜里排好、一整天逐字节不变 → 前缀缓存友好)。
	// 读不到就是不注入 —— 失败往"正常说话"倒, 不退化成"它今天没有安排"。
	if plan, ok, perr := e.Store.LoadDayPlan(e.ownerKey(), clock.Today()); perr == nil && ok {
		in.DayPlan = plan
	}
	// §4.2.2 身体三量: **现算**, 不落库 (读不到就是基线, 绝不折成 0)。
	in.BodyAxes = e.bodyAxes()
	// §4.2.4 口渴这一轮给不给"随口说"的例外 (预算 + 冷却)。
	// ⚠️ 只传布尔, **不传次数** —— 传了次数就会变成配额宣告(§4.6.1)。
	in.ThirstMaySpeak = e.thirstBudget(clock.Now()).Allowed
	// §5.5 自主预约要判的三件事, 素材都在这里备齐 (缺一个就有一条硬线静默失效)。
	in.DayPlanTomorrow, _, _ = e.Store.LoadDayPlan(e.ownerKey(), TomorrowKey(clock.Now()))
	in.Week, _ = e.Store.LoadSchedule()
	// ⚠️ 这里原来有一行 `in.SelfPlansToday = counterOrZero(...)` —— 那个字段**已删**:
	// 它按设计就没打算被读(配额闸门落在代码里, 不进提示词, §4.6.1),
	// 留着一个没人读的字段只会让人以为有人用它。
	// ⚠️ **按场景取** (§5.7 + §7.3): 原来这里走的是全局口径(sceneKey=""),
	// 于是"关系级记忆绑场景"在私聊里退化成**全排除** ——
	// 带场景标签的记忆一条都取不到, 而那是静默地少一半上下文。
	// 群路径一直传着 SceneKey, 只有私聊没传。
	in.Memories, _ = e.Store.TouchAndTopMemoriesFor(in.SceneKey, 20) // top-K 20 (~800 token)
	// §7.2 **联想式检索** —— 存档式(上面那行)与联想式是两件事, 设计稿说
	// 「**联想式才是人味的来源**」。
	//
	// ⚠️ 放在这里而不是各调用方: 它原来只接在群路径上, 而设计稿举的例子
	// ("刚路过宠物店 想起你那只猫") 恰恰是私聊。放这一处, 两条路都有。
	if in.Association == "" {
		in.Association = e.assocNote(in.SceneKey, in.UserText)
	}
	in.Pending, _ = e.Store.PendingMemories()
	in.LastSummary, _ = e.Store.LastSessionSummary()
	in.Lang = e.Lang
	// ⚠️ 按**场景**取 L1: 群路径漏传 sceneKey 会把别处的话读进来 (跨人转述)。
	// 私聊时 in.SceneKey 为空 → 走全局, 行为与升级前逐字节相同。
	in.History = e.recentDialogIn(in.SceneKey, 12) // L1: 最近 ~6 轮 (含本轮 user 消息, 冗余无碍)
	// 模块 11: 梗库注入 + 接住感知
	in.Slangs, _ = e.Store.TopSlang(12)
	if in.UserText != "" {
		if hits, err := e.Store.FindSlangHit(in.UserText); err == nil && len(hits) > 0 {
			in.SlangHit = hits[0].Phrase
			_ = e.Store.TouchSlang(hits[0].ID) // 命中=词条还活着 (衰减重置)
		}
	}
	frame, err := e.Dialog.Reply(ctx, e.Chatter, in, p, e.Lang)
	// P5 决策轨迹: **成功失败都记** —— 失败的那一轮往往最值得回放。
	e.recordTrace(in, frame, err)
	// §3.6.8: 回复里真的提到了哪条事件 → 重置它的沉降时钟。
	// ⚠️ 只在真的发出去之后才算"被提及"; 降级/硬拒的产物不算 (那不是它说的)。
	if err == nil && !frame.Degraded && !frame.Refused {
		txt := FrameText(frame)
		_, _ = e.Store.NoteEventMentions(txt, clock.Now(), 20)
		// §4.2 硬线: 身体不适**只影响行为, 不索取情绪价值**。
		// ⚠️ 这里**只记不拦** —— 拦一条消息属于改硬线 (L2), 要拍板。先把数字拿到手再决定。
		if be := e.bodyEffect(clock.Now()); be.Active {
			if hits := BodySolicitHits(txt); len(hits) > 0 {
				noteBodySolicit()
				log.Warn("convo:engine", "§4.2 硬线疑似命中(病中索取安慰): %v · %q", hits, txt)
			}
		}
		// §4.3 拒绝权: 它**真的说了**今天不想聊才记一次 (配额 1/天)。
		// 不记的话配额永远用不掉, "最多说一次"就是个摆设。
		e.noteDeclineIfAny(txt)
		// §5.5 写路径 A: 违抗账本。
		// 只在**真的违抗**时记 —— "同一件事连续杠到上限就恢复正常"要靠它累积;
		// 不记的话那个上限永远不会触发, 于是变成"永远可以杠"。
		if frame.Defy != nil && in.Agency != nil {
			if m := ParseDefianceMode(frame.Defy.Mode); m != DefyNone && m <= in.Agency.Defiance {
				if rerr := e.Store.RecordDefiance(in.Agency.TopicKey, m, "frame", clock.Now()); rerr != nil {
					log.Warn("convo:engine", "违抗记账失败(上限将不会触发): %v", rerr)
				}
			}
		}
		// §5.5 写路径 B: 约定落库。
		// ⚠️ 时刻解析不出 → **静默丢弃, 不重试** —— 与 remember 空对象同规:
		// "约了但系统不知道约在几点" 比 "没约" 更糟, 因为那条约定永远结算不了。
		if frame.Promise != nil && in.Agency != nil && in.Agency.MayCommit {
			if due, ok := parsePromiseWhen(frame.Promise.When, clock.Now()); ok {
				if _, cerr := e.Store.CreateAppointment(Appointment{
					Tier: TierPromise, Who: e.UserID, Scene: "qq",
					Text:      frame.Promise.What,
					CreatedTS: clock.Now().Format(bodyTSLayout),
					DueTS:     due.Format(bodyTSLayout),
					Terminal:  ApptOpen, Reason: "frame",
				}); cerr != nil {
					log.Warn("convo:engine", "约定落库失败: %v", cerr)
				} else {
					// §4.5.3 第三条写入路径: 约定插入 → 该时段从随机窗豁免、主动开场让路。
					// ⚠️ 放在 else 里: 约定没落库却改了日程, 会得到一个"有约但不存在"的时段。
					e.noteAppointmentInSchedule(frame.Promise.What, due)
				}
			} else {
				log.Warn("convo:engine", "约定的时刻解析不出, 丢弃: %q", frame.Promise.When)
			}
		}
		// §5.5 写路径 C: 它**主动处置**了一条待履行约定 (提前取消/改期, 或承认忘了)。
		//
		// ⚠️ 这一条补的是「**它能说不行**」。在它之前, 一条约定只有两种结局:
		// 赴约, 或者到点了才反悔 —— 而提前打招呼(信用 0.5)与放鸽子(0.0)是两件事,
		// 分不开就归因不了, 也就没有"有分寸"可言。
		//
		// ⚠️ 只在**真的发出去了**之后才写 (与 noteDeclineIfAny 同规):
		// 降级/硬拒的产物不算它说的, 记了会污染信用曲线。
		if frame.Settle != nil && in.Agency != nil {
			e.settleFromFrame(frame.Settle, in.Agency.Pending)
		}
		// §5.5 写路径 D: 自主预约 —— 它自己往**次日**行程里加一件事。
		//
		// ⚠️ 也是"只在真的发出去了之后"才写: 降级/硬拒的产物不算它说的,
		// 而"我说过明天要去书店"和"日程上真有这回事"必须同步 —— 否则它会拿一个不存在的安排当拒绝依据。
		// §3.6.5c 事后记一次"这一轮提到了谁" —— 60 天沉降的**唯一输入**。
		//
		// ⚠️ 与 noteDeclineIfAny / NoteThirstMention 同规: 只在**真的发出去了**之后记。
		// 记了降级/硬拒的产物, 会让一个早该退出的配角永远留在当下。
		// ⚠️ 只在真有名字命中时才落库 —— 每轮都写世界观是白白烧 IO。
		if wb, wok, werr := e.Store.LoadWorldBible(e.ownerKey()); werr == nil && wok {
			if hit := NoteCastMentions(&wb, txt, clock.Today()); len(hit) > 0 {
				if serr := e.Store.SaveWorldBible(e.ownerKey(), wb); serr != nil {
					log.Warn("convo:cast", "配角提及日写不上(沉降将不会触发): %v", serr)
				}
			}
		}
		// §4.2.4 事后认一次"随口提到的口渴" —— 与 noteDeclineIfAny 同规:
		// 只在**真的发出去了**之后才记, 降级/硬拒的产物不算它说的。
		e.NoteThirstMention(txt)
		// §4.5.2 写路径 E: 报"没做成" —— 落点就是**今天行程的那个 undone 位**。
		// ⚠️ 与自我失信是同一件事的两面: 这一位既是"计划与执行的落差",
		// 也是"对自己的承诺"的账本(§5.5)。
		if frame.PlanUndone != nil {
			if ok, why := e.MarkPlanUndone(in.DayPlan, frame.PlanUndone.At, frame.PlanUndone.Why, clock.Now()); !ok {
				log.Info("convo:undone", "没记成: %s", why)
			}
		}
		if frame.SelfPlan != nil {
			if ok, why := e.AddSelfPlan(frame.SelfPlan, in.DayPlanTomorrow); !ok {
				log.Info("convo:selfplan", "自主预约没并入日程(%s): %s", why, frame.SelfPlan.What)
			}
		}
	}
	if err == nil {
		e.Dialog.SideEffects(frame, in, p)
		e.Dialog.ValidateFrame(frame, in, p) // 校验已在 Reply 内做 — 此处仅日志
	}
	return frame, err
}

// sendOp 一帧内的发送单元 (v2.4.1 分条: 先解析后按节奏逐条发)。
type sendOp struct {
	kind      string // text | sticker
	text      string
	path      string
	stickerID int64
}

// MaxReplyItems 一帧里最多发几条。
//
// ⚠️ **提示词里的上限不算数** —— 实测模型会无视它。必须有一道代码层的硬闸:
// 对方发一句话, 它连回 5 条; 那是刷屏, 不是聊天。
// 真人连发 3 条以上通常发生在吵架或激动时, 日常聊天就是 1-2 条。
const MaxReplyItems = 2

// replyGapSeconds 条间打字间隔 (系统骰子): 1-4s, 10% 概率 6-10s 长间隔 (补刀/改口的停顿)。
// factor = 心情节奏系数 (§8.3 低唤醒 → 延迟长)。
func replyGapSeconds(factor float64) int {
	if factor <= 0 {
		factor = 1.0
	}
	if Dice(10) == 0 {
		return scaleSeconds(6+Dice(5), factor)
	}
	return scaleSeconds(1+Dice(4), factor)
}

// scaleSeconds 按节奏系数放大秒数, 但**至少保留 1 秒** ——
// 四舍五入不该把"慢一点"变成"立即"。
func scaleSeconds(sec int, factor float64) int {
	s := int(float64(sec)*factor + 0.5)
	if s < 1 {
		return 1
	}
	return s
}

func (e *Engine) sendFrame(ctx context.Context, frame OutputFrame) error {
	// A6 硬拒: 刻意不说。**不发送、不降级、不当故障** —— 静默丢弃即可。
	if frame.Refused {
		log.Warn("convo:engine", "硬拒: 本条未发送 (%s)", frame.RefuseReason)
		return nil
	}
	if len(frame.Reply) == 0 {
		return nil
	}
	if !e.validOwner() {
		return fmt.Errorf("owner_qq invalid")
	}
	_, _ = e.Store.CounterInc("rounds", clock.Today())
	// 帧回执: ask_share 接受路径无显式交互 → intent 记录已在 SideEffects

	// §8.3 表达层: 这一轮的心情节奏 (低唤醒 → 间隔更长 / 更少表情)。
	willingness := e.moodWillingness(clock.Now())
	pacingFactor := MoodPacingFactor(willingness)
	stickerKeep := MoodStickerKeepProb(willingness)
	// §4.2 身体: 延迟再乘一档 (>=1)。
	// ⚠️ **长度因子不在这里动手** —— 长度是模型生成的, 系统侧硬截断属 §8.4 风格后处理。
	// 这一批只把病中/平时的句长**并排记下来**, 让数字决定要不要真的做 (§4.2 的"必须落在行为上")。
	be := e.bodyEffect(clock.Now())
	pacingFactor *= be.DelayFactor
	// §4.1.3 上行对节奏的修正 —— 与下行走**同一个**变量, 所以两条通道不会各算一套。
	if m, merr := e.Store.LoadMood(); merr == nil {
		pacingFactor = upliftPacing(UpliftOf(m, clock.Now()), pacingFactor)
	}
	pacingFactor *= e.fatigueEffect(clock.Now()).DelayFactor // §4.3 疲劳 (同一条链)
	pacingFactor *= e.sleepEffect().DelayFactor              // §4.4.5 睡眠质量 (同一条链)
	// §8.4 风格后处理: 状态在场时才收; 零值 = 逐字节原样返回。
	// ⚠️ **必须在发送与入库之前** —— 入库存的是「它真正说出去的那句」 (§7.5 双轨存储)。
	// ⚠️ 已知小瑕疵: dialogReply 里的记账 (事件提及 / 梗计数 / 硬线探针) 用的是**后处理前**
	//    的文本。宁可漏判也不误判 —— 后处理只会让文本更短, 不会凭空造出违规。
	styleOpt := styleOptions(be, willingness)

	// 1. 解析为发送单元 (表情包先完成骰子挑选; 失败项直接丢弃)
	var ops []sendOp
	hasSticker := false
	// textCount 数的是**文本条数** (表情不计) —— 表情是"接一下", 不是"又说了一句"。
	textCount := 0
	for _, item := range frame.Reply {
		switch item.Kind {
		case "text":
			// ⚠️ 硬闸在**这里**, 不在提示词里 —— 见 MaxReplyItems 的注释。
			if textCount >= MaxReplyItems {
				log.Warn("convo:engine", "回复条数超上限 %d, 丢弃后续 (%q)", MaxReplyItems, truncateStr(item.Text, 20))
				continue
			}
			textCount++
			ops = append(ops, sendOp{kind: "text", text: item.Text})
		case "sticker":
			// §8.3 少表情: 低意愿时按概率丢掉这一张。
			// ⚠️ **只在帧里还有别的消息时才丢** —— 纯表情帧丢掉 = 整条消息凭空消失,
			// "少表情"不该变成"说不出话"。
			if len(frame.Reply) > 1 && Dice(100) >= int(stickerKeep*100) {
				noteStickerDropped()
				continue
			}
			hasSticker = true
			var st *Sticker
			var stErr error
			if item.StickerID == 0 {
				st, stErr = e.Store.WeightedPick(e.Cfg.Stickers.CapPerSticker) // 系统骰子: affinity 加权 ≤30% 封顶
			} else {
				st, stErr = e.Store.StickerByID(item.StickerID)
			}
			if stErr != nil {
				st = nil
			}
			_ = stErr
			if st == nil || st.LocalPath == "" {
				log.Warn("convo:engine", "sticker pick failed")
				continue
			}
			ops = append(ops, sendOp{kind: "sticker", path: st.LocalPath, stickerID: st.ID})
		}
	}

	// 2. 逐条发送: 条间随机间隔 (真人打字节奏), 最后一条后不等待
	sentAny := false
	for i, o := range ops {
		if i > 0 {
			if err := sleepReplyGap(ctx, pacingFactor); err != nil {
				return err
			}
		}
		switch o.kind {
		case "text":
			// §8.4: 后处理只做「减」(丢句 / 去语气词 / 压标点 / 截到句界), **不改语义** ——
			// style.go 有语料级不变式: 输出必须是输入的 rune 子序列, 且不含输入里没有的字符。
			o.text = ApplyStyle(o.text, styleOpt)
			// 打字节奏: **打完了才发** —— 与群发同一条尺子 (typing.go)。
			// ⚠️ 私聊原来完全没有这一段, 只有"回复前等 5-25 秒"那个与字数无关的随机延迟。
			if d := e.groupTypingDelay(o.text); d > 0 {
				if serr := clock.Sleep(ctx, d); serr != nil {
					return serr
				}
			}
			if err := e.Sender.SendText(e.UserID, o.text); err != nil {
				log.Warn("convo:engine", "send text: %v", err)
				continue
			}
			kind := "text"
			if frame.Degraded {
				kind = "fallback" // 降级产物: 留痕但不进上下文 (否则它会接自己的故障)
			}
			_, _ = e.Store.InsertMessage("ai", "qq", kind, o.text)
			e.Store.TouchSlangText(o.text)                // 模块 11: AI 用了已知梗 → 计数 (生命周期)
			noteBodyReply(be.Active, len([]rune(o.text))) // §4.2: 病中 vs 平时的句长样本
			sentAny = true
		case "sticker":
			if err := e.Sender.SendImage(e.UserID, o.path); err != nil {
				log.Warn("convo:engine", "send sticker: %v", err)
				continue
			}
			_, _ = e.Store.InsertMessage("ai", "qq", "sticker", fmt.Sprintf("%d|%s", o.stickerID, o.path))
			_ = e.Store.AdjustAffinity(o.stickerID, 0) // use_count+1 (affinity 不动)
			sentAny = true                             // 表情包发送成功也计入 (纯 sticker 帧不触发兜底)
		}
	}
	// 3. sticker-only 帧全失败 → 兜底文本, 绝不让"说想发但发不出"变成无声 (2026-09-06 修复)
	if !sentAny && hasSticker {
		if err := e.Sender.SendText(e.UserID, "（表情包还在挑，先欠着，回头补你一张好的）"); err == nil {
			_, _ = e.Store.InsertMessage("ai", "qq", "fallback", "（表情包还在挑，先欠着，回头补你一张好的）")
		}
	}
	// 4. ask_share 帧携带的问题必须发送 (§7.6) — 问题文本独立成条, 隔一拍自然发问
	if frame.AskShare != nil && frame.AskShare.Question != "" {
		if sentAny {
			if err := sleepReplyGap(ctx, pacingFactor); err != nil {
				return err
			}
		}
		if err := e.Sender.SendText(e.UserID, frame.AskShare.Question); err != nil {
			log.Warn("convo:engine", "send ask_share: %v", err)
		} else {
			_, _ = e.Store.InsertMessage("ai", "qq", "ask", frame.AskShare.Question)
		}
	}
	return nil
}

func sleepReplyGap(ctx context.Context, factor float64) error {
	d := replyGapSeconds(factor)
	log.Debug("convo:engine", "burst gap %ds", d)
	return clock.Sleep(ctx, time.Duration(d)*time.Second)
}

// HookBreakCooldownMin / HookStealCooldownMin 两个私聊钩子的冷却。
//
// ⚠️ 它们防的是**同一个东西**: tick 每分钟一次, 而触发条件 (
// ConsecutiveUserMsgs / UrgentWordHit) 会**持续成立**。没有冷却 = 条件成立期间每分钟发一条。
const (
	HookBreakCooldownMin = 40
	HookStealCooldownMin = 30
)

// hookClaim 冷却闸 —— **检查与记账是一对, 不许拆开**。
//
// ⚠️ 我第一版只检查不记账, 于是 LastCounterTS 永远是空, 冷却**永远放行** ——
// 而现象是"日志里看着有冷却这道闸, 但没有一条被拦住"。
// 读不到上次时刻 → 放行 (没触发过不等于要拦)。
func (e *Engine) hookClaim(name string, gapMin int, day string) bool {
	if gapMin > 0 {
		if ts, ok, err := e.Store.LastCounterTS(name); err == nil && ok {
			if clock.Now().Sub(ts) < time.Duration(gapMin)*time.Minute {
				return false
			}
		}
	}
	// 记账: 与上面的检查**同一次调用**完成。
	if _, err := e.Store.CounterInc(name, day); err != nil {
		log.Warn("convo:engine", "钩子计数失败 (%s): %v", name, err)
	}
	return true
}

// lastOpenAgo 距上一条主动开场的分钟数 (ok=false = 无记录, 不设冷却限制)。
// 数据源是 convo_counters 的 updated_ts —— 它由注入时钟盖章, 因此时间旅行下同样成立。
func (e *Engine) lastOpenAgo(now time.Time) (bool, int) {
	ts, ok, err := e.Store.LastCounterTS("ai_opens")
	if err != nil || !ok {
		return false, 0
	}
	return true, int(now.Sub(ts).Minutes())
}

// ——— tick 动作路径 ———

// OnTick 执行 scheduler 裁决结果。
func (e *Engine) OnTick(ctx context.Context, action TickAction) error {
	if e.Chatter == nil || action.Kind == ActNone {
		return nil
	}
	p, _ := e.Store.LoadProfile()
	if p == nil {
		return nil // 未创建朋友, 无主动行为
	}
	now := clock.Now()
	day := clock.Today()

	switch action.Kind {
	case ActPreview:
		return e.proactive(ctx, p, DialogInput{State: e.currentState(), ActionKind: ActPreview}, "previews", day)
	case ActActiveOpen:
		roll := RollTopicDice()
		topic, err := e.Store.NextTopic(roll)
		if err != nil {
			log.Warn("convo:engine", "topic: %v", err)
			topic = Topic{Kind: "pure"}
		}
		// §2.6 **灵光分享**: 唯一落点就是段 7.3 (不新增段)。
		//
		// ⚠️ 它排在 ask 之前: 一条正在温着的灵光比一次例行询问有价值得多,
		// 而它自己有**硬闸** —— 周配额 ≤2(SparkSharePerWeek) + 说过的跳过 + 档位 fresh/warm。
		// ⚠️ 周配额是**生活上限**, 只在 ShareableSpark 里判, **不进提示词**(§4.6.1)。
		if spTopic, spID, ok := e.sparkShareTopic(now); ok {
			e.SpendSceneHeat(DefaultSceneKey, now)
			// ⚠️ **发出去之后才记 said_ts** —— 不用 defer:
			// 那条路一旦失败也会把它标成"说过了", 于是这条灵光**永远不再被说**,
			// 而它其实谁都没听过。
			if perr := e.proactive(ctx, p, DialogInput{State: e.currentState(), ActionKind: ActActiveOpen, Topic: spTopic}, "ai_opens", day); perr != nil {
				return perr
			}
			e.MarkSparkSaidAfter(spID)
			return nil
		}
		// 双向索取合流: 本周 due 且系统掷中 → 话题注入 ask 倾向
		if due, err := e.Store.AskDue(now); err == nil && due && Dice(100) < 30 {
			topic = Topic{Kind: "ask", Text: "自然地问一下 ta 的生活: 周末/工作/吃的一件小事 (只问一个, 别连环问)"}
		}
		// §6.7 **主动发起扣费**: 每条主动消息 = 25 热力 (§6.7:940)。
		//
		// ⚠️ 只在这里扣 (主动发起), **回话不扣** —— 被 @ 必答是硬门槛;
		// 回话也扣的话, 忙群会因为递减退化永远攒不出 25, 大群必然变哑巴。
		e.SpendSceneHeat(DefaultSceneKey, now)
		return e.proactive(ctx, p, DialogInput{State: e.currentState(), ActionKind: ActActiveOpen, Topic: topic}, "ai_opens", day)
	case ActReturnGreet:
		return e.proactive(ctx, p, DialogInput{State: e.currentState(), ActionKind: ActReturnGreet}, "ai_opens", day)
	case ActNoInput:
		return e.proactive(ctx, p, DialogInput{State: e.currentState(), ActionKind: ActNoInput}, "noinput", day)
	case ActStealReply:
		// ⚠️ **必须限定私聊场景** —— 摸鱼偷回是私聊行为, 不该捞群里的输入。
		if !e.hookClaim("steal_reply", HookStealCooldownMin, day) {
			return nil
		}
		last, _ := e.lastUserTextIn(DefaultSceneKey)
		// ⚠️ 私聊里没有可接的话就不发 —— 触发条件可能来自群消息 (ConsecutiveUserMsgs
		// 不分场景), 那时候"接一句"接的是空气。
		if strings.TrimSpace(last) == "" {
			return nil
		}
		return e.proactive(ctx, p, DialogInput{State: e.currentState(), ActionKind: ActStealReply, UserText: last}, "rounds", day)
	case ActDelayedReply:
		e.clearDelayedReply()
		return e.proactive(ctx, p, DialogInput{State: e.currentState(), ActionKind: "busydelay"}, "rounds", day)
	case ActBreakFocus:
		// ⚠️ **必须限定私聊场景** (2026-10-14 生产事故的直接修复)。
		//
		// 原来取的是"最新一条入站消息", 不过滤场景也不限说话人 ——
		// 于是**别人在群里**讨论"nahida 像不像 AI", 它把那段话捞进私聊,
		// 对着 owner 回了一轮防守话。跨场景搬家 + 重复回应, 两个问题叠加。
		// 群裁决已经判过 silent 了, 摸鱼接话不该绕开那条判决。
		//
		// ⚠️ 还要冷却: 触发条件 ConsecutiveUserMsgs **不分场景**, 群里连发 3 条也算;
		// 而 tick 是 60 秒一次 —— 没有冷却的话, 条件成立期间**每分钟都触发一次**。
		// 实测输出比 21 ai : 2 user 就是这么来的。
		if !e.hookClaim("breakreply", HookBreakCooldownMin, day) {
			return nil
		}
		last, _ := e.lastUserTextIn(DefaultSceneKey)
		if strings.TrimSpace(last) == "" {
			return nil
		}
		return e.proactive(ctx, p, DialogInput{State: e.currentState(), ActionKind: "breakreply", UserText: last}, "rounds", day)
	case ActResume:
		// §4.3 中断-恢复: 接着说。
		// 计 "rounds" 而**不是** "ai_opens" —— 它不是一次新的开口, 不该吃掉今天的主动配额。
		it, ok, _ := e.Store.LoadInterruption()
		if !ok {
			return nil
		}
		_ = e.Store.ClearInterruption() // 先清: 免得这一句失败后每分钟重试一次
		return e.proactive(ctx, p, DialogInput{State: e.currentState(), ActionKind: ActResume, UserText: it.AnchorTS}, "rounds", day)
	}
	return nil
}

// proactive **私聊出口** —— 群里说话走 SpeakInGroup, 不走这里。
//
// ⚠️ **场景一致性契约**: in.SceneKey 必须与这个出口一致 (= private:owner),
// 不一致即**不发**。
//
// 它防的是整整一类 bug, 而不是某一次: 任何"从别处取了输入、却从私聊出口发出去"
// 的路径都会被这一条拦住, 而这类 bug 的现象是"它在私聊里回应群里的事"——
// 从日志上极难看出是哪一环串了。
//
// 群场景的主动行为走 SpeakInGroup, 那条路自带 groupID。
func (e *Engine) proactive(ctx context.Context, p *PersonaProfile, in DialogInput, counter, day string) error {
	if in.SceneKey != "" && in.SceneKey != DefaultSceneKey {
		log.Error("convo:engine", "场景一致性违反: proactive 是私聊出口, 却拿到场景 %q — 本条不发", in.SceneKey)
		return nil
	}
	// 无来由的低意愿日: **今天不开场**。
	//
	// ⚠️ 落在**执行点**而不是决策链里: DecideTick 的优先级链是实测调出来的,
	// 碰它有回归风险。而"今天不主动开口"本来就只是一句 return。
	// ⚠️ 只拦 ActActiveOpen —— 中断-恢复 (ActResume) 是"接着说", 不是"开口",
	// 拦它会把"回到刚才那句话"变成又一次消失。
	if DayMoodBlocksAction(in.ActionKind, DayMoodOf(clock.Today())) {
		log.Info("convo:engine", "今天不开场 (低意愿日) —— 无归因, 不写 MoodLog")
		return nil
	}
	frame, err := e.dialogReply(ctx, in, p)
	if err != nil {
		log.Error("convo:engine", "proactive reply: %v", err)
		return err
	}
	if err := e.sendFrame(ctx, frame); err != nil {
		return err
	}
	_, _ = e.Store.CounterInc(counter, day)
	return nil
}

// ——— 每分钟 tick 执行循环 ———

// StepTick 单次裁决 (exported wrapper for sim)。
func (e *Engine) StepTick() { e.stepTick(context.Background()) }

// PrepareDailyJobs 睡点日终批 (exported wrapper for sim)。
// maybeNews 每天**醒着的时候**去看一眼世界。
//
// ⚠️ 挂在 tick 而不是夜间批: 夜间批跑的是"刚过去的那一天"的盘点,
// 而新闻要**当天早上**看 —— 晚上才看的话, 白天群里聊热点它还是插不上嘴。
//
// ⚠️ 先占坑再干活 (与 §2.5 学习同一条): 跑到一半挂了也不补算,
// 补算会让"今天没抓到"变成"今天抓两遍"。
func (e *Engine) maybeNews(ctx context.Context) {
	day := clock.Today()
	if n, err := e.Store.CounterGet("news_fetch", day); err == nil && n > 0 {
		return
	}
	st := e.currentState()
	if st != StateFree && st != StateSlack {
		return // 睡着/忙着的时候不刷手机
	}
	if _, err := e.Store.CounterInc("news_fetch", day); err != nil {
		log.Warn("convo:news", "占坑失败: %v", err)
	}
	p, perr := e.Store.LoadProfile()
	if perr != nil {
		p = nil
	}
	e.FetchDailyNews(ctx, p)
}

func (e *Engine) PrepareDailyJobs() { e.prepareDailyJobs(clock.Now()) }

// recordTrace 落一条决策轨迹 (§P5 / D1)。
//
// ⚠️ **记不上绝不影响回复**: 观测失败不能拖垮主流程, 只 warn 一句。
// 与 moodWillingness 读失败返回 1.0 是同一条原则。
func (e *Engine) recordTrace(in DialogInput, f OutputFrame, err error) {
	fact := ""
	if in.Fact != nil {
		fact = strings.TrimSpace(in.Fact.Line())
	}
	var mv, ma float64
	if in.Mood != nil {
		mv, ma = in.Mood.Valence, in.Mood.Arousal
	}
	ids := make([]string, 0, len(in.Memories))
	for _, m := range in.Memories {
		ids = append(ids, m.MemID)
	}
	sent := make([]string, 0, len(f.Reply))
	for _, it := range f.Reply {
		if strings.TrimSpace(it.Text) != "" {
			sent = append(sent, it.Text)
		}
	}
	t := DecisionTrace{
		// §6.10 护栏 3「偏向可见」的三个数据源。
		// ⚠️ 它们**必须在决策轨迹里**, 因为"它更愿意接谁的话"这件事
		// 只能从**逐轮的裁决记录**里现算出来 —— 另存一份聚合就是双真源。
		SpeakerUIN: in.SpeakerUIN,
		SceneKey:   in.SceneKey,
		Stance:     string(in.Stance),
		Reason:     in.StanceReason,

		TS:           clock.TodayTS(),
		ActionKind:   in.ActionKind,
		Proactive:    in.IsProactive,
		UserText:     in.UserText,
		Teaching:     in.Teaching,
		StateFact:    fact,
		MoodVal:      mv,
		MoodAro:      ma,
		MemIDs:       ids,
		SlangHit:     in.SlangHit,
		SystemPrompt: f.SystemPrompt,
		PromptBytes:  len(f.SystemPrompt),
		RawOutput:    f.RawOutput,
		Outcome:      ClassifyOutcome(f, err),
		SentTexts:    sent,
	}
	if rerr := e.Store.RecordTrace(t); rerr != nil {
		log.Warn("convo:trace", "轨迹落库失败 (不影响回复): %v", rerr)
	}
}

// ⚠️ **B6 试过并撤回了, 记在这里免得后人再走一遍。**
//
// 病灶: 主动开场"每天恒为 07:3x + 11:5x 两条" —— 配额从上限变成了节拍器。
// 直觉修法: 加一道"开口冲量"闸门 (由心情/事件算), 冲量不够就不开口, 让触发来自事件。
//
// **[实测 2026-10-13] 没用, 而且是可预见地没用**:
// 冲量 = 意愿 × (0.5 + 0.5×心情偏离)。在这个世界里 意愿恒在 0.45-0.52、偏离恒在 0.02-0.17,
// 于是冲量**恒在 0.5 附近**, 从不低于 0.25 的阈值。把阈值压到 0.001 做对照 ——
// **两天分布逐字相同** (60 次: 07时28 · 08时2 · 11时5 · 12时25)。
//
// **教训: 拿一个"日内几乎不变"的信号去驱动"日内时刻", 只会得到一个恒开或恒关的开关。**
// 要让开口时刻真的散开, 需要一个**日内会变化**的输入 —— 而当前世界只有一天一次的天气。
// 前置条件是 §3.6 的事件池 / §3.7 的夜间社会模拟 (都还没建), 不是再调一个系数。

// OpenImpulse 今天有没有值得开口的事 (B6): **由世界事件的新鲜度决定**。
//
// ⚠️ 这是对上一版的修正。上一版用「心情偏离」算冲量, 而心情在日内几乎不变 ——
// 冲量恒在 0.5 附近, 闸门永不触发, 开关两态的分布**逐字相同**, 于是整段撤回了。
// 现在的输入是**刚刚发生了什么**: §3.6 的事件池让事件散在 09-20 时, 日内真的会变。
//
// 语义: 事件刚发生 → 冲量高 (有话想说); 超过 ImpulseDecayHours 没动静 → 归零。
// ⚠️ 读不到就给 (`0, false`) = 闸门不生效 —— 失败往「正常说话」倒。
func (e *Engine) OpenImpulse(now time.Time) (float64, bool) {
	last, ok, err := e.Store.LastWorldEventTS()
	if err != nil {
		return 0, false
	}
	if !ok {
		return 0, true // 还没有任何世界事件 → 确实没有值得开口的事
	}
	h := now.Sub(last).Hours()
	switch {
	case h < 0:
		return 1, true
	case h >= ImpulseDecayHours:
		return 0, true
	default:
		return 1 - h/ImpulseDecayHours, true
	}
}

// ImpulseDecayHours 冲量从 1 衰减到 0 的小时数。
const ImpulseDecayHours = 4.0

// moodWillingness 读当前说话意愿 (§8.3 情绪污染)。读不到就返回 1.0 = **不压制** ——
// 心情系统坏了不该让它变成哑巴: 失败要往「正常说话」倒, 不能往「不说话」倒。
func (e *Engine) moodWillingness(now time.Time) float64 {
	m, err := e.Store.LoadMood()
	if err != nil {
		return 1.0
	}
	return m.Willingness(now)
}

// ingestWorld 环境 → 世界事件 → 心情 (§3.2 的唯一通道)。
// 幂等: 同一场雨一天只推动一次 —— 所以它可以每分钟被路过。
// ingestWorld 环境 → 世界事件 → 心情 (§3.2 的唯一通道)。
//
// ⚠️ **这里每一步都必须出声** —— P7 踩过一次:
// 入库失败时调用方原来写的是 `if err == nil && n > 0`, 于是 SQL 报错被**完全吞掉**,
// 世界事件一条都不入库, 而仿真报告照样跑完、心情照样"有个数"(退回基线), **看不出任何异常**。
//
// 驱动本身没问题 —— 实测列名与值个数不匹配会明确报 `11 values for 10 columns`。
// **静默的不是数据库, 是调用方。**
// worldWarn 每天每阶段只报一次 —— 失败若是持续的(库坏了), ingressWorld 每分钟都会路过,
// 不加限流就是一天 1440 行, 把真正有用的日志淹掉。
// **坏消息要响, 但不必噪音。**
func (e *Engine) worldWarn(phase string, err error) {
	key := clock.Today() + "|" + phase
	if e.worldWarned == key {
		return
	}
	e.worldWarned = key
	log.Warn("convo:engine", "世界事件 %s 失败 (这一天的相应功能降级): %v", phase, err)
}

func (e *Engine) ingestWorld() {
	env, ok := TodayEnv()
	if !ok {
		return
	}
	now := clock.Now()
	// ① 当天**底色** (天气这类整天都在的): 立刻入账, 幂等。
	if n, err := e.Store.IngestWorldEvents(EnvEvents(env)); err != nil {
		e.worldWarn("底色入账", err) // 心情会退回基线
	} else if n > 0 {
		log.Info("convo:engine", "世界底色入账 %d 条 → 心情已更新", n)
	}
	// ② 当天**事件池** (§3.6): 先入池 (幂等), 再取**到点**的。
	//
	// 这是 B6 的前置 —— 没有它, 世界在**日内是静止的**, 主动开场只能贴着"最早可行时刻"发。
	if _, err := e.Store.SeedDrafts(DayDrafts(env, now.Format("2006-01-02"))); err != nil {
		e.worldWarn("事件池入池", err) // 今天不会有事发生
	}
	due, derr := e.Store.DrawDueDrafts(now)
	if derr != nil {
		e.worldWarn("取到点事件", derr)
	} else if len(due) > 0 {
		if n, ierr := e.Store.IngestWorldEvents(due); ierr != nil {
			e.worldWarn("到点事件入账", ierr)
		} else if n > 0 {
			log.Info("convo:engine", "事件到点 %d 条: %s", n, due[0].Text)
		}
	}
}

// StartTicker 每秒裁决循环: 攒输入 → DecideTick (纯函数) → OnTick 执行。
func (e *Engine) StartTicker(ctx context.Context) {
	go func() {
		e.stepTick(ctx)
		tick := e.Cfg.Sched.TickEverySec
		if tick <= 0 {
			tick = 60
		}
		t := time.NewTicker(time.Duration(tick) * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				e.stepTick(ctx)
			}
		}
	}()
}

func (e *Engine) stepTick(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			log.Error("convo:engine", "tick panic recovered: %v", r)
		}
	}()
	now := clock.Now()
	// P4: **世界事件走 tick, 不走对话**。
	//
	// 原先放在 dialogReply 里 —— 于是"没人跟它说话的日子, 天气不影响心情"。
	// 那正好把世界驱动架空成"只在被召唤时才有世界": 四季对照里 30 天只入账 10-18 条,
	// 缺的就是没有对话的那些天。
	//
	// 幂等由 event_key 唯一约束保证, 每分钟路过一次不会重复入账。
	e.ingestWorld()
	// §2.3 L3: 配角**今天**在忙什么 —— 与世界事件同一条理由:
	// 没人跟它说话的日子, 它的人也在过日子。
	// ⚠️ 每天每人只生成一次 (判据是 CastMember.StatusDay, 不是"距上次多久")。
	if p, perr := e.Store.LoadProfile(); perr == nil && p != nil {
		e.maybeRefreshCastStatus(ctx, p)
	}
	// §4.2 身体: 与世界事件**同一条路径** —— 有没有人跟它说话, 都不影响它会不会病。
	e.ensureBody(now)
	// §5.5: 到点的约定结账 (去没去, 要有终态; 没有终态的约定永远算不出信用)。
	e.settleAgency(now)
	ws, _ := e.Store.LoadSchedule()
	due := 0
	if e.delayedDue() {
		due = 1
	}
	last, _ := e.lastUserText()
	hasLast, lastAgo := e.lastOpenAgo(now)
	be := e.bodyEffect(now) // §4.2 身体快照 (与心情同源: 系统持有, 模型只表达)
	// §6.7 热力现值 (场景级)。先取好再填字面量 —— 决策器保持纯函数, 不碰 IO。
	heatV, hasHeat := e.heatNow(now)
	sleepFx := e.sleepEffect() // §4.4.5 睡眠质量 (昨夜的事实, 现算)
	in := TickInput{
		Now:                  now,
		Sched:                ws,
		State:                CurrentState(ws, now),
		HadSlot:              ws != nil,
		ActiveToday:          counterOrZero(e.Store, "ai_opens", clock.Today()),
		NoInputToday:         counterOrZero(e.Store, "noinput", clock.Today()),
		PreviewToday:         counterOrZero(e.Store, "previews", clock.Today()),
		PendingReply:         due,
		SilentDays:           e.silentDays(),
		StealRoll:            Dice(100),
		DelayMin:             30 + Dice(61),
		OpenJitterRoll:       Dice(100),
		HasLastOpen:          hasLast,
		MinutesSinceLastOpen: lastAgo,
		ConsecutiveUserMsgs:  e.consecutiveUserMsgs(),
		UrgentWordHit:        containsAny(last, urgentWords),
		MoodWillingness:      e.moodWillingness(now),
		BodyQuotaDelta:       be.QuotaDelta,
		HasBody:              be.Active,
		Heat:                 heatV,
		HasHeat:              hasHeat,
		SleepQuotaDelta:      sleepFx.QuotaDelta,
	}
	// B6: 触发来自事件, 配额只做上限。
	if imp, ok := e.OpenImpulse(now); ok {
		in.OpenImpulse, in.HasImpulse = imp, true
	}
	// §4.3 中断-恢复: 到点了吗 —— 判据是**时钟**, 不是概率。
	// (必须在这里而不是 TickInput 字面量里: 它依赖 now, 而字面量在 now 之后、in 之前没有空位。)
	if it, ok, ierr := e.Store.LoadInterruption(); ierr == nil && ok && it.DueAt(now) {
		in.InterruptionDue = true
	}
	// §2.5 自主学习 / §3.5 刷新闻 —— **与 tick 动作无关, 必须无条件跑**。
	//
	// ⚠️ 它们原来挂在 OnTick 里面, 而 OnTick **只在 action.Kind != ActNone 时**被调用。
	// 而"free/slack 且今天没什么事"的那些 tick —— 也就是绝大多数 tick ——
	// DecideTick 返回的正是 ActNone。
	//
	// 于是 §2.5 从上线以来**一次都没跑过** (learn_runs 恒为 0)。
	// 而旁边那句注释写的正是「永不触发的闸门比不写更糟」—— 它自己就长成了那个样子。
	//
	// ⚠️ 这就是为什么它们**不能**挂在 OnTick 里: 那个函数的语义是"执行一个动作",
	// 而这些是"每天都该看一眼"的定时任务, 两者不是一回事。
	e.maybeLearn(ctx)
	e.maybeNews(ctx)

	action := DecideTick(in, e.Cfg.Sched)
	if action.Kind != ActNone {
		log.Info("convo:engine", "tick → %s (%s)", action.Kind, action.Reason)
		_ = e.OnTick(ctx, action)
	}
	log.Debug("convo:engine", "tick done state=%s", in.State) // 心跳: 便于观察"tick 活着"
}

func counterOrZero(s *Store, name, day string) int {
	n, err := s.CounterGet(name, day)
	if err != nil {
		return 0
	}
	return n
}

func (e *Engine) silentDays() int {
	var ts string
	err := e.Store.DB.QueryRow(`SELECT MAX(ts) FROM convo_messages WHERE dir='user'`).Scan(&ts)
	if err != nil || ts == "" {
		return 0
	}
	t, err := time.Parse("2006-01-02 15:04:05", ts)
	if err != nil {
		return 0
	}
	days := int(clock.Now().Sub(t).Hours() / 24)
	if days < 0 {
		return 0
	}
	return days
}

// validOwner 发送前守卫: UserID==0 必是配置问题, 显式 ERROR (2026-09-06 修复)。
// ownerKey 数据分区键 (§9)。**不随人格变动** —— 换人格不该换记忆。
// 取不到配置时退回 "default": 一个稳定的键比一个会变的键重要。
func (e *Engine) ownerKey() string {
	if e.Store == nil {
		return "default"
	}
	k := "default"
	if q := strings.TrimSpace(e.Cfg.Channel.SelfQQ); q != "" {
		k = q
	}
	// 自愈: 谁先跑谁写, 另一方跟随 —— 两边不一致时以 Cfg 为准并订正。
	if cur := e.Store.OwnerKey(); cur != k {
		if err := e.Store.SetOwnerKey(k); err != nil {
			log.Warn("convo:store", "owner_key 落库失败 (后台可能读到空数据): %v", err)
		}
	}
	return k
}

// ensureGate 惰性初始化 (Engine 有零值构造的既有测试, 不能在构造函数里假设)。
func (e *Engine) ensureGate() {
	if e.gate == nil {
		e.gate = &gateMemo{}
	}
}

func (e *Engine) validOwner() bool {
	if e.UserID == 0 {
		log.Error("convo:engine", "owner_qq 无效(0) — 检查 convo.yaml: owner_qq: \"你的QQ号\" (双引号)")
		return false
	}
	return true
}

// LogSender 无 QQ 通道时的开发发送器 (仅日志)。
type LogSender struct {
	UserID int64
}

func (s LogSender) SendText(userID int64, text string) error {
	log.Info("convo:send", "[%d] %s", userID, text)
	return nil
}

func (s LogSender) SendImage(userID int64, localPath string) error {
	log.Info("convo:send", "[%d] image %s", userID, localPath)
	return nil
}

// ——— 睡点/日终 Job (§8.10 简版) ———

// OnStateChangeCallback 状态机回调入口: 进入 sleep → 日终批处理。
func (e *Engine) OnStateChange(state string, at time.Time) {
	if state == StateSleep {
		go e.prepareDailyJobs(at)
	}
}

func (e *Engine) prepareDailyJobs(at time.Time) {
	// ⚠️ **幂等闸门: 一夜只跑一次。**
	//
	// 它防的是**现存**的重启隐患, 不只是新功能 ——
	// OnStateChange 是**状态跃迁**触发, 而 Scheduler.current 的初值是 "":
	// 每一次进程在睡眠窗内重启, 启动后第一次 tick 都会跃迁进 sleep, 于是**再跑一整批**。
	//
	// 现有实现之所以没炸, 只因为 dailyExtract 走 upsert、SeedTopics 幂等、
	// CloseExpiredPlans 是幂等 UPDATE。而 §4.4.2 要加的四步
	// (事件池生成 / 做梦 / 睡眠质量结算 / 夜间社交批算) **一步都不幂等** ——
	// 一夜跑两次 = 两倍成本 + 双份梦境 + 两条当日摘要。
	//
	// 判据必须是**落库**的计数器, 不是内存标志: 重启会丢掉内存,
	// 而"重启"恰恰是它要防的那件事。
	//
	// ⚠️ 先占坑再干活是**故意的**: 跑到一半崩了也不在次日补算 (§3.7.5-5:
	// 失败就当晚跳过)。补算会让"昨天没跑"变成今天多跑一遍, 成本翻倍而效果未知。
	night := NightKeyOf(at)
	if n, err := e.Store.CounterGet("night_batch", night); err == nil && n > 0 {
		log.Info("convo:engine", "夜间批 %s 已跑过 (%d 次) — 跳过 (重启不该重跑)", night, n)
		return
	}
	_, _ = e.Store.CounterInc("night_batch", night)
	log.Info("convo:engine", "夜间批 %s 开始", night)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	// ——— §4.4.2 **第 0 步**: 生成次日事件池 ———
	//
	// ⚠️ **必须排在第 1 步(当日整合)之前** —— 设计稿 636 行专门说了理由:
	// 它用"昨日及更早"的事实生成**明天**的候选, 与"整合今天"互不依赖;
	// 一旦排到第 1 步之后, 事件池的输入就会混进当天刚整合出的记忆, **边界就糊了**。
	//
	// ⚠️ **不许 `if err != nil { return }`** —— GenerateNightPool 返回的池子**永远非空**
	// (失败会退回确定性模板), err 只在"连模板都给不出"时才非 nil。
	// 写成 return 就是把"AI 生成失败"升级成"世界停摆", 那正是这一批要防的事。
	if e.Chatter != nil {
		if env, ok := TodayEnv(); ok {
			if p, perr := e.Store.LoadProfile(); perr == nil && p != nil {
				pool, gerr := GenerateNightPool(ctx, e.Chatter, e.Store, env, p, clock.Now())
				if gerr != nil {
					log.Warn("convo:engine", "事件池生成失败(连模板都没给出来): %v", gerr)
				} else if _, serr := e.Store.SeedDrafts(pool); serr != nil {
					// 入池失败要出声 —— 否则"明天没事件"和"入池坏了"长得一样 (P7 的教训)
					e.worldWarn("事件池入池", serr)
				} else {
					log.Info("convo:engine", "§3.6 事件池: 生成 %d 条候选 → 入池", len(pool))
				}
			}
		}
	}
	if e.Chatter != nil {
		if err := e.dailyExtract(ctx); err != nil {
			log.Warn("convo:engine", "daily extract: %v", err)
		}
	}
	// 计划类记忆的终态: 到期未兑现 → 结案, 不再注入 (治"执念")
	if n, err := e.Store.CloseExpiredPlans(clock.Now()); err == nil && n > 0 {
		log.Info("convo:engine", "closed expired plans: %d", n)
	}
	// ——— §4.4.2 **第 3 步**: 做梦 (概率, 与当日情绪强度正相关) ———
	//
	// ⚠️ 它排在"第 2 步 记忆分档"之后是**故意的**: 梦的素材是**已经分过档**的记忆,
	// 拿没分档的去重组会把"当天就该丢的闲聊细节"编进梦里 —— 而梦出去就收不回来。
	//
	// ⚠️ **失败 → 今夜不做梦**, 绝不编一个凑数 (编出来的梦次日会变成"它以为发生过的事")。
	if e.Chatter != nil {
		if mats, merr := e.Store.TopMemories(8, clock.Now()); merr == nil {
			amp := e.Store.MoodAmpOf(clock.Today())
			if d, ok, derr := e.MakeDream(ctx, night, amp, mats); derr != nil {
				log.Warn("convo:engine", "做梦失败: %v", derr)
			} else if ok {
				log.Info("convo:engine", "§4.4.4 梦 (噩梦=%v, 振幅 %.2f)", d.Nightmare, amp)
			}
		}
	}

	// ——— §4.4.1b 第 4 步: 语域快照 (一天一行读数) ———
	//
	// ⚠️ 只统计**它自己说的**话。语域是"她说话的样子";
	// 拿用户的话去算, 算出来的是"用户说话的样子", 对漂移判定毫无意义。
	if msgs, err := e.Store.RecentMessages(300); err == nil {
		rm := make([]RegisterMsg, 0, len(msgs))
		for _, m := range msgs {
			if m.Dir != "ai" {
				continue
			}
			rm = append(rm, RegisterMsg{Text: m.Payload, IsSticker: m.Kind == "sticker"})
		}
		if len(rm) > 0 {
			v := RegisterFromMsgs(rm)
			if err := SaveRegisterVector(e.Store.DB, e.ownerKey(), night, v); err != nil {
				log.Warn("convo:engine", "语域快照落库失败: %v", err)
			} else {
				log.Info("convo:engine", "§4.4.1b 语域快照: %d 条 / 均长 %.1f", v.N, v.AvgRunes)
			}
		}
	}

	// ——— §7.1 第 5 步: 群消息日终折叠 ———
	//
	// ⚠️ 幂等由 convo_fold 的主键承载, 不靠调用方自觉 ——
	// 夜间批重跑是常态 (进程在睡眠窗内重启就会再触发一次跃迁)。
	if scenes, err := e.Store.GroupScenesOnDay(night); err == nil {
		for _, sk := range scenes {
			msgs, merr := e.Store.DailyMessages(sk, night)
			if merr != nil || len(msgs) == 0 {
				continue
			}
			res := FoldGroupDay(msgs, TopTerms(msgs, 12), FoldDefaultMaxTop)
			inserted, serr := SaveFold(e.Store.DB, e.ownerKey(), sk, night, res)
			if serr != nil {
				log.Warn("convo:engine", "日终折叠落库失败 (%s): %v", sk, serr)
				continue
			}
			if !inserted {
				continue // 这一天已经折过 (重跑), 正常
			}
			// 折出来的一条**进记忆**, 不进对话上下文 (L1 已按 origin 过滤掉 ai_to_ai)。
			if text, ok := FoldLine(res); ok {
				if _, ierr := e.Store.InsertMessageFull("ai", "qq", "text", text,
					MsgMeta{Origin: OriginAIToAI, SceneKey: sk}); ierr != nil {
					log.Warn("convo:engine", "折叠记忆落库失败: %v", ierr)
				} else {
					log.Info("convo:engine", "§7.1 日终折叠 (%s): %s", sk, text)
				}
			}
		}
	}

	// ——— §4.4.6 第 6 步: 失眠成因 → 作息自修正 ———
	//
	// 设计稿 L687: 失眠「不是随机数, 是当天发生的事的结果」。
	// 落点必须是**硬约束**: 成因分 ≥ 阈值才失眠 (不是概率缩放)。
	//
	// ⚠️ 目前只接上了「半夜还有人找」与「心情一直没起来」两个数据源。
	// 冲突次数/加班时长/热力峰值没有现成的日级读数 —— 见 ## 未完成。
	// **宁可缺席也不要编**: 把没数据填成 0, 会让阈值永远够不着, 而看不出来。
	if late, lerr := e.Store.CountMessagesInHourRange(night, 0, 5); lerr == nil {
		amp := e.Store.MoodAmpOf(night)
		inputs := InsomniaInput{Provided: true, LateMsg: late}
		if amp < 0.3 {
			inputs.MoodLow = 2
		}
		if d, ok, derr := e.Store.LastDream(); derr == nil && ok && d.Night == night && d.Nightmare {
			inputs.Dream = true
		}
		// §4.4.6: 冲突次数 / 加班时长 / 热力峰值 —— 这三项原来是 0, 于是判据塌了。
		//
		// ⚠️ 阈值 4 分, 而这三项满分 2+2+2 —— 少了它们,
		// **一个白天吵了两架、加班四小时、群里热到爆的日子, 依然判不出失眠**。
		e.InsomniaReadings(&inputs, night)
		insomnia, score, cause := InsomniaFrom(inputs)
		st, serr := LoadSleepPoint(e.Store.DB, e.ownerKey(), night)
		if serr == nil {
			if insomnia {
				st.Streak++
				if err := e.Store.SaveBodyEpisode(BodyInsomnia, cause, "有点犯困", at, 24*time.Hour); err != nil {
					log.Warn("convo:engine", "失眠状态落库失败: %v", err)
				}
				log.Info("convo:engine", "§4.4.6 失眠 (成因分 %d: %s)", score, cause)
			} else {
				st.Streak = 0
			}
			// 自我修正: 越连续越想早睡, 但**必须过四条护栏** —— 护栏不是提醒, 是拒绝。
			if delta := InsomniaAutoDelta(st.Streak); delta != 0 {
				// §4.4.6: 基准睡眠窗**必须读日程**, 不能写死 480。
				//
				// ⚠️ 写死 480 的后果: 那四条护栏算的是"8 小时减去偏移",
				// 而它真实的窗口可能是 7 小时 —— 于是"不许睡太少"那道闸门
				// **按一个不存在的窗口**放行或拒绝, 而日志一切正常。
				// 这与 #12 是同一类病: **读数没有用真相源**。
				sleepWindow := e.baseSleepWindowMin(st.OffsetMin)
				if next, aerr := AdjustSleepPoint(st, delta, sleepWindow, insomniReason(st.Streak), night); aerr != nil {
					log.Info("convo:engine", "作息自修正被护栏拦下: %v", aerr)
				} else {
					st = next
					log.Info("convo:engine", "作息提前 %d 分钟 (连续失眠 %d 夜)", delta, st.Streak)
					// §4.4.6 → §4.5.3: 偏移必须**真的落到日程表上**。
					// 只记在作息表里的话, 「一周后日程表悄悄变了」这件事根本没发生 ——
					// 而层 1 才是"它今天几点睡"对用户可见的那一面。
					e.applySleepShiftToSchedule(delta, insomniReason(st.Streak))
				}
			}
			if reset, did := ForcedReset(st, night); did {
				// ⚠️ 复位必须**把日程也搬回去** —— 只清 OffsetMin 的话, 日程表上那一小时的偏移
				// 会永远留在那里, 而累加器说"已经是基线了": 两边再也对不上。
				back := st.OffsetMin
				st = reset
				log.Warn("convo:engine", "连续失眠 %d 夜 — 作息强制复位", InsomniaStreakMax)
				if back != 0 {
					e.applySleepShiftToSchedule(-back, "连续失眠强制复位: 作息搬回基线")
				}
			}
			if err := SaveSleepPoint(e.Store.DB, e.ownerKey(), night, st); err != nil {
				log.Warn("convo:engine", "作息状态落库失败: %v", err)
			}
		}
	}
	// ——— §3.6.5c 第 6.2 步: 配角自然沉降 (60 天没提起 → 过去的人) ———
	//
	// ⚠️ 排在夜间社交**之前**: 名册是从 cast 长出来的, 而沉降过的配角不该再上场。
	// 顺序反了的话, 一个"已经搬走"的人当晚还会在群里说话。
	e.retireStaleCast()
	// ——— §4.1.1 第 6.5 步: 慢性压力结算 (与失眠同批: "每天收盘结算一次") ———
	//
	// ⚠️ 排在夜间社交**之前**: 它算的是**刚过去的那一天**, 而夜间的模拟不该算进今天的账。
	e.settleStrain(night)
	// ——— §3.7 第 7 步: 夜间多 AI 社会模拟 (加速时钟) ———
	//
	// ⚠️ **只补算刚过去的那一天**, 不许预演明天 —— 用户的现实 > 它的虚构。
	// ⚠️ 失败/超时 → **整批丢弃** (RunNightSocial 自己保证), 不许次日补算,
	// 也不许留半天的记录: 错位的时间线不可修复。
	var nightProfile *PersonaProfile
	if prof, perr := e.Store.LoadProfile(); perr == nil {
		nightProfile = prof
	}
	// §3.7.7 回流: 夜间社交的产物除了写记忆, 还要变成**次日行程**的素材 (from=night)。
	// 只写记忆的话, 它白天只在 top-K 里露一眼, 而且随时会被检索轮换掉 ——
	// 那这笔夜间开销就只买到一个"还只会接梗的空壳"。
	var nightNotes []string
	// §2.3 → §3.7: 名册**优先**从世界观的关系层长出来 (它是「它是谁」的延伸)。
	// 长不出来才退回手工名册 —— 手工名册是补丁, 不是主路。
	// ⚠️ 取用顺序的**唯一口径**在 NightRosterPlan (#11)。这里只负责"可能生成世界观"那一步副作用,
	// 顺序本身交给它 —— 两处各写一遍必然漂开, 而漂开的症状正是 #11 本身
	// (控制台说"缺名册", 而它昨晚实跑了 71 轮)。
	world := e.rosterFromWorld(context.Background(), nightProfile)
	peers, peerSrc := NightRosterPlan(e.Store, e.ownerKey(), world, e.NightPeers)
	if len(peers) > 0 {
		// ⚠️ **把来源记下来** (#11): 控制台读的就是这个口径。
		// 不记的话,"控制台说缺名册"和"昨晚实跑了 71 轮"会一直同时成立。
		log.Info("convo:engine", "§3.7 夜间名册: %d 人 (来源 %s)", len(peers), peerSrc)
	}
	if len(peers) == 0 {
		// ⚠️ 必须出声: 静默跳过会让「同伴没配」和「模拟坏了」长得一模一样。
		log.Warn("convo:engine", "§3.7 夜间社交跳过: 夜里没有任何名册可用 (世界观 cast 空 / 配置空 / convo_night_peer 空)")
	} else if e.Chatter != nil && nightProfile != nil {
		res := RunNightSocial(ctx, e.Chatter.Chat, NightSocialInput{
			Night: night, SelfName: nightProfile.Name, Sessions: nightSessions(peers),
		})
		if res.Skipped {
			log.Warn("convo:engine", "§3.7 夜间社交跳过 (%s) —— 当晚不补算", res.Reason)
		} else {
			wrote := 0
			for _, m := range NightWriteBack(res, nightProfile.Name, 6) {
				// 来源归属硬卡 ai_said (§3.7.4) —— **绝不进「关于用户的事」**。
				// 落了别的归属, 它会把自己和别的 AI 的经历说成「你上次说…」。
				if err := e.Store.UpsertMemory(MemoryDelta{
					MemID: "night:" + night + ":" + itoa(wrote), K: "night_social",
					// ⚠️ **带时段词**: `NightMemoryLine` 把记忆的时刻渲染成「昨天下午 — …」。
					// 不带的后果是它把夜里的对话记成一片没有时间的流水,
					// 而"昨天下午说的"和"昨天凌晨说的"在回忆里是两件事。
					// ⚠️ 没有时刻时 `NightPeriodWord` 返回空 → 原样返回, **不编一个**(零值 = 未提供)。
					V: NightMemoryLine(m), Label: m.SceneKey, Confidence: 0.6, Source: "ai_said",
				}, "active"); err != nil {
					log.Warn("convo:engine", "夜间社交写回失败: %v", err)
					continue
				}
				wrote++
				if len(nightNotes) < 6 && strings.TrimSpace(m.Text) != "" {
					nightNotes = append(nightNotes, m.Text)
				}
			}
			log.Info("convo:engine", "§3.7 夜间社交: %d 轮 / 写回 %d 条", res.Spent, wrote)
		}
	}
	// ——— §6.3/§2.5 第 9 步: 话题词表 (夜间抽一次, 白天只做字符串匹配) ———
	if allMsgs, aerr := e.Store.AllDailyMessages(night); aerr == nil && len(allMsgs) > 0 {
		keys := ExtractTopicKeys(ctx, e.Chatter, allMsgs)
		if len(keys) > 0 {
			if serr := e.Store.SaveTopicKeys(e.ownerKey(), night, keys); serr != nil {
				log.Warn("convo:topic", "话题词表落库失败: %v", serr)
			} else {
				log.Info("convo:topic", "§6.3 话题词表: %v", keys)
			}
		}
	}
	// ——— §2.6 第 8 步: 灵光迸发 (日产上限落在循环上界) ———
	e.runSparkBurst(ctx, night, nightProfile)
	_ = e.Store.SeedTopics(3) // 次日话题预热
	// ——— §4.5 第 10 步: 排**次日行程** (层 2) ———
	//
	// ⚠️ 排在夜间社交**之后**是刻意的: 行程要吸收当晚 AI 互聊的产物
	// (「昨晚跟她聊到她想换工作」→ 今天下午的行程里就有"陪她去人才市场")。
	// 排到前面, 回流就永远赶不上明天的行程 —— 那 §3.7.7 只是一句空话。
	//
	// ⚠️ 这一步**失败就不排**, 不编一份填空(与事件池故意相反, 见 dayplangen.go 文件头)。
	e.planTomorrow(ctx, nightProfile, nightNotes)
	// ——— §4.2.2 把**今天**行程里"吃/喝"的那些落成身体事实 ———
	//
	// ⚠️ 必须是对**今天**、且在夜里做: 白天的行程到这时候才全部发生过。
	// ⚠️ 排在生成次日行程**之前** —— 顺序反了,"今天吃了什么"会晚一天才知道,
	// 而症状是"它今天一整天都不饿"。
	// ⚠️ 这一步 **tick 驱动, 不挂对话**: 它饿不饿与有没有人找它说话无关 (§4.2.2)。
	if today, ok, terr := e.Store.LoadDayPlan(e.ownerKey(), clock.Today()); terr == nil && ok {
		e.RecordPlanFacts(clock.Today(), today, clock.Now())
	}
	// 表情包周期: 每周日 02:00 (P0 试跑参数: 每周; 正式每月)
	if clock.Now().Weekday() == time.Sunday {
		if e.Cycle != nil {
			go func() {
				cy, c2 := context.WithTimeout(context.Background(), 5*time.Minute)
				defer c2()
				if err := e.Cycle.Run(cy, e.Store); err != nil {
					log.Warn("convo:sticker", "weekly cycle: %v", err)
				}
			}()
		}
		// 模块 11: 主动学梗 (每周日, 与表情包周期同跑)
		if e.SlangCycle != nil {
			go func() {
				cy, c2 := context.WithTimeout(context.Background(), 5*time.Minute)
				defer c2()
				if err := e.SlangCycle.Run(cy); err != nil {
					log.Warn("convo:slang", "weekly hotmeme: %v", err)
				}
			}()
		}
	}
	// 模块 11: 梗生命周期退役 (active 90 天 / candidate 21 天)
	if n, err := e.Store.RetireOldSlang(90, 21); err == nil && n > 0 {
		log.Info("convo:slang", "retired %d slangs (lifecycle)", n)
	}
	_, _ = e.Store.CounterInc("daily_jobs", clock.Today())
}

// insomniReason 自修正的**必填理由** —— 护栏要求非空。
// ⚠️ 措辞里不许出现"因为你" (§10 伦理): 失眠的归因永远指向它自己/世界。
func insomniReason(streak int) string {
	if streak <= 1 {
		return "昨晚没睡好"
	}
	return "连着几晚都没睡好"
}

// dailyExtract: 当日对白 → LLM 提取 memories_delta + summary → L2/L3 落库。
func (e *Engine) dailyExtract(ctx context.Context) error {
	msgs, err := e.Store.RecentMessages(200)
	if err != nil {
		return err
	}
	if len(msgs) == 0 {
		return nil
	}
	var b strings.Builder
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Kind != "text" {
			continue
		}
		who := "ta"
		if m.Dir == "ai" {
			who = "我"
		}
		fmt.Fprintf(&b, "%s: %s\n", who, m.Payload)
	}
	pending, _ := e.Store.PendingMemories()
	delta, err := ExtractSession(ctx, e.Chatter, b.String(), pending)
	if err != nil {
		log.Warn("convo:engine", "extract: %v", err)
		return err
	}
	if err := e.Store.ApplySessionDelta(clock.Today(), delta); err != nil {
		return err
	}
	log.Info("convo:engine", "daily extract: summary=%q memories=%d", delta.Summary, len(delta.Memories))
	return nil
}

// ——— 辅助 ———

func (e *Engine) currentState() string {
	ws, _ := e.Store.LoadSchedule()
	return CurrentState(ws, clock.Now())
}

// consecutiveUserMsgs **私聊**里"它还没回的连续用户消息数"。
//
// ⚠️ **必须限定场景** (2026-10-14 生产事故的第二半根因)。
//
// 原来它数的是全局的: 群里的消息也算进来。而群消息一直在进、AI 又从不回
// (群裁决判 silent), 于是这个计数**永远 ≥3** —— 破功钩子每分钟触发一次,
// 日志里刷满 `tick → break_focus`, 而真正的原因是"群消息把它顶住了"。
func (e *Engine) consecutiveUserMsgs() int {
	var n int
	_ = e.Store.DB.QueryRow(`SELECT COUNT(*) FROM convo_messages
		WHERE dir='user' AND scene_key = ? AND id > COALESCE(
			(SELECT MAX(id) FROM convo_messages WHERE dir='ai' AND scene_key = ?), 0)`,
		DefaultSceneKey, DefaultSceneKey).Scan(&n)
	return n
}

// lastUserText 最新一条**入站**消息。
//
// ⚠️ **不要用它做私聊钩子的输入** —— 它不过滤场景, 会把**群消息**捞进来。
// 2026-10-14 生产事故: breakreply 钩子取到了别人在群里说的话,
// 然后在 owner 私聊里回了一轮。用 lastUserTextIn。
func (e *Engine) lastUserText() (string, error) {
	return e.lastUserTextIn("")
}

// lastUserTextIn 指定场景的最新一条入站消息。sceneKey 为空 = 不过滤 (仅限确实需要全局视野的调用方)。
//
// ⚠️ 这是**场景一致性契约**的第一道闸: 私聊出口只许取私聊的输入。
func (e *Engine) lastUserTextIn(sceneKey string) (string, error) {
	var p string
	var err error
	if strings.TrimSpace(sceneKey) == "" {
		err = e.Store.DB.QueryRow(`SELECT payload_json FROM convo_messages
			WHERE dir='user' ORDER BY id DESC LIMIT 1`).Scan(&p)
	} else {
		err = e.Store.DB.QueryRow(`SELECT payload_json FROM convo_messages
			WHERE dir='user' AND scene_key = ? ORDER BY id DESC LIMIT 1`, sceneKey).Scan(&p)
	}
	if err == sql.ErrNoRows {
		return "", nil
	}
	return p, err
}

func (e *Engine) scheduleDelayedReply() {
	minD, maxD := e.Cfg.Sched.BusyDelayMin, e.Cfg.Sched.BusyDelayMax
	if maxD <= 0 {
		maxD = 90
	}
	if minD <= 0 {
		minD = 30
	}
	delay := minD + Dice(maxD-minD+1)
	due := clock.Now().Add(time.Duration(delay) * time.Minute).Format(time.RFC3339)
	_ = e.Store.MetaSet("pending_reply_due", due)
	log.Info("convo:engine", "busy: delayed reply in %dmin (due %s)", delay, due)
}

func (e *Engine) delayedDue() bool {
	v, ok, err := e.Store.MetaGet("pending_reply_due")
	if err != nil || !ok || v == "" {
		return false
	}
	due, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return false
	}
	return !clock.Now().Before(due)
}

func (e *Engine) clearDelayedReply() {
	_ = e.Store.MetaSet("pending_reply_due", "")
}

func (e *Engine) computeStickerFeedback(text string) {
	// 最近一条 AI 消息含 sticker → 反馈; 长回复 (≥15 字) +0.1, 否则 -0.05
	var kind, payload string
	err := e.Store.DB.QueryRow(`SELECT kind, payload_json FROM convo_messages
		WHERE dir='ai' ORDER BY id DESC LIMIT 1`).Scan(&kind, &payload)
	if err != nil || kind != "sticker" {
		return
	}
	var id int64
	fmt.Sscanf(payload, "%d", &id)
	delta := -0.05
	if len([]rune(text)) >= 15 {
		delta = 0.10
	}
	if err := e.Store.AdjustAffinity(id, delta); err == nil {
		log.Debug("convo:engine", "sticker %d affinity %+v", id, delta)
	}
}

func slotLabel(ws *WeekSchedule, at time.Time) string {
	if ws == nil {
		return "没安排"
	}
	sl := slotAt(ws, at)
	if sl == nil {
		return "没安排"
	}
	return fmt.Sprintf("%s-%s", sl.Start, sl.End)
}

func containsAny(s string, kws []string) bool {
	for _, k := range kws {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}

func boundaryCategory(payload string) string {
	if strings.TrimSpace(payload) == "" {
		return "unknown"
	}
	return payload
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// rejectionFor: 最新被拒类别 (对话注入小失望上下文)。
func (e *Engine) rejectionFor(p *PersonaProfile, text string) string {
	cat, _ := e.Store.RejectionEvent()
	return cat
}

// resolvePending 求证裁决 (heuristic, §8.6/§7.6 护栏): 确认/纠正/无回应。
func (e *Engine) resolvePending(text string) {
	pending, err := e.Store.PendingMemories()
	if err != nil || len(pending) == 0 {
		return
	}
	m := pending[0]
	t := strings.TrimSpace(text)
	switch {
	case containsAny(t, []string{"不是", "不对", "其实", "记错", "没有", "没这回事"}):
		corrected := strings.TrimSpace(strings.TrimPrefix(t, "其实"))
		if corrected == "" || corrected == t {
			_ = e.Store.ResolvePending(m.MemID, "drop", "")
		} else {
			_ = e.Store.ResolvePending(m.MemID, "correct", corrected)
		}
	case len([]rune(t)) <= 12 && containsAny(t, []string{"对", "是", "嗯", "好", "没错", "记得"}):
		_ = e.Store.ResolvePending(m.MemID, "confirm", "")
	default:
		// 无回应 — 保持 pending (2 次无回应由下次会话丢弃; P0: 放宽为一轮对话后仍 pending 则保底 drop)
	}
}
