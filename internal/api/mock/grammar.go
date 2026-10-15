package mock

func grammarPointsList(lang string) string {
	switch lang {
	case "ja":
		return `{"grammar_points":[{"id":1,"title":"て形","explanation":"动词连用形 + て，表示动作的连续、原因、手段等","example":"食べてから寝ます","language":"ja"},{"id":2,"title":"被动语态","explanation":"动词未然形 + れる/られる，表示被动","example":"先生に褒められました","language":"ja"}],"total":2}`
	case "zh":
		return `{"grammar_points":[{"id":3,"title":"把字句","explanation":"用介词\"把\"将宾语提前，强调对宾语的处置","example":"我把作业做完了","language":"zh"}],"total":1}`
	case "en":
		return `{"grammar_points":[{"id":4,"title":"Present Perfect","explanation":"have/has + past participle, for actions with present relevance","example":"I have finished my homework","language":"en"}],"total":1}`
	case "ko":
		return `{"grammar_points":[{"id":5,"title":"은/는 vs 이/가","explanation":"은/는은 주제, 이/가는 주어를 나타내는 조사","example":"나는 학생입니다. 친구가 왔어요.","language":"ko"}],"total":1}`
	default:
		return `{"grammar_points":[],"total":0}`
	}
}
