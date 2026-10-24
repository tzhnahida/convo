package convo

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/tzhnahida/convo/internal/ai"
	"github.com/tzhnahida/convo/internal/log"
)

// ─────────────────────────────────────────────────────────────
// 表情包是**行为**, 不是装饰
//
// 文献依据 (屈济荣/李异平《作为"图像行为"的表情包》, 编辑之友 2018(10)):
//   表情包属于 Bredekamp 意义上的「图像行为」(Bildakt) ——
//   **图像是一种类似于言语活动的行为替代**, 不是面部表情的视觉再现。
//   它是「图文双构」的多模态符号: 图与字**互相锚定** (罗兰·巴特);
//   而它的意指依赖**互文性** —— 不懂那个梗就读不懂。
//
// 彭兰: 表情包是「用于表达**情感、情绪和态度**的图片」。
//
// ⚠️ 所以 `[表情]` 这个标记是**错的**: 它把一个**行为**压成了一个**类型标签**。
//   「一张表情包」和「一个嘲讽」是两回事, 而群里发生的是后者。
//
// ⚠️ 而"重复度高就不看图"也是错的 —— 重复只说明它是个贴纸,
//   不说明它**这次**想说什么。正确做法是: **看一次, 打标签, 之后复用标签**。
//   标签是**缓存**, 不是省略。
// ─────────────────────────────────────────────────────────────

// ImageLabel 一张图的标签。照片与表情包共用一套结构。
type ImageLabel struct {
	Kind      string `json:"kind"`      // sticker | photo
	Act       string `json:"act"`       // 这个表情在**做什么**
	Emotion   string `json:"emotion"`   // 基础情绪
	Intensity string `json:"intensity"` // 轻 | 中 | 重
	Text      string `json:"text"`      // 图上的字 (图文双构: 字是锚)
	Meaning   string `json:"meaning"`   // ≤15 字, 它想传达什么
}

// imageActs 表情包在群里**做的事**。
//
// ⚠️ 这张表是**行为**不是情绪 —— 文献明确: 表情包是"言语活动的行为替代"。
// 所以「阴阳怪气」「敷衍」这类必须单列: 它们不是情绪, 是**交际动作**。
var imageActs = []string{
	"附和", "反驳", "嘲讽", "阴阳怪气", "敷衍", "示好", "自嘲",
	"惊讶", "无语", "催促", "安慰", "庆祝", "求助", "陈述",
}

// imageEmotions 基础情绪 (Ekman 六类 + 中性)。
var imageEmotions = []string{"喜", "怒", "哀", "惧", "惊", "厌", "中性"}

// isOneOf 白名单校验 —— 模型爱自由发挥, 而自由发挥出来的标签聚合不起来。
func isOneOf(v string, allow []string) bool {
	v = strings.TrimSpace(v)
	for _, a := range allow {
		if v == a {
			return true
		}
	}
	return false
}

// LabelPrompt 给视觉模型的标注提示词。
func LabelPrompt() string {
	return "看这张图, 输出单行 JSON:\n" +
		"{\"kind\":\"sticker|photo\",\"act\":\"<见下>\",\"emotion\":\"<见下>\"," +
		"\"intensity\":\"轻|中|重\",\"text\":\"图上的字(没有就空)\",\"meaning\":\"不超过15字\"}\n\n" +
		"kind: sticker = 表情包/梗图/贴纸; photo = 真实照片/截图。\n" +
		"act **只能**从这些里选一个: " + strings.Join(imageActs, "/") + "\n" +
		"  —— 它指的是**这张图在对话里做了什么**, 不是图里画了什么。\n" +
		"emotion **只能**从这些里选一个: " + strings.Join(imageEmotions, "/") + "\n" +
		"  —— 如果这张图主要是「阴阳怪气/敷衍」这类**动作**, emotion 填最接近的那个, 别硬编。\n" +
		"text: 图上的文字**照抄**, 它是解读方向 (图文双构, 字是锚)。没有就空串。\n" +
		"meaning: 用一句话说它想传达什么, 不要复述画面。"
}

