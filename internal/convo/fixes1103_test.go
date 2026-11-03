package convo

import (
	"strings"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────
// 2026-11-03 用户实测 (2204 条真人消息 vs 它的 296 条):
// A 一条消息 = 一口气 · D 去省略号 · E 一轮至多一个问号。
// 三条都在**出口**做 (bubble.go), 不靠提示词自觉。
// ─────────────────────────────────────────────────────────────

// A: 换行/空行必须压平 —— 它曾用空行把两句话糊在一条里。
func TestNormalizeBubble_OneBreath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"烤肉拌饭 わかる〜\n\n19:11才到 你先忍一会儿", "烤肉拌饭 わかる〜 19:11才到 你先忍一会儿"},
		{"早\n起了没", "早 起了没"},
		{"  前面留白   中间  多空格  ", "前面留白 中间 多空格"},
	}
	for _, c := range cases {
		if got := NormalizeBubble(c.in, BubbleMaxQuestions); got != c.want {
			t.Errorf("NormalizeBubble(%q) = %q, 想要 %q", c.in, got, c.want)
		}
	}
	// ⚠️ 句号**不削**: 健康时 style.go 一个字符都不许改 (stylewire_test.go 钉着)。
	if got := NormalizeBubble("我在吃饭。", BubbleMaxQuestions); got != "我在吃饭。" {
		t.Errorf("句号被吃掉了: %q", got)
	}
}

// D: 首尾省略号是 AI 腔 (人 0.2% / 它 4.6%)。
func TestNormalizeBubble_EllipsisEdges(t *testing.T) {
	cases := map[string]string{
		"唔……":          "唔",
		"真的……这表情一模一样。": "真的……这表情一模一样。", // 中间的不动
		"……行吧":         "行吧",
	}
	for in, want := range cases {
		if got := NormalizeBubble(in, BubbleMaxQuestions); got != want {
			t.Errorf("NormalizeBubble(%q) = %q, 想要 %q", in, got, want)
		}
	}
}

// E: 一轮最多一个问号 —— 第二个问句整句丢, 不许把问号改成句号。
func TestNormalizeBubble_OneQuestion(t *testing.T) {
	got := NormalizeBubble("在吗？今天吃了啥？晚上有空没？", BubbleMaxQuestions)
	if strings.Count(got, "？") != 1 {
		t.Fatalf("问句没被砍到一个: %q", got)
	}
	if !strings.HasPrefix(got, "在吗？") {
		t.Fatalf("第一个问句应当保留: %q", got)
	}
	if strings.Contains(got, "晚上有空没") {
		t.Fatalf("后面的问句应当整句丢掉, 而不是抹掉问号: %q", got)
	}
	// 没有问句的文本原样。
	if got := NormalizeBubble("我刚回来。", BubbleMaxQuestions); got != "我刚回来。" {
		t.Fatalf("无问句被改动: %q", got)
	}
	// 整形后什么都不剩 → 调用方据此不发。
	if got := NormalizeBubble("……", BubbleMaxQuestions); got != "" {
		t.Fatalf("纯省略号应当归空: %q", got)
	}
}

// F: 旧 sticker 载荷是磁盘路径, 不能当"它说过的话"念出来。
func TestStickerHistoryLine(t *testing.T) {
	legacy := "486|data/convo/stickers/ja/1792505954267645894_232b9b63.gif"
	if got := StickerHistoryLine("sticker", legacy); got != "[表情]" {
		t.Errorf("旧载荷没归一: %q", got)
	}
	if got := StickerHistoryLine("sticker", "[表情]"); got != "[表情]" {
		t.Errorf("新载荷被改动: %q", got)
	}
	if got := StickerHistoryLine("sticker", "[图: 一个表情]"); got != "[图: 一个表情]" {
		t.Errorf("群里的图注被改动: %q", got)
	}
	// kind 不是 sticker 就一个字都不碰 (路径里的数字竖线在文本里可能是别的东西)。
	if got := StickerHistoryLine("text", legacy); got != legacy {
		t.Errorf("非 sticker 行被改动: %q", got)
	}
}

