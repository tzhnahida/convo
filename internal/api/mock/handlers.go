package mock

import (
	"net/http"
	"sync"
)

// ——— Quiz mock handlers ———

// Stateful mock session: the real backend eventually serves a "completed"
// response once the daily target is met. The old mock NEVER completed, so
// the completion screen and session summary were unreachable during
// Flutter UI development.
const mockSessionQuestions = 2

var (
	mockNextMu   sync.Mutex
	mockNextSeen = map[string]int{}
)

func HandleQuizSessionStart(w http.ResponseWriter, r *http.Request) {
	mockNextMu.Lock()
	mockNextSeen = map[string]int{} // fresh session
	mockNextMu.Unlock()
	writeJSON(w, `{"new_words":3,"new_grammar":1,"review_words":15,"review_grammar":2}`)
}

func HandleQuizProgress(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, `{"reviewed_today":8,"remaining":12,"daily_target":20}`)
}

func HandleQuizNext(w http.ResponseWriter, r *http.Request, defaultLang string) {
	lang := r.URL.Query().Get("language")
	if lang == "" {
		lang = defaultLang
	}
	mockNextMu.Lock()
	mockNextSeen[lang]++
	done := mockNextSeen[lang] >= mockSessionQuestions
	mockNextMu.Unlock()
	if done {
		writeJSON(w, `{"completed":true,"message":"Daily target met","reviewed":8,"daily_target":20,"daily_target_met":true}`)
		return
	}
	writeJSON(w, quizQuestion(lang))
}

func HandleQuizSubmit(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, `{"correct":true,"score":1.0,"feedback":"Correct!","source":"exact","next_review_at":"2026-05-11T00:00:00Z"}`)
}

func HandleQuizTodayStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, `{"reviewed":8,"correct":7,"accuracy":0.875}`)
}

// HandleQuizSummarize returns a plausible session summary + plan so the
// Flutter summary view is exercisable in mock mode (the real endpoint
// requires AI).
func HandleQuizSummarize(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, `{
  "summary": {
    "today_summary": {
      "overall": "今天完成了 8 题，正确率 87%，表现稳定。",
      "strengths": "词汇选择题全部答对",
      "weaknesses": "发音题需要更多跟读练习"
    },
    "suggestions": ["明天保持每日 20 分钟学习", "重点复习 drink 相关词汇"]
  },
  "plan": {
    "id": 1,
    "direction": "general",
    "direction_confidence": 0.8,
    "plan_json": "",
    "created_at": "2026-09-02T00:00:00Z"
  }
}`)
}

func HandleQuizComplete(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, `{"completed":true,"daily_target_met":false}`)
}

// ——— Words mock handlers ———

func HandleWordsList(w http.ResponseWriter, r *http.Request, defaultLang string) {
	lang := r.URL.Query().Get("language")
	if lang == "" {
		lang = defaultLang
	}
	writeJSON(w, wordsList(lang))
}

func HandleWordsGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	writeJSON(w, wordByID(id))
}

func HandleWordsCreate(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, `{"id":999,"word":"newword","reading":"newreading","meaning":"new meaning","pos":"noun"}`)
}

func HandleWordsUpdate(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, `{"id":1,"word":"updated","reading":"updated","meaning":"updated","pos":"verb"}`)
}

func HandleWordsDelete(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, `{"deleted":true}`)
}

func HandleWordsReset(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, `{"reset":true,"word_id":1}`)
}

func HandleWordsExport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", "attachment; filename=vocablock-export.json")
	w.Write([]byte(`[{"word":"hello","reading":"heˈloʊ","meaning":"a greeting","pos":"interjection"}]`))
}

func HandleWordsImport(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, `{"imported":5,"skipped":0}`)
}

func HandleWordsExpand(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, `{"verified":[],"pending_review":[]}`)
}

func HandleWordsPendingAccept(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, `{"id":1,"status":"active"}`)
}

func HandleWordsPendingReject(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, `{"id":1,"status":"rejected"}`)
}

// Grammar mock

func HandleGrammarPointsList(w http.ResponseWriter, r *http.Request, defaultLang string) {
	lang := r.URL.Query().Get("language")
	if lang == "" {
		lang = defaultLang
	}
	writeJSON(w, grammarPointsList(lang))
}

// AI mock

func HandleAIChat(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, `{"reply":"Hello! I'm your language learning assistant. How can I help you today?","model":"mock"}`)
}

func HandleAIProfile(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, `{"base_url":"https://api.deepseek.com","model":"deepseek-v4-pro","configured":true}`)
}

func HandleAIProfileUpdate(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, `{"saved":true,"language":"zh","direction":"general"}`)
}

// ——— TTS mock handlers ———

