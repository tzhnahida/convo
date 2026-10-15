package convo

// ─────────────────────────────────────────────────────────────
// §4.4.1b **语域漂移的读取口** (2026-10-16 接线)
//
// ⚠️ 它存在的理由是一条**会自我强化**的路:
//
//   > 夜间社交模拟会**反过来污染**白天的语域(模拟出的对话进记忆)。
//   > 语域已经变硬时再叠一层模拟, 等于让"变硬"自我强化 ——
//   > **这是本设计里唯一会正反馈的路径, 必须掐掉。**
//
// 而 `DriftDisablesNightSocial` 那个判据**从来没有调用者** ——
// 闸门一直开着。
//
// ⚠️ 口径与 `value.go` 里那条注入**同一个**:
// 基线取最近 7 天的并集平均, 最近取 1 天。
// 两处各写一遍的话, "提示词说它变硬了"与"闸门按变硬处理"会漂开 ——
// 而那正是最难看的一种不一致。
// ─────────────────────────────────────────────────────────────

// registerDriftDir 当前语域漂移方向。
//
// 返回 (方向, 是否算得出来, 错误)。
//
// ⚠️ **三态, 不是两态**:
//
//	· `driftKnown == false` → 样本不够 / 读不到 ⇒ 调用方**照常跑**;
//	· `driftKnown == true` 且方向是 `harder` ⇒ 挡住;
//	· `driftKnown == true` 且方向是 `softer`/空 ⇒ 照常跑。
//
// 把前两者并成一个 `false` 的话, "读不到"就会被当成"没漂" ——
// 而那时闸门**看起来在工作**, 实际什么都没拦。
func (e *Engine) registerDriftDir() (string, bool, error) {
	if e.Store == nil || e.Store.DB == nil {
		return "", false, nil
	}
	base, err := LoadRegisterBaseline(e.Store.DB, e.ownerKey(), 7)
	if err != nil {
		return "", false, err
	}
	recent, err := LoadRegisterBaseline(e.Store.DB, e.ownerKey(), 1)
	if err != nil {
		return "", false, err
	}
	d := DetectRegisterDrift(base, recent)
	if !d.Drifted {
		return d.Dir, false, nil
	}
	return d.Dir, true, nil
}
