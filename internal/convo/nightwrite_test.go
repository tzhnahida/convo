package convo

import (
	"testing"

	"github.com/tzhnahida/VocabLock/internal/clock"
)

// ─────────────────────────────────────────────────────────────
// §4.4.5 **一夜的读数** —— 而在此之前没有任何东西写过 `convo_night`
//
// ⚠️ 后果是一条**整节机制从来没生效过**:
//
// `sleepEffect()`(接了的)读 `LastNight()` 永远拿不到行
// ⇒ 每次落回默认值 ⇒ 「**昨夜没睡好 → 今天回得慢一点、话少一点**」
// (DelayFactor / LengthFactor / QuotaDelta 三个杠杆)**一次都没生效过**。
//
// ⚠️ 而它和前面几轮撞到的是同一类 —— **消费者接上了、生产者不存在**。
// ─────────────────────────────────────────────────────────────

// TestNight_OneRowAfterTheBatch (本条最重要)
//
// ⚠️ 它钉的是**整条链**, 不是"`SaveNight` 写得对不对":
// **批跑过之后, 睡眠质量结算必须拿得到那一夜。**
func TestNight_OneRowAfterTheBatch(t *testing.T) {
	// ⚠️ **用真实时钟当批的时刻** —— `sleepEffect()` 读的是 `clock.Now()`，
	// 而两边的归属日 (NightKeyOf) 必须落在同一天，否则这条用例测的是夹具。
	// (第一版用 nightEngine 的 20:00，而跑测试时是凌晨 ⇒ 归属日差一天 ⇒ 假红一次。)
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	at := clock.Now()
	if err := EnsureNightSchema(e.Store); err != nil {
		t.Fatal(err)
	}
	// 先钉基线: 还没跑批时**不该**有行。
	if _, ok, err := e.Store.LastNight(); err != nil || ok {
		t.Fatalf("还没跑批就有夜里的读数 (ok=%v err=%v)", ok, err)
	}

	e.prepareDailyJobs(at)

	_, ok, err := e.Store.LastNight()
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("**跑完一夜的批, 却没有一行夜里的读数** —— " +
			"那 `sleepEffect()` 永远拿默认值, §4.4.5 整节不生效")
	}

	// ⚠️ 而**读取口也必须跟着变** —— 只写库不改读数等于没接。
	fx := e.sleepEffect()
	if !fx.Active {
		t.Fatal("**睡眠质量结算说「没有读数」** —— 那条链还是断的")
	}
}
