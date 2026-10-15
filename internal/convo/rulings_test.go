package convo

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// ═══ 裁定 1:「被顶」用廉价判据 ═══

func TestChallengedOf_NeedsSharedCue(t *testing.T) {
	// 与它上一句**毫无关系**的一句否定 —— 不许算成被顶。
	if _, ok := ChallengedOf("不是这样，今天下雨了", "猫喜欢吃鱼"); ok {
		t.Fatal("在别的话题上说「不是」被算成了被顶 —— 它会去重组织一个跟这事无关的立场")
	}
	// 有共享线索 + 否定 → 算。
	q, ok := ChallengedOf("不是这样，猫才不喜欢吃鱼", "猫喜欢吃鱼吧")
	if !ok || q == "" {
		t.Fatal("明明在顶同一件事却没识别出来")
	}
	// 只有共享线索、没有否定 → 不算。
	if _, ok := ChallengedOf("猫喜欢吃鱼啊", "猫喜欢吃鱼吧"); ok {
		t.Fatal("附和不是被顶")
	}
}

// ⚠️ 关键反面: 否定词自己不能当线索, 否则「不是」两个字就自证了。
func TestChallengedOf_NegationWordIsNotACue(t *testing.T) {
	// 它上一句里恰好有「不是」两个字。
	if _, ok := ChallengedOf("不是", "我可不是那个意思"); ok {
		t.Fatal("「不是」两个字自证了 —— 判据退化成「只看到否定词就算被顶」")
	}
	if _, ok := ChallengedOf("不对不对", "不对吧"); ok {
		t.Fatal("同理: 两边都只有否定词, 没有实质线索")
	}
	// ⚠️ 上面两例的 2-gram 里都含停用字 (不/的), 是被 BigramCues 顺手挡掉的 ——
	// 显式的否定词过滤那一层**根本没被测到**。这一例的两个 2-gram (不成/成立)
	// 都不含停用字, 只能靠那一层挡。
	if _, ok := ChallengedOf("这根本不成立", "这个说法不成立吧"); ok {
		t.Fatal("共享线索全部落在否定词内部 —— 那是附和, 不是被顶")
	}
}

func TestChallengedOf_EmptyLastAI(t *testing.T) {
	if _, ok := ChallengedOf("不是这样", ""); ok {
		t.Fatal("它什么都没说的时候谈不上被顶")
	}
	if _, ok := ChallengedOf("", "猫喜欢吃鱼"); ok {
		t.Fatal("空消息不算被顶")
	}
}

// 热路径**不许**多一次 LLM 调用 (成本结构)。
func TestNoteChallenge_NoLLMCall(t *testing.T) {
	c := &sitCaptureChatter{}
	e, _ := groupSpeakEngine(t, c)
	e.noteChallenge("不是这样，猫才不喜欢吃鱼", "猫喜欢吃鱼吧")
	if c.n != 0 {
		t.Fatalf("被顶检测调了 %d 次 LLM —— 决策器不接 LLM 是硬纪律", c.n)
	}
	ch, err := e.Store.LoadChallenges(e.ownerKey(), clockTodayStr(), 5)
	if err != nil || len(ch) != 1 {
		t.Fatalf("素材没落库: %v %v", ch, err)
	}
	e.noteChallenge("今天天气不错", "猫喜欢吃鱼吧")
	ch, _ = e.Store.LoadChallenges(e.ownerKey(), clockTodayStr(), 5)
	if len(ch) != 1 {
		t.Fatal("无关的话不该产生素材")
	}
}

func clockTodayStr() string { return time.Now().Format("2006-01-02") }

// ═══ 裁定 3:话题词表 ═══

