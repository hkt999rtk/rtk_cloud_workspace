package main

import (
	"fmt"
	"testing"
	"time"
)

func TestArrangeCardsCompletedLimitAndFinishedTimeOrder(t *testing.T) {
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	var cards []Card
	for i := int64(1); i <= 22; i++ {
		cards = append(cards, Card{Key: fmt.Sprint(i), RunID: i, JobID: i, Status: "completed", RunStartedAt: base.Add(time.Duration(30-i) * time.Minute), UpdatedAt: base.Add(time.Duration(i/2) * time.Minute)})
	}
	_, _, completed := arrangeCards(cards, 20)
	if len(completed) != 20 {
		t.Fatalf("completed length = %d, want 20", len(completed))
	}
	for _, card := range completed {
		if card.RunID == 1 || card.RunID == 2 {
			t.Fatalf("oldest completed job %d was not evicted", card.RunID)
		}
	}
	for i := 1; i < len(completed); i++ {
		previous, current := finished(completed[i-1]), finished(completed[i])
		if previous.Before(current) || (previous.Equal(current) && completed[i-1].RunID < completed[i].RunID) {
			t.Fatalf("completed cards are not ordered by newest finish time at %d", i)
		}
	}
}

func TestArrangeCardsMovesSameRunAfterRerun(t *testing.T) {
	failed := Card{Key: "o/r/7", RunID: 7, Attempt: 1, Status: "completed", Conclusion: "failure"}
	_, _, completed := arrangeCards([]Card{failed}, 30)
	if len(completed) != 1 {
		t.Fatal("failed attempt should initially be completed")
	}
	rerun := failed
	rerun.Attempt, rerun.Status, rerun.Conclusion = 2, "in_progress", ""
	_, running, completed := arrangeCards([]Card{rerun}, 30)
	if len(running) != 1 || len(completed) != 0 || running[0].Key != failed.Key {
		t.Fatalf("rerun should keep key and move to running: %#v %#v", running, completed)
	}
}

func TestFailureClassification(t *testing.T) {
	for _, conclusion := range []string{"failure", "timed_out", "action_required", "startup_failure"} {
		if !isFailure(conclusion) {
			t.Errorf("%q should be a failure", conclusion)
		}
	}
	for _, conclusion := range []string{"success", "cancelled", "neutral", ""} {
		if isFailure(conclusion) {
			t.Errorf("%q should not be a failure", conclusion)
		}
	}
}

func TestCompletedConclusionDoesNotOverrideFinishedTime(t *testing.T) {
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	cards := []Card{
		{RunID: 1, Status: "completed", Conclusion: "failure", UpdatedAt: base.Add(time.Minute)},
		{RunID: 2, Status: "completed", Conclusion: "success", UpdatedAt: base.Add(3 * time.Minute)},
		{RunID: 3, Status: "completed", Conclusion: "cancelled", UpdatedAt: base.Add(2 * time.Minute)},
	}
	_, _, completed := arrangeCards(cards, 20)
	if len(completed) != 3 || completed[0].RunID != 2 || completed[1].RunID != 3 || completed[2].RunID != 1 {
		t.Fatalf("conclusion changed chronological order: %#v", completed)
	}
}
