package main

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestAcceptedStageHistoryRecognizesOnlyCompletedOriginalBootstrapCopyRetirement(t *testing.T) {
	for _, mode := range []string{"complete", "pending", "wrong-uid", "wrong-session", "replacement", "unrecorded"} {
		t.Run(mode, func(t *testing.T) {
			store, e, fixture, b := freshStageFixture(t)
			session := "33333333-3333-4333-8333-333333333333"
			ns := e.Journal.Stack + "-account-manager"
			object := deploymentplan.Object{"apiVersion": "v1", "kind": "Secret", "metadata": deploymentplan.Object{"name": "service-bootstrap-" + session, "namespace": ns, "labels": deploymentplan.Object{"rtk.realtek.com/stack": e.Journal.Stack, "rtk.cloud/bootstrap-session": session}}, "type": "Opaque", "data": deploymentplan.Object{"fixture": "cHVibGljLWZpeHR1cmU="}}
			p, err := deploymentplan.Compile(fixture.Inputs, []deploymentplan.Definition{{Object: object, Phase: "configuration"}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			check := func(context.Context, *deploymentplan.Plan) error { return e.Check(t.Context()) }
			if err = executeEnvironmentCreateStagePlan(t.Context(), store, e, "copy-source", p, b, check, check, check, func(context.Context) error { return nil }, io.Discard); err != nil {
				t.Fatal(err)
			}
			id := deploymentplan.ID(object)
			uid := deploymentplan.String(deploymentplan.Map(b.objects[id]["metadata"])["uid"])
			if mode != "unrecorded" {
				intentUID, intentSession := uid, session
				if mode == "wrong-uid" {
					intentUID = "99999999-9999-4999-8999-999999999999"
				}
				if mode == "wrong-session" {
					intentSession = "44444444-4444-4444-8444-444444444444"
				}
				key := "bootstrap-copy-retire-" + ns + "-" + session
				if err = e.BeginEntry(key, "bootstrap", mustProviderRecoveryJSON(deploymentplan.Object{"resource_id": id, "uid": intentUID, "session_id": intentSession}), time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
				if mode != "pending" {
					if err = e.RecordReceipt(key, mustProviderRecoveryJSON(deploymentplan.Object{"resource_id": id, "uid": intentUID, "absent": true})); err != nil {
						t.Fatal(err)
					}
					if err = e.CompleteEntry(key, time.Now().UTC()); err != nil {
						t.Fatal(err)
					}
				}
			}
			delete(b.objects, id)
			if mode == "replacement" {
				b.objects[id] = deploymentplan.Clone(object)
				deploymentplan.Map(b.objects[id]["metadata"])["uid"] = strings.Repeat("9", 36)
			}
			before := p.SHA256()
			_, err = verifyAcceptedStageAfterRecordedHistory(t.Context(), store, e, b, "kubernetes/copy-source")
			if (err == nil) != (mode == "complete") {
				t.Fatal("completed original retirement or protected failure misclassified", mode, err)
			}
			if p.SHA256() != before {
				t.Fatal("historical source plan rewritten")
			}
		})
	}
}
