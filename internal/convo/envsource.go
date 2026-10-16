package convo

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/tzhnahida/convo/internal/log"
)

// ─────────────────────────────────────────────────────────────
// §3.3 环境数据接缝的**生产实现**。
//
// ⚠️ **这个文件是补一个从没接过的线** (2026-10-14 查出来的)。
//
// `EnvSource` 此前只在 `cmd/convo-sim`(仿真) 和测试里被赋值 ——
// **生产进程一次都没赋过**。后果是一整条链全部静默不存在:
//
//   TodayEnv() → ok=false
//     ├─ 【你那边】天气/城市段 → 永远不注入
//     ├─ ingestWorld() 第一行就 return → **Pi 上 convo_world_event 至今 0 条**
//     └─ mood 的外部成因 → 从来没有
//
// 而用户的判断是「角色从来不说自己的生活」。
// **它不是不愿意说, 是它没有生活可说** —— 没有天气、没有城市、没有事。
// 提示词再怎么改也治不了这个: 素材压根不存在。
//
// 数据源都是**免费无 key** 的, 与设计稿 §3.3 写的一致:
//   天气 Open-Meteo  https://api.open-meteo.com/v1/forecast
//   地震 USGS FDSN   https://earthquake.usgs.gov/fdsnws/event/1/query
// ─────────────────────────────────────────────────────────────

// EnvConfig 接真实数据要的坐标与名字。
type EnvConfig struct {
	// City 显示用的城市名 —— **可以是虚构的** (角色世界观里的城市)。
	City string `yaml:"city"`
	// Lat / Lon 真实坐标 —— 天气/地震按它取。
	Lat float64 `yaml:"lat"`
	Lon float64 `yaml:"lon"`
	// TZ 天气时刻所用的时区名, 例 "Asia/Shanghai"。空 → 本地时区。
	TZ string `yaml:"tz"`
	// Enabled 显式开关。零值 false = 不接 (与"配了但坐标是 0,0"分得开)。
	Enabled bool `yaml:"enabled"`
}

// EnvHTTPTimeout 单次取数的上限。
//
// ⚠️ **必须有**: 这个函数跑在 tick 里 (每分钟一次)。没有超时的话,
// 一次网络卡死会把整个心跳拖住 —— 而心跳停了, 它就不说话了。
const EnvHTTPTimeout = 6 * time.Second

// ——— WMO 天气码 → 中文 ———
//
// ⚠️ 词表必须覆盖 `EnvEvents` 认得的那些字 (晴/阴/雨/雪/雾/多云) ——
// 那些字同时决定**心情底色**。这里译错一个字, 那边就静默不产生事件。
func WMOWeather(code int) string {
	switch code {
	case 0:
		return "晴"
	case 1:
		return "晴间多云"
	case 2:
		return "多云"
	case 3:
		return "阴"
	case 45, 48:
		return "雾"
	case 51, 53, 55, 56, 57:
		return "毛毛雨"
	case 61, 63, 65, 66, 67:
		return "雨"
	case 71, 73, 75, 77:
		return "雪"
	case 80, 81, 82:
		return "阵雨"
	case 85, 86:
		return "阵雪"
	case 95:
		return "雷阵雨"
	case 96, 99:
		return "雷暴冰雹"
	}
	// ⚠️ 认不出的码**不猜** —— 返回空串, 由调用方决定怎么处置。
	// 编一个"晴"出来会污染心情底色, 那是看不见的错。
	return ""
}

// ——— Open-Meteo ———

type openMeteoResp struct {
	Current struct {
		Temperature float64 `json:"temperature_2m"`
		// ⚠️ **指针, 不是 int** —— WMO 码 `0` 是**晴天**, 而字段缺失时
		// Go 也给出 0。用 int 的话「晴」和「没给」是同一个值,
		// 于是一个空响应 `{}` 会被读成"今天晴天" —— **凭空断言天气**。
		// (这条是被测试逼出来的: `{}` 一度解析成功。)
		Code *int `json:"weather_code"`
	} `json:"current"`
	Daily struct {
		Code []int `json:"weather_code"`
	} `json:"daily"`
	Hourly struct {
		Time []string `json:"time"`
		Code []int    `json:"weather_code"`
	} `json:"hourly"`
}

