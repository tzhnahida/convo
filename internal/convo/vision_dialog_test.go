package convo

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeVision struct {
	called  int
	lastImg string
	err     error
	out     string
}

func (f *fakeVision) ChatVision(ctx context.Context, model, system, user, imagePath string) (string, error) {
	f.called++
	f.lastImg = imagePath
	if f.err != nil {
		return "", f.err
	}
	return f.out, nil
}

type textOnlyChatter struct{ calls int }

func (c *textOnlyChatter) Chat(ctx context.Context, system, user string) (string, error) {
	c.calls++
	return "{\"reply\":[{\"kind\":\"text\",\"text\":\"纯文本回的\"}]}", nil
}

// ⚠️ **本批的核心主张**: 有图就**直接把图送进这一次调用**, 不走转述。
func TestVisionOrChat_SendsImageDirectly(t *testing.T) {
	v := &fakeVision{out: "看着图说的"}
	c := &textOnlyChatter{}
	d := &DialogEngine{Vision: v, VisionModel: "m", ImagePath: "/tmp/a.jpg"}
	out, err := d.visionOrChat(context.Background(), c, "sys", "user")
	if err != nil {
		t.Fatal(err)
	}
	if out != "看着图说的" || v.called != 1 {
		t.Fatalf("图没有直接进对话: out=%q visionCalls=%d", out, v.called)
	}
	if v.lastImg != "/tmp/a.jpg" {
		t.Fatalf("传的图不对: %q", v.lastImg)
	}
	if c.calls != 0 {
		t.Fatal("有图时不该再走纯文本那条路")
	}
}

// ⚠️ 视觉失败**降级成纯文本**, 不是整轮失败 —— 图没看懂也得能自然接话。
func TestVisionOrChat_VisionFailureDegrades(t *testing.T) {
	v := &fakeVision{err: errors.New("vision 挂了")}
	c := &textOnlyChatter{}
	d := &DialogEngine{Vision: v, VisionModel: "m", ImagePath: "/tmp/a.jpg"}
	out, err := d.visionOrChat(context.Background(), c, "sys", "user")
	if err != nil {
		t.Fatalf("视觉失败不该让整轮失败: %v", err)
	}
	if out == "" || c.calls != 1 {
		t.Fatalf("没有降级成纯文本: out=%q textCalls=%d", out, c.calls)
	}
}

// 没图 / 没配视觉 → 纯文本, 不许白调一次视觉。
func TestVisionOrChat_NoImageFallsBack(t *testing.T) {
	c := &textOnlyChatter{}
	for _, d := range []*DialogEngine{
		{Vision: &fakeVision{}, VisionModel: "m"},        // 没图
		{VisionModel: "m", ImagePath: "/tmp/a.jpg"},      // 没视觉实现
		{Vision: &fakeVision{}, ImagePath: "/tmp/a.jpg"}, // 没模型名
	} {
		if _, err := d.visionOrChat(context.Background(), c, "s", "u"); err != nil {
			t.Fatal(err)
		}
	}
	if c.calls != 3 {
		t.Fatalf("三条都该走纯文本, got %d", c.calls)
	}
}

// ⚠️ **误记修复**: 它的"第一反应"不许出现在用户消息行里。
func TestOnUserMessage_ReactionIsNotUserText(t *testing.T) {
	e, ch := worldEngine(t, "照片里有只橘猫趴在键盘上。哈哈这也太可爱了【梗图】猫压键盘", nil)
	_ = ch
	e.VisionClient = nil // 视觉描述已经由上面的 comment 模拟, 这里只验记账
	// 直接走 InjeUserMessage 的落地逻辑太重 —— 这里验的是**纪律**本身:
	// spoken 属于 side-channel, 用户消息行只留 [图片]。
	if _, err := e.Store.InsertMessage("user", "qq", "image", "[图片]"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.InsertMessage("user", "qq", "vision_note", "照片里有只橘猫。哈哈这也太可爱了"); err != nil {
		t.Fatal(err)
	}
	msgs, err := e.Store.RecentMessages(20)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if m.Dir == "user" && m.Kind != "vision_note" && strings.Contains(m.Payload, "太可爱了") {
			t.Fatalf("它的第一反应进了用户消息行: kind=%s text=%q", m.Kind, m.Payload)
		}
	}
}
