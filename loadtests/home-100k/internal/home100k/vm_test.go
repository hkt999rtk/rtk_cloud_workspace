package home100k

import "testing"

func TestBuildLifecycleActionsTagsEphemeralVMsByRunID(t *testing.T) {
	plan, err := NewPlan(PlanOptions{
		EnvRoot:   "cloud_env/staging/runtime",
		Brandname: "RTK",
		Region:    "us-sea",
	})
	if err != nil {
		t.Fatalf("NewPlan() error = %v", err)
	}
	actions := BuildLifecycleActions(plan, "run-123")

	createMixed := filterActions(actions, "provision-vm", "mixed")
	if len(createMixed) != 5 {
		t.Fatalf("mixed provision actions = %d, want 5", len(createMixed))
	}
	for _, action := range createMixed {
		if action.RunID != "run-123" {
			t.Fatalf("action run id = %q, want run-123", action.RunID)
		}
		if !contains(action.Tags, "home-100k") || !contains(action.Tags, "run-123") || !contains(action.Tags, "load-generator") || !contains(action.Tags, action.Role) {
			t.Fatalf("action tags = %#v, want home-100k/run/load-generator/role tags", action.Tags)
		}
	}
}

func TestBuildLifecycleActionsIncludesVideoOnlyVMs(t *testing.T) {
	plan, err := NewPlan(PlanOptions{
		EnvRoot:               "cloud_env/staging/runtime",
		Brandname:             "RTK",
		Region:                "us-sea",
		DeviceCount:           20000,
		VMCount:               2,
		VideoGeneratorVMCount: 2,
	})
	if err != nil {
		t.Fatalf("NewPlan() error = %v", err)
	}
	actions := BuildLifecycleActions(plan, "run-video")

	createVideo := filterActions(actions, "provision-vm", "video")
	if len(createVideo) != 2 {
		t.Fatalf("video provision actions = %d, want 2", len(createVideo))
	}
	for _, action := range createVideo {
		if !contains(action.Tags, "video") || !contains(action.Tags, "run-video") {
			t.Fatalf("video action tags = %#v, want video/run-id tags", action.Tags)
		}
	}
	if destroyVideo := filterActions(actions, "destroy-vm", "video"); len(destroyVideo) != 2 {
		t.Fatalf("video destroy actions = %d, want 2", len(destroyVideo))
	}
}

func TestBuildLifecycleActionsContainsRequiredWorkflowOrder(t *testing.T) {
	plan, err := NewPlan(PlanOptions{
		EnvRoot:   "cloud_env/staging/runtime",
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
