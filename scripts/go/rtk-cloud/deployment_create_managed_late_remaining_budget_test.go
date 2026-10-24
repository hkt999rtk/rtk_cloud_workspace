package main

import (
	"context"
	"testing"
	"time"
)

func TestManagedLateRemainingBudgetRetainsSignedLeafAbsoluteBound(t *testing.T) {
	r, a, now := managedLateRemainingNegativeFixture(t)
	var err error
	r.Authority, err = newManagedLateRemainingAuthority(r, a, now.Add(90*time.Minute), now, environmentPreflightBytesSHA256(mustProviderRecoveryJSON(a)))
	if err != nil {
		t.Fatal(err)
	}
	before := *r.Authority
	for _, duration := range []time.Duration{90 * time.Minute, 50 * time.Minute} {
		overall, cancel := context.WithDeadline(context.Background(), now.Add(duration))
		ctx, stop, err := beginManagedLateRemainingBudget(overall, managedLateBudgetMaterials(), r)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		budget, err := managedLateActiveBudget(ctx)
		if err != nil || !budget.StartedAt.Equal(r.NotBefore) || !budget.Deadline.Equal(before.Deadline) || budget.Limit != managedLateRemainingLimit || budget.PostMint || budget.InstallationClock != nil || budget.RemainingAuthority != r.Authority {
			t.Fatal("remaining operation reset or borrowed the first-install clock", err)
		}
		if err := verifyManagedLateSeedBudget(ctx, r.LeafNotAfter, r.SessionExpires); err != nil {
			t.Fatal(err)
		}
		if err := verifyManagedLateFinalizerBudget(ctx); err != nil {
			t.Fatal(err)
		}
		deadline, _ := ctx.Deadline()
		if deadline.After(before.Deadline) || deadline.After(now.Add(duration)) {
			t.Fatal("caller or signed-leaf bound extended")
		}
		stop()
		cancel()
	}
	if *r.Authority != before {
		t.Fatal("retry changed immutable remaining authority")
	}
	for _, mode := range []string{"source", "owner", "fingerprint", "epoch", "mixed-installation-clock"} {
		t.Run(mode, func(t *testing.T) {
			copy := *r
			authority := before
			copy.Authority = &authority
			switch mode {
			case "source":
				authority.SourceSHA = r.OwnerSHA
			case "owner":
				authority.OwnerSHA = r.SourceSHA
			case "fingerprint":
				authority.Fingerprint = r.KeySHA
			case "epoch":
				authority.StartedAt = authority.StartedAt.Add(time.Second)
			case "mixed-installation-clock":
				if managedLateRuntimeBudgetValid(managedLateRuntimeBudget{PostMint: true, RemainingAuthority: &authority}) {
					t.Fatal("mixed policy clocks accepted")
				}
				return
			}
			ctx, cancel := context.WithDeadline(context.Background(), now.Add(90*time.Minute))
			defer cancel()
			if _, stop, err := beginManagedLateRemainingBudget(ctx, managedLateBudgetMaterials(), &copy); err == nil {
				stop()
				t.Fatal("changed remaining clock accepted")
			}
		})
	}
}
