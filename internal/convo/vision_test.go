package convo

import "testing"

// A5: 图意标注必须走 side-channel —— 它曾经跟着描述整段回流成"用户说的话"。
func TestSplitMemeNote(t *testing.T) {
	cases := []struct {
		name       string
		comment    string
		wantSpoken string
		wantNote   string
	}{
		{
			"梗图: 口语与标注必须拆开",
			"一只猫在键盘上踩来踩去 笑死\n【梗图】猫在替主人写代码",
			"一只猫在键盘上踩来踩去 笑死",
			"猫在替主人写代码",
		},
		{
			"普通照片: 没有标注就不动它",
			"海边的日落 拍得挺好",
			"海边的日落 拍得挺好",
			"",
		},
		{
			"标注夹在中间也要切干净",
			"前一句\n【梗图】含义\n后一句",
			"前一句",
			"含义\n后一句",
		},
	}
	for _, c := range cases {
		sp, note := splitMemeNote(c.comment)
		if sp != c.wantSpoken {
			t.Errorf("%s: spoken = %q, want %q", c.name, sp, c.wantSpoken)
		}
		if note != c.wantNote {
			t.Errorf("%s: note = %q, want %q", c.name, note, c.wantNote)
		}
	}
}

// 🔴 反面: 标注**绝不能**留在 spoken 里 —— 那就是本病灶。
func TestSplitMemeNote_NeverLeaksMarker(t *testing.T) {
	comments := []string{
		"【梗图】只有标注没有口语",
		"描述\n【梗图】含义",
		"描述 【梗图】含义",
	}
	for _, c := range comments {
		sp, _ := splitMemeNote(c)
		if sp != "" && containsStr(sp, "【梗图】") {
			t.Errorf("标注泄漏进用户消息行: %q → %q", c, sp)
		}
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
