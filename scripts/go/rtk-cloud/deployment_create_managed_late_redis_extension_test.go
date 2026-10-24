package main

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestManagedLateRedisExtensionKeepsOriginalAndSupplementHistory(t *testing.T) {
	start := time.Now().UTC().Add(-200 * time.Minute)
	original := &createManagedLateNormalTailAuthority{StartedAt: start, Deadline: start.Add(90 * time.Minute)}
	base := &createManagedLateNormalTailSupplementAuthority{OriginalAuthoritySHA: deploymentplan.Digest(original), OriginalStartedAt: start, OriginalDeadline: original.Deadline, ExtendedDeadline: start.Add(180 * time.Minute), Deadline: start.Add(180 * time.Minute)}
	r := &createServiceSessionRecovery{normalTail: &createManagedLateNormalTail{Authority: original}, normalSupplement: &createManagedLateNormalTailSupplement{Approval: &createManagedLateNormalTailSupplementApproval{OriginalAuthoritySHA: deploymentplan.Digest(original), OriginalStartedAt: start, OriginalDeadline: original.Deadline, ExtendedDeadline: base.Deadline}, Authority: base}}
	originalRaw, _ := json.Marshal(original)
	baseRaw, _ := json.Marshal(base)
	oldIntent := managedLateNormalTailSupplementIntent(r)
	x := &createManagedLateNormalTailExtension{Approval: &createManagedLateNormalTailExtensionApproval{ProposalSHA: strings.Repeat("a", 64), ApprovalSHA: strings.Repeat("b", 64), OriginalAuthoritySHA: deploymentplan.Digest(original), BaseAuthoritySHA: deploymentplan.Digest(base), OriginalStartedAt: start, BaseDeadline: base.Deadline, ExtendedDeadline: start.Add(360 * time.Minute)}, CredentialSHA: strings.Repeat("c", 64)}
	r.normalSupplement.Extension = x
	intent := managedLateNormalTailExtensionIntent(r)
	d := &createManagedLateRedisDurable{Schema: managedLateRedisDurableSchema, NormalAuthoritySHA: deploymentplan.Digest(original), SupplementIntentSHA: oldIntent, ExtensionIntentSHA: intent}
	qualified := managedLateRedisDurableDigest(d)
	if intent == "" || managedLateNormalTailSupplementIntent(r) != oldIntent || managedLateNormalTailExtensionAuthoritySHA(r) != "" {
		t.Fatal("candidate intent rewrote old supplement or invented admission")
	}
	// The actual authority pin belongs to saved/completed evidence, never the
	// already qualified descriptor. This test constructs metadata only.
	x.Authority = &createManagedLateNormalTailExtensionAuthority{IntentSHA: intent, OriginalAuthoritySHA: d.NormalAuthoritySHA, BaseAuthoritySHA: deploymentplan.Digest(base), OriginalStartedAt: start, Deadline: x.Approval.ExtendedDeadline}
	authority := managedLateNormalTailExtensionAuthoritySHA(r)
	saved := managedLateRedisDurableSaved{Descriptor: *d, SupplementAuthoritySHA: deploymentplan.Digest(base), ExtensionAuthoritySHA: authority}
	completed := managedLateRedisDurableReceipt{DescriptorSHA: qualified, NormalAuthoritySHA: d.NormalAuthoritySHA, SupplementAuthoritySHA: deploymentplan.Digest(base), ExtensionAuthoritySHA: authority}
	if authority == "" || managedLateNormalTailExtensionIntent(r) != intent || managedLateRedisDurableDigest(d) != qualified || saved.ExtensionAuthoritySHA != completed.ExtensionAuthoritySHA || saved.SupplementAuthoritySHA != completed.SupplementAuthoritySHA {
		t.Fatal("admission mutated intent or merged distinct authority history")
	}
	afterOriginal, _ := json.Marshal(original)
	afterBase, _ := json.Marshal(base)
	if !bytes.Equal(originalRaw, afterOriginal) || !bytes.Equal(baseRaw, afterBase) || managedLateNormalTailSupplementIntent(r) != oldIntent {
		t.Fatal("new metadata changed literal original90 or admitted180")
	}
	x.Approval.ApprovalSHA = strings.Repeat("d", 64)
	if managedLateNormalTailExtensionIntent(r) == intent {
		t.Fatal("changed explicit approval retained the qualified Redis intent")
	}
	r.normalSupplement.Extension = nil
	if managedLateNormalTailExtensionIntent(r) != "" || managedLateNormalTailExtensionAuthoritySHA(r) != "" || !managedLateNormalTailEffectiveDeadline(r).Equal(base.Deadline) {
		t.Fatal("ordinary supplement acquired the candidate extension")
	}
}

func TestManagedLateRedisExtensionZeroFieldsPreservePriorWire(t *testing.T) {
	for _, value := range []any{createManagedLateRedisDurable{Schema: managedLateRedisDurableSchema, SupplementIntentSHA: strings.Repeat("a", 64)}, managedLateRedisDurableSaved{SupplementAuthoritySHA: strings.Repeat("b", 64)}, managedLateRedisDurableReceipt{Schema: managedLateRedisDurableSchema, SupplementAuthoritySHA: strings.Repeat("c", 64)}} {
		v := reflect.ValueOf(value)
		var fields []reflect.StructField
		var positions []int
		for i := range v.NumField() {
			field := v.Type().Field(i)
			if field.PkgPath != "" || field.Tag.Get("json") == "-" || field.Name == "ExtensionIntentSHA" || field.Name == "ExtensionAuthoritySHA" {
				continue
			}
			field.Offset, field.Index = 0, nil
			fields = append(fields, field)
			positions = append(positions, i)
		}
		old := reflect.New(reflect.StructOf(fields)).Elem()
		for i, position := range positions {
			old.Field(i).Set(v.Field(position))
		}
		before, beforeErr := json.Marshal(old.Interface())
		after, afterErr := json.Marshal(value)
		if beforeErr != nil || afterErr != nil || !bytes.Equal(before, after) {
			t.Fatalf("optional extension changed prior %T wire: %v/%v", value, beforeErr, afterErr)
		}
	}
}
