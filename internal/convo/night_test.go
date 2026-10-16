package convo

import (
	"testing"
	"time"

	"github.com/tzhnahida/convo/internal/clock"
)

// ——— 时间格式: 这条是本批最要紧的**回归尺子** ———
//
// 生产写库用的是 clock.TodayTS() (bodyTSLayout), 而本批新加的读取代码原先按
// RFC3339 解析 —— 那会**静默返回 false**, 判据④ 永不触发, 而测试自己插 RFC3339 时全绿。
// 所以这条测试**直接用真实写入器**, 不自己拼一个"看起来像"的格式。
func TestParseMsgTS_ReadsTheRealWriterFormat(t *testing.T) {
	if _, ok := ParseMsgTS(clock.TodayTS()); !ok {
		t.Fatalf("真实写入格式 %q 解析不了 —— 判据④ 会静默永不触发", clock.TodayTS())
	}
	if _, ok := ParseMsgTS(time.Now().Format(time.RFC3339)); !ok {
		t.Fatal("RFC3339 也要认 (兼容测试夹具与历史行)")
	}
	if _, ok := ParseMsgTS(""); ok {
		t.Fatal("空串不许解析成功")
	}
	if _, ok := ParseMsgTS("昨天下午"); ok {
		t.Fatal("垃圾输入不许解析成功 —— 静默当成零时刻会让冷场判定疯狂触发")
	}
}

