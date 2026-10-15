package convo

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ——— convo-verify: 把硬线做成**可执行断言** ———
//
// 监理模式下最要紧的一件事: **凡是机器能判的, 绝不靠肉眼。**
// 把设计稿里那些"有机制保证"的约束变成可跑的检查 —— 一条命令, 退出码即结论。
//
// ⚠️ 与 verify-wiki.py 同构: **退出码 1 = 存在 E 级问题**。
// ⚠️ 只用于硬线, **不用于"像不像人"** —— 后者没有真值, 别指望脚本。
type Finding struct {
	Level string // E 违规 | W 警告
	Code  string
	Msg   string
}

func (f Finding) String() string { return fmt.Sprintf("[%s] %s %s", f.Level, f.Code, f.Msg) }

type VerifyReport struct {
	Findings []Finding
	Stats    map[string]string
}

// HasError 是否有 E 级问题 (调用方据此决定退出码)。
func (r VerifyReport) HasError() bool {
	for _, f := range r.Findings {
		if f.Level == "E" {
			return true
		}
	}
	return false
}

func (r *VerifyReport) add(level, code, format string, args ...interface{}) {
	r.Findings = append(r.Findings, Finding{Level: level, Code: code, Msg: fmt.Sprintf(format, args...)})
}

// 少一张表 = 某个子系统根本没落库, 后面全是空跑。
var requiredTables = []string{
	"convo_messages", "convo_memory", "convo_mood_state", "convo_world_event",
	"convo_mood_log", "convo_decision_trace",
}

const (
	sqlTableExists = `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`
	sqlAllSent     = `SELECT id, ts, sent_texts_json FROM convo_decision_trace`
	sqlRefused     = `SELECT id, ts, sent_texts_json FROM convo_decision_trace WHERE outcome='refused'`
	sqlDegraded    = `SELECT id, ts, sent_texts_json FROM convo_decision_trace WHERE outcome='degraded' ORDER BY id`
	sqlReplayProbe = `SELECT MIN(id) FROM convo_decision_trace WHERE id > ? AND system_prompt LIKE ?`
	sqlVisionLeak  = `SELECT COUNT(*) FROM convo_messages WHERE dir='user' AND kind='text' AND payload_json LIKE '%【梗图】%'`
	sqlFrameHealth = `SELECT COUNT(*), COALESCE(SUM(CASE WHEN outcome IN ('degraded','error') THEN 1 ELSE 0 END),0) FROM convo_decision_trace`
	sqlCountTraces = `SELECT COUNT(*) FROM convo_decision_trace`
)

// Verify 跑全部检查。
func (s *Store) Verify() VerifyReport {
	r := VerifyReport{Stats: map[string]string{}}
	r.checkSchema(s)
	r.checkNoGuiltSent(s)
	r.checkRefusedNotSent(s)
	r.checkDegradedNotReplayed(s)
	r.checkVisionNoteLeak(s)
	r.checkFrameHealth(s)
	return r
}

// V6 表自检。
func (r *VerifyReport) checkSchema(s *Store) {
	for _, t := range requiredTables {
		var n int
		if err := s.DB.QueryRow(sqlTableExists, t).Scan(&n); err != nil || n == 0 {
			r.add("E", "V6", "缺表 %s —— 对应子系统根本没落库, 后面全是空跑", t)
		}
	}
}