// ParseImageLabel 解析标注结果。**校验不过就返回 false** —— 半个标签比没有更糟。
func ParseImageLabel(raw string) (ImageLabel, bool) {
	b, err := extractJSON(raw)
	if err != nil {
		return ImageLabel{}, false
	}
	var l ImageLabel
	if err := json.Unmarshal(b, &l); err != nil {
		return ImageLabel{}, false
	}
	l.Kind = strings.TrimSpace(strings.ToLower(l.Kind))
	if l.Kind != "sticker" && l.Kind != "photo" {
		return ImageLabel{}, false
	}
	// ⚠️ 表情包的 act 必须落在白名单里 —— 那是聚合的关键。
	// 照片没有 act 可言, 不校验。
	if l.Kind == "sticker" && !isOneOf(l.Act, imageActs) {
		return ImageLabel{}, false
	}
	// ⚠️ 情绪/强度填不准**不是致命伤** —— 补中性, 不整条丢。
	// 一条"知道它在嘲讽但情绪没归类"的标签, 比什么都没有强得多。
	if !isOneOf(l.Emotion, imageEmotions) {
		l.Emotion = "中性"
	}
	if !isOneOf(l.Intensity, []string{"轻", "中", "重"}) {
		l.Intensity = "中"
	}
	return l, true
}

// Render 标签在上下文里长什么样。
//
// ⚠️ **首次与重复用两种写法** (2026-10-14 用户定的口径):
//
//	第一次:  [图: 忍不住多咬一口, 撒娇式道歉]
//	之后:    [图: 这张图出现过多次, 内容往上找](照片)
//
// 为什么: 群里同一张贴纸会发几十次。每次都把完整描述铺一遍,
// 上下文会被同一份内容撑爆 —— 而模型看到的是一堆一模一样的行,
// 那既浪费 token 又让它以为"发生了很多次"。
//
// ⚠️ 引用式**必须带上短标**(kind/act), 否则它没法把这次和第一次对上。
func (l ImageLabel) Render() string { return l.RenderSeen(1) }

// RenderSeen 按"这是第几次见到这张图"渲染。
//
// ⚠️ 引用式的构造**只此一处** (imageRepeatMark), 因为
// 拼历史时的窗口内去重必须产出**逐字节相同**的串 ——
// 两处各拼一遍的话, 改了一处忘了另一处就再也对不上。
func (l ImageLabel) RenderSeen(seen int) string {
	if seen > 1 {
		// 短标要和首次那份**对得上**, 否则"往上找"找不到。
		return imageRepeatMark(l.shortTag())
	}
	full := l.renderFull()
	return "(" + l.shortTag() + ") " + full
}

// shortTag 一个能对上的短标 —— 首次那份也带它。
func (l ImageLabel) shortTag() string {
	if l.Kind == "photo" {
		return "照片"
	}
	if a := strings.TrimSpace(l.Act); a != "" {
		return "表情 — " + a
	}
	return "表情"
}

// renderFull 首次出现时的完整渲染。
func (l ImageLabel) renderFull() string {
	m := strings.TrimSpace(l.Meaning)
	if l.Kind == "photo" {
		if m == "" {
			return ImagePhotoMark
		}
		return "[图: " + m + "]"
	}
	var parts []string
	if a := strings.TrimSpace(l.Act); a != "" {
		parts = append(parts, a)
	}
	if e := strings.TrimSpace(l.Emotion); e != "" && e != "中性" {
		parts = append(parts, e)
	}
	if len(parts) == 0 {
		parts = append(parts, "表情")
	}
	hd := "[图: 表情 — " + strings.Join(parts, " ")
	if t := strings.TrimSpace(l.Text); t != "" {
		hd += " 「" + truncateStr(t, 12) + "」"
	}
	// ⚠️ 2026-10-24: **表情包不再渲染 meaning** —— 用户实测: 随手发的表情包
	// 被它当话读 (追问「这什么意思」、按图里的意思接话), 而表情包大多是情绪调味。
	// meaning 仍存库 (统计/回顾用), 但不进上下文 —— 只留 act / 情绪 / 图上的字。
	return hd + "]"
}

// MarshalTag 存库用的序列化。
func (l ImageLabel) MarshalTag() string {
	b, err := json.Marshal(l)
	if err != nil {
		return ""
	}
	return string(b)
}

// ParseTag 从库里读回标签。空串/坏数据 → false。
func ParseTag(s string) (ImageLabel, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return ImageLabel{}, false
	}
	var l ImageLabel
	if err := json.Unmarshal([]byte(s), &l); err != nil {
		return ImageLabel{}, false
	}
	if l.Kind == "" {
		return ImageLabel{}, false
	}
	return l, true
}

