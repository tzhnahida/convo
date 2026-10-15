package engine

import (
	"testing"
	"time"
)

func TestSM2FirstReviewCorrect(t *testing.T) {
	now := time.Now()
	r := SM2(2.5, 0, 0, 4, now, 0)
	if r == nil {
		t.Fatal("expected result")
	}
	if r.Interval != 1 {
		t.Errorf("first correct: interval = %d, want 1", r.Interval)
	}
	if r.Repetitions != 1 {
		t.Errorf("first correct: reps = %d, want 1", r.Repetitions)
	}
	if r.NextReviewAt.Before(now) {
		t.Error("next review should be in the future")
	}
}

func TestSM2SecondReviewCorrect(t *testing.T) {
	now := time.Now()
	r := SM2(2.5, 1, 1, 4, now, 0)
	if r == nil {
		t.Fatal("expected result")
	}
	if r.Interval != 6 {
		t.Errorf("second correct: interval = %d, want 6", r.Interval)
	}
	if r.Repetitions != 2 {
		t.Errorf("second correct: reps = %d, want 2", r.Repetitions)
	}
}

func TestSM2IncorrectResets(t *testing.T) {
	now := time.Now()
	r := SM2(2.5, 6, 2, 1, now, 0)
	if r == nil {
		t.Fatal("expected result")
	}
	if r.Interval != 1 {
		t.Errorf("incorrect: interval = %d, want 1", r.Interval)
	}
	if r.Repetitions != 0 {
		t.Errorf("incorrect: reps = %d, want 0", r.Repetitions)
	}
}

func TestSM2EasinessFloor(t *testing.T) {
	now := time.Now()
	// Multiple incorrect reviews should not push EF below 1.3
	ef := 2.5
	for i := 0; i < 20; i++ {
		r := SM2(ef, 1, 0, 0, now, 0)
		ef = r.EasinessFactor
	}
	if ef < 1.3 {
		t.Errorf("EF dropped below floor: %f", ef)
	}
}

func TestSM2InvalidQuality(t *testing.T) {
	r := SM2(2.5, 0, 0, -1, time.Now(), 0)
	if r != nil {
		t.Error("expected nil for quality < 0")
	}
	r = SM2(2.5, 0, 0, 6, time.Now(), 0)
	if r != nil {
		t.Error("expected nil for quality > 5")
	}
}

func TestSM2ThirdReviewCorrect(t *testing.T) {
	now := time.Now()
	// reps >= 2: interval = round(previous_interval * EF)
	// interval=6, ef=2.5 → round(6*2.5) = 15
	r := SM2(2.5, 6, 2, 4, now, 0)
	if r == nil {
		t.Fatal("expected result")
	}
	if r.Interval != 15 {
		t.Errorf("third correct: interval = %d, want 15", r.Interval)
	}
	if r.Repetitions != 3 {
		t.Errorf("third correct: reps = %d, want 3", r.Repetitions)
	}
}

func TestSM2QualityBoundaryPass(t *testing.T) {
	now := time.Now()
	// Quality 3 is the threshold: >= 3 means correct
	r := SM2(2.5, 0, 0, 3, now, 0)
	if r == nil {
		t.Fatal("expected result")
	}
	if r.Repetitions != 1 {
		t.Errorf("quality 3 should be correct: reps = %d, want 1", r.Repetitions)
	}
	if r.Interval != 1 {
		t.Errorf("quality 3 first: interval = %d, want 1", r.Interval)
	}
}

func TestSM2QualityBoundaryFail(t *testing.T) {
	now := time.Now()
	// Quality 2 is below threshold — resets
	r := SM2(2.5, 6, 3, 2, now, 0)
	if r == nil {
		t.Fatal("expected result")
	}
	if r.Repetitions != 0 {
		t.Errorf("quality 2 should reset: reps = %d, want 0", r.Repetitions)
	}
	if r.Interval != 1 {
		t.Errorf("quality 2 should reset: interval = %d, want 1", r.Interval)
	}
}

func TestSM2EFDecreasesOnLowQuality(t *testing.T) {
	now := time.Now()
	// quality 0 causes strong EF decrease
	r := SM2(2.5, 0, 0, 0, now, 0)
	if r == nil {
		t.Fatal("expected result")
	}
	if r.EasinessFactor >= 2.5 {
		t.Errorf("EF should decrease on quality 0: ef = %f", r.EasinessFactor)
	}
}

func TestSM2EFIncreasesOnHighQuality(t *testing.T) {
	now := time.Now()
	r := SM2(2.5, 0, 0, 5, now, 0)
	if r == nil {
		t.Fatal("expected result")
	}
	if r.EasinessFactor <= 2.5 {
		t.Errorf("EF should increase on quality 5: ef = %f", r.EasinessFactor)
	}
}

func TestSM2EFUnchangedOnQuality4(t *testing.T) {
	now := time.Now()
	// quality 4: EF delta ≈ 0.1 - (5-4)*(0.08+(5-4)*0.02) = 0.1 - 0.1 = 0
	r := SM2(2.5, 0, 0, 4, now, 0)
	if r == nil {
		t.Fatal("expected result")
	}
	if r.EasinessFactor != 2.5 {
		t.Errorf("EF should be unchanged on quality 4: ef = %f, want 2.5", r.EasinessFactor)
	}
}

func TestQualityFromScore(t *testing.T) {
	cases := []struct {
		score   float64
		quality int
	}{
		{1.0, 5},
		{0.9, 4},
		{0.6, 3},
		{0.4, 2},
		{0.2, 1},
		{0.05, 0},
	}
	for _, c := range cases {
		if q := QualityFromScore(c.score); q != c.quality {
			t.Errorf("QualityFromScore(%f) = %d, want %d", c.score, q, c.quality)
		}
	}
}
