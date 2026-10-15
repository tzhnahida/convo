package convo

import "sync"

// ——— §8.3 / §8.4 表达层: 「低唤醒」长什么样 ———
//
// 设计稿两处写了同一句话: **低唤醒表现为句子短 / 标点少 / 延迟长 / 少表情**。
//
// 这一层**故意落在系统侧, 不写进 prompt**:
//  ① 形式习惯交给模型会被它的随机性吃掉 —— 你在提示词里写"回慢一点", 它照样秒回;
//  ② 系统侧的东西**可测、可回归** —— 延迟秒数与表情张数都是硬数字, 能进验收。
//
// ⚠️ 本文件做**延迟**、**表情**, 以及**风格档位的计算**。
// §8.4 的文本改写本体在 style.go (纯函数, 带语料级不变式测试);
// 这里只负责"什么状态配什么档" —— 那是一条档位表, 不是文本处理。

// PacingStats 表达层的可观测读数 (与 FrameStats 同一模式: 包级状态 + 快照)。
type PacingStats struct {
	DelayCount      int // 打字延迟样本数
	DelaySeconds    int // 累计延迟秒数 (仿真里 Sleep 是空操作, 所以必须自己记)
	StickersDropped int // 因低意愿主动丢弃的表情
}

var (
	pacingMu    sync.Mutex
	pacingStats PacingStats
)

func ResetPacingStats() {
	pacingMu.Lock()
	pacingStats = PacingStats{}
	pacingMu.Unlock()
}

func PacingStatsSnapshot() PacingStats {
	pacingMu.Lock()
	defer pacingMu.Unlock()
	return pacingStats
}

// AvgDelaySeconds 平均打字延迟。
// ⚠️ 仿真把 clock.Sleep 变成空操作, 所以**这里记的是"本该等多久"** ——
// 它不是真实耗时, 而是拟人节奏的读数。别把它当成性能指标。
func (p PacingStats) AvgDelaySeconds() float64 {
	if p.DelayCount == 0 {
		return 0
	}
	return float64(p.DelaySeconds) / float64(p.DelayCount)
}

func notePacingDelay(sec int) {
	pacingMu.Lock()
	pacingStats.DelayCount++
	pacingStats.DelaySeconds += sec
	pacingMu.Unlock()
}

func noteStickerDropped() {
	pacingMu.Lock()
	pacingStats.StickersDropped++
	pacingMu.Unlock()
}

// MoodPacingFactor 低意愿时的节奏放慢系数 ∈ [1.0, 1.8]。
//
// ⚠️ **0 = 未提供 → 1.0 (不压制)**, 与 MoodWillingness 的零值约定保持一致:
// 少了这条, 任何忘了填意愿的调用方都会静默变成"事事都慢"。
// 与 MoodWillingnessFloor 同一条原则 —— **失败往"正常"倒。**
func MoodPacingFactor(willingness float64) float64 {
	if willingness <= 0 || willingness >= 0.6 {
		return 1.0
	}
	return 1.0 + (0.6-willingness)/0.6*0.8
}

// MoodStickerKeepProb 低意愿时"还发不发这张表情"的概率 ∈ [0.35, 1]。
//
// 下限 0.35 而不是 0: **少表情 ≠ 不发表情**。心情差到一张表情都不发,
// 那不是低落, 那是关机。
func MoodStickerKeepProb(willingness float64) float64 {
	if willingness <= 0 || willingness >= 0.7 {
		return 1.0
	}
	return 0.35 + 0.65*(willingness/0.7)
}

// ——— §8.4 风格档位: 什么状态配什么档 ———

// styleOptions 把当前状态折成风格档。**零值 StyleOptions = 原样返回** ——
// 与 MoodPacingFactor(0)=1.0 / BodyEffects 零值同一条约定: 失败往"正常说话"倒。
//
// 两条来源分开写, 因为它们**幅度不同**:
//   - §4.2 身体: 设计稿明说"消息长度 ×0.6" —— 它是这一档里唯一会**缩短**消息的;
//   - §8.3 低唤醒: 设计稿说的是"句子短 / 标点少", **没说变短** ——
//     所以心情那一档只收句子与标点, **不碰长度**。混在一起会让"心情差"变成"话变少",
//     而设计稿里的低落是"句子短", 不是"不说了"。
//
// ⚠️ FewPunctuation 只在**低唤醒**时开: 兴奋时说"真的！！！"是正常的,
// 把标点一律压平会把高兴也压成冷淡。
func styleOptions(be BodyEffects, willingness float64) StyleOptions {
	var opt StyleOptions
	if be.Active {
		opt.LengthFactor = be.LengthFactor // 1.0 = 不缩 (Effect 的零值就是 1.0)
		opt.ShortSentences = true
		opt.FewPunctuation = true
	}
	if willingness > 0 && willingness < 0.6 {
		opt.ShortSentences = true
		opt.FewPunctuation = true
	}
	return opt
}
