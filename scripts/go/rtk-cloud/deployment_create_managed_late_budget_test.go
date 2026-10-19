package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestManagedLatePreparationUsesOriginalCommandStart(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), managedLateOverallLimit)
	defer cancel()
	started := time.Now().Add(-29 * time.Minute)
	prepared, stop, err := managedLatePreparationContext(parent, started)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	deadline, _ := prepared.Deadline()
	if !deadline.Equal(started.Add(managedLatePreparationLimit)) || time.Until(deadline) > time.Minute {
		t.Fatal("expensive preparation reset the original command clock")
	}
	for _, start := range []time.Time{{}, time.Now().Add(time.Minute), time.Now().Add(-managedLatePreparationLimit)} {
		if _, _, err := managedLatePreparationContext(parent, start); err == nil {
			t.Fatal("invalid or exhausted preparation accepted")
		}
	}
}

func TestManagedLateNativeBudgetIsQualifiedFixedAndNonResettable(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), managedLateOverallLimit)
	defer cancel()
	materials := map[string]string{}
	if err := bindManagedLateBudgetMaterials(materials); err != nil {
		t.Fatal(err)
	}
	native, stop, err := beginManagedLateNativeBudget(parent, materials)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	budget, err := managedLateActiveBudget(native)
	deadline, _ := native.Deadline()
	if err != nil || !deadline.Equal(budget.StartedAt.Add(50*time.Minute)) || budget.Overall != parent {
		t.Fatal("native phase lacks its fixed original-parent deadline", err)
	}
	child, childStop := context.WithTimeout(native, 20*time.Minute)
	defer childStop()
	if again, err := managedLateActiveBudget(child); err != nil || !again.Deadline.Equal(deadline) {
		t.Fatal("a native recheck moved the fixed phase cutoff", err)
	}
	if _, _, err := beginManagedLateNativeBudget(native, materials); err == nil {
		t.Fatal("native clock could be reset")
	}
	for _, name := range []string{"short original deadline", "missing qualified pin", "changed qualified pin", "canceled parent", "unbounded parent"} {
		t.Run(name, func(t *testing.T) {
			ctx := parent
			pins := map[string]string{}
			for key, value := range materials {
				pins[key] = value
			}
			switch name {
			case "short original deadline":
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(parent, 49*time.Minute)
				defer stop()
			case "missing qualified pin":
				delete(pins, "accepted-service-core-late-budget-native")
			case "changed qualified pin":
				pins["accepted-service-core-late-budget-native"] = deploymentplan.Digest("60m")
			case "canceled parent":
				var stop context.CancelFunc
				ctx, stop = context.WithCancel(parent)
				stop()
			case "unbounded parent":
				ctx = context.Background()
			}
			if _, _, err := beginManagedLateNativeBudget(ctx, pins); err == nil {
				t.Fatal("unqualified native budget admitted")
			}
		})
	}
	if len(materials) != 6 || managedLateOverallLimit != 90*time.Minute || managedLateFinalizerLimit != 15*time.Minute {
		t.Fatal("qualified scope or fixed limits changed")
	}
}

func TestManagedLateActualLeafAndSessionCoverFixedFiftyMinutePhase(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), managedLateOverallLimit)
	defer cancel()
	native, stop, err := beginManagedLateNativeBudget(parent, managedLateBudgetMaterials())
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	budget, _ := managedLateActiveBudget(native)
	cutoff := budget.Deadline.Add(managedLateExpiryReserve)
	for _, test := range []struct {
		name      string
		leaf, sid time.Time
		pass      bool
	}{
		{"actual one-hour leaf", budget.StartedAt.Add(time.Hour), budget.StartedAt.Add(time.Hour), true},
		{"exact five-minute reserve", cutoff, cutoff, true},
		{"clipped leaf", cutoff.Add(-time.Nanosecond), cutoff, false},
		{"clipped native session", cutoff, cutoff.Add(-time.Nanosecond), false},
		{"expired session", cutoff, time.Now().Add(-time.Second), false},
		{"missing certificate", time.Time{}, cutoff, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := verifyManagedLateSeedBudget(native, test.leaf, test.sid); (err == nil) != test.pass {
				t.Fatal("requested TTL substituted for actual certificate/session expiry", err)
			}
		})
	}
	if err := verifyManagedLateFinalizerBudget(native); err != nil {
		t.Fatal(err)
	}
	short, shortStop := context.WithTimeout(native, 14*time.Minute)
	defer shortStop()
	if verifyManagedLateFinalizerBudget(short) == nil {
		t.Fatal("finalizer started without its full fifteen-minute reserve")
	}
}

