package convo

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// quotaArithRE 配额算术的句式。
//
// ⚠️ §4.6.1 的判据: **硬闸留在代码里, 但它感知到的是趋势, 不是计数。**
// 「最近夜里有点多」可以; 「本月噩梦配额 1/1 已用完」不行 —— 后者是一个**知道自己 KPI 的人**。
var quotaArithRE = regexp.MustCompile(`最多.{0,8}次|还剩.{0,6}次|不超过.{0,6}次|配额|额度`)

// quotaWhitelist 允许出现配额算术的地方。
//
// ⚠️ **按 (文件, 命中片段) 豁免, 不按整条字符串豁免** —— 后者会连带放过同一条字面量里的其它配额。
// ⚠️ 只有「给人看的」与「生成器的工作规则」能进这里;
// **凡是会进对话提示词、且描述它自己生活节奏的, 一律不许豁免。**
var quotaWhitelist = []struct{ file, frag, why string }{
	{"fatigue.go", "配额", "DeclinePermit 的 reason —— 只进日志与测试, 绝不进提示词(见 SituationInput.DeclineWhy)"},
	{"eventgen.go", "配额", "日志: 调用账本终态写不上时的告警"},
	{"heat.go", "配额", "日志: 热力读库失败 → 退回常数"},
	{"insomnia.go", "最多调整一次", "ErrSleepPointDailyCap 的错误消息 —— 给人看的, 不是给它的"},
	{"eventgen.go", "最多一次", "**生成器护栏**(§3.6.7-2 高情绪钩子限频)。角色永远看不到这个 prompt, 所以它不是「它知道自己配额」"},
	{"dialog.go", "最多 3 条", "**接口上限**, 不是生活上限: remember 是本系统的一个帧字段, 超出会被校验丢弃 —— 不告诉它反而会静默丢数据(见设计稿 §4.6.1「两类上限」)"},
	{"heatwire.go", "配额", "日志: 热力入账失败时的告警 —— 给人看的(它不涨的表现是「它变安静了」, 必须留下痕迹), 绝不进提示词"},
	{"bondwire.go", "配额", "日志: 关系量落库失败时的告警 —— 同上, 只在落库失败那条路上出现"},
	{"startlewiring.go", "额度", "日志: §4.4.4 惊醒求助的两条告警(额度读不到 / 记账失败) —— 给人看的, 绝不进提示词; 而它进提示词的那一句只有 StartleAskLine 那 3 个字"},
	{"sparkwire.go", "配额", "日志: 灵光 said_ts 记不上时的告警 —— 给人看的(记不上的后果是周配额算不准), 绝不进提示词"},
}

// TestNoQuotaArithmeticInInjectedText
//
// ⚠️ 它扫的是**全包的字符串字面量**(跳过注释与字符字面量) —— 不是某一张手写的函数清单。
// 手写清单会腐烂: 新加一个注入点, 清单不会报错, 而这条纪律就静默失效了。
//
// 代价是要维护一张白名单 —— 但白名单**必须写理由**, 而且豁免的是"这一条给人看的字符串",
// 不是"这个函数不用查"。
func TestNoQuotaArithmeticInInjectedText(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	files, lits, hits := 0, 0, 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if _, skip := promptScanSkip[name]; skip {
			continue
		}
		src, rerr := os.ReadFile(filepath.Join(".", name))
		if rerr != nil {
			t.Fatal(rerr)
		}
		files++
		for _, lit := range stringLiterals(string(src)) {
			lits++
			for _, m := range quotaArithRE.FindAllString(lit, -1) {
				if whitelisted(name, m) {
					continue
				}
				hits++
				t.Errorf("%s: 字符串里出现配额算术 %q —— §4.6.1 要求它感知的是**趋势**不是计数。\n"+
					"  若这是给人看的东西(日志/错误), 或生成器的护栏, 加进 quotaWhitelist 并写明理由;\n"+
					"  若这是接口上限(帧字段装得下几条), 也加进去, 理由写「接口上限」。\n"+
					"  若它描述的是**它自己生活的节奏**, 改措辞。尾是: %q", name, m, tailOf(lit))
			}
		}
	}
	// ⚠️ 反面: 一个都没扫到 = 这条测试恒真。
	if files < 20 || lits < 200 {
		t.Fatalf("只扫了 %d 个文件 / %d 个字符串 —— 扫描逻辑失效, 这条测试变成恒真", files, lits)
	}
	// ⚠️ 白名单条目必须仍然**存在**, 否则它只是在豁免一个已经不存在的字符串。
	for _, w := range quotaWhitelist {
		if !strings.Contains(readAll(t, w.file), w.frag) {
			t.Errorf("白名单条目已过期: %s 里找不到 %q —— 删掉这一条", w.file, w.frag)
		}
	}
	if hits == 0 {
		t.Logf("配额扫描: %d 个文件 / %d 个字符串, 无未豁免命中 (%d 条白名单)", files, lits, len(quotaWhitelist))
	}
}

func whitelisted(file, phrase string) bool {
	for _, w := range quotaWhitelist {
		if w.file == file && phrase == w.frag {
			return true
		}
	}
	return false
}

func readAll(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(".", name))
	if err != nil {
		t.Fatalf("白名单指向的文件不存在: %s", name)
	}
	return string(b)
}