// ─────────────────────────────────────────────────────────────
// 2026-11-03 串人事故: 模型把同群另一个人的外号「老缇」安在了当轮说话人头上。
// ─────────────────────────────────────────────────────────────

func TestAddressViolations(t *testing.T) {
	others := []string{"老缇", " 老缇 ", "骚鸽", "阿"}
	if v := AddressViolations("这不就是你想参加的那个嘛 老缇", "", others); len(v) == 0 {
		t.Fatal("借了别人的称呼应当违规")
	}
	// 说话人自己有称呼 → 不管; 这时"老缇"可能真就是他。
	if v := AddressViolations("老缇你说呢", "老缇", others); len(v) != 0 {
		t.Fatalf("自己就有这个称呼却报违规: %v", v)
	}
	// 单字称呼不查 (会误伤正常词)。
	if v := AddressViolations("阿这个不错", "", others); len(v) != 0 {
		t.Fatalf("单字称呼不该查: %v", v)
	}
	if v := AddressViolations("我先去吃饭了", "", others); len(v) != 0 {
		t.Fatalf("没提到任何称呼却报违规: %v", v)
	}
}

// 降级帧也必须在出口过校验链 —— 原来 degradeFrame 的产物**原样上线**,
// 于是「人家还让你别外传」这种没根据的话一路绿灯进了群。
func TestDegradedFrameStillChecked(t *testing.T) {
	d := &DialogEngine{}
	frame := OutputFrame{Reply: []FrameItem{{Kind: "text", Text: "这不就是你想参加的那个嘛 老缇"}}}
	in := DialogInput{OtherAddresses: []string{"老缇"}}
	v := d.degradedViolations(frame, in, &PersonaProfile{})
	if len(v) == 0 {
		t.Fatal("降级帧借了别人的称呼, 却没被拦")
	}
	clean := OutputFrame{Reply: []FrameItem{{Kind: "text", Text: "我先去吃饭了"}}}
	if v := d.degradedViolations(clean, in, &PersonaProfile{}); len(v) != 0 {
		t.Fatalf("干净的降级帧被误杀: %v", v)
	}
}

// 身份锚: 没有记忆、没有称呼的人**也必须有那一段**, 且明说他没被起过称呼。
func TestPersonNoteIdentityAnchor(t *testing.T) {
	line := PersonNoteLine("", "", "2735604537", nil, nil)
	if strings.TrimSpace(line) == "" {
		t.Fatal("没有记忆的人整段消失 —— 这正是串人的温床")
	}
	if !strings.Contains(line, "这一轮说话的人") {
		t.Errorf("缺标题: %q", line)
	}
	if !strings.Contains(line, "没有**给他起过称呼") {
		t.Errorf("没明说\"没起过称呼\" —— 模型会去别处找一个: %q", line)
	}
	if !strings.Contains(line, "别拿别人的外号叫他") {
		t.Errorf("缺\"别拿别人的外号\"这条禁令: %q", line)
	}
	// 有称呼就正常的挑一句。
	withAddr := PersonNoteLine("海梦", "老缇", "351390754", nil, nil)
	if !strings.Contains(withAddr, "叫他「老缇」") {
		t.Errorf("有称呼却没渲染: %q", withAddr)
	}
	// 全空才返回空串。
	if got := PersonNoteLine("", "", "", nil, nil); got != "" {
		t.Errorf("全空应当返回空串: %q", got)
	}
}