func HandleTTSWord(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, `{"url":"/data/audio/mock.mp3","format":"mp3","cached":true,"status":"ok"}`)
}

func HandleTTSText(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, `{"url":"/data/audio/mock.mp3","format":"mp3","cached":false,"status":"ok"}`)
}

// ——— Pronounce mock handler ———

func HandlePronounceAssess(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, `{"status":"ok","score":0.87,"phonemes":[{"phoneme":"t","score":0.92},{"phoneme":"a","score":0.85},{"phoneme":"be","score":0.78},{"phoneme":"ru","score":0.91}]}`)
}

// ——— Stats mock handlers ———

func HandleStatsHeatmap(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, `{"2026-05-01":15,"2026-05-02":20,"2026-05-03":0,"2026-05-04":18,"2026-05-05":22,"2026-05-06":12,"2026-05-07":25}`)
}

func HandleStatsStreak(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, `{"streak":5}`)
}

func HandleStatsDailyReport(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, `{"date":"today","words_reviewed":20,"words_correct":18,"accuracy":0.9}`)
}

// ——— System mock handlers ———

func HandleSystemLock(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, `{"locked":true}`)
}

func HandleSystemUnlock(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, `{"locked":false}`)
}

func HandleSystemPassiveQ(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, `{"word":"hello","reading":"heˈloʊ","meaning":"a greeting"}`)
}

func HandleSystemPassiveSubmit(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, `{"accepted":true}`)
}

func HandleSystemQuit(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, `{"quitting":true}`)
}

// ——— Config mock handlers ———

func HandleConfigPassword(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, `{"set":true}`)
}

func writeJSON(w http.ResponseWriter, json string) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(json))
}

// ——— Multi-language mock data ———

func wordByID(id string) string {
	m := map[string]string{
		"1":   `{"id":1,"word":"hello","reading":"heˈloʊ","meaning":"a greeting","pos":"interjection","language":"en","level":"A1","detail":{"syllable_count":"2","syntax":"interjection, used as greeting","collocation":"say hello","register":"neutral","etymology":"Old English \"hǽl\", related to \"hale\"","example_sentence":"Hello, how are you today?"},"easiness_factor":2.5,"interval":6,"repetitions":2,"total_attempts":5,"total_correct":4}`,
		"2":   `{"id":2,"word":"world","reading":"wɜːld","meaning":"the earth","pos":"noun","language":"en","level":"A1","detail":{"syllable_count":"1","syntax":"countable noun","collocation":"around the world","register":"neutral","etymology":"Old English \"weorold\"","example_sentence":"People all over the world speak English."},"easiness_factor":2.3,"interval":3,"repetitions":1,"total_attempts":3,"total_correct":2}`,
		"101": `{"id":101,"word":"食べる","reading":"たべる","meaning":"to eat","pos":"verb","language":"ja","level":"N5","detail":{"syllable_count":"3","morphology":"下一段活用動詞、連用形「食べ」","syntax":"他動詞、ヲ格を取る","collocation":"ご飯を食べる","register":"普通","etymology":"古典日本語「食ぶ」より","example_sentence":"毎日朝ごはんを食べます。","pitch_accent":"中高","conjugation_class":"一段","case_government":"ヲ","orthographic_variants":"喰べる","keigo_level":"召し上がる（尊敬語）"},"easiness_factor":2.5,"interval":6,"repetitions":2,"total_attempts":5,"total_correct":4}`,
		"201": `{"id":201,"word":"学习","reading":"xuéxí","meaning":"to study","pos":"verb","language":"zh","level":"HSK1","detail":{"syllable_count":"2","morphology":"离合词，可拆分使用","syntax":"及物动词，可带宾语","collocation":"努力学习","register":"普通","example_sentence":"我每天学习中文两个小时。"},"easiness_factor":2.5,"interval":6,"repetitions":2,"total_attempts":5,"total_correct":4}`,
		"301": `{"id":301,"word":"공부하다","reading":"gongbuhada","meaning":"to study","pos":"verb","language":"ko","level":"TOPIK1","detail":{"syllable_count":"4","morphology":"하다動詞、規則活用","syntax":"他動詞、을/를を取る","collocation":"열심히 공부하다","register":"普通","example_sentence":"매일 한국어를 공부합니다.","conjugation_class":"하다"},"easiness_factor":2.5,"interval":6,"repetitions":2,"total_attempts":5,"total_correct":4}`,
	}
	if v, ok := m[id]; ok {
		return v
	}
	return `{"id":1,"word":"hello","reading":"heˈloʊ","meaning":"a greeting","pos":"interjection","language":"en","level":"A1","detail":{},"easiness_factor":2.5,"interval":6,"repetitions":2,"total_attempts":5,"total_correct":4}`
}

