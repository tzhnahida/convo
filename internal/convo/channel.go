package convo

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/tzhnahida/VocabLock/internal/log"
)

// ——— OneBot v11 通道 (NapCat) ———
//
// 拓扑: NapCat 与 convo 同在 Pi。事件走反向 WS (NapCat 出向连入我们的
// WSListen — 符合 §8.1b "一切网络只允许主动出站" 的网络纪律, localhost 无碍);
// 发送走 NapCat HTTP API (APIBase)。
//
// ⚠️ 待实测 (NapCat 实机验证清单):
//   - set_self_profile (签名联动) 为 go-cqhttp/NapCat 扩展接口, 支持度待验证;
//   - 图片发送用本地文件路径 (NapCat 与 convo 同机, 直接给绝对路径)。

type OneBotClient struct {
	apiBase     string
	accessToken string
	http        *http.Client
	selfQQ      int64  // 登录信息回填 (小夜自己的 QQ)
	selfName    string // 登录信息回填 (昵称; set_qq_profile 必带)
}

func NewOneBotClient(apiBase, accessToken string) *OneBotClient {
	return &OneBotClient{
		apiBase:     apiBase,
		accessToken: accessToken,
		http:        &http.Client{Timeout: 20 * time.Second},
	}
}

// ——— OneBot v11 数据结构 ———

type OneBotSegment struct {
	Type string            `json:"type"`
	Data map[string]string `json:"data"`
}

// OneBotSender v11 事件里的 sender 对象。
//
// ⚠️ **这张表原来把这整块丢掉了** —— 于是 §6.10.4 的称呼规则
// ("在 A 群叫外号, 在 B 群叫全名")**连原料都没有**。
//
// ⚠️ `card`(群名片)优先于 `nickname`(账号昵称):
// 群名片是**他自愿在这个群里用的名字**, 而账号昵称是全局的 ——
// §6.10.4 原话:「不确定时用"你"或**群名片**, 绝不猜外号」。
type OneBotSender struct {
	Nickname string `json:"nickname"`
	Card     string `json:"card"`
}

// DisplayName 该用哪个名字称呼他: 群名片优先, 退回昵称, 都没有 → 空串。
//
// ⚠️ 空串是"未提供", **不是**"他叫空名字" —— 下游据此决定要不要覆盖。
func (s OneBotSender) DisplayName() string {
	if c := strings.TrimSpace(s.Card); c != "" {
		return c
	}
	return strings.TrimSpace(s.Nickname)
}

type OneBotEvent struct {
	PostType    string          `json:"post_type"` // message | notice | meta_event
	MessageType string          `json:"message_type"`
	UserID      int64           `json:"user_id"`
	GroupID     int64           `json:"group_id"`
	Message     json.RawMessage `json:"message"`
	RawMessage  string          `json:"raw_message"`
	MessageID   int32           `json:"message_id"`
	Time        int64           `json:"time"`
	Sender      OneBotSender    `json:"sender"`
}

// AtUINs 取出消息里所有 at 的目标 uin (§6.10.2 ② 的输入)。
//
// OneBot v11 的 at 段形如 {"type":"at","data":{"qq":"12345"}}。
// ⚠️ "all" (@全体) **原样返回**, 不在这里过滤 —— 它算不算"叫我"是
// IsAddressedToMe 的裁定 (scene.go 已定: @全体不算叫我), 这里先替它决定
// 就等于把一条判据挪到了 IO 层, 那条裁定以后就改不动了。
func (e OneBotEvent) AtUINs() []string {
	segs, err := e.Segments()
	if err != nil {
		return nil
	}
	var out []string
	for _, s := range segs {
		if s.Type != "at" {
			continue
		}
		if qq := strings.TrimSpace(s.Data["qq"]); qq != "" {
			out = append(out, qq)
		}
	}
	return out
}

