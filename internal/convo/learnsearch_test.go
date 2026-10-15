package convo

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeLearnSearch struct {
	hits []WebHit
	err  error
	q    string
}

func (f *fakeLearnSearch) SearchWeb(ctx context.Context, query string, limit int) ([]WebHit, error) {
	f.q = query
	return f.hits, f.err
}

func learnEngine(t *testing.T, outputs []string, srch LearnSearcher) *Engine {
	t.Helper()
	e, _ := groupSpeakEngine(t, &fakeChatter{outputs: outputs})
	e.LearnSearch = srch
	return e
}

const goodQuery = "{\"query\":\"爵士乐历史\"}"
const goodDigest = "{\"kind\":\"fact\",\"topic\":\"爵士乐\",\"content\":\"爵士乐最早是新奥尔良的游行乐队\",\"origin\":\"看了篇科普\"}"

func TestParseLearnQuery(t *testing.T) {
	if q, ok := ParseLearnQuery(goodQuery); !ok || q != "爵士乐历史" {
		t.Fatalf("want 爵士乐历史, got %q ok=%v", q, ok)
	}
	for _, bad := range []string{"", "{}", "{\"query\":\"\"}", "{\"query\":\"a\"}", "不是json"} {
		if _, ok := ParseLearnQuery(bad); ok {
			t.Fatalf("应当拒收: %q", bad)
		}
	}
}

func TestFormatHits_KeepsDescriptionAndURL(t *testing.T) {
	got := FormatHits([]WebHit{{Title: "爵士乐", URL: "https://x/a", Desc: "起源于游行乐队"}}, 5)
	if !strings.Contains(got, "起源于游行乐队") {
		t.Fatalf("丢了摘要 —— 模型只能拿标题猜, 那就是编: %q", got)
	}
	if !strings.Contains(got, "https://x/a") {
		t.Fatalf("丢了来源: %q", got)
	}
}

// ⚠️ **本批的头号契约**: 搜不到就不学, 绝不退回"让模型自己编一条"。
func TestRunLearning_NoHitsProducesNothing(t *testing.T) {
	e := learnEngine(t, []string{goodQuery, goodDigest}, &fakeLearnSearch{})
	if err := e.runLearning(context.Background(), &PersonaProfile{Name: "小夜"}); err != nil {
		t.Fatal(err)
	}
	items, _ := e.Store.RecentLearned(10)
	if len(items) != 0 {
		t.Fatalf("搜到 0 条却产出了碎片 —— 那就是编的: %+v", items)
	}
}

func TestRunLearning_SearchErrorProducesNothing(t *testing.T) {
	e := learnEngine(t, []string{goodQuery, goodDigest}, &fakeLearnSearch{err: errors.New("boom")})
	if err := e.runLearning(context.Background(), &PersonaProfile{Name: "小夜"}); err != nil {
		t.Fatal(err)
	}
	if items, _ := e.Store.RecentLearned(10); len(items) != 0 {
		t.Fatalf("搜索挂了却产出了碎片: %+v", items)
	}
}

func TestRunLearning_QueryFailureProducesNothing(t *testing.T) {
	e := learnEngine(t, []string{"{\"query\":\"\"}"}, &fakeLearnSearch{hits: []WebHit{{URL: "https://x"}}})
	if err := e.runLearning(context.Background(), &PersonaProfile{Name: "小夜"}); err != nil {
		t.Fatal(err)
	}
	if items, _ := e.Store.RecentLearned(10); len(items) != 0 {
		t.Fatalf("第一步没给出搜索词却学成了: %+v", items)
	}
}

// ⚠️ 来历**由代码写死**, 不信模型 —— 模型给的永远是「看了篇科普」这种查不到的话。
func TestRunLearning_OriginComesFromSearch(t *testing.T) {
	srch := &fakeLearnSearch{hits: []WebHit{{Title: "爵士乐 - 维基", URL: "https://zh.wikipedia.org/wiki/爵士乐", Desc: "起源于游行乐队"}}}
	e := learnEngine(t, []string{goodQuery, goodDigest}, srch)
	if err := e.runLearning(context.Background(), &PersonaProfile{Name: "小夜"}); err != nil {
		t.Fatal(err)
	}
	items, err := e.Store.RecentLearned(10)
	if err != nil || len(items) != 1 {
		t.Fatalf("应当学到一条: %+v err=%v", items, err)
	}
	if !strings.Contains(items[0].Origin, "https://zh.wikipedia.org") {
		t.Fatalf("来历不是真实来源, 而是 %q —— 那以后查不回去", items[0].Origin)
	}
	if strings.Contains(items[0].Origin, "看了篇科普") {
		t.Fatalf("用了模型编的来历: %q", items[0].Origin)
	}
	if srch.q != "爵士乐历史" {
		t.Fatalf("搜索词没有传给搜索口: %q", srch.q)
	}
	// 学到的内容本身照旧不许当经历。
	if items[0].Kind != LearnFact || !strings.Contains(items[0].Content, "新奥尔良") {
		t.Fatalf("内容不对: %+v", items[0])
	}
}

// 红线照旧: 消化出来的东西如果写成亲身经历, 一律拒收。
func TestRunLearning_StillRefusesFakeExperience(t *testing.T) {
	bad := "{\"kind\":\"interest\",\"topic\":\"爵士乐\",\"content\":\"我上周去了一个爵士酒吧\"}"
	e := learnEngine(t, []string{goodQuery, bad}, &fakeLearnSearch{hits: []WebHit{{URL: "https://x", Title: "t"}}})
	if err := e.runLearning(context.Background(), &PersonaProfile{Name: "小夜"}); err != nil {
		t.Fatal(err)
	}
	if items, _ := e.Store.RecentLearned(10); len(items) != 0 {
		t.Fatalf("编成经历的内容被收下了: %+v", items)
	}
}
