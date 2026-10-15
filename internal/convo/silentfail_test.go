package convo

import "testing"

// ——— 调查: 列名与值个数不匹配时, 驱动到底报不报错? ———
//
// 背景(P7): `IngestWorldEvents` 的 INSERT 列名清单一度是 10 个、值却是 11 个,
// 成因是**半落盘的编辑**。当时我以为"SQL 报错被 err==nil 吞掉了",
// 但同一时期的仿真报告**仍然显示入账成功** —— 这一点没查清, 记了遗留。
//
// 这条测试去查清它: **先自己造一个不匹配, 看驱动怎么说。**
func TestInvestigation_MismatchedInsertBehaviour(t *testing.T) {
	s, err := OpenStore(t.TempDir(), "test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// 故意: 10 个列名, 11 个值 —— 与 P7 当时的状态完全一致
	_, err = s.DB.Exec(`INSERT INTO convo_world_event
		(event_key, ts, kind, source, text, valence, arousal, intensity, weight, duration)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		"k1", "2026-05-01 10:00:00", "environment", "生活", "快递到了",
		0.2, 0.5, 0.4, 1.0, "pulse", "2026-05-01 10:00:00")
	t.Logf("列 10 / 值 11 → err = %v", err)

	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM convo_world_event`).Scan(&n)
	t.Logf("实际入库条数 = %d", n)

	// ✅ 结案 (2026-10-13): 驱动**会报错** —— 静默的不是数据库, 是调用方。
	if err == nil {
		t.Fatal("驱动竟然接受了列/值不匹配 —— 那整条推理都要重来")
	}
	if n != 0 {
		t.Fatalf("报错了却还入库 %d 条, 状态不确定", n)
	}
}
