package convo

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeWeb struct {
	titles    map[string]string   // 查询名 → 精确标题
	titleList map[string][]string // 查询名 → 多个候选标题 (优先于 titles)
	texts     map[string]string   // 标题 → 正文
	images    map[string]string   // 标题 → 头像
	disambig  map[string]bool     // 标题 → 是不是消歧义页
	// disambigLinks 标题 → 消歧义页里挂着的子项 (回退用)。
	disambigLinks map[string][]string
	searchErr     error // 注入整体失败
}

func (f *fakeWeb) SearchTitles(ctx context.Context, name string) ([]string, error) {
	if f.searchErr != nil {
		return nil, f.searchErr
	}
	if ts, ok := f.titleList[name]; ok {
		return ts, nil
	}
	if t, ok := f.titles[name]; ok {
		return []string{t}, nil
	}
	return nil, errors.New("no title")
}

// IsDisambiguation 让 fakeWeb 满足 DisambigChecker —— 消歧义这一路必须被测到,
// 而它是**可选接口**, 不实现的话代码会静默退回文案启发式 (那条也另有测试)。
func (f *fakeWeb) IsDisambiguation(ctx context.Context, title string) (bool, error) {
	return f.disambig[title], nil
}

// DisambigLinks 让 fakeWeb 满足 DisambigLinker —— 消歧义页当**目录**用的那条回退必须被测到。
func (f *fakeWeb) DisambigLinks(ctx context.Context, title string) ([]string, error) {
	return f.disambigLinks[title], nil
}

// 造一段够长的正文 —— 门槛是**字符**不是字节, 而中文一个字 3 字节。
// 真实的萌娘角色页在 2500-3500 字 (实测 2026-10-14), 所以夹具也该是这个量级;
// 原来那两段只有 130 字上下, 是照着旧的**字节**门槛凑的。
func longRoleText(head string) string {
	return head + strings.Repeat("他是这条街上有名的老好人, 谁家有事都愿意搭把手。", 12)
}

func (f *fakeWeb) Scrape(ctx context.Context, title string) (string, error) {
	if t, ok := f.texts[title]; ok {
		return t, nil
	}
	return "", errors.New("404")
}

func (f *fakeWeb) Image(ctx context.Context, title string) (string, error) {
	if t, ok := f.images[title]; ok {
		return t, nil
	}
	return "", errors.New("no image")
}

func TestGenerateRoleNames(t *testing.T) {
	f := &fakeChatter{outputs: []string{`{"names":["初音ミク","千反田愛瑠","后藤ひとり"]}`}}
	names, err := GenerateRoleNames(context.Background(), f, "ja")
	if err != nil || len(names) != 3 || names[0] != "初音ミク" {
		t.Fatalf("names = %v, %v", names, err)
	}
	// LLM 输出坏 JSON → fallback 名字
	f2 := &fakeChatter{outputs: []string{"hmm 不是 json"}}
	names, err = GenerateRoleNames(context.Background(), f2, "ja")
	if err != nil || len(names) != 3 {
		t.Fatalf("fallback names = %v, %v", names, err)
	}
}

func TestSearchRoles_ok(t *testing.T) {
	w := &fakeWeb{
		titles: map[string]string{
			"初音ミク":  "初音未来",
			"千反田愛瑠": "千反田爱瑠",
		},
		texts: map[string]string{
			"初音未来":  longRoleText("初音未来是Vocaloid虚拟歌姬, 双马尾, 世界第一公主殿下。2007年由Crypton Future Media发布, 是广为人知的虚拟歌手。她的代表曲目包括《千本樱》《世末舞厅》等, 在二次元文化中有极高知名度, 口头禅是 みく、歌うよ! 粉丝们称她为殿下。"),
			"千反田爱瑠": longRoleText("千反田爱瑠是《冰菓》的主角之一, 古典部的大小姐, 性格认真, 说话自带敬语, 像千金小姐一样有气质。一旦有感兴趣或不能理解的事情, 就会说'我很好奇'并眼睛发光, 成为好奇心的化身。黑长直, 好奇心旺盛, 天生自带让人无法拒绝的气场。"),
		},
		images: map[string]string{"初音未来": "https://storage.moegirl.org.cn/miku.jpg"},
	}
	// 第三个角色搜不到 → 丢弃, 只剩 2 张
	cardJSON := `{"roles":[
		{"name":"初音ミク","series":"Vocaloid","source_url":"u1","traits":["虚拟歌姬"],"desc":"虚拟歌姬","catchphrase":"みく、歌うよ!","famous_line":"みく、歌うよ","line_source":"摘录","image_url":""},
		{"name":"千反田愛瑠","series":"氷菓","source_url":"u2","traits":["好奇"],"desc":"大小姐","catchphrase":"私、気になります","famous_line":"私、気になります","line_source":"摘录","image_url":""}
	]}`
	f := &fakeChatter{outputs: []string{cardJSON}}
	roles, err := SearchRoles(context.Background(), f, w, []string{"初音ミク", "千反田愛瑠", "后藤ひとり"})
	if err != nil {
		t.Fatal(err)
	}
	if len(roles) != 2 || roles[0].Name != "初音ミク" || roles[1].Name != "千反田愛瑠" {
		t.Fatalf("roles = %+v", roles)
	}
	// 台词无来源 → 存疑标注 (防幻觉)
}

