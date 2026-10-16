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
	if !strings.Contains(got["德克萨斯"].Status, "盘货") {
		t.Fatalf("内容不对: %v", got)
	}
}

// 有的模型会包一层 {"cast":[...]} —— 认它, 但认不出就失败, 不许猜。
func TestParseCastStatus_WrappedObject(t *testing.T) {
	got, ok := ParseCastStatus(`{"cast":[{"name":"空","status":"在录新歌"}]}`)
	if !ok || got["空"].Status != "在录新歌" {
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
	s := got["a"].Status
	if !utf8.ValidString(s) {
		t.Fatalf("截出了坏 UTF-8: %q", s)
	}
	if n := utf8.RuneCountInString(s); n != CastStatusMaxRunes {
		t.Fatalf("应留 %d 个字符, 实际 %d", CastStatusMaxRunes, n)
	}
}

// TestApplyCastStatus_LeftRetiresMember
//
// ⚠️ §3.6.5c「明确退出」的生产者就是这里: 模型在日程里写了**结局** (「搬去南方了」)。
// 没有这一半, 退出就只剩「60 天没被提起」那条自然沉降 ——
// 而一个「他搬家了」的角色会一直留在当下生活层里, 然后若无其事地出现。
func TestApplyCastStatus_LeftRetiresMember(t *testing.T) {
	b := WorldBible{Cast: []CastMember{castOf("德克萨斯", "旧的", "2026-10-13")}}
	got := ApplyCastStatus(b, map[string]CastStatusUpdate{
		"德克萨斯": {Status: "搬走前跟我道了别", Left: "搬去南方了"},
	}, "2026-10-14")
	if !got.Cast[0].Retired {
		t.Fatalf("叙述了结局却没退出: %+v", got.Cast[0])
	}
	if got.Cast[0].RetireWhy != "搬去南方了" {
		t.Fatalf("退出理由没记下: %q", got.Cast[0].RetireWhy)
	}
	// 退出后必须在「过去的人」那一侧 —— 可以被回忆提起, 但不参与当下。
	if len(ActiveCast(got)) != 0 || len(PastCast(got)) != 1 {
		t.Fatalf("退出后应当只剩过去的人: active=%d past=%d", len(ActiveCast(got)), len(PastCast(got)))
	}
	// ⚠️ 反面: **只有 status、没有 left** 的人不许被退掉 (默认留空 = 还在过日子)。
	b2 := WorldBible{Cast: []CastMember{castOf("可颂", "旧的", "2026-10-13")}}
	got2 := ApplyCastStatus(b2, map[string]CastStatusUpdate{"可颂": {Status: "在算账"}}, "2026-10-14")
	if got2.Cast[0].Retired {
		t.Fatal("没写 left 却被退掉了 —— 那等于「忘了这个人」")
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
	got := ApplyCastStatus(b, map[string]CastStatusUpdate{"德克萨斯": {Status: "新的"}}, "2026-10-14")
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
