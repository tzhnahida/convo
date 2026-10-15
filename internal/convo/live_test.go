package convo

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// ⚠️ 关键词表要**窄**: 宽了每一句闲聊都会触发搜索,
// 而现象是"它回什么都慢半拍" —— 没人会想到是关键词表太宽。
func TestLiveQuestion(t *testing.T) {
	yes := []string{"最近有什么新游戏", "今天发生了什么", "这个多少钱", "发布了吗", "是不是真的"}
	for _, s := range yes {
		if !LiveQuestion(s) {
			t.Fatalf("应当触发: %q", s)
		}
	}
	no := []string{"", "在吗", "今天好累啊", "你喜欢吃什么", "哈哈哈哈", "晚安"}
	for _, s := range no {
		if LiveQuestion(s) {
			t.Fatalf("不该触发 (闲聊会变成慢半拍): %q", s)
		}
	}
}

// ⚠️ **"搜不到就问"** (2026-10-14 定) —— 不是"老实说不知道"。
func TestLiveNoResultNote_AsksBack(t *testing.T) {
	got := liveNoResultNote("枣椰蜜糖新品")
	if !strings.Contains(got, "什么都没搜到") {
		t.Fatalf("没有把「没搜到」这件事告诉它: %q", got)
	}
	if !strings.Contains(got, "反问") {
		t.Fatalf("没有要求它反问 —— 那它就只会说「我不知道」: %q", got)
	}
	if !strings.Contains(got, "不许编") {
		t.Fatalf("缺少「不许编」这条硬线: %q", got)
	}
}

// ⚠️ 搜到东西时必须**限制在材料内**, 否则它会把搜到的和自己的知识混着说。
func TestLiveResultNote_ConstrainsToMaterial(t *testing.T) {
	got := liveResultNote("某游戏", "- 标题\n  摘要\n  来源: https://x")
	if !strings.Contains(got, "https://x") {
		t.Fatal("材料没带上")
	}
	if !strings.Contains(got, "只许用上面材料里有的") {
		t.Fatalf("没有限制在材料内: %q", got)
	}
}

func TestLiveLookup_PlainChatDoesNotSearch(t *testing.T) {
	e, _ := groupSpeakEngine(t, &fakeChatter{outputs: []string{"{}"}})
	srch := &fakeLearnSearch{hits: []WebHit{{URL: "https://x"}}}
	e.LearnSearch = srch
	if _, ok := e.liveLookup(context.Background(), "在吗"); ok {
		t.Fatal("闲聊不该触发搜索")
	}
	if srch.q != "" {
		t.Fatal("闲聊居然真的去搜了")
	}
}

// ⚠️ **本批的头号契约**: 搜不到**不编**, 而是反问。
func TestLiveLookup_NoHitsAsksBack(t *testing.T) {
	e, _ := groupSpeakEngine(t, &fakeChatter{outputs: []string{"{\"query\":\"某游戏最新\"}"}})
	e.LearnSearch = &fakeLearnSearch{} // 搜到 0 条
	note, ok := e.liveLookup(context.Background(), "最近有什么新游戏")
	if !ok {
		t.Fatal("搜不到也应当注入 —— 注入的是反问指令")
	}
	if !strings.Contains(note, "反问") {
		t.Fatalf("搜不到时应当要求反问: %q", note)
	}
}

// 搜索**挂了**和"没搜到"在日志里要分开, 但对她来说都是"查不到"。
func TestLiveLookup_SearchErrorAlsoAsksBack(t *testing.T) {
	e, _ := groupSpeakEngine(t, &fakeChatter{outputs: []string{"{\"query\":\"某游戏最新消息\"}"}})
	e.LearnSearch = &fakeLearnSearch{err: errors.New("网络挂了")}
	note, ok := e.liveLookup(context.Background(), "最近有什么新闻")
	if !ok || !strings.Contains(note, "反问") {
		t.Fatalf("搜索失败也应当走反问: ok=%v note=%q", ok, note)
	}
}

func TestLiveLookup_HitsAreInjected(t *testing.T) {
	e, _ := groupSpeakEngine(t, &fakeChatter{outputs: []string{"{\"query\":\"某游戏\"}"}})
	e.LearnSearch = &fakeLearnSearch{hits: []WebHit{{Title: "标题", URL: "https://ok", Desc: "摘要"}}}
	note, ok := e.liveLookup(context.Background(), "最近有什么新游戏")
	if !ok || !strings.Contains(note, "https://ok") {
		t.Fatalf("搜到的材料没有注入: ok=%v note=%q", ok, note)
	}
}

func TestLiveLookup_QueryFailureDoesNotSearch(t *testing.T) {
	e, _ := groupSpeakEngine(t, &fakeChatter{outputs: []string{"不是 json"}})
	srch := &fakeLearnSearch{hits: []WebHit{{URL: "https://x"}}}
	e.LearnSearch = srch
	if _, ok := e.liveLookup(context.Background(), "最近有什么新游戏"); ok {
		t.Fatal("收不出搜索词时不该注入任何东西")
	}
	if srch.q != "" {
		t.Fatal("没收敛出搜索词却还是去搜了")
	}
}
