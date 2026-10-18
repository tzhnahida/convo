package convo

import (
	"strings"
	"testing"
)

// ——— §5.5 主体性: 接线测试 ———
//
// ⚠️ 又一组"它有没有被接上"的测试。agency.go 的单测证明了 DecideDefiance 判得对,
// 但**证明不了许可被送进了提示词、也证明不了超界会被拦** ——
// BodyLine 就是这么当了一整轮死代码的。

// TestDefianceLine_NoInternalNames 反面: **不许把内部档位名写进提示词**。
// 改坏成: 直接把 mode 字符串拼进去 → 红。那是我们的词汇, 它会照着念。
func TestDefianceLine_NoInternalNames(t *testing.T) {
	all := []DefianceMode{DefySilent, DefyDelay, DefyRefuse, DefyTalkBack}
	for _, m := range all {
		line := DefianceLine(m, "不喜欢吃香菜")
		if line == "" {
			t.Fatalf("档位 %d 应当有动作描述", m)
		}
		for _, leak := range []string{"silent", "delay", "refuse", "talkback", "Defy"} {
			if strings.Contains(strings.ToLower(line), strings.ToLower(leak)) {
				t.Fatalf("档位 %d 的段里泄露了内部名 %q:\n%s", m, leak, line)
			}
		}
	}
	// 零值 = 不给许可 = 空串。常驻的许可会让它动不动就杠。
	if got := DefianceLine(DefyNone, "随便"); got != "" {
		t.Fatalf("DefyNone 必须不注入, 得到:\n%s", got)
	}
}

// 正面: 给了许可 → 段真的进提示词, 且排在【本轮话题】**之后** (轮级而不是日级)。
func TestBuildSystem_DefianceReachesPrompt(t *testing.T) {
	d := &DialogEngine{}
	p := &PersonaProfile{Name: "小夜", Template: "bestie"}
	in := DialogInput{
		UserText: "陪我去逛街吧",
		Topic:    Topic{Kind: "life", Text: "周末有什么计划"},
		Agency:   &AgencyContext{Defiance: DefyRefuse, Stance: "不喜欢逛街", TopicKey: "逛街"},
	}
	sys := d.buildSystem(in, p, "ja")
	if !strings.Contains(sys, "【这一轮你可以不那么配合】") {
		t.Fatal("违抗许可没进提示词 —— 机制等于没接线")
	}
	iTopic := strings.Index(sys, "【本轮话题】")
	iDefy := strings.Index(sys, "【这一轮你可以不那么配合】")
	if iTopic < 0 || iDefy < 0 || iDefy < iTopic {
		t.Fatalf("违抗段必须在【本轮话题】之后 (轮级), 实际 topic=%d defy=%d", iTopic, iDefy)
	}
}

// 反面: 不给许可 → 提示词里**不许**出现这一段。
func TestBuildSystem_NoAgencyNoDefiance(t *testing.T) {
	d := &DialogEngine{}
	p := &PersonaProfile{Name: "小夜", Template: "bestie"}
	sys := d.buildSystem(DialogInput{UserText: "陪我去逛街吧", Topic: Topic{Kind: "life", Text: "计划"}}, p, "ja")
	if strings.Contains(sys, "【这一轮你可以不那么配合】") {
		t.Fatal("没给许可却注入了违抗段")
	}
}

// ——— 校验链: 许可就是许可, 不是建议 ———

func TestValidateFrame_DefyBeyondPermit(t *testing.T) {
	d := &DialogEngine{}
	p := &PersonaProfile{Name: "小夜"}
	base := DialogInput{Agency: &AgencyContext{Defiance: DefyDelay}}

	// 上界内 → 放行。
	ok := OutputFrame{Reply: []FrameItem{{Kind: "text", Text: "嗯"}}, Defy: &FrameDefy{Mode: "delay"}}
	if v := d.ValidateFrame(ok, base, p); len(v) != 0 {
		t.Fatalf("上界内的档位不该违规: %v", v)
	}
	// 超界 → 违规。改坏成: 删掉这段校验 → 红。
	over := OutputFrame{Reply: []FrameItem{{Kind: "text", Text: "我不去"}}, Defy: &FrameDefy{Mode: "talkback"}}
	if v := d.ValidateFrame(over, base, p); len(v) == 0 {
		t.Fatal("档位超界却没被拦 —— 那么「系统给上界」就只是一句建议")
	}
	// 乱填 → 违规。
	bad := OutputFrame{Reply: []FrameItem{{Kind: "text", Text: "嗯"}}, Defy: &FrameDefy{Mode: "随便杠一下"}}
	if v := d.ValidateFrame(bad, base, p); len(v) == 0 {
		t.Fatal("认不出的档位该拦")
	}
	// 反面: 没有 Agency (没接线) 时**任何**违抗都算越权 —— 零值必须是最保守那侧。
	if v := d.ValidateFrame(ok, DialogInput{}, p); len(v) == 0 {
		t.Fatal("没接线却放行了违抗")
	}
}

func TestValidateFrame_PromiseGate(t *testing.T) {
	d := &DialogEngine{}
	p := &PersonaProfile{Name: "小夜"}
	f := OutputFrame{Reply: []FrameItem{{Kind: "text", Text: "明天一起吃饭吧"}},
		Promise: &FramePromise{What: "一起吃饭", When: "明天 19:00"}}

	if v := d.ValidateFrame(f, DialogInput{Agency: &AgencyContext{MayCommit: true}}, p); len(v) != 0 {
		t.Fatalf("信用够时不该拦: %v", v)
	}
	// 改坏成: 不看 MayCommit → 红。信用低时它还能继续约 = 那个闸门白写。
	if v := d.ValidateFrame(f, DialogInput{Agency: &AgencyContext{MayCommit: false}}, p); len(v) == 0 {
		t.Fatal("信用不足却放行了新的约定")
	}
	if v := d.ValidateFrame(f, DialogInput{}, p); len(v) == 0 {
		t.Fatal("没接线却放行了约定")
	}
	// 空 what → 拦 (约定必须写得下来说的是什么, 否则归因不了)。
	empty := OutputFrame{Reply: []FrameItem{{Kind: "text", Text: "嗯"}}, Promise: &FramePromise{When: "明天 19:00"}}
	if v := d.ValidateFrame(empty, DialogInput{Agency: &AgencyContext{MayCommit: true}}, p); len(v) == 0 {
		t.Fatal("空 what 该拦")
	}
}
