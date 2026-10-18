package convo

import (
	"strings"
	"testing"
)

// ⚠️ 2026-10-18 生产事故: 「你还挺会挑」被判成 A 层(可违抗), 于是系统注入
// 「ta 在让你做一件你不太愿意的事」, 模型去找那个不存在的要求, 回了一句
// 「挑什么 我挑东西最在行了」。根因是判据里有一个**裸的「你」**。
func TestClassifyRequest_JokeIsNotARequest(t *testing.T) {
	// 调侃 / 陈述 —— 都不是请求, 必须落到"不违抗"的那一层。
	for _, s := range []string{"你还挺会挑", "你这话说的", "咱俩半斤八两", "数学看着头大"} {
		if got := ClassifyRequest(s, nil); got != TierSafety {
			t.Errorf("%q 被判成 %v —— 它不是请求, 不该给违抗许可", s, got)
		}
	}
	// 反面: 真正的请求/征询必须照旧识别出来, 别把功能一起删没了。
	// ⚠️ 判定顺序是 C → B → A: "一起吃饭"是**约定**(B), 比征询(A)更严 —— 那是设计。
	for _, s := range []string{"帮我看看这个", "你今晚有空吗", "能不能陪我去", "推荐一下"} {
		if got := ClassifyRequest(s, nil); got != TierStance {
			t.Errorf("%q 应该判成 A 层, 实际 %v", s, got)
		}
	}
}

// ⚠️ 假前提是这一条的核心: 我们不知道用户想干什么, 就**不许写进事实层**。
func TestDefianceLine_DoesNotClaimUserIntent(t *testing.T) {
	for _, m := range []DefianceMode{DefySilent, DefyDelay, DefyRefuse, DefyTalkBack} {
		line := DefianceLine(m, "")
		if line == "" {
			t.Fatalf("档位 %v 应当有许可文案", m)
		}
		if strings.Contains(line, "ta 在让你做") {
			t.Fatalf("还在断言用户的意图 (假前提): %q", line)
		}
		if !strings.Contains(line, DefianceAction(m)) {
			t.Fatalf("许可文案里没有具体动作 %q: %q", DefianceAction(m), line)
		}
	}
	if DefianceLine(DefyNone, "") != "" {
		t.Fatal("零档位不许注入任何许可")
	}
}
