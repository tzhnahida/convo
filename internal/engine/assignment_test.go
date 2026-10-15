package engine

import "testing"

func TestIsSlowType(t *testing.T) {
	slow := []string{"grammar", "reading_comp", "essay", "listening"}
	fast := []string{"choice", "fill", "pronounce", "spelling", "sentence", "translation", "diary"}

	for _, s := range slow {
		if !IsSlowType(s) {
			t.Errorf("IsSlowType(%q) = false, want true", s)
		}
	}
	for _, f := range fast {
		if IsSlowType(f) {
			t.Errorf("IsSlowType(%q) = true, want false", f)
		}
	}
}

func TestIsAssignmentType(t *testing.T) {
	assignments := []string{"sentence", "essay", "translation", "fill_passage", "diary"}
	quizzes := []string{"choice", "fill", "pronounce", "spelling", "listening", "reading_comp", "grammar"}

	for _, a := range assignments {
		if !IsAssignmentType(a) {
			t.Errorf("IsAssignmentType(%q) = false, want true", a)
		}
	}
	for _, q := range quizzes {
		if IsAssignmentType(q) {
			t.Errorf("IsAssignmentType(%q) = true, want false", q)
		}
	}
}

func TestTimeWeights(t *testing.T) {
	tests := []struct {
		name   string
		weight int
	}{
		{"choice", 1},
		{"fill", 1},
		{"pronounce", 1},
		{"spelling", 2},
		{"listening", 2},
		{"grammar", 5},
		{"reading_comp", 8},
		{"essay", 10},
		{"diary", 8},
		{"sentence", 5},
	}
	for _, tt := range tests {
		w := TimeWeights[tt.name]
		if w != tt.weight {
			t.Errorf("TimeWeights[%q] = %d, want %d", tt.name, w, tt.weight)
		}
	}
	// Unknown type defaults to 0 (caller handles default)
	if TimeWeights["unknown"] != 0 {
		t.Errorf("TimeWeights[unknown] = %d, want 0", TimeWeights["unknown"])
	}
}

func TestAssignmentTypes(t *testing.T) {
	types := []AssignmentType{
		AssignmentSentence, AssignmentEssay, AssignmentTranslation,
		AssignmentFillPassage, AssignmentDiary,
	}
	expected := []string{"sentence", "essay", "translation", "fill_passage", "diary"}
	for i, at := range types {
		if string(at) != expected[i] {
			t.Errorf("AssignmentType = %q, want %q", at, expected[i])
		}
	}
}
