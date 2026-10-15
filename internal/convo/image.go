package convo

import (
	"database/sql"
	"strings"

	"github.com/tzhnahida/VocabLock/internal/clock"
)

// ─────────────────────────────────────────────────────────────
// 图片: 按哈希去重 + 照片/表情包分流
//
// ⚠️ 判据来自实测 (2026-10-14 用户定的口径):
//   **重复度高的就是表情包, 一次性的才是照片。**
//
// 数据坐实:
//   16:03:34 / 16:02:24 / 15:55:32 —— 同一张 79b2858e….jpg 发了 3 次
//   14:38:17 —— 同一张 7b5cf845….jpg 连着两条
//   而 64cc0d94….png / f2a6d0f4….jpg 只出现一次
//
// 它同时省掉两笔冤枉钱:
//   ① **vision 调用** —— 同一张贴纸没必要反复描述 (重复的图占群里图片的大头);
//   ② **上下文** —— 20 条"[图片]"会把真正在说的话挤没。
//
// ⚠️ 但表情包**不许直接从上下文里删掉** —— "有人发了张表情"也是一件事。
// 它只是**降级成一个词** ([表情]), 而不是消失。
// ─────────────────────────────────────────────────────────────

// ImageRef 一条图片段: 路径 + 桥接方给的 md5。
type ImageRef struct {
	// Path 首选路径 (Candidates[0])。**不保证存在** —— 存在性由 IngestImage 验。
	Path string
	MD5  string
	// Candidates 按可信度排序的候选路径 (含 Path)。解析层给出, 存在性层筛选。
	Candidates []string
	// MessageID 这条消息在桥接方那边的 id —— **触发下载要用它**。
	MessageID int32
	// NeedsFetch 桥接方明说"图还没下" (`path_exists == "0"`)。
	//
	// ⚠️ 这不是"坏了", 是**它在等你去叫它** ——
	// 下载的唯一入口是 `get_image(message_id)`。
	NeedsFetch bool
}

// Resolve 选出**真的在盘上**的那个候选; 都不在 → 空串。
func (r ImageRef) Resolve() string {
	if len(r.Candidates) == 0 {
		return FirstExistingFile([]string{r.Path})
	}
	return FirstExistingFile(r.Candidates)
}

const (
	// ImageStickerRepeats 同一张图出现几次之后算表情包。
	// ⚠️ 3 次: 2 次可能只是手滑发重了, 3 次才是"这就是个贴纸"。
	ImageStickerRepeats = 3
	// ImagePhotoMark / ImageStickerMark 两种渲染标记。
	// ⚠️ **标记必须自解释** (2026-10-14 实测)。
	//
	// 原来是 `[图片]` / `[图片: 这边没加载出来]` —— 而模型**分不清**
	// "我看过了" 和 "我没看到": 它把前者也读成了"图有问题", 于是编
	// 「图太糊了」「一片空白」「图在我这儿裂了」。
	//
	// 现在的措辞只有两种状态, 而且**都说清了内容在哪**:
	//   看到了 → `[图: <内容>]`   (内容就在括号里)
	//   没看到 → `[图没读到]`     (别猜)
	ImagePhotoMark   = "[图: 一张图, 没细看]"
	ImageStickerMark = "[图: 一个表情]"
	// ImageUnreadMark 图片**没读到**时的标记。
	//
	// ⚠️ 与 [图片] 是**两回事**, 必须分开 (2026-10-14 用户截图):
	//   桥接方给了路径但文件没导出 → 我们压根看不到内容。
	//   而 [图片] 让它以为"图我收到了", 于是编出
	//   「图太糊了 看半天」「我也认不出来」—— **那是一张清晰的高清插画**。
	//
	// 宁可说"没加载出来", 也不许假装看过 —— 这是 §「不许凭空断言」的具体落点。
	// ⚠️ 与上面两个是**互斥**的两种状态 —— 而且它明确禁止猜。
	ImageUnreadMark = "[图没读到: 这张图我这边没拿到, 别猜内容]"
)

