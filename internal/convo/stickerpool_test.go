package convo

import (
	"os"
	"path/filepath"
	"testing"
)

func mkStickerFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestFileDigest_ContentIsTheIdentity 池的身份是**内容**: 同名与不同名都不算数。
func TestFileDigest_ContentIsTheIdentity(t *testing.T) {
	dir := t.TempDir()
	a := mkStickerFile(t, dir, "a.gif", "AAA")
	b := mkStickerFile(t, dir, "b.gif", "AAA")
	c := mkStickerFile(t, dir, "c.gif", "BBB")
	da, err := FileDigest(a)
	if err != nil {
		t.Fatal(err)
	}
	db, _ := FileDigest(b)
	dc, _ := FileDigest(c)
	if da != db {
		t.Fatalf("同样内容必须是同一个指纹: %s vs %s", da, db)
	}
	if da == dc {
		t.Fatalf("不同内容不该撞指纹: %s", da)
	}
	if _, err := FileDigest(filepath.Join(dir, "没有这个文件.gif")); err == nil {
		t.Fatal("读不到文件应当报错, 不能返回空指纹当成功")
	}
}

// TestStickerLookups 入库前那两次查问: 大小写、跨池、空值。
func TestStickerLookups(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir, "test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	id, err := store.InsertSticker("http://x/1.gif", filepath.Join(dir, "1.gif"), "ja", "tenor", 0.8, "AABB")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok, err := store.StickerByDigest("ja", "aabb"); err != nil || !ok || got != id {
		t.Fatalf("指纹命中应当不区分大小写: got=%d ok=%v err=%v", got, ok, err)
	}
	if _, ok, _ := store.StickerByDigest("ja", "CCDD"); ok {
		t.Fatal("库里没有的指纹不该命中")
	}
	if _, ok, _ := store.StickerByDigest("zh", "aabb"); ok {
		t.Fatal("别的语言池不算同一个池")
	}
	if _, ok, _ := store.StickerByDigest("ja", ""); ok {
		t.Fatal("空指纹一律当没有 —— 否则所有没指纹的老行会互相命中")
	}
	if got, ok, err := store.StickerBySrcURL("ja", "http://x/1.gif"); err != nil || !ok || got != id {
		t.Fatalf("URL 查问失败: got=%d ok=%v err=%v", got, ok, err)
	}
	if _, ok, _ := store.StickerBySrcURL("ja", "http://x/2.gif"); ok {
		t.Fatal("没收录过的 URL 不该命中")
	}
}

// TestDedupeStickers_KeepsTheBestAndMergesUse
//
// 这就是 2026-10-16 那次事故的缩小版: 同一张图三行 (affinity 0.5/0.9/0.7)。
// 保留 affinity 最高的那行, use_count **求和** (喜欢过的次数不该蒸发),
// 另外两行的文件删掉 —— 但**另一张图那行一根汗毛都不许动**。
func TestDedupeStickers_KeepsTheBestAndMergesUse(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir, "test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	a := mkStickerFile(t, dir, "a.gif", "SAME")
	b := mkStickerFile(t, dir, "b.gif", "SAME")
	c := mkStickerFile(t, dir, "c.gif", "SAME")
	d := mkStickerFile(t, dir, "d.gif", "OTHER")
	ida, _ := store.InsertSticker("u1", a, "ja", "firecrawl-img", 0.5, "")
	idb, _ := store.InsertSticker("u2", b, "ja", "firecrawl-img", 0.9, "")
	idc, _ := store.InsertSticker("u3", c, "ja", "firecrawl-img", 0.7, "")
	idd, _ := store.InsertSticker("u4", d, "ja", "firecrawl-img", 0.6, "")
	_, _ = store.DB.Exec("UPDATE stickers SET use_count=1 WHERE id=?", ida)
	_, _ = store.DB.Exec("UPDATE stickers SET use_count=2 WHERE id=?", idc)

	// ① 预演: 只算不写
	rep, err := store.DedupeStickers(true)
	if err != nil {
		t.Fatal(err)
	}
	// 三行同一张图 → 留 1 删 2; 另一张图那行自己一组、不动。
	if rep.RowsBefore != 4 || rep.RowsAfter != 2 || rep.Dropped != 2 || rep.FilesGone != 2 || rep.Groups != 2 {
		t.Fatalf("预演数字不对: %+v", rep)
	}
	if n := countStickers(t, store); n != 4 {
		t.Fatalf("预演不许改库, 现在 %d 行", n)
	}
	for _, p := range []string{a, b, c, d} {
		if !isRegularFile(p) {
			t.Fatalf("预演不许删文件: %s 没了", p)
		}
	}

	// ② 真跑
	rep, err = store.DedupeStickers(false)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Dropped != 2 || rep.FilesGone != 2 {
		t.Fatalf("真跑数字不对: %+v", rep)
	}
	if n := countStickers(t, store); n != 2 {
		t.Fatalf("收拢后应当 2 行 (b 与 d), 实为 %d", n)
	}
	var uses int
	var dig string
	if err := store.DB.QueryRow("SELECT use_count, COALESCE(digest,'') FROM stickers WHERE id=?", idb).Scan(&uses, &dig); err != nil {
		t.Fatal(err)
	}
	if uses != 3 {
		t.Fatalf("use_count 应当求和合并成 3, 实为 %d", uses)
	}
	if dig == "" {
		t.Fatal("保留行的指纹应当被补写 (老库是空的)")
	}
	for _, p := range []string{a, c} {
		if isRegularFile(p) {
			t.Fatalf("重复文件应当被删: %s", p)
		}
	}
	for _, p := range []string{b, d} {
		if !isRegularFile(p) {
			t.Fatalf("该留的文件被删了: %s", p)
		}
	}
	if _, ok, _ := store.StickerByDigest("ja", dig); !ok {
		t.Fatal("收拢后应当能按指纹查到保留行")
	}
	if got, ok, _ := store.StickerBySrcURL("ja", "u4"); !ok || got != idd {
		t.Fatal("另一张图那行不许被动")
	}

	// ③ 幂等: 再跑一次什么都不该发生
	rep, err = store.DedupeStickers(false)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Dropped != 0 || rep.FilesGone != 0 {
		t.Fatalf("第二次跑不该再有动作: %+v", rep)
	}
}

// TestDedupeStickers_MissingFileIsLeftAlone
//
// 文件已经不在的行算不出指纹 → 各自成组、一律不动, 只在报告里计 NoFile。
// (替用户删行 = 替用户删东西; 他的文件可能在别的盘上。)
func TestDedupeStickers_MissingFileIsLeftAlone(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir, "test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	missing := filepath.Join(dir, "gone.gif")
	idm, _ := store.InsertSticker("um", missing, "ja", "firecrawl-img", 0.5, "")
	p := mkStickerFile(t, dir, "ok.gif", "OK")
	_, _ = store.InsertSticker("uo", p, "ja", "firecrawl-img", 0.6, "")
	rep, err := store.DedupeStickers(false)
	if err != nil {
		t.Fatal(err)
	}
	if rep.NoFile != 1 {
		t.Fatalf("应当报 1 行无文件, 实为 %d", rep.NoFile)
	}
	if rep.Dropped != 0 {
		t.Fatalf("无文件的行不许被合并掉: %+v", rep)
	}
	if n := countStickers(t, store); n != 2 {
		t.Fatalf("两行都该还在, 实为 %d", n)
	}
	if _, ok, _ := store.StickerBySrcURL("ja", "um"); !ok {
		t.Fatalf("无文件那行被删了 (id=%d)", idm)
	}
}

func countStickers(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM stickers").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
