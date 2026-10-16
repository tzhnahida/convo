package convo

import (
	"github.com/tzhnahida/VocabLock/internal/clock"
	"strings"
	"testing"
	"time"
)

// ——— §6.10 用户区分 + §9 群与成员的数据分区: 契约测试 ———
//
// 每条契约都配反面用例, 格式统一为:
//     // 🔴 反面: 把 <实现> 改坏成 <X> → 这条会红。
// 反面用例不是补充, 是主体 —— 正面断言只能证明"路径通", 反面断言才证明"哪一刀挡住了泄漏"。
//
// 辅助函数一律带 sceneTest 前缀 (合并时与其他模块的同名 helper 不冲突)。

// 测试用场景键。全部用 group 键, 除非明确在测私聊 —— 免得
// "scene_key 里本来就含 uin" 这种事污染"注入里不许出现 uin"的断言。
const (
	sceneTestGroupA = "group:77777"
	sceneTestGroupB = "group:88888"
	sceneTestSelf   = "10001" // convo 自己的 uin
	sceneTestUserA  = "20002"
	sceneTestUserB  = "30003"
)

func sceneTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(t.TempDir(), "scene-test.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := EnsureSceneSchema(s); err != nil {
		t.Fatalf("ensure scene schema: %v", err)
	}
	return s
}

func sceneTestSeedScene(t *testing.T, s *Store, key, kind string) {
	t.Helper()
	sc := Scene{SceneKey: key, Kind: kind, Stage: StageStranger}
	if kind == SceneKindGroup {
		sc.GroupID = ScenePeerOf(key)
	} else {
		sc.PeerUIN = ScenePeerOf(key)
	}
	if err := s.saveSceneSeed(sc); err != nil {
		t.Fatalf("save scene %s: %v", key, err)
	}
}

type sceneTestCol struct {
	name    string
	typ     string
	notnull int
	pk      int
}

// sceneTestTableInfo 读一张表的列定义 (PRAGMA table_info)。
func sceneTestTableInfo(t *testing.T, s *Store, table string) map[string]sceneTestCol {
	t.Helper()
	rows, err := s.DB.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("pragma %s: %v", table, err)
	}
	defer rows.Close()
	out := map[string]sceneTestCol{}
	for rows.Next() {
		var c sceneTestCol
		var cid int
		var dflt interface{}
		if err := rows.Scan(&cid, &c.name, &c.typ, &c.notnull, &dflt, &c.pk); err != nil {
			t.Fatalf("scan pragma %s: %v", table, err)
		}
		out[c.name] = c
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("pragma rows %s: %v", table, err)
	}
	if len(out) == 0 {
		t.Fatalf("表 %s 不存在 (PRAGMA table_info 返回空)", table)
	}
	return out
}

func sceneTestCount(t *testing.T, s *Store, query string, args ...interface{}) int {
	t.Helper()
	var n int
	if err := s.DB.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("count query %q: %v", query, err)
	}
	return n
}

// ——— 契约 1: 表结构本身就是保证 (没有列 = 不可能渲染出来) ———

func TestScene_SchemaIdempotent_AndStructure(t *testing.T) {
	s := sceneTestStore(t)
	// 幂等: 再建一次不许报错 (EnsureXxxSchema 会被每条启动路径调用)。
	if err := EnsureSceneSchema(s); err != nil {
		t.Fatalf("EnsureSceneSchema 第二次调用报错 (不幂等): %v", err)
	}
	// 🔴 反面: 把 CREATE TABLE IF NOT EXISTS 改成 CREATE TABLE → 第二次调用报错 → 红。

	scene := sceneTestTableInfo(t, s, "convo_scene")
	for _, want := range []string{"scene_key", "kind", "self_role", "stage", "first_seen_ts"} {
		if _, ok := scene[want]; !ok {
			t.Errorf("convo_scene 缺列 %s", want)
		}
	}

	// ★ 群公共层记忆表**没有 uin 列** —— 这是"物理保证"的字面实现。
	ms := sceneTestTableInfo(t, s, "convo_memory_scene")
	if _, has := ms["uin"]; has {
		t.Fatal("convo_memory_scene 出现了 uin 列 —— 群公共层结构上就再也不能保证不渲染某个人")
	}
	// 🔴 反面: 给 convo_memory_scene 加一列 uin (或把 origin_uin 改名成 uin) → 红。
	if _, ok := ms["origin_uin"]; !ok {
		t.Error("血缘列 origin_uin 丢了 —— 审计/撤回会失去依据")
	}

	// ★ 关系层记忆表 uin NOT NULL: 漏传主体 → 空集, 不是返回所有人。
	mp := sceneTestTableInfo(t, s, "convo_memory_person")
	uinCol, ok := mp["uin"]
	if !ok {
		t.Fatal("convo_memory_person 缺 uin 列")
	}
	if uinCol.notnull != 1 {
		t.Fatal("convo_memory_person.uin 不是 NOT NULL —— 漏传主体的检索会退化成泄漏")
	}
	// 🔴 反面: 去掉 uin 的 NOT NULL (或允许写空串) → 红。

	mem := sceneTestTableInfo(t, s, "convo_scene_member")
	if mem["scene_key"].pk != 1 || mem["uin"].pk != 2 {
		t.Fatalf("convo_scene_member 主键必须是 (scene_key, uin): pk(scene_key)=%d pk(uin)=%d",
			mem["scene_key"].pk, mem["uin"].pk)
	}
	if _, has := mem["is_new"]; has {
		t.Error("convo_scene_member 里有粘住的 is_new 布尔量 —— 派生量不许落库 (纪律 #4), 由 first_seen_ts 现算")
	}
	// 🔴 反面: 加一个只写不清的 is_new 列 → 红 (没人能回答"谁在什么时候重算它")。
}

// ——— 契约 2: 禁跨人转述 (R1) ———