func TestManagedLateOverallContextReturnsOnlyAfterGenuineFinalizerReceipt(t *testing.T) {
	for _, name := range []string{"completed", "missing", "pending", "wrong session", "foreign environment", "copies remain", "missing CRL proof", "after phase deadline", "before phase start", "expired overall"} {
		t.Run(name, func(t *testing.T) {
			overall, stop := context.WithTimeout(context.Background(), 90*time.Minute)
			defer stop()
			started := time.Now().Add(-2 * time.Minute)
			budget := managedLateRuntimeBudget{StartedAt: started, Deadline: started.Add(50 * time.Minute), Overall: overall}
			ctx, cancel := context.WithDeadline(context.WithValue(overall, managedLateBudgetKey{}, budget), budget.Deadline)
			defer cancel()
			sid := "00000000-0000-4000-8000-000000000002"
			r := &createServiceSessionRecovery{ManagedLateSession: &createManagedLateSessionRecovery{NewSessionID: sid}, Ceremony: environmentCreateServiceCeremony{BootstrapSessionID: sid}}
			r.Ceremony.Intermediate.ID = "00000000-0000-4000-8000-000000000003"
			intent := deploymentplan.Object{"session_id": sid, "issuer_id": r.Ceremony.Intermediate.ID, "cleanup_binding": r.Ceremony.CleanupBinding}
			native := environmentCreateBootstrapFinalization{Environment: "dev", SessionID: sid, LeafSHA256: strings.Repeat("a", 64), CRLSHA256: strings.Repeat("b", 64), CleanupSHA256: strings.Repeat("c", 64), FinalizedAt: started.Add(time.Minute)}
			receipt := deploymentplan.Object{"native_finalization": native, "custody_tombstone_sha256": strings.Repeat("d", 64), "copies_absent": true}
			entry := environmentCreateJournalEntry{Key: "service-session-finalize", Phase: "bootstrap", State: "completed", StartedAt: started.Add(30 * time.Second), CompletedAt: started.Add(90 * time.Second), IntentSHA256: activeTransportJournalDigest(intent)}
			switch name {
			case "pending":
				entry.State = "pending"
			case "wrong session":
				native.SessionID = "00000000-0000-4000-8000-000000000009"
				receipt["native_finalization"] = native
			case "foreign environment":
				native.Environment = "staging"
				receipt["native_finalization"] = native
			case "copies remain":
				receipt["copies_absent"] = false
			case "missing CRL proof":
				native.CRLSHA256 = ""
				receipt["native_finalization"] = native
			case "after phase deadline":
				entry.CompletedAt = budget.Deadline.Add(time.Second)
			case "before phase start":
				entry.StartedAt = started.Add(-time.Second)
			case "expired overall":
				stop()
			}
			entry.Receipt = mustProviderRecoveryJSON(receipt)
			e := &environmentCreateExecution{Journal: environmentCreateJournal{Environment: "dev", Entries: []environmentCreateJournalEntry{entry}}}
			if name == "missing" {
				e.Journal.Entries = nil
			}
			got, err := managedLateContextAfterFinalization(ctx, environmentCreateBootstrapRuntime{Execution: e, ServiceSessionRecovery: r})
			if name == "completed" {
				if err != nil || got != overall {
					t.Fatal("genuine native finalizer did not restore original overall deadline", err)
				}
			} else if err == nil {
				t.Fatal("unfinalized authority restored overall execution")
			}
		})
	}
	plain := context.Background()
	if got, err := managedLateContextAfterFinalization(plain, environmentCreateBootstrapRuntime{}); err != nil || got != plain {
		t.Fatal("unrelated recovery profile changed")
	}
}

