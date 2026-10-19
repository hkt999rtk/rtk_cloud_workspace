package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedLateTerminalCapabilityFixture(t *testing.T) (*environmentCreateExecution, deploymentConfig, *createServiceSessionRecovery) {
	t.Helper()
	e, _ := managedLateTerminalHeld396Fixture(t)
	prefix := e.Journal
	prefix.Entries = prefix.Entries[:392]
	remaining := &createManagedLateRemaining{ParentSHA: e.Journal.PlanSHA256, OwnerSHA: deploymentplan.Digest(e.owner), CurrentSourceSHA: deploymentplan.Digest(prefix)}
	remaining.TerminalFinalizerCompletion = &createManagedLateTerminalFinalizerCompletion{Schema: managedLateTerminalFinalizerSchema, SourceSHA: deploymentplan.Digest(e.Journal), PrefixSHA: remaining.CurrentSourceSHA}
	r := &createServiceSessionRecovery{ManagedLateSession: &createManagedLateSessionRecovery{}, ManagedLateRemaining: remaining, ManagedTransportRecovery: &createManagedTransportRecovery{}, MQTTRoot: &environmentCreateRegisteredIssuer{}, MQTTIntermediate: &environmentCreateRegisteredIssuer{}, OpenBaoRoot: &environmentCreateRegisteredIssuer{}, OpenBaoIntermediate: &environmentCreateRegisteredIssuer{}}
	return e, deploymentConfig{Environment: e.Store.Environment}, r
}

func TestManagedLateTerminalCapabilityViewKeepsHeld396RoutingProvenance(t *testing.T) {
	e, cfg, r := managedLateTerminalCapabilityFixture(t)
	core, routingParent, raw, actualConfigMap := managedCoreRoutingFixture(t)
	var binding environmentAppRoutingBinding
	if json.Unmarshal(raw, &binding) != nil {
		t.Fatal("routing fixture")
	}
	binding.CreatePlanSHA256 = e.Journal.PlanSHA256
	raw = mustProviderRecoveryJSON(binding)
	core.Routing.Resources[0].Desired = environmentAppRoutingDefinition(binding).Object
	core.RoutingJournal.PlanSHA256 = core.Routing.SHA256()
	actualConfigMap = deploymentplan.Clone(core.Routing.Resources[0].Desired)
	deploymentplan.Map(actualConfigMap["metadata"])["uid"] = binding.DeclarationUID
	e.Journal.Entries[0] = routingParent.Entries[0]
	e.Journal.Entries[0].IntentSHA256 = activeTransportJournalDigest(binding)
	e.Journal.Entries[0].Receipt = mustProviderRecoveryJSON(binding)
	if err := e.save(); err != nil {
		t.Fatal(err)
	}
	prefix := e.Journal
	prefix.Entries = prefix.Entries[:392]
	r.ManagedLateRemaining.CurrentSourceSHA = deploymentplan.Digest(prefix)
	r.ManagedLateRemaining.TerminalFinalizerCompletion.SourceSHA = deploymentplan.Digest(e.Journal)
	r.ManagedLateRemaining.TerminalFinalizerCompletion.PrefixSHA = deploymentplan.Digest(prefix)
	before := deploymentplan.Digest([]any{e.Journal, e.owner, r})
	// This finite parsing seam grants no authority by itself. Both production
	// callers authenticate the full terminal source before using its view.
	view, err := managedLateTerminalCapabilitySourceView(context.Background(), cfg, e.Store, e, r)
	if err != nil || len(view.Journal.Entries) != 392 || view.Check(context.Background()) == nil || checkManagedLatePureSourceExecution(context.Background(), view) != nil {
		t.Fatal("exact held396 did not yield a read-only routing ancestry view", err)
	}
	full, err := validateManagedCoreRoutingPublication(core, e.Journal, raw, actualConfigMap)
	if err != nil {
		t.Fatal("actual396 lost its original accepted routing proof", err)
	}
	bounded, err := validateManagedCoreRoutingPublication(core, view.Journal, raw, actualConfigMap)
	if err != nil || deploymentplan.Digest(full) != deploymentplan.Digest(bounded) {
		t.Fatal("pure392 routing provenance differed from actual396", err)
	}
	if e.Check(context.Background()) != nil || before != deploymentplan.Digest([]any{e.Journal, e.owner, r}) {
		t.Fatal("routing ancestry changed the actual held396 or descriptor")
	}
}

