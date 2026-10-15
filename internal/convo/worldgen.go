package convo

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// worldBiblePrompt L1/L2/L3 的生成提示词。
//
// ⚠️ 三件事必须写进提示词, 否则生成出来的东西要么过不了 Validate 要么过不了护栏:
//  1. **关系类型只能是四类** —— 模型最爱自由发挥出「青梅竹马」;
//  2. **2-5 人** —— 少了没有关系层, 多了记不住;
//  3. **不许恋爱向/未成年/伤害向** —— 产品红线。
func worldBiblePrompt(p *PersonaProfile, origin string) string {
	var b strings.Builder
	b.WriteString("你是 " + p.Name + "。" + p.Personality + "\n")
	if origin != "" {
		b.WriteString("你来自: " + origin + "\n")
	}
	b.WriteString("请写出**你的来处** —— 你生活的世界, 和你最熟的那几个人。\n\n")
	b.WriteString("严格输出单行 JSON:\n")
	b.WriteString("{\"self\":\"你在这个世界里的位置, 一句话\",")
	b.WriteString("\"places\":[{\"text\":\"地点\",\"source\":\"出处或空\"}],")
	b.WriteString("\"orgs\":[],\"rules\":[],\"timeline\":[],")
	b.WriteString("\"cast\":[{\"name\":\"名字\",\"relation\":\"companion\",")
	b.WriteString("\"history\":\"一段共同历史, 40 字内\",\"topics\":\"话题偏好\",")
	b.WriteString("\"status\":\"现在在忙什么, 20 字内\",\"source\":\"出处或空\"}]}\n\n")
	b.WriteString("硬性要求:\n")
	b.WriteString("① relation **只能是** companion(同伴) / rival(对手) / family(家人) / mentor(师长);\n")
	b.WriteString("② cast **至少 2 人、最多 5 人**;\n")
	b.WriteString("③ 不许写成恋人/暧昧关系, 不许涉及未成年, 不许有伤害内容;\n")
	b.WriteString("④ **不要逐字抄原作文本** —— 用自己的话概括;\n")
	b.WriteString("⑤ places/rules 等有原作依据的填 source(作品名), 你自己编的 source 留空。")
	return b.String() + NowLine()
}

// ParseWorldBible 解析生成结果。
func ParseWorldBible(raw string) (WorldBible, bool) {
	b, err := extractJSON(raw)
	if err != nil {
		return WorldBible{}, false
	}
	var wb WorldBible
	if err := json.Unmarshal(b, &wb); err != nil {
		return WorldBible{}, false
	}
	return NormalizeWorldBible(wb), true
}

// GenerateWorldBible 生成一份世界观 —— **落库前必须过 Validate 与护栏**。
//
// ⚠️ 校验不过就**不返回半个世界**: 半个世界观比没有更糟 ——
// §2.3 要求「现编必须落库再用」, 落了一半的库, 下次生成会和它矛盾。
func GenerateWorldBible(ctx context.Context, c Chatter, p *PersonaProfile) (WorldBible, error) {
	if c == nil || p == nil {
		return WorldBible{}, fmt.Errorf("world bible: 人格或模型缺失")
	}
	origin := ""
	if p.Origin != nil {
		origin = strings.TrimSpace(p.Origin.Name + " " + p.Origin.Series)
	}
	out, err := c.Chat(ctx, worldBiblePrompt(p, origin), "开始。")
	if err != nil {
		return WorldBible{}, err
	}
	wb, ok := ParseWorldBible(out)
	if !ok {
		return WorldBible{}, fmt.Errorf("world bible: 产出不是合法 JSON")
	}
	if err := ValidateWorldBible(wb); err != nil {
		return WorldBible{}, err
	}
	return wb, nil
}
