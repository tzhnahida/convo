package convo

import (
	"encoding/json"
	"strings"
	"testing"
)

// ⚠️ **判据** (用户定的口径): 重复度高的就是表情包, 一次性的才是照片。
// 数据坐实: 79b2858e….jpg 发了 3 次; 64cc0d94….png 只 1 次。
func TestImageMark_RepeatsBecomeSticker(t *testing.T) {
	if ImageIsSticker(1) || ImageIsSticker(2) {
		t.Fatal("只发过一两次的是照片, 不是表情包")
	}
	if !ImageIsSticker(ImageStickerRepeats) {
		t.Fatalf("发到 %d 次就该算表情包了", ImageStickerRepeats)
	}
	if ImageMark(1) != ImagePhotoMark || ImageMark(5) != ImageStickerMark {
		t.Fatal("回退标记不对 (没有标签时用它)")
	}
}

func TestNoteImage_CountsPerHash(t *testing.T) {
	s := newTestStore(t)
	owner := "u1"
	if n, _ := s.NoteImage(owner, "", "/x.jpg"); n != 0 {
		t.Fatalf("没哈希时应当返回 0 (当成照片), got %d", n)
	}
	if n, _ := s.NoteImage(owner, "AABB", "/x.jpg"); n != 1 {
		t.Fatalf("第一次应当是 1, got %d", n)
	}
	if n, _ := s.NoteImage(owner, "aabb", "/x.jpg"); n != 2 {
		t.Fatalf("大小写应当归一, got %d", n)
	}
	if n, _ := s.NoteImage(owner, "aabb", "/x.jpg"); n != 3 {
		t.Fatalf("第三次应当是 3, got %d", n)
	}
	if s.ImageSeen(owner, "AABB") != 3 {
		t.Fatal("ImageSeen 读回来的数不对")
	}
	if n, _ := s.NoteImage(owner, "ccdd", "/y.jpg"); n != 1 {
		t.Fatalf("另一张图不该被算进上一张: %d", n)
	}
}

// ⚠️ **标签是缓存, 不是省略** —— 同一张贴纸第二次出现时不必再看图,
// 但它**这次想说什么**由标签承载, 而不是被丢掉。
func TestImageTag_RoundTrip(t *testing.T) {
	s := newTestStore(t)
	owner := "u1"
	if _, tag := s.NoteImage(owner, "aa", "/a.gif"); tag != "" {
		t.Fatalf("第一次还没有标签, got %q", tag)
	}
	l := ImageLabel{Kind: "sticker", Act: "阴阳怪气", Emotion: "厌", Intensity: "轻",
		Text: "你说得对", Meaning: "嘴上同意心里不服"}
	if err := s.SetImageTag(owner, "aa", l.MarshalTag()); err != nil {
		t.Fatal(err)
	}
	if _, tag := s.NoteImage(owner, "aa", "/a.gif"); tag == "" {
		t.Fatal("第二次应当能拿到缓存的标签")
	}
	back, ok := ParseTag(l.MarshalTag())
	if !ok || back.Act != "阴阳怪气" || back.Text != "你说得对" {
		t.Fatalf("标签往返丢了信息: %+v", back)
	}
}

// ⚠️ **表情包是行为, 不是装饰** (屈济荣/李异平《作为"图像行为"的表情包》)。
// 所以 `[表情]` 这种类型标签是**错的** —— 群里发生的是「一个嘲讽」。
func TestImageLabel_RendersTheActNotTheType(t *testing.T) {
	l := ImageLabel{Kind: "sticker", Act: "嘲讽", Emotion: "厌", Intensity: "中",
		Text: "就这", Meaning: "看不起"}
	got := l.Render()
	if !strings.Contains(got, "嘲讽") {
		t.Fatalf("没渲染出**行为**: %q", got)
	}
	// 图文双构: 图上的字是**锚**, 必须带上。
	if !strings.Contains(got, "就这") {
		t.Fatalf("丢了图上的字 (字是解读方向的锚): %q", got)
	}
	if got == ImageStickerMark {
		t.Fatal("退化成了类型标签")
	}
	// 照片走另一套: 关心"是什么"而不是"在做什么"。
	p := ImageLabel{Kind: "photo", Meaning: "一只橘猫趴在键盘上"}
	if g := p.Render(); !strings.Contains(g, "照片") || !strings.Contains(g, "橘猫") {
		t.Fatalf("照片渲染不对: %q", g)
	}
}

