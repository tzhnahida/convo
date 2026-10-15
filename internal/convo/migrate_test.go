package convo

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// ⚠️ **回归尺子**: 一组迁移只许挂在自己的检查下面。
//
// 真实事故: 5 条 `ALTER TABLE convo_memory` 被塞进了 `if stickers.note 不存在` 的分支。
// Pi 上 stickers.note 早就有 → 分支不进 → source 列永远加不上 →
// 每一次 UpsertMemory / dailyExtract 都报 "no column named source",
// 而现象只是"它不记事"「夜里什么都没发生」。
func TestMigrate_MemoryColumnsAreNotGatedOnStickers(t *testing.T) {
	dir := t.TempDir()
	file := "old.db"
	// 造一个"老库": 有 stickers.note (于是那个 if 不会进), 但 convo_memory 缺列。
	db, err := sql.Open("sqlite", filepath.Join(dir, file))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE stickers (id INTEGER PRIMARY KEY, note TEXT DEFAULT '')`,
		// ⚠️ 夹具必须是**真实的旧 schema** —— 少列会让"补列之后写不进去"看起来像迁移的错,
		// 其实是夹具自己的错 (这条我踩过一次)。
		`CREATE TABLE convo_memory (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			mem_id TEXT UNIQUE, k TEXT, v TEXT, label TEXT, kind TEXT, description TEXT,
			confidence REAL, src_event_id TEXT, state TEXT DEFAULT 'active',
			created_ts TEXT, updated_ts TEXT, access_count INTEGER DEFAULT 0, last_access_ts TEXT)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Close()

	// 走正常打开 (会跑 migrate)。
	s, err := OpenStore(dir, file)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	for _, col := range []string{"source", "last_injected_ts", "inject_count", "due_ts", "status"} {
		var n int
		if err := s.DB.QueryRow("SELECT COUNT(*) FROM pragma_table_info('convo_memory') WHERE name = ?", col).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("迁移没有补上 convo_memory.%s —— 它被挂在了别人的检查下面", col)
		}
	}
	// 补上了就必须**真的能写** —— 列存在但写入路径失败一样是坏的。
	if err := s.UpsertMemory(MemoryDelta{MemID: "m1", K: "food", V: "爱吃甜的",
		Label: "口味", Kind: "fact", Confidence: 0.9, Source: "user_said"}, "active"); err != nil {
		t.Fatalf("补列之后仍然写不进去: %v", err)
	}
	got, err := s.TopMemoriesFor("", 5, nowForTest())
	if err != nil || len(got) != 1 {
		t.Fatalf("写入后读不回来: %+v err=%v", got, err)
	}
}

// 新库也必须一次到位 (CREATE 里带全了就不该依赖 ALTER)。
func TestMigrate_FreshDBHasMemoryColumns(t *testing.T) {
	s := newTestStore(t)
	for _, col := range []string{"source", "sensitivity", "scene_key", "slot_key", "superseded_ts", "valid_until"} {
		var n int
		if err := s.DB.QueryRow("SELECT COUNT(*) FROM pragma_table_info('convo_memory') WHERE name = ?", col).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("新库缺列 %s", col)
		}
	}
}
