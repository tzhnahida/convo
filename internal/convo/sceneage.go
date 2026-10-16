package convo

import "time"

// sceneFirstSeenTS 这个场景**第一次被登记**的时刻。
//
// 读不到 / 解析不了 → **零值** ✓, 而调用方据此**不判**(往正常说话倒 ✓)。
//
// ⚠️ 它存在的理由在 `IsNewMember` 的一段注释里 —— 见那里。
func (s *Store) sceneFirstSeenTS(sceneKey string) time.Time {
	var first string
	if err := s.DB.QueryRow(`SELECT COALESCE(first_seen_ts,'') FROM convo_scene WHERE scene_key=?`,
		sceneKey).Scan(&first); err != nil {
		return time.Time{}
	}
	t, err := time.ParseInLocation(sceneTSLayout, first, time.Local)
	if err != nil {
		return time.Time{}
	}
	return t
}
