package convo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// promptScanSkip 豁免清单 —— **一份要写明理由的白名单**。
//
// ⚠️ 新增的 .go 文件**默认会被扫**; 想豁免必须在这里写清楚为什么。
var promptScanSkip = map[string]string{
	"dashboard_html.go": "整页 HTML/CSS/JS 的 raw string, 不是提示词; 里面本来就有大量行尾 +",
}

// TestNoPromptHasSwallowedConcat
//
// ⚠️ 铁律: 提示词是**字符串**, 所以语法错**编译期报不出来**, 模型也不会报错 ——
// 它只会安静地带着残骸去回答。这个项目已经踩过两次(2026-10-15):
// schedulePrompt 与 demoPrompt 里躺着**字面的** " + ", 而且 NowLine() **从来没被调用过**。
//
// 判据(只扫字符串**内容**, 不扫代码、不扫注释):
//
//  1. 任何字符串内容里有一行以 " +" 结尾 → Go 的拼接符被写进了字符串里;
//  2. 任何字符串内容里出现字面的 "NowLine()" → 函数调用被写进了字符串里。
//
// 第二条是特例化的写法, 但它抓的是**同一类**: 代码残骸落进提示词。
func TestNoPromptHasSwallowedConcat(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	files, lits := 0, 0
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
			for _, ln := range strings.Split(lit, "\n") {
				if strings.HasSuffix(ln, " +") {
					t.Errorf("%s: 字符串里有一行以 \" +\" 结尾 —— 拼接符被写进了字符串内容, 尾是: %q", name, tailOf(ln))
				}
			}
			if strings.Contains(lit, "NowLine()") {
				t.Errorf("%s: 字符串里出现字面的 \"NowLine()\" —— 函数调用被写进了字符串内容", name)
			}
		}
	}
	// ⚠️ 反面: 一个都没扫到 → 这条测试是**恒真**的。恒真的尺子比没有更糟。
	if files < 20 || lits < 200 {
		t.Fatalf("只扫了 %d 个文件 / %d 个字符串 —— 扫描逻辑失效, 这条测试变成恒真", files, lits)
	}
	// ⚠️ 白名单过期 = 静默少扫一个文件。
	for f := range promptScanSkip {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("白名单里的 %s 不存在 —— 白名单过期了, 删掉它", f)
		}
	}
}

func tailOf(s string) string {
	r := []rune(s)
	if len(r) > 60 {
		r = r[len(r)-60:]
	}
	return string(r)
}

// stringLiterals 抠出一个 Go 源文件里所有字符串字面量的**内容**。
//
// ⚠️ 必须跳过注释与字符字面量 —— 否则"解释这个 bug 的注释"会把这条测试自己搞红。
func stringLiterals(src string) []string {
	rs := []rune(src)
	var out []string
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		if c == '/' && i+1 < len(rs) && rs[i+1] == '/' {
			for i < len(rs) && rs[i] != '\n' {
				i++
			}
			continue
		}
		if c == '/' && i+1 < len(rs) && rs[i+1] == '*' {
			i += 2
			for i+1 < len(rs) && !(rs[i] == '*' && rs[i+1] == '/') {
				i++
			}
			i++
			continue
		}
		if c == '\'' {
			i++
			for i < len(rs) {
				if rs[i] == '\\' {
					i += 2
					continue
				}
				if rs[i] == '\'' {
					break
				}
				i++
			}
			continue
		}
		if c == '`' {
			i++
			start := i
			for i < len(rs) && rs[i] != '`' {
				i++
			}
			out = append(out, string(rs[start:i]))
			continue
		}
		if c == '"' {
			i++
			var b strings.Builder
			for i < len(rs) {
				if rs[i] == '\\' {
					i++
					if i < len(rs) {
						b.WriteRune(rs[i])
						i++
					}
					continue
				}
				if rs[i] == '"' {
					break
				}
				b.WriteRune(rs[i])
				i++
			}
			out = append(out, b.String())
			continue
		}
	}
	return out
}
