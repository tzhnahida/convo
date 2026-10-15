package convo

import (
	"encoding/json"
	"testing"
)

// 真实样本 (取自 2026-10-13 的 arena 失败原文) —— 这是全部 23 次失败的同一个畸形。
const realBadFrame = `{"reply":[{"kind":"text","text":"等等 灰色卫衣？"},{"kind":"text","text","text":"我记性也不好 别说了"},{"kind":"text","text","text":"那件是不是你说领口有点松的那件"}],"mood":"疑惑"}`

func TestRepairJSONRealSample(t *testing.T) {
	// 前置: 原样确实解析不了 (否则这条测试没意义)
	if _, err := extractJSON(realBadFrame); err == nil {
		t.Fatal("前置不成立: 原始样本居然能解析")
	}
	fixed, kind := RepairJSON(realBadFrame)
	if kind != "dupkey" {
		t.Fatalf("应命中 dupkey, 实得 %q", kind)
	}
	raw, err := extractJSON(fixed)
	if err != nil {
		t.Fatalf("修复后仍解析不了: %v\n%s", err, fixed)
	}
	var f OutputFrame
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	// 内容不许丢: 三条 text 一条都不能少
	if len(f.Reply) != 3 {
		t.Fatalf("修复丢了消息: 期望 3 条, 实得 %d", len(f.Reply))
	}
	if f.Reply[1].Text != "我记性也不好 别说了" {
		t.Fatalf("第 2 条内容错了: %q", f.Reply[1].Text)
	}
}

// 反面: **不许动**正常帧 —— 修复器只在解析失败后才跑, 但这条不变量值得单独钉住。
func TestRepairJSONLeavesGoodFramesAlone(t *testing.T) {
	good := `{"reply":[{"kind":"text","text":"嗯"}],"mood":"good"}`
	if fixed, kind := RepairJSON(good); kind != "" || fixed != good {
		t.Fatalf("正常帧被改了: kind=%q", kind)
	}
}

// 反面: 两个键**不同**时不许猜 —— 那会静默丢字段, 比降级更糟 (降级至少是可见的)。
func TestRepairJSONRefusesDifferentKeys(t *testing.T) {
	weird := `{"a":1,"b":2}` // 合法 JSON, 但假设有人构造了 "a","b": 这种非法序列
	if _, kind := RepairJSON(weird); kind != "" {
		t.Fatal("合法 JSON 不该被修复器碰")
	}
	bad := `{reply:1,"aaa","bbb":2}`
	if _, kind := RepairJSON(bad); kind != "" {
		t.Fatalf("键不同的畸形必须跳过 (不许丢字段), 实得 %q", kind)
	}
}

// 反面: 字符串值里的转义引号不该被误伤 —— 那会把用户说的话改掉。
func TestRepairJSONIgnoresEscapedQuotesInValues(t *testing.T) {
	// 值里含: 她说 "a","a": 这样的一句 (合法 JSON, 靠 \" 转义)
	s := `{"reply":[{"kind":"text","text":"她说 \"a\",\"a\": 这样"}]}`
	if fixed, kind := RepairJSON(s); kind != "" || fixed != s {
		t.Fatalf("转义引号被误伤: kind=%q\n%s", kind, fixed)
	}
}

func TestRepairJSONMultipleOccurrences(t *testing.T) {
	s := `{"reply":[{"kind":"text","text","text":"一"},{"kind":"text","text","text":"二"}]}`
	fixed, kind := RepairJSON(s)
	if kind != "dupkey" {
		t.Fatalf("应命中, 实得 %q", kind)
	}
	if _, err := extractJSON(fixed); err != nil {
		t.Fatalf("多处畸形应一次修完: %v\n%s", err, fixed)
	}
}
