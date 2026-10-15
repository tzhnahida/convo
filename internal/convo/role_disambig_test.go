package convo

import (
	"context"
	"testing"
)

// TestSearchRoles_FallsBackToDisambiguationLinks
//
// ⚠️ opensearch 的候选**不一定**包含子项。少回一个子项, 整个角色就从候选里静默消失,
// 而现象只是"提炼出来的角色卡很薄"—— 那正是 2026-10-14 那次事故的形状。
//
// 消歧义页**自己**写着它指向谁, 那份目录比再猜一次可靠。
func TestSearchRoles_FallsBackToDisambiguationLinks(t *testing.T) {
	w := &fakeWeb{
		titleList: map[string][]string{"能天使": {"能天使"}},
		texts: map[string]string{
			"能天使":       lookAlikeDisambig(),
			"能天使(明日方舟)": longRoleText("能天使是《明日方舟》的登场角色。"),
		},
		images:        map[string]string{"能天使(明日方舟)": "http://img/x.png"},
		disambig:      map[string]bool{"能天使": true},
		disambigLinks: map[string][]string{"能天使": {"能天使(明日方舟)", "能天使(其他)"}},
	}
	w.titles = map[string]string{}
	// ⚠️ 用一张**真**的角色卡 JSON 当夹具 —— SearchRoles 抽完正文还要过一遍模型。
	cardJSON := "{\"roles\":[{\"name\":\"能天使\",\"series\":\"明日方舟\",\"desc\":\"企鹅物流的信使\",\"traits\":[\"信使\"],\"catchphrase\":\"\",\"famous_line\":\"\",\"line_source\":\"\",\"source_url\":\"u1\",\"image_url\":\"\"}]}"
	f := &fakeChatter{outputs: []string{cardJSON}}
	cards, err := SearchRoles(context.Background(), f, w, []string{"能天使"})
	if err != nil {
		t.Fatalf("回退没生效, 整个角色丢了: %v", err)
	}
	if len(cards) != 1 {
		t.Fatalf("应当出 1 张卡, 实得 %d", len(cards))
	}
	if cards[0].Series != "明日方舟" {
		t.Fatalf("回退拿到的不是真正文页: %+v", cards[0])
	}
}

// TestSearchRoles_DisambiguationWithoutUsableLinksStillSkips
//
// ⚠️ 反面: 收进来的子项**也**不可用 → 仍然整条跳过。
// 回退不是"无论如何都要出一张卡" —— 出一张从目录里拼出来的卡比没有更糟。
func TestSearchRoles_DisambiguationWithoutUsableLinksStillSkips(t *testing.T) {
	w := &fakeWeb{
		titleList: map[string][]string{"能天使": {"能天使"}},
		texts: map[string]string{
			"能天使":  lookAlikeDisambig(),
			"另一个它": "太薄了",
		},
		images:        map[string]string{},
		disambig:      map[string]bool{"能天使": true, "另一个它": true},
		disambigLinks: map[string][]string{"能天使": {"另一个它"}},
	}
	w.titles = map[string]string{}
	f := &fakeChatter{outputs: []string{"{\"roles\":[]}"}}
	if _, err := SearchRoles(context.Background(), f, w, []string{"能天使"}); err == nil {
		t.Fatal("子项也不可用, 却不报错 —— 那会出一张从目录拼出来的假卡")
	}
}

// lookAlikeDisambig 一页**又短又像目录**的消歧义页 (两条判据都命中)。
func lookAlikeDisambig() string {
	return "能天使可以指：\n* 能天使(明日方舟)\n* 能天使(其他)"
}
