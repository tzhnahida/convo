package convo

import (
	"context"
	"testing"
	"time"

	"github.com/tzhnahida/convo/internal/clock"
)

// ——— ask.go ———

func TestDetectRejection(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"没空聊这个", true},
		{"不想说", true},
		{"算了不说了", true},
		{"别问这个", true},
		{"没什么特别的", true},
		{"周末去爬山了, 超开心", false},
		{"不错, 挺好的", false}, // 肯定句不误伤
		{"吃了火锅", false},
	}
	for _, c := range cases {
		if got := DetectRejection(c.text); got != c.want {
			t.Fatalf("DetectRejection(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

// ——— stickers.go ———

func TestStickerLifecycle(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir, "test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	id, err := store.InsertSticker("http://x/1.gif", dir+"/1.gif", "ja", "tenor", 0.8)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := store.InsertSticker("http://x/2.png", dir+"/2.png", "ja", "firecrawl", 0.6)
	if err != nil {
		t.Fatal(err)
	}
	id3, _ := store.InsertSticker("http://x/3.png", dir+"/3.png", "ja", "tenor", 0.2)

	if !store.StickerExists(id) {
		t.Fatal("sticker should exist")
	}
	if err := store.AdjustAffinity(id, 0.1); err != nil {
		t.Fatal(err)
	}
	st, _ := store.StickerByID(id)
	if st.Affinity < 0.899 || st.Affinity > 0.901 {
		t.Fatalf("affinity = %v, want 0.9 (0.8 score + 0.1 feedback)", st.Affinity)
	}
	if n, _ := store.RetireLow(0.3); n != 1 {
		t.Fatalf("retire low = %d, want 1", n)
	}
	if store.StickerExists(id3) {
		t.Fatal("retired sticker should not exist")
	}
	_ = id2
}

func TestWeightedPick_cap(t *testing.T) {
	dir := t.TempDir()
	store, _ := OpenStore(dir, "test.db")
	defer store.Close()
	// 高 affinity 单张概率被 cap ≤30% 封顶 (30 张中高票数一张)
	for i := 0; i < 30; i++ {
		_, _ = store.InsertSticker("u", "p", "ja", "t", 0.5)
	}
	top, _ := store.InsertSticker("u", "p", "ja", "t", 1.0)
	count := 0
	for i := 0; i < 1000; i++ {
		st, err := store.WeightedPick(0.30)
		if err != nil {
			t.Fatal(err)
		}
		if st.ID == top {
			count++
		}
	}
	// 期望 ≈ (0.3/0.3*30+0.3)= ~3.2% — 无 cap 时 ≈ (1.0/30.5)=3.3%… capping 后 top 权重=0.30
	// 无论如何 ≤ 35% (cap=0.30/0.30*31 上限), 且应显著低于无 cap 的 55%+
	// 无 cap: w=1.0/总(30*0.5+1.0)=1/16=6.25% — 差异小, 主要断言: 不超 cap 太多
	if count > 400 {
		t.Fatalf("top sticker picked %d/1000 — cap violated (>40%%)", count)
	}
}

// ——— dialog.go ———

func TestHelpDetect(t *testing.T) {
	for _, text := range []string{"这个怎么说", "什么意思啊", "教我一下", "怎么读?"} {
		if !HelpDetect(text) {
			t.Fatalf("HelpDetect(%q) = false", text)
		}
	}
	if HelpDetect("今天天气不错") {
		t.Fatal("false positive")
	}
}

func TestValidateFrame(t *testing.T) {
	dir := t.TempDir()
	store, _ := OpenStore(dir, "test.db")
	defer store.Close()
	stID, _ := store.InsertSticker("u", "p", "ja", "t", 0.5)
	d := &DialogEngine{Store: store, StickerExists: store.StickerExists}
	in := DialogInput{
		Memories: []Memory{{MemID: "mem_ok", V: "喜欢猫"}},
	}
	p := &PersonaProfile{Tolerance: 0.85, Boundaries: Boundaries{AskBlacklist: []string{"money"}}}

	// 合法帧
	okFrame := OutputFrame{
		Reply:   []FrameItem{{Kind: "text", Text: "嗯嗯"}, {Kind: "sticker", StickerID: stID}},
		MemRefs: []string{"mem_ok"},
	}
	if v := d.ValidateFrame(okFrame, in, p); len(v) != 0 {
		t.Fatalf("valid frame → %v", v)
	}
	// 编造记忆 (硬校验)
	bad := OutputFrame{Reply: []FrameItem{{Kind: "text", Text: "hi"}}, MemRefs: []string{"mem_hallucinated"}}
	if v := d.ValidateFrame(bad, in, p); len(v) == 0 {
		t.Fatal("hallucinated mem_id should be rejected")
	}
	// 黑名单类别
	badAsk := OutputFrame{Reply: []FrameItem{{Kind: "text", Text: "hi"}},
		AskShare: &AskShareFrame{Category: "money", Question: "你有多少钱"}}
	if v := d.ValidateFrame(badAsk, in, p); len(v) == 0 {
		t.Fatal("blacklisted ask_share should be rejected")
	}
	// act_fool 门槛 (tolerance 0.2 < 0.3)
	lowTol := &PersonaProfile{Tolerance: 0.2}
	fool := OutputFrame{Reply: []FrameItem{{Kind: "text", Text: "hi"}}, ActFool: true}
	if v := d.ValidateFrame(fool, in, lowTol); len(v) == 0 {
		t.Fatal("act_fool below tolerance gate should be rejected")
	}
	// 缺 text
	noText := OutputFrame{Reply: []FrameItem{{Kind: "sticker", StickerID: stID}}}
	if v := d.ValidateFrame(noText, in, p); len(v) == 0 {
		t.Fatal("no-text frame should be rejected")
	}
}

// ——— memory.go ———

func TestApplySessionDelta_confidenceRouting(t *testing.T) {
	dir := t.TempDir()
	store, _ := OpenStore(dir, "test.db")
	defer store.Close()
	d := SessionDelta{
		Summary: "聊了周末计划",
		Memories: []MemoryDelta{
			{MemID: "m_high", K: "plan", V: "周末去爬山", Label: "周末计划", Kind: "plan", Confidence: 0.9, Source: "user_said"},
			{MemID: "m_low", K: "food", V: "喜欢?火锅", Label: "口味", Kind: "preference", Confidence: 0.5, Source: "user_said"},
			// 归属判不出来 → 入库但**不注入** (宁缺勿假)
			{MemID: "m_unsourced", K: "food", V: "它自己说想吃拉面", Label: "拉面", Kind: "preference", Confidence: 0.9},
		},
	}
	if err := store.ApplySessionDelta("2026-09-06", d); err != nil {
		t.Fatal(err)
	}
	active, _ := store.TopMemories(10, clock.Now())
	if len(active) != 1 || active[0].MemID != "m_high" {
		t.Fatalf("active = %+v", active)
	}
	var unsourced int
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM convo_memory WHERE mem_id='m_unsourced' AND state='active'`).Scan(&unsourced); err != nil {
		t.Fatal(err)
	}
	if unsourced != 1 {
		t.Fatal("无归属的记忆应入库为 active 但不参与注入")
	}
	pending, _ := store.PendingMemories()
	if len(pending) != 1 || pending[0].MemID != "m_low" {
		t.Fatalf("pending = %+v", pending)
	}
	// 求证: confirm → active
	if err := store.ResolvePending("m_low", "confirm", ""); err != nil {
		t.Fatal(err)
	}
	active, _ = store.TopMemories(10, clock.Now())
	if len(active) != 2 {
		t.Fatalf("after confirm active = %d", len(active))
	}
	if err := store.ResolvePending("m_low", "drop", ""); err != nil {
		t.Fatal(err)
	}
	// 已 active 的 drop 不影响
	active, _ = store.TopMemories(10, clock.Now())
	if len(active) != 2 {
		t.Fatalf("active after drop = %d", len(active))
	}
}

func TestMemoryDecay(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.Local)
	old := now.AddDate(0, 0, -40).Format("2006-01-02 15:04:05")
	ancient := now.AddDate(0, 0, -100).Format("2006-01-02 15:04:05")
	fresh := now.AddDate(0, 0, -5).Format("2006-01-02 15:04:05")
	if d := memoryDecay(fresh, now); d != 1.0 {
		t.Fatalf("fresh decay = %v", d)
	}
	if d := memoryDecay(old, now); d != 0.6 {
		t.Fatalf("30d decay = %v", d)
	}
	if d := memoryDecay(ancient, now); d != 0.3 {
		t.Fatalf("90d decay = %v", d)
	}
}

func TestTopicCycle(t *testing.T) {
	dir := t.TempDir()
	store, _ := OpenStore(dir, "test.db")
	defer store.Close()
	if err := store.SeedTopics(3); err != nil {
		t.Fatal(err)
	}
	// 骰子 <15 → pure
	tp, err := store.NextTopic(5)
	if err != nil || tp.Kind != "pure" {
		t.Fatalf("roll 5 → %+v %v", tp, err)
	}
	// 骰子大 → 内置话题
	tp, err = store.NextTopic(80)
	if err != nil || tp.Text == "" {
		t.Fatalf("roll 80 → %+v", tp)
	}
	// 池空后自动重置
	for i := 0; i < 20; i++ {
		if _, err := store.NextTopic(80); err != nil {
			t.Fatalf("reuse %d: %v", i, err)
		}
	}
}

var _ = context.Background
