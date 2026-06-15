package home100k

import "testing"

func TestBuildLifecycleActionsTagsEphemeralVMsByRunID(t *testing.T) {
	plan, err := NewPlan(PlanOptions{
		EnvRoot:   "cloud_env/staging/lke",
		Brandname: "RTK",
		Region:    "us-sea",
	})
	if err != nil {
		t.Fatalf("NewPlan() error = %v", err)
	}
	actions := BuildLifecycleActions(plan, "run-123")

	createMixed := filterActions(actions, "provision-vm", "mixed")
	if len(createMixed) != 10 {
		t.Fatalf("mixed provision actions = %d, want 10", len(createMixed))
	}
	for _, action := range createMixed {
		if action.RunID != "run-123" {
			t.Fatalf("action run id = %q, want run-123", action.RunID)
		}
		if !contains(action.Tags, "home-100k") || !contains(action.Tags, "run-123") || !contains(action.Tags, action.Role) {
			t.Fatalf("action tags = %#v, want home-100k/run/role tags", action.Tags)
		}
	}
}

func TestBuildLifecycleActionsContainsRequiredWorkflowOrder(t *testing.T) {
	plan, err := NewPlan(PlanOptions{
		EnvRoot:   "cloud_env/staging/lke",
		Brandname: "RTK",
		Region:    "us-sea",
	})
	if err != nil {
		t.Fatalf("NewPlan() error = %v", err)
	}
	actions := BuildLifecycleActions(plan, "run-123")
	wantOrder := []string{
		"provision-vm",
		"sync",
		"run-stages",
		"collect",
		"collect-server-evidence",
		"aggregate",
		"destroy-vm",
	}
	last := -1
	for _, want := range wantOrder {
		idx := firstActionIndex(actions, want)
		if idx < 0 {
			t.Fatalf("missing action %q in %#v", want, actions)
		}
		if idx <= last {
			t.Fatalf("action %q index %d is not after previous index %d", want, idx, last)
		}
		last = idx
	}
}

func filterActions(actions []LifecycleAction, action string, role string) []LifecycleAction {
	out := []LifecycleAction{}
	for _, item := range actions {
		if item.Action == action && item.Role == role {
			out = append(out, item)
		}
	}
	return out
}

func firstActionIndex(actions []LifecycleAction, action string) int {
	for idx, item := range actions {
		if item.Action == action {
			return idx
		}
	}
	return -1
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
