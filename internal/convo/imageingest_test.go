package convo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pngFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "x.png")
	// 最小合法 PNG 头 —— prepareImage/LabelImage 只把它当路径用。
	b := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0, 0, 0, 0}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// ⚠️ **本批的头号契约** (2026-10-14 用户截图):
//
//	群友: [图] @nahida 这是谁呀?
//	它:   ? 图里那个?  /  我这没看清 你截的谁啊
//
// 它知道有张图, 但**看不到内容** —— 群路径当初根本没接 vision。
// 这条测试钉的是: **照片必须带着路径出去**, 好让对话真的看一眼。
func TestIngestImage_PhotoIsShownToTheDialogue(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	v := &fakeVision{out: `{"kind":"photo","meaning":"一只橘猫趴在键盘上"}`}
	e.VisionClient, e.VisionModel = v, "m"
	mark, look := e.IngestImage(context.Background(), ImageRef{Path: pngFile(t), MD5: "aa11"})
	if !strings.Contains(mark, "照片") {
		t.Fatalf("标记不对: %q", mark)
	}
	if look == "" {
		t.Fatal("照片没给路径 —— 它会像截图里那样说「我这没看清」")
	}
	if v.called == 0 {
		t.Fatal("没调视觉")
	}
}

// ⚠️ 表情包**不给看图**: 标签已经承载了"这个表情在做什么",
// 再看一遍图只是多一次调用。
func TestIngestImage_StickerUsesLabelOnly(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	v := &fakeVision{out: `{"kind":"sticker","act":"嘲讽","emotion":"厌","meaning":"看不起"}`}
	e.VisionClient, e.VisionModel = v, "m"
	mark, look := e.IngestImage(context.Background(), ImageRef{Path: pngFile(t), MD5: "bb22"})
	if !strings.Contains(mark, "嘲讽") {
		t.Fatalf("没渲染出行为: %q", mark)
	}
	if look != "" {
		t.Fatalf("表情包不该再送一遍图: %q", look)
	}
}

// ⚠️ **标签是缓存**: 同一张图第二次不必再看。
func TestIngestImage_SecondSightUsesCache(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	v := &fakeVision{out: `{"kind":"photo","meaning":"一只猫"}`}
	e.VisionClient, e.VisionModel = v, "m"
	ref := ImageRef{Path: pngFile(t), MD5: "cc33"}
	if _, look := e.IngestImage(context.Background(), ref); look == "" {
		t.Fatal("第一次该给图")
	}
	first := v.called
	mark, _ := e.IngestImage(context.Background(), ref)
	if v.called != first {
		t.Fatalf("第二次又看了一遍图 (%d → %d) —— 缓存没生效", first, v.called)
	}
	// ⚠️ **第二次入库仍然是完整标记** (2026-10-14 改的口径)。
	//
	// 原来这里断言的是"第二次该是引用式" —— 那正是线上那个 bug:
	// 引用式被在**入库时**烧进 payload, 而它指向的完整内容可能已经
	// 滑出历史窗口, 于是「往上找」找到的是另一句「往上找」。
	//
	// 现在入库只负责**如实记下这张图是什么**; 要不要折叠成引用式,
	// 由拼历史那一刻按窗口决定 (CollapseRepeatedImageMarks)。
	if !strings.Contains(mark, "一只猫") {
		t.Fatalf("入库必须是自足的完整标记, 不是悬空指针: %q", mark)
	}
	if strings.Contains(mark, "往上找") {
		t.Fatalf("入库时不许先生成引用式 —— 那时还不知道窗口里有没有原件: %q", mark)
	}
}

// ⚠️ 折叠这件事**只有拼历史时**做得到, 所以这里正着测一遍:
// 同一份完整标记出现两次 → 第一次完整, 第二次指针。
func TestIngestImage_RepeatsCollapseInHistory(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	v := &fakeVision{out: `{"kind":"photo","meaning":"一只猫"}`}
	e.VisionClient, e.VisionModel = v, "m"
	ref := ImageRef{Path: pngFile(t), MD5: "ee55"}
	mark, _ := e.IngestImage(context.Background(), ref)
	lines := []string{"群友1: " + mark, "群友2: " + mark}
	got := CollapseRepeatedImageMarks(lines)
	if !strings.Contains(got[0], "一只猫") {
		t.Fatalf("窗口里第一次要完整: %q", got[0])
	}
	if strings.Contains(got[1], "一只猫") {
		t.Fatalf("窗口里第二次要折叠: %q", got[1])
	}
}

