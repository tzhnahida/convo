package convo

import (
	"strings"
	"testing"
)

func cast(name string, r RelationKind) CastMember {
	return CastMember{Name: name, Relation: r, History: "一起长大的", Status: "在整理书"}
}

// ⚠️ 关系类型是**闭集**。多一类就会长出「暧昧对象」这种。
func TestRelationKind_FourOnly(t *testing.T) {
	for _, r := range []RelationKind{RelationCompanion, RelationRival, RelationFamily, RelationMentor} {
		if !r.Valid() || r.Label() == "" {
			t.Fatalf("%s 应当是合法关系", r)
		}
	}
	for _, r := range []RelationKind{"", "lover", "青梅竹马", "crush"} {
		if r.Valid() {
			t.Fatalf("%q 不该合法", r)
		}
	}
}

// ⚠️ **低于 2 人 = 没有关系层** —— 一个谁都不认识的角色讲不出「我的一天」。
func TestValidateWorldBible_NeedsTwoPeople(t *testing.T) {
	one := WorldBible{Cast: []CastMember{cast("兰那罗", RelationCompanion)}}
	if err := ValidateWorldBible(one); err == nil {
		t.Fatal("只有一个人应当被拒")
	}
	two := WorldBible{Cast: []CastMember{cast("兰那罗", RelationCompanion), cast("散兵", RelationRival)}}
	if err := ValidateWorldBible(two); err != nil {
		t.Fatalf("两个人应当通过: %v", err)
	}
}

// ⚠️ **动态配角不占定稿配额** (§2.3 明文)。
// 合成一个上限的话, 动态升级会把最熟的人挤出去。
func TestNormalizeWorldBible_DynamicDoesNotConsumeQuota(t *testing.T) {
	b := WorldBible{}
	for i := 0; i < 5; i++ {
		b.Cast = append(b.Cast, cast(string(rune('a'+i)), RelationCompanion))
	}
	for i := 0; i < 12; i++ {
		m := cast(string(rune('A'+i)), RelationCompanion)
		m.Dynamic = true
		b.Cast = append(b.Cast, m)
	}
	n := NormalizeWorldBible(b)
	if len(n.Fixed()) != 5 {
		t.Fatalf("定稿 5 人应当一个不少, got %d", len(n.Fixed()))
	}
	if len(n.Dynamic()) != CastMaxDynamic {
		t.Fatalf("动态裁到 %d, got %d", CastMaxDynamic, len(n.Dynamic()))
	}
	if err := ValidateWorldBible(n); err != nil {
		t.Fatalf("裁剪之后应当合法: %v", err)
	}
}

// ⚠️ 这几条是**产品红线**, 不是内容偏好。
func TestCastGuard_RedLines(t *testing.T) {
	for _, bad := range []string{"他的恋人", "暧昧对象", "初中生", "自残倾向", "虐待过它"} {
		m := CastMember{Name: "某人", Relation: RelationCompanion, History: bad}
		if err := CastGuard(m); err == nil {
			t.Fatalf("应当拦下: %q", bad)
		}
	}
	good := CastMember{Name: "兰那罗", Relation: RelationCompanion, History: "在森林里一起待过一个雨季"}
	if err := CastGuard(good); err != nil {
		t.Fatalf("正常描述被拦了: %v", err)
	}
}

// ⚠️ 超长 = 抄的。逐字复述原作文本是 §2.3 明文禁止的。
func TestCastGuard_TooLongIsCopying(t *testing.T) {
	long := make([]rune, WorldFactMaxRunes+1)
	for i := range long {
		long[i] = '字'
	}
	m := CastMember{Name: "某人", Relation: RelationCompanion, History: string(long)}
	if err := CastGuard(m); err == nil {
		t.Fatal("超长应当被拦下")
	}
}

func TestValidateWorldBible_DuplicateName(t *testing.T) {
	b := WorldBible{Cast: []CastMember{cast("散兵", RelationRival), cast("散兵", RelationRival)}}
	if err := ValidateWorldBible(b); err == nil {
		t.Fatal("同名应当被拒")
	}
}

// ⚠️ 现编的必须**自曝** —— 否则模型会把它当原作设定讲出去, 而群里必有懂的人。
func TestWorldPrompt_MarksInventionAndSeparatesNow(t *testing.T) {
	b := WorldBible{
		Self: "须弥的草神",
		Setting: WorldSetting{
			Places: []WorldFact{{Text: "净善宫", Source: "原神"}},
			Rules:  []WorldFact{{Text: "我每周三要整理档案"}},
		},
		Cast: []CastMember{
			{Name: "散兵", Relation: RelationRival, History: "吵过一次", Status: "在写论文"},
			{Name: "兰那罗", Relation: RelationCompanion, Status: "在种蘑菇"},
		},
	}
	got := WorldPrompt(b)
	// ⚠️ 有出处的条目**正常注入就够了** —— 出处是给审计看的, 不是给它念的。
	// 标注只加在**现编**那一侧: 那个标记的唯一作用是阻止模型把现编当原作设定讲出去。
	if !strings.Contains(got, "净善宫") {
		t.Fatal("有出处的条目应当正常注入")
	}
	if strings.Contains(got, "净善宫 (这条是你自己编的") {
		t.Fatal("有出处的条目被误标成现编")
	}
	if !strings.Contains(got, "自己编的") {
		t.Fatal("现编的条目没有自曝 —— 它会当成原作设定讲出去")
	}
	if !strings.Contains(got, "不在这个群里") {
		t.Fatal("缺少「这些人不在这个群里」的护栏 —— 不说清它会把他们当群友")
	}
	if !strings.Contains(got, "【他们现在】") {
		t.Fatal("L3 应当单独成段")
	}
	if !strings.Contains(got, "散兵 现在在写论文") {
		t.Fatalf("L3 内容不对:\n%s", got)
	}
}

