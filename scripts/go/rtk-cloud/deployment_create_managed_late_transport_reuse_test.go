package main

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedLateTransportReuseObserved(t *testing.T, store secretStore, command *environmentCreateExecution) *environmentCreateExecution {
	t.Helper()
	observed := &environmentCreateExecution{Store: store}
	if readEnvironmentPreflightJSON(filepath.Join(store.Root, "deployment/.execution-active/owner.json"), &observed.owner) != nil || readExecutionJSON(filepath.Join(store.Root, command.owner.Journal), &observed.Journal) != nil {
		t.Fatal("isolated current owner/journal could not be reread")
	}
	return observed
}

func TestManagedLateTransportReuseFourFreshScopesShareOnlyPureHistory(t *testing.T) {
	store, command, cfg, _ := managedLateSourceReuseNegativeFixture(t)
	derivations, observations := 0, 0
	for range 4 {
		observed := managedLateTransportReuseObserved(t, store, command)
		view, err := managedLateTransportSourceExecution(store, command.Journal.PlanSHA256, observed, command)
		if err != nil || view == observed || view.lateSourceReuse != command.lateSourceReuse || observed.lateSourceReuse != nil {
			t.Fatal("exact fresh view did not borrow only this command's capability", err)
		}
		// These are the two distinct historical-content modes used by each
		// temporary-transport scope. Native/current observations stay outside it.
		view.Journal.Entries = slices.Clone(view.Journal.Entries[:370])
		view.Journal.KubernetesLeases = slices.Clone(view.Journal.KubernetesLeases)
		for _, kind := range []string{"completed-post-content", "completed-post-content/historical-clock"} {
			var history map[string]string
			hit, err := readManagedLateSourceReuse(store, view, cfg, kind, &history)
			if err != nil {
				t.Fatal(err)
			}
			if !hit {
				derivations++
				history = map[string]string{"authenticated-original": "history"}
				if err := rememberManagedLateSourceReuse(store, view, cfg, kind, history); err != nil {
					t.Fatal(err)
				}
			}
			history["authenticated-original"] = "caller-only-change"
		}
		observations++
	}
	if derivations != 2 || observations != 4 {
		t.Fatalf("want two pure derivations and four fresh observation boundaries, got %d/%d", derivations, observations)
	}
	changed := cfg
	changed.Values = map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev", "changed": "configuration"}
	view := managedLateTransportReuseObserved(t, store, command)
	view, err := managedLateTransportSourceExecution(store, command.Journal.PlanSHA256, view, command)
	if err != nil {
		t.Fatal(err)
	}
	view.Journal.Entries = slices.Clone(view.Journal.Entries[:370])
	view.Journal.KubernetesLeases = slices.Clone(view.Journal.KubernetesLeases)
	var history map[string]string
	if hit, err := readManagedLateSourceReuse(store, view, changed, "completed-post-content", &history); err != nil || hit {
		t.Fatal("changed configuration borrowed another pure source", err)
	}
}

func TestManagedLateTransportReuseRefusesForeignObservedOrCommandSource(t *testing.T) {
	for _, mode := range []string{"command-root", "command-config-root", "command-environment", "observed-root", "observed-config-root", "observed-environment", "parent", "command-owner", "observed-owner", "command-journal", "observed-journal", "observed-lease", "manifest-bytes", "duplicate-command", "missing-observed"} {
		t.Run(mode, func(t *testing.T) {
			store, command, _, paths := managedLateSourceReuseNegativeFixture(t)
			observed := managedLateTransportReuseObserved(t, store, command)
			parent := command.Journal.PlanSHA256
			commands := []*environmentCreateExecution{command}
			switch mode {
			case "command-root":
				command.Store.Root += "-foreign"
			case "command-config-root":
				command.Store.ConfigRoot += "-foreign"
			case "command-environment":
				command.Store.Environment = "staging"
			case "observed-root":
				observed.Store.Root += "-foreign"
			case "observed-config-root":
				observed.Store.ConfigRoot += "-foreign"
			case "observed-environment":
				observed.Store.Environment = "staging"
			case "parent":
				parent = strings.Repeat("e", 64)
			case "command-owner":
				command.owner.OwnerToken = "foreign"
			case "observed-owner":
				observed.owner.Journal += "-foreign"
			case "command-journal":
				command.Journal.OwnerToken = "foreign"
			case "observed-journal":
				observed.Journal.Entries[0].IntentSHA256 = strings.Repeat("e", 64)
			case "observed-lease":
				observed.Journal.KubernetesLeases = append(observed.Journal.KubernetesLeases, deploymentplan.LeaseOwner{UID: "foreign"})
			case "manifest-bytes":
				managedLateSourceReuseNegativeWrite(t, store, paths["material"], []byte(`{"material":"foreign"}`))
			case "duplicate-command":
				commands = append(commands, command)
			case "missing-observed":
				observed = nil
			}
			if _, err := managedLateTransportSourceExecution(store, parent, observed, commands...); err == nil {
				t.Fatal("foreign source borrowed immutable-history authority", mode)
			}
		})
	}
}

func TestManagedLateTransportReuseDroppedOrInvalidatedCapabilityDerivesFresh(t *testing.T) {
	store, command, cfg, _ := managedLateSourceReuseNegativeFixture(t)
	const kind = "isolated-transport-history"
	if err := rememberManagedLateSourceReuse(store, command, cfg, kind, map[string]string{"original": "retained"}); err != nil {
		t.Fatal(err)
	}
	before := deploymentplan.Digest(command.Journal)
	for _, mode := range []string{"ordinary", "explicit-nil", "dropped", "invalidated"} {
		t.Run(mode, func(t *testing.T) {
			observed := managedLateTransportReuseObserved(t, store, command)
			selected := *command
			var commands []*environmentCreateExecution
			switch mode {
			case "explicit-nil":
				commands = []*environmentCreateExecution{nil}
			case "dropped":
				selected.lateSourceReuse = nil
				commands = []*environmentCreateExecution{&selected}
			case "invalidated":
				invalidateManagedLateSourceReuse(&selected)
				commands = []*environmentCreateExecution{&selected}
			}
			view, err := managedLateTransportSourceExecution(store, command.Journal.PlanSHA256, observed, commands...)
			if err != nil || view != observed || view.lateSourceReuse != nil {
				t.Fatal("absent or invalidated capability became source authority", err)
			}
			var history map[string]string
			if hit, err := readManagedLateSourceReuse(store, view, cfg, kind, &history); err != nil || hit {
				t.Fatal("ordinary fresh derivation reused another command's retained source", err)
			}
		})
	}
	if deploymentplan.Digest(command.Journal) != before {
		t.Fatal("transport reuse changed current execution history")
	}
	// No capability survives command serialization either.
	raw, err := json.Marshal(command)
	var next environmentCreateExecution
	if err != nil || json.Unmarshal(raw, &next) != nil || next.lateSourceReuse != nil {
		t.Fatal("command-local source capability became durable authority", err)
	}
}
