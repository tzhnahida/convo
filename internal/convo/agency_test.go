package convo

// ——— §5.5 主体性: 反面用例优先 ———
//
// 每条"反面"用例的注释都写了**改坏成什么会让它红** —— 它们是契约的钉子, 不是覆盖率。

import (
	"strings"
	"testing"
	"time"
)

// ——— 测试辅助 (ag 前缀, 避免与既有测试的同名 helper 冲突) ———

func agTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(t.TempDir(), "agency-test.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := EnsureAgencySchema(s); err != nil {
		s.Close()
		t.Fatalf("ensure agency schema: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func agDay(d, hh int) time.Time {
	return time.Date(2026, 5, d, hh, 0, 0, 0, time.Local)
}

func agHas(list []string, want string) bool {
	for _, x := range list {
		if x == want {
			return true
		}
	}
	return false
}

// agSeedSettled 直接往 ledger 写一条**已结算事实** (绕过所有方法 —— 派生量的测试必须这样写)。
func agSeedSettled(t *testing.T, s *Store, terminal, reason string, at time.Time, amendsTS string) int64 {
	t.Helper()
	res, err := s.DB.Exec("INSERT INTO convo_appointment "+
		"(tier, who, scene, text, created_ts, due_ts, terminal, terminal_ts, reason, amends_ts) "+
		"VALUES (?,?,?,?,?,?,?,?,?,?)",
		"B", int64(10001), "qq", "一起吃饭", agTS(at), agTS(at), terminal, agTS(at), reason, amendsTS)
	if err != nil {
		t.Fatalf("seed settled: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

// ═══════════════════════════════════════════════════════════════════
// 一、三层权限
// ═══════════════════════════════════════════════════════════════════

func TestClassifyRequest_ThreeTiers(t *testing.T) {
	cases := []struct {
		text string
		want RequestTier
	}{
		{"救命 我在医院", TierSafety},
		{"借我点钱周转一下", TierSafety},
		{"提醒我八点吃药", TierSafety}, // C 压 B: 健康/用药不许被"约定"降级
		{"说好明天陪我吃饭吗", TierPromise},
		{"你喜欢吃辣吗", TierStance},
		{"陪我聊聊呗", TierStance},
	}
	for _, c := range cases {
		if got := ClassifyRequest(c.text, nil); got != c.want {
			t.Errorf("ClassifyRequest(%q) = %s, 想要 %s", c.text, got, c.want)
		}
	}
}

// 🔴 反面: 判不准 → **TierSafety** (判不准就顺从, M20-① 不许随机违抗)。
// 改坏成: 兜底 return TierStance → 这条立刻红 —— 每一次判据漏词都会变成一次撒野许可。
func TestClassifyRequest_UnknownFallsBackToSafety(t *testing.T) {
	for _, text := range []string{"", "   ", "哦。", "zzz", "……", "!!!"} {
		if got := ClassifyRequest(text, nil); got != TierSafety {
			t.Fatalf("ClassifyRequest(%q) = %s —— 判不准必须回落到 TierSafety(顺从), 兜底不许给违抗权", text, got)
		}
	}
}

// 🔴 反面: B(约定) 必须先于 A(普通对话) 判定。
// 改坏成: 先判 A → "说好明天陪我吃饭吗" 会降级成 TierStance → 红。
// 后果不是小事: 同一句话在 A 层可以**免费**违抗, 那就是 M8/M20-④ 的无代价毁约。
func TestClassifyRequest_PromiseBeatsStance(t *testing.T) {
	if got := ClassifyRequest("说好明天陪我吃饭吗", nil); got != TierPromise {
		t.Fatalf("同时命中 A 与 B 时必须判 B(有代价的那层), 得到 %s", got)
	}
}

// 🔴 反面: 调用方传**旧表**(含"在吗")也不许把敲门语抬进 C 层 (C1 / M5 硬线)。
// 改坏成: 去掉 hitUrgent 里的 agPingWords 过滤 → "在吗" 变 TierSafety → 红。
func TestClassifyRequest_OldUrgentTableDoesNotPromotePing(t *testing.T) {
	old := []string{"救命", "急事", "很急", "紧急", "在吗", "在么", "help"}
	got := ClassifyRequest("在吗", old)
	if got == TierSafety {
		t.Fatal("'在吗' 被旧 urgent 表抬进 C 层了 —— 情绪宣泄不是求助 (M5), 且它会顶成 immediate+ActBreakFocus")
	}
	if got != TierStance {
		t.Fatalf("'在吗' 是普通搭话, 应该是 A 层, 得到 %s", got)
	}
}

// 🔴 反面: urgent 是**并集**, 不是替换 —— 零值(nil)/空表都必须保留收窄后的内置表。
// 改坏成: nil 当作"没有紧急词" → "急事" 掉出 C 层 → 红 (真求助被降级成可违抗)。
func TestClassifyRequest_UrgentIsAdditive(t *testing.T) {
	for _, extra := range [][]string{nil, {}, {"自定义词"}} {
		if got := ClassifyRequest("有急事找你", extra); got != TierSafety {
			t.Fatalf("extra=%v: '急事' 必须仍是 C 层, 得到 %s", extra, got)
		}
	}
	if got := ClassifyRequest("自定义词", []string{"自定义词"}); got != TierSafety {
		t.Fatalf("调用方补的词必须生效, 得到 %s", got)
	}
}

// 🔴 反面: C1 的收窄表本身。改坏成: 把 "在吗"/"在么" 加回 UrgentWordsNarrowed → 红。
func TestUrgentWordsNarrowed_DropsPingKeepsHelp(t *testing.T) {
	if agHas(UrgentWordsNarrowed, "在吗") || agHas(UrgentWordsNarrowed, "在么") {
		t.Fatal("收窄表里又有敲门语了 —— C1 会复发 (M5)")
	}
	if !agHas(UrgentWordsNarrowed, "救命") || !agHas(UrgentWordsNarrowed, "help") {
		t.Fatal("收窄不等于删空: 真求助词必须留着")
	}
}

// 🔴 反面: 三层判定不许有随机成分 (与 DecideDefiance/DecideBreach 同一条纪律)。
// 改坏成: 任何概率/随机分支 → 200 次里必然出现不一致。
func TestClassifyRequest_DeterministicNotRandom(t *testing.T) {
	const text = "说好明天陪我吃饭吗"
	want := ClassifyRequest(text, nil)
	for i := 0; i < 200; i++ {
		if got := ClassifyRequest(text, nil); got != want {
			t.Fatalf("第 %d 次给出了不同的层: %s vs %s", i, got, want)
		}
	}
}

// ═══════════════════════════════════════════════════════════════════
// 二、立场
// ═══════════════════════════════════════════════════════════════════

func agPersonaWithViews(views string) *PersonaProfile {
	return &PersonaProfile{Name: "小夜", Attrs: map[string]string{"views": views}}
}

func TestStancesOf_ParsesViews(t *testing.T) {
	st := StancesOf(agPersonaWithViews("不喜欢吃香菜；讨厌早起；喜欢下雨天"))
	if len(st) != 3 {
		t.Fatalf("应该解析出 3 条立场, 得到 %d: %+v", len(st), st)
	}
	if st[0].Kind != StanceDislike || st[0].Key != "吃香菜" {
		t.Fatalf("第一条应是 dislike/吃香菜, 得到 %s/%s", st[0].Kind, st[0].Key)
	}
	if st[2].Kind != StancePreference {
		t.Fatalf("第三条应是 preference, 得到 %s", st[2].Kind)
	}
}

// 🔴 反面: 极性不许被抹掉。"我最喜欢吃香菜" 不能命中 dislike(不喜欢吃香菜)。
// 改坏成: 让 Key 也参与子串匹配 / 关键词只留"香菜" → 这条立刻红。
func TestStanceHit_PolarityIsKeptApart(t *testing.T) {
	st := StancesOf(agPersonaWithViews("不喜欢吃香菜"))
	if _, ok := StanceHit(st, "", "我不喜欢吃香菜"); !ok {
		t.Fatal("带极性的原句应该命中")
	}
	if got, ok := StanceHit(st, "", "我最喜欢吃香菜了"); ok {
		t.Fatalf("反对的立场被'喜欢'命中了 (极性被抹掉): %+v", got)
	}
	// topicKey 是"同一件事"的精确对齐, 这时才允许不带极性的说法对上
	if _, ok := StanceHit(st, "吃香菜", ""); !ok {
		t.Fatal("topicKey 精确命中 Key 应该算有依据")
	}
}

// 🔴 反面: 没有立场 = 没有依据。改坏成: 空表/空 persona 也返回 true → 红
// (那正是"随机逆反"的形状 —— M14 说违抗必须有依据)。
func TestStanceHit_NoEvidenceIsFalse(t *testing.T) {
	if _, ok := StanceHit(nil, "吃香菜", "随便什么文本"); ok {
		t.Fatal("空立场表不许命中")
	}
	if _, ok := StanceHit(StancesOf(nil), "", "在吗"); ok {
		t.Fatal("nil persona 不许命中")
	}
	if _, ok := StanceHit(StancesOf(&PersonaProfile{}), "吃香菜", ""); ok {
		t.Fatal("没有 views 的 persona 不许命中")
	}
	if _, ok := StanceHit(StancesOf(agPersonaWithViews("不喜欢吃香菜")), "", ""); ok {
		t.Fatal("空文本不许命中 (没有『这件事』可言)")
	}
}

// 🔴 反面: 命中要取**最长关键词** (确定性), 不是"表里第一条"。
// 改坏成: 首个命中就返回 → 会拿到短的"香菜"(view) 而不是"不喜欢吃香菜"(dislike) → 红。
func TestStanceHit_LongestKeywordWins(t *testing.T) {
	st := StancesOf(agPersonaWithViews("香菜；不喜欢吃香菜"))
	got, ok := StanceHit(st, "", "我不喜欢吃香菜")
	if !ok {
		t.Fatal("应该命中")
	}
	if got.Kind != StanceDislike {
		t.Fatalf("应取最长的关键词(带极性的那条), 得到 %+v", got)
	}
}

// ═══════════════════════════════════════════════════════════════════
// 三、违抗裁决
// ═══════════════════════════════════════════════════════════════════

func TestDecideDefiance_StanceAllowsRefuseAndStyleGivesTalkBack(t *testing.T) {
	base := DefianceInput{Tier: TierStance, TopicKey: "吃香菜", HasStance: true}
	if got := DecideDefiance(base, SchedulerConfig{}); got.Mode != DefyRefuse {
		t.Fatalf("StyleTier 未提供 → 默认档应是 ③直说拒绝, 得到 %v (%s)", got.Mode, got.Reason)
	}
	base.StyleTier = DefyTalkBack
	if got := DecideDefiance(base, SchedulerConfig{}); got.Mode != DefyTalkBack {
		t.Fatalf("有立场 + 损友人格 → ④顶回去, 得到 %v (%s)", got.Mode, got.Reason)
	}
}

// 🔴 反面: 没有依据 → 不许违抗 (M14/M20-①)。人格再"损"也不行。
// 改坏成: 默认给一档 (哪怕是最低的沉默) → 红。
func TestDecideDefiance_NoEvidenceMeansNone(t *testing.T) {
	in := DefianceInput{Tier: TierStance, TopicKey: "随便", StyleTier: DefyTalkBack}
	if got := DecideDefiance(in, SchedulerConfig{}); got.Mode != DefyNone {
		t.Fatalf("立场/心情/身体都没命中时不许违抗, 得到 %v (%s)", got.Mode, got.Reason)
	}
}

// 🔴 反面: 依据给**上界** —— 只是心情低(没有立场)时不许顶回去。
// 改坏成: 去掉 ceiling 夹紧 → 心情低 + 损友人格 = ④顶回去 → 红 (那是没有立场的顶嘴)。
func TestDecideDefiance_EvidenceCapsTheMode(t *testing.T) {
	in := DefianceInput{Tier: TierStance, MoodLow: true, StyleTier: DefyTalkBack}
	if got := DecideDefiance(in, SchedulerConfig{}); got.Mode > DefyRefuse {
		t.Fatalf("没有立场支撑时最多 ③直说拒绝, 得到 %v", got.Mode)
	}
	body := DefianceInput{Tier: TierStance, BodyActive: true, StyleTier: DefyTalkBack}
	if got := DecideDefiance(body, SchedulerConfig{}); got.Mode != DefyDelay {
		t.Fatalf("身体不适是事实不是意见 → 最多 ②拖延, 得到 %v", got.Mode)
	}
}

// 🔴 反面: **同一件事连续违抗必须封顶, 且封顶后是"恢复正常"** ——
// 不是降一档继续杠 (那它就会对你永久杠上)。
// 改坏成: 超限时返回 DefySilent(或任何非 None) → 红。
func TestDecideDefiance_StreakCapRestoresNormal(t *testing.T) {
	in := DefianceInput{Tier: TierStance, TopicKey: "吃香菜", HasStance: true,
		StyleTier: DefyTalkBack, StreakCount: 2, MaxStreak: 2}
	got := DecideDefiance(in, SchedulerConfig{})
	if got.Mode != DefyNone {
		t.Fatalf("到上限必须恢复正常(DefyNone), 得到 %v (%s)", got.Mode, got.Reason)
	}
	if !strings.Contains(got.Reason, "上限") {
		t.Fatalf("归因里必须写明是上限拦下的: %s", got.Reason)
	}
	in.StreakCount = 1 // 边界: 还差一次, 仍然允许
	if got := DecideDefiance(in, SchedulerConfig{}); got.Mode != DefyTalkBack {
		t.Fatalf("未到上限时应该照常违抗, 得到 %v", got.Mode)
	}
}

// 🔴 反面: 上限的零值 = 未提供 → 回落常量, **不是**"上限 0 = 永远不许违抗"。
// 改坏成: 直接拿 in.MaxStreak 比 (0 时 streak>=0 恒真 → 永不违抗) → 红。
func TestDecideDefiance_DefaultCapWhenUnset(t *testing.T) {
	in := DefianceInput{Tier: TierStance, HasStance: true, StyleTier: DefyRefuse, StreakCount: 1}
	if got := DecideDefiance(in, SchedulerConfig{}); got.Mode != DefyRefuse {
		t.Fatalf("MaxStreak 未提供时应回落到默认上限(2), 得到 %v (%s)", got.Mode, got.Reason)
	}
	in.StreakCount = 2
	if got := DecideDefiance(in, SchedulerConfig{}); got.Mode != DefyNone {
		t.Fatalf("默认上限 2 到顶必须恢复正常, 得到 %v", got.Mode)
	}
}

// 🔴 反面: C 层永远不违抗 (M4/M12), 哪怕有立场、哪怕是损友人格。
// 改坏成: 先判依据再判层 (C 层也能走依据分支) → 红。
func TestDecideDefiance_SafetyTierNeverDefies(t *testing.T) {
	in := DefianceInput{Tier: TierSafety, TopicKey: "医院", HasStance: true,
		MoodLow: true, BodyActive: true, StyleTier: DefyTalkBack}
	if got := DecideDefiance(in, SchedulerConfig{}); got.Mode != DefyNone {
		t.Fatalf("C 层(安全与求助)不许违抗, 得到 %v (%s)", got.Mode, got.Reason)
	}
}

// 🔴 反面: B 层的违抗是**毁约**, 必须走 DecideBreach + ledger 记账。
// 改坏成: 把 B 层并进 A 层 (按依据出档) → 红 —— 那一档是"没有代价的毁约"(M8/M20-④)。
func TestDecideDefiance_PromiseTierGoesToBreachNotDefiance(t *testing.T) {
	in := DefianceInput{Tier: TierPromise, HasStance: true, MoodLow: true,
		BodyActive: true, StyleTier: DefyTalkBack}
	if got := DecideDefiance(in, SchedulerConfig{}); got.Mode != DefyNone {
		t.Fatalf("B 层不许在违抗档里出结论, 得到 %v (%s)", got.Mode, got.Reason)
	}
}

// 🔴 反面: 层未提供 = 未提供 ≠ 极值 → 不违抗 (零值不是"可以违抗")。
// 改坏成: 把 "" 当作 A 层处理 → 红。
func TestDecideDefiance_MissingTierMeansNone(t *testing.T) {
	in := DefianceInput{TopicKey: "x", HasStance: true, StyleTier: DefyTalkBack}
	if got := DecideDefiance(in, SchedulerConfig{}); got.Mode != DefyNone {
		t.Fatalf("权限层未提供时不许违抗, 得到 %v", got.Mode)
	}
}

// 🔴 反面: 违抗是**判据**, 不是概率。改坏成: 把任何率乘进档位 → 200 次里必然出现不一致。
func TestDecideDefiance_DeterministicNotRandom(t *testing.T) {
	in := DefianceInput{Tier: TierStance, TopicKey: "吃香菜", StreakCount: 1,
		HasStance: true, MoodLow: true, StyleTier: DefyTalkBack}
	want := DecideDefiance(in, SchedulerConfig{})
	if want.Mode != DefyTalkBack {
		t.Fatalf("全依据成立时应到 ④顶回去, 得到 %v (%s)", want.Mode, want.Reason)
	}
	for i := 0; i < 200; i++ {
		if got := DecideDefiance(in, SchedulerConfig{}); got != want {
			t.Fatalf("第 %d 次结果不同: %+v vs %+v", i, got, want)
		}
	}
}

// 🔴 反面: 人格模板查不到 → 0 = 未提供 → 走默认档, 不是"沉默"。
// 改坏成: 查不到返回 DefySilent → 红 (零值不能落进冷暴力那一档)。
func TestDefianceStyleFor_TemplatesAndUnknown(t *testing.T) {
	if got := DefianceStyleFor("troll"); got != DefyTalkBack {
		t.Fatalf("损友偏 ④, 得到 %v", got)
	}
	if got := DefianceStyleFor("mentor"); got != DefyRefuse {
		t.Fatalf("导师偏 ③, 得到 %v", got)
	}
	if got := DefianceStyleFor("没有这个模板"); got != DefyNone {
		t.Fatalf("查不到必须是 0(未提供), 得到 %v", got)
	}
}

func TestDefianceMode_StringRoundTrip(t *testing.T) {
	for _, m := range []DefianceMode{DefySilent, DefyDelay, DefyRefuse, DefyTalkBack} {
		if got := ParseDefianceMode(m.String()); got != m {
			t.Fatalf("%v 往返失败, 得到 %v", m, got)
		}
	}
	if got := ParseDefianceMode("乱写的"); got != DefyNone {
		t.Fatalf("认不出的档位必须回落 DefyNone, 得到 %v", got)
	}
	if DefyNone.String() != "none" {
		t.Fatalf("零值必须是 none, 得到 %s", DefyNone.String())
	}
}

func TestDefianceHits_RecognizesSentText(t *testing.T) {
	if got := DefianceHits("等下再说吧"); len(got) == 0 {
		t.Fatal("②拖延 应该被认出来")
	}
	if got := DefianceHits("我不想聊这个"); len(got) == 0 {
		t.Fatal("③直说拒绝 应该被认出来")
	}
	if got := DefianceHits("凭什么啊"); len(got) == 0 {
		t.Fatal("④顶回去 应该被认出来")
	}
	if got := DefianceHits(""); got != nil {
		t.Fatalf("空文本不该认出任何档: %v", got)
	}
}

// ═══════════════════════════════════════════════════════════════════
// 四、约定 / ledger
// ═══════════════════════════════════════════════════════════════════

// 🔴 反面: 建表必须幂等 (主控会在迁移链里调; 老库可能已经调过一次)。
// 改坏成: 去掉 IF NOT EXISTS → 第二次调用报错 → 红。
func TestEnsureAgencySchema_Idempotent(t *testing.T) {
	s := agTestStore(t)
	if err := EnsureAgencySchema(s); err != nil {
		t.Fatalf("第二次建表必须不报错: %v", err)
	}
	var n int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM convo_appointment").Scan(&n); err != nil {
		t.Fatalf("建表后表必须可用: %v", err)
	}
}

func TestAppointment_RoundTripAndTierDefault(t *testing.T) {
	s := agTestStore(t)
	now := agDay(10, 18)
	due := agDay(10, 20)

	id, err := s.CreateAppointment(Appointment{
		Tier: "", Who: 10001, Scene: "qq", Text: "周五一起吃饭",
		DueTS: agTS(due), Reason: "说好了",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	pending, err := s.PendingAppointments(now)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("未到期应有 1 条 pending, 得到 %d", len(pending))
	}
	a := pending[0]
	if a.ID != id || a.Who != 10001 || a.Scene != "qq" || a.Text != "周五一起吃饭" {
		t.Fatalf("字段没保全: %+v", a)
	}
	if a.Tier != TierPromise {
		t.Fatalf("Tier 未提供 → 入库按 B 层, 得到 %q", a.Tier)
	}
	if a.CreatedTS == "" {
		t.Fatal("CreatedTS 零值应由注入时钟盖章")
	}
	if a.Terminal != ApptOpen || a.Amends {
		t.Fatalf("新约定必须是未结算且未找补: %+v", a)
	}
	if dueList, _ := s.DueAppointments(now); len(dueList) != 0 {
		t.Fatalf("还没到期不该进 due: %d", len(dueList))
	}
	if dueList, _ := s.DueAppointments(agDay(10, 21)); len(dueList) != 1 {
		t.Fatalf("到期后应进 due: %d", len(dueList))
	}
	if err := s.SettleAppointment(id, ApptKept, "", due); err != nil {
		t.Fatalf("settle kept: %v", err)
	}
	if dueList, _ := s.DueAppointments(agDay(10, 21)); len(dueList) != 0 {
		t.Fatal("结算后不该再出现在 due 里")
	}
}

// 🔴 反面: Pending 与 Due 必须是**划分** (不重叠, 并集 = 未结算), Pending 必须看 now。
// 改坏成: Pending 忽略 now (返回全部未结算) → 红。
func TestAppointment_PendingAndDuePartition(t *testing.T) {
	s := agTestStore(t)
	now := agDay(10, 18)
	mk := func(due time.Time) int64 {
		dueTS := ""
		if !due.IsZero() {
			dueTS = agTS(due) // 零值 = 没定时刻: 必须是空串, 不是 "0001-01-01 ..."
		}
		id, err := s.CreateAppointment(Appointment{Tier: TierPromise, Who: 1, Scene: "qq", Text: "x", DueTS: dueTS})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	past := mk(agDay(10, 17))
	future := mk(agDay(11, 17))
	noDue := mk(time.Time{})
	settled := mk(agDay(10, 16))
	if err := s.SettleAppointment(settled, ApptKept, "", agDay(10, 17)); err != nil {
		t.Fatal(err)
	}
	pending, _ := s.PendingAppointments(now)
	due, _ := s.DueAppointments(now)
	seen := map[int64]int{}
	for _, a := range pending {
		seen[a.ID]++
	}
	for _, a := range due {
		seen[a.ID]++
	}
	if seen[past] != 1 || seen[future] != 1 || seen[noDue] != 1 {
		t.Fatalf("未结算的三条必须各出现在且只出现在一侧: %v", seen)
	}
	if seen[settled] != 0 {
		t.Fatalf("已结算的不许出现在任何一侧: %v", seen)
	}
	if len(due) != 1 || len(pending) != 2 {
		t.Fatalf("划分错了: due=%d pending=%d", len(due), len(pending))
	}
}

// 🔴 反面: 事实不许改写 —— 重复结算会污染 ledger (代价与率都由它算)。
// 改坏成: 用 UPSERT 覆盖 (无 terminal=” 条件) → 红。
func TestSettleAppointment_RejectsDoubleSettle(t *testing.T) {
	s := agTestStore(t)
	now := agDay(10, 12)
	id, _ := s.CreateAppointment(Appointment{Tier: TierPromise, Who: 1, Scene: "qq", Text: "x", DueTS: agTS(now)})
	if err := s.SettleAppointment(id, ApptKept, "", now); err != nil {
		t.Fatalf("首次结算应成功: %v", err)
	}
	if err := s.SettleAppointment(id, ApptBreached, "改口", now); err == nil {
		t.Fatal("已结算的约定不许被二次结算 (事实不可改写)")
	}
	if err := s.SettleAppointment(99999, ApptKept, "", now); err == nil {
		t.Fatal("不存在的 id 必须报错")
	}
}

// 🔴 反面: 毁约必须写理由 (M7: 写不下来就归因不了), 且认不出的终态必须报错。
// 改坏成: 空理由也放行 → 红 (那就回到 C3 的静默结案)。
func TestSettleAppointment_RequiresReasonAndKnownTerminal(t *testing.T) {
	s := agTestStore(t)
	now := agDay(10, 12)
	id, _ := s.CreateAppointment(Appointment{Tier: TierPromise, Who: 1, Scene: "qq", Text: "x", DueTS: agTS(now)})
	if err := s.SettleAppointment(id, ApptBreached, "  ", now); err == nil {
		t.Fatal("故意毁约不给理由必须报错")
	}
	if err := s.SettleAppointment(id, "done", "乱写的终态", now); err == nil {
		t.Fatal("四态之外的终态必须报错")
	}
	if err := s.SettleAppointment(id, ApptOpen, "", now); err == nil {
		t.Fatal("空终态不是结论")
	}
	// 失败不许留下半截状态
	if due, _ := s.DueAppointments(agDay(10, 13)); len(due) != 1 {
		t.Fatalf("报错后约定必须仍是未结算: %d", len(due))
	}
}

func TestMarkAmends_OnlyForBreachOrForget(t *testing.T) {
	s := agTestStore(t)
	now := agDay(10, 12)
	open, _ := s.CreateAppointment(Appointment{Tier: TierPromise, Who: 1, Scene: "qq", Text: "x", DueTS: agTS(now)})
	if err := s.MarkAmends(open, now); err == nil {
		t.Fatal("没毁约就不许找补")
	}
	kept, _ := s.CreateAppointment(Appointment{Tier: TierPromise, Who: 1, Scene: "qq", Text: "x", DueTS: agTS(now)})
	if err := s.SettleAppointment(kept, ApptKept, "", now); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkAmends(kept, now); err == nil {
		t.Fatal("赴约的约定不许被标成'找补过'")
	}
	bad, _ := s.CreateAppointment(Appointment{Tier: TierPromise, Who: 1, Scene: "qq", Text: "x", DueTS: agTS(now)})
	if err := s.SettleAppointment(bad, ApptBreached, "临时有事", now); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkAmends(bad, now); err != nil {
		t.Fatalf("毁约后找补应该成功: %v", err)
	}
	list, _ := s.agSettledSince(now, 0)
	for _, a := range list {
		if a.ID == bad {
			if !a.Amends || a.AmendsTS == "" {
				t.Fatalf("找补是事实(时刻), Amends 是它的现算视图: %+v", a)
			}
		}
	}
}

// ═══════════════════════════════════════════════════════════════════
// 五、派生量: 现算, 不落库
// ═══════════════════════════════════════════════════════════════════

// 🔴 反面: 信用/率必须**从 ledger 现算** —— 手改事实后当次调用就得变。
// 改坏成: 把 reliability/breach_count 存成一列 (或加缓存) → 红。
func TestReliability_RecomputedFromLedger(t *testing.T) {
	s := agTestStore(t)
	now := agDay(20, 12)
	r0, err := s.Reliability(now, 0)
	if err != nil {
		t.Fatal(err)
	}
	if r0 != 1.0 {
		t.Fatalf("空 ledger 是满信用: %v", r0)
	}
	agSeedSettled(t, s, ApptBreached, "不想去", agDay(19, 12), "")
	r1, _ := s.Reliability(now, 0)
	if r1 != 0.0 {
		t.Fatalf("一次毁约后信用应为 0, 得到 %v", r1)
	}
	agSeedSettled(t, s, ApptKept, "", agDay(20, 11), "")
	r2, _ := s.Reliability(now, 0)
	if r2 != 0.5 {
		t.Fatalf("1 毁 1 赴 → 0.5, 得到 %v", r2)
	}
	// 档位表 (不是连续缩放): 用**同样的样本数**才比得出来 —— 均值会被样本数冲淡。
	mean := func(second string) float64 {
		st := agTestStore(t)
		agSeedSettled(t, st, ApptBreached, "不想去", agDay(19, 12), "")
		agSeedSettled(t, st, second, "另一个终态", agDay(20, 10), "")
		v, err := st.Reliability(now, 0)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	breachMean := mean(ApptBreached)  // 0 + 0   → 0
	forgotMean := mean(ApptForgot)    // 0 + 0.25 → 0.125
	cancelMean := mean(ApptCancelled) // 0 + 0.5  → 0.25
	if !(breachMean < forgotMean && forgotMean < cancelMean) {
		t.Fatalf("信用档位表错序: 毁约 %v < 忘了 %v < 取消 %v", breachMean, forgotMean, cancelMean)
	}
}

// 🔴 反面: 无样本 = 满信用。改坏成 "没有记录 → 0 信用" → 红
// (后果: 新关系一上来就 MayCommit=false, 它永远不能主动约)。
func TestReliability_NoSampleIsFullCredit(t *testing.T) {
	s := agTestStore(t)
	now := agDay(1, 12)
	r, err := s.Reliability(now, 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if r != 1.0 {
		t.Fatalf("没有履约记录 = 还没有失信, 必须满信用: %v", r)
	}
	if ok, why := s.MayCommit(now); !ok {
		t.Fatalf("空 ledger 不许锁死约定权: %s", why)
	}
	rate, _ := s.BreachRate(now, 30*24*time.Hour)
	if rate != 0 {
		t.Fatalf("无样本的毁约率是 0: %v", rate)
	}
}

// 🔴 反面: 窗口必须真的起作用 (M11 的 30 天滚动)。改坏成忽略 window → 红。
func TestBreachRate_Windowed(t *testing.T) {
	s := agTestStore(t)
	now := agDay(30, 12)
	agSeedSettled(t, s, ApptBreached, "不想去", agDay(29, 12), "")
	agSeedSettled(t, s, ApptKept, "", agDay(28, 12), "")
	agSeedSettled(t, s, ApptBreached, "不想去", agDay(1, 12), "") // 窗口外
	rate, err := s.BreachRate(now, 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if rate != 0.5 {
		t.Fatalf("窗口内 1 毁 1 赴 → 0.5 (窗口外那条不许算), 得到 %v", rate)
	}
	all, _ := s.BreachRate(now, 0)
	if all <= rate {
		t.Fatalf("全历史应包含窗口外那条: %v vs %v", all, rate)
	}
}

func TestBreachStreak_ConsecutiveBrokenByKept(t *testing.T) {
	s := agTestStore(t)
	now := agDay(20, 12)
	agSeedSettled(t, s, ApptBreached, "不想去", agDay(18, 12), "")
	n, _ := s.BreachStreak(now)
	if n != 1 {
		t.Fatalf("1 次毁约 → 1, 得到 %d", n)
	}
	agSeedSettled(t, s, ApptForgot, "忘了", agDay(19, 12), "")
	if n, _ = s.BreachStreak(now); n != 2 {
		t.Fatalf("毁约+忘了 = 连续 2 次, 得到 %d", n)
	}
	agSeedSettled(t, s, ApptKept, "", agDay(20, 11), "")
	if n, _ = s.BreachStreak(now); n != 0 {
		t.Fatalf("赴约必须打断连续 (否则永久杠上), 得到 %d", n)
	}
}

// 🔴 反面: 陈旧的一串不再算数 —— 否则"短期失去约定权"变成**永久锁死**。
// 改坏成: 去掉 AgencyStreakResetAfter 判断 → 30 天前的两次毁约仍锁死闸门 → 红。
func TestBreachStreak_StaleStreakExpires(t *testing.T) {
	s := agTestStore(t)
	old := agDay(1, 12)
	agSeedSettled(t, s, ApptBreached, "不想去", old, "")
	agSeedSettled(t, s, ApptBreached, "不想去", old.Add(time.Hour), "")
	if n, _ := s.BreachStreak(old.Add(2 * time.Hour)); n != 2 {
		t.Fatalf("当时应算连续 2 次, 得到 %d", n)
	}
	now := old.Add(AgencyStreakResetAfter + 24*time.Hour)
	if n, _ := s.BreachStreak(now); n != 0 {
		t.Fatalf("过了记仇期应归零, 得到 %d", n)
	}
	if ok, why := s.MayCommit(now); !ok {
		t.Fatalf("记仇期过后必须自动恢复约定权: %s", why)
	}
}

// 🔴 反面: 连续毁约 → 短暂失去"主动约定权"是**硬闸门**, 不是概率; 且找补能打开它。
// 改坏成: MayCommit 恒 true (或按率缩放) → 红。
func TestMayCommit_HardGateOpensAfterAmends(t *testing.T) {
	s := agTestStore(t)
	now := agDay(20, 12)
	id1 := agSeedSettled(t, s, ApptBreached, "不想去", agDay(18, 12), "")
	id2 := agSeedSettled(t, s, ApptBreached, "不想去", agDay(19, 12), "")
	if ok, _ := s.MayCommit(now); ok {
		t.Fatal("连续毁约到上限 → 必须停主动约定权")
	}
	owed, err := s.CompensationOwed(now)
	if err != nil || !owed {
		t.Fatalf("连续 2 次毁约后欠一次补偿: owed=%v err=%v", owed, err)
	}
	if err := s.MarkAmends(id2, now); err != nil {
		t.Fatal(err)
	}
	if owed, _ = s.CompensationOwed(now); owed {
		t.Fatal("找补过了就不再欠")
	}
	if ok, why := s.MayCommit(now); !ok {
		t.Fatalf("找补后约定权必须恢复: %s", why)
	}
	_ = id1
}

// 🔴 反面: 读库失败必须**放行** (失败往"正常说话"倒), 不许静默锁死。
// 改坏成: err != nil 时 return false → 红。
func TestMayCommit_FailsOpenOnReadError(t *testing.T) {
	s := agTestStore(t)
	now := agDay(20, 12)
	agSeedSettled(t, s, ApptBreached, "不想去", agDay(18, 12), "")
	agSeedSettled(t, s, ApptBreached, "不想去", agDay(19, 12), "")
	if err := s.DB.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	ok, why := s.MayCommit(now)
	if !ok {
		t.Fatalf("ledger 读不到时必须放行(照常生活), 得到停权: %s", why)
	}
}

func TestCompensationOwed_NeedsStreakMax(t *testing.T) {
	s := agTestStore(t)
	now := agDay(20, 12)
	agSeedSettled(t, s, ApptBreached, "不想去", agDay(18, 12), "")
	if owed, _ := s.CompensationOwed(now); owed {
		t.Fatal("只毁约一次不欠补偿 (M10 说的是连续两次)")
	}
	agSeedSettled(t, s, ApptBreached, "不想去", agDay(19, 12), "")
	if owed, _ := s.CompensationOwed(now); !owed {
		t.Fatal("连续两次毁约后必须欠一次补偿行为")
	}
}

// 🔴 反面: "连续违抗"必须由**顺从记录**打断, 否则同一件事的计数只增不减。
// 改坏成: DefianceStreak 只数违抗行 (忽略 DefyNone) → 红。
func TestDefianceStreak_ComplianceBreaksStreak(t *testing.T) {
	s := agTestStore(t)
	now := agDay(20, 12)
	if err := s.RecordDefiance("吃香菜", DefyRefuse, "不喜欢", now); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordDefiance("吃香菜", DefyRefuse, "不喜欢", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordDefiance("早起", DefyDelay, "起不来", now); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.DefianceStreak("吃香菜", now); n != 2 {
		t.Fatalf("同一件事连续 2 次, 得到 %d", n)
	}
	if n, _ := s.DefianceStreak("早起", now); n != 1 {
		t.Fatalf("别的 topic 不许串台, 得到 %d", n)
	}
	if err := s.RecordDefiance("吃香菜", DefyNone, "这次答应了", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.DefianceStreak("吃香菜", now); n != 0 {
		t.Fatalf("顺从一次必须让连续计数归零, 得到 %d", n)
	}
	if n, _ := s.DefianceStreak("", now); n != 0 {
		t.Fatalf("没有'同一件事'的键就不该有连续计数, 得到 %d", n)
	}
}

// 端到端: ledger 现算的 streak 正是 DecideDefiance 的上限输入 (配额闭环)。
func TestDefianceStreak_FeedsDecideDefiance(t *testing.T) {
	s := agTestStore(t)
	now := agDay(20, 12)
	in := DefianceInput{Tier: TierStance, TopicKey: "吃香菜", HasStance: true, StyleTier: DefyTalkBack}
	for i := 0; i < 3; i++ {
		streak, err := s.DefianceStreak(in.TopicKey, now)
		if err != nil {
			t.Fatal(err)
		}
		in.StreakCount = streak
		got := DecideDefiance(in, SchedulerConfig{})
		if err := s.RecordDefiance(in.TopicKey, got.Mode, got.Reason, now.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
		if i < DefianceStreakMaxDefault && got.Mode == DefyNone {
			t.Fatalf("第 %d 次不该已经被上限拦住: %s", i+1, got.Reason)
		}
		if i >= DefianceStreakMaxDefault && got.Mode != DefyNone {
			t.Fatalf("第 %d 次必须恢复正常, 得到 %v", i+1, got.Mode)
		}
	}
}

// ═══════════════════════════════════════════════════════════════════
// 六、毁约裁决
// ═══════════════════════════════════════════════════════════════════

func agOpenAppt(tier RequestTier) Appointment {
	return Appointment{ID: 1, Tier: tier, Who: 10001, Scene: "qq", Text: "一起吃饭",
		CreatedTS: "2026-05-10 12:00:00", DueTS: "2026-05-10 19:00:00"}
}

// 🔴 反面: 没有内在依据 → **赴约** (失败往"说到做到"倒; M7 不许掷骰子)。
// 改坏成: 默认 return true → 红 —— 那就变成随机毁约 (M20-①④)。
func TestDecideBreach_NoEvidenceKeepsAppointment(t *testing.T) {
	breach, reason := DecideBreach(BreachInput{Appt: agOpenAppt(TierPromise)})
	if breach {
		t.Fatalf("零值输入(没有依据)必须赴约, 却毁约了: %s", reason)
	}
	if !strings.Contains(reason, "赴约") {
		t.Fatalf("归因应写明是赴约: %s", reason)
	}
	// 弱依据也不算: 身体在场但 severity 未到门槛
	weak := BreachInput{Appt: agOpenAppt(TierPromise), BodyActive: true, BodySeverity: BreachSeverityMin - 0.01}
	if breach, reason := DecideBreach(weak); breach {
		t.Fatalf("轻微不适不是毁约依据: %s", reason)
	}
}

// 🔴 反面: C 层约定**绝对不可毁** (M12), 哪怕日程冲突 + 身体不适。
// 改坏成: 把 Tier 判断挪到依据之后 (C 层也走依据) → 红。
func TestDecideBreach_SafetyTierNeverBreached(t *testing.T) {
	in := BreachInput{Appt: agOpenAppt(TierSafety), SlotConflict: true,
		BodyActive: true, BodySeverity: 0.9, Valence: -0.9, WorldNotes: []EventNote{{Text: "暴雨"}}}
	if breach, reason := DecideBreach(in); breach {
		t.Fatalf("C 层约定不可毁, 理由: %s", reason)
	}
}

func TestDecideBreach_EvidenceBranches(t *testing.T) {
	if breach, reason := DecideBreach(BreachInput{Appt: agOpenAppt(TierPromise), SlotConflict: true}); !breach {
		t.Fatalf("日程冲突应算依据: %s", reason)
	}
	body := BreachInput{Appt: agOpenAppt(TierPromise), BodyActive: true, BodySeverity: 0.8}
	breach, reason := DecideBreach(body)
	if !breach || !strings.Contains(reason, "身体") {
		t.Fatalf("身体不适应算依据: %v %s", breach, reason)
	}
	world := BreachInput{Appt: agOpenAppt(TierPromise), WorldNotes: []EventNote{{Stage: StageDetail, Text: "下暴雨"}}}
	if breach, reason := DecideBreach(world); !breach || !strings.Contains(reason, "下暴雨") {
		t.Fatalf("世界事件应算依据且写进理由: %v %s", breach, reason)
	}
	mood := BreachInput{Appt: agOpenAppt(TierPromise), Valence: -0.6, Arousal: 0.9}
	if breach, _ := DecideBreach(mood); !breach {
		t.Fatal("心情低到门槛应算依据")
	}
	// ⚠️ Arousal 只影响措辞, 不影响"算不算依据"
	low := BreachInput{Appt: agOpenAppt(TierPromise), Valence: -0.6, Arousal: 0.0}
	if breach, _ := DecideBreach(low); !breach {
		t.Fatal("低唤醒也是依据 (双阈值是拒绝权那条线的判据, 不在这里重抄)")
	}
}

// 🔴 反面: 已结算的约定不许被改写 (事实就是事实)。
// 改坏成: 去掉 Terminal 检查 → 红。
func TestDecideBreach_SettledNotRewritten(t *testing.T) {
	a := agOpenAppt(TierPromise)
	a.Terminal = ApptKept
	if breach, _ := DecideBreach(BreachInput{Appt: a, SlotConflict: true, Valence: -0.9}); breach {
		t.Fatal("已结算的约定不许再毁一次")
	}
}

// 🔴 反面: 陈旧到模糊/归档的世界事件不是"今天的理由"。
// 改坏成: 不看 Stage 直接用第一条 → 红 (30 天前的事会让它今天不去)。
func TestDecideBreach_StaleWorldNoteIsNotAReason(t *testing.T) {
	stale := BreachInput{Appt: agOpenAppt(TierPromise),
		WorldNotes: []EventNote{{Stage: StageArchived, Text: "一个月前下过暴雨"}}}
	if breach, reason := DecideBreach(stale); breach {
		t.Fatalf("归档事件不该是今天的理由: %s", reason)
	}
	// Stage 未提供(零值) = 不知道阶段 → 放行, 不许让一条强事件静默失效
	unknown := BreachInput{Appt: agOpenAppt(TierPromise), WorldNotes: []EventNote{{Text: "地震了"}}}
	if breach, _ := DecideBreach(unknown); !breach {
		t.Fatal("Stage 未提供时不能把事件判成陈旧")
	}
}

// 🔴 反面: 约定 Tier 的零值语义 —— 未提供 → 按 B 层 (约定天然是 B 层的).
// 改坏成: 零值按 C 层 (不可毁) → 红, 因为那会让"忘了填 Tier"静默废掉整个毁约机制。
func TestDecideBreach_EmptyTierMeansPromise(t *testing.T) {
	in := BreachInput{Appt: agOpenAppt(""), SlotConflict: true}
	if breach, reason := DecideBreach(in); !breach {
		t.Fatalf("Tier 未提供的约定按 B 层处理, 应可毁: %s", reason)
	}
}

// 🔴 反面: 毁约也不是概率事件。改坏成: 按率/随机决定 → 200 次里必然不一致。
func TestDecideBreach_DeterministicNotRandom(t *testing.T) {
	in := BreachInput{Appt: agOpenAppt(TierPromise), Valence: -0.4, Arousal: 0.2}
	wantBreach, wantReason := DecideBreach(in)
	for i := 0; i < 200; i++ {
		b, r := DecideBreach(in)
		if b != wantBreach || r != wantReason {
			t.Fatalf("第 %d 次结果不同: (%v,%s) vs (%v,%s)", i, b, r, wantBreach, wantReason)
		}
	}
}