// visionReadyPaths 喂给视觉 API 之前把**动图抽成多帧**。
//
// ⚠️ 这一版是被官方最佳实践**纠正**过的 (第一版做错了)。
//
// 来源: karminski《DeepSeek V4 Flash Vision 视频输入最佳实践》(2026-08-22):
//
//	「虽然模型支持 gif 输入, 但是**它只识别 gif 的第一帧**(写代码验证了)。
//	  抽帧的最佳实践: 抽成 JPEG/PNG, **按时间顺序作为多个 image_url 传入**,
//	  并在 prompt 里写明采样率和时间轴。」
//
// ⚠️ 我第一版是「首帧+末帧**横向拼成一张图**」—— 错的:
//
//	· 模型把拼图当成了**左右对比梗图** (实测它就是这么读的);
//	· 拼起来把分辨率摊薄一半;
//	· **"左右"这个空间关系盖过了"先后"这个时间关系**。
//	多帧必须**分开传**: 时间轴由**顺序**表达, 不由像素布局表达。
//
// 返回 (图片路径列表, 是不是动图)。单图时列表长度为 1。
func visionReadyPaths(path string) ([]string, bool) {
	if frames, ok := ai.ExtractFrames(path, ai.MaxAnimFrames); ok {
		return frames, true
	}
	return []string{path}, false
}

// animatedNote 告诉模型这是**按时间顺序**抽的帧。
//
// ⚠️ 措辞关键在"第 1 帧…第 N 帧" —— 与 API 里图片的排列顺序一一对应。
// 说"左边/右边"就把时间关系降级成了空间关系, 而模型会顺着空间关系去理解。
const animatedNote = "\n\n⚠️ 上面是**同一张动图按时间顺序抽的帧** (第 1 帧 → 最后一帧), " +
	"不是多张不同的图, 也不是左右对比梗图。请描述它**从什么变成什么**。"

// visionChat 统一入口: 多帧走多图, 单图走原路。
//
// ⚠️ 用类型断言而不是往 VisionChatter 接口里加方法 —— 那会让所有测试替身一起挂掉,
// 而"多图"是**可选能力**: 不支持时退回单图, 行为仍然正确。
func visionChat(ctx context.Context, vc any, model, system, prompt string, paths []string) (string, error) {
	if len(paths) == 1 {
		c, ok := vc.(VisionChatter)
		if !ok {
			return "", errNoVision
		}
		return c.ChatVision(ctx, model, system, prompt, paths[0])
	}
	mc, ok := vc.(interface {
		ChatVisionMulti(context.Context, string, string, string, []string) (string, error)
	})
	if !ok {
		// 退化成只看第一帧 —— 比什么都不看强, 但**日志要说清**。
		log.Warn("convo:vision", "视觉口不支持多图, 动图只送第 1 帧")
		c, ok2 := vc.(VisionChatter)
		if !ok2 {
			return "", errNoVision
		}
		return c.ChatVision(ctx, model, system, prompt, paths[0])
	}
	// ⚠️ 帧是中间产物, 用完即删 —— 否则临时目录会越堆越多。
	defer ai.CleanupFrames(paths)
	return mc.ChatVisionMulti(ctx, model, system, prompt, paths)
}

var errNoVision = errStr("视觉口不可用")

// LabelImage 看图打标签。失败返回 false —— 调用方退回"只记一笔"。
//
// ⚠️ 它与 describeImage 是**两件事**:
//
//	describeImage 问"你看到了什么"(为照片写的);
//	LabelImage   问"这张图在**做什么**"(为表情包写的)。
//	混用会让表情包被描述成画面内容, 而它在群里的作用是别的东西。
func (e *Engine) LabelImage(ctx context.Context, path string) (ImageLabel, bool) {
	if e.VisionClient == nil || e.VisionModel == "" {
		return ImageLabel{}, false
	}
	paths, animated := visionReadyPaths(path)
	prompt := LabelPrompt()
	if animated {
		prompt += animatedNote
	}
	// ⚠️ 传下去的是**抽出来的帧** —— 别把原来的动图又发一遍 (它只认第一帧)。
	out, err := visionChat(ctx, e.VisionClient, e.VisionModel,
		"你是表情包与图片的标注器。只输出 JSON, 不要任何其他文本。", prompt, paths)
	if err != nil {
		return ImageLabel{}, false
	}
	return ParseImageLabel(out)
}
