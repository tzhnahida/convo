package convo

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

// ——— 控制台页面: 页面要什么 vs 服务给什么 ———
//
// ⚠️ 这一组测的不是"页面好不好看"(那要人看)。它测的是一类**静默事故**:
//
//   页面 fetch 一个**没注册**的路由 → 那张卡永远是红的, 而在此之前
//   这种错只表现为"这块没数据", 没人会想到去查路由表;
//   轨迹的 segments 空掉 → 段位着色**静默退化**成纯文本,
//   而"某一段悄悄没被注入"正是这个项目里最难发现的一类事故。
//
// 所以断言落在**契约**上: 页面开的每一路数据, 服务端都真的在供应。

// consoleSpecRE 抠出页面 SPEC 数组里的路径字面量。
var consoleSpecRE = regexp.MustCompile(`["[a-z_]+","(/[a-z-/]+)"]`)

// TestConsolePage_EverySpecPathIsARegisteredRoute
//
// 消费者(页面)在生产路径上被真的调用到 —— 少注册一条路由就红。
func TestConsolePage_EverySpecPathIsARegisteredRoute(t *testing.T) {
	paths := consoleSpecRE.FindAllStringSubmatch(dashboardHTML, -1)
	if len(paths) < 12 {
		t.Fatalf("SPEC 只解析出 %d 条路径 —— 正则和页面已经对不上了, 这条测试本身失效", len(paths))
	}
	s := adminServer(t)
	mux := http.NewServeMux()
	s.registerRoutes(mux)

	for _, m := range paths {
		target := "/api/v1/convo" + m[1]
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
		// ⚠️ 判据是"没有被 mux 的兜底 FileServer 当成静态文件 404 掉"。
		// 处理器自己返回 500(比如表还没建)不算错 —— 那说明路由**存在**。
		if w.Code == http.StatusNotFound && strings.Contains(w.Body.String(), "404 page not found") {
			t.Errorf("控制台要 %s, 但 server.go 里没有注册这条路由", target)
		}
	}
}

// TestConsolePage_ZeroExternalDependency
//
// ⚠️ 这一页是**观测手段**。CDN / 外链字体一断, 页面就白屏 ——
// 而白屏和"服务挂了"长得一模一样。所以风格必须自己实现, 请求必须一个都不外发。
func TestConsolePage_ZeroExternalDependency(t *testing.T) {
	for _, bad := range []string{"<script src", "<link ", "cdn.", "://unpkg", "://cdnjs", "@import"} {
		if strings.Contains(dashboardHTML, bad) {
			t.Errorf("控制台出现了外部依赖 %q —— 观测手段不该有断网风险", bad)
		}
	}
	// Modern Gradient 的签名 token (手写 CSS 版)
	for _, want := range []string{
		"radial-gradient",         // 渐变背景(禁止纯色主要区域)
		"backdrop-filter",         // 玻璃卡片
		"background-clip:text",    // 渐变文字
		"border-radius:var(--r2)", // 大圆角
		"prefers-reduced-motion",  // 动效降级
		"box-shadow:0 30px 70px",  // 彩色光晕
	} {
		if !strings.Contains(dashboardHTML, want) {
			t.Errorf("Modern Gradient 的 %q 不见了 —— 风格漂移", want)
		}
	}
	// 页面**不含 token**: 只从 URL 读一次再记忆, 不许把任何真 token 写进源码。
	if strings.Contains(dashboardHTML, "token=7f91") || strings.Contains(dashboardHTML, "Bearer ") {
		t.Error("控制台里写死了凭据")
	}
}

