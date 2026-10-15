package convo

import (
	"context"
	"strings"
	"time"

	"github.com/tzhnahida/VocabLock/internal/clock"
	"github.com/tzhnahida/VocabLock/internal/log"
)

// ─── §2.5 / §2.6 的引擎侧接线 ───────────────────────────────

// tasteNote 段 4b 的文本 (周档)。
func (e *Engine) tasteNote(p *PersonaProfile) string {
	if e.Store == nil || p == nil {
		return ""
	}
	mem, _ := e.Store.TopMemories(20, clock.Now())
	learned, _ := e.Store.RecentLearned(20)
	line, ok := TasteTrailLine(TasteInput{
		PersonaInterests: splitInterests(p.Attrs["interests"]),
		Memories:         mem,
		Learned:          learned,
		HasLearned:       len(learned) > 0,
	}, 4)
	if !ok {
		return ""
	}
	return line
}

// revealNote 段 7.4 (§2.5.3 不经意透露)。空串 = 这一轮不带出。
//
// ⚠️ 话题键是**两档**(见 topicOf): 夜间的**话题词表**优先, 命中的**梗**兜底。
//
// 原来这里写的是「当前取命中的梗」—— 那是**更早的形态**, 而一句描述错误形态的
// 注释比没有注释更坏: 读的人会照着它去别处找 bug。
//
// ⚠️ 真正的窄口是**词表是封闭的**: 它由夜间批从当天对话里抽(生产实测一天 5 个键),
// 加上 13 条黑话 —— 一句话不在这个闭集里就没有话题键, 于是这一轮不带出。
//
// 要拓宽只能每轮抽一次话题, 也就是**每条消息一次 LLM 调用**。
// 那不是"补个小功能", 是给热路径加一次网络往返 —— 所以它**是一个决定, 不是欠账**。
// 机制本身是真的: 冷却在骰子之前判, 带出了就记事实。
func (e *Engine) revealNote(text string) string {
	if e.Store == nil {
		return ""
	}
	topic := e.topicOf(text)
	if topic == "" {
		return ""
	}
	cands, err := e.Store.LearnedByTopic(topic, 5)
	if err != nil || len(cands) == 0 {
		return ""
	}
	dec, ok := DecideReveal(RevealInput{
		UserText: text, TopicKey: topic, Candidates: cands,
		Roll: Dice(100), HasRoll: true, Now: clock.Now(),
	})
	if !ok {
		return ""
	}
	line, ok := LearnedLine(dec)
	if !ok {
		return ""
	}
	if err := e.Store.MarkRevealed(dec.Item.ID, clock.Now()); err != nil {
		log.Warn("convo:learn", "透露计数失败: %v", err)
	}
	return line
}

// runSparkBurst §2.6.3 夜间迸发。
//
// ⚠️ 上限落在**循环上界** (SparkDailyMax), 不是概率 ——
// 「每天产生 ≤2-3 个」是配额, 配额不能用概率近似。
// ⚠️ 没有素材 → DecideBurst 恒 false, 一次 LLM 都不会发 (硬线①)。
func (e *Engine) runSparkBurst(ctx context.Context, night string, p *PersonaProfile) {
	if e.Chatter == nil || p == nil {
		return
	}
	mats, err := e.Store.TopMemories(8, clock.Now())
	if err != nil {
		return
	}
	// §2.6.3 迸发时机②: 当天被顶过的素材 (夜里消费, 不在热路径生成)。
	challenged := ""
	if chs, cerr := e.Store.LoadChallenges(e.ownerKey(), night, 3); cerr == nil && len(chs) > 0 {
		challenged = chs[0]
	}
	for i := 0; i < SparkDailyMax; i++ {
		dec, ok := DecideBurst(SparkBurstInput{
			DreamMaterial: mats, HasDream: len(mats) > 0,
			Challenged: challenged,
			Roll:       DayRoll("spark:"+night+":"+itoa(i), 100), HasRoll: true,
		}, SparkBurstConfig{})
		if !ok {
			continue
		}
		// §2.6.5: 把**被接住过的方向**喂回去 —— 这是"想更多"的唯一落点。
		// ⚠️ 每条都重新取一次: 第一条生成完之后, 库里的强化方向可能已经变了。
		out, cerr := e.Chatter.Chat(ctx, sparkSystemPrompt(p, dec, e.Store.ReinforcedTopics(3)), "开始。")
		if cerr != nil {
			log.Warn("convo:spark", "灵光生成失败 (这一条跳过, 不编一个凑数): %v", cerr)
			continue
		}
		spk, ok := ParseSpark(out, dec, clock.Now().Format(bodyTSLayout))
		if !ok {
			continue
		}
		if _, serr := e.Store.SaveSpark(spk); serr != nil {
			log.Warn("convo:spark", "灵光落库失败: %v", serr)
		} else {
			log.Info("convo:spark", "§2.6 灵光 [%s] %s", spk.Kind, spk.Content)
		}
	}
}

