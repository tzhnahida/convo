package convo

import (
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────
// §2.6 灵光的"之后": 说出来
//
// ⚠️ 这一节原来只有"产生" —— SparkLine / MarkSparkSaid / TouchSpark **全都没有
// 生产调用者**。也就是说: **它想出来的东西, 说不出。**
// ─────────────────────────────────────────────────────────────

func sparkStore(t *testing.T) *Store {
	t.Helper()
	s := newTestStore(t)
	if err := EnsureSparkSchema(s.DB); err != nil {
		t.Fatal(err)
	}
	return s
}

// seedSpark 落一条**温着**的灵光 (ParseSpark 给新生 0.5, 这里照抄)。
func seedSpark(t *testing.T, s *Store, content, talkKey string, heat float64, saidTS string) int64 {
	t.Helper()
	id, err := s.SaveSpark(Spark{
		Kind: SparkIdea, Content: content, TalkKey: talkKey,
		Source:    SparkSource{Kind: "dream", RefID: "m", HasRef: true},
		HeatDelta: heat, BornTS: time.Now().Format(bodyTSLayout), SaidTS: saidTS,
	})
	if err != nil || id == 0 {
		t.Fatalf("落库失败: id=%d err=%v", id, err)
	}
	return id
}

// TestShareableSpark_WeeklyCapIsAHardGate
//
// ⚠️ §2.6.6 硬线 5「分享 ≤2/周」是**硬闸**, 不是概率缩放。
func TestShareableSpark_WeeklyCapIsAHardGate(t *testing.T) {
	s := sparkStore(t)
	now := time.Now()
	// 两条温着的、没说过。
	seedSpark(t, s, "会唱歌的电阻", "合成器", 0.9, "")
	seedSpark(t, s, "倒着长的树", "植物", 0.9, "")

	if _, ok := s.ShareableSpark(now); !ok {
		t.Fatal("没到配额却说没有可分享的 —— 那这条接线是空的")
	}
	// 把一条标成"刚说过" → 还有一条可分享。
	one := s.SparksSaidSince(now)
	if one != 0 {
		t.Fatalf("夹具坏了: 一开始不应当有说过的, 实得 %d", one)
	}
	all, _ := s.AllSparks(10)
	if err := s.MarkSparkSaid(all[0].ID, now); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.ShareableSpark(now); !ok {
		t.Fatal("才说了 1 条就不给分享了 —— 上限是 2")
	}
	if err := s.MarkSparkSaid(all[1].ID, now); err != nil {
		t.Fatal(err)
	}
	// ⚠️ 配额用完 → **一条都不给**。
	if sp, ok := s.ShareableSpark(now); ok {
		t.Fatalf("配额已经用完却还给了一条: %s", sp.Content)
	}
	// ⚠️ 把 said_ts 推到**八天前**: 周配额不再被占 —— 但那条灵光**依然不会再被分享**。
	//
	// 因为"说过的不再说"是**另一道门**(而且是无限期的): 同一句"我突然想到"讲两遍
	// 就是念稿, 与它多久以前说过无关。第一版我在这里断言"应当能再分享", 那是我把
	// 两道门当成了一道。
	old := now.AddDate(0, 0, -8)
	if _, err := s.DB.Exec("UPDATE convo_spark SET said_ts = ?", old.Format(bodyTSLayout)); err != nil {
		t.Fatal(err)
	}
	if n := s.SparksSaidSince(now.AddDate(0, 0, -7)); n != 0 {
		t.Fatalf("八天前的分享仍然占着本周配额: %d", n)
	}
	if sp, ok := s.ShareableSpark(now); ok {
		t.Fatalf("说过的灵光又可以被分享了: %s", sp.Content)
	}
	// 而配额腾出来之后, **一条新的**温灵光确实能被分享 —— 这才证明门槛是配额不是别的。
	seedSpark(t, s, "八天后的新想法", "k9", 0.9, "")
	if _, ok := s.ShareableSpark(now); !ok {
		t.Fatal("配额腾出来了却还分享不了")
	}
}

// TestShareableSpark_SkipsSaidAndCold
//
// ⚠️ 三个门都要真的挡住: 说过的 / 冷掉的 / 内容不干净的。
func TestShareableSpark_SkipsSaidAndCold(t *testing.T) {
	s := sparkStore(t)
	now := time.Now()
	// ① 说过的: 温着也不再说 —— 同一句"我突然想到"讲两遍就是念稿。
	seedSpark(t, s, "已经说过的", "k1", 0.9, now.Add(-time.Hour).Format(bodyTSLayout))
	if sp, ok := s.ShareableSpark(now); ok {
		t.Fatalf("说过的又被挑出来了: %s", sp.Content)
	}
	// ② 冷掉的: heat 低于 DropBelow(0.2) 且没说过 → dropped, 不主动说。
	seedSpark(t, s, "冷掉的", "k2", 0.1, "")
	if sp, ok := s.ShareableSpark(now); ok {
		t.Fatalf("冷掉的被挑出来了: %s", sp.Content)
	}
	// ⚠️ 而它**仍然在库里** —— 放下不是删除 (设计稿 L224)。
	all, _ := s.AllSparks(10)
	if len(all) != 2 {
		t.Fatalf("冷掉的那条被删了? 库里应当还有 2 条, 实得 %d", len(all))
	}
	// ③ 温着的、没说过的 → 挑出来, 而且挑的是**最新的那条**。
	seedSpark(t, s, "温着的这条", "k3", 0.8, "")
	sp, ok := s.ShareableSpark(now)
	if !ok {
		t.Fatal("温着的那条没被挑出来")
	}
	if sp.Content != "温着的这条" {
		t.Fatalf("挑错了: %s", sp.Content)
	}
}

// TestShareableSpark_ReadFailureSaysNo
//
// ⚠️ 失败往**"这次不说"**倒 —— 说多了比少说一次糟得多,
// 而"少说一次"没有任何人会发现。
func TestShareableSpark_ReadFailureSaysNo(t *testing.T) {
	if _, ok := (&Store{}).ShareableSpark(time.Now()); ok {
		t.Fatal("没有库的时候不该挑出一条灵光")
	}
	// ⚠️ `SparksSaidSince` 在没库时返回 **0** —— 它是个**计数**, 谎报"已说过两条"
	// 比返回 0 更坏。fail-safe 落在**判定的那一侧**(ShareableSpark 直接返回 false),
	// 而不是让计数函数说谎。
	if n := (&Store{}).SparksSaidSince(time.Now()); n != 0 {
		t.Fatalf("没有库时计数应当是 0, 实得 %d", n)
	}
}

// TestSparkShareTopic_NoInterfaceNoSpark
//
// ⚠️ 条件③「与当前语境有接口」是这条路上**真的一关**: 灵光再好, 接不上当前语境就不抛。
// 缺它的表现是它突然说一句「我前几天看到个事」, 而前文没有任何东西接得住。
func TestSparkShareTopic_NoInterfaceNoSpark(t *testing.T) {
	e := gateEngine(t)
	if err := EnsureSparkSchema(e.Store.DB); err != nil {
		t.Fatal(err)
	}
	seedSpark(t, e.Store, "会唱歌的电阻", "合成器", 0.9, "")
	if _, _, ok := e.sparkShareTopic(time.Now(), "今天菜市场的茄子挺便宜"); ok {
		t.Fatal("灵光的线索与当前语境毫无交集, 却抛了出来 —— 那正是「凭空抛出」")
	}
}

// TestSparkShareTopic_IsTheProductionEntry
//
// ⚠️ **接线尺子**: 光测 ShareableSpark 证明不了它真的被接进那一轮。
func TestSparkShareTopic_IsTheProductionEntry(t *testing.T) {
	e := gateEngine(t)
	if err := EnsureSparkSchema(e.Store.DB); err != nil {
		t.Fatal(err)
	}
	// ⚠️ 条件③「与当前语境有接口」现在也在这条路上: 话题要**真的接得住**这条灵光。
	// 传空话题会让它因为「没有接口」而不抛出 —— 那是另一条判据, 不是这个测试要测的。
	if _, _, ok := e.sparkShareTopic(time.Now(), ""); ok {
		t.Fatal("一条灵光都没有却说可以分享")
	}
	seedSpark(t, e.Store, "会唱歌的电阻", "合成器", 0.9, "")
	topic, id, ok := e.sparkShareTopic(time.Now(), "合成器")
	if !ok || id <= 0 {
		t.Fatalf("有温着的灵光却没挑出来: ok=%v id=%d", ok, id)
	}
	// ⚠️ kind 必须是 "spark" —— dialog.go 靠它决定走哪条注入分支。
	if topic.Kind != "spark" {
		t.Fatalf("话题种类不对: %q —— 那段 7.3 会走成普通话题", topic.Kind)
	}
	if topic.Text == "" {
		t.Fatal("话题文本是空的")
	}
	// ⚠️ 而它渲染出来的那一行必须带**两条禁止条款**(不许解释 / 不许当事实)。
	for _, want := range []string{"不要解释", "不许"} {
		if !containsStr(topic.Text, want) {
			t.Fatalf("渲染出来的行少了 %q:\n%s", want, topic.Text)
		}
	}
}

// TestMarkSparkSaidAfter_IsWhatMakesTheCapWork
//
// ⚠️ 不记账 → 周配额永远读到 0 → 同一句可以被说无数次。
func TestMarkSparkSaidAfter_IsWhatMakesTheCapWork(t *testing.T) {
	e := gateEngine(t)
	if err := EnsureSparkSchema(e.Store.DB); err != nil {
		t.Fatal(err)
	}
	id := seedSpark(t, e.Store, "会唱歌的电阻", "合成器", 0.9, "")
	if n := e.Store.SparksSaidSince(time.Now().AddDate(0, 0, -7)); n != 0 {
		t.Fatalf("一开始不应当有说过的: %d", n)
	}
	e.MarkSparkSaidAfter(id)
	if n := e.Store.SparksSaidSince(time.Now().AddDate(0, 0, -7)); n != 1 {
		t.Fatalf("记完之后应当有 1 条, 实得 %d —— 不记账配额就是空的", n)
	}
	// 而它记完之后**不再被挑出来**。
	if _, ok := e.Store.ShareableSpark(time.Now()); ok {
		t.Fatal("刚说过的还被挑得出来")
	}
	// id=0 不许写库(拿不到 id 的那些路径不该误伤别人)。
	e.MarkSparkSaidAfter(0)
	if n := e.Store.SparksSaidSince(time.Now().AddDate(0, 0, -7)); n != 1 {
		t.Fatalf("id=0 改动了库: %d", n)
	}
}
