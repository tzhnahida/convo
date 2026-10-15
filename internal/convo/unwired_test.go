package convo

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────
// 「代码写完但没接线」的**自动尺子**
//
// ⚠️ 这一批连着抓到六次同一个形态 —— 热力入账 / 冲突判定 / 关系量的正向信号 /
// 话题键注释 / 夜间时间散布 / **整节灵光的"之后"** —— 而它们**全是人肉发现的**。
//
// 共同形状只有一句话: **函数写完了、单元测试也绿了、生产调用者为零。**
// 而它的表现全都不是"报错", 而是"某个机制悄悄不生效":
//
//	热力不涨   → 它不再主动开口(看起来像"变安静了")
//	冲突不判   → 它会劝架, 只是毫无节制(看起来像"很热心")
//	亲密度不涨 → 那条硬闸门永远关着(看起来像"慢热")
//	灵光不接   → 它想出来的东西说完就散了(看起来像"随口一提")
// ─────────────────────────────────────────────────────────────

// stripGoNoise 一次扫过源码, 把**注释与三种字符串字面量**全部换成空格。
//
// ⚠️ **为什么不是几条正则** —— 这是本文件踩得最深的一个坑, 三次才写对:
//
//	① 只剥注释 → "名字出现在报错文案里"也算引用 (ObserveMember 就这么漏掉的);
//	② 加"先剥双引号串、再剥反引号串" → **顺序**是错的, 而反引号串里可以放双引号,
//	   于是那个双引号跟后面某个配成对, 一口吞掉几百行 → 冒出 485 个假的"零调用";
//	③ 把顺序调过来 → 又冒出 532 个 (双引号串里有单引号, 单引号那一步再吞一次)。
//
// **根因**: 每一步都会被另一种定界符弄糊涂 —— 靠"顺序"兜不住。
// 一次扫描带状态就没有这个问题。
//
// ⚠️ 反引号用 '\x60' 写, 免得 Go 源码里出现真的反引号。
func stripGoNoise(src string) string {
	out := []byte(src)
	n := len(src)
	blank := func(from, to int) {
		for k := from; k < to && k < n; k++ {
			if out[k] != '\n' {
				out[k] = ' '
			}
		}
	}
	i := 0
	for i < n {
		c := src[i]
		switch {
		case c == '/' && i+1 < n && src[i+1] == '/':
			j := i
			for j < n && src[j] != '\n' {
				j++
			}
			blank(i, j)
			i = j
		case c == '/' && i+1 < n && src[i+1] == '*':
			j := i + 2
			for j+1 < n && !(src[j] == '*' && src[j+1] == '/') {
				j++
			}
			if j+1 < n {
				j += 2
			} else {
				j = n
			}
			blank(i, j)
			i = j
		case c == '"' || c == '\'' || c == '\x60':
			quote := c
			j := i + 1
			for j < n {
				if quote != '\x60' && src[j] == '\\' {
					j += 2
					continue
				}
				if src[j] == quote {
					j++
					break
				}
				// ⚠️ 未闭合的引号(源码里不该有): 到此为止, **别吞掉后面几百行**。
				if src[j] == '\n' && quote != '\x60' {
					break
				}
				j++
			}
			blank(i, j)
			i = j
		default:
			i++
		}
	}
	return string(out)
}

