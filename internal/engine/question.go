package engine

import (
	"math/rand"
	"strings"
)

// QuestionType enumerates all supported question types.
type QuestionType string

const (
	TypeChoice       QuestionType = "choice"
	TypeFill         QuestionType = "fill"
	TypeSpelling     QuestionType = "spelling"
	TypePronounce    QuestionType = "pronounce"    // recall the pronunciation (type kana/pinyin)
	TypeMeaning      QuestionType = "meaning"      // write the translation (see word, type meaning)
	TypeListening    QuestionType = "listening"    // hear audio, answer
	TypeReadingComp  QuestionType = "reading_comp" // read passage, answer questions
	TypeGrammar      QuestionType = "grammar"
	TypeReadWord     QuestionType = "read_word"     // read a word aloud, scored by phoneme
	TypeReadSentence QuestionType = "read_sentence" // read a sentence aloud, scored by match
)

// promptTemplates maps language code → question type → prompt prefix.
// The "lang" key is the primary subtag of explanation_lang (e.g. "zh" from "zh-CN", "ko" from "ko").
var promptTemplates = map[string]map[string]string{
	"zh": {
		"choice":        "选择正确释义: ",
		"fill":          "填空: ",
		"spelling":      "拼写单词: ",
		"pronounce":     "输入读音: ",
		"meaning":       "输入释义: ",
		"read_word":     "朗读单词: ",
		"read_sentence": "朗读句子: ",
	},
	"en": {
		"choice":        "Select the correct meaning for: ",
		"fill":          "Fill in the blank: ",
		"spelling":      "Spell the word for: ",
		"pronounce":     "Enter the pronunciation of: ",
		"meaning":       "What does this mean?: ",
		"read_word":     "Read the word aloud: ",
		"read_sentence": "Read the sentence aloud: ",
	},
	"ko": {
		"choice":        "올바른 뜻을 고르세요: ",
		"fill":          "빈칸 채우기: ",
		"spelling":      "단어 철자 쓰기: ",
		"pronounce":     "발음 입력: ",
		"meaning":       "뜻을 입력하세요: ",
		"read_word":     "단어를 소리내어 읽으세요: ",
		"read_sentence": "문장을 소리내어 읽으세요: ",
	},
	"ja": {
		"choice":        "正しい意味を選んでください: ",
		"fill":          "空欄を埋めてください: ",
		"spelling":      "単語の綴りを入力: ",
		"pronounce":     "読み方を入力: ",
		"meaning":       "意味を入力: ",
		"read_word":     "単語を声に出して読んでください: ",
		"read_sentence": "文章を声に出して読んでください: ",
	},
}

// promptPrefix returns the localized prompt prefix for a question type and explanation language.
// Falls back to "en" if the language is not in the map.
func promptPrefix(qType, explLang string) string {
	lang := explLang
	if idx := strings.Index(explLang, "-"); idx > 0 {
		lang = explLang[:idx]
	}
	tmpl, ok := promptTemplates[lang]
	if !ok {
		tmpl = promptTemplates["en"]
	}
	if s, ok := tmpl[qType]; ok {
		return s
	}
	return ""
}

// Question represents a single quiz question.
type Question struct {
	ID          string       `json:"id"`
	Type        QuestionType `json:"type"`
	SubjectType string       `json:"subject_type"` // "word" | "grammar"
	SubjectID   int64        `json:"subject_id"`
	Prompt      string       `json:"prompt"`
	Options     []string     `json:"options,omitempty"`
	Answer      string       `json:"-"` // not sent to client
}

// GenerateChoice creates a multiple-choice question given a word and distractors.
// explLang is the explanation_lang from the active language config (e.g. "zh-CN", "ko", "en").
func GenerateChoice(word *WordInfo, distractors []*WordInfo, explLang string) *Question {
	options := make([]string, 0, 4)
	options = append(options, word.Meaning)

	// Pick up to 3 distractors
	for i := 0; i < len(distractors) && len(options) < 4; i++ {
		if distractors[i].Meaning != word.Meaning {
			options = append(options, distractors[i].Meaning)
		}
	}

	// Shuffle
	rand.Shuffle(len(options), func(i, j int) {
		options[i], options[j] = options[j], options[i]
	})

	return &Question{
		ID:          genID("word", "choice", word.ID),
		Type:        TypeChoice,
		SubjectType: "word",
		SubjectID:   word.ID,
		Prompt:      promptPrefix("choice", explLang) + word.Word,
		Options:     options,
		Answer:      word.Meaning,
	}
}