// ⚠️ act 必须落在白名单里 —— 模型爱自由发挥, 而自由发挥的标签**聚合不起来**。
func TestParseImageLabel_ActMustBeInWhitelist(t *testing.T) {
	bad := `{"kind":"sticker","act":"莫名其妙的自创标签","emotion":"喜","meaning":"x"}`
	if _, ok := ParseImageLabel(bad); ok {
		t.Fatal("白名单外的 act 该被拒 —— 否则标签库会变成一锅乱炖")
	}
	good := `{"kind":"sticker","act":"嘲讽","emotion":"厌","intensity":"中","text":"就这","meaning":"看不起"}`
	l, ok := ParseImageLabel(good)
	if !ok {
		t.Fatal("合法标签被拒了")
	}
	if l.Act != "嘲讽" || l.Text != "就这" {
		t.Fatalf("解析不对: %+v", l)
	}
}

// ⚠️ 情绪填不准**不是致命伤** —— 补中性, 不整条丢。
// 一条"知道它在嘲讽但情绪没归类"的标签, 比什么都没有强得多。
func TestParseImageLabel_SoftFieldsDegrade(t *testing.T) {
	l, ok := ParseImageLabel(`{"kind":"sticker","act":"敷衍","emotion":"开心","intensity":"巨大","meaning":"懒得理"}`)
	if !ok {
		t.Fatal("软字段不合法不该整条丢")
	}
	if l.Emotion != "中性" || l.Intensity != "中" {
		t.Fatalf("软字段没降级: %+v", l)
	}
	if l.Act != "敷衍" {
		t.Fatal("硬字段 (act) 该保留")
	}
}

// kind 是硬字段: 认不出来就整条丢 —— "不知道这是照片还是表情"没法用。
func TestParseImageLabel_KindIsHard(t *testing.T) {
	for _, bad := range []string{`{"kind":"","act":"附和"}`, `{"kind":"weird","act":"附和"}`, `不是 json`} {
		if _, ok := ParseImageLabel(bad); ok {
			t.Fatalf("不该通过: %q", bad)
		}
	}
}

// ⚠️ ImageRefs 必须与 ImageFiles 取到**同一张** —— 分叉的话,
// 判重用一个文件、看图用另一个。
func TestImageRefs_MatchesImageFilesOrder(t *testing.T) {
	raw, _ := json.Marshal(map[string]interface{}{
		"post_type": "message",
		"message": []interface{}{map[string]interface{}{
			"type": "image",
			"data": map[string]string{
				"file": "64CC0D94.png", "md5": "64cc0d94",
				"path": "/home/tzh/.nimbus/media/64cc0d94.png", "path_exists": "1",
			},
		}},
	})
	var ev OneBotEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		t.Fatal(err)
	}
	files := ev.ImageFiles()
	refs := ev.ImageRefs()
	if len(files) != 1 || len(refs) != 1 || files[0] != refs[0].Path {
		t.Fatalf("两条路不一致: files=%v refs=%+v", files, refs)
	}
	if refs[0].MD5 != "64cc0d94" {
		t.Fatalf("md5 没带出来: %+v", refs[0])
	}
}

func TestImageRefs_SkipsNonImageOnly(t *testing.T) {
	raw, _ := json.Marshal(map[string]interface{}{
		"post_type": "message",
		"message": []interface{}{
			map[string]interface{}{"type": "text", "data": map[string]string{"text": "hi", "md5": "nope"}},
			map[string]interface{}{"type": "image", "data": map[string]string{"path": "/gone.png", "path_exists": "0", "md5": "gone"}},
		},
	})
	var ev OneBotEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		t.Fatal(err)
	}
	// ⚠️ 非图片段必须跳过; 但**"文件不存在"不是解析层的事** ——
	// 解析层只给候选, 存在性由 ImageRef.Resolve 判 (那是 IO, 要单独测)。
	refs := ev.ImageRefs()
	if len(refs) != 1 {
		t.Fatalf("只该剩那张 image 段: %+v", refs)
	}
	if refs[0].Resolve() != "" {
		t.Fatal("那是个不存在的路径, Resolve 应当返回空")
	}
}

// ⚠️ 标签进上下文, 而且 **kind=image/sticker 必须放行** —— 原来只放行 text/ask,
// 图片被整个滤掉, 于是它看不到"有人发了张图"。
func TestRecentDialog_LabelSurvives(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	sk := "group:9"
	l := ImageLabel{Kind: "sticker", Act: "阴阳怪气", Meaning: "嘴上同意心里不服"}
	if _, err := e.Store.InsertMessageFull("user", "qq", "sticker", l.Render(), MsgMeta{SceneKey: sk, SenderUIN: "9009"}); err != nil {
		t.Fatal(err)
	}
	got := e.recentDialogIn(sk, 10)
	if !strings.Contains(got, "阴阳怪气") {
		t.Fatalf("标签被从上下文里删掉了 —— 它读不到那个表情在做什么: %q", got)
	}
}