func TestSceneMemory_PersonPartition_NoCrossUser(t *testing.T) {
	s := sceneTestStore(t)
	sceneTestSeedScene(t, s, sceneTestGroupA, SceneKindGroup)

	saved, err := s.SavePersonMemory(PersonMemory{
		MemID: "pm-a", UIN: sceneTestUserA, SceneKey: sceneTestGroupA,
		K: "pet", V: "A 养了一只叫豆豆的猫", Label: "豆豆", Confidence: 0.9,
	})
	if err != nil || !saved {
		t.Fatalf("正面用例: A 的关系记忆应能落库, saved=%v err=%v", saved, err)
	}

	// 正面: 以 A 为主体检索, 拿得到 A 的。
	gotA, err := s.PersonMemories(sceneTestGroupA, sceneTestUserA)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotA) != 1 || gotA[0].MemID != "pm-a" {
		t.Fatalf("以 A 为检索主体应得到 A 的记忆, 实得 %+v", gotA)
	}

	// ★ 反面 (R1): 以 B 为主体检索, **不许**出现 A 的那条。
	// 🔴 改坏成: 把 WHERE scene_key=? AND uin=? 里的 `AND uin=?` 删掉 (只按场景过滤) → 这里会拿到 A 的 → 红。
	gotB, err := s.PersonMemories(sceneTestGroupA, sceneTestUserB)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotB) != 0 {
		t.Fatalf("跨人泄漏: 以 B 检索却拿到 %d 条 (含 %q)", len(gotB), gotB[0].V)
	}

	// ★ 反面 (R1 的第二种改法): 漏传主体 → **空集**(安静), 不是"所有人"。
	// 🔴 改坏成: uin 为空时跳过主体条件 → 这里会拿到 A 的 → 红。
	gotEmpty, err := s.PersonMemories(sceneTestGroupA, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(gotEmpty) != 0 {
		t.Fatalf("漏传主体却返回了 %d 条 —— 必须退化为空集", len(gotEmpty))
	}
}

func TestSceneMemory_PersonPartition_NoCrossScene(t *testing.T) {
	s := sceneTestStore(t)
	sceneTestSeedScene(t, s, sceneTestGroupA, SceneKindGroup)
	sceneTestSeedScene(t, s, sceneTestGroupB, SceneKindGroup)

	if _, err := s.SavePersonMemory(PersonMemory{
		MemID: "pm-a", UIN: sceneTestUserA, SceneKey: sceneTestGroupA,
		K: "food", V: "A 在 A 群说过爱吃辣", Confidence: 0.9,
	}); err != nil {
		t.Fatal(err)
	}
	// ★ 反面: A 群的事不许在 B 群被检索出来。
	// 🔴 改坏成: 去掉 WHERE scene_key=? → 在 B 群会拿到 A 群的那条 → 红。
	got, err := s.PersonMemories(sceneTestGroupB, sceneTestUserA)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("跨场景泄漏: B 群检索拿到 A 群的 %d 条", len(got))
	}
}

// ——— 契约 3: 全局事实不许带场景标识, 且必须本人在场 (R7) ———

func TestSceneMemory_GlobalFact_NoSceneLeak(t *testing.T) {
	s := sceneTestStore(t)
	sceneTestSeedScene(t, s, sceneTestGroupA, SceneKindGroup)
	sceneTestSeedScene(t, s, sceneTestGroupB, SceneKindGroup)

	if _, err := s.SavePersonMemory(PersonMemory{
		MemID: "pm-g", UIN: sceneTestUserA, SceneKey: sceneTestGroupA, GlobalFact: true,
		K: "job", V: "A 是做硬件的", Confidence: 0.95,
	}); err != nil {
		t.Fatal(err)
	}

	// 正面: 本人在场时可以取到他的稳定事实。
	facts, err := s.PersonGlobalFacts(sceneTestUserA)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 1 || !facts[0].GlobalFact {
		t.Fatalf("应取到 1 条全局事实, 实得 %+v", facts)
	}
	// ★ 物理保证: 该投影里**没有 scene_key**, 场景标识结构上进不了注入文本。
	// 🔴 改坏成: 投影里带上 scene_key (或 SELECT *) → SceneKey 非空 → 红。
	if facts[0].SceneKey != "" {
		t.Fatalf("全局事实带出了来源场景 %q —— 跨场景暴露", facts[0].SceneKey)
	}

	// ★ 反面: 没传主体 → 空集, 不许"谁都给"。
	// 🔴 改坏成: uin 为空时不加条件 → 这里会拿到那条 → 红。
	none, err := s.PersonGlobalFacts("")
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Fatalf("漏传主体却返回了 %d 条全局事实", len(none))
	}

	// ★ 反面: 全局事实**不构成**"在别的场景讲 A 群的事"的素材 —— 常规检索仍按来源场景过滤。
	// 🔴 改坏成: global_fact=1 的行忽略 scene_key → 在 B 群会拿到 → 红。
	grabbed, err := s.PersonMemories(sceneTestGroupB, sceneTestUserA)
	if err != nil {
		t.Fatal(err)
	}
	if len(grabbed) != 0 {
		t.Fatalf("全局事实被当成跨场景素材: B 群检索拿到 %d 条", len(grabbed))
	}
}

// ——— 契约 4: 个人私域没有表 (R2) ———

func TestScene_PrivateNeverPersisted(t *testing.T) {
	s := sceneTestStore(t)
	sceneTestSeedScene(t, s, sceneTestGroupA, SceneKindGroup)

	// 正面: 普通关系记忆照常落库 (否则下面的断言可能是"因为写入口整个坏了"才通过的)。
	okNormal, err := s.SavePersonMemory(PersonMemory{
		MemID: "pm-ok", UIN: sceneTestUserA, SceneKey: sceneTestGroupA,
		K: "food", V: "A 爱吃辣", Confidence: 0.9,
	})
	if err != nil || !okNormal {
		t.Fatalf("普通关系记忆应能落库: saved=%v err=%v", okNormal, err)
	}

	// ★ 反面 (R2): 声明 private 的内容在**写入口**就被丢弃。
	// 🔴 改坏成: 去掉丢弃、改成"落库并标 sensitivity='relation'" → 下面的 COUNT 会 >0 → 红。
	ok, err := s.SavePersonMemory(PersonMemory{
		MemID: "pm-secret", UIN: sceneTestUserA, SceneKey: sceneTestGroupA,
		K: "health", V: "我最近在吃抗抑郁药", Sensitivity: SensitivityPrivate, Confidence: 0.9,
	})
	if err != nil {
		t.Fatalf("刻意丢弃不是故障, 不该报错: %v", err)
	}
	if ok {
		t.Fatal("private 内容被写进关系层了 —— 个人私域必须没有表")
	}
	if n := sceneTestCount(t, s, `SELECT COUNT(*) FROM convo_memory_person WHERE v LIKE '%抗抑郁%'`); n != 0 {
		t.Fatalf("关系层里查出了私密内容 %d 条", n)
	}

	// ★ 反面: 模型**漏标** sensitivity 时, 兜底词表也要拦住 (两网取严)。
	// 🔴 改坏成: 只信 declared 字段、不跑词表 → 这条会落库 → 红。
	okLeak, err := s.SavePersonMemory(PersonMemory{
		MemID: "pm-leak", UIN: sceneTestUserA, SceneKey: sceneTestGroupA,
		K: "health", V: "我最近在吃抗抑郁药", Confidence: 0.9,
	})
	if err != nil {
		t.Fatal(err)
	}
	if okLeak {
		t.Fatal("未声明但命中高敏词的内容落库了 —— 兜底网没生效")
	}

	// ★ 反面: 群公共层同样不许落私密内容 ("不进任何表").
	// 🔴 改坏成: 只在 PersonMemory 的写入口过滤 → 这条会落进 convo_memory_scene → 红。
	okScene, err := s.SaveSceneMemory(SceneMemory{
		MemID: "sm-secret", SceneKey: sceneTestGroupA, K: "health", V: "群里说自己在吃抗抑郁药",
	})
	if err != nil {
		t.Fatal(err)
	}
	if okScene {
		t.Fatal("private 内容落进群公共层了")
	}
	if n := sceneTestCount(t, s, `SELECT COUNT(*) FROM convo_memory_scene WHERE v LIKE '%抗抑郁%'`); n != 0 {
		t.Fatalf("群公共层里查出了私密内容 %d 条", n)
	}

	// 反面: 全库扫一遍 —— 私密文本不许出现在任何一张记忆表里。
	for _, tbl := range []string{"convo_memory_person", "convo_memory_scene"} {
		if n := sceneTestCount(t, s, "SELECT COUNT(*) FROM "+tbl+" WHERE v LIKE '%抗抑郁%'"); n != 0 {
			t.Fatalf("%s 残留私密内容 %d 条", tbl, n)
		}
	}
}

