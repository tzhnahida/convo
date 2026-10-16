package convo

import (
	"strings"
	"time"

	"github.com/tzhnahida/convo/internal/clock"
	"github.com/tzhnahida/convo/internal/log"
)

// ─────────────────────────────────────────────────────────────
// §3.6.5c **配角的退出机制** (2026-10-15)
//
// | 触发 | 处置 |
// |---|---|
// | 它自己叙述了结局("他搬家了" / "那家店关门了") | 从**当下生活层**移除 |
// | **长期未被提及**(60 天) | 同上(自然沉降) |
//
// 退出后转入**"过去的人"**: **可以被回忆提起, 但不参与当下**(不能"今天又见到他了")。
//
// ⚠️ **不许复活** —— 说过搬家的不能再若无其事地出现。
// 这不是洁癖: 一个"上周搬走这周又出现"的角色, 会让**整个世界的可信度**一起塌掉,
// 而塌掉之后用户不会去分辨哪一句是真的。
//
// ⚠️ 没有这一层会怎样: 世界观一生效就有 5 个配角, 然后**只进不出** ——
// 一年后它有 40 个"生活里的人", 而它记得的每一个都还在原地等它。
// **那不是丰富, 是不会结束。**
// ─────────────────────────────────────────────────────────────

// CastRetireDays 多久没提起就自然沉降 (§3.6.5c)。
const CastRetireDays = 60

// RetireReasonStale 长期没提起。
const RetireReasonStale = "很久没提起过了"

// ActiveCast 当下生活层 —— **它还生活在一起的人**。
//
// ⚠️ 所有"今天跟谁干了什么"的消费者都必须用它, 不能用 b.Cast。
func ActiveCast(b WorldBible) []CastMember {
	out := make([]CastMember, 0, len(b.Cast))
	for _, m := range b.Cast {
		if !m.Retired {
			out = append(out, m)
		}
	}
	return out
}

// PastCast 过去的人 —— 可以被回忆提起, **不参与当下**。
func PastCast(b WorldBible) []CastMember {
	out := make([]CastMember, 0, len(b.Cast))
	for _, m := range b.Cast {
		if m.Retired {
			out = append(out, m)
		}
	}
	return out
}

// RetireStaleCast 把长期没提起的配角沉降掉 (§3.6.5c「自然沉降」)。
//
// 返回被退掉的名字 (给日志/owner)。
//
// ⚠️ 判据是「**最后一次被提及**」, 不是"StatusDay" —— 后者只说明状态刷新过,
// 而刷新是凌晨自动跑的, 与"它有没有在我生活里出现"是两件事。
// 拿 StatusDay 判的话, **永远不会有配角退出**。
func RetireStaleCast(b *WorldBible, now time.Time) []string {
	if b == nil {
		return nil
	}
	var gone []string
	for i := range b.Cast {
		m := &b.Cast[i]
		if m.Retired {
			continue
		}
		last := strings.TrimSpace(m.LastSeenDay)
		if last == "" {
			// ⚠️ 从没记过 → **不退出**。
			// 零值 = 未提供 ≠ "六十天没见" —— 折成后者会让所有新配角一开始就是过去的人。
			continue
		}
		t, ok := ParseDay(last)
		if !ok {
			continue // 解析不出 → 不动 (失败往"还在"倒)
		}
		if now.Sub(t) >= CastRetireDays*24*time.Hour {
			m.Retired = true
			m.RetiredTS = now.Format(bodyTSLayout)
			m.RetireWhy = RetireReasonStale
			gone = append(gone, m.Name)
		}
	}
	return gone
}

// RetireCastMember 明确退出 (它自己叙述了结局: "他搬家了")。
//
// reason 必填 —— 没有理由的退出与"忘了这个人"长得一模一样。
func RetireCastMember(b *WorldBible, name, reason string, now time.Time) bool {
	if b == nil || strings.TrimSpace(reason) == "" {
		return false
	}
	for i := range b.Cast {
		if b.Cast[i].Name != name || b.Cast[i].Retired {
			continue
		}
		b.Cast[i].Retired = true
		b.Cast[i].RetiredTS = now.Format(bodyTSLayout)
		b.Cast[i].RetireWhy = reason
		return true
	}
	return false
}

// NoteCastMentions 把"这一轮提到了谁"记账 (判 60 天沉降的唯一输入)。
//
// ⚠️ 与 NoteEventMentions 同规: 只在**真的发出去了**之后记。
// 降级/硬拒的产物不算它说的 —— 记了会让一个早该退出的配角永远留在当下。
func NoteCastMentions(b *WorldBible, text, day string) []string {
	if b == nil || strings.TrimSpace(text) == "" {
		return nil
	}
	var hit []string
	for i := range b.Cast {
		m := &b.Cast[i]
		if strings.TrimSpace(m.Name) == "" {
			continue
		}
		if !strings.Contains(text, m.Name) {
			continue
		}
		m.LastSeenDay = day
		hit = append(hit, m.Name)
	}
	return hit
}

// PastCastLine 「过去的人」那一段 —— 可以被回忆提起, 但不参与当下。
//
// ⚠️ 空 → 一个字都不加。
func PastCastLine(b WorldBible) string {
	past := PastCast(b)
	if len(past) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n【过去的人】(系统事实 —— 这些人和你已经不在一起了; 可以**回忆**, 但不能当成今天的事)\n")
	for _, m := range past {
		why := m.RetireWhy
		if why == "" {
			why = "已经不在身边了"
		}
		sb.WriteString("- " + m.Name + ": " + why + "\n")
	}
	sb.WriteString("  ⚠️ **不许让他们若无其事地回到今天** —— 除非有**明确的理由**(他回来办事),\n")
	sb.WriteString("  而那本身要算一件**发生的事**, 不是随口一提。\n")
	return sb.String()
}

// retireStaleCast 夜间批调用: 沉降 + 落库。
func (e *Engine) retireStaleCast() {
	if e.Store == nil {
		return
	}
	owner := e.ownerKey()
	b, ok, err := e.Store.LoadWorldBible(owner)
	if err != nil || !ok {
		return
	}
	gone := RetireStaleCast(&b, clock.Now())
	if len(gone) == 0 {
		return
	}
	if serr := e.Store.SaveWorldBible(owner, b); serr != nil {
		log.Warn("convo:cast", "配角沉降落库失败: %v", serr)
		return
	}
	log.Info("convo:cast", "§3.6.5c %d 位配角长期没提起, 转入「过去的人」: %v", len(gone), gone)
}