func managedLatePostMintBudgetFixture() *createManagedLatePostMint {
	started := time.Now().UTC().Add(-10 * time.Minute)
	expires := started.Add(8 * time.Hour)
	post := &createManagedLatePostMint{InstallationPolicy: managedLateInstallationPolicy, Review: strings.Repeat("a", 64), SourceSHA: strings.Repeat("b", 64), SourceOwnerSHA: strings.Repeat("c", 64), Fingerprint: strings.Repeat("d", 64), SessionExpires: expires, LeafNotAfter: expires, Ceremony: environmentCreateServiceCeremony{BootstrapSessionID: "00000000-0000-4000-8000-000000000002"}}
	post.InstallationClock = &createManagedLateInstallationClock{Schema: managedLateInstallationClockSchema, Parent: strings.Repeat("e", 64), Review: post.Review, SourceSHA: post.SourceSHA, OwnerSHA: post.SourceOwnerSHA, CheckerSHA: strings.Repeat("f", 64), SessionID: post.Ceremony.BootstrapSessionID, Fingerprint: post.Fingerprint, DescriptorSHA: managedLatePostMintDigest(post), QualificationSHA: strings.Repeat("1", 64), StartedAt: started, Deadline: started.Add(managedLateOverallLimit), OverallDeadline: started.Add(managedLateOverallLimit), SessionExpires: expires, LeafNotAfter: expires}
	return post
}

func TestManagedLatePostMintBudgetPreservesAdmittedClock(t *testing.T) {
	for _, mode := range []string{"maximum", "original caller shortened", "authority shortened", "retry caller shortened"} {
		t.Run(mode, func(t *testing.T) {
			post := managedLatePostMintBudgetFixture()
			clock := post.InstallationClock
			if mode == "original caller shortened" {
				clock.OverallDeadline = clock.StartedAt.Add(60 * time.Minute)
			}
			if mode == "authority shortened" {
				post.SessionExpires = clock.StartedAt.Add(65 * time.Minute)
				post.LeafNotAfter = post.SessionExpires
				clock.SessionExpires, clock.LeafNotAfter = post.SessionExpires, post.LeafNotAfter
				clock.DescriptorSHA = managedLatePostMintDigest(post)
			}
			clock.Deadline = managedLateInstallationClockDeadline(clock.StartedAt, clock.OverallDeadline, clock.LeafNotAfter, clock.SessionExpires)
			materials := managedLateBudgetMaterials()
			materials["accepted-service-core-late-post-mint-native-budget"] = managedLatePostMintBudgetPin(post)
			callerDeadline := time.Now().Add(managedLateOverallLimit)
			if mode == "retry caller shortened" {
				callerDeadline = time.Now().Add(40 * time.Minute)
			}
			overall, stop := context.WithDeadline(context.Background(), callerDeadline)
			defer stop()
			native, cancel, err := beginManagedLatePostMintBudget(overall, materials, post)
			if err != nil {
				t.Fatal(err)
			}
			defer cancel()
			budget, err := managedLateActiveBudget(native)
			deadline, ok := native.Deadline()
			wanted := clock.Deadline
			if callerDeadline.Before(wanted) {
				wanted = callerDeadline
			}
			if err != nil || !ok || !budget.StartedAt.Equal(clock.StartedAt) || !budget.Deadline.Equal(clock.Deadline) || !deadline.Equal(wanted) || !budget.PostMint || budget.InstallationClock != clock {
				t.Fatal("post-mint recovery changed its committed installation clock", err)
			}
			if overallDeadline, ok := budget.Overall.Deadline(); !ok || !overallDeadline.Equal(wanted) {
				t.Fatal("post-mint handoff extended original cutoff")
			}
			if _, _, err := beginManagedLatePostMintBudget(native, materials, post); err == nil {
				t.Fatal("nested budget reset admitted")
			}
		})
	}
}

