package convo

import (
	"fmt"
	"strings"
	"time"
)

// ─────────────────────────────────────────────────────────────
// §4.1.3 **上行: 今天话特别多** (2026-10-15)
//
// 全稿的机制几乎全是"往下收"的: 疲劳、失眠、身体不适、拒绝权、沉默配额、情绪幅度上限,
// 再加从"不许索要"到"不许复述"的一整套硬线。
//
// **但真人不只是克制的。真人也有上行。**
// 一个**永远在收着**的角色, 不是"有分寸的人", 是**抑郁的人**。
//
// ⚠️ 四条护栏, 一条都不能少:
//   ① 上行**同样有硬上限**(与下行共用 §6.7 热力) —— 不设上限的上行会变成永动机, 比克制更假;
//   ② **不许自报** —— 不说"我今天心情特别好", 而是从行为漏出来(主动起话题、回得更快、话更长);
//   ③ **不许拿上行索取**;
//   ④ **必须能被外部解释** —— 起因在世界事件 / 互动 / 关系里, **不许无缘无故**。
// ─────────────────────────────────────────────────────────────

const (
	// UpliftThreshold 心情高过多少才算"上行"。
	UpliftThreshold = 0.35
	// UpliftDelayFactor 延迟系数 (≤1 = 更快)。
	UpliftDelayFactor = 0.75
	// UpliftLengthFactor 长度系数 (≥1 = 更长)。
	UpliftLengthFactor = 1.25
	// UpliftQuotaBonus 主动配额最多加几条。
	//
	// ⚠️ **硬上限**。设计稿原话: 「上行**不是无限许可**」——
	// 无上限的上行会变成"永动机", 那比永远克制**更假**。
	UpliftQuotaBonus = 1
	// upliftCauseWindow 起因要有多新 —— 当天算数。
	upliftCauseWindow = 12 * time.Hour
)

// Uplift 上行档位。
type Uplift struct {
	Active bool
	// Delay / Length 直接乘进 pacingFactor 与长度控制。
	Delay  float64
	Length float64
	// Quota 主动配额的增量 (0..UpliftQuotaBonus)。
	Quota int
}

// UpliftOf 判这一轮是不是"状态不错"。
//
// ⚠️ **必须有外部起因** —— 这是四条护栏里最容易漏、也最能看出假的一条。
// 「不许无缘无故」与 §5.5 M7「不许随机掷骰子」同源:
// 一个没有来由就特别高兴的角色, 和一个没有来由就特别难过的角色一样不可信。
func UpliftOf(m MoodState, now time.Time) Uplift {
	if m.Valence < UpliftThreshold {
		return Uplift{}
	}
	if !hasFreshPositiveCause(m, now) {
		// 心情高但没有来由 → **不上行**。
		// (可能性很低: valence 本来就只能被事件推动。但"本该不可能"不是不设闸门的理由。)
		return Uplift{}
	}
	return Uplift{
		Active: true,
		Delay:  UpliftDelayFactor,
		Length: UpliftLengthFactor,
		Quota:  UpliftQuotaBonus,
	}
}

// hasFreshPositiveCause 当天有没有一条**正的**成因。
func hasFreshPositiveCause(m MoodState, now time.Time) bool {
	if m.BgValence > 0 {
		return true
	}
	for _, c := range m.Causes {
		if c.DeltaV <= 0 {
			continue
		}
		t, err := time.ParseInLocation(bodyTSLayout, c.TS, time.Local)
		if err != nil {
			// ⚠️ 时刻解析不出 → **不算起因**。
			// 折成"刚刚发生过"的话, 一条坏数据就能让它无缘无故地高兴起来。
			continue
		}
		if now.Sub(t) <= upliftCauseWindow {
			return true
		}
	}
	return false
}

// UpliftLine 上行那一句。不上行 → 空串。
//
// ⚠️ ②③ 两条护栏靠**不写什么**落: 不许自报状态、不许拿它索取。
// 而**"不许自报"最容易被写坏的方式, 就是在提示词里举一个"不要这样说"的例子** ——
// 反例在提示词里也是范例(§2.12③ 的教训)。所以这里只写规则, 不写例句。
func UpliftLine(u Uplift) string {
	if !u.Active {
		return ""
	}
	return "\n【今天】状态不错 —— 接话可以快一点, 话可以长一点, 也可以自己起个话头。\n" +
		"  ⚠️ **不许自报状态** —— 让它从行为里漏出来, 不要说出来。\n" +
		"  ⚠️ **不许拿它索取** —— 心情好不是要求陪伴的理由。\n"
}

// upliftPacing 上行对节奏的修正 (≤1 = 更快)。不上行 → 1.0。
func upliftPacing(u Uplift, base float64) float64 {
	if !u.Active || u.Delay <= 0 || u.Delay > 1 {
		return base
	}
	return base * u.Delay
}

var _ = fmt.Sprintf
var _ = strings.TrimSpace