// ——— 契约 5: 注入路径读不出血缘 (R5) ———

func TestSceneMemory_InjectionExcludesOrigin(t *testing.T) {
	s := sceneTestStore(t)
	sceneTestSeedScene(t, s, sceneTestGroupA, SceneKindGroup)

	if ok, err := s.SaveSceneMemory(SceneMemory{
		MemID: "sm-1", SceneKey: sceneTestGroupA, K: "food", V: "上周三大家一起去吃了火锅",
		Label: "火锅局", Kind: "meme", Confidence: 0.9, OriginUIN: sceneTestUserA,
	}); err != nil || !ok {
		t.Fatalf("群公共记忆应能落库: saved=%v err=%v", ok, err)
	}

	// 正面: 内容确实渲染出来了 (否则下面的"不含 uin"可能是空集造成的假通过)。
	lines, err := s.SceneMemoryInjection(sceneTestGroupA, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "火锅") {
		t.Fatalf("注入行应含公共记忆内容, 实得 %+v", lines)
	}

	// ★ 反面 (R5): 注入文本里**不许出现任何 uin**。
	// 🔴 改坏成: 列清单换成 SELECT * (把 origin_uin 一起取出来渲染) → 下面命中 → 红。
	for _, ln := range lines {
		if strings.Contains(ln, sceneTestUserA) {
			t.Fatalf("注入行泄漏了血缘 uin: %q", ln)
		}
		if strings.Contains(ln, "owner") || strings.Contains(ln, "群主") {
			t.Fatalf("注入行出现了 owner 标记 (§6.10.7 禁止): %q", ln)
		}
	}

	// ★ 反面: 注入用的列清单里根本就没有血缘列 —— 这条断言守的是"改坏的方式"。
	// 🔴 改坏成: 在 sceneMemoryInjectCols 里加上 origin_uin → 红。
	if strings.Contains(sceneMemoryInjectCols, "origin") {
		t.Fatalf("注入列清单里出现了血缘列: %s", sceneMemoryInjectCols)
	}

	// 正面: 血缘仍然**可审计** —— 数据在库里, 只是注入路径查不出来。
	origin, err := s.SceneMemoryOrigin("sm-1")
	if err != nil {
		t.Fatal(err)
	}
	if origin != sceneTestUserA {
		t.Fatalf("血缘审计读出 %q, 期望 %q", origin, sceneTestUserA)
	}
}

// ——— 契约 6: 身份锚是 uin, 昵称/群名片只做显示 (R10) ———

func TestScene_Identity_AnchorIsUin(t *testing.T) {
	s := sceneTestStore(t)
	sceneTestSeedScene(t, s, sceneTestGroupA, SceneKindGroup)

	// 两个人把群名片改成**同一个名字**。
	newA, err := s.ObserveMember(sceneTestGroupA, sceneTestUserA, "小明", "小明")
	if err != nil {
		t.Fatal(err)
	}
	newB, err := s.ObserveMember(sceneTestGroupA, sceneTestUserB, "小明", "小明")
	if err != nil {
		t.Fatal(err)
	}
	if !newA || !newB {
		t.Fatalf("两个不同的 uin 都应是本场景首次出现: newA=%v newB=%v", newA, newB)
	}

	// ★ 反面 (R10): 主键是 (scene_key, uin), 同名的人必须是**两行**。
	// 🔴 改坏成: 用 group_card / display_name 当归属键 → 两行并成一行 → len==1 → 红。
	members, err := s.SceneMembers(sceneTestGroupA)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 2 {
		t.Fatalf("同名的两个 uin 应是两行成员, 实得 %d 行: %+v", len(members), members)
	}
	seen := map[string]bool{}
	for _, m := range members {
		seen[m.UIN] = true
	}
	if !seen[sceneTestUserA] || !seen[sceneTestUserB] {
		t.Fatalf("成员行丢了 uin 锚: %+v", seen)
	}

	// ★ 反面: 称呼也必须挂在 uin 上 —— 给 A 起的称呼不许落到同名的 B 头上。
	if err := s.SetMemberAddress(sceneTestGroupA, sceneTestUserA, "老明"); err != nil {
		t.Fatal(err)
	}
	mb, err := s.SceneMemberByUIN(sceneTestGroupA, sceneTestUserB)
	if err != nil {
		t.Fatal(err)
	}
	if mb == nil {
		t.Fatal("B 的成员行丢了")
	}
	if mb.AddressAs != "" {
		t.Fatalf("给 A 起的称呼串到了同名的 B 上: %q", mb.AddressAs)
	}
	// 🔴 改坏成: SetMemberAddress 的 WHERE 用 display_name/group_card 定位 → 上面的称呼会串 → 红。

	// 显示字段与身份锚是两码事: 改名片不许动 uin。
	if _, err := s.ObserveMember(sceneTestGroupA, sceneTestUserA, "大名鼎鼎", "大明"); err != nil {
		t.Fatal(err)
	}
	ma, err := s.SceneMemberByUIN(sceneTestGroupA, sceneTestUserA)
	if err != nil {
		t.Fatal(err)
	}
	if ma.UIN != sceneTestUserA || ma.GroupCard != "大明" {
		t.Fatalf("改名后锚或显示字段不对: %+v", ma)
	}
}

// ——— 契约 7: 称呼按场景各存一份 (R32) ———

