package convo

import (
	"context"
	"strings"
	"testing"
	"time"
)

// ——— §8.4 接线测试 ———
//
// style.go 的 29 条单测证明了 ApplyStyle 改得对, 但证明不了**它被调用了** ——
// BodyLine 就是这么当了一整轮死代码的 (2 万行里全绿的死代码, 单测发现不了)。

type styleCapSender struct{ texts []string }

func (s *styleCapSender) SendText(_ int64, text string) error {
	s.texts = append(s.texts, text)
	return nil
}
func (s *styleCapSender) SendImage(_ int64, _ string) error { return nil }

func styleEngine(t *testing.T) (*Engine, *styleCapSender) {
	t.Helper()
	s := newTestStore(t)
	snd := &styleCapSender{}
	return &Engine{Store: s, Cfg: DefaultConfig(), Sender: snd, UserID: 1, Lang: "ja"}, snd
}

// 正面: 病中 → 发出去的文本**被收过** (更短, 且不是原文)。
// 改坏成: 删掉 sendFrame 里的 ApplyStyle 调用 → 红。
func TestSendFrame_AppliesStyleWhenSick(t *testing.T) {
	e, snd := styleEngine(t)
	now := time.Now()
	if err := e.Store.SaveBodyEpisode(BodyCold, "昨天淋了雨", "嗓子有点哑", now, 72*time.Hour); err != nil {
		t.Fatal(err)
	}
	long := "我今天真的不想出门。因为外面在下雨，而且我有点累。改天再说吧。"
	if err := e.sendFrame(context.Background(), FrameForText(long)); err != nil {
		t.Fatalf("sendFrame: %v", err)
	}
	if len(snd.texts) != 1 {
		t.Fatalf("应当发出 1 条, 实际 %d", len(snd.texts))
	}
	got := snd.texts[0]
	if got == long {
		t.Fatal("病中却原样发出 —— ApplyStyle 没接线 (§8.4 是 100% 死代码)")
	}
	if len([]rune(got)) >= len([]rune(long)) {
		t.Fatalf("后处理没让消息变短: %d -> %d", len([]rune(long)), len([]rune(got)))
	}
	// 语料级不变式: 输出必须是输入的**子序列**(style.go 的契约, 这里再钉一次防止接线处搞乱)。
	if !styleIsSubsequence([]rune(long), []rune(got)) {
		t.Fatalf("后处理改写了语义 (输出不是输入的子序列):\n原文 %q\n发出 %q", long, got)
	}
	// 反面: 不许被处理成空串。
	if strings.TrimSpace(got) == "" {
		t.Fatal("后处理把消息变成了空串")
	}
}

// 反面: **没病且心情好 → 逐字节原样发出**。
// 改坏成: 无条件 ApplyStyle (例如忘了看 be.Active) → 红。
// 零值必须什么都不做 —— 任何调用方忘了填参数, 都不该被静默改掉措辞。
func TestSendFrame_NoStyleWhenHealthyAndCalm(t *testing.T) {
	e, snd := styleEngine(t)
	if err := e.Store.SaveMoodState(MoodState{Valence: 1.0, Arousal: 1.0, Updated: time.Now().Format(bodyTSLayout)}); err != nil {
		t.Fatal(err)
	}
	text := "我今天真的不想出门。因为外面在下雨，而且我有点累。"
	if err := e.sendFrame(context.Background(), FrameForText(text)); err != nil {
		t.Fatalf("sendFrame: %v", err)
	}
	if len(snd.texts) != 1 {
		t.Fatalf("应当发出 1 条, 实际 %d", len(snd.texts))
	}
	if snd.texts[0] != text {
		t.Fatalf("没病+心情好却改了措辞:\n原文 %q\n发出 %q", text, snd.texts[0])
	}
}

// —— 档位表本身 ——

// 反面: **心情差不许碰长度**。设计稿说低唤醒是"句子短 / 标点少", 没说"话变少"。
// 改坏成: 在 willingness 分支里也设 LengthFactor → 红。
func TestStyleOptions_MoodDoesNotShorten(t *testing.T) {
	opt := styleOptions(BodyEffects{DelayFactor: 1, LengthFactor: 1}, 0.3)
	if opt.LengthFactor != 0 {
		t.Fatalf("心情差设了 LengthFactor=%v —— 那会把「低落」变成「话变少」", opt.LengthFactor)
	}
	if !opt.ShortSentences || !opt.FewPunctuation {
		t.Fatalf("低唤醒应当收句子与标点: %+v", opt)
	}
}

// 反面: **心情好 + 身体好 = 零值** (什么都不做)。
func TestStyleOptions_CalmIsZero(t *testing.T) {
	if opt := styleOptions(BodyEffects{DelayFactor: 1, LengthFactor: 1}, 1.0); opt != (StyleOptions{}) {
		t.Fatalf("平静时应当是零值, 得到 %+v", opt)
	}
	// 零值 = 未提供 → 也必须什么都不做 (与 moodWillingness 读不到返回 1.0 同规)。
	if opt := styleOptions(BodyEffects{DelayFactor: 1, LengthFactor: 1}, 0); opt != (StyleOptions{}) {
		t.Fatalf("willingness=0 (未提供) 应当是零值, 得到 %+v", opt)
	}
}

// styleIsSubsequence 判断 sub 是否是 full 的子序列 (按 rune)。
func styleIsSubsequence(full, sub []rune) bool {
	i := 0
	for _, r := range full {
		if i < len(sub) && sub[i] == r {
			i++
		}
	}
	return i == len(sub)
}
