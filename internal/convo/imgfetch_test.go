package convo

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type fakeFetcher struct {
	calls   int
	lastMID int32
	ret     string
	err     error
}

func (f *fakeFetcher) FetchImage(messageID int32) (string, error) {
	f.calls++
	f.lastMID = messageID
	return f.ret, f.err
}

func evFrom(t *testing.T, raw string) OneBotEvent {
	t.Helper()
	var ev OneBotEvent
	if err := json.Unmarshal([]byte(raw), &ev); err != nil {
		t.Fatal(err)
	}
	return ev
}

const needFetchEv = `{"post_type":"message","message_type":"group","group_id":1,"message_id":4242,
  "message":[{"type":"image","data":{"file":"ABCD.jpg","md5":"aabbccdd",
  "path":"/home/u/.nimbus/media/aabbccdd.jpg","path_kind":"export","path_exists":"0","size":"123"}}]}`

const hasPathEv = `{"post_type":"message","message_type":"group","group_id":1,"message_id":99,
  "message":[{"type":"image","data":{"file":"ABCD.jpg","md5":"aabbccdd",
  "path":"/home/u/.nimbus/media/aabbccdd.jpg","path_exists":"1"}}]}`

// ⚠️ **本批的第一条口径** (桥接方 2026-10-14 定的): 判据是 `path_exists == "1"`。
//
// 原来写 `!= "0"` 是因为它曾经"存在才给"这个字段 ——
// 文件不在时它**索性不带**, 于是 `"" != "0"` 成立, 我们照样采用坏路径。
// 现在它**永远有值**, 不用再猜字段在不在。
func TestImageRefs_PathOnlyWhenExistsIsOne(t *testing.T) {
	// path_exists == "0" → **不许**采用那个 path (它还没被写出来)。
	ev := evFrom(t, needFetchEv)
	refs := ev.ImageRefs()
	if len(refs) != 1 {
		t.Fatalf("应当拿到 1 个图片段: %+v", refs)
	}
	if !refs[0].NeedsFetch {
		t.Fatal("path_exists=0 应当标成「需要下载」")
	}
	if refs[0].MessageID != 4242 {
		t.Fatalf("message_id 没带出来 (触发下载要用它): %d", refs[0].MessageID)
	}
	// ⚠️ path 不许进候选 —— 那是"将要写进去的地方", 不是已有的文件。
	for _, c := range refs[0].Candidates {
		if c == "/home/u/.nimbus/media/aabbccdd.jpg" {
			t.Fatalf("path_exists=0 却采用了的 path: %v", refs[0].Candidates)
		}
	}
	// path_exists == "1" → 采用。
	ev2 := evFrom(t, hasPathEv)
	r2 := ev2.ImageRefs()
	if len(r2) != 1 || r2[0].NeedsFetch {
		t.Fatalf("path_exists=1 不该标成需要下载: %+v", r2)
	}
	if r2[0].Path != "/home/u/.nimbus/media/aabbccdd.jpg" {
		t.Fatalf("path_exists=1 时该用 path: %q", r2[0].Path)
	}
}

// ⚠️ **本批的第二条口径**: `path_exists == "0"` 时调 get_image —— 那是**唯一的下载入口**。
//
// 桥接方不会主动下。不叫它, 那张图永远不存在, 而我们只会说"没加载出来"。
func TestIngestImage_TriggersFetchWhenNotDownloaded(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	f := &fakeFetcher{}
	e.ImageFetch = f
	e.VisionClient, e.VisionModel = nil, "" // 只验"有没有去叫它下"
	ev := evFrom(t, needFetchEv)
	refs := ev.ImageRefs()
	mark, look := e.IngestImage(context.Background(), refs[0])
	if f.calls != 1 {
		t.Fatalf("没去触发下载 —— 那张图永远不会存在 (calls=%d)", f.calls)
	}
	if f.lastMID != 4242 {
		t.Fatalf("触发下载时没带对 message_id: %d", f.lastMID)
	}
	if look != "" {
		t.Fatal("文件还没下到, 不该给路径")
	}
	if mark == "" {
		t.Fatal("必须给标记, 否则会留下空行")
	}
}

// ⚠️ 下载完了**再验一次** —— "它说下好了"和"文件真的在"是两件事。
func TestIngestImage_VerifiesAfterFetch(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	d := t.TempDir()
	real := filepath.Join(d, "ok.jpg")
	if err := os.WriteFile(real, []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}, 0o644); err != nil {
		t.Fatal(err)
	}
	e.ImageFetch = &fakeFetcher{ret: real}
	v := &fakeVision{out: `{"kind":"photo","meaning":"一张图"}`}
	e.VisionClient, e.VisionModel = v, "m"
	ev := evFrom(t, needFetchEv)
	_, look := e.IngestImage(context.Background(), ev.ImageRefs()[0])
	if look == "" {
		t.Fatal("下载回来的文件真的在, 却没给路径")
	}
}

// 下载失败 → 如实说没读到, 不许假装。
func TestIngestImage_FetchFailureSaysUnread(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	e.ImageFetch = &fakeFetcher{err: errors.New("桥挂了")}
	ev := evFrom(t, needFetchEv)
	mark, look := e.IngestImage(context.Background(), ev.ImageRefs()[0])
	if mark != ImageUnreadMark {
		t.Fatalf("下载失败该说没读到, got %q", mark)
	}
	if look != "" {
		t.Fatal("下载失败还给路径?")
	}
}

// 没有 ImageFetch (未接线) 时不许崩, 也不许假装有图。
func TestIngestImage_NoFetcherDegrades(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	e.ImageFetch = nil
	ev := evFrom(t, needFetchEv)
	mark, _ := e.IngestImage(context.Background(), ev.ImageRefs()[0])
	if mark != ImageUnreadMark {
		t.Fatalf("没接下载口时该如实说没读到, got %q", mark)
	}
}

// 响应形状不写死 —— 猜错的代价是"图永远取不到", 而现象是"图片功能整体不工作"。
func TestExtractMediaPath(t *testing.T) {
	cases := []struct{ in, want string }{
		// ⚠️ **实测的真实形状** —— 我第一版一个都不认, 于是每次都说"没回路径"。
		{`{"kind":"image","media":[{"localPath":"/qq/nt_data/Pic/x.jpg","fileName":"X.JPG","md5":"x","exists":true}]}`, "/qq/nt_data/Pic/x.jpg"},
		// exists=false = 这个元素没有实际文件, 不许当成下载结果。
		{`{"media":[{"localPath":"/qq/gone.jpg","exists":false},{"localPath":"/qq/real.jpg","exists":true}]}`, "/qq/real.jpg"},
		// 只有 fileName 时也认 (退一步总比"没回路径"强)。
		{`{"media":[{"fileName":"ONLY.JPG","exists":true}]}`, "ONLY.JPG"},
		{`{"file":"/a/b.jpg"}`, "/a/b.jpg"},
		{`{"path":"/c/d.png"}`, "/c/d.png"},
		{`{"url":"https://x/e.gif"}`, "https://x/e.gif"},
		{`"/plain/f.jpg"`, "/plain/f.jpg"},
		{`{}`, ""},
		{`null`, ""},
		{``, ""},
		{`{"file":""}`, ""},
	}
	for _, c := range cases {
		if got := extractMediaPath(json.RawMessage(c.in)); got != c.want {
			t.Fatalf("%s → %q, want %q", c.in, got, c.want)
		}
	}
}