func wordsList(lang string) string {
	switch lang {
	case "en":
		return `{"words":[
			{"id":1,"word":"hello","reading":"heˈloʊ","meaning":"a greeting","pos":"interjection","language":"en","level":"A1","detail":{"syllable_count":"2","syntax":"interjection, used as greeting","collocation":"say hello","register":"neutral","etymology":"Old English \"hǽl\"","example_sentence":"Hello, how are you today?"},"easiness_factor":2.5,"interval":6,"repetitions":2,"total_attempts":5,"total_correct":4},
			{"id":2,"word":"world","reading":"wɜːld","meaning":"the earth","pos":"noun","language":"en","level":"A1","detail":{"syllable_count":"1","syntax":"countable noun","collocation":"around the world","register":"neutral","example_sentence":"People all over the world speak English."},"easiness_factor":2.3,"interval":3,"repetitions":1,"total_attempts":3,"total_correct":2},
			{"id":3,"word":"abandon","reading":"əˈbændən","meaning":"to leave behind","pos":"verb","language":"en","level":"B2","detail":{"syllable_count":"3","morphology":"regular verb","syntax":"transitive verb","collocation":"abandon hope","register":"formal","etymology":"Old French \"abandoner\"","example_sentence":"They had to abandon the sinking ship."},"easiness_factor":2.5,"interval":0,"repetitions":0,"total_attempts":0,"total_correct":0},
			{"id":4,"word":"benefit","reading":"ˈbɛnɪfɪt","meaning":"an advantage","pos":"noun","language":"en","level":"B1","detail":{"syllable_count":"3","syntax":"countable noun","collocation":"health benefit","register":"neutral","etymology":"Latin \"benefactum\"","example_sentence":"Exercise has many health benefits."},"easiness_factor":2.5,"interval":0,"repetitions":0,"total_attempts":0,"total_correct":0}
		],"total":4}`
	case "ja":
		return `{"words":[
			{"id":101,"word":"食べる","reading":"たべる","meaning":"to eat","pos":"verb","language":"ja","level":"N5","detail":{"syllable_count":"3","morphology":"下一段活用動詞、連用形「食べ」","syntax":"他動詞、ヲ格を取る","collocation":"ご飯を食べる","register":"普通","etymology":"古典日本語「食ぶ」より","example_sentence":"毎日朝ごはんを食べます。","pitch_accent":"中高","conjugation_class":"一段","case_government":"ヲ","orthographic_variants":"喰べる","keigo_level":"召し上がる（尊敬語）"},"easiness_factor":2.5,"interval":6,"repetitions":2,"total_attempts":5,"total_correct":4},
			{"id":102,"word":"飲む","reading":"のむ","meaning":"to drink","pos":"verb","language":"ja","level":"N5","detail":{"syllable_count":"2","morphology":"五段活用動詞、連用形「飲み」","syntax":"他動詞、ヲ格を取る","collocation":"水を飲む","register":"普通","example_sentence":"毎朝コーヒーを飲みます。","pitch_accent":"頭高","conjugation_class":"五段","case_government":"ヲ"},"easiness_factor":2.3,"interval":3,"repetitions":1,"total_attempts":3,"total_correct":2},
			{"id":103,"word":"歩く","reading":"あるく","meaning":"to walk","pos":"verb","language":"ja","level":"N5","detail":{"syllable_count":"3","morphology":"五段活用動詞、連用形「歩き」","syntax":"自動詞、ガ格を取る","collocation":"道を歩く","register":"普通","etymology":"古典日本語「歩く」より","example_sentence":"毎日公園を歩きます。","pitch_accent":"中高","conjugation_class":"五段","case_government":"ガ"},"easiness_factor":2.5,"interval":0,"repetitions":0,"total_attempts":0,"total_correct":0},
			{"id":104,"word":"学校","reading":"がっこう","meaning":"school","pos":"noun","language":"ja","level":"N5","detail":{"syllable_count":"4","syntax":"場所名詞、ニ/デ格を取る","collocation":"学校に行く","register":"普通","etymology":"「学」+「校」の漢語","example_sentence":"子供たちは学校へ行きました。","pitch_accent":"頭高"},"easiness_factor":2.5,"interval":0,"repetitions":0,"total_attempts":0,"total_correct":0}
		],"total":4}`
	case "zh":
		return `{"words":[
			{"id":201,"word":"学习","reading":"xuéxí","meaning":"to study","pos":"verb","language":"zh","level":"HSK1","detail":{"syllable_count":"2","morphology":"离合词，可拆分使用","syntax":"及物动词，可带宾语","collocation":"努力学习","register":"普通","example_sentence":"我每天学习中文两个小时。"},"easiness_factor":2.5,"interval":6,"repetitions":2,"total_attempts":5,"total_correct":4},
			{"id":202,"word":"工作","reading":"gōngzuò","meaning":"to work","pos":"verb","language":"zh","level":"HSK1","detail":{"syllable_count":"2","morphology":"兼类词（动/名）","syntax":"不及物动词，也可作名词","collocation":"认真工作","register":"普通","example_sentence":"他在一家大公司工作。"},"easiness_factor":2.3,"interval":3,"repetitions":1,"total_attempts":3,"total_correct":2},
			{"id":203,"word":"美丽","reading":"měilì","meaning":"beautiful","pos":"adj","language":"zh","level":"HSK3","detail":{"syllable_count":"2","syntax":"形容词，修饰名词","collocation":"美丽的风景","register":"书面/正式","example_sentence":"这里的风景非常美丽。"},"easiness_factor":2.5,"interval":0,"repetitions":0,"total_attempts":0,"total_correct":0},
			{"id":204,"word":"电脑","reading":"diànnǎo","meaning":"computer","pos":"noun","language":"zh","level":"HSK1","detail":{"syllable_count":"2","syntax":"名词，可作主语或宾语","collocation":"打开电脑","register":"普通","etymology":"「电子」+「大脑」","example_sentence":"我的电脑坏了，需要修理。"},"easiness_factor":2.5,"interval":0,"repetitions":0,"total_attempts":0,"total_correct":0}
		],"total":4}`
	case "ko":
		return `{"words":[
			{"id":301,"word":"공부하다","reading":"gongbuhada","meaning":"to study","pos":"verb","language":"ko","level":"TOPIK1","detail":{"syllable_count":"4","morphology":"하다動詞、規則活用","syntax":"他動詞、을/를を取る","collocation":"열심히 공부하다","register":"普通","example_sentence":"매일 한국어를 공부합니다.","conjugation_class":"하다"},"easiness_factor":2.5,"interval":6,"repetitions":2,"total_attempts":5,"total_correct":4},
			{"id":302,"word":"아름답다","reading":"areumdapda","meaning":"beautiful","pos":"adj","language":"ko","level":"TOPIK1","detail":{"syllable_count":"4","morphology":"ㅂ不規則形容詞","syntax":"連体形「아름다운」で名詞修飾","collocation":"아름다운 풍경","register":"普通","example_sentence":"오늘 하늘이 정말 아름답습니다.","conjugation_class":"ㅂ変格"},"easiness_factor":2.3,"interval":3,"repetitions":1,"total_attempts":3,"total_correct":2},
			{"id":303,"word":"학교","reading":"hakgyo","meaning":"school","pos":"noun","language":"ko","level":"TOPIK1","detail":{"syllable_count":"2","syntax":"場所名詞、에/에서を取る","collocation":"학교에 가다","register":"普通","etymology":"漢字語「學校」","example_sentence":"아이들이 학교에 갔습니다."},"easiness_factor":2.5,"interval":0,"repetitions":0,"total_attempts":0,"total_correct":0},
			{"id":304,"word":"사랑","reading":"sarang","meaning":"love","pos":"noun","language":"ko","level":"TOPIK1","detail":{"syllable_count":"2","syntax":"名詞、을/를を取る","collocation":"사랑을 고백하다","register":"普通","example_sentence":"사랑은 아름다운 감정입니다."},"easiness_factor":2.5,"interval":0,"repetitions":0,"total_attempts":0,"total_correct":0}
		],"total":4}`
	default:
		return `{"words":[],"total":0}`
	}
}

func quizQuestion(lang string) string {
	switch lang {
	case "en":
		return `{"id":"wc1","type":"choice","subject_type":"word","subject_id":1,"prompt":"What does 'hello' mean?","options":["a greeting","goodbye","thank you","sorry"]}`
	case "ja":
		return `{"id":"wc101","type":"choice","subject_type":"word","subject_id":101,"prompt":"「食べる」的意思是什么？","options":["吃","喝","睡","走"]}`
	case "zh":
		return `{"id":"wc201","type":"choice","subject_type":"word","subject_id":201,"prompt":"'学习' 的正确拼音是？","options":["xuéxí","xiūxi","gōngzuò","wánshuǎ"]}`
	case "ko":
		return `{"id":"wc301","type":"choice","subject_type":"word","subject_id":301,"prompt":"'공부하다' 的意思是什么？","options":["学习","睡觉","吃饭","跑步"]}`
	default:
		return `{"id":"wc1","type":"choice","subject_type":"word","subject_id":1,"prompt":"What does 'hello' mean?","options":["a greeting","goodbye","thank you","sorry"]}`
	}
}
