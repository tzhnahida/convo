package convo

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func castOf(name, status, day string) CastMember {
	return CastMember{Name: name, Relation: RelationCompanion, Status: status, StatusDay: day}
}

// ⚠️ 判据是**日期串**, 不是"距上次多久" —— 后者在跨天那一拍会算错,
// 表现是某天忘了刷新: 静默, 且无从察觉。
func TestCastStatusStale_ByDayString(t *testing.T) {
	today := "2026-10-14"
	cases := []struct {
		name string
		b    WorldBible
		want bool
	}{
		{"没有人就不该刷", WorldBible{}, false},
		{"全是今天的", WorldBible{Cast: []CastMember{castOf("a", "x", today), castOf("b", "y", today)}}, false},
		{"有一个人是昨天的", WorldBible{Cast: []CastMember{castOf("a", "x", today), castOf("b", "y", "2026-10-13")}}, true},
		{"从没刷过 (StatusDay 空)", WorldBible{Cast: []CastMember{castOf("a", "x", "")}}, true},
	}
	for _, c := range cases {
		if got := CastStatusStale(c.b, today); got != c.want {
			t.Fatalf("%s: want %v got %v", c.name, c.want, got)
		}
	}
}

func TestParseCastStatus_PlainArray(t *testing.T) {
	got, ok := ParseCastStatus(`[{"name":"德克萨斯","status":"在三号仓库盘货, 手冻僵了"},{"name":"可颂","status":"被老板叫去算账"}]`)
	if !ok || len(got) != 2 {
		t.Fatalf("解析失败: ok=%v got=%v", ok, got)
	}
	if !strings.Contains(got["德克萨斯"], "盘货") {
		t.Fatalf("内容不对: %v", got)
	}
}

// 有的模型会包一层 {"cast":[...]} —— 认它, 但认不出就失败, 不许猜。
func TestParseCastStatus_WrappedObject(t *testing.T) {
	got, ok := ParseCastStatus(`{"cast":[{"name":"空","status":"在录新歌"}]}`)
	if !ok || got["空"] != "在录新歌" {
		t.Fatalf("包一层就解析不出来: ok=%v got=%v", ok, got)
	}
	for _, bad := range []string{"", "不是 JSON", "[]", `[{"name":"","status":"x"}]`, `[{"name":"a","status":""}]`} {
		if _, ok := ParseCastStatus(bad); ok {
			t.Fatalf("这种输入不该解析成功: %q", bad)
		}
	}
}

// ⚠️ 长度上限必须**按字符** —— 按字节切会把汉字切坏, 而那是要进提示词的。
func TestParseCastStatus_TruncatesRunesNotBytes(t *testing.T) {
	long := strings.Repeat("茶", 60)
	got, ok := ParseCastStatus(`[{"name":"a","status":"` + long + `"}]`)
	if !ok {
		t.Fatal("解析失败")
	}
	s := got["a"]
	if !utf8.ValidString(s) {
		t.Fatalf("截出了坏 UTF-8: %q", s)
	}
	if n := utf8.RuneCountInString(s); n != CastStatusMaxRunes {
		t.Fatalf("应留 %d 个字符, 实际 %d", CastStatusMaxRunes, n)
	}
}

// ⚠️ **只动命中的那些人** —— 没更新的人必须保留旧的 StatusDay,
// 于是明天还会被算作 stale 再试一次。
//
// 若把没更新的人也标成"今天已更新", 一次模型抽风就会让那个人
// **永久停在旧日程上**, 而且没人会发现。
func TestApplyCastStatus_UnmatchedKeepOldDay(t *testing.T) {
	b := WorldBible{Cast: []CastMember{
		castOf("德克萨斯", "旧的", "2026-10-13"),
		castOf("可颂", "也可颂", "2026-10-13"),
	}}
	got := ApplyCastStatus(b, map[string]string{"德克萨斯": "新的"}, "2026-10-14")
	if got.Cast[0].Status != "新的" || got.Cast[0].StatusDay != "2026-10-14" {
		t.Fatalf("命中的人没更新: %+v", got.Cast[0])
	}
	if got.Cast[1].Status != "也可颂" || got.Cast[1].StatusDay != "2026-10-13" {
		t.Fatalf("没命中的人被改了: %+v", got.Cast[1])
	}
	// 关键: 明天它还得是 stale。
	if !CastStatusStale(got, "2026-10-15") {
		t.Fatal("没命中的人明天必须再试一次, 否则它会永久停在旧日程上")
	}
}

// 生成失败**不动库** —— 旧日程比没有日程好。
func TestRefreshCastStatus_FailureKeepsOld(t *testing.T) {
	b := WorldBible{Cast: []CastMember{castOf("a", "旧日程", "2026-10-13")}}
	nb, ok := RefreshCastStatus(nil, nil, nil, b, "2026-10-14", "")
	if ok {
		t.Fatal("没有模型却报成功")
	}
	if nb.Cast[0].Status != "旧日程" {
		t.Fatalf("失败时动了数据: %+v", nb.Cast[0])
	}
}