func TestMatchTopicKey_LongestWins(t *testing.T) {
	keys := []string{"爵士乐", "爵士乐历史", "做饭"}
	k, ok := MatchTopicKey("他最近在读爵士乐历史的书", keys)
	if !ok || k != "爵士乐历史" {
		t.Fatalf("必须报最长的那个, got %q", k)
	}
	if _, ok := MatchTopicKey("今天天气不错", keys); ok {
		t.Fatal("没命中就是没命中")
	}
	if _, ok := MatchTopicKey("随便", nil); ok {
		t.Fatal("空词表不命中")
	}
	// 单字键不许进 (噪声太大)。
	if _, ok := MatchTopicKey("我今天很好", []string{"我"}); ok {
		t.Fatal("单字键不许命中")
	}
}

func TestParseTopicKeys_RejectsJunk(t *testing.T) {
	bad := []string{"", "{}", "{\"keys\":[]}", "{\"keys\":[\"聊天\",\"日常\"]}", "{\"keys\":[\"我\"]}"}
	for _, s := range bad {
		if _, ok := ParseTopicKeys(s); ok {
			t.Fatalf("应当拒收: %s", s)
		}
	}
	got, ok := ParseTopicKeys("{\"keys\":[\"爵士乐\",\"合成器\",\"爵士乐\",\"聊天\"]}")
	if !ok || len(got) != 2 || got[0] != "爵士乐" {
		t.Fatalf("应当去重并丢掉空词: %v ok=%v", got, ok)
	}
}

// ⚠️ 兜底是设计的一部分, 不是降级告警。
func TestExtractTopicKeys_FallsBackToTopTerms(t *testing.T) {
	msgs := []FoldMsg{{Text: "猫粮买了吗"}, {Text: "猫又胖了"}, {Text: "考试真难"}, {Text: "考试要挂"}}
	got := ExtractTopicKeys(context.Background(), &fakeChatter{err: errors.New("boom")}, msgs)
	if len(got) == 0 {
		t.Fatal("LLM 失败时必须退回 2-gram 统计 —— 不能一条话题都没有")
	}
	joined := strings.Join(got, ",")
	if !strings.Contains(joined, "猫") && !strings.Contains(joined, "考试") {
		t.Fatalf("兜底没抽出当天话题: %v", got)
	}
	// 没有消息 → 不发 LLM 调用。
	c := &sitCaptureChatter{}
	if len(ExtractTopicKeys(context.Background(), c, nil)) != 0 {
		t.Fatal("没有消息不该产出话题")
	}
	if c.n != 0 {
		t.Fatal("没有消息却调了 LLM")
	}
}

// ⚠️ 这条是**回归尺子**: DailyMessages("", day) 只会匹配空场景键,
// 而写入侧会把空场景键补成默认值 —— 于是话题抽取一条消息都拿不到,
// 而编译、测试、日志全绿 (只有"词表永远是空的"这一个现象)。
func TestAllDailyMessages_SeesNonDefaultSceneKeys(t *testing.T) {
	e, night, _ := nightEngine(t)
	if _, err := e.Store.InsertMessageFull("user", "qq", "text", "猫又胖了",
		MsgMeta{Origin: OriginUser, SceneKey: "group:777", SenderUIN: "11"}); err != nil {
		t.Fatal(err)
	}
	all, err := e.Store.AllDailyMessages(night)
	if err != nil || len(all) == 0 {
		t.Fatalf("按场景过滤的版本会漏掉这些行: %v %v", all, err)
	}
	one, _ := e.Store.DailyMessages("", night)
	if len(one) != 0 {
		t.Fatal("这条断言本身失效了 —— DailyMessages 的语义变了")
	}
}

// 端到端: 词表落库后, 判据① 的话题**不再退回梗表**。
func TestTopicOf_PrefersTopicKeysOverSlang(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	if _, err := e.Store.UpsertSlang(SlangDelta{Phrase: "麻了", Meaning: "累", Example: "麻了",
		SourceQuote: "麻了", Confidence: 0.9}, "user"); err != nil {
		t.Fatal(err)
	}
	if got := e.topicOf("今天真的麻了"); got != "麻了" {
		t.Fatalf("没有词表时应当退回梗命中, got %q", got)
	}
	if err := e.Store.SaveTopicKeys(e.ownerKey(), "2026-03-01", []string{"加班", "麻了"}); err != nil {
		t.Fatal(err)
	}
	long := "今天又加班了，真的麻了"
	if got := e.topicOf(long); got != "加班" {
		t.Fatalf("词表里有更长的话题键时应当优先, got %q", got)
	}
}

