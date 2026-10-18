package convo

import "testing"

func TestEchoKeyNormalizes(t *testing.T) {
	a := EchoKey("我脑子也还没转过来。")
	b := EchoKey(" 我脑子也还没转过来！ ")
	c := EchoKey("[QQ表情38]我脑子也还没转过来")
	if a != b || a != c {
		t.Fatalf("归一化没把标点/空白/表情吃掉: %q / %q / %q", a, b, c)
	}
	if EchoKey("我 脑子") != EchoKey("我脑子") {
		t.Fatal("空白没有被吃掉")
	}
}

func TestIsSelfEcho(t *testing.T) {
	recent := map[string]bool{
		EchoKey("我脑子也还没转过来"): true,
		EchoKey("嗯"):         true, // 太短, 不参与
	}
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"整句相等", "我脑子也还没转过来", true},
		{"原句 + 补一句(实测那次)", "我脑子也还没转过来 咱俩半斤八两", true},
		{"改写过", "脑子还是懵的", false},
		{"短回应重复不算", "嗯", false},
		{"只是同词", "转过来一点了", false},
	}
	for _, c := range cases {
		if got := isSelfEcho(EchoKey(c.text), recent); got != c.want {
			t.Errorf("%s: isSelfEcho(%q)=%v, 期望 %v", c.name, c.text, got, c.want)
		}
	}
}

// ⚠️ 违规只在**整条都是复读**时给出 —— 部分复读走出口剔除, 不该把整条重生成一遍。
func TestSelfEchoViolation_OnlyWhenWholeReplyEchoes(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.InsertMessage("ai", "qq", "text", "我脑子也还没转过来"); err != nil {
		t.Fatal(err)
	}
	d := &DialogEngine{Store: s}

	whole := OutputFrame{Reply: []FrameItem{{Kind: "text", Text: "我脑子也还没转过来。"}}}
	if v := d.selfEchoViolation(whole); v == "" {
		t.Fatal("整条复读必须算违规 (要重生成一次)")
	}
	partial := OutputFrame{Reply: []FrameItem{
		{Kind: "text", Text: "我脑子也还没转过来"},
		{Kind: "text", Text: "不过今天确实有点钝"},
	}}
	if v := d.selfEchoViolation(partial); v != "" {
		t.Fatalf("部分复读不该触发整条重生成: %s", v)
	}
	fresh := OutputFrame{Reply: []FrameItem{{Kind: "text", Text: "脑子还在启动中"}}}
	if v := d.selfEchoViolation(fresh); v != "" {
		t.Fatalf("新话被误判成复读: %s", v)
	}
}

// 出口兜底: 复读的那行剔掉, 其余照发。
func TestDropSelfEcho_StripsOnlyTheEchoLine(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.InsertMessage("ai", "qq", "text", "我脑子也还没转过来"); err != nil {
		t.Fatal(err)
	}
	e := &Engine{Store: s}
	got := e.dropSelfEcho(OutputFrame{Reply: []FrameItem{
		{Kind: "text", Text: "我脑子也还没转过来"},
		{Kind: "text", Text: "不过今天确实有点钝"},
		{Kind: "sticker", StickerID: 7},
	}})
	if len(got.Reply) != 2 {
		t.Fatalf("应该剔掉 1 行、剩 2 行, 实际 %d", len(got.Reply))
	}
	if got.Reply[0].Text != "不过今天确实有点钝" {
		t.Fatalf("剔错了行: %+v", got.Reply)
	}
	if got.Reply[1].Kind != "sticker" {
		t.Fatal("表情包被误删了")
	}
}
