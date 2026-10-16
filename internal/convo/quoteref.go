package convo

import (
	"strconv"
	"strings"
	"time"
)

// ReplyRefSuffix 引用后缀 —— 附在这一行的**末尾** ✓。
//
// 形状 (2026-10-16 群主定):
//
// (回<被引用那条的当日序号>){<谁> <原话> <时间点>}
//
// ⚠️ 为什么是**后缀**而不是改行首: 行首那套格式有 21 处用例钉着 ✓,
// 而"把被引用的原话接上"是**纯追加** ✓ —— 它不改任何既有行 ✓,
// 却正面解决"「为什么」没有指代对象"那件事 ✓。
//
// ⚠️ 被引用的那条**可能在窗口之外** ✗ ⇒ `quoted` 里必须有它 ✓
// (调用方负责回查并填进来 ✓, 见 `LoadQuotedByMsgIDs`)。
// ⚠️ 查不到就**什么都不加** ✓ —— 宁可不附, 也不编一句"往上找" ✗。
func ReplyRefSuffix(m MessageRecord, quoted map[string]MessageRecord, nameByUIN map[string]string) string {
	id := strings.TrimSpace(m.ReplyTo)
	if id == "" {
		return "" // 不是引用回复
	}
	q, ok := quoted[id]
	if !ok {
		return "" // 引用的那条找不到 —— 不猜
	}
	txt := strings.TrimSpace(q.Payload)
	if txt == "" {
		txt = "[图片]"
	}
	if r := []rune(txt); len(r) > 60 {
		txt = string(r[:60]) + "…"
	}
	who := "她"
	if q.Dir != "ai" {
		who = "群友"
		if u := strings.TrimSpace(q.SenderUIN); u != "" {
			if n := strings.TrimSpace(nameByUIN[u]); n != "" {
				who = n + "(" + UinTail(u) + ")"
			} else {
				who = "群友(" + UinTail(u) + ")"
			}
		}
	}
	// 时间点取 HH:MM ✓ —— 跨天时序号本来就会重来 ✓, 而时间点不会 ✗,
	// 所以两个都给 ✓(群主定的是"（用户id）（时间点）" ✓)。
	hm := ""
	if t, err := time.ParseInLocation("2006-01-02 15:04:05", q.TS, time.Local); err == nil {
		hm = " " + t.Format("15:04")
	}
	seq := ""
	if q.DaySeq > 0 {
		seq = strconv.Itoa(q.DaySeq)
	}
	return " (回" + seq + "){" + who + " " + txt + hm + "}"
}

// LoadQuotedByMsgIDs 按平台 id 批量回查被引用的消息 ✓。
//
// ⚠️ **一次查询, 不在循环里逐条查** ✓ —— 与 `recentDialogIn` 取名字同一条规矩 ✓。
func (s *Store) LoadQuotedByMsgIDs(ids []string) (map[string]MessageRecord, error) {
	out := map[string]MessageRecord{}
	if len(ids) == 0 {
		return out, nil
	}
	ph := make([]string, 0, len(ids))
	args := make([]interface{}, 0, len(ids))
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			continue
		}
		ph = append(ph, "?")
		args = append(args, id)
	}
	if len(ph) == 0 {
		return out, nil
	}
	rows, err := s.DB.Query(`SELECT COALESCE(msg_id,''), COALESCE(day_seq,0), COALESCE(dir,''), `+
		`COALESCE(sender_uin,''), COALESCE(payload_json,''), COALESCE(ts,'') `+
		`FROM convo_messages WHERE msg_id IN (`+strings.Join(ph, ",")+`)`, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var r MessageRecord
		if err := rows.Scan(&r.MsgID, &r.DaySeq, &r.Dir, &r.SenderUIN, &r.Payload, &r.TS); err != nil {
			continue
		}
		out[r.MsgID] = r
	}
	return out, rows.Err()
}
