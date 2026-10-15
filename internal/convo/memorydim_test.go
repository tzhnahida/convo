package convo

import (
	"testing"
	"time"
)

// ——— §7.3 维一 敏感度: 能不能跨人用 ———

func TestSensitivity_PublicMustBeExplicit(t *testing.T) {
	// 默认是关系级 —— 什么都往公共级倒, 会让每条记忆都长出腿。
	if got := ClassifySensitivity("", "今天天气不错"); got != SensitivityRelation {
		t.Fatalf("默认必须是 relation, 得到 %q", got)
	}
	if got := ClassifySensitivity("public", ""); got != SensitivityPublic {
		t.Fatalf("显式声明 public 应当被认出来, 得到 %q", got)
	}
	if got := ClassifySensitivity("公开", ""); got != SensitivityPublic {
		t.Fatalf("中文别名也要认, 得到 %q", got)
	}
	// 私域优先于一切 (既有契约不能被新分级削弱)。
	if got := ClassifySensitivity("public", "我最近在吃抗抑郁药"); got != SensitivityPrivate {
		t.Fatalf("私域词表必须压过显式 public 声明, 得到 %q", got)
	}
}

func TestMemoryUsableIn_RelationIsSceneBound(t *testing.T) {
	now := time.Now()
	m := Memory{MemID: "m1", Sensitivity: SensitivityRelation, SceneKey: "group:A"}
	if !MemoryUsableIn(m, "group:A", now) {
		t.Fatal("它自己那个场景必须能用")
	}
	if MemoryUsableIn(m, "group:B", now) {
		t.Fatal("关系级记忆漏到了别的群 —— 那正是「禁止跨人转述」的读侧入口")
	}
	// 公共级可跨。
	pub := Memory{MemID: "m2", Sensitivity: SensitivityPublic, SceneKey: "group:A"}
	if !MemoryUsableIn(pub, "group:B", now) {
		t.Fatal("公共级应当可以跨场景")
	}
}

// ⚠️ 零回归: 历史行没有 sensitivity / scene_key。
// 把它们读成"关系级且不属于任何场景"→ 全部不可用 → 「它突然什么都不记得了」。
func TestMemoryUsableIn_UnscopedIsAllowed(t *testing.T) {
	now := time.Now()
	legacy := Memory{MemID: "old"}
	if !MemoryUsableIn(legacy, "group:A", now) {
		t.Fatal("存量记忆被新加的闸门挡死了 —— 零值必须是「未提供」而不是「禁止」")
	}
	if !MemoryUsableIn(Memory{MemID: "x", Sensitivity: SensitivityRelation}, "group:A", now) {
		t.Fatal("关系级但没有场景归属 → 未提供, 应当放行")
	}
}

func TestMemoryUsableIn_TimeWindow(t *testing.T) {
	now := time.Now()
	expired := Memory{MemID: "m", ValidUntil: now.Add(-time.Hour).Format(bodyTSLayout)}
	if MemoryUsableIn(expired, "", now) {
		t.Fatal("过期了还能用")
	}
	future := Memory{MemID: "m", ValidUntil: now.Add(time.Hour).Format(bodyTSLayout)}
	if !MemoryUsableIn(future, "", now) {
		t.Fatal("还没过期就说不能用")
	}
	// 解析不了的期限 → 宁可不用。
	bad := Memory{MemID: "m", ValidUntil: "下个月吧"}
	if MemoryUsableIn(bad, "", now) {
		t.Fatal("解析不了的失效区间应当按不可用处理")
	}
	sup := Memory{MemID: "m", SupersededTS: now.Format(bodyTSLayout)}
	if MemoryUsableIn(sup, "", now) {
		t.Fatal("被取代了还能用")
	}
	priv := Memory{MemID: "m", Sensitivity: SensitivityPrivate}
	if MemoryUsableIn(priv, "", now) {
		t.Fatal("私域内容读到就是脏数据, 不许用")
	}
}

// ——— §7.3 维三 时间: 旧事实被新事实取代 ———

func TestMemorySlot_PlansDoNotSupersede(t *testing.T) {
	// 计划类天然并存 —— 自动派生槽位会让它们互相作废。
	for _, k := range []string{"plan", "event", "todo", "reminder", "schedule"} {
		if got := MemorySlot(k, "plan", "下周三体检"); got != "" {
			t.Fatalf("类型 %s 不应当构成槽位, 得到 %q", k, got)
		}
	}
	if got := MemorySlot("fact", "pet", "猫名"); got != "pet|猫名" {
		t.Fatalf("属性类应当构成槽位, 得到 %q", got)
	}
	if MemorySlot("fact", "pet", "") != "" || MemorySlot("fact", "", "猫名") != "" {
		t.Fatal("缺一半就不构成槽 —— 宁可不错杀")
	}
}

