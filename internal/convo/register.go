package convo

import (
	"database/sql"
	"math"
	"strings"
)

// ─────────────────────────────────────────────────────────────
// §4.4.1b 语域漂移 (register drift)
//
// 语域 = 「她最近说话的样子」。它不是人格 (人格几十轮不变)，而是**近况**：
// 连着几天被怼，她会变短、变硬；连着几天被温柔对待，她会变软、变长。
//
// ⚠️ 为什么是标量向量而不是 LLM 自我报告：决策器不接 LLM (硬纪律)。
// 所以漂移只能从**可数的表面特征**里算出来 —— 句长/语气词/疑问/表情/感叹号。
// ⚠️ 为什么不是词袋：词袋对采样量极度敏感，10 条消息的窗口里任何一个内容词
// 都是噪声。这五个量是**说话方式**，不是**说话内容** —— 内容会换，方式不会。
// ─────────────────────────────────────────────────────────────

// RegisterMsg 一条待统计的消息。
type RegisterMsg struct {
	Text      string
	IsSticker bool
}

// RegisterVector 一份语域快照。
type RegisterVector struct {
	N        int     `json:"n"`
	AvgRunes float64 `json:"avg_runes"`
	Modal    float64 `json:"modal"`    // 语气词 / 条
	Question float64 `json:"question"` // 疑问句 / 条
	Sticker  float64 `json:"sticker"`  // 表情 / 条
	Bang     float64 `json:"bang"`     // 感叹号 / 条
}

// registerModals 语气词表。**它统计的是"软"的信号，不是内容。**
var registerModals = []string{"哈哈", "嘿嘿", "嗯", "呀", "啦", "嘛", "诶", "哦", "唉", "呜", "嗷", "诶嘿"}

// RegisterFromMsgs 从一段消息里算出语域向量。
// ⚠️ 空输入返回**零向量** (N=0)，而零向量必须在 DetectRegisterDrift 里被当成
// 「没数据」而不是「极端值」 —— 见那里的 N 下限判断。
func RegisterFromMsgs(msgs []RegisterMsg) RegisterVector {
	var v RegisterVector
	if len(msgs) == 0 {
		return v
	}
	var runes, modal, question, sticker, bang int
	for _, m := range msgs {
		if m.IsSticker {
			sticker++
			continue
		}
		t := strings.TrimSpace(m.Text)
		if t == "" {
			continue
		}
		runes += len([]rune(t))
		for _, wd := range registerModals {
			modal += strings.Count(t, wd)
		}
		if strings.ContainsAny(t, "?？") {
			question++
		}
		bang += strings.Count(t, "!") + strings.Count(t, "！")
	}
	v.N = len(msgs)
	d := float64(v.N)
	v.AvgRunes = float64(runes) / d
	v.Modal = float64(modal) / d
	v.Question = float64(question) / d
	v.Sticker = float64(sticker) / d
	v.Bang = float64(bang) / d
	return v
}

const (
	// RegisterMinSamples 判断漂移所需的最少样本。
	// ⚠️ 这是**闸门**，不是缩放：8 条以下的窗口里，句长均值能差 3 个字全是噪声。
	RegisterMinSamples = 8
	// RegisterDriftMin 漂移判定阈值 (加权相对变化)。
	RegisterDriftMin = 0.30

	RegisterDirSofter = "softer"
	RegisterDirHarder = "harder"
)

// RegisterDrift 漂移判定结果。
type RegisterDrift struct {
	Drifted   bool
	Dir       string
	Magnitude float64
}

// relDelta 相对变化。floor 防止分母趋零把噪声放大成"漂移"。
func relDelta(base, cur, floor float64) float64 {
	b := base
	if b < floor {
		b = floor
	}
	return (cur - b) / b
}

// DetectRegisterDrift 判定"最近"相对"基线"是否漂了，以及往哪边漂。
//
// 方向的定义（这是本模块唯一的解释权）：
//
//	变软 = 句更长 + 语气词更多 + 表情更多
//	变硬 = 句更短 + 语气词更少 + 表情更少
//
// 两个方向**同时**算分，取大的那个 —— 不允许"更软的分也为正"就报更软。
func DetectRegisterDrift(base, recent RegisterVector) RegisterDrift {
	var d RegisterDrift
	if base.N < RegisterMinSamples || recent.N < RegisterMinSamples {
		return d
	}
	soft := 0.45*relDelta(base.AvgRunes, recent.AvgRunes, 6) +
		0.35*relDelta(base.Modal, recent.Modal, 0.15) +
		0.20*relDelta(base.Sticker, recent.Sticker, 0.05)
	hard := 0.45*relDelta(recent.AvgRunes, base.AvgRunes, 6) +
		0.35*relDelta(recent.Modal, base.Modal, 0.15) +
		0.20*relDelta(recent.Sticker, base.Sticker, 0.05)
	d.Magnitude = math.Max(soft, hard)
	switch {
	case soft >= RegisterDriftMin && soft > hard:
		d.Drifted, d.Dir = true, RegisterDirSofter
	case hard >= RegisterDriftMin && hard > soft:
		d.Drifted, d.Dir = true, RegisterDirHarder
	}
	// ⚠️ 这里原来还填一格 `d.Detail`(base/recent/soft/hard 三行明细) ——
	// **它只写不读**, 已删。要说清漂在哪, `Dir` + `Magnitude` 就够了;
	// 存一串没有读者的明细, 只会让它看起来比实际更详细。
	return d
}