// TestConsolePage_CoversEveryDataSurface
//
// 「该有的数据都要能看到」的机器可检版本: 每一类数据在页面上都有落点。
func TestConsolePage_CoversEveryDataSurface(t *testing.T) {
	want := map[string]string{
		"世界观":  "self",
		"配角状态": "status_day",
		"现编标注": "unsure",
		"偏向":   "冷落",
		"关系倍率": "/owner_mult",
		"决策":   "system prompt",
		"记忆":   "物理删除",
		"记忆三维": "slot_key",
		"灵光":   "half_life_h",
		"学习":   "带出次数",
		"夜伴":   "名册",
		"日程":   "free",
		"表情包":  "aff",
		"黑话":   "source_quote",
		"边界":   "硬线",
		"配额":   "沉默率",
	}
	for label, marker := range want {
		if !strings.Contains(dashboardHTML, marker) {
			t.Errorf("页面上找不到「%s」的数据 —— 标记 %q 不在", label, marker)
		}
	}
	// 每个分页都要有入口
	for _, tab := range []string{"概览", "世界观", "人格", "对话", "记忆", "学习", "社交", "表情包", "黑话"} {
		if !strings.Contains(dashboardHTML, tab) {
			t.Errorf("分页 %q 没有入口", tab)
		}
	}
}

// TestTraces_SearchNarrowsWithoutSwallowingEverything
func TestTraces_SearchNarrowsWithoutSwallowingEverything(t *testing.T) {
	s := adminServer(t)
	ins := func(stance, reason string) {
		t.Helper()
		if _, err := s.Store.DB.Exec(
			`INSERT INTO convo_decision_trace (ts, action_kind, speaker_uin, scene_key, stance, reason)
			 VALUES (?,?,?,?,?,?)`,
			time.Now().Format(bodyTSLayout), "speak", "10001", "group:1", stance, reason); err != nil {
			t.Fatal(err)
		}
	}
	ins("silent", "刚说过话，冷却中")
	ins("speak", "有人点名")
	ins("react", "别人在聊它不熟的话题")

	// 正面: 搜得到, 而且只有那一条
	got := getJSON(t, s.handleTraces, "/api/v1/convo/traces?q="+url.QueryEscape("冷却"))
	if n := int(got["total"].(float64)); n != 1 {
		t.Fatalf("搜「冷却」应命中 1 条, 实得 %d", n)
	}

	// 反面: 搜不到必须是 **0 条**。
	// 退化成"搜索失败 → 全量返回"会让"搜不到"看起来像"到处都是" —— 最坏的一种撒谎。
	got2 := getJSON(t, s.handleTraces, "/api/v1/convo/traces?q="+url.QueryEscape("这个词一定不存在zzz"))
	if n := int(got2["total"].(float64)); n != 0 {
		t.Fatalf("搜不存在的词应 0 条, 实得 %d (会不会被当成空串匹配了?)", n)
	}
	if len(got2["traces"].([]interface{})) != 0 {
		t.Fatal("count 是 0 但列表非空")
	}

	// 反面: 空 q 不等于"匹配空串"
	got3 := getJSON(t, s.handleTraces, "/api/v1/convo/traces")
	if n := int(got3["total"].(float64)); n != 3 {
		t.Fatalf("不带 q 应全量 3 条, 实得 %d", n)
	}
}