// knownUnwired **存量登记** —— 本批清点出来的"零生产调用者"。
//
// ⚠️ 为什么是登记而不是白名单:
//
// 第一次扫(有洞的那版)只报了 3 个; 把"注释也算引用"那个洞补上之后,
// 出来的是 **97 个** —— 它不是一个可以逐条写理由的清单,
// 而是**一整个还没接线的存量**(整节 §6.10 群成员 / §2.6 灵光的"之后" /
// §7.2 联想 / §4.4 梦的惊跳 / 事件池账本 / 夜间分层 ……)。
//
// ⚠️ 所以这条尺子是**棘轮**, 不是闸门:
//
//	· 出现**新的**零调用函数 → 红(那才是它要防的事);
//	· 存量里有一个被接上了却没从这里删掉 → **也红**(名单必须跟着缩)。
//
// 逐条列在 §2.36; 清掉一个就删一行。
//
// ⚠️ 补上"字符串字面量"那个变体之后, **又冒出 2 个**(MoodLog / ObserveMember) ——
// 也就是说这份名单被清点过**两次**, 而两次都不完整。
//
// ⚠️ **这一条已经被触发过三次**: 两次在 §2.6 灵光那一节, 第三次是 §6.x 插话收敛
// (ChimeComplaintHit / MarkChimeComplaint) —— 那份"被说了就收敛"的安全阀。
//
// (原记) ⚠️ 这一条已经被触发过两次, 两次都在 §2.6 那一节:
//
//	第一次: 接上"说出来" → SparkLine / MarkSparkSaid 从名单消失;
//	第二次: 接上"被接住" → TouchSpark 从名单消失。
//
// (原记) ⚠️ 这一条已经被触发过一次: 2026-10-15 接上 §2.6 灵光的"说出来"之后,
// SparkLine 与 MarkSparkSaid 从这份名单里消失了 —— 而"消失了却没删"本身也会红。
// 那个反方向是刻意加的: 没有它, 名单只会越长越像噪声。
var knownUnwired = map[string]bool{
	"Associate":                true,
	"AvgProduced":              true,
	"BreachRate":               true,
	"BuildAssocIndex":          true,
	"ClearChimeComplaint":      true,
	"ClearCoachRules":          true,
	"ConflictActFrameIsClean":  true,
	"ConflictActionCount":      true,
	"ConflictActions":          true,
	"DayMoodAllowsProactive":   true,
	"DecideShare":              true,
	"DeclineForcesImmediate":   true,
	"DefaultGroupConfig":       true,
	"DefianceHits":             true,
	"EventGenCallsOn":          true,
	"EventGenDaySummary":       true,
	"EventGenLedger":           true,
	"EventGenStatsSnapshot":    true,
	"FallbackRate":             true,
	"FatigueQuotaProjection":   true,
	"FeedbackBoundaryLine":     true,
	"ForgetRequested":          true,
	"GenerateDayPool":          true,
	"GroupRoleLabel":           true,
	"HabitFromMemories":        true,
	"HeatQuotaAt":              true,
	"ImageFiles":               true,
	"ImageMark":                true,
	"ImageSeen":                true,
	"InsomniaCauseLine":        true,
	"IntentRetrievable":        true,
	"LastSceneMessageAt":       true,
	"LoadFold":                 true,
	"LongMemoryDecay":          true,
	"MarkAmends":               true,
	"NightDisablesNightSocial": true,
	"NightTierStatsSnapshot":   true,
	"OnUserMessage":            true,
	"PromptDiff":               true,
	"PromptStructureProblem":   true,
	"RecentHeatLog":            true,
	"Reliability":              true,
	"ResetEventGenStats":       true,
	"ResetNightTierStats":      true,
	"ResolveConflict":          true,
	"RetireCastMember":         true,
	"SaveInterruption":         true,
	"SaveMood":                 true,
	"SaveNight":                true,
	"SaveScene":                true,
	"SaveSceneMemory":          true,
	"SceneMemoryInjection":     true,
	"SceneMemoryOrigin":        true,
	"SetEventGenBudget":        true,
	"SetMemberAddress":         true,
	"SetMemberStage":           true,
	"SetMemberTaboo":           true,
	"SettleHeat":               true,
	"MoodLog":                  true,
	"SlangByPhrase":            true,
	"SparkHasInterface":        true,
	"SpeakInGroup":             true,
	"Speaking":                 true,
	"StartleMaySend":           true,
	"StartlePermit":            true,
	"StartleTarget":            true,
	"StickerOnlyFrame":         true,
	"SuspendedAt":              true,
	"TasteCounts":              true,
	"TierMemory":               true,
	"TierReason":               true,
	"TokenCount":               true,
	"TopicKeysSorted":          true,
	"TouchAndTopMemories":      true,
	"Unwrap":                   true,
	"WithDay":                  true,
	"captureUserSticker":       true,
	"describeImage":            true,
	"extractMemeNote":          true,
	"fmtStrain":                true,
	"hasAnySubstr":             true,
	"learnSystemPrompt":        true,
	"resetEventGenMemGate":     true,
	"splitMemeNote":            true,
	"topicKeyDay":              true,
}

