package convo

import (
	"strings"
	"time"

	"github.com/tzhnahida/convo/internal/clock"
)

// ─────────────────────────────────────────────────────────────
// 打字节奏: 「说话按字长加延迟」
//
// 原来的延迟是 `5 + Dice(21)` 秒 —— **与字数无关**:
//   「嗯」等 5-25 秒, 一段 200 字的分析也等 5-25 秒。两头都不像人。
//
// ⚠️ 三段式, 缺一不可:
//   ① **基础反应** —— 看到消息到决定回。**不是 0**: 秒回不像人, 像机器人。
//   ② **打字时间** —— 按字数。中文真人 ~5 字/秒 (含想词)。
//   ③ **上限** —— 没有上限的话 500 字要等 90 秒, 那不是「真人节奏」, 是「卡死」。
//
// ⚠️ 它**乘现成的 factor 链** (心情/疲劳/睡眠质量/低意愿日): 累了打字更慢,
// 这与"打字速度"本是两回事, 但方向一致, 所以复用那条链而不是另造一条。
// ─────────────────────────────────────────────────────────────

const (
	// TypingBaseSeconds 基础反应: 看到 → 决定回。
	// ⚠️ 不许设成 0 —— 那会让「秒回」变成常态, 而秒回是客服最明显的特征。
	TypingBaseSeconds = 2.0
	// TypingPerCharSeconds 每字耗时 (含想词)。~5.5 字/秒。
	TypingPerCharSeconds = 0.18
	// TypingMaxSeconds **绝对**上限 (乘完 factor 之后再卡)。
	// 它是"人还可能在打字"这条线, 不是公式里的一个项。
	TypingMaxSeconds = 25.0
)

// TypingDelayFor 打完这段字要多久。**纯函数**, 便于测。
//
// text 为空 → 0 (没有要打的东西, 不该等)。
func TypingDelayFor(text string, factor float64, jitterPct int) time.Duration {
	n := len([]rune(strings.TrimSpace(text)))
	if n == 0 {
		return 0
	}
	if factor < 1 {
		// factor < 1 = 比平时**快** —— 那条链的方向是「≥1 表示更慢」,
		// 小于 1 的值只可能来自调用方算错, 不许让它把延迟压到 0。
		factor = 1
	}
	sec := TypingBaseSeconds + float64(n)*TypingPerCharSeconds
	sec *= 1 + float64(jitterPct)/100.0 // 抖动: 真人不会每次都一样
	sec *= factor
	if sec > TypingMaxSeconds {
		sec = TypingMaxSeconds // ⚠️ 上限**在最后**卡 —— 它是人理线, 不是公式项
	}
	return time.Duration(sec * float64(time.Second))
}

// pacingFactor 现成的节奏链 —— **只有这一份实现**。
//
// ⚠️ 两处各写一份的话, 改了心情忘了疲劳, 现象是"有时准有时不准", 极难查。
func (e *Engine) pacingFactor() float64 {
	now := clock.Now()
	// ⚠️ **身体效应 (§4.2) 必须在链里** —— 我第一版漏了它,
	// 于是群里的打字延迟不算生理周期, 而私聊那条路算。两处不一致, 最难查。
	f := MoodPacingFactor(e.moodWillingness(now)) *
		e.bodyEffect(now).DelayFactor *
		e.fatigueEffect(now).DelayFactor *
		e.sleepEffect().DelayFactor *
		DayMoodDelayFactor(DayMoodOf(clock.Today()))
	if f < 1 {
		f = 1
	}
	return f
}

// groupTypingDelay 群发一条文本前的打字延迟。
func (e *Engine) groupTypingDelay(text string) time.Duration {
	// 抖动取 -15% ~ +15%。
	j := Dice(31) - 15
	return TypingDelayFor(text, e.pacingFactor(), j)
}