// Segments parses the event message into typed segments (text|image|face…).
//
// NapCat v4.15 三形态全兼容 (2026-09-07 msg-diag 定案):
//  1. 原生数组 — ⚠️ data 值可能含数字 (e.g. image 的 sub_type:1),
//     map[string]string 直接反序列化整数组会类型错误 → 容忍转换;
//  2. JSON 数组的字符串形态 — NapCat 对 array 序列化的产物, 先解字符串再按数组解析;
//  3. CQ 字符串 — 正则解析, 值 HTML 反转义 (&amp;→&)。
func (e OneBotEvent) Segments() ([]OneBotSegment, error) {
	if len(e.Message) == 0 {
		return nil, nil
	}
	// 1. 原生数组 (容忍数字值)
	if segs, err := parseSegmentArray(e.Message); err == nil {
		return segs, nil
	}
	// 2/3. 字符串形态: 先试 JSON 数组, 再试 CQ 正则
	var s string
	if err := json.Unmarshal(e.Message, &s); err == nil {
		if segs, err := parseSegmentArray([]byte(s)); err == nil {
			return segs, nil
		}
		return cqSegments(s), nil
	}
	// 兜底: 裸 CQ 文本 (非 JSON 包装)
	if bytes.Contains(e.Message, []byte("[CQ:")) {
		return cqSegments(string(e.Message)), nil
	}
	return nil, fmt.Errorf("message 无法识别: %s", truncateStr(string(e.Message), 120))
}

// parseSegmentArray 解析段数组; Data 值类型容忍 (string/number/bool → 统一 string)。
func parseSegmentArray(raw []byte) ([]OneBotSegment, error) {
	var arr []struct {
		Type string                 `json:"type"`
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil, err
	}
	segs := make([]OneBotSegment, 0, len(arr))
	for _, sr := range arr {
		data := make(map[string]string, len(sr.Data))
		for k, v := range sr.Data {
			data[k] = scalarString(v)
		}
		segs = append(segs, OneBotSegment{Type: sr.Type, Data: data})
	}
	return segs, nil
}

func scalarString(v interface{}) string {
	switch t := v.(type) {
	case string:
		return html.UnescapeString(t) // 数组形态同样带实体转义 (&amp;→&, 2026-09-07 实证)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	case nil:
		return ""
	default:
		return fmt.Sprint(t)
	}
}

// cqRe matches [CQ:type,k=v,...] codes.
var cqRe = regexp.MustCompile(`\[CQ:([a-zA-Z0-9_]+)((?:,[^\[\]]*)?)\]`)

// cqSegments 解析 CQ 字符串 → 段列表: CQ 码之间的原文为 text 段,
// CQ 码本体为对应类型段 (image 的 file/url 等值反转义)。
func cqSegments(s string) []OneBotSegment {
	var out []OneBotSegment
	pos := 0
	for _, m := range cqRe.FindAllStringSubmatchIndex(s, -1) {
		if m[0] > pos {
			out = append(out, OneBotSegment{Type: "text", Data: map[string]string{"text": s[pos:m[0]]}})
		}
		typ := s[m[2]:m[3]]
		data := map[string]string{}
		if params := strings.TrimPrefix(s[m[4]:m[5]], ","); params != "" {
			for _, kv := range strings.Split(params, ",") {
				parts := strings.SplitN(kv, "=", 2)
				if len(parts) == 2 {
					data[strings.TrimSpace(parts[0])] = html.UnescapeString(strings.TrimSpace(parts[1]))
				}
			}
		}
		out = append(out, OneBotSegment{Type: typ, Data: data})
		pos = m[1]
	}
	if pos < len(s) {
		out = append(out, OneBotSegment{Type: "text", Data: map[string]string{"text": s[pos:]}})
	}
	return out
}

