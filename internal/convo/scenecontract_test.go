package convo

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// ⚠️ **本批的头号契约** —— 它防的是整整一类 bug, 而不是某一次:
// 任何"从别处取了输入、却从私聊出口发出去"的路径都会被这一条拦住。
//
// 2026-10-14 生产事故: breakreply 钩子取到了**别人在群里**说的话,
// 然后在 owner 私聊里回了一轮防守话。
func TestProactive_RefusesForeignScene(t *testing.T) {
	ch := &countingChatter{out: "{}"}
	e, _ := groupSpeakEngine(t, ch)
	p := &PersonaProfile{Name: "小夜"}
	// 群里的话从私聊出口走 → **一次模型调用都不该有**。
	if err := e.proactive(context.Background(), p,
		DialogInput{SceneKey: "group:1034712035", ActionKind: "breakreply", UserText: "群友说的话"}, "rounds", "2026-10-14"); err != nil {
		t.Fatal(err)
	}
	if ch.calls != 0 {
		t.Fatal("群场景的输入从私聊出口发出去了 —— 场景一致性契约没生效")
	}
	// 反面: 私聊场景必须照常走 —— 否则这条契约把正常路径也堵死了。
	_ = e.proactive(context.Background(), p,
		DialogInput{SceneKey: DefaultSceneKey, ActionKind: "breakreply", UserText: "主人说的话"}, "rounds", "2026-10-14")
	if ch.calls == 0 {
		t.Fatal("私聊场景被误拦了 —— 契约收得太紧")
	}
}

// ⚠️ 取样必须按场景过滤 —— 这是事故的第一道闸。
func TestLastUserTextIn_FiltersScene(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	if _, err := e.Store.InsertMessageFull("user", "qq", "text", "私聊的话", MsgMeta{SceneKey: DefaultSceneKey}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.InsertMessageFull("user", "qq", "text", "群里的话", MsgMeta{SceneKey: "group:1"}); err != nil {
		t.Fatal(err)
	}
	got, err := e.lastUserTextIn(DefaultSceneKey)
	if err != nil {
		t.Fatal(err)
	}
	if got != "私聊的话" {
		t.Fatalf("取样串场景了: %q —— 群里最新那条会被捞进私聊", got)
	}
	// 而不过滤的那个版本确实会捞到群里的话 —— 这就是原来那条 bug。
	all, _ := e.lastUserText()
	if all != "群里的话" {
		t.Fatalf("这条测试的前提变了, 需要重新审视: %q", all)
	}
}

// ⚠️ tick 每分钟一次, 而触发条件会**持续成立** ——
// 没有冷却 = 条件成立期间每分钟发一条 (实测 21 ai : 2 user 就是这么来的)。
func TestHookClaim_CooldownAndAccounting(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	d := "2026-10-14"
	// ⚠️ **检查与记账是一对** —— 我第一版只检查不记账, 冷却永远放行。
	if !e.hookClaim("breakreply", HookBreakCooldownMin, d) {
		t.Fatal("从没触发过就该放行")
	}
	// 记账必须真的落库 —— 否则下一次读到的还是空。
	if _, ok, _ := e.Store.LastCounterTS("breakreply"); !ok {
		t.Fatal("放行了却没记账 —— 冷却闸等于不存在")
	}
	if e.hookClaim("breakreply", HookBreakCooldownMin, d) {
		t.Fatal("刚触发过又放行 —— 它会每分钟发一条")
	}
	if !e.hookClaim("steal_reply", HookStealCooldownMin, d) {
		t.Fatal("两个钩子的冷却必须各算各的")
	}
	if !e.hookClaim("breakreply", 0, d) {
		t.Fatal("gapMin=0 应当表示不设冷却")
	}
}

// ⚠️ 2026-10-14 生产事故: 对方连说「知道吗」「你说说细节」「**你去查查**」,
// 它全靠人设躲开, 零检索 —— 因为触发词表里根本没有"查/搜"这类词。
func TestLiveQuestion_ExplicitLookupCommand(t *testing.T) {
	for _, s := range []string{
		"你去查查", "帮我查一下", "搜一下这个", "上网查查", "百度一下",
		"尊界v800刹车断裂知道吗", "听说过这个吗", "是不是真的",
	} {
		if !LiveQuestion(s) {
			t.Fatalf("应当触发检索: %q", s)
		}
	}
	// 反面照旧: 闲聊**不许**触发 (否则它回什么都慢半拍)。
	for _, s := range []string{"", "在吗", "今天好累啊", "你喜欢吃什么", "晚安", "哈哈"} {
		if LiveQuestion(s) {
			t.Fatalf("闲聊不该触发检索: %q", s)
		}
	}
}

// ⚠️ 桥接方用**它自己的 cwd** 解析相对路径 ——
// convo 的 cwd 是 /home/tzh/convo, nimbus 的是 /home/tzh/.nimbus。
// 实测: 群表情三次发送失败 "image 文件不存在", 而文件在 convo 那边是好的。
func TestAbsMediaPath(t *testing.T) {
	got := AbsMediaPath("data/convo/stickers/ja/x.gif")
	if !filepath.IsAbs(got) {
		t.Fatalf("应当转成绝对路径: %q", got)
	}
	if filepath.Base(got) != "x.gif" {
		t.Fatalf("路径尾巴被改了: %q", got)
	}
	// ⚠️ 用**平台自身规则**构造绝对路径 —— 写死 "/already/abs.gif" 在 Windows 上
	// 根本不是绝对路径 (Windows 要盘符), 测试会红, 而生产跑在 Linux 上是好的。
	abs := filepath.Join(t.TempDir(), "abs.gif")
	if !filepath.IsAbs(abs) {
		t.Fatalf("夹具本身不是绝对路径, 这条测试没意义: %q", abs)
	}
	if AbsMediaPath(abs) != abs {
		t.Fatal("已经是绝对路径的不该再动")
	}
	if AbsMediaPath("") != "" {
		t.Fatal("空路径不该变成 cwd")
	}
	if !strings.HasSuffix(AbsMediaPath("a.gif"), "a.gif") {
		t.Fatal("相对路径也要能转")
	}
}