// ⚠️ 标注失败时**更要给图** —— 那时标签帮不上忙, 图是唯一的信息源。
func TestIngestImage_LabelFailureStillShowsTheImage(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	v := &fakeVision{err: errStr("视觉挂了")}
	e.VisionClient, e.VisionModel = v, "m"
	mark, look := e.IngestImage(context.Background(), ImageRef{Path: pngFile(t), MD5: "dd44"})
	if look == "" {
		t.Fatal("标注失败却连图都不给 —— 那它只能说「我没看清」")
	}
	if !strings.Contains(mark, "图") {
		t.Fatalf("兜底标记不对: %q", mark)
	}
}

// 没有视觉口时不许崩, 也不许返回空标记。
func TestIngestImage_NoVisionDegrades(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	e.VisionClient, e.VisionModel = nil, ""
	mark, _ := e.IngestImage(context.Background(), ImageRef{Path: pngFile(t), MD5: "ee55"})
	if mark == "" {
		t.Fatal("没有视觉时必须给兜底标记, 否则会留下空行")
	}
}

// ⚠️ 没 md5 也要能走 (保守: 当成照片, 给看)。
func TestIngestImage_NoHashStillWorks(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	v := &fakeVision{out: `{"kind":"photo","meaning":"风景"}`}
	e.VisionClient, e.VisionModel = v, "m"
	mark, look := e.IngestImage(context.Background(), ImageRef{Path: pngFile(t)})
	if mark == "" || look == "" {
		t.Fatalf("没哈希时该当成照片: mark=%q look=%q", mark, look)
	}
}

// ⚠️ **本批最重的一条**: 图片**没读到**时必须说"没读到", 不许留 [图片]。
//
// 实测 (用户截图): 桥接方给了 path 但文件没导出 → 我们看不到内容,
// 而 [图片] 让它以为收到了图, 于是编出「图太糊了 看半天」「我也认不出来」
// —— 那是一张 1357×1920 的**清晰高清插画**。
func TestIngestImage_MissingFileSaysSoInsteadOfPretending(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	v := &fakeVision{out: `{"kind":"photo","meaning":"不该被调用"}`}
	e.VisionClient, e.VisionModel = v, "m"
	mark, look := e.IngestImage(context.Background(), ImageRef{
		Path: "/nonexistent/nope.jpg", MD5: "ffff",
	})
	if mark != ImageUnreadMark {
		t.Fatalf("文件不在时该说「没加载出来」, got %q —— [图片] 会让它编出「图太糊了」", mark)
	}
	if mark == ImagePhotoMark {
		t.Fatal("不许退回 [图片]: 那等于告诉它「图我收到了」")
	}
	if look != "" {
		t.Fatal("文件都不在还给路径?")
	}
	if v.called != 0 {
		t.Fatal("文件不在还去调视觉 —— 白花一次调用")
	}
}

// ⚠️ 判据是 **Stat**, 不是桥接方给的 path_exists 字段。
// 实测: nimbus 的 path_exists 是"存在才给", 文件不在时**不带**这个字段,
// 所以 `path_exists != "0"` 在缺字段时会放行一个坏路径。
func TestLocalCandidates_TriesMd5Convention(t *testing.T) {
	d := t.TempDir()
	t.Setenv("NIMBUS_MEDIA_DIR", d)
	// 只按 md5 约定造一个文件 —— 桥接方给的 path 是错的。
	real := filepath.Join(d, "aabbccdd.jpg")
	if err := os.WriteFile(real, []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}, 0o644); err != nil {
		t.Fatal(err)
	}
	cands := localCandidates(map[string]string{
		"path": "/wrong/place/x.jpg",
		"md5":  "aabbccdd",
		"file": "AABBCCDD.jpg",
	})
	got := FirstExistingFile(cands)
	if got != real {
		t.Fatalf("没用 md5 约定找回那张图: got %q, want %q (候选 %v)", got, real, cands)
	}
}

// 都不在 → 空串 (调用方据此判断"没读到")。
func TestFirstExistingFile_NoneIsEmpty(t *testing.T) {
	if got := FirstExistingFile([]string{"/nope/a.jpg", "", "/nope/b.jpg"}); got != "" {
		t.Fatalf("都不该存在, got %q", got)
	}
	// 空文件也不算数 —— 半截下载的 0 字节目录同样读不出图。
	d := t.TempDir()
	empty := filepath.Join(d, "e.jpg")
	_ = os.WriteFile(empty, nil, 0o644)
	if got := FirstExistingFile([]string{empty}); got != "" {
		t.Fatalf("0 字节的文件不该算数: %q", got)
	}
}