// ImageFiles 提取事件中的图片。优先 url 字段 (可下载), 否则 file 字段 (NapCat 本地路径)。
// 2026-09-07 Pi 实证: CQ 字符串形态下 file= 是本地缓存名, url= 才是可下载地址。
// ImageRefs 图片段 + 桥接方给的 md5 (判重与照片/表情包分流要用)。
//
// ⚠️ **这是纯解析, 不碰磁盘。** 分层是刻意的:
//
//	· 解析 (这里)  —— 给出**候选路径列表**, 按可信度排序;
//	· 存在性 (IngestImage) —— Stat, 选第一个真的在的。
//
// 混在一起的代价是实测过的: 我第一版在这里 Stat, 于是
// 单测必须造真文件才能验"字段顺序", 而顺序恰恰是最该被独立钉住的东西。
//
// ⚠️ 顺序是 `path` → `url` → `file` (2026-10-14 生产事故的直接修复):
//
//	file = "64CC0D94…C.png"                          裸文件名, **不是路径**
//	path = "/home/tzh/.nimbus/media/64cc0d94….png"   导出后的**绝对路径**
//
// 原来只认 url/file, 于是拿一个裸文件名当路径去 open —— 相对 cwd 找不到,
// 降级成「[图片]」。**图片管线其实一直是通的, 只是字段没接对。**
func (e OneBotEvent) ImageRefs() []ImageRef {
	segs, err := e.Segments()
	if err != nil {
		return nil
	}
	var out []ImageRef
	for _, s := range segs {
		if s.Type != "image" {
			continue
		}
		// ⚠️ 候选里**也包含 md5 按约定拼出来的路径** —— 桥接方的导出时灵时不灵,
		// 而"文件其实在, 只是它没给对路径"是很常见的一种。
		cands := localCandidates(s.Data)
		if len(cands) == 0 && s.Data["path_exists"] != "0" {
			continue
		}
		p := ""
		if len(cands) > 0 {
			p = cands[0]
		}
		out = append(out, ImageRef{
			Path:       p,
			MD5:        s.Data["md5"],
			Candidates: cands,
			MessageID:  e.MessageID,
			// ⚠️ 桥接方明说"还没下" —— 这不是错误, 是**等我们叫它**。
			NeedsFetch: s.Data["path_exists"] == "0",
		})
	}
	return out
}

func (e OneBotEvent) ImageFiles() []string {
	var out []string
	for _, r := range e.ImageRefs() {
		out = append(out, r.Path)
	}
	return out
}

func (e OneBotEvent) PlainText() string {
	segs, err := e.Segments()
	if err != nil {
		return e.RawMessage // 解析失败才退回原文 (旧行为)
	}
	var b bytes.Buffer
	for _, s := range segs {
		if s.Type == "text" {
			b.WriteString(s.Data["text"])
		} else if s.Type == "face" {
			fmt.Fprintf(&b, "[QQ表情%s]", s.Data["id"])
		}
	}
	// 纯图/纯表情消息 → 空串 (绝不让 CQ 码泄漏进对话文本; 图片走 ImageFiles 通道)
	return b.String()
}

// ——— 发送 ———

type oneBotAPI struct {
	Action string          `json:"action"`
	Params json.RawMessage `json:"params"`
}

// unknownOutcomeError 传输层失败 —— **不知道对面到底执行了没有**。
//
// ⚠️ 它与"服务端明确回了错误"是**两回事**, 而这个区分是有后果的:
//
//	超时 / 连接被掐 → 请求可能已经送达, 动作可能已经执行
//	404 / 405 / 非 200 → 对面明确拒了, 动作没有执行
//
// 前者再打另一条轨 = **同一条消息发两遍**。对 `get_image` 无所谓 (幂等),
// 对 `send_group_msg` / `send_private_msg` 就是群里看到的"复读机"。
type unknownOutcomeError struct{ err error }

func (e unknownOutcomeError) Error() string { return e.err.Error() }

func (e unknownOutcomeError) Unwrap() error { return e.err }

// IsUnknownOutcome 这次失败是不是"结局未知"。
func IsUnknownOutcome(err error) bool {
	var u unknownOutcomeError
	return errors.As(err, &u)
}

