package ai

import (
	"embed"
	"fmt"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"
)

//go:embed prompts/*.yaml
var promptFS embed.FS

type PromptTemplate struct {
	Version     string  `yaml:"version"`
	System      string  `yaml:"system"`
	User        string  `yaml:"user"`
	Temperature float64 `yaml:"temperature"`
}

type PromptVars struct {
	Word            string
	Reading         string
	Meaning         string
	POS             string
	UserAnswer      string
	ExpectedAnswer  string
	Language        string
	Level           string
	TeacherRole     string
	ExplanationLang string
	ExtraContext    string
	QuestionType    string
	// Assignment fields
	AssignmentType     string
	Topic              string
	WordList           string // comma-separated words to use
	GrammarPoint       string
	GrammarTitle       string
	GrammarExplanation string
	GrammarExample     string
	ExistingWords      string // comma-separated known words
	ExistingGrammar    string // comma-separated existing grammar titles
	VocabSummary       string // categorized vocabulary summary (nouns: X, verbs: Y, etc.)
	QueryResult        string // word availability check results (e.g. "猫=yes, 犬=no")
	ExampleFeedback    string // feedback when AI example contains unknown words
	WordCount          int
	ExpandCount        int    // target number of words AI should generate
	DuplicateWords     string // duplicates from previous AI round
	UserSubmission     string
	// Summary/plan fields
	SessionStats  string // JSON blob of today's stats
	YesterdayPlan string
	WeakPoints    string
	TrendData     string
}

func LoadPrompt(name string) (*PromptTemplate, error) {
	data, err := promptFS.ReadFile("prompts/" + name + ".yaml")
	if err != nil {
		return nil, fmt.Errorf("read prompt %s: %w", name, err)
	}

	var pt PromptTemplate
	if err := yaml.Unmarshal(data, &pt); err != nil {
		return nil, fmt.Errorf("parse prompt %s: %w", name, err)
	}
	return &pt, nil
}

func (pt *PromptTemplate) BuildMessages(vars *PromptVars) ([]Message, error) {
	system, err := renderTemplate(pt.System, vars)
	if err != nil {
		return nil, fmt.Errorf("render system: %w", err)
	}
	user, err := renderTemplate(pt.User, vars)
	if err != nil {
		return nil, fmt.Errorf("render user: %w", err)
	}
	return []Message{
		{Role: "system", Content: system},
		{Role: "user", Content: user},
	}, nil
}

func renderTemplate(tmpl string, vars *PromptVars) (string, error) {
	t, err := template.New("p").Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("parse template: %w", err)
	}
	var buf strings.Builder
	if err := t.Execute(&buf, vars); err != nil {
		return "", fmt.Errorf("execute template: %w", err)
	}
	return buf.String(), nil
}