// ═══ 裁定 2:同伴名册 ═══

func TestNightPeers_RoundTrip(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	if _, err := e.Store.LoadNightPeers(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []NightPeer{{UIN: 1, Name: "阿七", Brief: "话少", Weight: 3}, {UIN: 2, Name: "小北", Brief: "爱抬杠"}} {
		if err := e.Store.SaveNightPeer(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.Store.SaveNightPeer(NightPeer{UIN: 0, Name: "没uin"}); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.SaveNightPeer(NightPeer{UIN: 9, Name: "  "}); err != nil {
		t.Fatal(err)
	}
	ps, err := e.Store.LoadNightPeers()
	if err != nil || len(ps) != 2 {
		t.Fatalf("不合规的名册条目被收下了: %+v %v", ps, err)
	}
	if ps[0].Name != "阿七" {
		t.Fatalf("应当按权重降序: %+v", ps)
	}
}

// ⚠️ **这条是「配了就真的跑」的证据。**
// 没有它, §3.7 就是一个「机制完整、测试完整、永远不执行」的模块 ——
// 那正是本项目已经犯过两次的错 (BodyLine / ApplyStyle)。
// nightLineChatter 说短句的假模型 (真模型的输出会被 CleanNightTurn 的长度闸门管着)。
type nightLineChatter struct {
	n          int
	lastSystem string
}

func (c *nightLineChatter) Chat(ctx context.Context, system, user string) (string, error) {
	c.n++
	c.lastSystem = system
	if c.n%2 == 0 {
		return "哈哈 确实", nil
	}
	return "嗯 我也觉得", nil
}

func TestNightBatch_RunsRealSessionsWhenPeersAreConfigured(t *testing.T) {
	e, _, at := nightEngineWith(t, &nightLineChatter{})
	for _, p := range []NightPeer{{UIN: 1, Name: "阿七", Brief: "话少", Weight: 2}, {UIN: 2, Name: "小北", Brief: "爱抬杠", Weight: 1}} {
		if err := e.Store.SaveNightPeer(p); err != nil {
			t.Fatal(err)
		}
	}
	e.prepareDailyJobs(at)
	var n int
	if err := e.Store.DB.QueryRow("SELECT COUNT(*) FROM convo_memory WHERE k = ?", "night_social").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("配了同伴名册却一条夜间记忆都没写回 —— §3.7 是死代码")
	}
	// §3.7.4 来源归属硬性要求: 一律 ai_said, **绝不进「关于用户的事」**。
	var bad int
	if err := e.Store.DB.QueryRow("SELECT COUNT(*) FROM convo_memory WHERE k = ? AND source <> ?", "night_social", "ai_said").Scan(&bad); err != nil {
		t.Fatal(err)
	}
	if bad != 0 {
		t.Fatalf("%d 条夜间记忆的归属不是 ai_said —— 它会把自己和别的 AI 的经历说成「你上次说…」", bad)
	}
}

func TestNightBatch_EmptyRosterWritesNothing(t *testing.T) {
	e, _, at := nightEngine(t)
	e.prepareDailyJobs(at)
	var n int
	if err := e.Store.DB.QueryRow("SELECT COUNT(*) FROM convo_memory WHERE k = ?", "night_social").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("没有名册却写回了夜间记忆")
	}
}

// nightEngineWith 与 nightEngine 相同, 但换成指定的假模型。
func nightEngineWith(t *testing.T, c Chatter) (*Engine, string, time.Time) {
	t.Helper()
	e, _ := groupSpeakEngine(t, c)
	now := time.Now()
	at := time.Date(now.Year(), now.Month(), now.Day(), 20, 0, 0, 0, time.Local)
	return e, NightKeyOf(at), at
}
