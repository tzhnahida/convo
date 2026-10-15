package convo

import "github.com/tzhnahida/VocabLock/internal/log"

// §6.8 偏向**上裁决路径**的接线。
//
// ⚠️ 这一段曾经完全不存在: `ValueWithBias` 只在 bias_test.go 里被调用,
// 也就是说「它更愿意接谁的话」这个机制**从来没有生效过**, 而它的单元测试全绿。
// 这是本项目第四次同型事故 (BodyLine / ApplyStyle / guard.go / conflictact.go)。
//
// 落点 (§6.8): 偏向**不改门槛**, 只改分数 —— 门槛是 §6.3 的事, 两者不能互相污染。
//
// ⚠️ 第二个返回值是「**有没有数据源**」, 不是"Δ 是不是 0"。
// 实测: 一个没有任何关系记录的场景, DecideBias 会给出 Δ=-1 ——
// 凭空扣分。表现是「它对新群里的人莫名冷淡」, 而每一个上下文都对。
// 所以没有数据源时**必须连偏向都不带**, 而不是带一个零。
func (e *Engine) groupBias(sc SceneMsg) (BiasDecision, bool) {
	if e.Store == nil || sc.SceneKey == "" {
		return BiasDecision{}, false
	}
	in := BiasInput{Speaker: Speaker{Scene: sc.SceneKey}}
	// ① 关系权重: §5.1 的三个量。
	// ⚠️ **Loaded=false 时 HasCloseness/HasRegard 保持 false** ——
	// 「没有记录」不等于「关系为零」, 混起来会让每个新群的人都先被扣一次分。
	br, err := LoadBond(e.Store.DB, e.ownerKey(), sc.SceneKey)
	if err != nil {
		// 静默的不是数据库, 是调用方: 读失败会让偏向**永远为零**, 而看不出是坏了。
		log.Warn("convo:bias", "关系量读取失败, 本轮按未提供处理: %v", err)
		return BiasDecision{}, false
	}
	if !br.Loaded {
		return BiasDecision{}, false
	}
	// ⚠️⚠️ **零值 = 未提供, 不是极值** —— 这条在这里踩过一次真的坑:
	//
	// recordGateOutcome 在**第一条消息之后**就会写一行关系记录 (Msgs=1, Closeness=0)。
	// 于是第二条消息读到的不是「还没建立」, 而是「亲密度为零 = 疏远」,
	// DecideBias 给出 Δ=-1 —— **刚认识就被扣分**。
	//
	// 判据: 只有**出现过正面证据** (分享过私事 / 道过谢) 才算「有关系量」。
	// 「聊过二十句但没分享过任何事」不是疏远, 是没有证据。
	if br.Closeness > 0 {
		in.Closeness, in.HasCloseness = br.Closeness, true
	}
	if br.Regard > 0 {
		in.Regard, in.HasRegard = br.Regard, true
	}
	if !in.HasCloseness && !in.HasRegard {
		return BiasDecision{}, false // 一条正面证据都没有 → 不带偏向
	}
	return DecideBias(in, DefaultBiasConfig()), true
}
