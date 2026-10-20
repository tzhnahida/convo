package convo

import (
	"regexp"
	"strings"

	"github.com/tzhnahida/convo/internal/clock"
	"github.com/tzhnahida/convo/internal/log"
)

// ─────────────────────────────────────────────────────────────
// 口头答应 → 系统补记 (2026-10-20 实测)
//
// 现场:
//   你: 明天晚上7点能不能来找我聊天呢?
//   它: 嗯 可以 / 不过七点我可能还在外面 晚点也行吧
//   → convo_appointment 仍是 0 行。
//
// **说话和记账是两条路**: 模型在文本里答应了, 但帧里没带 promise,
// 于是明晚它不会记得 —— 那是失约的根, 不是不礼貌。
// 这里做兜底: 文本里"答应了一件未来时间的事" → 系统补一条约定。
// ─────────────────────────────────────────────────────────────

// promiseAskMarkers 用户在**约时间**。
var promiseAskMarkers = []string{"能不能", "可以吗", "有空吗", "约", "一起", "来找我", "见面"}

// promiseTimeMarkers 句子里有**未来时间**。
var promiseTimeMarkers = []string{"明天", "今晚", "后天", "周末", "周", "点"}

// promiseYesMarkers 它的回复是**答应**。
var promiseYesMarkers = []string{"可以", "行", "好", "没问题", "当然", "嗯"}

var promiseHourRe = regexp.MustCompile("(\\d{1,2})\\s*点")

// maybeRecordVerbalPromise 帧里没有 promise 时, 用文本兜底记一条约定。
func (e *Engine) maybeRecordVerbalPromise(in DialogInput, frame OutputFrame) {
	if e.Store == nil || frame.Refused || frame.Promise != nil {
		return
	}
	ask := strings.TrimSpace(in.UserText)
	if ask == "" {
		return
	}
	if !containsAny(ask, promiseAskMarkers) || !containsAny(ask, promiseTimeMarkers) {
		return
	}
	reply := FrameText(frame)
	if strings.TrimSpace(reply) == "" || !containsAny(reply, promiseYesMarkers) {
		return
	}
	// 幂等: 同一条原话已经有未结算的约定 → 不重复记 (重试/多帧时会重复调)。
	var n int
	_ = e.Store.DB.QueryRow("SELECT count(*) FROM convo_appointment WHERE text = ? AND terminal = ''", ask).Scan(&n)
	if n > 0 {
		return
	}
	due := ""
	if m := promiseHourRe.FindStringSubmatch(ask); len(m) == 2 {
		hour := m[1]
		day := clock.Today()
		if strings.Contains(ask, "明天") {
			day = clock.Now().Add(24 * 3600 * 1e9).Format("2006-01-02")
		}
		if len(hour) == 1 {
			hour = "0" + hour
		}
		due = day + " " + hour + ":00:00"
	}
	if _, err := e.Store.CreateAppointment(Appointment{
		Who: 0, Scene: in.SceneKey, Text: ask, DueTS: due,
		Reason: "口头答应 (系统补记: 帧里没带 promise)",
	}); err != nil {
		log.Warn("convo:agency", "口头约定补记失败: %v", err)
		return
	}
	log.Info("convo:agency", "口头约定已补记: %q (due=%s)", truncateRunes(ask, 40), due)
}