// TestNoUnwiredFunctions 扫「定义了但从没被非测试代码调用过」的函数。
//
// 判定: 在**全仓库非测试 .go 剥掉注释之后**出现的标识符次数 > 它作为函数定义出现的次数。
// 只看本包会误报导出 API(它们被 cmd/ 或别的包用)。
func TestNoUnwiredFunctions(t *testing.T) {
	repoRoot := filepath.Join("..", "..")

	var all strings.Builder
	fileCount := 0
	werr := filepath.Walk(repoRoot, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // 读不到就跳过 —— 这条尺子不该因为一个权限问题红
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "node_modules", "vendor", "data":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil
		}
		all.Write(b)
		all.WriteString("\n")
		fileCount++
		return nil
	})
	if werr != nil {
		t.Fatalf("遍历仓库失败: %v", werr)
	}
	// ⚠️ 工作目录是包目录, 所以仓库根是 ../..
	// —— 扫不到东西时**必须红**, 否则这条尺子会以"零问题"的形式静默失效。
	if fileCount < 50 {
		t.Fatalf("只扫到 %d 个非测试 .go —— 路径不对(测试的工作目录应当是包目录)", fileCount)
	}
	repo := stripGoNoise(all.String())

	// 词频 + 定义行计数 —— **各扫一次**, 不是每个函数扫一遍。
	// (第一版是后者: 1169 × 整个仓库, 实测 47 秒, 而它每次 go test 都要跑。)
	wordRE := regexp.MustCompile("[A-Za-z_]\\w*")
	freq := map[string]int{}
	for _, w := range wordRE.FindAllString(repo, -1) {
		freq[w]++
	}
	defRE := regexp.MustCompile("(?m)^func\\s+(?:\\([^)]*\\)\\s+)?([A-Za-z_]\\w*)\\s*\\(")
	defCount := map[string]int{}
	for _, m := range defRE.FindAllStringSubmatch(repo, -1) {
		defCount[m[1]]++
	}

	// 本包的定义
	defined := map[string]string{}
	entries, derr := os.ReadDir(".")
	if derr != nil {
		t.Fatal(derr)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		b, rerr := os.ReadFile(e.Name())
		if rerr != nil {
			continue
		}
		for _, m := range defRE.FindAllStringSubmatch(stripGoNoise(string(b)), -1) {
			defined[m[1]] = e.Name()
		}
	}
	if len(defined) < 500 {
		t.Fatalf("只解析出 %d 个定义 —— 正则坏了", len(defined))
	}

	found := map[string]string{} // name → file
	for name, file := range defined {
		if freq[name]-defCount[name] <= 0 {
			found[name] = file
		}
	}

	// ① 新的零调用函数 → 红。
	var added []string
	for name, file := range found {
		if !knownUnwired[name] {
			added = append(added, file+"  "+name)
		}
	}
	sort.Strings(added)
	if len(added) > 0 {
		t.Fatalf("**新出现了**零生产调用者的函数 —— 要么接上生产路径, 要么删掉:\n  %s",
			strings.Join(added, "\n  "))
	}

	// ② 存量里已经被接上(或删掉)的 → 也红: 名单必须跟着缩, 否则它会腐烂成噪声。
	var stale []string
	for name := range knownUnwired {
		if _, still := found[name]; !still {
			stale = append(stale, name)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Fatalf("这些**已经从存量里消失**(接上了或删掉了) —— 从 knownUnwired 里删掉它们:\n  %s",
			strings.Join(stale, "\n  "))
	}
}
