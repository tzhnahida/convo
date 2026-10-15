package convo

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// ⚠️ **只存搜到的** —— 与 §2.5「搜不到就不学」同一条纪律。
func TestFetchDailyNews_NoHitsStoresNothing(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	e.LearnSearch = &fakeLearnSearch{} // 搜到 0 条
	if n := e.FetchDailyNews(context.Background(), &PersonaProfile{Name: "小夜"}); n != 0 {
		t.Fatalf("搜不到却存了 %d 条 —— 那就是编的", n)
	}
	if items, _ := e.Store.RecentNews(e.ownerKey(), 10); len(items) != 0 {
		t.Fatalf("库里不该有东西: %+v", items)
	}
}

func TestFetchDailyNews_SearchErrorStoresNothing(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	e.LearnSearch = &fakeLearnSearch{err: errors.New("网络挂了")}
	if n := e.FetchDailyNews(context.Background(), &PersonaProfile{Name: "小夜"}); n != 0 {
		t.Fatalf("搜索挂了却存了 %d 条", n)
	}
}

func TestFetchDailyNews_StoresRealHits(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	e.LearnSearch = &fakeLearnSearch{hits: []WebHit{
		{Title: "某地发生地震", URL: "https://news/1", Desc: "震级 6.2"},
	}}
	n := e.FetchDailyNews(context.Background(), &PersonaProfile{Name: "小夜"})
	if n == 0 {
		t.Fatal("搜到了却没存")
	}
	note := e.newsNote()
	if !strings.Contains(note, "某地发生地震") {
		t.Fatalf("没注入: %q", note)
	}
	// ⚠️ §3.5 硬约束④: 不许拿事件索取注意力。这条禁令必须在注入里。
	if !strings.Contains(note, "别硬塞") {
		t.Fatalf("缺少「别硬塞」那条禁令 —— 它会变成每日新闻播报: %q", note)
	}
	// ⚠️ 也不许说成自己亲历的。
	if !strings.Contains(note, "不是你在场") {
		t.Fatalf("缺少「不是亲历」的约束: %q", note)
	}
}

func TestNewsNote_EmptyIsEmpty(t *testing.T) {
	if NewsNote(nil) != "" {
		t.Fatal("没有新闻时必须是空串 —— 空段会占位")
	}
	if NewsNote([]NewsItem{{Title: "  "}}) != "" {
		t.Fatal("空标题不该渲染出骨架")
	}
}

// ⚠️ 保温期: 短了它晚上就忘, 长了它会拿三天前的事当"最近"。
func TestRecentNews_KeepsWindow(t *testing.T) {
	s := newTestStore(t)
	owner := "u1"
	if err := s.SaveNews(owner, NewsItem{Title: "刚发生的"}); err != nil {
		t.Fatal(err)
	}
	// 伪造一条三天前的 —— 超出 48 小时保温期, 不该出现。
	old := "2000-01-01 00:00:00"
	if _, err := s.DB.Exec("INSERT INTO convo_news (owner, ts, title, url, summary) VALUES (?,?,?,?,?)",
		owner, old, "很久以前的", "", ""); err != nil {
		t.Fatal(err)
	}
	items, err := s.RecentNews(owner, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Title != "刚发生的" {
		t.Fatalf("保温期没生效: %+v", items)
	}
}

func TestSaveNews_EmptyTitleIsNoop(t *testing.T) {
	s := newTestStore(t)
	if err := s.SaveNews("u1", NewsItem{Title: "   "}); err != nil {
		t.Fatal(err)
	}
	if items, _ := s.RecentNews("u1", 5); len(items) != 0 {
		t.Fatalf("空标题被存进去了: %+v", items)
	}
}

// ⚠️ 查询词只有两条: 多了它会变成新闻聚合器 —— 而它是个朋友, 不是资讯台。
func TestNewsQueries_IsCheap(t *testing.T) {
	qs := newsQueries(&PersonaProfile{Name: "x", Attrs: map[string]string{"interests": "星空 天文"}})
	if len(qs) > 2 {
		t.Fatalf("查询词太多, 它要变资讯台了: %v", qs)
	}
	if len(qs) == 0 || !strings.Contains(qs[0], "热点") {
		t.Fatalf("第一条该是「大家都在聊什么」: %v", qs)
	}
}