// call 双轨调用 (2026-09-06 修复, 覆盖 NapCat API 形态差异):
//
//	轨 1: POST {base}/{action} + 裸 payload — OneBot v11 标准 (NapCat v4.15 实测定案:
//	      body 直接按 payload 解析, {action,params} 包装会 TypeError)
//	轨 2: POST {base} + action-in-body 包装 — go-cqhttp 风格 (NapCat 的 / 是状态端点, 兜底弱)
//
// 轨 1 成功即返回; 服务端**明确拒绝**时退轨 2;
// ⚠️ 传输层失败 (**结局未知**) 时**立刻收手, 不许退轨 2** —— 见 unknownOutcomeError。
//
// ⚠️ 这条"不退"的规则是有代价的: 万一哪天桥对不存在的子路径是**掐连接**而不是回 404,
// 兜底就永远走不到了。但那时的症状是"接口整体不通"——**看得见**;
// 而重发的症状是群里多出一条消息 —— 那要等人抱怨才发现。
// 宁可要看得见的坏, 不要看不见的坏。
func (c *OneBotClient) call(action string, params interface{}, out interface{}) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	wrapped, err := json.Marshal(oneBotAPI{Action: action, Params: raw})
	if err != nil {
		return err
	}
	var lastErr error
	// 轨1: 标准轨 + 裸 payload
	if err := c.callOnce(c.apiBase+"/"+action, raw, out); err == nil {
		return nil
	} else {
		lastErr = err
		log.Warn("convo:channel", "api try %s: %v", c.apiBase+"/"+action, err)
		if IsUnknownOutcome(err) {
			// ⚠️ **收手**。对面可能已经收到了, 再打一遍就是重发。
			// 这条日志是刻意做成 ERROR 级的: 它会伴随"消息可能发丢了",
			// 而"发丢了"和"发重了"在群里都不好, 但只有前者能被容忍。
			log.Error("convo:channel", "  ^ 结局未知 (%s) — 不试兜底轨, 避免重发", action)
			return lastErr
		}
	}
	// 轨2: body 轨 + 包装 (兜底)
	if err := c.callOnce(c.apiBase, wrapped, out); err == nil {
		return nil
	} else {
		lastErr = err
		log.Warn("convo:channel", "api try %s: %v", c.apiBase, err)
	}
	return lastErr
}

