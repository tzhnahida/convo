package convo

import "testing"

// ClassifyRaw 决定"修复 pass"的设计方向, 所以每一档都要有代表样本。
// 分档依据是**修复手法不同**, 不是措辞不同 —— 改这个函数前先想清楚修复手法变没变。
func TestClassifyRaw(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"零输出", "", "empty"},
		{"只有空白", "   \n  ", "empty"},
		{"标准帧", `{"reply":[{"kind":"text","text":"嗯"}]}`, "object_ok"},
		{"围栏里带帧(能抽出)", "```json\n{\"reply\":[]}\n```", "object_ok"},
		{"纯散文", "今天好累啊, 不想说话了", "prose"},
		{"只有围栏没有对象", "```\n嗯嗯\n```", "fence_only"},
		{"被截断", `{"reply":[{"kind":"text","text":"我今`, "truncated"},
		{"语法不合法(裸键)", `{reply:[{kind:"text"}]}`, "invalid_json"},
		{"语法不合法(尾逗号)", `{"reply":[],}`, "invalid_json"},
	}
	for _, c := range cases {
		if got := ClassifyRaw(c.raw); got != c.want {
			t.Errorf("%s: ClassifyRaw(%q) = %q, want %q", c.name, c.raw, got, c.want)
		}
	}
}

// 反面用例: 分类器不许把**正常帧**误判成失败 —— 那会让失败率虚高, 直接误导修复方向。
func TestClassifyRawDoesNotCondemnGoodFrames(t *testing.T) {
	good := []string{
		`{"reply":[{"kind":"text","text":"嗯"}],"mood":"good"}`,
		"前缀废话\n{\"reply\":[{\"kind\":\"text\",\"text\":\"好\"}]}\n后缀废话",
		"```json\n{\"reply\":[{\"kind\":\"text\",\"text\":\"好\"}]}\n```",
	}
	for _, g := range good {
		if got := ClassifyRaw(g); got != "object_ok" {
			t.Errorf("正常帧被误判为 %q (失败率会虚高): %q", got, g)
		}
	}
}

func TestTruncRunes(t *testing.T) {
	// 按字符截断: 中文按字节切会切出半个字
	if got := truncRunes("一二三四五", 3); got != "一二三…" {
		t.Fatalf("truncRunes = %q", got)
	}
	if got := truncRunes("一二", 5); got != "一二" {
		t.Fatalf("不该加省略号: %q", got)
	}
}

// 修复 pass 上线后**真实剩下的 3 条失败**(2026-10-13 arena)。
// 它们的原文确实畸形, 但一修就能解析 —— 真正失败的原因是**修好之后内容违规**(空 text 项)。
// 这类必须与"解析器修不动"分开, 否则下一步会去继续加强解析器, 而该改的是提示词。
func TestClassifyTerminalFailureRealSamples(t *testing.T) {
	samples := []string{
		`{"reply":[{"kind":"text","text":"阴天15度还行 我这边也阴"},{"kind":"text","text","text":""}],"mem_id_refs":[],"mood":"平淡"}`,
		`{"reply":[{"kind":"text","text":"哦对 我正闭关呢"},{"kind":"text","text","content":""}],"mem_id_refs":["user_learning_japanese"],"mood":"懒散"}`,
		`{"reply":[{"kind":"text","text":"头发那事我记着呢 就是懒得想"},{"kind":"text","text","text_note":"第二句"}],"mood":"懒散"}`,
	}
	// 三条样本**同病不同形**: 都是"第 2 条 text 项写坏了, 结果 text 为空"。
	// 但修复器能碰的只有第一条 —— 后两条第三个键被**改了名**, 而"改名"没有唯一正确的修法,
	// 猜错会静默丢字段。所以它们保持 invalid_json, 这是**故意的**, 不是漏修。
	want := []string{"repaired_then_invalid", "invalid_json", "invalid_json"}
	for i, s := range samples {
		got := ClassifyTerminalFailure(s)
		if got != want[i] {
			t.Errorf("样本 %d: 分类 = %q, want %q", i+1, got, want[i])
		}
		if got == "object_ok" {
			t.Errorf("样本 %d 被判成 object_ok —— 它明明失败了", i+1)
		}
	}
}

// 反面: 修复器**不许**对"第三个键被改名"下手 —— 没有唯一正确的修法, 猜错就是静默丢字段。
func TestRepairJSONRefusesRenamedThirdKey(t *testing.T) {
	for _, s := range []string{
		`{"kind":"text","text","content":""}`,
		`{"kind":"text","text","text_note":"第二句"}`,
	} {
		if fixed, kind := RepairJSON(s); kind != "" || fixed != s {
			t.Errorf("改名畸形必须原样放行 (不许猜): kind=%q → %s", kind, fixed)
		}
	}
}