// ⚠️ **这一条是 2026-10-14 那次"角色卡太薄"的根因尺子。**
//
// 实测: 搜「能天使」时 opensearch 回了两个标题,
//
//	能天使            → 319 字节, **消歧义页**(「能天使可以指：…」)
//	能天使(明日方舟)  → 7271 字节, 真正文页
//
// 旧代码是"第一个够厚的就用", 而消歧义页越过了 150 的**字节**门槛,
// 于是真正的正文页**压根没被看过** —— 提炼出来只有一句
// 「《明日方舟》的登场角色。」。德克萨斯一模一样。
//
// 现在必须: 跳过消歧义页, 并且**取最长的那份**。
func TestSearchRoles_SkipsDisambiguationAndTakesLongest(t *testing.T) {
	const disambigText = "能天使可以指：\n\n能天使(明日方舟)————《明日方舟》的登场角色。\n新约能天使————《明日方舟》的登场角色。\n能天使(MS)————《机动战士高达系列》的一款高达。"
	real := longRoleText("能天使，本名蕾缪乐，是游戏《明日方舟》及其衍生作品的登场角色。拉特兰出身的萨科塔，企鹅物流的信使。")
	w := &fakeWeb{
		titleList: map[string][]string{"能天使": {"能天使", "能天使(明日方舟)"}},
		texts: map[string]string{
			"能天使":       disambigText,
			"能天使(明日方舟)": real,
		},
		disambig: map[string]bool{"能天使": true},
	}
	var seen string
	fc := &captureChatter{onPrompt: func(p string) { seen = p }, outputs: []string{oneRoleJSON}}
	if _, err := SearchRoles(context.Background(), fc, w, []string{"能天使"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(seen, "能天使可以指") {
		t.Fatal("消歧义页的正文被喂给了模型 —— 角色卡就只能是那一句目录")
	}
	if !strings.Contains(seen, "蕾缪乐") {
		t.Fatal("真正文页没被用上 —— 这正是「角色卡太薄」的根因")
	}
}

// 反面: 拿不到权威信号 (实现不了 DisambigChecker) 时, **文案判据必须兜住**。
func TestSearchRoles_DisambigByTextWhenCheckerAbsent(t *testing.T) {
	w := &textOnlyWeb{
		titles: map[string][]string{"能天使": {"能天使", "能天使(明日方舟)"}},
		texts: map[string]string{
			"能天使":       "能天使可以指：\n\n能天使(明日方舟)————《明日方舟》的登场角色。",
			"能天使(明日方舟)": longRoleText("能天使，本名蕾缪乐，企鹅物流的信使。"),
		},
	}
	var seen string
	fc := &captureChatter{onPrompt: func(p string) { seen = p }, outputs: []string{oneRoleJSON}}
	if _, err := SearchRoles(context.Background(), fc, w, []string{"能天使"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(seen, "可以指") || !strings.Contains(seen, "蕾缪乐") {
		t.Fatalf("文案兜底没生效:\n%.300s", seen)
	}
}

// ⚠️ **门槛的单位是字符, 不是字节。**
//
// 旧的 `len(text) < 150` 数的是字节 → 中文一个字 3 字节 →
// 那道门槛实际只拦得住 50 个字的页面, 消歧义页就是这么溜进去的。
func TestMinRoleTextRunes_IsRunesNotBytes(t *testing.T) {
	// 90 个汉字 = 270 字节, 但只有 90 字 → 薄, 必须拦。
	thin := strings.Repeat("茶", 90)
	if len(thin) < MinRoleTextRunes {
		t.Fatal("夹具没意义: 这段的字节数应当超过门槛, 否则测不出单位问题")
	}
	if len([]rune(thin)) >= MinRoleTextRunes {
		t.Fatal("夹具没意义: 这段的字符数应当低于门槛")
	}
	w := &fakeWeb{
		titles: map[string]string{"甲": "甲页"},
		texts:  map[string]string{"甲页": thin},
	}
	if _, err := SearchRoles(context.Background(), &fakeChatter{outputs: []string{"{}"}}, w, []string{"甲"}); err == nil {
		t.Fatal("270 字节 / 90 字的薄页被当成了素材 —— 门槛又按字节算了")
	}
}

// 消歧义文案判据: 短 + 关键词才算; **长文里出现"可以指"不许误判**
// (误判 = 静默丢角色, 那是看不见的损失)。
func TestLooksLikeDisambiguation(t *testing.T) {
	if !LooksLikeDisambiguation("能天使可以指：\n\n能天使(明日方舟)————《明日方舟》的登场角色。") {
		t.Fatal("教科书式的消歧义页没认出来")
	}
	long := "这个词可以指很多东西。" + strings.Repeat("正文正文正文正文正文正文正文正文正文。", 100)
	if LooksLikeDisambiguation(long) {
		t.Fatal("长正文里的「可以指」被误判成消歧义页 —— 会静默丢角色")
	}
	if LooksLikeDisambiguation("初音未来是虚拟歌姬。") {
		t.Fatal("正常短句被误判")
	}
}

// captureChatter 记下喂给模型的提示词 —— 这一批要断言的正是"素材换没换掉"。
type captureChatter struct {
	onPrompt func(string)
	outputs  []string
	i        int
}

func (c *captureChatter) Chat(ctx context.Context, system, user string) (string, error) {
	if c.onPrompt != nil {
		c.onPrompt(system + "\n" + user)
	}
	if c.i >= len(c.outputs) {
		return "", errors.New("capture: no more outputs")
	}
	out := c.outputs[c.i]
	c.i++
	return out, nil
}

const oneRoleJSON = `{"roles":[{"name":"能天使","series":"明日方舟","traits":["信使"],"desc":"企鹅物流的信使","catchphrase":"","famous_line":"","line_source":"","image_url":""}]}`

// textOnlyWeb 只有 WebSearcher 三件套, **不实现** DisambigChecker。
type textOnlyWeb struct {
	titles map[string][]string
	texts  map[string]string
}

func (w *textOnlyWeb) SearchTitles(ctx context.Context, name string) ([]string, error) {
	if ts, ok := w.titles[name]; ok {
		return ts, nil
	}
	return nil, errors.New("no title")
}

func (w *textOnlyWeb) Scrape(ctx context.Context, title string) (string, error) {
	if t, ok := w.texts[title]; ok {
		return t, nil
	}
	return "", errors.New("404")
}

func (w *textOnlyWeb) Image(ctx context.Context, title string) (string, error) {
	return "", errors.New("no image")
}

func TestSearchRoles_allFail(t *testing.T) {
	w := &fakeWeb{searchErr: errors.New("quota")}
	_ = strings.TrimSpace // keep import
	_, err := SearchRoles(context.Background(), &fakeChatter{outputs: []string{`{"names":["a","b","c"]}`}}, w, []string{"a", "b", "c"})
	if err == nil {
		t.Fatal("expected error when all searches fail")
	}
}

func TestBuildRoleProfile(t *testing.T) {
	a := &ProfileAnswers{Name: "x", Identity: "student", Template: "bestie", Q1: "high", Q2: "bestie", Q3: "daily"}
	r := RoleCard{Name: "千反田愛瑠", Series: "氷菓", Traits: []string{"好奇"}, Desc: "好奇大小姐", Catchphrase: "私、気になります", Origin: "search"}
	p := BuildRoleProfile(a, r)
	if p.Name != "千反田愛瑠" || p.Catchphrase != "私、気になります" || p.Origin.Series != "氷菓" {
		t.Fatalf("profile = %+v", p)
	}
	if !strings.Contains(p.Personality, "好奇") {
		t.Fatalf("personality missing traits: %q", p.Personality)
	}
	// 三问映射不受影响
	if p.HelperBias != 0.55 || p.Novelty != 0.7 {
		t.Fatalf("axes = %v %v", p.HelperBias, p.Novelty)
	}
}
