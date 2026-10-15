package convo

import (
	"strings"
	"testing"
)

// ⚠️ **最大的坑: 群里大半消息不是人话, 是指令。**
// 实测语料: `st 棍母` `st otto` `music 人生啊能不能放过我这一次` —— 那是别的 bot 的命令。
func TestIsNaturalUtterance(t *testing.T) {
	yes := []string{"我去", "唉", "应该是吧?", "这东西能吃吗", "p站真是啥都有啊", "nb", "真不敢开r18"}
	for _, s := range yes {
		if !IsNaturalUtterance(s) {
			t.Fatalf("这是人话, 不该剔掉: %q", s)
		}
	}
	no := []string{
		"st 棍母", "st otto", "music 人生啊能不能放过我这一次", "点歌 起风了",
		"help", "#签到", "/status", "!!rank",
		"", " ", "?", "1", "12", "!!",
		strings.Repeat("字", 41),
	}
	for _, s := range no {
		if IsNaturalUtterance(s) {
			t.Fatalf("不该进语料: %q", s)
		}
	}
}

// ⚠️ 指令**不许**污染句长基线 —— 那正是"它写得比群里长"的隐藏原因之一。
func TestGroupVoiceFrom_FiltersCommands(t *testing.T) {
	msgs := []string{"我去", "唉", "应该是吧?", "st 一个非常非常非常长的指令参数反正二十多个字"}
	v := GroupVoiceFrom(msgs)
	if v.N != 3 {
		t.Fatalf("应当只算 3 条人话, got %d", v.N)
	}
	if v.AvgRunes > 6 {
		t.Fatalf("指令把均值拉高了: %.1f", v.AvgRunes)
	}
}

// ⚠️ **零值 = 未提供 ≠ 0 字**: 没有样本时不许给出"平均 0 字"的结论。
func TestGroupVoiceFrom_EmptyIsZero(t *testing.T) {
	v := GroupVoiceFrom(nil)
	if v.N != 0 || v.AvgRunes != 0 {
		t.Fatalf("空语料应当是零值: %+v", v)
	}
	if _, ok := GroupVoiceLine(v); ok {
		t.Fatal("没有样本时不该注入任何东西")
	}
}

// ⚠️ **样本不足的门槛是硬约束** —— 3 条算出"平均 6 个字"会让它憋成复读机。
func TestGroupVoiceLine_MinSamplesIsHard(t *testing.T) {
	var few []string
	for i := 0; i < GroupVoiceMinSamples-1; i++ {
		few = append(few, "我去")
	}
	if _, ok := GroupVoiceLine(GroupVoiceFrom(few)); ok {
		t.Fatalf("%d 条不该出结论 (门槛 %d)", len(few), GroupVoiceMinSamples)
	}
	var enough []string
	for i := 0; i < GroupVoiceMinSamples; i++ {
		enough = append(enough, "我去")
	}
	line, ok := GroupVoiceLine(GroupVoiceFrom(enough))
	if !ok {
		t.Fatal("到门槛了就该出结论")
	}
	if !strings.Contains(line, "别写得比他们卷") {
		t.Fatalf("缺少关键那句约束: %q", line)
	}
}

// 端到端: 库里取真实消息 → 统计 → 注入。
func TestGroupVoice_EndToEnd(t *testing.T) {
	e, _ := groupSpeakEngine(t, &sitCaptureChatter{})
	sk := "group:99"
	for _, m := range []string{"我去", "唉", "这东西能吃吗", "应该是吧?", "p站真是啥都有啊",
		"nb", "我入", "真不敢开r18", "扫", "我入", "唉", "我去", "st 长长的指令参数占位"} {
		if _, err := e.Store.InsertMessageFull("user", "qq", "text", m, MsgMeta{SceneKey: sk}); err != nil {
			t.Fatal(err)
		}
	}
	// ⚠️ 它自己说的**不该**进语料 —— 学自己说话是自我强化。
	if _, err := e.Store.InsertMessageFull("ai", "qq", "text", "我这两天在整理星空的笔记噢", MsgMeta{SceneKey: sk}); err != nil {
		t.Fatal(err)
	}
	v := e.groupVoice(sk, 200)
	if v.N != 12 {
		t.Fatalf("应当只算 12 条真人话, got %d", v.N)
	}
	line := voiceNoteOrEmpty(v)
	if line == "" {
		t.Fatal("够样本了却没生成注入文本")
	}
	if !strings.Contains(line, "这个群怎么说话") {
		t.Fatalf("注入文本不对: %q", line)
	}
}

// ⚠️ **中位数比均值更该用** —— 实测均值 7.7 而被少数长消息拉高, 中位只有 5。
// 拿均值当尺子会让它写得比群里长一半。
func TestGroupVoice_UsesMedianNotMean(t *testing.T) {
	// ⚠️ 夹具必须够门槛 (GroupVoiceMinSamples) —— 少一条就不出结论, 测试会红得莫名其妙。
	msgs := []string{"唉", "行", "能", "扫", "嗨", "扣"}
	msgs = append(msgs, msgs[:6]...)                   // 12 条短句
	msgs = append(msgs, strings.Repeat("很长很长的一句话", 5)) // 1 条长的把均值拉高
	v := GroupVoiceFrom(msgs)
	if v.Median >= v.AvgRunes {
		t.Fatalf("中位应当低于被拉高的均值: median=%.1f avg=%.1f", v.Median, v.AvgRunes)
	}
	if v.ShortRate < 0.5 {
		t.Fatalf("6/7 是 5 字以内, 短句率不该只有 %.2f", v.ShortRate)
	}
	line, ok := GroupVoiceLine(v)
	if !ok {
		t.Fatal("该出结论")
	}
	if !strings.Contains(line, "单字") {
		t.Fatalf("短句率这么高却没告诉它单字是常态: %q", line)
	}
	if !strings.Contains(line, "中位") {
		t.Fatalf("应当报中位数而不是均值: %q", line)
	}
}
