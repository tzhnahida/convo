package convo

import (
	"context"
	"strings"
	"time"

	"github.com/tzhnahida/VocabLock/internal/log"
)

// IngestImage 一张入站图片的统一处理: 判重 → 打标签(带缓存) → 决定要不要给它看。
//
// 返回:
//
//	mark  —— 进上下文的文字标记 (如 "[表情: 反驳 怒 「愤怒喵！」]")
//	look  —— 值得让对话**真的看这张图**时的本地路径; 空 = 标签够用了
//
// ⚠️ 私聊与群**用同一条**。群路径当初漏接了 vision, 表现是
// "群友 @它问图里的人是谁, 它回「我这没看清」" —— 而它其实拿得到那张图。
//
// ⚠️ 表情包**不给看图**: 标签已经承载了"这个表情在做什么" (行为),
// 再看一遍图只是多一次调用。照片相反 —— 它值得被真的看一眼。
func (e *Engine) IngestImage(ctx context.Context, ref ImageRef) (mark, look string) {
	if e.Store == nil {
		return ImagePhotoMark, ""
	}
	// ⚠️ **先确认文件真的在** (2026-10-14 实测: 桥接方给的 path 常常指向不存在的文件)。
	// 不在 → 给它一个**说清"没读到"的标记**, 而不是 [图片]。
	// 后者会让它以为收到了图, 于是编出「图太糊了 看半天」。
	local := ref.Resolve()
	// ——— 取不到就**叫桥接方去下** ———
	//
	// ⚠️ 桥接方**不会主动下**: 下载的唯一入口是 `get_image(message_id)`。
	// 所以 `path_exists == "0"` 不是"这张图坏了", 而是"还没下, 你来叫一声"。
	//
	// 不叫它的后果就是用户看到的那两句:
	//   「图太糊了 看半天」/「我这边看到的就是一片空」
	// —— 而那是两张清晰的高清插画。
	if local == "" && ref.NeedsFetch && e.ImageFetch != nil {
		if p, ferr := e.ImageFetch.FetchImage(ref.MessageID); ferr != nil {
			log.Warn("convo:engine", "触发下载失败 (message_id=%d): %v", ref.MessageID, ferr)
		} else if p != "" {
			// ⚠️ 下载完**再验一次** —— 桥接方回的路径不一定就是最终落点,
			// 而"它说下好了"和"文件真的在"是两件事。
			//
			// ⚠️ 还要**等一下**: 桥接方回 transferStatus=4 (已下载) 之后,
			// 文件落地仍可能是异步的 (它先落盘再更新状态, 或者反过来)。
			// 不等的话会撞上一个纯粹的竞态 —— 而表现是"偶尔取不到图",
			// 那种间歇性故障最难查。
			//
			// 上限 3 秒: 够了, 而且不会把一个必然失败的下载拖成十几秒。
			for try := 0; try < 6; try++ {
				local = FirstExistingFile(append([]string{p}, ref.Candidates...))
				if local != "" {
					break
				}
				select {
				case <-ctx.Done():
					return ImageUnreadMark, ""
				case <-time.After(500 * time.Millisecond):
				}
			}
			if local == "" {
				log.Warn("convo:engine", "下载回了路径但等了 3 秒文件仍不在: %s", p)
			}
		}
	}
	if local == "" {
		log.Warn("convo:engine", "图片没读到 (needsFetch=%v, message_id=%d, 候选 %v) — 如实告诉它",
			ref.NeedsFetch, ref.MessageID, ref.Candidates)
		return ImageUnreadMark, ""
	}
	seen, cached := e.Store.NoteImage(e.ownerKey(), ref.MD5, local)
	// 有标签 → 复用, 不看图 (标签是缓存, 不是省略)。
	if l, ok := ParseTag(cached); ok {
		// ⚠️ **一律渲染完整标记** (2026-10-14 改)。
		//
		// 原来这里是 `l.RenderSeen(seen)` —— 入库时就把"第几次见到"
		// 烧进 payload。后果是线上出现两条**都**是引用式的行,
		// 它们指向的完整内容早已滑出窗口 → 「往上找」找到的是另一句
		// 「往上找」, 指针悬空, 它只能回「你发的那图我这边看不清内容」。
		//
		// 去重没取消, 只是**搬到了拼历史那一刻** (CollapseRepeatedImageMarks):
		// 那时才知道窗口里到底有没有那份完整内容。
		mark := l.Render()
		if l.Kind == "photo" {
			return mark, local
		}
		return mark, ""
	}
	imgPath, err := e.prepareImage(ctx, local)
	if err != nil {
		log.Warn("convo:engine", "image prepare: %v", err)
		return ImagePhotoMark, ""
	}
	l, ok := e.LabelImage(ctx, imgPath)
	if !ok {
		// 标注不出来 —— 至少把"有人发了张图"记下来, 而且**给它看**,
		// 因为这时候标签帮不上忙, 图是唯一的信息源。
		log.Warn("convo:engine", "标注失败, 退回让它直接看图")
		return ImagePhotoMark, imgPath
	}
	if serr := e.Store.SetImageTag(e.ownerKey(), ref.MD5, l.MarshalTag()); serr != nil {
		log.Warn("convo:engine", "标签落库失败: %v", serr)
	}
	// ⚠️ 同样一律完整 —— 理由见上面那段。
	mark = l.Render()
	log.Info("convo:engine", "标注: %s (%s)", mark, l.Meaning)
	if l.Kind != "sticker" {
		return mark, imgPath
	}
	// 表情包: 偷图 (群里发得够多才收), 但**不给看图**。
	note := strings.TrimSpace(l.Meaning)
	if _, adopted, aerr := e.AdoptSticker(ctx, imgPath, ref.MD5, note, seen); aerr != nil {
		log.Warn("convo:engine", "偷图失败: %v", aerr)
	} else if !adopted && seen < AdoptMinSeen {
		log.Info("convo:engine", "表情包还不够流行 (%d/%d), 先记账", seen, AdoptMinSeen)
	}
	return mark, ""
}
