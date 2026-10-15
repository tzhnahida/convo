package convo

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tzhnahida/VocabLock/internal/clock"
)

// ⚠️ **本批的头号契约** (2026-10-14 用户实测):
//
//	群友: [引用一张照片] @nahida 你在图片中看到了什么？
//	它:   [图: QQ表情265] 就一个表情包啊
//
// 桥接方**不输出 reply 段** (段类型统计 {text,at,face,image,file} 里没有它),
// 所以引用的图到不了 convo。近似补法: 去看**刚发的那张**。
func TestLookOrRecent_UsesRecentPhoto(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	d := t.TempDir()
	p := filepath.Join(d, "a.jpg")
	if err := os.WriteFile(p, []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.DB.Exec("INSERT INTO convo_image (owner, md5, path, seen_n, first_ts, last_ts, tag) VALUES (?,?,?,?,?,?,?)",
		e.ownerKey(), "aa", p, 1, clock.Now().Format(bodyTSLayout), clock.Now().Format(bodyTSLayout),
		ImageLabel{Kind: "photo", Meaning: "两个人"}.MarshalTag()); err != nil {
		t.Fatal(err)
	}
	got := e.lookOrRecent("", " 你在图片中看到了什么？")
	if got != p {
		t.Fatalf("提了图却没去找最近的图: %q", got)
	}
}

// ⚠️ **没提图就不许找** —— 否则一句"在吗"也会让它看到几张图前的旧图,
// 然后莫名其妙地开始描述。
func TestLookOrRecent_NoMentionNoLookup(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	d := t.TempDir()
	p := filepath.Join(d, "a.jpg")
	_ = os.WriteFile(p, []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}, 0o644)
	_, _ = e.Store.DB.Exec("INSERT INTO convo_image (owner, md5, path, seen_n, first_ts, last_ts, tag) VALUES (?,?,?,?,?,?,?)",
		e.ownerKey(), "bb", p, 1, clock.Now().Format(bodyTSLayout), clock.Now().Format(bodyTSLayout), "")
	for _, q := range []string{"在吗", "今天打游戏赢了", "?", "好家伙"} {
		if got := e.lookOrRecent("", q); got != "" {
			t.Fatalf("%q 没提图却去找了: %q", q, got)
		}
	}
}

// ⚠️ **表情包不给看** —— 用户问的通常是照片, 而且贴纸的标签就够了。
func TestLookOrRecent_SkipsStickers(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	d := t.TempDir()
	p := filepath.Join(d, "s.gif")
	_ = os.WriteFile(p, []byte("GIF89a"), 0o644)
	_, _ = e.Store.DB.Exec("INSERT INTO convo_image (owner, md5, path, seen_n, first_ts, last_ts, tag) VALUES (?,?,?,?,?,?,?)",
		e.ownerKey(), "cc", p, 5, clock.Now().Format(bodyTSLayout), clock.Now().Format(bodyTSLayout),
		ImageLabel{Kind: "sticker", Act: "嘲讽"}.MarshalTag())
	if got := e.lookOrRecent("", "看图"); got != "" {
		t.Fatalf("表情包不该被当成「用户问的那张图」: %q", got)
	}
}

// ⚠️ **也要看时间窗** —— 十分钟前那张不相干的图不该被翻出来。
func TestLookOrRecent_RespectsWindow(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	d := t.TempDir()
	p := filepath.Join(d, "old.jpg")
	_ = os.WriteFile(p, []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}, 0o644)
	old := clock.Now().Add(-30 * time.Minute).Format(bodyTSLayout)
	_, _ = e.Store.DB.Exec("INSERT INTO convo_image (owner, md5, path, seen_n, first_ts, last_ts, tag) VALUES (?,?,?,?,?,?,?)",
		e.ownerKey(), "dd", p, 1, old, old, ImageLabel{Kind: "photo", Meaning: "很久以前"}.MarshalTag())
	if got := e.lookOrRecent("", "看图"); got != "" {
		t.Fatalf("30 分钟前的图不该被翻出来: %q", got)
	}
}

// ⚠️ **文件不在也不许给** —— 库里有记录不等于文件还在
// (QQ 会清缓存, inbox 也会被 prune)。
func TestLookOrRecent_SkipsMissingFiles(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	_, _ = e.Store.DB.Exec("INSERT INTO convo_image (owner, md5, path, seen_n, first_ts, last_ts, tag) VALUES (?,?,?,?,?,?,?)",
		e.ownerKey(), "ee", "/nonexistent/x.jpg", 1, clock.Now().Format(bodyTSLayout), clock.Now().Format(bodyTSLayout),
		ImageLabel{Kind: "photo", Meaning: "早没了"}.MarshalTag())
	if got := e.lookOrRecent("", "看图"); got != "" {
		t.Fatalf("文件不在却给了路径: %q", got)
	}
}

// 有图时**直接用**, 不去找最近的 —— 当前这条消息的图永远优先。
func TestLookOrRecent_ExplicitWins(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	if got := e.lookOrRecent("/explicit/x.jpg", "看图"); got != "/explicit/x.jpg" {
		t.Fatalf("当前消息的图应当优先: %q", got)
	}
}

func TestMentionsImage(t *testing.T) {
	yes := []string{"看图", "这图是啥", "你在图片中看到了什么？", "描述一下这张照片", "表情包再来点"}
	no := []string{"", "在吗", "今天打游戏赢了", "?", "好家伙", "st是什么"}
	for _, s := range yes {
		if !MentionsImage(s) {
			t.Fatalf("%q 该被判为在说图", s)
		}
	}
	for _, s := range no {
		if MentionsImage(s) {
			t.Fatalf("%q 不该被判为在说图", s)
		}
	}
}
