package convo

import (
	"strings"
	"testing"
)

// P1: 决策时的「发言预约」要能立刻挡住并发突发。
func TestSpeechBudget_ReservationBlocksBurst(t *testing.T) {
	s := newTestStore(t)
	if err := EnsureSpeechGuardSchema(s.DB); err != nil {
		t.Fatal(err)
	}
	e := &Engine{Store: s, Cfg: DefaultConfig()}
	sc := SceneMsg{SceneKey: sceneWireKey}
	if ok, why := e.speechBudget(sc); !ok {
		t.Fatalf("一开始就被挡了: %s", why)
	}
	if err := s.MarkSpeakAttempt(sceneWireKey); err != nil {
		t.Fatal(err)
	}
	if ok, why := e.speechBudget(sc); ok || !strings.Contains(why, "话太密") {
		t.Fatalf("预约之后预算没有立刻生效: ok=%v why=%q", ok, why)
	}
}

// P2: 只有「像在对它说」才允许会话中直接接。
func TestLooksAddressedToIt(t *testing.T) {
	yes := []string{"你呢", "你们在干嘛", "这个怎么弄？", "你在吗", "今天好累啊，你呢", "真的吗"}
	for _, s := range yes {
		if !looksAddressedToIt(s) {
			t.Errorf("%q 应当算在对它说", s)
		}
	}
	no := []string{"今天好累", "耳机复活了", "哈哈哈", "", "这段代码怎么调", "是吧"}
	for _, s := range no {
		if looksAddressedToIt(s) {
			t.Errorf("%q 不该算在对它说", s)
		}
	}
}

// P3: 纯标点等于没说话。
func TestIsContentlessText(t *testing.T) {
	for _, s := range []string{"？", "?", "。。。", "…", "  ", "！！！"} {
		if !isContentlessText(s) {
			t.Errorf("%q 应当算空内容", s)
		}
	}
	for _, s := range []string{"嗯", "好", "行吧", "笑死"} {
		if isContentlessText(s) {
			t.Errorf("%q 是正常回复, 不该被拦", s)
		}
	}
}

// P3: 同义句比较要忽略标点空白。
func TestNormalizeForCompare(t *testing.T) {
	if normalizeForCompare("耳机复活了！") != normalizeForCompare("耳机 复活了") {
		t.Fatal("归一化后应当相等")
	}
	if normalizeForCompare("耳机复活了") == normalizeForCompare("耳机线断了") {
		t.Fatal("不同的话不该相等")
	}
}

// P4: 更宽的「自己的过去」说法也要被事实层抓住。
func TestGrounding_SoftHistoryClaim(t *testing.T) {
	in := DialogInput{UserText: "今天群里聊到考试"}
	for _, s := range []string{"我高中也这样", "我们学校也差不多", "我当年也考过"} {
		if hits := GroundingViolations(s, in); len(hits) == 0 {
			t.Errorf("%q 应当命中第一人称经历", s)
		}
	}
}