// ParseOpenMeteo 把 Open-Meteo 的响应解析成 EnvDay。**纯函数, 可测。**
//
// ⚠️ 解析不出天气 → ok=false (不编一个假天气)。
// 但**时刻表 (Moments) 允许为空** —— 那是"今天没有转折", 是合法状态。
func ParseOpenMeteo(raw []byte, city string) (EnvDay, bool) {
	var r openMeteoResp
	if err := json.Unmarshal(raw, &r); err != nil {
		return EnvDay{}, false
	}
	// 当前码优先; 缺了退当天预报码; 都没有 → **不猜, 直接失败**。
	code := -1
	if r.Current.Code != nil {
		code = *r.Current.Code
	} else if len(r.Daily.Code) > 0 {
		code = r.Daily.Code[0]
	}
	w := WMOWeather(code)
	if w == "" {
		return EnvDay{}, false
	}
	return EnvDay{
		City:    strings.TrimSpace(city),
		Weather: w,
		TempC:   int(roundHalf(r.Current.Temperature)),
		Moments: hourlyTurns(r.Hourly.Time, r.Hourly.Code),
	}, true
}

func roundHalf(f float64) float64 {
	if f < 0 {
		return -float64(int(-f + 0.5))
	}
	return float64(int(f + 0.5))
}

// hourlyTurns 从逐时天气里挑出**转折点** ("14:00 转小雨")。
//
// ⚠️ **不是每个小时都报** —— 24 条"还是晴"是噪声, 只会让它念叨天气。
// 只在天气码**变了**的时候记一条, 上限 4 条。
func hourlyTurns(times []string, codes []int) []EnvMoment {
	if len(times) == 0 || len(codes) == 0 {
		return nil
	}
	n := len(times)
	if len(codes) < n {
		n = len(codes)
	}
	var out []EnvMoment
	prev := codes[0]
	for i := 1; i < n && len(out) < 4; i++ {
		if codes[i] == prev {
			continue
		}
		prev = codes[i]
		w := WMOWeather(codes[i])
		if w == "" {
			continue
		}
		hhmm := times[i]
		if len(hhmm) >= 16 { // "2026-10-14T14:00" → "14:00"
			hhmm = hhmm[11:16]
		}
		out = append(out, EnvMoment{TS: hhmm, Text: "转" + w})
	}
	return out
}

// ——— USGS 地震 ———

type usgsResp struct {
	Features []struct {
		Properties struct {
			Mag   float64 `json:"mag"`
			Place string  `json:"place"`
		} `json:"properties"`
	} `json:"features"`
}

// ParseUSGSQuakes 把 USGS 的 GeoJSON 解析成几行中文。**纯函数, 可测。**
//
// ⚠️ 震级门槛 (调用方传的 URL 里带 minmagnitude) 已经把"每天都有小震"滤掉了。
// 这里再兜一道: 没震级、没地点的条目直接丢 —— 宁可少报, 不可报半条。
func ParseUSGSQuakes(raw []byte) []string {
	var r usgsResp
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil
	}
	var out []string
	for _, f := range r.Features {
		place := strings.TrimSpace(f.Properties.Place)
		if place == "" || f.Properties.Mag <= 0 {
			continue
		}
		out = append(out, fmt.Sprintf("附近 %.1f 级地震 (%s)", f.Properties.Mag, place))
		if len(out) >= 3 {
			break
		}
	}
	return out
}

// ——— HTTP 取数 ———

func envGet(c *http.Client, rawURL string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "convo/1.0 (+personal assistant)")
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	buf := make([]byte, 0, 8192)
	tmp := make([]byte, 4096)
	for {
		n, rerr := resp.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if rerr != nil {
			break
		}
		if len(buf) > 1<<20 { // 1MB 封顶 —— 对方回一整页 HTML 也不至于吃满内存
			break
		}
	}
	return buf, nil
}

