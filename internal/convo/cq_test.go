package convo

import (
	"encoding/json"
	"strings"
	"testing"
)

// ——— v2.4.2: CQ 字符串形态兼容 (Pi 实证根因: NapCat messagePostFormat 未生效) ———

func TestCqSegments_imageWithAmpersand(t *testing.T) {
	// Pi 证据 5: url 含 &amp; (fileid 与 rkey 之间)
	raw := `[CQ:image,file=abc.jpg,url=https://multimedia.nt.qq.com.cn/download?fileid=1&amp;rkey=2x,type=flash]`
	segs := cqSegments(raw)
	if len(segs) != 1 || segs[0].Type != "image" {
		t.Fatalf("want 1 image seg, got %+v", segs)
	}
	if got := segs[0].Data["url"]; got != "https://multimedia.nt.qq.com.cn/download?fileid=1&rkey=2x" {
		t.Fatalf("url 未反转义: %q", got)
	}
	if segs[0].Data["file"] != "abc.jpg" {
		t.Fatalf("file = %q", segs[0].Data["file"])
	}
}

func TestCqSegments_textAroundCodes(t *testing.T) {
	raw := "看看这个[CQ:image,file=x.png,url=http://a/b?c=1]怎么样[CQ:face,id=14]"
	segs := cqSegments(raw)
	if len(segs) != 4 {
		t.Fatalf("want 4 segs, got %+v", segs)
	}
	if segs[0].Type != "text" || segs[0].Data["text"] != "看看这个" {
		t.Fatalf("segs[0] = %+v", segs[0])
	}
	if segs[1].Type != "image" || segs[1].Data["file"] != "x.png" {
		t.Fatalf("segs[1] = %+v", segs[1])
	}
	if segs[2].Type != "text" || segs[2].Data["text"] != "怎么样" {
		t.Fatalf("segs[2] = %+v", segs[2])
	}
	if segs[3].Type != "face" || segs[3].Data["id"] != "14" {
		t.Fatalf("segs[3] = %+v", segs[3])
	}
}

func TestOneBotEvent_StringMessage_ImageFiles(t *testing.T) {
	// Pi 证据 1: 事件 message 是 CQ 字符串且带嵌套引号 (JSON 编码后为双反斜杠形态也兼容)
	raw := `[CQ:image,file=abc.jpg,url=https://example.com/download?fileid=1&amp;rkey=2]` // JSON string form
	b, _ := json.Marshal(raw)                                                             // 模拟 wire 上的 JSON 字符串值
	ev := OneBotEvent{Message: b}
	files := ev.ImageFiles()
	if len(files) != 1 {
		t.Fatalf("ImageFiles on CQ string = %v, want 1", files)
	}
	if files[0] != "https://example.com/download?fileid=1&rkey=2" {
		t.Fatalf("ImageFiles 应优先 url 且反转义, got %q", files[0])
	}
	// PlainText 不含 CQ 码残渣
	pt := ev.PlainText()
	if strings.Contains(pt, "[CQ:") {
		t.Fatalf("PlainText 泄漏 CQ 码: %q", pt)
	}
}

func TestOneBotEvent_ArrayMessage_StillWorks(t *testing.T) {
	raw := `[{"type":"text","data":{"text":"你好"}},{"type":"image","data":{"file":"/a/b.png"}}]`
	ev := OneBotEvent{Message: json.RawMessage(raw)}
	if got := ev.PlainText(); got != "你好" {
		t.Fatalf("PlainText = %q", got)
	}
	files := ev.ImageFiles()
	if len(files) != 1 || files[0] != "/a/b.png" {
		t.Fatalf("ImageFiles = %v", files)
	}
}

func TestCqSegments_noCodes_isText(t *testing.T) {
	segs := cqSegments("今天天气不错")
	if len(segs) != 1 || segs[0].Type != "text" || segs[0].Data["text"] != "今天天气不错" {
		t.Fatalf("got %+v", segs)
	}
}
