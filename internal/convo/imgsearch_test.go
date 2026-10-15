package convo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ⚠️ **本批的核心**: 不带 sources:["images"] 时搜索返回的是**网页地址**
// (pixiv 作品页/Pinterest 图钉/B站视频), 下回来就是 HTML。
// 实测那 4 个「内容是网页的 gif」就是这么来的。
func TestFirecrawlSearcher_AsksForImagesSource(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 2048)
		n, _ := r.Body.Read(b)
		gotBody = string(b[:n])
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"data": map[string]interface{}{"images": []map[string]interface{}{
				{"title": "二次元摇头GIF", "imageUrl": "https://s1.aigei.com/src/img/gif/07/x.gif?q=1", "imageWidth": 282, "imageHeight": 282},
			}},
		})
	}))
	defer srv.Close()
	f := &FirecrawlSearcher{Base: srv.URL, HTTP: &http.Client{Timeout: 5 * time.Second}}
	got, err := f.Search(context.Background(), []string{"二次元 表情包 gif"}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotBody, `"images"`) {
		t.Fatalf("没要 images 源 —— 拿回来的会是网页地址: %s", gotBody)
	}
	if len(got) != 1 || !strings.Contains(got[0].URL, "aigei.com") {
		t.Fatalf("没取 imageUrl 直链: %+v", got)
	}
	if got[0].From != "firecrawl-img" {
		t.Fatalf("来源标注丢了: %+v", got[0])
	}
}

// ⚠️ 图标/矢量图/横幅要早丢 —— 下完再丢是白花一次网络。
func TestIsJunkImageURL(t *testing.T) {
	cases := []struct {
		u    string
		w, h int
		junk bool
	}{
		{"https://x/logo.svg", 100, 100, true},
		{"https://x/icon.png", 32, 32, true},
		{"https://x/banner.jpg", 1200, 100, true},
		{"https://x/tall.jpg", 100, 1200, true},
		{"https://x/sticker.gif", 282, 282, false},
		{"https://x/photo.jpg", 1200, 800, false},
		{"https://x/unknown.gif", 0, 0, false}, // 尺寸未知 → 放行 (宁可多下一次)
	}
	for _, c := range cases {
		if got := IsJunkImageURL(c.u, c.w, c.h); got != c.junk {
			t.Fatalf("%s (%dx%d) → junk=%v, want %v", c.u, c.w, c.h, got, c.junk)
		}
	}
}

// images 一条都没用时才回退网页地址 —— 老路径不该因为这条改动整条断掉。
func TestFirecrawlSearcher_FallsBackToWeb(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"data": map[string]interface{}{
				"images": []map[string]interface{}{{"title": "logo", "imageUrl": "https://x/a.svg", "imageWidth": 16, "imageHeight": 16}},
				"web":    []map[string]interface{}{{"title": "某页", "url": "https://fallback/1"}},
			},
		})
	}))
	defer srv.Close()
	f := &FirecrawlSearcher{Base: srv.URL, HTTP: &http.Client{Timeout: 5 * time.Second}}
	got, err := f.Search(context.Background(), []string{"x"}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].URL != "https://fallback/1" {
		t.Fatalf("回退没生效: %+v", got)
	}
}

// ⚠️ 搜索词要**短**, 而且一条一次 —— 拼成一句等于要求页面同时含全部词。
func TestStickerKeywords_AreShortAndSeparate(t *testing.T) {
	ks := (&StickerCycle{}).keywords(nil)
	if len(ks) == 0 {
		t.Fatal("没有搜索词")
	}
	for _, k := range ks {
		if len([]rune(k)) > 24 {
			t.Fatalf("查询太长, 图片搜索命中率会崩: %q", k)
		}
		if strings.Contains(k, "  ") {
			t.Fatalf("查询里有连续空格: %q", k)
		}
	}
}