func (c *OneBotClient) callOnce(url string, body []byte, out interface{}) error {
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.accessToken)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// ⚠️ **一律算"结局未知"** —— 这里不细分超时/拒连/中断。
		//
		// 细分理论上能多救回一点兜底机会 (比如连接被拒其实肯定没送达),
		// 但那要按平台判 syscall 错误码, 判错一次的代价是**多发一条消息**。
		// 而形态差异 (双轨存在的理由) 从来不在传输层, 它表现为
		// 404/405/200-但-body-不是-OneBot —— 那些**照旧会走兜底**。
		// 所以这里放弃的那点兜底机会, 换取的是"绝不重发"。
		return unknownOutcomeError{fmt.Errorf("http do: %w", err)}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("status %d: %s", resp.StatusCode, truncateStr(string(raw), 300))
	}
	var env struct {
		Status  string          `json:"status"`
		RetCode int             `json:"retcode"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("parse: %w raw=%s", err, truncateStr(string(raw), 300))
	}
	if env.Status != "ok" && env.RetCode != 0 {
		return fmt.Errorf("status=%s retcode=%d msg=%q raw=%s",
			env.Status, env.RetCode, env.Message, truncateStr(string(raw), 300))
	}
	if out != nil && len(env.Data) > 0 {
		return json.Unmarshal(env.Data, out)
	}
	return nil
}

func (c *OneBotClient) SendPrivate(userID int64, segs []OneBotSegment) error {
	return c.call("send_private_msg", map[string]interface{}{
		"user_id": userID,
		"message": segs,
	}, nil)
}

func (c *OneBotClient) SendText(userID int64, text string) error {
	return c.SendPrivate(userID, []OneBotSegment{{Type: "text", Data: map[string]string{"text": text}}})
}

// SendImage sends a local image file (absolute path on the Pi).
func (c *OneBotClient) SendImage(userID int64, localPath string) error {
	return c.SendPrivate(userID, []OneBotSegment{{Type: "image", Data: map[string]string{"file": AbsMediaPath(localPath)}}})
}

// ——— 群发送 (§6.10) ———

// SendGroup 往群里发消息段。
//
// ⚠️ 与私聊是**两个不同的 API** (send_group_msg vs send_private_msg),
// 目标字段也不同 (group_id vs user_id)。把它写成"SendPrivate 换个参数"
// 会让一条本该进群的消息**私聊发给主人** —— 那是最难发现的一类事故:
// 群里什么都没发生, 而 owner 收到一条莫名其妙的私聊。
func (c *OneBotClient) SendGroup(groupID int64, segs []OneBotSegment) error {
	return c.call("send_group_msg", map[string]interface{}{
		"group_id": groupID,
		"message":  segs,
	}, nil)
}

// SendGroupText 群里发一条纯文本。
func (c *OneBotClient) SendGroupText(groupID int64, text string) error {
	return c.SendGroup(groupID, []OneBotSegment{{Type: "text", Data: map[string]string{"text": text}}})
}

// SendGroupImage 群里发一张本地图片。
func (c *OneBotClient) SendGroupImage(groupID int64, localPath string) error {
	// ⚠️ 绝对路径 —— 桥接方用自己的 cwd 解析相对路径 (见 AbsMediaPath)。
	return c.SendGroup(groupID, []OneBotSegment{{Type: "image", Data: map[string]string{"file": AbsMediaPath(localPath)}}})
}

// LoginInfo queries the bot's own QQ identity (回填 self_qq 配置).
func (c *OneBotClient) LoginInfo() (userID int64, nickname string, err error) {
	var d struct {
		UserID   int64  `json:"user_id"`
		Nickname string `json:"nickname"`
	}
	if err := c.call("get_login_info", map[string]interface{}{}, &d); err != nil {
		return 0, "", err
	}
	c.selfName = d.Nickname
	return d.UserID, d.Nickname, nil
}

// SetSignature 状态→签名联动 (拟人名片).
//
// ⚠️ 2026-09-06 Pi 实测定案: set_self_profile 在 NapCat 4.15/4.18 均报
// "不支持的Api"; 正确接口为 set_qq_profile — **必须带 nickname 字段**,
// 签名走 personal_note (实测 ok/result=0)。
func (c *OneBotClient) SetSignature(sig string) error {
	if c.selfName == "" {
		// 自举顺序防御: 签名先于登录信息时自动补; 拿不到名字 → 跳过本次 (避免 400)
		if _, name, err := c.LoginInfo(); err == nil {
			c.selfName = name
		}
	}
	if c.selfName == "" {
		log.Warn("convo:channel", "set_qq_profile skipped: nickname 未知 (get_login_info 未成功)")
		return fmt.Errorf("nickname unknown")
	}
	err := c.call("set_qq_profile", map[string]interface{}{
		"nickname":      c.selfName,
		"personal_note": sig,
	}, nil)
	if err != nil {
		return fmt.Errorf("set_qq_profile (须带 nickname/签名=personal_note): %w", err)
	}
	return nil
}

// SigByState 状态→QQ 签名的默认映射 (§8.3 五档)。
var SigByState = map[string]string{
	StateSleep: "睡了 zZ",
	StateFocus: "闭关中，勿扰",
	StateBusy:  "赶工中……",
	StateSlack: "摸鱼中",
	StateFree:  "在的",
}

// LinkSignatureAndName 在状态切换时联动签名 (信道弱化处理: 失败仅降级)。
func (c *OneBotClient) LinkSignature(state string) {
	sig, ok := SigByState[state]
	if !ok {
		sig = SigByState[StateFree]
	}
	if err := c.SetSignature(sig); err != nil {
		log.Warn("convo:channel", "signature link degraded (%s): %v", state, err)
	}
}