// RegisterDriftLine 漂移 → 注入段。
// ⚠️ 措辞里明确写了「不要解释原因 / 不要点破」 —— 语域是**近况的副产物**，
// 让它开口解释自己为什么变冷淡，等于把内部状态变成一个它自己都不知道的谜。
func RegisterDriftLine(d RegisterDrift) (string, bool) {
	if !d.Drifted {
		return "", false
	}
	switch d.Dir {
	case RegisterDirHarder:
		return "【说话方式】最近你说话明显变短、变硬了。表现：句子短、少用语气词、不爱发表情。" +
			"**不要解释原因**，也别道歉 —— 就照这个样子说。", true
	case RegisterDirSofter:
		return "【说话方式】最近你说话变软了：会长一点、语气词和表情都多了。" +
			"**不要点破**，照这个样子说就行。", true
	}
	return "", false
}

// DriftDisablesNightSocial §3.7 夜间多 AI 社会模拟的闸门。
//
// 理由：夜间社交模拟会**反过来污染**白天的语域（模拟出的对话进记忆）。
// 语域已经变硬时再叠一层模拟，等于让"变硬"自我强化 —— 这是本设计里
// 唯一会正反馈的路径，必须掐掉。
func DriftDisablesNightSocial(dir string) bool {
	return dir == RegisterDirHarder
}

// ── 持久化 ──────────────────────────────────────────────

// EnsureRegisterSchema 建表。
// ⚠️ 表名/列名是**接口**：写入方与读取方必须一致，测试里显式 DROP 过。
func EnsureRegisterSchema(db *sql.DB) error {
	_, err := db.Exec("CREATE TABLE IF NOT EXISTS convo_register (" +
		"owner TEXT NOT NULL, " +
		"day TEXT NOT NULL, " +
		"n INTEGER NOT NULL DEFAULT 0, " +
		"avg_runes REAL NOT NULL DEFAULT 0, " +
		"modal REAL NOT NULL DEFAULT 0, " +
		"question REAL NOT NULL DEFAULT 0, " +
		"sticker REAL NOT NULL DEFAULT 0, " +
		"bang REAL NOT NULL DEFAULT 0, " +
		"PRIMARY KEY (owner, day))")
	return err
}

// SaveRegisterVector 落一天的语域快照 (同一天重算则覆盖 —— 它是**读数**不是流水)。
func SaveRegisterVector(db *sql.DB, owner, day string, v RegisterVector) error {
	_, err := db.Exec("INSERT OR REPLACE INTO convo_register "+
		"(owner, day, n, avg_runes, modal, question, sticker, bang) VALUES (?,?,?,?,?,?,?,?)",
		owner, day, v.N, v.AvgRunes, v.Modal, v.Question, v.Sticker, v.Bang)
	return err
}

// LoadRegisterBaseline 取最近 days 天的**并集平均**作为基线。
// ⚠️ 没有数据时返回 N=0 的零向量 —— 调用方必须把它当"没基线"，
// 而不是"基线为零"。DetectRegisterDrift 的 N 下限已经把这条路堵死了。
func LoadRegisterBaseline(db *sql.DB, owner string, days int) (RegisterVector, error) {
	var v RegisterVector
	if days <= 0 {
		return v, nil
	}
	rows, err := db.Query("SELECT n, avg_runes, modal, question, sticker, bang FROM convo_register "+
		"WHERE owner = ? ORDER BY day DESC LIMIT ?", owner, days)
	if err != nil {
		return v, err
	}
	defer rows.Close()
	var totN int
	var sLen, sModal, sQ, sS, sB float64
	for rows.Next() {
		var n int
		var a, m, q, st, b float64
		if err := rows.Scan(&n, &a, &m, &q, &st, &b); err != nil {
			return v, err
		}
		if n <= 0 {
			continue
		}
		totN += n
		sLen += a * float64(n)
		sModal += m * float64(n)
		sQ += q * float64(n)
		sS += st * float64(n)
		sB += b * float64(n)
	}
	if err := rows.Err(); err != nil {
		return v, err
	}
	if totN == 0 {
		return v, nil
	}
	d := float64(totN)
	v = RegisterVector{N: totN, AvgRunes: sLen / d, Modal: sModal / d, Question: sQ / d, Sticker: sS / d, Bang: sB / d}
	return v, nil
}
