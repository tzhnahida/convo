package convo

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ⚠️ 词表必须覆盖 EnvEvents 认得的那些字 —— 那些字决定**心情底色**。
//
// 这一条是防"译错一个字, 那边静默不产生事件"的: 实测踩过,
// 词表只覆盖晴/阴/雨/雪, 天气池里的"多云"与"闷热"全军覆没,
// 表现是 30 天里 12 天心情完全不受天气影响, 而报告看不出来。
func TestWMOWeather_CoversWhatEnvEventsUnderstands(t *testing.T) {
	// EnvEvents 的 switch 认得: 暴雨/雷, 雪, 雨, 阴, 多云, 闷热, 雾, 晴
	for _, want := range []string{"晴", "多云", "阴", "雨", "雪", "雾", "雷"} {
		found := false
		for _, code := range []int{0, 1, 2, 3, 45, 51, 61, 71, 80, 95, 96} {
			if strings.Contains(WMOWeather(code), want) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("词表里没有任何一个码译出 %q —— 那种天气的日子心情不会受一点影响", want)
		}
	}
}

// ⚠️ 认不出的码**不许猜**。编一个"晴"出来会污染心情底色, 那是看不见的错。
func TestWMOWeather_UnknownIsEmptyNotGuessed(t *testing.T) {
	for _, code := range []int{-1, 7, 42, 999} {
		if got := WMOWeather(code); got != "" {
			t.Fatalf("码 %d 不认识却译成了 %q —— 宁可空着", code, got)
		}
	}
}

// 解析不出天气 → ok=false。**不编一个假天气。**
func TestParseOpenMeteo_GarbageIsNotOk(t *testing.T) {
	for _, raw := range []string{"", "not json", "{}", `{"current":{"weather_code":999}}`} {
		if _, ok := ParseOpenMeteo([]byte(raw), "测试城"); ok {
			t.Fatalf("这种输入不该解析成功: %q", raw)
		}
	}
}

func TestParseOpenMeteo_RealShape(t *testing.T) {
	raw := `{"current":{"temperature_2m":21.6,"weather_code":2},
		"hourly":{"time":["2026-10-14T09:00","2026-10-14T14:00","2026-10-14T18:00"],
		"weather_code":[2,61,61]}}`
	env, ok := ParseOpenMeteo([]byte(raw), "龙门")
	if !ok {
		t.Fatal("正常响应解析失败")
	}
	if env.Weather != "多云" {
		t.Fatalf("天气译错: %q", env.Weather)
	}
	if env.TempC != 22 {
		t.Fatalf("温度取整错: %d (21.6 应进到 22)", env.TempC)
	}
	if env.City != "龙门" {
		t.Fatalf("城市没带上: %q", env.City)
	}
	if len(env.Moments) != 1 || env.Moments[0].TS != "14:00" {
		t.Fatalf("转折点没挑对: %+v", env.Moments)
	}
	if !strings.Contains(env.Moments[0].Text, "雨") {
		t.Fatalf("转折文案不对: %q", env.Moments[0].Text)
	}
}

// ⚠️ **没有转折就不给 Moments** —— 24 条"还是晴"是噪声,
// 只会让它念叨天气 (而 EnvDay.Line 明确写了"别每句都提天气")。
func TestHourlyTurns_FlatWeatherYieldsNothing(t *testing.T) {
	times := []string{"2026-10-14T09:00", "2026-10-14T10:00", "2026-10-14T11:00"}
	if got := hourlyTurns(times, []int{0, 0, 0}); len(got) != 0 {
		t.Fatalf("全天一个天气却给出转折点: %+v", got)
	}
}

// 长度不齐不许越界 —— 接口少给一段是常事。
func TestHourlyTurns_MismatchedLengthsDoNotPanic(t *testing.T) {
	if got := hourlyTurns([]string{"a"}, []int{0, 1, 2}); got != nil {
		t.Fatalf("时刻比码少时该返回空: %+v", got)
	}
	if got := hourlyTurns(nil, []int{0, 1}); got != nil {
		t.Fatalf("没有时刻该返回空: %+v", got)
	}
}

func TestParseUSGSQuakes_RealShape(t *testing.T) {
	raw := `{"features":[{"properties":{"mag":4.7,"place":"12km N of Somewhere"}},
		{"properties":{"mag":0,"place":"没震级"}},{"properties":{"mag":5.1,"place":""}}]}`
	got := ParseUSGSQuakes([]byte(raw))
	if len(got) != 1 {
		t.Fatalf("该只留 1 条, 实际 %d: %v", len(got), got)
	}
	if !strings.Contains(got[0], "4.7") || !strings.Contains(got[0], "Somewhere") {
		t.Fatalf("文案不对: %q", got[0])
	}
}

// 没震 → 空列表, **不是错误**。"没有"和"不知道"是两件事。
func TestParseUSGSQuakes_EmptyIsFine(t *testing.T) {
	if got := ParseUSGSQuakes([]byte(`{"features":[]}`)); len(got) != 0 {
		t.Fatalf("没有地震却给出 %v", got)
	}
	if got := ParseUSGSQuakes([]byte("坏了")); got != nil {
		t.Fatalf("坏响应该返回 nil: %v", got)
	}
}

// ——— 缓存: tick 里绝不许阻塞 ———

// ⚠️ 换天之后**不许再给昨天的天气** —— 那是"下着昨天的雨过今天"。
func TestCachedEnvSource_NewDayDoesNotReuseYesterday(t *testing.T) {
	d1 := time.Date(2026, 10, 14, 9, 0, 0, 0, time.Local)
	d2 := time.Date(2026, 10, 15, 9, 0, 0, 0, time.Local)
	src := CachedEnvSource(func(time.Time) (EnvDay, bool) {
		return EnvDay{Weather: "晴", TempC: 20}, true
	}, time.Hour)
	src(d1)
	if _, ok := src(d2); ok {
		t.Fatal("换天第一次就给数据了 —— 那份是昨天的")
	}
}

// 取数失败 → ok=false, 而且**不许编**。
func TestCachedEnvSource_FailureYieldsNotOk(t *testing.T) {
	src := CachedEnvSource(func(time.Time) (EnvDay, bool) { return EnvDay{}, false }, time.Minute)
	if env, ok := src(time.Now()); ok {
		t.Fatalf("取数失败却给了数据: %+v", env)
	}
}

// 第二次 (缓存已就绪) 必须给同一份, 且不再调 fetcher。
func TestCachedEnvSource_ServesCachedWithoutRefetch(t *testing.T) {
	var calls int32
	src := CachedEnvSource(func(time.Time) (EnvDay, bool) {
		atomic.AddInt32(&calls, 1)
		return EnvDay{Weather: "阴", TempC: 15}, true
	}, time.Hour)
	now := time.Now()
	src(now) // 触发后台刷新
	deadline := time.Now().Add(2 * time.Second)
	for {
		env, ok := src(now)
		if ok {
			if env.Weather != "阴" {
				t.Fatalf("缓存内容不对: %+v", env)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("等不到后台刷新结果")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for i := 0; i < 10; i++ {
		src(now)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("缓存期内又取了 %d 次数 —— 每分钟 tick 会把对方打爆", n)
	}
}