// EnsureImageSchema 建表。
func EnsureImageSchema(db *sql.DB) error {
	_, err := db.Exec("CREATE TABLE IF NOT EXISTS convo_image (" +
		"owner TEXT NOT NULL DEFAULT '', md5 TEXT NOT NULL, " +
		"path TEXT NOT NULL DEFAULT '', seen_n INTEGER NOT NULL DEFAULT 0, " +
		"first_ts TEXT NOT NULL DEFAULT '', last_ts TEXT NOT NULL DEFAULT '', " +
		"tag TEXT NOT NULL DEFAULT '', " +
		"PRIMARY KEY (owner, md5))")
	// ⚠️ 老库补列 —— **无条件跑**, 不挂在任何别的检查下面。
	// (这个项目里我犯过一次: 一组 ALTER 挂在 `stickers.note` 的检查里,
	//  结果 Pi 上 convo_memory.source 一个月都没加上, 记忆写入全废。)
	_, _ = db.Exec("ALTER TABLE convo_image ADD COLUMN tag TEXT NOT NULL DEFAULT ''")
	return err
}

// NoteImage 记一次"这张图又来了", 返回**累计次数**。
//
// ⚠️ md5 为空时返回 0 —— 没哈希就没法判重, 那就**当成照片**(保守: 宁可多看一次)。
func (s *Store) NoteImage(owner, md5, path string) (int, string) {
	m := strings.ToLower(strings.TrimSpace(md5))
	if m == "" {
		return 0, ""
	}
	now := clock.Now().Format(bodyTSLayout)
	if _, err := s.DB.Exec("INSERT INTO convo_image (owner, md5, path, seen_n, first_ts, last_ts) "+
		"VALUES (?,?,?,1,?,?) ON CONFLICT(owner, md5) DO UPDATE SET "+
		"seen_n = convo_image.seen_n + 1, last_ts = excluded.last_ts, "+
		"path = CASE WHEN excluded.path != '' THEN excluded.path ELSE convo_image.path END",
		owner, m, path, now, now); err != nil {
		return 0, ""
	}
	var n int
	var tag string
	if err := s.DB.QueryRow("SELECT seen_n, COALESCE(tag,'') FROM convo_image WHERE owner = ? AND md5 = ?", owner, m).Scan(&n, &tag); err != nil {
		return 0, ""
	}
	return n, tag
}

// SetImageTag 存这张图的标签 (第一次看过之后)。
//
// ⚠️ 标签是**缓存**不是省略: 同一张贴纸第二次出现时不必再看图,
// 但它**这次想说什么**由标签承载 —— 而不是被丢掉。
func (s *Store) SetImageTag(owner, md5, tag string) error {
	m := strings.ToLower(strings.TrimSpace(md5))
	if m == "" || strings.TrimSpace(tag) == "" {
		return nil
	}
	_, err := s.DB.Exec("UPDATE convo_image SET tag = ? WHERE owner = ? AND md5 = ?", tag, owner, m)
	return err
}

// ImageSeen 这张图见过几次 (0 = 没见过)。
func (s *Store) ImageSeen(owner, md5 string) int {
	m := strings.ToLower(strings.TrimSpace(md5))
	if m == "" {
		return 0
	}
	var n int
	if err := s.DB.QueryRow("SELECT seen_n FROM convo_image WHERE owner = ? AND md5 = ?", owner, m).Scan(&n); err != nil {
		return 0
	}
	return n
}

// ImageIsSticker 这张图算不算表情包 (重复度够高)。
func ImageIsSticker(seen int) bool { return seen >= ImageStickerRepeats }

// ImageMark 这张图在上下文里该显示成什么。
//
// ⚠️ 表情包**降级成一个词, 不是删掉** —— "有人发了张表情"也是一件事。
func ImageMark(seen int) string {
	if ImageIsSticker(seen) {
		return ImageStickerMark
	}
	return ImagePhotoMark
}

// ImageRow 图片表里的一行 (回溯用)。
type ImageRow struct {
	Path string
	TS   string
	Kind string
}

// RecentImagePaths 最近的几张图, 新的在前。
//
// ⚠️ kind 从 tag 里现推 —— 那是**派生量**, 不落库 (这个项目的老纪律)。
func (s *Store) RecentImagePaths(owner string, limit int) ([]ImageRow, error) {
	if limit <= 0 {
		limit = 8
	}
	rows, err := s.DB.Query("SELECT COALESCE(path,''), COALESCE(last_ts,''), COALESCE(tag,'') "+
		"FROM convo_image WHERE owner = ? AND path != '' ORDER BY last_ts DESC LIMIT ?", owner, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ImageRow
	for rows.Next() {
		var r ImageRow
		var tag string
		if rows.Scan(&r.Path, &r.TS, &tag) != nil {
			continue
		}
		if l, ok := ParseTag(tag); ok {
			r.Kind = l.Kind
		}
		out = append(out, r)
	}
	return out, nil
}
