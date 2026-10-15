package convo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/tzhnahida/VocabLock/internal/log"
)

// ——— 创建朋友 v2: 双轨角色流 ———
//
// A 路线 (AI 搜现成二次元角色):
//   LLM 按"目标语 + 三问偏好"列 3 个候选角色名 → 逐角色 搜索(百科)→ 抓正文
//   → 一次 LLM 提炼 3 张角色卡 → 用户选 1 / 换一组;
//   全失败 → 内置角色包 → 仍失败 → 自动落 B 路线 (原创)。
//
// 防幻觉: 角色卡一切字段源自抓取文本; 抓取失败=丢候选; 无来源台词标 ⚠️。

type RoleCard struct {
	Name        string   `json:"name"`
	Series      string   `json:"series"`
	SourceURL   string   `json:"source_url"`
	Traits      []string `json:"traits"` // 萌点标签
	Desc        string   `json:"desc"`
	Catchphrase string   `json:"catchphrase"`
	FamousLine  string   `json:"famous_line"`
	LineSource  string   `json:"line_source"` // ⚠️ 无来源=存疑
	ImageURL    string   `json:"image_url"`
	Origin      string   `json:"origin"` // search | builtin
}

// WebSearcher 角色资料获取接口 (萌娘百科 MediaWiki API 实现; 测试用 fake)。
// 2026-09-06 实测定案: firecrawl 未认证搜索返回乱码, 弃用; 萌娘百科公开
// API (opensearch/extracts/pageimages) 免认证直连, 大陆网络可用。
type WebSearcher interface {
	SearchTitles(ctx context.Context, name string) ([]string, error) // opensearch → 候选标题(前 3)
	Scrape(ctx context.Context, title string) (string, error)        // extracts → 正文
	Image(ctx context.Context, title string) (string, error)         // pageimages → 头像 URL
}

// MoegirlWeb 萌娘百科 (zh.moegirl.org.cn) MediaWiki API 客户端。
type MoegirlWeb struct {
	Base string
	HTTP *http.Client
}

func (m *MoegirlWeb) api(ctx context.Context, qs string) ([]byte, error) {
	url := m.Base + "/api.php?" + qs
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := m.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if strings.Contains(string(body), `"error"`) {
		return nil, fmt.Errorf("moegirl api error: %s", truncateStr(string(body), 120))
	}
	return body, nil
}