// TestTraces_SegmentsReachThePage
//
// ⚠️ 消费者在生产路径上 —— 页面靠 segments 给 system prompt 着色。
// segments 空掉不会报错, 它只会静默退化成一段纯文本。
func TestTraces_SegmentsReachThePage(t *testing.T) {
	s := adminServer(t)
	prompt := "【示例对话】\nA: 早\nB: 早\n【你现在】\n刚睡醒，还没吃饭\n【输出】\n说话"
	if _, err := s.Store.DB.Exec(
		`INSERT INTO convo_decision_trace (ts, system_prompt, prompt_bytes) VALUES (?,?,?)`,
		time.Now().Format(bodyTSLayout), prompt, len(prompt)); err != nil {
		t.Fatal(err)
	}
	got := getJSON(t, s.handleTraces, "/api/v1/convo/traces?limit=1")
	trs := got["traces"].([]interface{})
	if len(trs) != 1 {
		t.Fatalf("应得 1 条轨迹, 实得 %d", len(trs))
	}
	row := trs[0].(map[string]interface{})

	// system prompt 必须**整段**回传。只回 hash 或只回长度的话,
	// 「它当时到底看到了什么」这个问题就永远答不上 —— 而那是这张表的唯一理由。
	if p, _ := row["prompt"].(string); !strings.Contains(p, "刚睡醒") {
		t.Fatalf("system prompt 没整段回传: %q", p)
	}

	segs, _ := row["segments"].([]interface{})
	if len(segs) != 3 {
		t.Fatalf("应解析出 3 个段位(示例/状态/输出), 实得 %d —— 页面的段位着色会静默退化", len(segs))
	}
	names := map[string]bool{}
	last := -1
	for _, sg := range segs {
		m := sg.(map[string]interface{})
		names[m["name"].(string)] = true
		st, en := int(m["start"].(float64)), int(m["end"].(float64))
		if st < 0 || en <= st {
			t.Errorf("段位区间非法: %v", m)
		}
		if st <= last {
			t.Errorf("段位没按出现位置排序: %v (页面会画出重叠的高亮)", m)
		}
		last = st
	}
	for _, w := range []string{"示例", "状态", "输出"} {
		if !names[w] {
			t.Errorf("段位 %q 没传到页面, 它就无法被着色", w)
		}
	}
}

// consoleScript 抠出页面里的 <script> 块。
func consoleScript(t *testing.T) string {
	t.Helper()
	i := strings.Index(dashboardHTML, "<script>")
	j := strings.Index(dashboardHTML, "</script>")
	if i < 0 || j <= i {
		t.Fatal("页面里找不到 <script> 块 —— 提取逻辑失效, 下面那条测试会变成恒真")
	}
	return dashboardHTML[i+len("<script>") : j]
}

// TestConsolePage_ScriptDelimitersBalance
//
// ⚠️ 为什么值得单独写一条:
// 整页 HTML 就是一个 Go raw string, JS 里的语法错误**编译期一个都报不出来**。
// 它的表现是「页面下半部分全白」—— 而那和「还没有数据」长得**一模一样**。
// 这一版就是这么白屏的: 三元组少一层外括号、字符串里少一个转义引号, 各踩一次。
//
// 这条只扫「字符串有没有闭合、括号有没有配平」—— 抓不到全部语法错,
// 但抓得到**那一类**, 而且不需要 node、每次 go test 都会跑。
// 部署脚本里另外还有一道 node --check 的硬闸。
func TestConsolePage_ScriptDelimitersBalance(t *testing.T) {
	rs := []rune(consoleScript(t))
	var stack []rune
	var quote rune
	inLine, inBlock := false, false
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		var nx rune
		if i+1 < len(rs) {
			nx = rs[i+1]
		}
		if inLine {
			if c == '\n' {
				inLine = false
			}
			continue
		}
		if inBlock {
			if c == '*' && nx == '/' {
				inBlock = false
				i++
			}
			continue
		}
		if quote != 0 {
			if c == '\\' {
				i++
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		switch {
		case c == '/' && nx == '/':
			inLine = true
			i++
		case c == '/' && nx == '*':
			inBlock = true
			i++
		case c == '\'' || c == '"' || c == '`':
			quote = c
		case c == '(' || c == '[' || c == '{':
			stack = append(stack, c)
		case c == ')' || c == ']' || c == '}':
			if len(stack) == 0 {
				t.Fatalf("脚本第 %d 个字符处括号提前闭合 —— 页面会整块白屏", i)
			}
			open := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			ok := (open == '(' && c == ')') || (open == '[' && c == ']') || (open == '{' && c == '}')
			if !ok {
				t.Fatalf("括号错配: %c 配上了 %c", open, c)
			}
		}
	}
	if quote != 0 {
		t.Fatalf("有没闭合的字符串 (起点 %c) —— 后面整段 JS 都会被吞掉", quote)
	}
	if len(stack) != 0 {
		t.Fatalf("还有 %d 个括号没闭合", len(stack))
	}
}
