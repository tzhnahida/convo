package convo

import (
	"path/filepath"
	"testing"
)

// TestPruneOrphanDownloads_OnlyTouchesDownloaderLeftovers
//
// 判据必须**又窄又明确**: 只有"池里没有行指着它"且"文件名是下载器的形状"才删。
// ⚠️ 池目录默认就是内置包目录 —— 用户往里丢一张图是合法的, 那一下绝不能删。
func TestPruneOrphanDownloads_OnlyTouchesDownloaderLeftovers(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir, "test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	// ① 下载残留 (匹配形状, 没有任何行指着它) -> 该删
	orphan := mkStickerFile(t, dir, "1791973675974228752_4c606425.jpg", "LEFTOVER")
	// ② 用户手放的图 (形状不对) -> 一根汗毛都不许动
	userPicked := mkStickerFile(t, dir, "我很喜欢这张.gif", "USER")
	// ③ 有行指着的下载文件 -> 不许动
	kept := mkStickerFile(t, dir, "1791973675974228753_aabbccdd.gif", "KEEP")
	if _, err := store.InsertSticker("http://x/k.gif", kept, "ja", "firecrawl-img", 0.8, "DEADBEEF"); err != nil {
		t.Fatal(err)
	}

	// 预演: 只算不删
	if n, err := store.PruneOrphanDownloads(dir, true); err != nil || n != 1 {
		t.Fatalf("预演应当报 1 个孤儿: n=%d err=%v", n, err)
	}
	if !isRegularFile(orphan) {
		t.Fatal("预演不许删文件")
	}

	if n, err := store.PruneOrphanDownloads(dir, false); err != nil || n != 1 {
		t.Fatalf("实跑应当删 1 个: n=%d err=%v", n, err)
	}
	if isRegularFile(orphan) {
		t.Fatal("下载残留应当被删掉")
	}
	for _, p := range []string{userPicked, kept} {
		if !isRegularFile(p) {
			t.Fatalf("不该删的文件没了: %s", p)
		}
	}
	// 目录不存在: 不算错 (运维命令不该因此失败)
	if n, err := store.PruneOrphanDownloads(filepath.Join(dir, "没有这个目录"), false); err != nil || n != 0 {
		t.Fatalf("目录不存在应当是 0 而不是错误: n=%d err=%v", n, err)
	}
}
