package convo

import (
	"strings"

	"github.com/tzhnahida/VocabLock/internal/clock"
	"github.com/tzhnahida/VocabLock/internal/log"
)

// ─────────────────────────────────────────────────────────────
// §7.4 **遗忘权**的执行侧 (2026-10-16 接线)
//
// 设计稿 L1582 / L1859:
//
//   > **遗忘权**: 任何人说"**别记我**" → **物理删除**该主体的全部记忆,
//   > 且**不可被它的社会学习逆转**。
//   > 遗忘权**不能被"它学到的边界"覆盖**。
//
// ⚠️ 而 `ForgetRequested` 那个**判据从来没有调用者** ——
// 也就是说: **用户在对话里行使遗忘权, 没有人在听。**
//
// (能擦的只有 owner 的接口 `handlers_bias.go` 那一条 ——
//  而设计稿说的是"**任何人**说", 那是**用户**的权利, 不是管理功能。)
//
// ⚠️ 它与棘轮看不见的那一类**不同**: 那个函数确实有定义、有测试、也"有人读它"吗 ——
// 不, 它连读者都没有。棘轮**看得见**它(所以它在存量里 ✓),
// 而存量 68 条里**没有一条**标注了"这一条是用户权利"。
// ⇒ 按名字分类找不出它, 得按**设计稿的承诺**去找。
// ─────────────────────────────────────────────────────────────

// noteForgetRequest 用户行使遗忘权 —— **唯一的落点**。
//
// 返回 true = 这一轮是遗忘请求 (调用方不必据此改回法, 正常回即可)。
//
// ⚠️ **两档动作分开**:
//
//	· 明确的 → `ForgetSubject`(物理删除 + 立碑) —— 设计稿要求的那个动作;
//	· 含糊的 → **只记日志**。
//
// ⚠️ 为什么含糊的那一档不删: `ForgetSubject` 立的是**永久**的碑
// (这个人从此不再被记), 而「忘了吧」可以是在说某件具体的事。
// 误判一次 = **不可逆地**抹掉一个人。宁可少删。
//
// ⚠️ **它绝不覆盖"它学到的边界"**(设计稿最后一句):
// 这里没有任何"如果关系够熟就网开一面"的分支 —— 遗忘权优先于一切社交判断。
func (e *Engine) noteForgetRequest(uin, text string) bool {
	if e.Store == nil || strings.TrimSpace(uin) == "" {
		return false
	}
	if !ForgetRequested(text) {
		return false
	}
	if !ForgetEraseRequested(text) {
		// ⚠️ **出声**: 静默不删会让"我看懂了但选择不删"与"没接线"长得一样。
		log.Info("convo:forget", "§7.4 疑似遗忘请求(措辞含糊, 按保守处理: 不删): %q", text)
		return true
	}
	rep, err := e.Store.ForgetSubject(uin, clock.Now())
	if err != nil {
		// ⚠️ 删失败**必须出声** —— 用户以为删了, 而它还在记。
		log.Warn("convo:forget", "§7.4 遗忘权执行失败(uin=%s, 用户以为删了): %v", uin, err)
		return true
	}
	log.Warn("convo:forget", "§7.4 遗忘权: uin=%s 物理删除 %d 行并立碑", uin, rep.Total)
	return true
}