// ⚠️ 本维的核心契约: 新事实写下之后, **旧的那条不许再被注入**。
func TestSupersede_OldFactStopsBeingInjected(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	base := MemoryDelta{K: "pet", Label: "猫名", Kind: "fact", Confidence: 0.9, Source: "user_said"}
	a := base
	a.MemID, a.V = "m1", "叫豆豆"
	if err := e.Store.UpsertMemory(a, "active"); err != nil {
		t.Fatal(err)
	}
	got, err := e.Store.TopMemoriesFor("", 10, time.Now())
	if err != nil || len(got) != 1 {
		t.Fatalf("want 1, got %v %v", got, err)
	}
	b := base
	b.MemID, b.V = "m2", "叫咪咪"
	if err := e.Store.UpsertMemory(b, "active"); err != nil {
		t.Fatal(err)
	}
	got, _ = e.Store.TopMemoriesFor("", 10, time.Now())
	if len(got) != 1 || got[0].MemID != "m2" {
		t.Fatalf("旧事实没有被取代 —— 它会同时记得两个猫名: %v", got)
	}
	// ⚠️ 取代是 UPDATE 不是 DELETE: 「旧事实没发生过」和「旧事实被改了」是两件事。
	var v, sup string
	if err := e.Store.DB.QueryRow("SELECT v, COALESCE(superseded_ts,'') FROM convo_memory WHERE mem_id = 'm1'").Scan(&v, &sup); err != nil {
		t.Fatalf("旧事实被删了 —— 那就无法回答「它以前以为猫叫什么」: %v", err)
	}
	if v != "叫豆豆" || sup == "" {
		t.Fatalf("旧行应当保留原值并标记失效时刻, got v=%q superseded=%q", v, sup)
	}
	// 计划类不取代。
	// ⚠️ 必须填 Source: 零值会被归到 inferred, 而 inferred **不会被注入** ——
	// 夹具漏填的表现是「两个计划一条都查不回来」, 与"被取代了"长得一模一样。
	p1 := MemoryDelta{MemID: "p1", K: "plan", Label: "体检", Kind: "plan", V: "下周三", Confidence: 0.9, Source: "user_said"}
	p2 := MemoryDelta{MemID: "p2", K: "plan", Label: "交房租", Kind: "plan", V: "月底", Confidence: 0.9, Source: "user_said"}
	if err := e.Store.UpsertMemory(p1, "active"); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.UpsertMemory(p2, "active"); err != nil {
		t.Fatal(err)
	}
	got, _ = e.Store.TopMemoriesFor("", 10, time.Now())
	var plans int
	for _, m := range got {
		if m.Kind == "plan" {
			plans++
		}
	}
	if plans != 2 {
		t.Fatalf("两个计划应当并存, 实际 %d —— 自动派生槽位把计划互相作废了", plans)
	}
}

// 端到端: 场景归属真的落库并且真的挡得住。
func TestTopMemoriesFor_ScopedWriteIsNotVisibleElsewhere(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	if err := e.Store.UpsertMemory(MemoryDelta{MemID: "g1", K: "work", Label: "甲群的事",
		Kind: "fact", V: "甲群在筹备聚会", Confidence: 0.9, Source: "user_said",
		Sensitivity: SensitivityRelation, SceneKey: "group:A"}, "active"); err != nil {
		t.Fatal(err)
	}
	inA, _ := e.Store.TopMemoriesFor("group:A", 10, time.Now())
	if len(inA) != 1 {
		t.Fatalf("自己那个群应当看得到: %v", inA)
	}
	inB, _ := e.Store.TopMemoriesFor("group:B", 10, time.Now())
	if len(inB) != 0 {
		t.Fatalf("甲群的事漏进了乙群: %v", inB)
	}
}

// ⚠️ 契约: **未标来源的记忆落库后不会被注入**。
//
// 这是保守默认 (来源不明的记忆不该进上下文), 但它是个**静默陷阱**:
// 写的时候不报错, 读的时候悄悄少一条, 而现象只是「它好像没记住」。
// 所以 UpsertMemory 现在会对空来源出声 —— 这条测试同时钉住"默认是保守的"。
func TestUpsertMemory_EmptySourceIsNotInjected(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	if err := e.Store.UpsertMemory(MemoryDelta{MemID: "n1", K: "pet", Label: "猫名",
		Kind: "fact", V: "叫豆豆", Confidence: 0.9}, "active"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := e.Store.DB.QueryRow("SELECT COUNT(*) FROM convo_memory WHERE mem_id='n1'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("保守默认**不是拒收**, 是落库但不注入")
	}
	got, _ := e.Store.TopMemoriesFor("", 10, time.Now())
	for _, m := range got {
		if m.MemID == "n1" {
			t.Fatal("来源不明的记忆被注入了 —— 归属是 §7.3 的第二个维度, 不能省")
		}
	}
}