// nightSessions 从同伴名册建会话。
// 两个以上同伴 → 开一个公开层的群; 每个同伴另开一条成对私密。
func nightSessions(peers []NightPeer) []NightSession {
	if len(peers) == 0 {
		return nil
	}
	var out []NightSession
	if len(peers) >= 2 {
		out = append(out, NightSession{Kind: NightSessionGroup, Scene: "group:night", Peers: peers})
	}
	for _, pr := range peers {
		out = append(out, NightSession{Kind: NightSessionDM, Scene: "night:dm:" + itoa(int(pr.UIN)), Peers: []NightPeer{pr}})
	}
	return out
}

// maybeLearn 今天该学、现在能学、且今天还没学 → 起一个 Job。
//
// ⚠️ **先占坑再干活** (与夜间批的 night_batch 同一条):
// 跑到一半崩了也不在今天重试。重复学习会产出重复碎片, 而碎片会以
// 「它最近在看 X」的形态说出口 —— 说两遍同一个 X 就是播报。
func (e *Engine) maybeLearn(ctx context.Context) {
	if e.Store == nil || e.Chatter == nil {
		return
	}
	now := clock.Now()
	if !shouldLearnToday(now) || !e.learnEligible(now) {
		return
	}
	if n, err := e.Store.CounterGet("learn_runs", clock.Today()); err == nil && n > 0 {
		return
	}
	p, err := e.Store.LoadProfile()
	if err != nil || p == nil {
		return
	}
	if _, err := e.Store.CounterInc("learn_runs", clock.Today()); err != nil {
		return // 抢不到坑就不跑, 免得重复
	}
	go func() {
		c, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if err := e.runLearning(c, p); err != nil {
			log.Warn("convo:learn", "学习 Job: %v", err)
		}
	}()
}

// topicOf 这一句在聊什么。词表优先, 梗命中兜底。
func (e *Engine) topicOf(text string) string {
	if e.Store == nil {
		return ""
	}
	if keys, err := e.Store.RecentTopicKeys(e.ownerKey(), 7); err == nil {
		if k, ok := MatchTopicKey(text, keys); ok {
			return k
		}
	}
	if hits, err := e.Store.FindSlangHit(text); err == nil && len(hits) > 0 {
		return hits[0].Phrase
	}
	return ""
}

// noteChallenge §2.6.3 迸发时机②: 把「被顶」记成素材, 由夜间批消费。
//
// ⚠️ **它刻意不在这里生成灵光**: 那会让每一次用户反驳都多一次 LLM 调用,
// 而「重新组织立场」当轮的回复已经做完了。Spark 是事后的沉淀物。
func (e *Engine) noteChallenge(text, lastAIText string) {
	if e.Store == nil {
		return
	}
	q, ok := ChallengedOf(text, lastAIText)
	if !ok {
		return
	}
	if err := e.Store.SaveChallenge(e.ownerKey(), clock.Today(), q, clock.Now()); err != nil {
		log.Warn("convo:spark", "被顶素材落库失败: %v", err)
		return
	}
	log.Info("convo:spark", "§2.6.3 被顶 (记素材, 不在热路径生成灵光)")
}

// lastAIText 它最近说过的那几句 (被顶判据的对照面)。
func (e *Engine) lastAIText(n int) string {
	if e.Store == nil {
		return ""
	}
	msgs, err := e.Store.RecentMessages(n * 4)
	if err != nil {
		return ""
	}
	var b []string
	for i := len(msgs) - 1; i >= 0 && len(b) < n; i-- {
		if msgs[i].Dir == "ai" && msgs[i].Kind == "text" {
			b = append(b, msgs[i].Payload)
		}
	}
	return strings.Join(b, " ")
}