func (m *MoegirlWeb) SearchTitles(ctx context.Context, name string) ([]string, error) {
	qs := "action=opensearch&search=" + urlQueryEscape(name) + "&format=json&limit=3"
	body, err := m.api(ctx, qs)
	if err != nil {
		return nil, err
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(body, &arr); err != nil {
		return nil, err
	}
	if len(arr) < 2 {
		return nil, fmt.Errorf("opensearch bad shape")
	}
	var titles []string
	if err := json.Unmarshal(arr[1], &titles); err != nil || len(titles) == 0 {
		return nil, fmt.Errorf("no title for %q", name)
	}
	if len(titles) > 3 {
		titles = titles[:3]
	}
	return titles, nil
}

func (m *MoegirlWeb) Scrape(ctx context.Context, title string) (string, error) {
	qs := "action=query&prop=extracts&explaintext=1&redirects=1&titles=" +
		urlQueryEscape(title) + "&format=json"
	body, err := m.api(ctx, qs)
	if err != nil {
		return "", err
	}
	var out struct {
		Query struct {
			Pages map[string]struct {
				Extract string `json:"extract"`
			} `json:"pages"`
		} `json:"query"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", err
	}
	for _, p := range out.Query.Pages {
		if p.Extract == "" {
			return "", fmt.Errorf("empty extract for %q", title)
		}
		return p.Extract, nil
	}
	return "", fmt.Errorf("no page for %q", title)
}

// DisambigChecker 可选的"这一页是不是消歧义页"能力。
//
// ⚠️ 做成**可选接口**而不是塞进 WebSearcher: 后者一改, 所有测试用的假实现
// 都得跟着改 —— 而这条能力对假实现毫无意义。可选接口 + 类型断言,
// 拿到就用, 拿不到就退回文案启发式。
type DisambigChecker interface {
	IsDisambiguation(ctx context.Context, title string) (bool, error)
}

// IsDisambiguation 问 MediaWiki 这一页是不是消歧义页。
//
// ⚠️ 实测 (2026-10-14): 萌娘用 `pageprops.disambiguation` 标得很干净 ——
//
//	能天使          → {"disambiguation": ""}   ← 消歧义页
//	能天使(明日方舟) → {"page_image_free": …}   ← 真正文页
//
// 这是**权威信号**, 比"正文里有没有写可以指"可靠得多。文案启发式只作兜底。
func (m *MoegirlWeb) IsDisambiguation(ctx context.Context, title string) (bool, error) {
	qs := "action=query&prop=pageprops&redirects=1&titles=" + urlQueryEscape(title) + "&format=json"
	body, err := m.api(ctx, qs)
	if err != nil {
		return false, err
	}
	var out struct {
		Query struct {
			Pages map[string]struct {
				PageProps map[string]string `json:"pageprops"`
			} `json:"pages"`
		} `json:"query"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return false, err
	}
	for _, p := range out.Query.Pages {
		_, hit := p.PageProps["disambiguation"]
		return hit, nil
	}
	return false, fmt.Errorf("pageprops: no page for %q", title)
}

// DisambigLinker 可选接口: 把消歧义页里的**链接标题**取出来。
//
// ⚠️ 为什么要这一层: opensearch 的候选**不一定**包含子项。
// 实测 2026-10-14 那次 opensearch 恰好回了 `能天使(明日方舟)`, 所以没做;
// 但那**不是保证** —— 少回一个子项, 整个角色就从候选里静默消失了,
// 而现象只是"提炼出来的角色卡很薄"。消歧义页里的链接是这份目录自己写的, 比再猜一次可靠。
type DisambigLinker interface {
	DisambigLinks(ctx context.Context, title string) ([]string, error)
}

// DisambigLinks 取消歧义页上的链接标题 (前 20 个)。
func (m *MoegirlWeb) DisambigLinks(ctx context.Context, title string) ([]string, error) {
	qs := "action=query&prop=links&redirects=1&plnamespace=0&pllimit=20&titles=" +
		urlQueryEscape(title) + "&format=json"
	body, err := m.api(ctx, qs)
	if err != nil {
		return nil, err
	}
	var out struct {
		Query struct {
			Pages map[string]struct {
				Links []struct {
					Title string `json:"title"`
				} `json:"links"`
			} `json:"pages"`
		} `json:"query"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	var titles []string
	for _, p := range out.Query.Pages {
		for _, l := range p.Links {
			if t := strings.TrimSpace(l.Title); t != "" {
				titles = append(titles, t)
			}
		}
	}
	return titles, nil
}

// disambigLinkCap 回退时最多展开多少个消歧义子项 (防病态页面)。
const disambigLinkCap = 20

// LooksLikeDisambiguation 文案兜底 —— 拿不到权威信号时才用。
//
// ⚠️ 加长度上限是**必须的**: 正文里出现"可以指"三个字的地方多得是,
// 只看关键词会把正经文章误判成消歧义页, 而那是**静默丢角色**。
func LooksLikeDisambiguation(text string) bool {
	r := []rune(text)
	if len(r) > 1000 {
		return false
	}
	for _, kw := range []string{"可以指：", "可能指：", "可以指:", "可能指:", "可以指 :"} {
		if strings.Contains(text, kw) {
			return true
		}
	}
	return false
}

// MinRoleTextRunes 正文短于此 = 薄页, 不当素材。
//
// ⚠️ 单位是**字符**不是字节。原来是 `len(text) < 150`, 而 len() 数的是字节 ——
// 中文一个字 3 字节, 于是那道门槛实际只拦得住 50 个字的页面。
const MinRoleTextRunes = 150

// RoleTextMaxRunes 单个角色喂给模型的正文上限 (字符)。
//
// ⚠️ 原来写的是 `text[:4000]` —— 又是**按字节切**, 会把汉字切成坏 UTF-8。
// 而且 4000 字节 ≈ 1333 个汉字, 对一份 2649 字的正文相当于砍掉一半,
// 砍掉的恰恰是「档案 / 语音」那些**最能提炼性格**的部分。
const RoleTextMaxRunes = 3000

func (m *MoegirlWeb) Image(ctx context.Context, title string) (string, error) {
	qs := "action=query&prop=pageimages&piprop=original&titles=" + urlQueryEscape(title) + "&format=json"
	body, err := m.api(ctx, qs)
	if err != nil {
		return "", err
	}
	var out struct {
		Query struct {
			Pages map[string]struct {
				Original struct {
					Source string `json:"source"`
				} `json:"original"`
			} `json:"pages"`
		} `json:"query"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", err
	}
	for _, p := range out.Query.Pages {
		if p.Original.Source != "" {
			return p.Original.Source, nil
		}
	}
	return "", fmt.Errorf("no image for %q", title)
}

// truncateStr 截到 n **个字符** (不是字节)。
//
// ⚠️ 2026-10-14 修: 原来写的是 `s[:n]`, 按**字节**切。
// 线上真出过 —— 表情包标签「HEYTEA 喜茶恶搞联名」被切成
//
//	[图: 表情 — 嘲讽 喜 「HEYTEA 喜\xe8\x8c」 — ...]
//
// 那个 `\xe8\x8c` 是「茶」的前两个字节, 渲染成 `�` 进了提示词。
// 一个汉字 3 字节 → 按字节切**必然**切坏, 不是概率问题, 是算术问题。
//
// 所有调用点都是"给人/给模型看的一小段", 没有一个要求字节预算,
// 所以按字符切是对的 (并且对纯 ASCII 的结果与从前逐字节一致)。
func truncateStr(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// ——— 管线 ———

// GenerateSimilarNames 用户提交任意名字/关键词 → LLM 列 2 个"类似"角色名
// (同圈/同萌点/同气质), 本身先没搜到时的同源候选。
func GenerateSimilarNames(ctx context.Context, c Chatter, seed string) ([]string, error) {
	prompt := fmt.Sprintf(`用户想找「%s」这个角色做语言学习好友, 但也要看看类似气质的。

列 2 个日本二次元里和「%s」气质/圈层/萌点相似的角色名 (不要重复它本身, 要知名度高的)。

输出单行 JSON: {"names":["角色名","角色名"]}`, seed, seed)
	out, err := c.Chat(ctx, "你输出单行 JSON。", prompt)
	if err != nil {
		return nil, err
	}
	raw, err := extractJSON(out)
	if err != nil {
		return nil, err
	}
	var r struct {
		Names []string `json:"names"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	if len(r.Names) > 2 {
		r.Names = r.Names[:2]
	}
	return r.Names, nil
}

// RoleSearchPlan 候选名单规划: 用户提交名字 → [名字 + 类似 2];
// 留空 → LLM 按偏好列 3 个。
func RoleSearchPlan(ctx context.Context, c Chatter, seed string) ([]string, error) {
	if strings.TrimSpace(seed) != "" {
		names := []string{strings.TrimSpace(seed)}
		similar, err := GenerateSimilarNames(ctx, c, seed)
		if err != nil {
			log.Warn("convo:role", "similar names: %v", err)
		}
		for _, n := range similar {
			if n != seed {
				names = append(names, n)
			}
		}
		if len(names) > 3 {
			names = names[:3]
		}
		// 补足 3 个 (类似只出 1 个时)
		for _, fb := range []string{"初音ミク", "千反田愛瑠", "后藤ひとり"} {
			if len(names) >= 3 {
				break
			}
			names = append(names, fb)
		}
		return names, nil
	}
	return GenerateRoleNames(ctx, c, "ja")
}

// GenerateRoleNames LLM 列 3 个候选角色名 (目标语 + 三问偏好; 纯 LLM 零网络)。
func GenerateRoleNames(ctx context.Context, c Chatter, lang string) ([]string, error) {
	prompt := fmt.Sprintf(`列 3 个适合做"用户的语言学习好友"的二次元角色 (来自 %s 圈/日本动画, 性格鲜明、有公认萌点、知名度高)。

输出单行 JSON: {"names":["角色名","角色名","角色名"]}

要求: 角色名用通行中文名或原文名, 不要作品名。`, lang)
	out, err := c.Chat(ctx, "你输出单行 JSON。", prompt)
	if err != nil {
		return nil, err
	}
	raw, err := extractJSON(out)
	if err != nil {
		return []string{"初音ミク", "千反田愛瑠", "后藤ひとり"}, nil
	}
	var r struct {
		Names []string `json:"names"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return []string{"初音ミク", "千反田愛瑠", "后藤ひとり"}, nil
	}
	if len(r.Names) < 3 {
		r.Names = append(r.Names, "初音ミク", "千反田愛瑠", "后藤ひとり")
	}
	return r.Names[:3], nil
}

// SearchRoles 全链: 名字 → opensearch 精确标题 → extracts 正文 + pageimages 头像
// → LLM 提炼角色卡 (某角色失败=丢候选)。
func SearchRoles(ctx context.Context, c Chatter, w WebSearcher, names []string) ([]RoleCard, error) {
	var texts []struct {
		Name    string `json:"name"`
		URL     string `json:"url"`
		Content string `json:"content"`
		Image   string `json:"image"`
	}
	for _, name := range names {
		titles, err := w.SearchTitles(ctx, name)
		if err != nil {
			log.Warn("convo:role", "opensearch %q: %v", name, err)
			continue
		}
		// ⚠️ **逐个试完再挑最长的, 不是"第一个够厚的就用"** (2026-10-14 改)。
		//
		// 旧写法踩了一个很贵的坑, 而且完全静默:
		//
		//	能天使   → 319 字节, 是**消歧义页**(「能天使可以指：…」)
		//	能天使(明日方舟) → 7271 字节, 才是真正的正文页
		//
		// opensearch 两个标题都回了, 而消歧义页 319 > 150 的门槛, 于是
		// **第一个候选就"通过"了**, 真正的正文页压根没被看过。
		// 结果就是角色卡提炼出一句「《明日方舟》的登场角色。」——
		// 那不是模型偷懒, 是它只拿到了那份目录。
		//
		// 德克萨斯一模一样 (261 字节的消歧义页 vs 3378 字的正文页)。
		// 中日文角色名与通用词撞车时**特别**容易这样 —— 而二次元角色名
		// 撞车是常态, 不是例外。
		var bestText, bestTitle, bestImg string
		var bestRunes int
		// pending 消歧义页里挂着的子项标题 —— 主循环全不可用时再试。
		var pending []string
		// tryOne 试一个标题。返回"用上了没有"(不包括"只是收进了 pending")。
		tryOne := func(title string) bool {
			if dc, ok := w.(DisambigChecker); ok {
				if isDis, derr := dc.IsDisambiguation(ctx, title); derr == nil && isDis {
					// ⚠️ 这一页本身不是正文, 但**它是一份目录** —— 收着, 别丢。
					// 少了这一句, opensearch 一旦没把子项列出来, 这个角色就**静默消失**。
					if dl, lok := w.(DisambigLinker); lok && len(pending) < disambigLinkCap {
						if links, lerr := dl.DisambigLinks(ctx, title); lerr == nil {
							pending = append(pending, links...)
							log.Info("convo:role", "消歧义页 %q 挂着 %d 个子项 (留作回退)", title, len(links))
						}
					}
					log.Info("convo:role", "跳过消歧义页 %q (不是正文)", title)
					return false
				}
			}
			text, err := w.Scrape(ctx, title)
			if err != nil {
				log.Debug("convo:role", "title %q scrape fail: %v", title, err)
				return false
			}
			// 拿不到权威信号时用文案兜底 —— 两条路都要挡。
			if LooksLikeDisambiguation(text) {
				log.Info("convo:role", "跳过疑似消歧义页 %q (文案判据)", title)
				return false
			}
			n := len([]rune(text))
			if n < MinRoleTextRunes {
				log.Debug("convo:role", "title %q 太薄 (%d 字) — 跳过", title, n)
				return false
			}
			if n > bestRunes {
				bestText, bestTitle, bestRunes = text, title, n
			}
			return true
		}
		for _, title := range titles {
			tryOne(title)
		}
		if bestTitle == "" && len(pending) > 0 {
			// ⚠️ **只回退一趟, 不递归**: range 在进入时就把切片头定死了,
			// 所以这一趟里新收进来的 pending 不会再被展开 —— 那正是我们要的(防套娃)。
			log.Info("convo:role", "%q 的直接候选全不可用, 回退到消歧义页里的 %d 个子项", name, len(pending))
			for _, title := range pending {
				tryOne(title)
			}
		}
		if bestTitle == "" {
			log.Warn("convo:role", "no scrapeable title for %q (候选 %v)", name, titles)
			continue
		}
		// 图片只给选中的那一页问一次 —— 之前是每试一个标题就问一次。
		bestImg, _ = w.Image(ctx, bestTitle)
		text := truncateStr(bestText, RoleTextMaxRunes)
		log.Info("convo:role", "%q → 用 %q 的正文 (%d 字, 截到 %d)",
			name, bestTitle, bestRunes, len([]rune(text)))
		texts = append(texts, struct {
			Name    string `json:"name"`
			URL     string `json:"url"`
			Content string `json:"content"`
			Image   string `json:"image"`
		}{name, "https://zh.moegirl.org.cn/" + urlQueryEscape(bestTitle), text, bestImg})
	}
	if len(texts) == 0 {
		return nil, fmt.Errorf("no scrapeable candidates")
	}
	jb, _ := json.Marshal(texts)
	prompt := `从下面的百科摘录提炼角色卡。输出单行 JSON:
{"roles":[{"name":"","series":"","source_url":"","traits":["萌点"],"desc":"两句话性格概述","catchphrase":"口头禅(来自原文,没有就空串)","famous_line":"名台词(来自原文,没有就空串)","line_source":"台词出处行","image_url":"形象图 URL(来自原文,没有就空串)"}]}

规则: 每个角色对应一份摘录; 所有字段只能来自摘录原文 (image_url 直接抄摘录的 image 字段), 摘录里没有的填空串/空数组; 绝不编造台词。

摘录:
` + string(jb)
	out, err := c.Chat(ctx, "你输出单行 JSON。", prompt)
	if err != nil {
		return nil, err
	}
	raw, err := extractJSON(out)
	if err != nil {
		return nil, err
	}
	var res struct {
		Roles []RoleCard `json:"roles"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	for i := range res.Roles {
		res.Roles[i].Origin = "search"
		if res.Roles[i].FamousLine != "" && res.Roles[i].LineSource == "" {
			res.Roles[i].LineSource = "⚠️ 存疑: 台词无来源标注"
		}
	}
	return res.Roles, nil
}

// ——— 内置角色包 (全链失败兜底; 内容为预置, 非搜索所得) ———

func BuiltinRolePack() []RoleCard {
	return []RoleCard{
		{Name: "初音ミク", Series: "Vocaloid", Origin: "builtin", SourceURL: "builtin",
			Traits:      []string{"虚拟歌姬", "双马尾", "世界第一公主殿下"},
			Desc:        "总部在涩谷的虚拟歌姬, 天真又元气, 用歌声说话的人。",
			Catchphrase: "みく、歌うよ!", FamousLine: "世界の終わり、始まりのうた", LineSource: "⚠️ 存疑: 内置包非搜索所得"},
		{Name: "千反田愛瑠", Series: "氷菓", Origin: "builtin", SourceURL: "builtin",
			Traits:      []string{"好奇宝宝", "黑长直", "名门大小姐"},
			Desc:        "古典部的大小姐, 对一切事物充满好奇心, 礼貌温柔但求知欲爆棚。",
			Catchphrase: "私、気になります!", FamousLine: "私、気になります", LineSource: "⚠️ 存疑: 内置包非搜索所得"},
		{Name: "后藤ひとり", Series: "ぼっち・ざ・ろっく", Origin: "builtin", SourceURL: "builtin",
			Traits:      []string{"社恐", "吉他手", "波奇"},
			Desc:        "极度社恐的吉他少女, 在乐队里一点点长大的小孤独。",
			Catchphrase: "あ、あの……ご、ごめんなさい", FamousLine: "ぼっちだけど、バンドやってます", LineSource: "⚠️ 存疑: 内置包非搜索所得"},
	}
}
