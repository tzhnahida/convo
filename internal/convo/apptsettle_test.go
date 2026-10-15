package convo

import (
	"strings"
	"testing"
	"time"
)

// ⚠️ 本文件测的是 **§5.5 约定的主动处置**(提前取消/改期/承认忘了),
// 与 settle.go(世界事件的**沉降**)不是一回事 —— 名字像, 机制完全不同。

func apptSettleFixture(t *testing.T) (*Store, *Engine, int64) {
	t.Helper()
	s := agTestStore(t)
	id, err := s.CreateAppointment(Appointment{
		Tier: TierPromise, Who: 1, Scene: "qq", Text: "一起吃饭",
		// ⚠️ DueTS 必须在**未来** —— 设成"现在"的话它不是"待履行"而是"到点了",
		// PendingAppointments 返回空, 于是整条用例会**因为错误的原因通过**。
		CreatedTS: time.Now().Format(bodyTSLayout),
		DueTS:     time.Now().Add(24 * time.Hour).Format(bodyTSLayout),
		Terminal:  ApptOpen,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, &Engine{Store: s, Cfg: DefaultConfig()}, id
}

func mustPending(t *testing.T, s *Store) []Appointment {
	t.Helper()
	list, err := s.PendingAppointments(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return list
}

// TestApptSettle_CancelledIsFinallyProducible
//
// ⚠️ **这条是本项的立案理由。** 在它之前 ApptCancelled 有定义、有信用分(0.5)、有注释,
// 却**没有任何生产路径** —— 于是一条约定只有两种结局: 赴约, 或者到点了才反悔。
// 「AI 可以拒绝」缺的正是这条路径。
func TestApptSettle_CancelledIsFinallyProducible(t *testing.T) {
	s, e, id := apptSettleFixture(t)
	// ⚠️ 第二参必须是**模型当时看到的那份列表** —— 生产路径传的是 in.Agency.Pending,
	// 而不是"去库里现查一遍"。传 nil 就是一条也认不出来(我自己第一版就是这么错的)。
	e.settleFromFrame(&FrameSettle{ID: id, State: ApptCancelled, Reason: "那天要去送一趟急件"}, mustPending(t, s))

	if agencyHasPending(mustPending(t, s), id) {
		t.Fatal("处置之后它还在待履行列表里 —— 终态没写上")
	}
	// 正面: 账本里真的记了(而不是没写) —— 信用曲线必须动。
	rel, err := s.Reliability(time.Now(), 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if rel >= 1.0 {
		t.Fatalf("取消了却还是满信用(%.2f)—— 说明这条终态根本没进账本", rel)
	}
}

// TestApptSettle_RescheduleReallyCreatesANewOne
//
// ⚠️ §4.5.3-4「不可约时**协商替代时段**」—— 光说不行, 得真的重约一条。
func TestApptSettle_RescheduleReallyCreatesANewOne(t *testing.T) {
	s, e, id := apptSettleFixture(t)
	e.settleFromFrame(&FrameSettle{ID: id, State: ApptCancelled, Reason: "那天有事", Instead: "明天 19:00"}, mustPending(t, s))

	pending := mustPending(t, s)
	if len(pending) != 1 {
		t.Fatalf("改期之后应当恰好剩 1 条待履行, 实得 %d", len(pending))
	}
	if pending[0].ID == id {
		t.Fatal("剩下的还是原来那条 —— 那叫没结算, 不叫改期")
	}
	if pending[0].Text != "一起吃饭" {
		t.Errorf("改期把内容弄丢了: %q", pending[0].Text)
	}
}

// TestApptSettle_UnparsableInsteadCancelsOnly
//
// 反面: 替代时段解析不出 → **只取消, 不重约**(不许落一条永远结算不了的幽灵记录)。
func TestApptSettle_UnparsableInsteadCancelsOnly(t *testing.T) {
	s, e, id := apptSettleFixture(t)
	e.settleFromFrame(&FrameSettle{ID: id, State: ApptCancelled, Reason: "那天有事", Instead: "周末吧"}, mustPending(t, s))
	if pending := mustPending(t, s); len(pending) != 0 {
		t.Fatalf("时段解析不出却落了新约定 —— 那是一条永远结算不了的幽灵记录: %+v", pending)
	}
}

// TestApptSettle_ForgotSettlesWithoutReason
func TestApptSettle_ForgotSettlesWithoutReason(t *testing.T) {
	s, e, id := apptSettleFixture(t)
	e.settleFromFrame(&FrameSettle{ID: id, State: ApptForgot}, mustPending(t, s))
	if agencyHasPending(mustPending(t, s), id) {
		t.Fatal("承认忘了之后还在待履行列表里")
	}
}

// TestApptSettle_UnknownIDIsRefusedAtWritePath
//
// ⚠️ 第二道闸: 校验链之外, **写路径自己也不许认一个不在待履行列表里的 id**。
// 这条写路径会改信用曲线, 而信用曲线又反过来决定 MayCommit —— 写错一次, 后面全是错的。
func TestApptSettle_UnknownIDIsRefusedAtWritePath(t *testing.T) {
	s, e, _ := apptSettleFixture(t)
	e.settleFromFrame(&FrameSettle{ID: 99999, State: ApptCancelled, Reason: "x"}, mustPending(t, s))
	if len(mustPending(t, s)) != 1 {
		t.Fatal("处置了一个不存在的 id, 却动了真实的账本")
	}
}

// ——— 校验链 ———

// settleFrame 造一个**带 text** 的帧 —— 校验链会拦"缺少 text 消息",
// 所以只填 Settle 的帧测出来的是别的东西, 不是这条规则。
func settleFrame(s *FrameSettle) OutputFrame {
	return OutputFrame{
		Reply:  []FrameItem{{Kind: "text", Text: "那天我可能去不了"}},
		Settle: s,
	}
}

func apptSettleInput(id int64) DialogInput {
	return DialogInput{
		UserText: "在吗",
		Agency: &AgencyContext{Pending: []Appointment{
			{ID: id, Text: "一起吃饭", DueTS: "2026-10-16 19:00:00"},
		}},
	}
}

// TestValidateFrame_ApptSettleChecksBothEnds
//
// ⚠️ 四种反面都要拦: 编 id / 缺 id / state 非法 / cancelled 没理由。
func TestValidateFrame_ApptSettleChecksBothEnds(t *testing.T) {
	d := &DialogEngine{}
	p := &PersonaProfile{Name: "小夜", Template: "bestie"}
	const id = 7
	in := apptSettleInput(id)

	if v := d.ValidateFrame(settleFrame(&FrameSettle{ID: id, State: ApptCancelled, Reason: "有事"}), in, p); len(v) != 0 {
		t.Fatalf("合法处置被拦: %v", v)
	}
	if v := d.ValidateFrame(settleFrame(&FrameSettle{ID: id, State: ApptForgot}), in, p); len(v) != 0 {
		t.Fatalf("forgot 不该要理由, 却被拦: %v", v)
	}
	if v := d.ValidateFrame(settleFrame(&FrameSettle{ID: 4242, State: ApptCancelled, Reason: "x"}), in, p); len(v) == 0 {
		t.Error("编了一个不在待履行列表里的 id —— 必须拦")
	}
	if v := d.ValidateFrame(settleFrame(&FrameSettle{State: ApptCancelled, Reason: "x"}), in, p); len(v) == 0 {
		t.Error("缺 id 必须拦")
	}
	// kept / breached 由**系统**判, 不许它自己安在头上。
	for _, bad := range []string{ApptKept, ApptBreached, "whatever"} {
		if v := d.ValidateFrame(settleFrame(&FrameSettle{ID: id, State: bad}), in, p); len(v) == 0 {
			t.Errorf("state=%q 必须拦", bad)
		}
	}
	if v := d.ValidateFrame(settleFrame(&FrameSettle{ID: id, State: ApptCancelled}), in, p); len(v) == 0 {
		t.Error("取消却不给理由 —— §5.5「没有表达的处置等于没发生」, 必须拦")
	}
}

// TestSituationLine_CarriesPendingIDs
//
// ⚠️ 没有 id, 模型只能靠"那件事"指代 —— 而系统无法确定它指的是哪一条,
// 于是"提前取消"这条路径**永远落不了地**。这是它一直只有 kept/breached 的原因之一。
func TestSituationLine_CarriesPendingIDs(t *testing.T) {
	line := SituationLine(SituationInput{
		Now:     time.Now(),
		Pending: []Appointment{{ID: 12, Text: "一起吃饭", DueTS: "2026-10-16 19:00:00"}},
	})
	if !strings.Contains(line, "#12") {
		t.Errorf("待履行段没带 id —— 提前取消就指不了目标:\n%s", line)
	}
	for _, must := range []string{"settle_promise", "cancelled", "forgot", "提前"} {
		if !strings.Contains(line, must) {
			t.Errorf("待履行段没告诉它怎么处置, 缺 %q:\n%s", must, line)
		}
	}
}
