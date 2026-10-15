package convo

import (
	"encoding/json"
	"testing"
)

func evWithSeg(t *testing.T, seg map[string]interface{}) OneBotEvent {
	t.Helper()
	raw, err := json.Marshal(map[string]interface{}{
		"post_type": "message", "message_type": "group",
		"message": []interface{}{seg},
	})
	if err != nil {
		t.Fatal(err)
	}
	var ev OneBotEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		t.Fatal(err)
	}
	return ev
}

// ⚠️ **本批的头号契约**: nimbus 给的是 `path` (导出后的绝对路径),
// 而原来只认 url/file —— file 是**裸文件名**, 拿它当路径必然找不到。
func TestImageFiles_PrefersPath(t *testing.T) {
	ev := evWithSeg(t, map[string]interface{}{
		"type": "image",
		"data": map[string]string{
			"file":        "64CC0D94ED0BE94FE1CEEC774FE1957C.png",
			"path":        "/home/tzh/.nimbus/media/64cc0d94ed0be94fe1ceec774fe1957c.png",
			"path_exists": "1",
		},
	})
	got := ev.ImageFiles()
	if len(got) != 1 {
		t.Fatalf("应当拿到 1 个: %v", got)
	}
	if got[0] != "/home/tzh/.nimbus/media/64cc0d94ed0be94fe1ceec774fe1957c.png" {
		t.Fatalf("没有优先取 path —— 拿到的是 %q, 那是个裸文件名", got[0])
	}
}

// ⚠️ 桥接方明说了"这个文件不在"时不许拿它去 open —— 白跑一趟还会把日志刷满。
func TestImageFiles_SkipsMissingPath(t *testing.T) {
	ev := evWithSeg(t, map[string]interface{}{
		"type": "image",
		"data": map[string]string{
			"file": "gone.png", "path": "/nope/gone.png", "path_exists": "0",
			"url": "https://example.com/x.png",
		},
	})
	got := ev.ImageFiles()
	if len(got) != 1 || got[0] != "https://example.com/x.png" {
		t.Fatalf("path_exists=0 时应当退回 url: %v", got)
	}
}

func TestImageFiles_FallsBackToFile(t *testing.T) {
	ev := evWithSeg(t, map[string]interface{}{
		"type": "image",
		"data": map[string]string{"file": "only.png"},
	})
	got := ev.ImageFiles()
	if len(got) != 1 || got[0] != "only.png" {
		t.Fatalf("啥都没有时应当退回 file: %v", got)
	}
}

func TestImageFiles_IgnoresNonImage(t *testing.T) {
	ev := evWithSeg(t, map[string]interface{}{
		"type": "text",
		"data": map[string]string{"text": "hello", "path": "/should/not/be/taken"},
	})
	if got := ev.ImageFiles(); len(got) != 0 {
		t.Fatalf("非图片段不该被当成图: %v", got)
	}
}