func TestWorldPrompt_EmptyIsSafe(t *testing.T) {
	if WorldPrompt(WorldBible{}) == "" {
		t.Fatal("空世界观也该有骨架, 不该崩")
	}
}

// ⚠️ §2.3 与 §3.7 之间**唯一**的那根线。
func TestNightPeersFromCast(t *testing.T) {
	b := WorldBible{Cast: []CastMember{
		{Name: "老师", Relation: RelationMentor, Status: "在讲课"},
		{Name: "兰那罗", Relation: RelationCompanion, Status: "在种蘑菇"},
		{Name: "散兵", Relation: RelationRival, Status: "在写论文"},
		{Name: "", Relation: RelationCompanion},
	}}
	peers := NightPeersFromCast(b)
	if len(peers) != 3 {
		t.Fatalf("空名字应当被跳过, got %d", len(peers))
	}
	if peers[0].Name != "兰那罗" {
		t.Fatalf("同伴权重最高, 应当排第一, got %s", peers[0].Name)
	}
	if peers[2].Name != "老师" {
		t.Fatalf("师长权重最低, got %s", peers[2].Name)
	}
	if peers[0].Brief != "在种蘑菇" {
		t.Fatalf("brief 应当取 L3 状态, got %q", peers[0].Brief)
	}
}

// ⚠️ 不认识的关系**不参与**夜间社交 (fail-closed), 而不是给个默认权重。
func TestRelationWeight_FailClosed(t *testing.T) {
	if relationWeight(RelationKind("weird")) != 0 {
		t.Fatal("未知关系必须权重 0")
	}
	if relationWeight(RelationCompanion) <= relationWeight(RelationMentor) {
		t.Fatal("同伴应当比师长亲")
	}
}

func TestWorldBible_RoundTrip(t *testing.T) {
	s := newTestStore(t)
	owner := "u1"
	if _, ok, _ := s.LoadWorldBible(owner); ok {
		t.Fatal("空库不该有世界观")
	}
	b := WorldBible{Self: "草神", Cast: []CastMember{cast("兰那罗", RelationCompanion), cast("散兵", RelationRival)}}
	if err := s.SaveWorldBible(owner, b); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.LoadWorldBible(owner)
	if err != nil || !ok {
		t.Fatalf("读不回来: ok=%v err=%v", ok, err)
	}
	if len(got.Cast) != 2 || got.Self != "草神" {
		t.Fatalf("内容不对: %+v", got)
	}
	// 校验不过的**不许写进去** —— 半个世界观比没有更糟。
	if err := s.SaveWorldBible(owner, WorldBible{Cast: []CastMember{cast("只有一个", RelationCompanion)}}); err == nil {
		t.Fatal("非法世界观应当被拒")
	}
	after, _, _ := s.LoadWorldBible(owner)
	if len(after.Cast) != 2 {
		t.Fatal("被拒的写入不该改动既有世界观")
	}
}

// ⚠️ **本批的头号契约** (用户实测反馈):
//
//	上一版护栏写的是「也别声称他们在线」, 被读成了**"不许提他们"** ——
//	结果它从来不说自己的生活: 问它今天干嘛, 它说"闭关看书"。
//
//	而配角的**全部意义**就是让它的生活有内容 (服务世界观), 不是要被保护起来的。
func TestCastNote_InvitesTalkingAboutThem(t *testing.T) {
	b := WorldBible{
		Self: "小夜",
		Cast: []CastMember{
			{Name: "阿岚", Relation: RelationCompanion, History: "一起住了三年", Status: "在准备考试"},
		},
	}
	note := WorldPrompt(b)
	// 允诺必须**正面**写出来 —— 只删掉禁令是不够的, 模型会把沉默当默认。
	if !strings.Contains(note, "随时可以提他们") {
		t.Fatalf("没有正面允诺提他们 —— 它会继续不说自己的生活:\n%s", note)
	}
	// ⚠️ 那条把生活说成禁区的措辞**不许回来**。
	if strings.Contains(note, "也别声称他们在线") {
		t.Fatal("旧的禁令回来了 —— 它会让它闭口不谈自己的生活")
	}
	// 该拦的两件仍要拦。
	if !strings.Contains(note, "别把他们说成群友") {
		t.Fatal("缺「别说成群友」—— 它会在群里认错人")
	}
	if !strings.Contains(note, "别拿他们去评价群里的人") {
		t.Fatal("缺「别拿他们评价群里的人」")
	}
	// 光报"我在看书"就是没生活 —— 这条警示必须在。
	if !strings.Contains(note, "你没有生活") {
		t.Fatal("缺「只报我在看书 = 你没有生活」的警示")
	}
}
