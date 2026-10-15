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

// ⚠️ **本批的头号契约**: Firecrawl v2 的 /search **只接受 POST**。
//
// 拿 GET 打过去是 405, 而那个错误长得像"没配 key" ——
// 于是整条搜索管线静默死了很久: 自主学习学不到、对话搜索永远反问、表情包 searched=0。
func TestFirecrawlLearnSearch_UsesPost(t *testing.T) {
	var gotMethod, gotBody, gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotCT = r.Header.Get("Content-Type")
		b := make([]byte, 512)
		n, _ := r.Body.Read(b)
		gotBody = string(b[:n])
		// 新版响应: data 是 {"web":[...]}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"data": map[string]interface{}{"web": []map[string]string{
				{"title": "某地地震", "url": "https://news/1", "description": "震级 6.2"},
			}},
		})
	}))
	defer srv.Close()

	f := &FirecrawlLearnSearch{Base: srv.URL, HTTP: &http.Client{Timeout: 5 * time.Second}}
	hits, err := f.SearchWeb(context.Background(), "今日热点", 5)
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("必须 POST, 实际 %s —— GET 会拿到 405", gotMethod)
	}
	if !strings.Contains(gotCT, "application/json") {
		t.Fatalf("POST 必须带 JSON Content-Type, 实际 %q", gotCT)
	}
	if !strings.Contains(gotBody, "今日热点") {
		t.Fatalf("query 没进 body: %q", gotBody)
	}
	if len(hits) != 1 || hits[0].Title != "某地地震" {
		t.Fatalf("新版 data.web 没解析出来: %+v", hits)
	}
}

// ⚠️ 服务端回滚时不该让整条管线再死一次 —— 旧版的 `data` 是数组。
func TestFirecrawlLearnSearch_AcceptsLegacyArrayShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"data": []map[string]string{
				{"title": "旧格式", "url": "https://old/1", "description": "d"},
			},
		})
	}))
	defer srv.Close()
	f := &FirecrawlLearnSearch{Base: srv.URL, HTTP: &http.Client{Timeout: 5 * time.Second}}
	hits, err := f.SearchWeb(context.Background(), "x y", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Title != "旧格式" {
		t.Fatalf("旧版数组格式没兼容: %+v", hits)
	}
}

// 表情包那条路同样的 bug —— 它是 searched=0 的直接来源。
func TestFirecrawlSearcher_UsesPost(t *testing.T) {
	var gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"data":    map[string]interface{}{"web": []map[string]string{{"title": "猫", "url": "https://i/1.gif"}}},
		})
	}))
	defer srv.Close()
	f := &FirecrawlSearcher{Base: srv.URL, HTTP: &http.Client{Timeout: 5 * time.Second}}
	got, err := f.Search(context.Background(), []string{"猫"}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("表情包搜索也必须 POST, 实际 %s", gotMethod)
	}
	if len(got) != 1 || got[0].URL != "https://i/1.gif" {
		t.Fatalf("没解析出来: %+v", got)
	}
	if got[0].From != "firecrawl" {
		t.Fatalf("来源标注丢了: %+v", got[0])
	}
}