// GenerateFill creates a fill-in-the-blank question.
// If word.Example is non-empty, uses sentence-based cloze deletion
// (replace the target word in the example sentence with a blank).
// Falls back to single-word masking when no example is available.
func GenerateFill(word *WordInfo, explLang string) *Question {
	prompt := ""
	answer := word.Word

	if word.Example != "" {
		// Sentence-based cloze: "彼は手紙を___。" instead of "か_く (吃)"
		cloze := strings.Replace(word.Example, word.Word, "___", 1)
		if cloze != word.Example {
			prompt = promptPrefix("fill", explLang) + " 「" + cloze + "」"
		}
	}

	if prompt == "" {
		// Fallback: single-word masking
		blank := maskWord(word.Word)
		prompt = promptPrefix("fill", explLang) + blank + " (" + word.Meaning + ")"
	}

	return &Question{
		ID:          genID("word", "fill", word.ID),
		Type:        TypeFill,
		SubjectType: "word",
		SubjectID:   word.ID,
		Prompt:      prompt,
		Answer:      answer,
	}
}

// GenerateSpelling creates a spelling dictation question.
func GenerateSpelling(word *WordInfo, explLang string) *Question {
	return &Question{
		ID:          genID("word", "spelling", word.ID),
		Type:        TypeSpelling,
		SubjectType: "word",
		SubjectID:   word.ID,
		Prompt:      promptPrefix("spelling", explLang) + word.Meaning,
		Answer:      word.Word,
	}
}

// GeneratePronounce creates a pronunciation recall question.
func GeneratePronounce(word *WordInfo, explLang string) *Question {
	return &Question{
		ID:          genID("word", "pronounce", word.ID),
		Type:        TypePronounce,
		SubjectType: "word",
		SubjectID:   word.ID,
		Prompt:      promptPrefix("pronounce", explLang) + word.Word,
		Answer:      word.Reading,
	}
}

// GenerateMeaning creates a "see word, write meaning" question.
// Answer is the word's meaning (translation in the explanation language).
func GenerateMeaning(word *WordInfo, explLang string) *Question {
	return &Question{
		ID:          genID("word", "meaning", word.ID),
		Type:        TypeMeaning,
		SubjectType: "word",
		SubjectID:   word.ID,
		Prompt:      promptPrefix("meaning", explLang) + word.Word,
		Answer:      word.Meaning,
	}
}

// GenerateReadWord creates a "read aloud" question where the user
// records themselves pronouncing the word. Answer is the reading (kana).
func GenerateReadWord(word *WordInfo, explLang string) *Question {
	return &Question{
		ID:          genID("word", "read_word", word.ID),
		Type:        TypeReadWord,
		SubjectType: "word",
		SubjectID:   word.ID,
		Prompt:      promptPrefix("read_word", explLang) + word.Word,
		Answer:      word.Reading,
	}
}

// GenerateReadSentence creates a "read sentence aloud" question.
// sentence is the text to read. Answer is the sentence itself.
func GenerateReadSentence(sentence string, explLang string) *Question {
	id := "rs" + itoa(int64(len(sentence))) + itoa(int64(len(sentence)%100))
	return &Question{
		ID:          id,
		Type:        TypeReadSentence,
		SubjectType: "sentence",
		SubjectID:   0,
		Prompt:      promptPrefix("read_sentence", explLang) + sentence,
		Answer:      sentence,
	}
}

// WordInfo is the minimal word data needed for question generation.
type WordInfo struct {
	ID      int64
	Word    string
	Reading string
	Meaning string
	POS     string
	Example string // optional example sentence for fill/context questions
}

func maskWord(s string) string {
	if len(s) <= 1 {
		return "_"
	}
	runes := []rune(s)
	n := len(runes)
	reveal := n / 3
	if reveal < 1 {
		reveal = 1
	}
	masked := make([]rune, n)
	for i := range masked {
		masked[i] = '_'
	}
	// Reveal a few positions (first, and evenly spaced)
	masked[0] = runes[0]
	for i := 1; i < reveal; i++ {
		pos := i * n / reveal
		if pos < n {
			masked[pos] = runes[pos]
		}
	}
	return strings.TrimSpace(string(masked))
}

func genID(subjectType string, qtype string, subjectID int64) string {
	// wc42 = word-choice-42, gf7 = grammar-fill-7
	return string(subjectType[0]) + string(qtype[0]) + itoa(subjectID)
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}
