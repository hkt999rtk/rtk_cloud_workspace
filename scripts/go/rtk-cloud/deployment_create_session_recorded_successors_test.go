package main

import (
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestSessionRecordedSuccessorRequiresCompletedAdmittedReceipt(t *testing.T) {
	store, e, plan, _ := freshStageFixture(t)
	resource := plan.Resources[0]
	base := []activeTransportRenewalResource{sessionRecordedOwnerBase(resource.ID, "original-uid", resource.Desired)}
	got, err := completedSessionRecordedSuccessors(t.Context(), store, e, base)
	if err != nil || len(got) != 1 || !deploymentplan.Matches(base[0].Desired, got[0].Desired) {
		t.Fatal("missing session must preserve original", err)
	}
	key := "service-session-batches-" + strings.Repeat("a", 64)
	now := time.Now().UTC()
	if err = e.BeginEntry(key, "service-session-batches", []byte(`{"fixture":true}`), now); err != nil {
		t.Fatal(err)
	}
	if _, err = completedSessionRecordedSuccessors(t.Context(), store, e, base); err == nil {
		t.Fatal("unadmitted pending session accepted")
	}
	if err = e.RecordReceipt(key, []byte(`{"fixture":true}`)); err != nil {
		t.Fatal(err)
	}
	if err = e.CompleteEntry(key, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err = completedSessionRecordedSuccessors(t.Context(), store, e, base); err == nil {
		t.Fatal("unauthenticated completed session accepted")
	}
}
