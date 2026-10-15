package convo

import (
	"testing"
	"time"
)

// ——— §8.3 情绪污染: 心情进说话决策器 ———

func moodAt(v, a float64, now time.Time) MoodState {
	m := MoodState{Valence: v, Arousal: a}
	m.Updated = now.Format("2006-01-02 15:04:05")
	return m
}

// 意愿必须是**确定**的: 同一个心情值两次调用结果相同
// (设计约束「是自身状态导致的波动, 不是随机」就靠这条守住)。
func TestWillingness_DeterministicNotRandom(t *testing.T) {
	now := tueAt(14, 0)
	m := moodAt(0.1, 0.5, now)
	if m.Willingness(now) != m.Willingness(now) {
		t.Fatal("同一个心情值给出了不同的意愿 —— 那就成了随机")
	}
}

func TestWillingness_Monotonic(t *testing.T) {
	now := tueAt(14, 0)
	good := moodAt(0.5, 0.7, now).Willingness(now)
	bad := moodAt(-0.4, 0.7, now).Willingness(now)
	if !(good > bad) {
		t.Fatalf("好心情的意愿应高于坏心情: %v vs %v", good, bad)
	}
	tired := moodAt(0.1, 0.2, now).Willingness(now)
	fresh := moodAt(0.1, 0.8, now).Willingness(now)
	if !(fresh > tired) {
		t.Fatalf("高唤醒的意愿应高于低唤醒: %v vs %v", fresh, tired)
	}
}

// 🔴 反面: **心情不是开关** —— 再差也不许把意愿压到 0, 否则成了「心情差 = 不理人」。
func TestWillingness_NeverZero(t *testing.T) {
	now := tueAt(14, 0)
	worst := moodAt(-1.0, 0.0, now).Willingness(now)
	if worst <= 0 {
		t.Fatalf("最差心情的意愿 = %v, 必须 > 0", worst)
	}
	if worst < MoodWillingnessFloor {
		t.Fatalf("意愿 %v 跌破下限 %v", worst, MoodWillingnessFloor)
	}
}

// ——— 心情 → 主动配额 ———

func moodOpenCfg() SchedulerConfig {
	c := testCfg()
	c.MoodLowWillingness = 0.45
	return c
}

func openableTick() TickInput {
	in := base(tueAt(11, 50), StateFree)
	in.HasLastOpen = true
	in.MinutesSinceLastOpen = 300 // 冷却已过
	in.OpenJitterRoll = 0         // 抖动必过
	return in
}

// 正面: 意愿低于门槛时, 当日主动**少一次**。
func TestDecideTick_LowMoodCutsQuota(t *testing.T) {
	in := openableTick()
	in.ActiveToday = 1
	in.MoodWillingness = 0.9
	if got := DecideTick(in, moodOpenCfg()).Kind; got != ActActiveOpen {
		t.Fatalf("意愿正常时第 2 次应放行, 实得 %q", got)
	}
	in.MoodWillingness = 0.3
	if got := DecideTick(in, moodOpenCfg()).Kind; got == ActActiveOpen {
		t.Fatal("低意愿时不应再开第 2 次口")
	}
}

// 🔴 反面 1: **幅度上限** —— 低意愿只减 1, 不许把当天配额清成 0。
func TestDecideTick_LowMoodNeverZeroesQuota(t *testing.T) {
	in := openableTick()
	in.ActiveToday = 0
	in.MoodWillingness = 0.3 // 很低
	if got := DecideTick(in, moodOpenCfg()).Kind; got != ActActiveOpen {
		t.Fatalf("心情再差也该留 1 次开口, 实得 %q", got)
	}
}

// 🔴 反面 2: **未提供 ≠ 心情极差**。零值必须解释成「不压制」, 否则每个忘了填的调用方
// 都会静默变成低意愿 —— 一个安静的默认值 bug。
func TestDecideTick_MissingWillingnessDoesNotCutQuota(t *testing.T) {
	in := openableTick()
	in.ActiveToday = 1
	// MoodWillingness 故意不设 (=0)
	if got := DecideTick(in, moodOpenCfg()).Kind; got != ActActiveOpen {
		t.Fatalf("未提供意愿时不该削减配额, 实得 %q", got)
	}
}
