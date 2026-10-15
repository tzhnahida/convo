package convo

import (
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────
// §5.5 **找补** (M10): 「连续两次毁约后必须有一次补偿行为」
//
// ⚠️ 这条链原来断在最后一格:
//
//	`MayCommit` ← `CompensationOwed` ← 最近那次毁约的 `amends_ts == ""`
//
// 而 `MarkAmends` **零调用** ⇒ `amends_ts` 无写入者 ⇒
// 连续两次毁约之后**只能等 7 天记仇期**。
//
// ⚠️ 而**提示词那一侧早就在叫它找补了** ——
// 这是「提示词里的上限不算数」的**镜像**: 那一条是"说了不做",
// 这一条是"**做了不算**"。
// ─────────────────────────────────────────────────────────────

func amendsEngine(t *testing.T) (*Engine, time.Time) {
	t.Helper()
	e := gateEngine(t)
	if err := EnsureAgencySchema(e.Store); err != nil {
		t.Fatal(err)
	}
	return e, time.Now()
}

// owesAmends 造出"欠一次补偿"的局面: 连续两次毁约, 且最近那次没找补。
func owesAmends(t *testing.T, e *Engine, now time.Time) {
	t.Helper()
	// ⚠️ 用现成的夹具(直接落终态) —— 这条不变量测的是"找补有没有被记上",
	// 不是"毁约怎么变成毁约"。
	agSeedSettled(t, e.Store, ApptBreached, "不想去", now.Add(-72*time.Hour), "")
	agSeedSettled(t, e.Store, ApptBreached, "不想去", now.Add(-48*time.Hour), "")
	if owed, err := e.Store.CompensationOwed(now); err != nil || !owed {
		t.Fatalf("夹具坏了: 应当欠着补偿 (owed=%v err=%v)", owed, err)
	}
}

// TestAmends_RecordsAndUnlocks (正面)
func TestAmends_RecordsAndUnlocks(t *testing.T) {
	e, now := amendsEngine(t)
	owesAmends(t, e, now)

	// 先钉住"不能约" —— 否则下面那句"解锁了"没有对照。
	if ok, _ := e.Store.MayCommit(now); ok {
		t.Fatal("欠着补偿却还能主动约定 —— 夹具不对")
	}

	e.noteAmendsIfAny("上次放你鸽子了, 改天请你吃饭赔罪")

	if owed, _ := e.Store.CompensationOwed(now); owed {
		t.Fatal("**说了找补却仍然欠着** —— 那 `amends_ts` 还是没写上")
	}
	if ok, why := e.Store.MayCommit(now); !ok {
		t.Fatalf("找补之后**约定权没有解锁**: %s", why)
	}
}

// TestAmends_NotOwedWritesNothing (反面)
//
// ⚠️ 不欠的时候说"改天请你"只是**客气话**, 不许写任何一行。
func TestAmends_NotOwedWritesNothing(t *testing.T) {
	e, _ := amendsEngine(t)
	// 一次毁约都还没有。
	e.noteAmendsIfAny("改天请你吃饭")
	var n int
	if err := e.Store.DB.QueryRow("SELECT COUNT(*) FROM convo_appointment WHERE amends_ts <> ''").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("不欠补偿却写了 %d 行找补 —— 那是把客气话当成了账", n)
	}
}

// TestAmends_MarkersAreNarrow (反面)
//
// ⚠️ 句式表**刻意收得窄** —— 它决定的是"解开主动约定权"。
// 认错了一次不该认的, 那份"欠着"就白欠了。
func TestAmends_MarkersAreNarrow(t *testing.T) {
	for _, s := range []string{"不好意思", "抱歉", "对不起", "我错了"} {
		if hits := AmendsHits(s); len(hits) != 0 {
			t.Fatalf("通用客套 %q 被当成了找补: %v", s, hits)
		}
	}
	// 而真正的补偿承诺**必须认出来**。
	for _, s := range []string{"改天请你吃饭", "算我欠你一次", "下次请你喝奶茶"} {
		if hits := AmendsHits(s); len(hits) == 0 {
			t.Fatalf("真正的找补 %q 没认出来", s)
		}
	}
}

// TestAmends_OnlyForBreached (反面)
//
// ⚠️ `MarkAmends` 自带的护栏: **只给已毁约/已忘的**找补 ——
// 赴约过的约定"找补"没有意义, 写上去等于凭空造一条账。
func TestAmends_OnlyForBreached(t *testing.T) {
	e, now := amendsEngine(t)
	kept := agSeedSettled(t, e.Store, ApptKept, "去了", now.Add(-24*time.Hour), "")
	// ⚠️ 它**报错**, 不是静默跳过 —— 那是对的:
	// 静默跳过会让"调用方传错了 id"与"这条本来就不该找补"长得一模一样
	// (P7: 静默的不是数据库, 是调用方)。
	if err := e.Store.MarkAmends(kept, now); err == nil {
		t.Fatal("**赴约过的约定竟然允许找补** —— 那条护栏没生效")
	}
	var got string
	if err := e.Store.DB.QueryRow("SELECT COALESCE(amends_ts,'') FROM convo_appointment WHERE id=?", kept).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("**赴约过的约定被写上了找补**: %q —— 那条护栏没生效", got)
	}
}
