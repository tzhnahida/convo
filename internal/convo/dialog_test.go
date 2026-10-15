package convo

import (
	"context"
	"errors"
	"testing"
)

// 重试链: 第 1 次帧违规 (编造 mem) → 第 2 次修正通过 → 无降级
func TestDialogReply_retryOnce(t *testing.T) {
	dir := t.TempDir()
	store, _ := OpenStore(dir, "test.db")
	defer store.Close()
	d := &DialogEngine{Store: store, StickerExists: store.StickerExists}

	// 第 1 次输出: 引用不存在的 mem (违规); 第 2 次输出: 合法帧
	first := `{"reply":[{"kind":"text","text":"记得你喜欢猫"}],"mem_id_refs":["fake_mem"]}`
	second := `{"reply":[{"kind":"text","text":"嗯嗯, 我还记得你说过喜欢猫"}]}`
	f := &fakeChatter{outputs: []string{first, second}}

	p := &PersonaProfile{Name: "小夜", Description: "夜猫子朋友", Tolerance: 0.85}
	frame, err := d.Reply(context.Background(), f, DialogInput{UserText: "hi", State: StateFree, Memories: []Memory{{MemID: "real_mem", V: "喜欢猫"}}}, p, "ja")
	if err != nil {
		t.Fatal(err)
	}
	if len(frame.Reply) != 1 || frame.Reply[0].Text != "嗯嗯, 我还记得你说过喜欢猫" {
		t.Fatalf("frame = %+v", frame)
	}
	if f.i != 2 {
		t.Fatalf("expected 2 LLM calls, got %d", f.i)
	}
}

// 两次都违规 → 降级纯文本 (LLM 原始输出)
func TestDialogReply_ViolationsRefuseNotDegrade(t *testing.T) {
	dir := t.TempDir()
	store, _ := OpenStore(dir, "test.db")
	defer store.Close()
	d := &DialogEngine{Store: store, StickerExists: store.StickerExists}

	// 两次都不合法; 第 2 次**含可抢救文本** —— 降级应保留它说过的话,
	// 且**不再多打一次 LLM**(旧实现会打第 3 次)。
	bad1 := `{"reply":[{"kind":"sticker","sticker_id":999}],"mem_id_refs":["fake"]}`
	bad2 := `{"reply":[{"kind":"text","text":"就这样吧, 我随便说说"},{"kind":"visual","url":"http://x"}]}`
	plain := "就这样吧, 我随便说说"
	f := &fakeChatter{outputs: []string{bad1, bad2}}

	p := &PersonaProfile{Name: "小夜", Tolerance: 0.85}
	frame, err := d.Reply(context.Background(), f, DialogInput{UserText: "hi", State: StateFree}, p, "ja")
	if err != nil {
		t.Fatal(err)
	}
	// §8.2 **一般违规也拒** (2026-10-13 拍板): 改前这里是"降级放行, 复用原始输出",
	// 改后是"丢弃不发送"。契约变了, 这条测试跟着变 —— **不是测试坏了, 是行为改了**。
	if !frame.Refused {
		t.Fatalf("§8.2 违规即拒: 期望 Refused, 得到 %+v (Degraded=%v)", frame.Reply, frame.Degraded)
	}
	if frame.Degraded {
		t.Fatal("拒绝 ≠ 降级: 降级产物会被发出去, 拒绝不会")
	}
	if frame.RefuseReason == "" {
		t.Fatal("拒绝必须带原因 —— 否则回放时不知道它为什么不说")
	}
	if f.i != 2 {
		t.Fatalf("LLM 调用次数 = %d, 期望 2 (重试一次后仍违规才拒, 不该再多打)", f.i)
	}
	_ = plain // 保留原变量以免上面的构造漂移; 拒绝路径下它不该被用到
}

// TestDialogReply_FormatFailureStillDegrades 反面: **格式故障仍要降级**。
//
// ⚠️ 这条是 §8.2 改动的边界线。把"没解析出 JSON"也当成违规一起拒掉,
// 后果是它遇到一次模型抽风就整天不说话 —— 那是把"故障"当"说错话"处理。
// 纪律: 内容违规 → 拒; 格式故障 → 降级 (失败往"正常说话"倒)。
func TestDialogReply_FormatFailureStillDegrades(t *testing.T) {
	store, _ := OpenStore(t.TempDir(), "test.db")
	defer store.Close()
	d := &DialogEngine{Store: store, StickerExists: store.StickerExists}
	// 两次都完全不是 JSON —— **没有任何 violations 可言**, 只是没按帧格式说话。
	f := &fakeChatter{outputs: []string{"我就是随便说说", "还是不按格式来"}}
	p := &PersonaProfile{Name: "小夜", Tolerance: 0.85}
	frame, err := d.Reply(context.Background(), f, DialogInput{UserText: "hi", State: StateFree}, p, "ja")
	if err != nil {
		t.Fatalf("格式故障不该报错: %v", err)
	}
	if frame.Refused {
		t.Fatal("格式故障被当成内容违规拒掉了 —— 它会因为一次抽风就整天不说话")
	}
	if !frame.Degraded {
		t.Fatal("格式故障必须走降级 (带 Degraded 标记, 不进上下文)")
	}
	if len(frame.Reply) == 0 || frame.Reply[0].Text == "" {
		t.Fatal("降级必须产出一条能发出去的话")
	}
}

func TestDialogReply_llmError(t *testing.T) {
	dir := t.TempDir()
	store, _ := OpenStore(dir, "test.db")
	defer store.Close()
	d := &DialogEngine{Store: store, StickerExists: store.StickerExists}
	f := &fakeChatter{err: errors.New("boom")}
	_, err := d.Reply(context.Background(), f, DialogInput{UserText: "hi", State: StateFree},
		&PersonaProfile{Name: "小夜"}, "ja")
	if err == nil {
		t.Fatal("expected error on LLM failure")
	}
}

// 无文本可抢救 → 用**轮换的**兜底句 (固定一句会变成口头禅: 实测重复 33 次)
func TestDegradeFrame_rotatesFallback(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		f := degradeFrame(`{"reply":[{"kind":"visual","url":"http://x"}]}`)
		if !f.Degraded {
			t.Fatal("fallback frame must be marked Degraded")
		}
		seen[f.Reply[0].Text] = true
	}
	if len(seen) < 2 {
		t.Fatalf("兜底句未轮换, 只见到 %d 种", len(seen))
	}
	for txt := range seen {
		found := false
		for _, l := range fallbackLines {
			if l == txt {
				found = true
			}
		}
		if !found {
			t.Fatalf("兜底句不在池内: %q", txt)
		}
	}
}
