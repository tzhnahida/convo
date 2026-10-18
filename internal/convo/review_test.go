package convo

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestParseVerdict(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		ok   bool
		why  string
		bad  bool
	}{
		{"同意", "{\"ok\":true}", true, "", false},
		{"打回带原因", "{\"ok\":false,\"reason\":\"踩了 4. 字面梗\"}", false, "踩了 4. 字面梗", false},
		{"pass 别名", "{\"pass\":true}", true, "", false},
		{"前后有话", "好的:\n{\"ok\":false,\"reason\":\"复读\"}\n以上", false, "复读", false},
		{"没有 JSON", "我觉得还行", false, "", true},
		{"没有 ok 字段", "{\"reason\":\"x\"}", false, "", true},
	}
	for _, c := range cases {
		v, err := ParseVerdict(c.raw)
		if c.bad {
			if err == nil {
				t.Errorf("%s: 应当报错, 得到 %+v", c.name, v)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if v.OK != c.ok || v.Reason != c.why {
			t.Errorf("%s: 得到 %+v, 期望 ok=%v reason=%q", c.name, v, c.ok, c.why)
		}
	}
}

// 审核机拿到的是**人设 + 规则**, 不是生成侧的风格指南。
func TestReviewSystemPrompt_CarriesPersonaAndRules(t *testing.T) {
	sys := ReviewSystemPrompt(&PersonaProfile{
		Name:        "喜多川海梦",
		Description: "东京的辣妹 coser",
		Personality: "直球、爱闹",
	}, "- 别刷屏\n- 别提现实里的品牌")
	for _, want := range []string{"喜多川海梦", "东京的辣妹", "直球、爱闹", "别刷屏", "别提现实里的品牌", "不可越过的红线", "\"ok\""} {
		if !strings.Contains(sys, want) {
			t.Fatalf("审核提示词里没有 %q:\n%s", want, sys)
		}
	}
	// 反面: 风格细节不该进审核口 —— 那会把"不像它"也判成不合格。
	if p := (&PersonaProfile{Name: "小夜", Catchphrase: "えへへ"}); strings.Contains(ReviewSystemPrompt(p, ""), "えへへ") {
		t.Fatal("口头禅进了审核口 —— 审核只该判越界, 不该判风格")
	}
}

// 审核机自己坏掉 → **放行** (fail-open), 不许把对话变成哑巴。
func TestReviewFrame_FailOpenAndDisabled(t *testing.T) {
	s := newTestStore(t)
	p := &PersonaProfile{Name: "小夜"}
	frame := OutputFrame{Reply: []FrameItem{{Kind: "text", Text: "嗯 我在"}}}

	// 关掉 = 不该打回 (也不会去调用审核机)。
	off := &Engine{Store: s, Cfg: Config{}, Reviewer: &fakeChatter{err: errors.New("boom")}}
	if v := off.ReviewFrame(context.Background(), DialogInput{}, p, frame); !v.OK {
		t.Fatal("审核关掉时不该打回")
	}

	// 开着但审核机报错 → 放行。
	bad := &Engine{Store: s, Cfg: Config{Review: ReviewConfig{Enabled: true, TimeoutSec: 5}},
		Reviewer: &fakeChatter{err: errors.New("网络抖动")}}
	if v := bad.ReviewFrame(context.Background(), DialogInput{}, p, frame); !v.OK {
		t.Fatal("审核机挂了必须放行 (fail-open)")
	}

	// 开着但输出看不懂 → 也放行。
	junk := &Engine{Store: s, Cfg: Config{Review: ReviewConfig{Enabled: true, TimeoutSec: 5}},
		Reviewer: &fakeChatter{outputs: []string{"我觉得还行"}}}
	if v := junk.ReviewFrame(context.Background(), DialogInput{}, p, frame); !v.OK {
		t.Fatal("审核机输出不合法时必须放行")
	}

	// 判了"不过" → 打回, 且原因非空。
	rej := &Engine{Store: s, Cfg: Config{Review: ReviewConfig{Enabled: true, TimeoutSec: 5}},
		Reviewer: &fakeChatter{outputs: []string{"{\"ok\":false,\"reason\":\"4. 字面梗\"}"}}}
	v := rej.ReviewFrame(context.Background(), DialogInput{}, p, frame)
	if v.OK || !strings.Contains(v.Reason, "字面梗") {
		t.Fatalf("应当打回并带原因, 得到 %+v", v)
	}
}

// 端到端: 打回 → 带原因重生成 → 通过就发。
func TestReview_RejectThenApproveRetries(t *testing.T) {
	store, err := OpenStore(t.TempDir(), "test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	d := &DialogEngine{Store: store, StickerExists: store.StickerExists}

	first := "{\"reply\":[{\"kind\":\"text\",\"text\":\"挑什么 我挑东西最在行了\"}]}"
	second := "{\"reply\":[{\"kind\":\"text\",\"text\":\"哼 我这叫劳逸结合\"}]}"
	f := &fakeChatter{outputs: []string{first, second}}

	reviews := 0
	d.Review = func(_ context.Context, _ DialogInput, _ *PersonaProfile, frame OutputFrame) Verdict {
		reviews++
		if strings.Contains(FrameText(frame), "挑东西") {
			return Verdict{OK: false, Reason: "4. 把别人的调侃降级成字面意思"}
		}
		return Verdict{OK: true}
	}
	p := &PersonaProfile{Name: "小夜", Tolerance: 0.85}
	frame, err := d.Reply(context.Background(), f,
		DialogInput{UserText: "你还挺会挑", State: StateFree}, p, "ja")
	if err != nil {
		t.Fatal(err)
	}
	if got := FrameText(frame); !strings.Contains(got, "劳逸结合") {
		t.Fatalf("打回后没有重生成: %q", got)
	}
	if reviews != 2 || f.i != 2 {
		t.Fatalf("应当审 2 次、生成 2 次, 实际 reviews=%d calls=%d", reviews, f.i)
	}
}

// 两次都被打回 → 不发 (宁可少说一句)。
func TestReview_AlwaysRejectDrops(t *testing.T) {
	store, err := OpenStore(t.TempDir(), "test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	d := &DialogEngine{Store: store, StickerExists: store.StickerExists}
	line := "{\"reply\":[{\"kind\":\"text\",\"text\":\"你为什么不理我\"}]}"
	f := &fakeChatter{outputs: []string{line, line}}
	d.Review = func(context.Context, DialogInput, *PersonaProfile, OutputFrame) Verdict {
		return Verdict{OK: false, Reason: "2. 制造内疚"}
	}
	p := &PersonaProfile{Name: "小夜", Tolerance: 0.85}
	frame, err := d.Reply(context.Background(), f,
		DialogInput{UserText: "在吗", State: StateFree}, p, "ja")
	if err != nil {
		t.Fatal(err)
	}
	if !frame.Refused {
		t.Fatalf("两次打回必须不发, 得到 %+v", frame)
	}
}
