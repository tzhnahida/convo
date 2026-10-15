package ai

import "testing"

func TestLoadPromptExists(t *testing.T) {
	pt, err := LoadPrompt("judge")
	if err != nil {
		t.Fatalf("LoadPrompt judge: %v", err)
	}
	if pt.Version != "2.0" {
		t.Errorf("version = %q, want 2.0", pt.Version)
	}
	if pt.System == "" {
		t.Error("system prompt is empty")
	}
	if pt.User == "" {
		t.Error("user prompt is empty")
	}
}

func TestBuildMessages(t *testing.T) {
	pt, err := LoadPrompt("judge")
	if err != nil {
		t.Fatalf("LoadPrompt: %v", err)
	}

	vars := &PromptVars{
		Word:            "hello",
		Reading:         "həˈloʊ",
		Meaning:         "你好",
		ExpectedAnswer:  "你好",
		UserAnswer:      "你好",
		ExplanationLang: "zh",
	}

	messages, err := pt.BuildMessages(vars)
	if err != nil {
		t.Fatalf("BuildMessages: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages))
	}
	if messages[0].Role != "system" {
		t.Errorf("first message role = %q, want system", messages[0].Role)
	}
	if messages[1].Role != "user" {
		t.Errorf("second message role = %q, want user", messages[1].Role)
	}
	// Check that template variables were rendered
	if messages[1].Content == "" {
		t.Error("user message is empty")
	}
}

func TestBuildMessagesEmptyVars(t *testing.T) {
	pt, _ := LoadPrompt("judge")

	// Even with empty values, template should render (struct fields always exist)
	vars := &PromptVars{}
	messages, err := pt.BuildMessages(vars)
	if err != nil {
		t.Fatalf("BuildMessages with empty vars: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages))
	}
}

func TestRenderTemplateInvalidSyntax(t *testing.T) {
	// Template with unclosed action
	_, err := renderTemplate("hello {{.Name", &PromptVars{})
	if err == nil {
		t.Error("expected parse error for invalid template")
	}
}
