package convo

import (
	"os"
	"strings"
	"time"

	"github.com/tzhnahida/convo/internal/clock"

	"github.com/tzhnahida/convo/internal/log"
)

// ─────────────────────────────────────────────────────────────
// 用户提图但消息里没有图 → 去看**最近那张**
//
// ⚠️ 这是替桥接方补一个缺口 (2026-10-14 实测):
//
//	群友: [引用一张两个人的照片] @nahida 你在图片中看到了什么？
//	它:   [图: QQ表情265] 就一个表情包啊     ← 看的是另一条消息
//	它:   你要我描述 我也就看见一张脸
//
// 桥接方给的段类型统计: {text, at, face, image, file} —— **没有 reply**。
// 引用的信息在桥那边就丢了, convo 拿不到任何指向被引用消息的线索。
//
// ⚠️ 所以这里用的是**近似**: "刚发的那张图" 几乎总是用户指的那张。
// 它不完美, 但比"对着一条 [QQ表情265] 文本硬说看到了图"强得多。
// ─────────────────────────────────────────────────────────────

// ImageRecallWindow 往回找图的**时间窗**。
//
// ⚠️ 5 分钟: 短了遇上"发完图聊两句再问"就找不到;
// 长了会把十分钟前那张不相干的图给它, 而它会认真地描述错的那张。
const ImageRecallWindow = 5 * time.Minute

// imageMentionMarkers 用户"在说图"的信号。
//
// ⚠️ **必须真的提到图才去找** —— 无条件给最近那张的话,
// 一句普通的"在吗"也会让它看到几张图前的旧图, 然后莫名其妙地开始描述。
var imageMentionMarkers = []string{
	"看图", "这图", "那张图", "这张图", "图里", "图上", "图片", "照片", "截图",
	"表情包", "看到的", "描述一下", "这是什么图", "图呢", "识别",
}

// MentionsImage 这句话是不是在说一张图。
func MentionsImage(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" {
		return false
	}
	for _, m := range imageMentionMarkers {
		if strings.Contains(t, m) {
			return true
		}
	}
	return false
}

// recentImagePath 最近的、**真的还在盘上**的图。空 = 没有。
//
// ⚠️ 判据是 `convo_image.last_ts` 而不是消息时间 ——
// 前者是"我们处理过这张图"的时刻, 与用户"刚发完"的体感一致。
// ⚠️ 也要 `os.Stat` 一次: 库里有记录不等于文件还在
// (QQ 会清缓存, 而 inbox 也可能被 prune)。
func (e *Engine) recentImagePath() string {
	if e.Store == nil {
		return ""
	}
	items, err := e.Store.RecentImagePaths(e.ownerKey(), 8)
	if err != nil {
		return ""
	}
	cutoff := clock.Now().Add(-ImageRecallWindow)
	for _, it := range items {
		ts, perr := time.ParseInLocation(bodyTSLayout, it.TS, time.Local)
		if perr != nil || ts.Before(cutoff) {
			continue
		}
		// ⚠️ 表情包**不给看图** —— 它的标签就够了, 而且"看一张贴纸"
		// 往往不是用户想问的 (用户问的通常是照片)。
		if it.Kind == "sticker" {
			continue
		}
		if _, serr := os.Stat(it.Path); serr != nil {
			continue
		}
		log.Info("convo:engine", "用户提图但没带图 — 用最近那张: %s", it.Path)
		return it.Path
	}
	return ""
}

// lookOrRecent 有图就用图; 没图但它提了图 → 用最近那张照片。
//
// ⚠️ 两个前提**缺一不可**: 真的提到图 + 5 分钟内有照片。
// 少任何一个都会让它对着不相干的旧图开始描述 ——
// 那比"没看到"更糟, 因为它是**自信地描述错的东西**。
func (e *Engine) lookOrRecent(lookPath, text string) string {
	if lookPath != "" {
		return lookPath
	}
	if !MentionsImage(text) {
		return ""
	}
	return e.recentImagePath()
}