func TestLastSceneMessageAt_ReadsRealInsertedRows(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	if _, err := e.Store.InsertMessageFull("user", "qq", "text", "上一条真话",
		MsgMeta{Origin: OriginUser, SceneKey: "group:77", SenderUIN: "11"}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := LastSceneMessageAtExcluding(e.Store.DB, "group:77", "不是这条", time.Now())
	if err != nil || !ok {
		t.Fatalf("真实写入的行读不回来 (ok=%v err=%v) —— 时间格式不一致", ok, err)
	}
	if time.Since(got) > time.Minute || time.Until(got) > time.Minute {
		t.Fatalf("解析出来的时刻明显不对: %v (now=%v)", got, time.Now())
	}
}

// ——— 夜间批三步 ———

func nightEngine(t *testing.T) (*Engine, string, time.Time) {
	t.Helper()
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	// 选一个 NightKeyOf 一定等于今天的时刻 (hour >= 12 → 归属当天)。
	now := time.Now()
	at := time.Date(now.Year(), now.Month(), now.Day(), 20, 0, 0, 0, time.Local)
	return e, NightKeyOf(at), at
}

func TestNightBatch_FoldsGroupDayIntoOneMemory(t *testing.T) {
	e, night, at := nightEngine(t)
	for _, s := range []string{"猫粮买了吗", "猫又胖了", "考试周真难熬", "考试要挂"} {
		if _, err := e.Store.InsertMessageFull("user", "qq", "text", s,
			MsgMeta{Origin: OriginUser, SceneKey: "group:88", SenderUIN: "11"}); err != nil {
			t.Fatal(err)
		}
	}
	e.prepareDailyJobs(at)
	// ⚠️ 直接读库: `LoadFold` 已删 (见 newmods_test.go 的说明)。
	var foldTotal int
	var foldTopics, foldText string
	if err := e.Store.DB.QueryRow("SELECT total, topics, text FROM convo_fold WHERE owner=? AND scene_key=? AND day=?",
		e.ownerKey(), "group:88", night).Scan(&foldTotal, &foldTopics, &foldText); err != nil {
		t.Fatalf("日终折叠没有落库: %v", err)
	}
	res := FoldResult{Total: foldTotal, Text: foldText}
	if res.Total != 4 {
		t.Fatalf("应当折进 4 条, 实际 %d", res.Total)
	}
	if foldTopics == "" {
		t.Fatal("自带话题抽取没抽到任何话题")
	}
	// 折出来的一条必须进记忆, 以 ai_to_ai 落库 (L1 会把它过滤掉)。
	if _, ok, _ := LastSceneMessageAtExcluding(e.Store.DB, "group:88", "不是这条", time.Now()); !ok {
		t.Fatal("折叠结果没有以消息形式落库")
	}
	// 幂等: 再跑一遍不许改 (夜间批重跑是常态)。
	e.Store.DB.Exec("DELETE FROM convo_counters WHERE name = ?", "night_batch")
	e.prepareDailyJobs(at)
	// ⚠️ 直接读库: `LoadFold` 于 2026-10-16 删除 —— 折叠写完就**就地**注入
	// (convo.go 那一块用的是刚算出来的 res), 全库没有"事后读回"的消费者。
	// 而这条断言要的只是"库里那一行没被重跑改掉", 一条 SELECT 就够。
	var keptText string
	if err := e.Store.DB.QueryRow("SELECT text FROM convo_fold WHERE owner=? AND scene_key=? AND day=?",
		e.ownerKey(), "group:88", night).Scan(&keptText); err != nil {
		t.Fatalf("折叠行读不回来: %v", err)
	}
	if keptText != res.Text {
		t.Fatalf("重跑改了折叠结果:\n%q\n%q", res.Text, keptText)
	}
}

// §4.4.1b: 语域只统计**它自己**的话。
func TestNightBatch_RegisterSnapshotUsesOnlyItsOwnWords(t *testing.T) {
	e, night, at := nightEngine(t)
	for i := 0; i < 12; i++ {
		if _, err := e.Store.InsertMessageFull("user", "qq", "text", "用户说的很长很长很长很长很长很长很长的一句话啊哈哈",
			MsgMeta{Origin: OriginUser, SceneKey: "private:1", SenderUIN: "1"}); err != nil {
			t.Fatal(err)
		}
		if _, err := e.Store.InsertMessageFull("ai", "qq", "text", "嗯",
			MsgMeta{Origin: OriginAIToUser, SceneKey: "private:1"}); err != nil {
			t.Fatal(err)
		}
	}
	e.prepareDailyJobs(at)
	v, err := LoadRegisterBaseline(e.Store.DB, e.ownerKey(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if v.N == 0 {
		t.Fatal("语域快照没有落库")
	}
	if v.AvgRunes > 5 {
		t.Fatalf("均句长 %.1f —— 把用户的话算进去了 (它自己只说了「嗯」)", v.AvgRunes)
	}
	_ = night
}

// §4.4.6: 失眠由**当天发生的事**决定, 不是骰子。
func TestNightBatch_InsomniaComesFromCauses(t *testing.T) {
	e, _, at := nightEngine(t)
	// 半夜有人找 (第二条夜消息用凌晨时刻插入)。
	dayStart := time.Date(at.Year(), at.Month(), at.Day(), 2, 0, 0, 0, time.Local)
	for i := 0; i < 2; i++ {
		if _, err := e.Store.DB.Exec("INSERT INTO convo_messages (ts, dir, channel, kind, payload_json, origin, scene_key, sender_uin) VALUES (?,?,?,?,?,?,?,?)",
			dayStart.Format(bodyTSLayout), "in", "qq", "text", "睡不着", "user", "private:1", "1"); err != nil {
			t.Fatal(err)
		}
	}
	e.prepareDailyJobs(at)
	st, err := LoadSleepPoint(e.Store.DB, e.ownerKey(), NightKeyOf(at))
	if err != nil {
		t.Fatal(err)
	}
	if st.Streak == 0 {
		t.Fatal("半夜被找了两条却没有判成失眠 —— 失眠又退回随机数了")
	}
	if st.OffsetMin >= 0 {
		t.Fatalf("失眠之后没有自我修正 (偏移 %d)", st.OffsetMin)
	}
}

// 护栏的持久化承载者: 周额度不落库, 四条护栏里两条是装饰。
func TestSleepPoint_WeekRolloverAndCaps(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	owner := e.ownerKey()
	monday := "2026-03-02" // 周一
	if WeekStartOf(monday) != monday {
		t.Fatalf("WeekStartOf 算错了: %s", WeekStartOf(monday))
	}
	if WeekStartOf("2026-03-08") != monday {
		t.Fatalf("周日应归入同一周: %s", WeekStartOf("2026-03-08"))
	}
	st := SleepPointState{Day: monday}
	for i := 0; i < 4; i++ {
		d := monday
		if i > 0 {
			d = "2026-03-0" + itoa(i+2)
		}
		next, err := AdjustSleepPoint(st, -30, 480, "没睡好", d)
		if err != nil {
			t.Fatalf("第 %d 次调整被拦: %v", i+1, err)
		}
		st = next
	}
	if st.WeekUsedMin != 120 {
		t.Fatalf("周额度应当是 120, 实际 %d", st.WeekUsedMin)
	}
	if _, err := AdjustSleepPoint(st, -30, 480, "没睡好", "2026-03-07"); err != ErrSleepPointWeekTooBig {
		t.Fatalf("周额度用满后必须拒绝, 得到 %v", err)
	}
	if err := SaveSleepPoint(e.Store.DB, owner, "2026-03-07", st); err != nil {
		t.Fatal(err)
	}
	back, err := LoadSleepPoint(e.Store.DB, owner, "2026-03-07")
	if err != nil || back.WeekUsedMin != 120 {
		t.Fatalf("同周读回应当保留额度: %+v err=%v", back, err)
	}
	next, err := LoadSleepPoint(e.Store.DB, owner, "2026-03-09") // 下一周
	if err != nil {
		t.Fatal(err)
	}
	if next.WeekUsedMin != 0 {
		t.Fatalf("跨周必须归零, 否则额度用满后**永远**卡死: %+v", next)
	}
}

// body.go 的纯随机失眠必须已经拆掉。
func TestBodyOnset_NoRandomInsomnia(t *testing.T) {
	// 让骰子必然命中旧分支 (Insomnia < 5), 若代码还在, 就会返回失眠。
	for i := 0; i < 200; i++ {
		st, ok := BodyOnsetFromEnv(EnvDay{}, BodyRoll{Insomnia: 0}, time.Now())
		if ok && st.Kind == BodyInsomnia {
			t.Fatal("失眠仍然由随机骰子决定 —— 设计稿 L687 要求它是当天发生的事的结果")
		}
	}
}
