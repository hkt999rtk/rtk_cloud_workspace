package main

import (
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
	"time"
)

func TestManagedLateRedisSupplementBindsIntentBeforeAuthority(t *testing.T) {
	oldStart := time.Now().UTC().Add(-100 * time.Minute)
	old := &createManagedLateNormalTailAuthority{StartedAt: oldStart, Deadline: oldStart.Add(90 * time.Minute)}
	oldSHA := deploymentplan.Digest(old)
	approval := &createManagedLateNormalTailSupplementApproval{ProposalSHA: strings.Repeat("a", 64), ApprovalSHA: strings.Repeat("b", 64), ParentSHA: strings.Repeat("c", 64), SourceSHA: strings.Repeat("d", 64), OriginalAuthoritySHA: oldSHA, OriginalStartedAt: old.StartedAt, OriginalDeadline: old.Deadline, ExtendedDeadline: old.StartedAt.Add(180 * time.Minute)}
	r := &createServiceSessionRecovery{normalTail: &createManagedLateNormalTail{Authority: old}, normalSupplement: &createManagedLateNormalTailSupplement{Approval: approval, CredentialSHA: strings.Repeat("e", 64)}}
	intent := managedLateNormalTailSupplementIntent(r)
	d := &createManagedLateRedisDurable{Schema: managedLateRedisDurableSchema, ParentSHA: approval.ParentSHA, SourceSHA: approval.SourceSHA, NormalAuthoritySHA: oldSHA, SupplementIntentSHA: intent}
	qualified := managedLateRedisDurableDigest(d)
	if managedLateNormalTailSupplementAuthoritySHA(r) != "" || !managedLateNormalTailEffectiveDeadline(r).Equal(approval.ExtendedDeadline) {
		t.Fatal("read-only intent invented an authority or restarted the anchor")
	}
	// The first fresh admission supplies a separate immutable authority pin. It
	// cannot alter the already qualified migration intent or the original90.
	r.normalSupplement.Authority = &createManagedLateNormalTailSupplementAuthority{IntentSHA: intent, OriginalAuthoritySHA: oldSHA, OriginalStartedAt: oldStart, OriginalDeadline: old.Deadline, ExtendedDeadline: approval.ExtendedDeadline, Deadline: approval.ExtendedDeadline}
	actual := managedLateNormalTailSupplementAuthoritySHA(r)
	if actual == "" || managedLateNormalTailSupplementIntent(r) != intent || managedLateRedisDurableDigest(d) != qualified || deploymentplan.Digest(old) != oldSHA {
		t.Fatal("admission mutated qualified intent or original authority")
	}
	saved := managedLateRedisDurableSaved{Descriptor: *d, SupplementAuthoritySHA: actual}
	complete := managedLateRedisDurableReceipt{DescriptorSHA: qualified, NormalAuthoritySHA: oldSHA, SupplementAuthoritySHA: actual}
	if saved.SupplementAuthoritySHA != complete.SupplementAuthoritySHA || saved.Descriptor.NormalAuthoritySHA != complete.NormalAuthoritySHA {
		t.Fatal("saved source/completion lost separate authority links")
	}
	before := intent
	approval.ApprovalSHA = strings.Repeat("f", 64)
	if managedLateNormalTailSupplementIntent(r) == before {
		t.Fatal("changed human approval kept qualified intent")
	}
	ordinary := &createServiceSessionRecovery{normalTail: &createManagedLateNormalTail{Authority: old}}
	if !managedLateNormalTailEffectiveDeadline(ordinary).Equal(old.Deadline) || managedLateNormalTailSupplementIntent(ordinary) != "" || managedLateNormalTailSupplementAuthoritySHA(ordinary) != "" {
		t.Fatal("ordinary90 acquired supplemental time or proof")
	}
}
