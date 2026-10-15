package engine

// AssignmentType enumerates after-class assignment types.
type AssignmentType string

const (
	AssignmentSentence    AssignmentType = "sentence"
	AssignmentEssay       AssignmentType = "essay"
	AssignmentTranslation AssignmentType = "translation"
	AssignmentFillPassage AssignmentType = "fill_passage"
	AssignmentDiary       AssignmentType = "diary"
)

// Assignment represents a single after-class assignment.
type Assignment struct {
	ID        string         `json:"id"`
	Type      AssignmentType `json:"type"`
	Prompt    string         `json:"prompt"`
	Context   string         `json:"context"`    // words/grammar points to use
	WordCount int            `json:"word_count"` // target word count (essay), 0 = not applicable
}

// AssignmentSubmission is what the user sends back.
type AssignmentSubmission struct {
	AssignmentID string `json:"assignment_id"`
	Answer       string `json:"answer"`
}

// AssignmentGrade is the AI grading result.
type AssignmentGrade struct {
	Original  string            `json:"original"`
	Corrected string            `json:"corrected"`
	Score     float64           `json:"score"` // 0.0–1.0
	Feedback  string            `json:"feedback"`
	Errors    []AssignmentError `json:"errors"`
}

// AssignmentError describes a single mistake.
type AssignmentError struct {
	Type        string `json:"type"`        // grammar/spelling/word_choice/style
	Original    string `json:"original"`    // the problematic text fragment
	Correction  string `json:"correction"`  // suggested fix
	Explanation string `json:"explanation"` // brief note
}

// TimeWeights maps question/assignment types to relative time cost units.
// 1 unit ≈ 10 seconds.
var TimeWeights = map[string]int{
	// Quiz types
	"choice":        1,
	"fill":          1,
	"pronounce":     1,
	"spelling":      2,
	"listening":     2,
	"grammar":       5,
	"reading_comp":  8,
	"read_word":     2,
	"read_sentence": 3,
	// Assignment types
	"sentence":     5,
	"fill_passage": 5,
	"translation":  5,
	"essay":        10,
	"diary":        8,
}

// SlowTypeThreshold is the maximum fraction of today's questions that can be slow types.
const SlowTypeThreshold = 0.25

// IsSlowType returns true if the type is time-heavy.
func IsSlowType(t string) bool {
	switch t {
	case "grammar", "reading_comp", "essay", "listening":
		return true
	}
	return false
}

// IsAssignmentType returns true if the type is an after-class assignment.
func IsAssignmentType(t string) bool {
	switch t {
	case "sentence", "essay", "translation", "fill_passage", "diary":
		return true
	}
	return false
}
