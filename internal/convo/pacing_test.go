package convo

import "testing"

// ——— §8.3 表达层: 低唤醒 → 延迟长 / 少表情 ———

// 🔴 反面(最重要): **0 = 未提供 → 不压制**。
// 少了这条, 任何忘了填意愿的调用方都会静默变成「事事都慢、处处少表情」。
// 与 MoodWillingness 的零值约定、moodWillingness() 的失败返回 1.0 是同一条原则:
// **失败往「正常」倒。**
func TestPacing_ZeroWillingnessMeansNoSuppression(t *testing.T) {
	if got := MoodPacingFactor(0); got != 1.0 {
		t.Fatalf("意愿未提供时节奏系数应为 1.0, 实得 %v", got)
	}
	if got := MoodStickerKeepProb(0); got != 1.0 {
		t.Fatalf("意愿未提供时表情保留率应为 1.0, 实得 %v", got)
	}
}

func TestPacing_HighWillingnessIsNeutral(t *testing.T) {
	if got := MoodPacingFactor(0.9); got != 1.0 {
		t.Fatalf("高意愿不该放慢, 实得 %v", got)
	}
	if got := MoodStickerKeepProb(0.9); got != 1.0 {
		t.Fatalf("高意愿不该丢表情, 实得 %v", got)
	}
}

func TestPacing_Monotonic(t *testing.T) {
	prev := 0.0
	for _, w := range []float64{0.25, 0.35, 0.45, 0.55} {
		f := MoodPacingFactor(w)
		if f < 1.0 {
			t.Fatalf("节奏系数不许小于 1 (否则成了「心情好反而慢」): %v → %v", w, f)
		}
		if prev != 0 && f > prev {
			t.Fatalf("意愿越低应越慢, 但 %v 比上一档快: %v > %v", w, f, prev)
		}
		prev = f
	}
	if !(MoodStickerKeepProb(0.3) < MoodStickerKeepProb(0.6)) {
		t.Fatal("意愿越低应越容易丢表情")
	}
}

// 🔴 反面: **少表情 ≠ 不发表情**。心情再差也得留得住一张。
func TestStickerKeepProb_NeverZero(t *testing.T) {
	worst := MoodStickerKeepProb(0.01)
	if worst <= 0 {
		t.Fatalf("最差意愿下保留率 = %v, 必须 > 0", worst)
	}
	if worst < 0.34 {
		t.Fatalf("保留率 %v 跌破下限 —— 那成了关机不是低落", worst)
	}
}

// scaleSeconds 不许把「慢一点」四舍五入成「立即」。
func TestScaleSeconds_FloorIsOne(t *testing.T) {
	if got := scaleSeconds(1, 0.1); got != 1 {
		t.Fatalf("四舍五入不该产生 0 秒, 实得 %d", got)
	}
	if got := scaleSeconds(10, 1.5); got != 15 {
		t.Fatalf("10s × 1.5 应为 15s, 实得 %d", got)
	}
}
