package convo

import (
	"regexp"
	"strings"
)

// ——— 修复 pass (§8.2 "解析容错") ———
//
// [实测 2026-10-13] 真 LLM 帧失败率 37-43%, 形态 **100% 是 invalid_json**, 且失败样本里
// **每一条**都是同一个畸形 —— 输出多条 text 时, 第 2 条起的对象把键写重了一次:
//
//	错误: {"kind":"text","text","text":"我记性也不好"}
//	正确: {"kind":"text","text":"我记性也不好"}
//
// 成因推测: 输出协议里示范的 schema **只有一条 text 项**; 模型要扩成多条时, 复制的键没收住。
// 第 1 条永远是对的, 第 2 条起才坏 —— 样本无一例外。
//
// ⚠️ **必须匹配"三连"而不是"两连"** —— 这一条是测试逼出来的, 值得记下来:
//
//	正常帧里也有 `"kind":"text","text":"嗯"` 这段字节 —— 它的子串 `"text","text":`
//	和畸形长得一模一样。若按"两连"修, 会把正常帧的 `text` 键**删掉**, 得到 `{"kind":"text":"嗯"}`。
//
// 区别在**第三个**: 畸形是 `"text","text","text":` (三连), 正常是 `"text","text":` (两连)。
// 而合法 JSON 里不可能出现三连(那本身就是裸字符串), 所以只修三连是**安全的**。
//
// 安全边界: 这个修复**只在解析已经失败之后**才跑, 修完还要重新走一遍 extractJSON + json.Valid。
// 最坏情况等于没修 —— 不可能把好帧改坏。且它是纯字符串确定性操作, **不增加任何 LLM 调用**。
var dupKeyRe = regexp.MustCompile(`"([A-Za-z_][A-Za-z0-9_]*)"\s*,\s*"([A-Za-z_][A-Za-z0-9_]*)"\s*,\s*"([A-Za-z_][A-Za-z0-9_]*)"\s*:`)

// RepairJSON 尝试修复已知畸形; 返回 (修好的文本, 命中的修复名)。
// 没命中 → 原样返回 + 空串 (调用方据此走原路径)。
//
// ⚠️ Go 的 regexp 是 RE2, **不支持反向引用**, 所以"三个键必须相同"只能在代码里比 ——
// 这也正好让我们可以拒绝猜测: 键不同就**跳过**, 绝不擅自丢字段。
func RepairJSON(s string) (string, string) {
	locs := dupKeyRe.FindAllStringSubmatchIndex(s, -1)
	if len(locs) == 0 {
		return s, ""
	}
	var b strings.Builder
	last, fired := 0, 0
	for _, m := range locs {
		k1, k2, k3 := s[m[2]:m[3]], s[m[4]:m[5]], s[m[6]:m[7]]
		if k1 != k2 || k2 != k3 {
			continue // 键不全同 → 不是这个畸形, 不猜
		}
		b.WriteString(s[last:m[0]])
		b.WriteString(`"` + k1 + `","` + k1 + `":`) // 三连 → 两连 (只去掉一个)
		last = m[1]
		fired++
	}
	if fired == 0 {
		return s, ""
	}
	b.WriteString(s[last:])
	return b.String(), "dupkey"
}
