package convo

import "testing"

// TestItoa_HandlesBigAndNegative
//
// ⚠️ 回归: 老实现用 `var b [8]byte` 从后往前填, **超过 8 位就下标越界 panic**。
// 而它每天经手的正是 QQ 号(最多 10 位) —— 名册里放一个 9 位以上的号,
// 夜间批会崩在 `night:dm:<uin>` 这一行上, 症状只是"某天夜里没跑"。
//
// 世界观配角的场景键改用负数 UIN 之后, 负号又多吃一位 —— 同一个坑的第二种踩法。
func TestItoa_HandlesBigAndNegative(t *testing.T) {
	cases := map[int]string{
		0:          "0",
		-1:         "-1",
		12345678:   "12345678",
		123456789:  "123456789",  // 9 位(真实 QQ 号常见长度) —— 老实现从这里开始崩
		1234567890: "1234567890", // 10 位
		-987654321: "-987654321", // 负号多吃一位
	}
	for in, want := range cases {
		if got := itoa(in); got != want {
			t.Errorf("itoa(%d) = %q, 想要 %q", in, got, want)
		}
	}
}