func TestManagedLatePostMintBudgetRejectsDeadlineAndPinDrift(t *testing.T) {
	for _, name := range []string{"missing-clock", "future-start", "extended-deadline", "expired", "insufficient-finalizer", "wrong-pin", "missing-contract", "short-overall", "wrong-session", "wrong-fingerprint", "descriptor-drift", "source-drift", "legacy-start"} {
		t.Run(name, func(t *testing.T) {
			post := managedLatePostMintBudgetFixture()
			clock := post.InstallationClock
			materials := managedLateBudgetMaterials()
			materials["accepted-service-core-late-post-mint-native-budget"] = managedLatePostMintBudgetPin(post)
			switch name {
			case "missing-clock":
				post.InstallationClock = nil
			case "future-start":
				clock.StartedAt = time.Now().Add(time.Minute)
				clock.OverallDeadline = clock.StartedAt.Add(managedLateOverallLimit)
				clock.Deadline = clock.OverallDeadline
			case "extended-deadline":
				clock.Deadline = clock.Deadline.Add(time.Minute)
			case "expired", "insufficient-finalizer":
				elapsed := 2 * time.Hour
				if name == "insufficient-finalizer" {
					elapsed = 76 * time.Minute
				}
				clock.StartedAt = time.Now().Add(-elapsed)
				clock.OverallDeadline = clock.StartedAt.Add(managedLateOverallLimit)
				clock.Deadline = clock.OverallDeadline
			case "wrong-pin":
				materials["accepted-service-core-late-post-mint-native-budget"] = deploymentplan.Digest("foreign")
			case "missing-contract":
				delete(materials, "accepted-service-core-late-budget-native")
			case "wrong-session":
				clock.SessionID = "00000000-0000-4000-8000-000000000003"
			case "wrong-fingerprint":
				clock.Fingerprint = strings.Repeat("2", 64)
			case "descriptor-drift":
				clock.DescriptorSHA = strings.Repeat("2", 64)
			case "source-drift":
				clock.SourceSHA = strings.Repeat("2", 64)
			case "legacy-start":
				post.NativeStartedAt = clock.StartedAt
			}
			limit := managedLateOverallLimit
			if name == "short-overall" {
				limit = time.Minute
			}
			ctx, cancel := context.WithTimeout(context.Background(), limit)
			defer cancel()
			if native, stop, err := beginManagedLatePostMintBudget(ctx, materials, post); err == nil {
				stop()
				t.Fatal("unsafe post-mint budget admitted", native)
			}
		})
	}
}

func TestManagedLatePostMintLimitCannotBroadenOrdinaryBudget(t *testing.T) {
	started := time.Now().UTC().Add(-time.Minute)
	overall, cancel := context.WithTimeout(context.Background(), managedLateOverallLimit)
	defer cancel()
	for _, budget := range []managedLateRuntimeBudget{
		{StartedAt: started, Deadline: started.Add(managedLateOverallLimit), Overall: overall, Limit: managedLateOverallLimit},
		{StartedAt: started, Deadline: started.Add(managedLateOverallLimit + time.Minute), Overall: overall, Limit: managedLateOverallLimit + time.Minute, PostMint: true},
		{StartedAt: started, Deadline: started.Add(managedLateNativeLimit), Overall: overall, Limit: managedLateNativeLimit, PostMint: true},
	} {
		ctx, stop := context.WithDeadline(context.WithValue(overall, managedLateBudgetKey{}, budget), budget.Deadline)
		if _, err := managedLateActiveBudget(ctx); err == nil {
			stop()
			t.Fatal("ordinary or foreign profile acquired post-mint cap")
		}
		stop()
	}
}