func TestManagedLateTerminalCapabilityViewRefusesForeignAndUnsafeSource(t *testing.T) {
	for _, mode := range []string{"missing-marker", "foreign-source", "foreign-prefix", "foreign-parent", "foreign-owner", "foreign-store", "foreign-config", "foreign-schema", "unsafe-view", "unknown-tail", "canonical-drift", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			e, cfg, r := managedLateTerminalCapabilityFixture(t)
			store, ctx := e.Store, context.Background()
			switch mode {
			case "missing-marker":
				r.ManagedLateRemaining.TerminalFinalizerCompletion = nil
			case "foreign-source":
				r.ManagedLateRemaining.TerminalFinalizerCompletion.SourceSHA = strings.Repeat("9", 64)
			case "foreign-prefix":
				r.ManagedLateRemaining.CurrentSourceSHA = strings.Repeat("9", 64)
				r.ManagedLateRemaining.TerminalFinalizerCompletion.PrefixSHA = strings.Repeat("9", 64)
			case "foreign-parent":
				r.ManagedLateRemaining.ParentSHA = strings.Repeat("9", 64)
			case "foreign-owner":
				r.ManagedLateRemaining.OwnerSHA = strings.Repeat("9", 64)
			case "foreign-store":
				store.ConfigRoot += "-foreign"
			case "foreign-config":
				cfg.Environment = "staging"
			case "foreign-schema":
				r.ManagedLateRemaining.TerminalFinalizerCompletion.Schema = "foreign"
			case "unsafe-view":
				var err error
				e, err = managedLateTerminalPureSourceView(ctx, e)
				if err != nil {
					t.Fatal(err)
				}
			case "unknown-tail":
				e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: "foreign"})
			case "canonical-drift":
				managedLateSourceReuseNegativeWrite(t, store, e.owner.Journal, []byte(`{"foreign":true}`))
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if view, err := managedLateTerminalCapabilitySourceView(ctx, cfg, store, e, r); err == nil || view != nil {
				t.Fatal("foreign/unheld source gained terminal parsing view")
			}
		})
	}
}

func TestManagedLateTerminalCapabilityAndRoutingRefuseMarkerBeforeNativeIO(t *testing.T) {
	for _, mode := range []string{"marker-only", "foreign-source", "unsafe-view"} {
		t.Run(mode, func(t *testing.T) {
			e, cfg, r := managedLateTerminalCapabilityFixture(t)
			if mode == "foreign-source" {
				r.ManagedLateRemaining.TerminalFinalizerCompletion.SourceSHA = strings.Repeat("9", 64)
			} else if mode == "unsafe-view" {
				var err error
				e, err = managedLateTerminalPureSourceView(context.Background(), e)
				if err != nil {
					t.Fatal(err)
				}
			}
			p := &createFoundationRecoveryPlan{Materials: map[string]string{"original": strings.Repeat("1", 64)}}
			before := deploymentplan.Digest([]any{e.Journal, r, p})
			// No configured backend exists: the strong terminal gate must reject
			// these unproved descriptors before any publication/native getter.
			err := qualifyEnvironmentCreateManagedTransportCapabilities(context.Background(), cfg, e.Store, e, environmentKubernetesBackend{}, nil, p, r)
			if err == nil || !strings.Contains(err.Error(), "terminal-finalizer-") || strings.Contains(err.Error(), "all-CA-ACTIVE") {
				t.Fatal("terminal marker reached legacy checkpoint or native capability IO", err)
			}
			if _, err := readManagedLateRoutingCapability(context.Background(), cfg, e.Store, e, environmentKubernetesBackend{}, r, p); err == nil || !strings.Contains(err.Error(), "terminal-finalizer-") {
				t.Fatal("unproved terminal marker reached routing native IO", err)
			}
			if before != deploymentplan.Digest([]any{e.Journal, r, p}) {
				t.Fatal("rejected terminal capability changed source/evidence")
			}
		})
	}
}
