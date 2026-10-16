// Temporary demo-data seeder for local admin screenshots (LLBot 注册申请).
// Usage: go run ./cmd/seeddemo <dataDir>
package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"

	"github.com/tzhnahida/convo/internal/convo"
)

func main() {
	dir := os.Args[1]
	store, err := convo.OpenStore(dir, "convo-demo.db")
	if err != nil {
		panic(err)
	}
	defer store.Close()

	// 1. profile
	if err := store.SaveProfile(&convo.PersonaProfile{
		Name:        "小夜",
		Identity:    "night_owl",
		Template:    "bestie",
		Description: "住在你 QQ 好友列表里的二次元朋友, 夜猫子大学生, 会摸鱼会吐槽会记你的梗",
		Personality: "有点懒散但很黏你, 嘴硬心软, 说话短句碎句, 爱用语气词",
		Catchphrase: "诶嘿~",
		SentenceLen: "short",
		ToneWords:   "呢/啦/诶",
		Tags:        []string{"二次元", "夜猫", "好友"},
	}); err != nil {
		panic(fmt.Sprintf("profile: %v", err))
	}

	// 2. schedule (weekday: 学习两天 + 晚上摸鱼; weekend: 陪你)
	slots := func(day string, s ...convo.Slot) convo.DaySchedule {
		return convo.DaySchedule{Day: day, Slots: s}
	}
	wk := convo.WeekSchedule{Days: []convo.DaySchedule{
		slots("mon", convo.Slot{Start: "00:00", End: "07:30", State: convo.StateSleep}, convo.Slot{Start: "07:30", End: "12:00", State: convo.StateFocus}, convo.Slot{Start: "12:00", End: "14:00", State: convo.StateFree}, convo.Slot{Start: "14:00", End: "18:00", State: convo.StateFocus}, convo.Slot{Start: "18:00", End: "23:30", State: convo.StateFree}, convo.Slot{Start: "23:30", End: "24:00", State: convo.StateSleep}),
		slots("tue", convo.Slot{Start: "00:00", End: "07:30", State: convo.StateSleep}, convo.Slot{Start: "07:30", End: "12:00", State: convo.StateFocus}, convo.Slot{Start: "12:00", End: "14:00", State: convo.StateFree}, convo.Slot{Start: "14:00", End: "18:00", State: convo.StateFocus}, convo.Slot{Start: "18:00", End: "23:30", State: convo.StateFree}, convo.Slot{Start: "23:30", End: "24:00", State: convo.StateSleep}),
		slots("wed", convo.Slot{Start: "00:00", End: "07:30", State: convo.StateSleep}, convo.Slot{Start: "07:30", End: "12:00", State: convo.StateFocus}, convo.Slot{Start: "12:00", End: "14:00", State: convo.StateFree}, convo.Slot{Start: "14:00", End: "18:00", State: convo.StateFocus}, convo.Slot{Start: "18:00", End: "23:30", State: convo.StateFree}, convo.Slot{Start: "23:30", End: "24:00", State: convo.StateSleep}),
		slots("thu", convo.Slot{Start: "00:00", End: "07:30", State: convo.StateSleep}, convo.Slot{Start: "07:30", End: "12:00", State: convo.StateFocus}, convo.Slot{Start: "12:00", End: "14:00", State: convo.StateFree}, convo.Slot{Start: "14:00", End: "18:00", State: convo.StateFocus}, convo.Slot{Start: "18:00", End: "23:30", State: convo.StateFree}, convo.Slot{Start: "23:30", End: "24:00", State: convo.StateSleep}),
		slots("fri", convo.Slot{Start: "00:00", End: "07:30", State: convo.StateSleep}, convo.Slot{Start: "07:30", End: "12:00", State: convo.StateFocus}, convo.Slot{Start: "12:00", End: "14:00", State: convo.StateFree}, convo.Slot{Start: "14:00", End: "18:00", State: convo.StateFocus}, convo.Slot{Start: "18:00", End: "23:30", State: convo.StateFree}, convo.Slot{Start: "23:30", End: "24:00", State: convo.StateSleep}),
		slots("sat", convo.Slot{Start: "00:00", End: "07:30", State: convo.StateSleep}, convo.Slot{Start: "07:30", End: "24:00", State: convo.StateFree}),
		slots("sun", convo.Slot{Start: "00:00", End: "07:30", State: convo.StateSleep}, convo.Slot{Start: "07:30", End: "24:00", State: convo.StateFree}),
	}, SleepStart: "23:30", SleepEnd: "07:30"}
	if err := store.SaveSchedule(&wk); err != nil {
		panic(fmt.Sprintf("schedule: %v", err))
	}

	// 3. recent messages (微信式短句, 含梗)
	msgs := []struct {
		dir, payload string
	}{
		{"user", "在干嘛"},
		{"ai", "躺着刷视频呢 uᐛu 你呢"},
		{"user", "今天加班 麻了"},
		{"ai", "啊这……摸摸 加班人辛苦"},
		{"user", "我发你的猫图看到没"},
		{"ai", "看到了!! 猫猫祟祟 太可爱了"},
		{"user", "这周去爬山不"},
		{"ai", "好呀好呀 你说哪天 我把日程调开"},
		{"user", "笑死 我老板今天又画饼"},
		{"ai", "典 我直接一整个绷不住"},
		{"ai", "你怼回去没"},
		{"user", "没 怂了 下周再说"},
	}
	for _, m := range msgs {
		_, _ = store.InsertMessage(m.dir, "demo", "text", m.payload)
	}
	for _, name := range []string{"user_msgs", "rounds", "ai_opens", "ask_share_accept", "ask_share_reject"} {
		_, _ = store.CounterInc(name, "2026-09-08")
	}

	// 4. memories
	mems := []convo.MemoryDelta{
		{MemID: "user_shares_night_photos", K: "hobby", V: "用户喜欢拍夜景和星空, 会发照片分享", Label: "夜景摄影", Kind: "preference", Description: "发给小夜看过的照片主题", Confidence: 0.9},
		{MemID: "user_likes_mountain_hiking", K: "plan", V: "用户这周想去爬山, 说定了要一起", Label: "爬山计划", Kind: "plan", Description: "本周计划", Confidence: 0.9},
		{MemID: "user_work_overtime_heavy", K: "work", V: "用户最近加班多, 经常被画饼", Label: "加班狗", Kind: "fact", Description: "现状", Confidence: 0.85},
		{MemID: "user_has_cat", K: "pet", V: "用户养了只狸花猫, 叫年糕", Label: "猫叫年糕", Kind: "fact", Description: "宠物", Confidence: 0.9},
	}
	for _, m := range mems {
		if err := store.UpsertMemory(m, "active"); err != nil {
			panic(fmt.Sprintf("mem: %v", err))
		}
	}

	// 5. slangs (用户教的 + 它学的)
	slangs := []struct {
		phrase, meaning, example, src string
		conf                          float64
	}{
		{"麻了", "累到无语, 心态崩了", "今天加班麻了", "user", 0.9},
		{"典", "经典场面/经典言论, 用于吐槽", "典 我直接绷不住", "user", 0.9},
		{"画饼", "老板用空头承诺忽悠人", "老板又画饼了", "user", 0.85},
		{"猫猫祟祟", "形容猫偷偷摸摸很可爱", "猫猫祟祟探头", "user", 0.8},
		{"难绷", "绷不住了, 太搞笑了", "难绷 哈哈哈哈", "web", 0.6},
		{"偷吃token", "AI 偷偷用算力, 自嘲梗", "我不是我没有", "web", 0.6},
	}
	for _, s := range slangs {
		if _, err := store.UpsertSlang(convo.SlangDelta{Phrase: s.phrase, Meaning: s.meaning, Example: s.example, Confidence: s.conf}, s.src); err != nil {
			panic(fmt.Sprintf("slang: %v", err))
		}
	}

	// 6. stickers (纯色 PNG 演示图 + note)
	stickerDir := filepath.Join(dir, "convo", "stickers", "ja")
	if err := os.MkdirAll(stickerDir, 0755); err != nil {
		panic(err)
	}
	stk := []struct {
		name, note string
		affinity   float64
		c          color.RGBA
	}{
		{"cat_cutie.png", "猫猫祟祟探头", 0.8, color.RGBA{255, 214, 165, 255}},
		{"crash.png", "打工人破防", 0.65, color.RGBA{255, 183, 178, 255}},
		{"huh.png", "啊？", 0.5, color.RGBA{186, 220, 255, 255}},
		{"give.png", "给你点赞", 0.4, color.RGBA{200, 240, 200, 255}},
	}
	for _, s := range stk {
		img := image.NewRGBA(image.Rect(0, 0, 240, 240))
		for y := 0; y < 240; y++ {
			for x := 0; x < 240; x++ {
				img.Set(x, y, s.c)
			}
		}
		p := filepath.Join(stickerDir, s.name)
		f, err := os.Create(p)
		if err != nil {
			panic(err)
		}
		if err := png.Encode(f, img); err != nil {
			panic(err)
		}
		f.Close()
		id, err := store.InsertSticker("demo://"+s.name, p, "ja", "demo", s.affinity)
		if err != nil {
			panic(fmt.Sprintf("sticker: %v", err))
		}
		_ = store.SetStickerNote(id, s.note)
	}

	fmt.Println("seeded OK:", dir)
}