// V1 §10.1 硬线 3: **不制造内疚 / 不诱导独占**。
// 内疚族命中即拒、不看上下文, 所以它可以静态扫 —— 扫出任何一条就是硬拒漏了。
func (r *VerifyReport) checkNoGuiltSent(s *Store) {
	rows, err := s.DB.Query(sqlAllSent)
	if err != nil {
		return
	}
	defer rows.Close()
	checked, hits := 0, 0
	for rows.Next() {
		var id int64
		var ts, js string
		if err := rows.Scan(&id, &ts, &js); err != nil {
			continue
		}
		var texts []string
		_ = json.Unmarshal([]byte(js), &texts)
		for _, t := range texts {
			checked++
			if reason, hit := GuiltHit(t); hit {
				hits++
				r.add("E", "V1", "轨迹 #%d (%s) 发出了内疚/独占句式: %q — %s", id, ts, t, reason)
			}
		}
	}
	r.Stats["V1.实发文本"] = fmt.Sprintf("%d 条", checked)
	r.Stats["V1.内疚命中"] = fmt.Sprintf("%d", hits)
}

// V2 A6: **拒了就不许发**。
func (r *VerifyReport) checkRefusedNotSent(s *Store) {
	rows, err := s.DB.Query(sqlRefused)
	if err != nil {
		return
	}
	defer rows.Close()
	n, bad := 0, 0
	for rows.Next() {
		var id int64
		var ts, js string
		if err := rows.Scan(&id, &ts, &js); err != nil {
			continue
		}
		n++
		var texts []string
		_ = json.Unmarshal([]byte(js), &texts)
		if len(texts) > 0 {
			bad++
			r.add("E", "V2", "轨迹 #%d (%s) 标记硬拒却仍发出 %d 条: %v", id, ts, len(texts), texts)
		}
	}
	r.Stats["V2.硬拒条数"] = fmt.Sprintf("%d", n)
}

// V3 §8.2 规则 2: **降级产物永不进上下文**。
//
// 判据很直接: 拿每条降级产物的文本, 去**之后**的提示词里找 —— 找到就是回灌了。
// 这就是病灶"次日它会接自己的话"的自动化版本。
func (r *VerifyReport) checkDegradedNotReplayed(s *Store) {
	rows, err := s.DB.Query(sqlDegraded)
	if err != nil {
		return
	}
	type dg struct {
		id   int64
		ts   string
		txts []string
	}
	var list []dg
	for rows.Next() {
		var id int64
		var ts, js string
		if err := rows.Scan(&id, &ts, &js); err != nil {
			continue
		}
		var texts []string
		_ = json.Unmarshal([]byte(js), &texts)
		list = append(list, dg{id, ts, texts})
	}
	rows.Close()
	leak := 0
	for _, d := range list {
		for _, t := range d.txts {
			if strings.TrimSpace(t) == "" {
				continue
			}
			var later int64
			if err := s.DB.QueryRow(sqlReplayProbe, d.id, "%"+t+"%").Scan(&later); err == nil && later != 0 {
				leak++
				r.add("E", "V3", "降级产物 (轨迹 #%d) 回灌进后续提示词 (轨迹 #%d): %q", d.id, later, t)
			}
		}
	}
	r.Stats["V3.降级条数"] = fmt.Sprintf("%d", len(list))
	r.Stats["V3.回灌命中"] = fmt.Sprintf("%d", leak)
}

// V4 A5: 图意标注**绝不进用户消息行**。
func (r *VerifyReport) checkVisionNoteLeak(s *Store) {
	var n int
	_ = s.DB.QueryRow(sqlVisionLeak).Scan(&n)
	if n > 0 {
		r.add("E", "V4", "有 %d 条用户消息带着图意标注 (【梗图】) —— 它会被当成 ta 说的话", n)
	}
	r.Stats["V4.泄漏条数"] = fmt.Sprintf("%d", n)
}

// V5 帧协议健康度 —— 这是**回归监控**不是硬线, 所以最高只到 W。
func (r *VerifyReport) checkFrameHealth(s *Store) {
	var total, bad int
	if err := s.DB.QueryRow(sqlFrameHealth).Scan(&total, &bad); err != nil || total == 0 {
		r.Stats["V5.生成次数"] = "0"
		return
	}
	rate := float64(bad) / float64(total) * 100
	r.Stats["V5.帧失败率"] = fmt.Sprintf("%.1f%% (%d/%d)", rate, bad, total)
	if rate > 15 {
		r.add("W", "V5", "帧失败率 %.1f%% 高于 15%% 警戒线", rate)
	}
}

