package convo

import (
	"strings"

	"github.com/tzhnahida/convo/internal/clock"
	"github.com/tzhnahida/convo/internal/log"
)

// ─────────────────────────────────────────────────────────────
// §5.6 **场景阶段的写入侧** (2026-10-16 接线)
//
// ⚠️ 在它之前, `convo_scene.stage` **只有插入时那一次写入**:
//
//	`EnsureScene` / `ObserveMember` 插进去时一律 `StageStranger`,
//	而**没有任何东西升级过它** —— 唯一会 UPDATE 它的 `SaveScene` 零调用。
//
// ⚠️ 而读它的两条路都是**群里的社交默认**:
//
//	· `SelfRoleLine`(§6.10.5): 「**关系阶段决定"能不能怼"**」——
//		设计稿说它「**比人设厚度重要**」(L929「固定位 > 人设厚度」);
//	· `conflictwire` 的 A1: 「阶段=陌生 → **有冲突也不掺和**」。
//
// ⇒ 后果是: **它在群里永远按"陌生"那一档说话**,
// 而"关系变熟了"这件事在群路径上**不存在** ——
// 它不会怼、不会起外号、不掺和任何冲突, 无论私聊里多熟。
//
// ⚠️ 这又是"消费者接上了、生产者不存在"那一类 ——
// 而棘轮(只看"有没有调用者")**看不见它**: `SelfRoleLine` 有调用者 ✓,
// 缺的是**写那个字段的人**。
// ─────────────────────────────────────────────────────────────

// SceneStageFromBond 关系量阶段 → **场景阶段**。
//
// ⚠️ **刻意不重用 `heatStageOf`**: 它只有三档
// (它自己的注释写着「Acquaint 与 Stranger 共用最小 cap」),
// 而场景阶段有四档 —— 拿它当映射会把「**认识**」降级成「**陌生**」。
//
// ⚠️⚠️ **两套常量对同一个概念有两种拼法, 这个函数是它们唯一的转换点**:
//
//	`bond.go`      : BondStageAcquaint = "**acquaint**"
//	`scene.go`     : StageAcquaint     = "**acquaintance**"
//
// 直接把 `BondStage` 强转成 `string` 写进 `convo_scene.stage` 会得到一个
// **谁也认不出来的值** —— `SelfRoleLine` 的 switch 会静默落进 `default`,
// 而 default 的表现恰好**和"陌生"一样**, 所以那个 bug 看起来"一切正常"。
//
// ⇒ 所以这里**逐档显式映射**, 而不是 `string(stage)`。
func SceneStageFromBond(stage BondStage) string {
	switch stage {
	case BondStageIntimate:
		return StageIntimate
	case BondStageFamiliar:
		return StageFamiliar
	case BondStageAcquaint:
		// ⚠️ 注意拼法: 是 `StageAcquaint`("acquaintance"), 不是 bond 那边的 "acquaint"。
		return StageAcquaint
	default:
		return StageStranger
	}
}

// NoteSceneStage 把关系阶段**落到 `convo_scene.stage`**。
//
// ⚠️ **关系量由调用方传进来**, 不在这里重读 —— 调用方刚从库里读到它,
// 再读一次只是平白多一次 IO。而"两处各自读一次"还有一个更坏的后果:
// 两次读之间关系量可能已经变了, 于是落库的阶段与调用方看到的不是同一个。
//
// ⚠️ **只在变了的时候写** —— 每一轮都 UPDATE 会让 `updated_ts` 失去意义
// (而它是"这个场景最后动过"的读数), 还会平白多一次写。
//
// ⚠️ **还没有关系量 → 什么都不做**(保持现状), 不是"降级成陌生":
// `EnsureScene` 插入时那条 `stranger` 就是它该有的样子。
func (e *Engine) NoteSceneStage(sceneKey string, rec BondRecord) {
	if e.Store == nil || e.Store.DB == nil {
		return
	}
	if strings.TrimSpace(sceneKey) == "" {
		return
	}
	if !rec.Loaded {
		return // 还没有关系量 = **未提供** → 不动(插入时那条 stranger 就是它该有的样子)
	}
	want := SceneStageFromBond(BondStageWithMult(rec.Bond, e.OwnerMultForScene(sceneKey)))
	var cur string
	if err := e.Store.DB.QueryRow("SELECT COALESCE(stage,'') FROM convo_scene WHERE scene_key=?",
		sceneKey).Scan(&cur); err != nil {
		return // 场景行还没建 → 由 EnsureScene 建, 这一轮不碰
	}
	if cur == want {
		return // 没变就不写
	}
	if _, err := e.Store.DB.Exec("UPDATE convo_scene SET stage=?, updated_ts=? WHERE scene_key=?",
		want, clock.TodayTS(), sceneKey); err != nil {
		log.Warn("convo:bond", "§5.6 场景阶段落库失败(群里仍按旧档说话): %v", err)
		return
	}
	log.Info("convo:bond", "§5.6 场景阶段 %s → %s (场景 %s)", cur, want, sceneKey)
}
