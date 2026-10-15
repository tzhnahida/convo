package convo

import (
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────
// §6.10.3 多主体发言流: **`昵称(uin 后四位): 内容`**
//
// 设计稿 L1487 原话: 「history **不能是"你:/她:"二元**, 必须是
// **`昵称(uin 后四位): 内容`** —— 否则模型分不清谁在说」。
//
// ⚠️ 而实现只做了"两段半": 它拼的是 `群友` + uin 尾号, **没有昵称**。
// ─────────────────────────────────────────────────────────────

// TestSceneHistoryLine_DesignFormat
//
// ⚠️ 格式本身是**契约**: 模型靠它分人。
func TestSceneHistoryLine_DesignFormat(t *testing.T) {
	got := SceneHistoryLine("小明", "10001", "在吗")
	if got != "小明(0001): 在吗" {
		t.Fatalf("格式不对: %q", got)
	}
	// ⚠️ **同名两个人必须分得开** —— 那正是尾号存在的理由。
	a := SceneHistoryLine("小明", "10001", "在吗")
	b := SceneHistoryLine("小明", "20002", "在吗")
	if a == b {
		t.Fatal("两个同名的 uin 渲染成了一模一样的行 —— 模型分不出谁在说")
	}
	// ⚠️ 没名字时落"某人", 而**仍然带尾号** ——
	// 也就是说"没名字"**不会退化成二元**。
	anon := SceneHistoryLine("", "10001", "在吗")
	if !strings.Contains(anon, "0001") {
		t.Fatalf("没名字时丢了 uin 尾号: %q", anon)
	}
}

// TestObserveMember_EmptyNameDoesNotClobber (反面)
//
// ⚠️ **零值 = 未提供, 不是"把它清空"。**
//
// 原来是无条件 `SET display_name=?` —— 于是**任何一次没带名字的调用都会抹掉昵称**。
// 而这条路是**每一轮**都要走的: 谁在某一轮带上了名字, 下一轮就被清掉了。
func TestObserveMember_EmptyNameDoesNotClobber(t *testing.T) {
	s := newTestStore(t)
	if err := EnsureSceneSchema(s); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ObserveMember("group:7001", "10001", "小明", "小明"); err != nil {
		t.Fatal(err)
	}
	// 再来一次**没带名字**的(现实中每一轮都会发生)。
	for i := 0; i < 3; i++ {
		if _, err := s.ObserveMember("group:7001", "10001", "", ""); err != nil {
			t.Fatal(err)
		}
	}
	m, err := s.SceneMemberByUIN("group:7001", "10001")
	if err != nil {
		t.Fatal(err)
	}
	if m == nil || m.DisplayName != "小明" {
		t.Fatalf("**昵称被空值抹掉了**: %+v", m)
	}
	// ⚠️ 而**非空**的新名字仍然要能覆盖(那是"他改了群名片")。
	if _, err := s.ObserveMember("group:7001", "10001", "大明", ""); err != nil {
		t.Fatal(err)
	}
	m2, _ := s.SceneMemberByUIN("group:7001", "10001")
	if m2.DisplayName != "大明" {
		t.Fatalf("改了名字却没更新: %q", m2.DisplayName)
	}
}

// TestOneBotSender_DisplayNamePrefersCard
//
// ⚠️ §6.10.4 原话:「不确定时用"你"或**群名片**, 绝不猜外号」——
// 群名片是**他自愿在这个群里用的名字**, 账号昵称是全局的。
func TestOneBotSender_DisplayNamePrefersCard(t *testing.T) {
	if got := (OneBotSender{Nickname: "全名", Card: "老明"}).DisplayName(); got != "老明" {
		t.Fatalf("群名片没优先: %q", got)
	}
	if got := (OneBotSender{Nickname: "全名"}).DisplayName(); got != "全名" {
		t.Fatalf("没名片时应当退回昵称: %q", got)
	}
	// ⚠️ 都没有 → **空串**(= 未提供), 不是"他叫空名字"。
	if got := (OneBotSender{}).DisplayName(); got != "" {
		t.Fatalf("两个都空时应当返回空串, 实得 %q", got)
	}
	// 全是空格也算没给。
	if got := (OneBotSender{Nickname: "  "}).DisplayName(); got != "" {
		t.Fatalf("空白昵称应当算未提供, 实得 %q", got)
	}
}