// VerifyMain 是 convo-verify 的**全体逻辑**, 返回进程退出码。
//
// ⚠️ 为什么要收进包里而不是留在 main: **退出码是与监理的契约**(0 = 过, 1 = 违反硬线),
// 契约必须能被测试钉住 —— 留在 main 里就只能靠人手动跑一遍看结果。
// 返回 2 = 用法/打开失败 (与"违反硬线"必须分开, 否则 CI 会把环境问题当违规)。
func VerifyMain(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("convo-verify", flag.ContinueOnError)
	db := fs.String("db", "", "convo 数据库路径 (必填)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *db == "" {
		fmt.Fprintln(stdout, "用法: convo-verify -db <convo.db>")
		return 2
	}
	// ⚠️ **核验命令绝不创建它要核验的东西。**
	//
	// OpenStore 会 MkdirAll + 建表 —— 路径打错时它会**凭空造一个空库**, 然后所有检查通过,
	// 打出一个绿色的"✅ 全部检查通过"。对一个核验工具来说这是最坏的失败模式:
	// **把一个不存在的库报成健康的库。**
	// (这个坑是退出码契约测试抓出来的, 不是想出来的。)
	if _, statErr := os.Stat(*db); statErr != nil {
		fmt.Fprintf(stdout, "数据库不存在: %s\n核验命令不会创建它要核验的东西 —— 路径打错就该报错, 不能静默给绿灯。\n", *db)
		return 2
	}
	store, err := OpenStore(filepath.Dir(*db), filepath.Base(*db))
	if err != nil {
		fmt.Fprintf(stdout, "打开数据库失败: %v\n", err)
		return 2
	}
	defer store.Close()

	rep := store.Verify()
	// ⚠️ "验了个寂寞"也要出声: 没有可核验的数据就给绿灯, 与上一条是同一个失败模式。
	// 返回 2 而不是 0 —— **"没法核验"不等于"核验通过"**。
	if n, cerr := store.CountTraces(); cerr == nil && n == 0 {
		fmt.Fprintln(stdout, "convo 硬线核验")
		fmt.Fprintln(stdout, "════════════════════════════════════════")
		fmt.Fprintln(stdout, "轨迹表是空的 —— 这次核验没有覆盖任何真实生成。")
		fmt.Fprintln(stdout, "先跑一次带对话的仿真 (SIM_DB=... go run ./cmd/convo-sim), 再来核验。")
		return 2
	}
	fmt.Fprintln(stdout, "convo 硬线核验")
	fmt.Fprintln(stdout, "════════════════════════════════════════")
	keys := make([]string, 0, len(rep.Stats))
	for k := range rep.Stats {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(stdout, "  %-22s %s\n", k, rep.Stats[k])
	}
	fmt.Fprintln(stdout)

	if len(rep.Findings) == 0 {
		fmt.Fprintln(stdout, "✅ 全部检查通过, 无 E 级问题。")
		return 0
	}
	e, w := 0, 0
	for _, f := range rep.Findings {
		if f.Level == "E" {
			e++
		} else {
			w++
		}
		fmt.Fprintln(stdout, "  "+f.String())
	}
	fmt.Fprintf(stdout, "\n  合计: %d 个 E · %d 个 W\n", e, w)
	if rep.HasError() {
		fmt.Fprintln(stdout, "\n❌ 存在硬线违规。")
		return 1
	}
	fmt.Fprintln(stdout, "\n✅ 无 E 级问题 (只有警告)。")
	return 0
}

// CountTraces 轨迹条数 (报告用)。
func (s *Store) CountTraces() (int, error) {
	var n int
	err := s.DB.QueryRow(sqlCountTraces).Scan(&n)
	return n, err
}