// HTTPEnvSource 生产取数器: 天气 (Open-Meteo) + 地震 (USGS)。
//
// ⚠️ **天气是主, 地震是附** —— 天气取不到 → ok=false (整段消失);
// 地震取不到 → 只是今天没有地震这一项, 天气照old常给。
// 两者的失败方向不同, 因为它们的"缺省含义"不同: 前者是"不知道", 后者是"没有"。
// 把"不知道"当成"没有"就是编。
func HTTPEnvSource(cfg EnvConfig) func(time.Time) (EnvDay, bool) {
	client := &http.Client{Timeout: EnvHTTPTimeout}
	loc := time.Local
	if s := strings.TrimSpace(cfg.TZ); s != "" {
		if l, err := time.LoadLocation(s); err == nil {
			loc = l
		}
	}
	return func(day time.Time) (EnvDay, bool) {
		d := day.In(loc).Format("2006-01-02")
		q := url.Values{}
		q.Set("latitude", fmt.Sprintf("%.4f", cfg.Lat))
		q.Set("longitude", fmt.Sprintf("%.4f", cfg.Lon))
		q.Set("current", "temperature_2m,weather_code")
		q.Set("hourly", "weather_code")
		q.Set("timezone", loc.String())
		q.Set("forecast_days", "1")
		raw, err := envGet(client, "https://api.open-meteo.com/v1/forecast?"+q.Encode())
		if err != nil {
			log.Warn("convo:env", "天气取不到 (%v) — 今天不注入【你那边】, 不编", err)
			return EnvDay{}, false
		}
		env, ok := ParseOpenMeteo(raw, cfg.City)
		if !ok {
			log.Warn("convo:env", "天气解析不出 (码不在词表里) — 不编")
			return EnvDay{}, false
		}
		env.Date = d
		// 地震: 近 24h、坐标 300km 内、4.0 级以上。
		//
		// ⚠️ 门槛定在 4.0 是有意的: 更低的话每天都有几条, 而"每天都有地震"
		// 不叫新闻, 叫噪声 —— 它会把这条当成日常背景, 反而更假。
		qq := url.Values{}
		qq.Set("format", "geojson")
		qq.Set("latitude", fmt.Sprintf("%.4f", cfg.Lat))
		qq.Set("longitude", fmt.Sprintf("%.4f", cfg.Lon))
		qq.Set("maxradiuskm", "300")
		qq.Set("minmagnitude", "4.0")
		qq.Set("starttime", day.AddDate(0, 0, -1).Format("2006-01-02"))
		if qraw, qerr := envGet(client, "https://earthquake.usgs.gov/fdsnws/event/1/query?"+qq.Encode()); qerr == nil {
			env.Events = ParseUSGSQuakes(qraw)
		}
		return env, true
	}
}

// ——— 缓存: tick 里**绝不许阻塞** ———

// CachedEnvSource 把取数器包成"同步返回缓存 + 后台刷新"。
//
// ⚠️ **这是硬要求, 不是优化**: TodayEnv 跑在每分钟一次的 tick 里,
// 直接在里面发 HTTP → 一次网络抖动就把心跳卡住 6 秒,
// 而心跳卡住的表象是"它突然不说话了" —— 极难归因。
//
// 语义:
//
//	今天已取到 + 未过期  → 直接给缓存
//	过期 / 换天          → **触发后台刷新**, 本次仍给今天已有的那份
//	今天还没有           → ok=false (零值 = 未提供, 不是"今天没天气")
func CachedEnvSource(fetch func(time.Time) (EnvDay, bool), ttl time.Duration) func(time.Time) (EnvDay, bool) {
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	var mu sync.Mutex
	var cur EnvDay
	var curDay string
	var have bool
	var gotAt time.Time
	inflight := false

	return func(now time.Time) (EnvDay, bool) {
		today := now.Format("2006-01-02")
		mu.Lock()
		if curDay != today {
			// 换天了: 昨天的那份**不能再给** (它是昨天的天气)。
			curDay, have, gotAt = today, false, time.Time{}
			cur = EnvDay{}
		}
		fresh := have && now.Sub(gotAt) < ttl
		out, ok := cur, have
		needRefresh := !fresh && !inflight
		if needRefresh {
			inflight = true
		}
		mu.Unlock()

		if needRefresh {
			go func() {
				defer func() {
					if r := recover(); r != nil {
						log.Error("convo:env", "环境取数 panic: %v", r)
					}
					mu.Lock()
					inflight = false
					mu.Unlock()
				}()
				env, ok := fetch(now)
				mu.Lock()
				defer mu.Unlock()
				if !ok {
					// ⚠️ 失败**不清空**今天已经拿到的那份 —— 天气不会因为
					// 一次请求失败就变。但要**出声**, 否则"一直没数据"
					// 和"偶尔失败"在日志里长得一样。
					log.Warn("convo:env", "环境刷新失败 (保留今天已有的 %v)", have)
					return
				}
				cur, have, gotAt = env, true, now
				log.Info("convo:env", "环境就绪: %s %s %d°C, 事件 %d 条",
					env.City, env.Weather, env.TempC, len(env.Moments))
			}()
		}
		return out, ok
	}
}