// B: 日终回顾不许写成监控周报, 话题不许来自图注。
func TestFoldNoCameraLog(t *testing.T) {
	msgs := []FoldMsg{
		{SenderUIN: 1, Text: "[图: 一个表情]"},
		{SenderUIN: 2, Text: "[图: 一张图, 没细看]"},
		{SenderUIN: 3, Text: "耳机又复活了 换了个小尾巴"},
		{SenderUIN: 1, Text: "小尾巴是耳放那种吗"},
	}
	r := FoldGroupDay(msgs, []string{"表情", "照片", "耳机", "小尾巴"}, 3)
	for _, tp := range r.Topics {
		if tp == "表情" || tp == "照片" {
			t.Fatalf("图注词混进了话题: %v", r.Topics)
		}
	}
	// ⚠️ 不钉第一名: 命中数打平时按词序排, 「小尾巴」本来就在「耳机」前面。
	foundEarphone := false
	for _, tp := range r.Topics {
		if tp == "耳机" {
			foundEarphone = true
		}
	}
	if !foundEarphone {
		t.Fatalf("耳机没被选进话题: %v", r.Topics)
	}
	if len(r.Topics) == 0 {
		t.Fatal("一条话题都没有")
	}
	if !strings.Contains(r.Text, "有人说到「") {
		t.Errorf("回顾里没有一句具体的话: %q", r.Text)
	}
	if strings.Contains(r.Text, "条消息") || strings.Contains(r.Text, "个人") {
		t.Errorf("还是统计腔: %q", r.Text)
	}
	// 纯图的一天 = 没聊什么, 不是「主要在聊：表情」。
	blank := FoldGroupDay([]FoldMsg{{SenderUIN: 1, Text: "[图: 一个表情]"}}, []string{"表情"}, 3)
	if strings.Contains(blank.Text, "表情") {
		t.Errorf("纯图日子的回顾提到了表情: %q", blank.Text)
	}
}

// C: 问候按天轮换 + 预告窗口按天抖动。
func TestDayOpenerRotates(t *testing.T) {
	d1 := time.Date(2026, 11, 3, 7, 20, 0, 0, time.Local)
	d1b := time.Date(2026, 11, 3, 9, 50, 0, 0, time.Local)
	if dayOpener(d1) != dayOpener(d1b) {
		t.Fatal("同一天换了句子 —— 重生成会不一致")
	}
	seen := map[string]bool{}
	for i := 0; i < 8; i++ {
		day := d1.AddDate(0, 0, i)
		s := dayOpener(day)
		if !looksLikeGreeting(s) {
			t.Errorf("%s 的口头禅不是问候: %q", day.Format("01-02"), s)
		}
		seen[s] = true
	}
	if len(seen) < 2 {
		t.Fatalf("八天全是同一句 —— 就成了闹钟: %v", seen)
	}
}

func TestPreviewLeadVariesByDay(t *testing.T) {
	vals := map[int]bool{}
	for i := 0; i < 14; i++ {
		day := time.Date(2026, 11, 1, 0, 0, 0, 0, time.Local).AddDate(0, 0, i).Format("2006-01-02")
		lead := previewLeadMinutes(day, 450)
		if lead < 4 || lead > 30 {
			t.Fatalf("%s 的提前量越界: %d", day, lead)
		}
		vals[lead] = true
	}
	if len(vals) < 5 {
		t.Fatalf("两周里提前量几乎不变 (%d 种) —— 又会天天同一个点", len(vals))
	}
	// 窗口必须真的包住目标时刻: 提前量那一分钟要命中, 槽前 1 分钟不命中。
	day := time.Date(2026, 11, 3, 0, 0, 0, 0, time.Local)
	lead := previewLeadMinutes(day.Format("2006-01-02"), 450)
	hit := day.Add(7*time.Hour + time.Duration(30-lead)*time.Minute)
	if !inPreviewWindow(hit, 450) {
		t.Fatalf("提前量那一分钟没命中: %s", hit.Format("15:04"))
	}
	if inPreviewWindow(day.Add(7*time.Hour+29*time.Minute), 450) {
		t.Fatal("槽前 1 分钟还在窗口里 —— 那就还是同一个时刻")
	}
}