func TestScene_AddressIsSceneScoped(t *testing.T) {
	s := sceneTestStore(t)
	sceneTestSeedScene(t, s, sceneTestGroupA, SceneKindGroup)
	sceneTestSeedScene(t, s, sceneTestGroupB, SceneKindGroup)
	for _, key := range []string{sceneTestGroupA, sceneTestGroupB} {
		if _, err := s.ObserveMember(key, sceneTestUserA, "本名", "本名"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetMemberAddress(sceneTestGroupA, sceneTestUserA, "小外号"); err != nil {
		t.Fatal(err)
	}
	// ★ 反面 (R32): A 群叫外号, B 群必须是空的 (等着叫全名), 两边不许串。
	// 🔴 改坏成: 称呼存成全局一张表 (键只有 uin) → B 群会读到"小外号" → 红。
	mb, err := s.SceneMemberByUIN(sceneTestGroupB, sceneTestUserA)
	if err != nil {
		t.Fatal(err)
	}
	if mb.AddressAs != "" {
		t.Fatalf("A 群的称呼串到了 B 群: %q", mb.AddressAs)
	}
	if err := s.SetMemberAddress(sceneTestGroupB, sceneTestUserA, "全名"); err != nil {
		t.Fatal(err)
	}
	ma, _ := s.SceneMemberByUIN(sceneTestGroupA, sceneTestUserA)
	mb2, _ := s.SceneMemberByUIN(sceneTestGroupB, sceneTestUserA)
	if ma.AddressAs != "小外号" || mb2.AddressAs != "全名" {
		t.Fatalf("两场景的称呼互相覆盖了: A=%q B=%q", ma.AddressAs, mb2.AddressAs)
	}
}

// ——— 契约 8: 新人识别按 uin (R31), 沉淀字段不被改名冲掉 ———

func TestScene_ObserveMember_IsNewByUin_AndKeepsSediment(t *testing.T) {
	s := sceneTestStore(t)
	sceneTestSeedScene(t, s, sceneTestGroupA, SceneKindGroup)

	new1, err := s.ObserveMember(sceneTestGroupA, sceneTestUserA, "旧名", "旧名片")
	if err != nil {
		t.Fatal(err)
	}
	if !new1 {
		t.Fatal("首次出现的 uin 应判为新人")
	}
	if err := s.SetMemberAddress(sceneTestGroupA, sceneTestUserA, "老明"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMemberTaboo(sceneTestGroupA, sceneTestUserA, []string{"减肥"}); err != nil {
		t.Fatal(err)
	}

	// ★ 反面 (R31): 老成员**改了名**不许被当成新人。
	// 🔴 改坏成: 用"昵称没见过"判新人 (或整个删掉已有行重建) → 这里 new2 会是 true → 红。
	new2, err := s.ObserveMember(sceneTestGroupA, sceneTestUserA, "完全不同的新名字", "新名片")
	if err != nil {
		t.Fatal(err)
	}
	if new2 {
		t.Fatal("改名后又被判成新人 —— 它会重走一遍'不接梗不点名'的新人待遇")
	}

	// ★ 反面: 沉淀字段 (称呼/禁区/阶段) 不许被一次改名冲掉。
	// 🔴 改坏成: 用 INSERT OR REPLACE 整行覆盖 → 称呼与禁区被清空 → 红。
	m, err := s.SceneMemberByUIN(sceneTestGroupA, sceneTestUserA)
	if err != nil {
		t.Fatal(err)
	}
	if m.AddressAs != "老明" {
		t.Fatalf("改名冲掉了称呼: %q", m.AddressAs)
	}
	if len(m.Taboo) != 1 || m.Taboo[0] != "减肥" {
		t.Fatalf("改名冲掉了禁区: %+v", m.Taboo)
	}
	if m.DisplayName != "完全不同的新名字" || m.GroupCard != "新名片" {
		t.Fatalf("显示字段应更新为最新: %+v", m)
	}
	if m.FirstSeenTS == "" {
		t.Fatal("first_seen_ts 丢了 —— is_new 的现算就没有依据了")
	}
}

func TestScene_SetMemberAddress_UnknownMemberErrors(t *testing.T) {
	s := sceneTestStore(t)
	sceneTestSeedScene(t, s, sceneTestGroupA, SceneKindGroup)
	// ★ 反面: 成员不存在时**报错**, 不静默成功。
	// 🔴 改坏成: 不检查 RowsAffected → 这里 err==nil → 红 (P7: 静默的不是数据库, 是调用方)。
	if err := s.SetMemberAddress(sceneTestGroupA, "99999", "谁啊"); err == nil {
		t.Fatal("给不存在的成员写称呼居然成功了 —— 静默成功会让上游一直以为写进去了")
	}
}

func TestScene_IsNewMember_ComputedFromFirstSeen(t *testing.T) {
	s := sceneTestStore(t)
	sceneTestSeedScene(t, s, sceneTestGroupA, SceneKindGroup)
	// ⚠️ **场景必须拨到过去** —— "新人"这个词只在"早就认识的群"里有意义 ✓:
	// `first_seen_ts` 是**它自己第一次见到这个人**的时刻 ✓,
	// 所以刚建立的场景里所有人都是"今天第一次见" ✗ (那会让整群沉默 24h ✓,
	// 2026-10-16 生产上真的发生了 4 小时 —— 见 `newmember_test.go` 那三条)。
	past := time.Now().Add(-30 * 24 * time.Hour).Format(sceneTSLayout)
	if _, err := s.DB.Exec("UPDATE convo_scene SET first_seen_ts=? WHERE scene_key=?", past, sceneTestGroupA); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ObserveMember(sceneTestGroupA, sceneTestUserA, "小明", ""); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if isNew, err := s.IsNewMember(sceneTestGroupA, sceneTestUserA, now, 7*24*time.Hour); err != nil || !isNew {
		t.Fatalf("刚出现的人应在新人窗口内: isNew=%v err=%v", isNew, err)
	}
	// ★ 反面: 窗口是**现算**的 —— 窗口外就是老成员, 不需要任何人来"清标记"。
	// 🔴 改坏成: 读一个只写不清的 is_new 布尔列 → 这里永远是新人 → 红。
	if isNew, err := s.IsNewMember(sceneTestGroupA, sceneTestUserA, now.Add(30*24*time.Hour), 7*24*time.Hour); err != nil || isNew {
		t.Fatalf("30 天后仍是新人 —— 说明存了派生量而不是现算: isNew=%v err=%v", isNew, err)
	}
	if isNew, err := s.IsNewMember(sceneTestGroupA, "88888", now, 7*24*time.Hour); err != nil || !isNew {
		t.Fatalf("从没见过的 uin 应算新人: isNew=%v err=%v", isNew, err)
	}
}

// ——— 契约 9: §6.10.2 ① @我 → 接 ———

func TestAddressee_AtSelf(t *testing.T) {
	self := SelfIdentity{UIN: sceneTestSelf, Nickname: "小夜"}
	m := SceneMsg{SceneKey: sceneTestGroupA, UIN: sceneTestUserA, Text: "在吗", AtUINs: []string{sceneTestSelf}}
	ok, why := IsAddressedToMe(m, self, GroupConfig{})
	if !ok || why != ReasonAtSelf {
		t.Fatalf("@我 必须接: ok=%v why=%s", ok, why)
	}
	// 🔴 反面: 把 at 段的扫描删掉 (只看文本) → 这条会落到 ④undecidable → 红。
	// 🔴 反面: 把 at 段当文本比对 (at 里是 uin, 文本里没有 uin) → 同样会红。

	// 硬门槛: 任何配置都不许把"被 @ 必答"关掉。
	ok2, why2 := IsAddressedToMe(m, self, GroupConfig{ReplyWhenAtOthers: false, ThreadWindowMin: 1})
	if !ok2 || why2 != ReasonAtSelf {
		t.Fatalf("被 @ 是硬门槛, 不许被任何配置压掉: ok=%v why=%s", ok2, why2)
	}
}

// ——— 契约 10: §6.10.2 ② @别人 → 不接 (R29) ———

func TestAddressee_AtOtherDoesNotReply(t *testing.T) {
	self := SelfIdentity{UIN: sceneTestSelf, Nickname: "小夜"}
	base := SceneMsg{SceneKey: sceneTestGroupA, UIN: sceneTestUserA, Text: "@某人 你来看这个", AtUINs: []string{sceneTestUserB}}

	// ★ 反面 (R29): @的是别人 → 不接。零值配置下这是默认落点。
	// 🔴 改坏成: "@别人也接" (例如把 atOthersPresent 的结果丢掉) → 这里 ok==true → 红。
	ok, why := IsAddressedToMe(base, self, GroupConfig{})
	if ok || why != ReasonAtOthers {
		t.Fatalf("@别人 不该接: ok=%v why=%s", ok, why)
	}

	// 例外 1: @了别人但文本点了我的名 → 接 (§6.10.2 的"除非被点名")。
	named := base
	named.Text = "@某人 小夜刚才说的那个我也想看"
	ok2, why2 := IsAddressedToMe(named, self, GroupConfig{})
	if !ok2 || why2 != ReasonAtOthersNamed {
		t.Fatalf("被点名时应接: ok=%v why=%s", ok2, why2)
	}
	// 🔴 反面: 把"被点名"的例外删掉 → 这里 ok==false → 红。

	// 例外 2: 配置显式打开 (零值 false = 不接 —— 不许把默认设成接)。
	ok3, why3 := IsAddressedToMe(base, self, GroupConfig{ReplyWhenAtOthers: true})
	if !ok3 || why3 != ReasonAtOthersAllowed {
		t.Fatalf("配置显式打开时应接: ok=%v why=%s", ok3, why3)
	}
	// 🔴 反面: 把 ReplyWhenAtOthers 的处理正负颠倒 (零值=true) → 上面第一条会红。

	// @全体不算单独叫我 → 走"@别人"分支。
	all := SceneMsg{SceneKey: sceneTestGroupA, UIN: sceneTestUserA, Text: "通知一下", AtUINs: []string{"all"}}
	if ok4, why4 := IsAddressedToMe(all, self, GroupConfig{}); ok4 || why4 != ReasonAtOthers {
		t.Fatalf("@全体不是单独叫我: ok=%v why=%s", ok4, why4)
	}
}

// ——— 契约 11: §6.10.2 ④ 判不出来 → 沉默 (R30) ———

func TestAddressee_UndecidableSilent(t *testing.T) {
	self := SelfIdentity{UIN: sceneTestSelf, Nickname: "小夜"}
	m := SceneMsg{SceneKey: sceneTestGroupA, UIN: sceneTestUserA, Text: "今天天气不错"}
	// ★ 反面 (R30): 无 @、无线程 → 判不出来 → 沉默。
	// 🔴 改坏成: "判不出来 → 回一句" (返回 true) → 红。
	ok, why := IsAddressedToMe(m, self, GroupConfig{})
	if ok || why != ReasonUndecidable {
		t.Fatalf("判不出来必须沉默: ok=%v why=%s", ok, why)
	}

	// 上一条是别人、也没指明收件人是我 → 同样判不出来。
	m2 := m
	m2.HasPrev, m2.PrevUIN = true, sceneTestUserB
	if ok2, why2 := IsAddressedToMe(m2, self, GroupConfig{}); ok2 || why2 != ReasonUndecidable {
		t.Fatalf("上一条是别人且未指名 → 沉默: ok=%v why=%s", ok2, why2)
	}
}

// ——— 契约 12: §6.10.2 ③ 无 @ 按线程归属 (thread_window_min 的零值兜底) ———

func TestAddressee_ThreadWindow(t *testing.T) {
	self := SelfIdentity{UIN: sceneTestSelf, Nickname: "小夜"}
	base := time.Date(2026, 5, 11, 14, 0, 0, 0, time.Local)

	// 正面: 上一条是我说的, 3 分钟内 → 这是我能接的线程。
	m := SceneMsg{
		SceneKey: sceneTestGroupA, UIN: sceneTestUserA, Text: "对",
		HasPrev: true, PrevUIN: sceneTestSelf, PrevTS: base, TS: base.Add(3 * time.Minute),
	}
	ok, why := IsAddressedToMe(m, self, GroupConfig{})
	if !ok || why != ReasonThreadOurs {
		t.Fatalf("上一条是我说的且在窗口内应接: ok=%v why=%s", ok, why)
	}

	// ★ 反面: 超过 thread_window_min (默认 5) → 线程已经凉了 → 沉默。
	// 🔴 改坏成: 删掉窗口判断 (只看"上一条是谁") → 这里 ok==true → 红。
	m.TS = base.Add(6 * time.Minute)
	if ok2, why2 := IsAddressedToMe(m, self, GroupConfig{}); ok2 || why2 != ReasonUndecidable {
		t.Fatalf("超窗的线程不该接: ok=%v why=%s", ok2, why2)
	}

	// ★ 反面: 窗口的零值必须**兜底成 5**, 不是 0。
	// 🔴 改坏成: NormalizeGroupConfig 里去掉 ThreadWindowMin 的兜底 (0 保持 0) → 3 分钟那条会先红。
	if got := NormalizeGroupConfig(GroupConfig{}).ThreadWindowMin; got != DefaultThreadWindowMin {
		t.Fatalf("thread_window_min 零值应兜底为 %d, 实得 %d", DefaultThreadWindowMin, got)
	}

	// 上一条的收件人是我 (帧的 to) → 接。
	m3 := SceneMsg{
		SceneKey: sceneTestGroupA, UIN: sceneTestUserA, Text: "嗯",
		HasPrev: true, PrevUIN: sceneTestUserB, PrevToUIN: sceneTestSelf,
		PrevTS: base, TS: base.Add(2 * time.Minute),
	}
	if ok3, why3 := IsAddressedToMe(m3, self, GroupConfig{}); !ok3 || why3 != ReasonThreadToSelf {
		t.Fatalf("上一条指名给我应接: ok=%v why=%s", ok3, why3)
	}
	// 🔴 反面: 忽略 PrevToUIN → 这条会落到 undecidable → 红。

	// ★ 反面: 时间戳未提供 ≠ 超时 (纪律 #1) —— 零值不许把线程判据整个关掉。
	m4 := m3
	m4.TS, m4.PrevTS = time.Time{}, time.Time{}
	if ok4, why4 := IsAddressedToMe(m4, self, GroupConfig{}); !ok4 || why4 != ReasonThreadToSelf {
		t.Fatalf("时间戳缺失时不许当成'过期线程': ok=%v why=%s", ok4, why4)
	}
	// 🔴 反面: 把缺失的时间戳当成 0 时刻去减 (得到巨大的间隔) → 这条会红。
}

// ——— 契约 13: 私聊只有一个主体 —— 不许因为"判不出来"变哑巴 ———

func TestAddressee_PrivateScene_AlwaysReplies(t *testing.T) {
	self := SelfIdentity{UIN: sceneTestSelf, Nickname: "小夜"}
	m := SceneMsg{SceneKey: SceneKeyPrivate(sceneTestUserA), UIN: sceneTestUserA, Text: "今天天气不错"}
	ok, why := IsAddressedToMe(m, self, GroupConfig{})
	if !ok || why != ReasonPrivateScene {
		t.Fatalf("私聊里只有一个主体, 必须接: ok=%v why=%s", ok, why)
	}
	// 🔴 反面: 把私聊也丢进群判据 → 无 @ 无线程 → 沉默 → 它在私聊里变成哑巴 → 红。
}

// ——— 契约 14: 自己的回声不接 (防自问自答死循环) ———

func TestAddressee_SelfEchoIgnored(t *testing.T) {
	self := SelfIdentity{UIN: sceneTestSelf, Nickname: "小夜"}
	m := SceneMsg{SceneKey: sceneTestGroupA, UIN: sceneTestSelf, Text: "@小夜 你在干嘛"}
	ok, why := IsAddressedToMe(m, self, GroupConfig{})
	if ok || why != ReasonSelfEcho {
		t.Fatalf("自己发出的消息被回推时必须忽略: ok=%v why=%s", ok, why)
	}
	// 🔴 反面: 不判发言人与自己是否同一个 uin → 它会接自己的话, 无限自问自答 → 红。
}

// ——— 契约 15: 系统故障往说话倒, 语义判不出往沉默倒 (R28 的边界) ———

func TestSceneSceneLoadFailure_FallsBackToSpeaking(t *testing.T) {
	s := sceneTestStore(t)
	sceneTestSeedScene(t, s, sceneTestGroupA, SceneKindGroup)
	self := SelfIdentity{UIN: sceneTestSelf, Nickname: "小夜"}
	// 一条"语义判不出来"的消息 —— 正常路径下它的落点是沉默。
	m := SceneMsg{SceneKey: sceneTestGroupA, UIN: sceneTestUserA, Text: "今天天气不错"}

	ok, why := s.ResolveAddressee(m, self, GroupConfig{})
	if ok || why != ReasonUndecidable {
		t.Fatalf("正常路径: 判不出来应沉默, 实得 ok=%v why=%s", ok, why)
	}

	// 未登记的场景 → 规格 §6.1 与"读失败"同列 → 往说话倒。
	miss := m
	miss.SceneKey = sceneTestGroupB
	if ok2, why2 := s.ResolveAddressee(miss, self, GroupConfig{}); !ok2 || why2 != ReasonSceneMissing {
		t.Fatalf("场景未登记应往说话倒: ok=%v why=%s", ok2, why2)
	}

	// 场景键没给 → 同样是故障侧。
	none := m
	none.SceneKey = ""
	if ok3, why3 := s.ResolveAddressee(none, self, GroupConfig{}); !ok3 || why3 != ReasonNoSceneKey {
		t.Fatalf("没给场景键应往说话倒: ok=%v why=%s", ok3, why3)
	}

	// ★ 反面 (R28): **读库失败**必须往说话倒。
	// 🔴 改坏成: 把 LoadScene 的错误归进"判不出来 → 沉默" (return false, ReasonUndecidable) → 这里 ok==false → 红。
	// 🔴 改坏成: 读失败时 return false (任何 reason) → 红。
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	ok4, why4 := s.ResolveAddressee(m, self, GroupConfig{})
	if !ok4 {
		t.Fatalf("读场景失败时系统故障往说话倒, 实得 ok=false why=%s (纪律 #3 的反面)", why4)
	}
	if why4 != ReasonSceneLoadFailed {
		t.Fatalf("读失败的 reason 必须与'判不出来'分开: got %s want %s", why4, ReasonSceneLoadFailed)
	}
}

// ——— 契约 16: 白名单零值 = 一个都不接 (R33), 但私聊不受它管 ———

func TestScene_WhitelistEmptyMeansNone(t *testing.T) {
	// ★ 反面 (R33): 全零配置 = 群通路未启用 = 谁也不接。
	// 🔴 改坏成: "空白名单 = 接所有群" → 下面第一条会红。
	if WhitelistAllows(GroupConfig{}, "77777") {
		t.Fatal("空白名单却放行了 —— 硬门槛的默认值不许是全开")
	}
	on := GroupConfig{Enabled: true}
	if WhitelistAllows(on, "77777") {
		t.Fatal("Enabled=true 但白名单为空 = 没有任何群被启用")
	}
	on.Whitelist = []string{"77777"}
	if !WhitelistAllows(on, "77777") || WhitelistAllows(on, "88888") {
		t.Fatal("白名单命中判定不对")
	}
	if WhitelistAllows(GroupConfig{Whitelist: []string{"77777"}}, "77777") {
		t.Fatal("Enabled=false 时白名单不该生效")
	}

	// 私聊**不受群白名单约束**: 拿新门槛去卡旧通路 = 让私聊变哑巴。
	if !SceneAllowed(GroupConfig{}, SceneKeyPrivate(sceneTestUserA)) {
		t.Fatal("私聊被群白名单卡住了 —— 白名单管的是'接不接某个群的事件'")
	}
	if SceneAllowed(GroupConfig{}, sceneTestGroupA) {
		t.Fatal("群场景在空白名单下应被拦下")
	}
	// 🔴 反面: SceneAllowed 对所有场景都套白名单 → 上面私聊那条会红。
}

// ——— 契约 17: 多主体发言流按 uin 区分 (R9 / R10 的注入侧) ———

func TestScene_HistoryLine_MultiParty(t *testing.T) {
	// 三个人里有两个同名 —— 只按昵称渲染就分不出谁是谁。
	l1 := SceneHistoryLine("小明", "10001", "在吗")
	l2 := SceneHistoryLine("小明", "20002", "在")
	l3 := SceneHistoryLine("小红", "30003", "嗯")
	speaker := func(s string) string {
		if i := strings.Index(s, ": "); i >= 0 {
			return s[:i]
		}
		return s
	}
	got := []string{speaker(l1), speaker(l2), speaker(l3)}
	if got[0] == got[1] {
		t.Fatalf("两个同名的发言人渲染成了同一个说话人: %q", got[0])
	}
	// 🔴 反面 (R9): 改回 `你:/她:` 二元 (或去掉 uin 后缀只留昵称) → 上面两个前缀相同 → 红。
	// 注入里只出现 uin 的**后四位** (够区分人, 又不必把完整号写进提示词)。
	if !strings.Contains(l1, "(0001)") {
		t.Fatalf("注入行里没有身份锚的 uin 后缀: %q", l1)
	}
	if l1 != "小明(0001): 在吗" {
		t.Fatalf("行格式应稳定为 昵称(uin 后四位): 内容, 实得 %q", l1)
	}
	if l3 != "小红(0003): 嗯" {
		t.Fatalf("行格式应稳定为 昵称(uin 后四位): 内容, 实得 %q", l3)
	}
	// 空名字也不许退化成二元。
	if !strings.Contains(SceneHistoryLine("", "40004", "喂"), "(0004)") {
		t.Fatal("没有昵称时仍必须靠 uin 后缀区分人")
	}
}

// ——— 契约 18: 问题③ 我以什么身份回 ———

func TestScene_EffectiveSelfRole(t *testing.T) {
	if got := EffectiveSelfRole(RoleAdmin, RoleMember); got != RoleAdmin {
		t.Fatalf("配置显式优先: %s", got)
	}
	if got := EffectiveSelfRole("", RoleOwner); got != RoleOwner {
		t.Fatalf("库里次之: %s", got)
	}
	if got := EffectiveSelfRole("群主", ""); got != RoleOwner {
		t.Fatalf("中文角色名应归一化: %s", got)
	}
	// ★ 反面: 取不到一律落最保守档 member —— 未取到 ≠ admin, 更 ≠ owner。
	// 🔴 改坏成: 取不到时默认 admin/owner → 它会以为自己有管理权限 → 红。
	if got := EffectiveSelfRole("", ""); got != RoleMember {
		t.Fatalf("取不到角色必须落 member: %s", got)
	}
	if got := EffectiveSelfRole("superuser", "wat"); got != RoleMember {
		t.Fatalf("认不出来的角色必须落 member: %s", got)
	}
}

// ——— 契约 19: 敏感度判定的 fail-closed 与"别把日常闲聊判成私密" ———

func TestScene_ClassifySensitivity(t *testing.T) {
	if got := ClassifySensitivity(SensitivityPrivate, ""); got != SensitivityPrivate {
		t.Fatalf("显式 private: %s", got)
	}
	// ★ 反面: 大小写/空格不许绕过 —— private 的判定是 fail-closed 的。
	// 🔴 改坏成: 只匹配小写字面量 "private" → 下面两条会判成 relation → 红。
	if got := ClassifySensitivity("  PRIVATE ", ""); got != SensitivityPrivate {
		t.Fatalf("大小写绕过: %s", got)
	}
	if got := ClassifySensitivity("Secret", ""); got != SensitivityPrivate {
		t.Fatalf("别名绕过: %s", got)
	}
	if got := ClassifySensitivity("", "我最近在吃抗抑郁药"); got != SensitivityPrivate {
		t.Fatalf("兜底词表没拦住: %s", got)
	}
	// ★ 反面: 零值 = 未提供 ≠ 极值 —— 空声明 + 日常内容 → relation, 不许默认丢光。
	if got := ClassifySensitivity("", "今天吃了个火锅"); got != SensitivityRelation {
		t.Fatalf("日常内容被误判成私密: %s", got)
	}
	if got := ClassifySensitivity("", ""); got != SensitivityRelation {
		t.Fatalf("空输入的零值语义: %s", got)
	}
	// 🔴 反面: 把默认改成 private → 上面两条会红 (整个记忆系统会停止积累)。
}

// ——— 契约 20: 配置零值兜底与场景行自身的事实性 ———

func TestScene_ConfigZeroMeansDefault(t *testing.T) {
	n := NormalizeGroupConfig(GroupConfig{ThreadWindowMin: 0, HistorySubjects: -3})
	if n.ThreadWindowMin != DefaultThreadWindowMin || n.HistorySubjects != DefaultHistorySubjects {
		t.Fatalf("数值零值应兜底: %+v", n)
	}
	n2 := NormalizeGroupConfig(n)
	if n2.ThreadWindowMin != n.ThreadWindowMin || n2.HistorySubjects != n.HistorySubjects {
		t.Fatalf("兜底必须幂等: %+v vs %+v", n, n2)
	}
	// ★ 反面: bool 不动 —— 零值 false = 未提供 (不 @ / @别人不接), 由 overlay 保证语义。
	// 🔴 改坏成: 在 NormalizeGroupConfig 里把 bool 也"兜底"成 true → 上面这段会红。
	if n.ReplyWhenAtOthers || n.DefaultAtRecipient || n.Enabled {
		t.Fatal("bool 的零值语义被兜底改掉了")
	}
}

func TestScene_SaveScene_PreservesFirstSeen(t *testing.T) {
	s := sceneTestStore(t)
	// ⚠️ 刻意**注入一个过去的时刻**再断言它不变 —— 让实现自己盖"现在"是测不出来的:
	// 同一秒内两次 SaveScene 会得到逐字节相同的字符串, 覆盖了也看不出来 (第一版就踩了这个坑)。
	const seeded = "2020-01-01 00:00:00"
	if err := s.saveSceneSeed(Scene{SceneKey: sceneTestGroupA, Kind: SceneKindGroup, GroupID: "77777", FirstSeenTS: seeded}); err != nil {
		t.Fatal(err)
	}
	first, err := s.LoadScene(sceneTestGroupA)
	if err != nil || first == nil {
		t.Fatalf("load scene: %v %v", first, err)
	}
	if first.FirstSeenTS != seeded {
		t.Fatalf("显式给的 first_seen_ts 没被尊重: %q", first.FirstSeenTS)
	}
	// 没给 first_seen_ts 时, 首次插入必须自己盖一个。
	if err := s.saveSceneSeed(Scene{SceneKey: sceneTestGroupB, Kind: SceneKindGroup, GroupID: "88888"}); err != nil {
		t.Fatal(err)
	}
	b, err := s.LoadScene(sceneTestGroupB)
	if err != nil {
		t.Fatal(err)
	}
	if b.FirstSeenTS == "" {
		t.Fatal("首次插入必须盖 first_seen_ts")
	}
	// ★ 反面: first_seen_ts 是**事实**, 不许被后续 upsert 覆盖 (调用方通常不会带它)。
	// 🔴 改坏成: ON CONFLICT DO UPDATE 里带上 first_seen_ts=excluded.first_seen_ts → 下面会变 → 红。
	if err := s.saveSceneSeed(Scene{SceneKey: sceneTestGroupA, Stage: StageFamiliar}); err != nil {
		t.Fatal(err)
	}
	third, err := s.LoadScene(sceneTestGroupA)
	if err != nil {
		t.Fatal(err)
	}
	if third.FirstSeenTS != seeded {
		t.Fatalf("第二次 SaveScene 覆盖了 first_seen_ts: %q → %q", seeded, third.FirstSeenTS)
	}
	if third.Stage != StageFamiliar {
		t.Fatalf("其他字段应被更新: %+v", third)
	}
}

// ——— 契约 21: 写入口缺主体/缺场景是**报错**, 不是静默 ———

func TestScene_WritersRequireSubjectAndScene(t *testing.T) {
	s := sceneTestStore(t)
	// 🔴 反面: 把"没有主体"也变成 (false, nil) 静默丢弃 → 上层永远不知道记忆没落库 → 红。
	if _, err := s.SavePersonMemory(PersonMemory{MemID: "x", SceneKey: sceneTestGroupA, V: "无主体"}); err == nil {
		t.Fatal("关系层记忆缺 uin 必须报错 (静默丢弃 = P7 的坑)")
	}
	if _, err := s.SavePersonMemory(PersonMemory{MemID: "x", UIN: sceneTestUserA, V: "无场景"}); err == nil {
		t.Fatal("关系层记忆缺 scene_key 必须报错")
	}
	if _, err := s.SaveSceneMemory(SceneMemory{MemID: "x", V: "无场景"}); err == nil {
		t.Fatal("群公共记忆缺 scene_key 必须报错")
	}
	if _, err := s.SaveSceneMemory(SceneMemory{SceneKey: sceneTestGroupA, V: "无 mem_id"}); err == nil {
		t.Fatal("缺 mem_id 必须报错")
	}
	// 正面: 参数齐了就落库, 且 sensitivity 被归一到 relation (private 根本进不来)。
	ok, err := s.SavePersonMemory(PersonMemory{
		MemID: "pm-1", UIN: sceneTestUserA, SceneKey: sceneTestGroupA, K: "food", V: "爱吃辣",
	})
	if err != nil || !ok {
		t.Fatalf("正常写入应成功: saved=%v err=%v", ok, err)
	}
	got, err := s.PersonMemories(sceneTestGroupA, sceneTestUserA)
	if err != nil || len(got) != 1 {
		t.Fatalf("读回失败: %+v %v", got, err)
	}
	if got[0].Sensitivity != SensitivityRelation {
		t.Fatalf("落库的 sensitivity 只允许 relation, 实得 %q", got[0].Sensitivity)
	}
}

// saveSceneSeed **测试内**的场景播种 —— 原来那个生产函数 `SaveScene` 已删。
//
// ⚠️ 为什么删它: 它的 `ON CONFLICT DO UPDATE` 会**覆盖** `self_role` 与 `stage`,
// 而群消息那条路**每一轮**都要登记场景 —— 那是 `EnsureScene` 存在的全部理由
// (见它的注释)。**接上生产是危险的**, 而它只被测试当成播种工具用。
//
// ⚠️ 所以播种能力**搬进测试**: "把一行造出来"是测试的需要, **不该是一个生产 API**。
func (s *Store) saveSceneSeed(sc Scene) error {
	if sc.Kind == "" {
		sc.Kind = SceneKindOf(sc.SceneKey)
	}
	if sc.Platform == "" {
		sc.Platform = "qq"
	}
	if sc.Stage == "" {
		sc.Stage = StageStranger
	}
	now := clock.TodayTS()
	if sc.FirstSeenTS == "" {
		sc.FirstSeenTS = now
	}
	if sc.UpdatedTS == "" {
		sc.UpdatedTS = now
	}
	_, err := s.DB.Exec("INSERT INTO convo_scene"+
		"(scene_key, kind, platform, group_id, peer_uin, self_role, stage, intimacy, first_seen_ts, last_msg_ts, updated_ts) "+
		"VALUES(?,?,?,?,?,?,?,?,?,?,?) "+
		"ON CONFLICT(scene_key) DO UPDATE SET kind=excluded.kind, platform=excluded.platform, "+
		"group_id=excluded.group_id, peer_uin=excluded.peer_uin, self_role=excluded.self_role, "+
		"stage=excluded.stage, intimacy=excluded.intimacy, last_msg_ts=excluded.last_msg_ts, "+
		"updated_ts=excluded.updated_ts",
		sc.SceneKey, sc.Kind, sc.Platform, sc.GroupID, sc.PeerUIN, sc.SelfRole, sc.Stage,
		sc.Intimacy, sc.FirstSeenTS, sc.LastMsgTS, sc.UpdatedTS)
	return err
}

// TestScene_EnsureScene_PreservesFirstSeen **钉在生产路径上**。
//
// ⚠️ 原来那条 `TestScene_SaveScene_PreservesFirstSeen` 测的是 `SaveScene` ——
// 而它已删(它只被测试用, 且**接上生产是危险的**: 它会覆盖 `self_role` 与 `stage`)。
//
// ⚠️ 而那条性质 ——「`first_seen_ts` 是**事实**, 不许被后续 upsert 覆盖」——
// **在生产路径上是 `EnsureScene` 的**: 它的 `DO UPDATE SET` 里只有 `last_msg_ts` ✓。
// ⇒ 所以按"用例要钉生产路径"那条规矩, 它改钉在这里 ✓。
//
// ⚠️ 而这里**刻意把 first_seen_ts 拨回过去**再断言它不变 ——
// 让实现自己盖"现在"是测不出来的: 同一秒内两次会得到逐字节相同的字符串。
func TestScene_EnsureScene_PreservesFirstSeen(t *testing.T) {
	s := sceneTestStore(t)
	const seeded = "2020-01-01 00:00:00"
	if err := s.EnsureScene(sceneTestGroupA); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("UPDATE convo_scene SET first_seen_ts=? WHERE scene_key=?",
		seeded, sceneTestGroupA); err != nil {
		t.Fatal(err)
	}
	// 再走一遍 —— 这一次一定落到 ON CONFLICT 那条分支上。
	if err := s.EnsureScene(sceneTestGroupA); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadScene(sceneTestGroupA)
	if err != nil || got == nil {
		t.Fatalf("load scene: %v %v", got, err)
	}
	if got.FirstSeenTS != seeded {
		t.Fatalf("**first_seen_ts 被冲突分支覆盖了**: %q, 期望 %q", got.FirstSeenTS, seeded)
	}
}
