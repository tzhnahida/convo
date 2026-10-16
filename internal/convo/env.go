package convo

import (
	"fmt"
	"strings"
	"time"

	"github.com/tzhnahida/convo/internal/clock"
)

// EnvDay 是"它那边"的当天环境: 天气 / 温度 / 生活事件(地震、台风、花粉…)。
//
// 拟人的素材不是设定集, 而是"它有自己的城市与天气, 也有今天发生的事" ——
// 这一层同时喂三个地方: 注入段【你那边】(可自然提起)、mood 的外部成因、
// 以及生活流 micro-timeline(它的一天)。
type EnvDay struct {
	Date    string   `json:"date"`
	City    string   `json:"city"`
	Weather string   `json:"weather"`
	TempC   int      `json:"temp_c"`
	Events  []string `json:"events,omitempty"`

	// Moments 是**带时刻**的生活事件。
	//
	// ⚠️ 为什么要多这一层: `Events` 只能表达"今天发生了什么事", 它们全都在一天开始时
	// 一次性入账 —— 于是**世界在日内是静止的**, 主动开场只能贴着"最早可行时刻"发 (B6)。
	//
	// 带时刻不是为了好看, 是因为**真实数据源本来就带时间**: USGS 地震 API 返回发生时刻,
	// 天气预警有生效时间。把时刻丢掉, 是我们自己把信息扔了。
	Moments []EnvMoment `json:"moments,omitempty"`
}

// EnvMoment 一个带时刻的环境事件 (HH:MM, 当地时间)。
type EnvMoment struct {
	TS   string `json:"ts"`
	Text string `json:"text"`
}

// EnvSource 是环境数据接缝 (与 clock / Dice 同构)。
//
//	仿真: 季节模型(确定性, 见 cmd/convo-sim/simEnv)
//	正式: 接真实数据而不改引擎一行 ——
//	      历史/实时天气 Open-Meteo(免费无 key, 支持按经纬度取历史 archive)
//	      地震 USGS FDSN event API(免费无 key, 按时间窗+经纬度半径查询)
var EnvSource func(day time.Time) (EnvDay, bool)

// TodayEnv 返回当天环境 (无数据源 → ok=false, 注入段整段消失)。
func TodayEnv() (EnvDay, bool) {
	if EnvSource == nil {
		return EnvDay{}, false
	}
	return EnvSource(clock.Now())
}

// Line 渲染成注入用的一行 (空串 = 不注入)。
func (e EnvDay) Line() string {
	if e.Weather == "" && len(e.Events) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("【你那边】")
	if e.City != "" {
		b.WriteString(e.City + " ")
	}
	if e.Weather != "" {
		fmt.Fprintf(&b, "%s %d°C", e.Weather, e.TempC)
	}
	if len(e.Events) > 0 {
		if e.Weather != "" {
			b.WriteString("; ")
		}
		b.WriteString(strings.Join(e.Events, "; "))
	}
	b.WriteString("。(这是你自己的天气与生活, 聊到相关的可以自然提起 —— 但别每句都提天气。)\n")
	return b.String()
}
