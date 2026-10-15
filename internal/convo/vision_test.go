package convo

// ⚠️ 这个文件现在**只剩一个共享的测试辅助**(`containsStr`)。
//
// 它原来的两个用例已经跟着 `splitMemeNote` 一起删掉了 —— 见下。

// ⚠️ 这里原来有两个关于 `splitMemeNote` 的用例（含一条「标注绝不能泄漏进 spoken」的反面用例），
// 而那个函数**已删**。
//
// 它们钉的性质是 A5 病灶：图意标注不能跟着描述回流成用户说的话。
//
// ⚠️ 而现在那个病灶**从结构上不可能发生**：梗图含义是 `l.Meaning` 这个
// **结构化字段**，不再是从散文里解析出来的一行标记 ——
// 不存在一个「拆得干不干净」的函数了。
//
// ⇒ 用例跟着函数一起消失是**升级**，不是丢失覆盖：
// 要钉那条性质，该钉的是**打标那一侧**（`l.Meaning` 的来源）。

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
