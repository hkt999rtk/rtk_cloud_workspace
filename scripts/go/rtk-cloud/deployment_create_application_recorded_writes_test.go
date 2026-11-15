package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCompletedApplicationOwnerWritesPreserveOriginalOwnerChain(t *testing.T) {
	for _, mode := range []string{"valid", "accepted", "accepted-allocated", "partial-prefix", "changed-baseline", "changed-owner", "changed-live", "missing-child-write"} {
		t.Run(mode, func(t *testing.T) {
			store, e, prior, backend := freshStageFixture(t)
			check := func(context.Context, *deploymentplan.Plan) error { return e.Check(t.Context()) }
			if err := executeEnvironmentCreateStagePlan(t.Context(), store, e, "prior", prior, backend, check, check, check, func(context.Context) error { return nil }, io.Discard); err != nil {
				t.Fatal(err)
			}
			_, priorJournal, err := acceptedCreateStage(store, e, "kubernetes/prior")
			if err != nil {
				t.Fatal(err)
			}
			id := prior.Resources[0].ID
			before := deploymentplan.Clone(backend.objects[id])
			after := deploymentplan.Clone(before)
			deploymentplan.Map(after["data"])["root"] = "approved-successor"
			defs := []deploymentplan.Definition{{Object: after, Phase: "configuration"}}
			if mode == "partial-prefix" {
				other := deploymentplan.Clone(after)
				deploymentplan.Map(other["metadata"])["name"] = "zz-pending"
				delete(deploymentplan.Map(other["metadata"]), "uid")
				delete(deploymentplan.Map(other["metadata"]), "resourceVersion")
				defs = append(defs, deploymentplan.Definition{Object: other, Phase: "configuration"})
			}
			latest, err := deploymentplan.Compile(prior.Inputs, defs, []deploymentplan.Object{before})
			if err != nil || latest.Resources[0].Action != "update" {
				t.Fatal("failed to compile original update", err)
			}
			if mode != "accepted" && mode != "accepted-allocated" {
				backend.failure = "after-write-configuration"
			}
			capture := func(context.Context) error {
				if mode == "accepted-allocated" {
					return errors.New("isolated parent receipt interruption")
				}
				return nil
			}
			err = executeEnvironmentCreateStagePlan(t.Context(), store, e, "managed-application-owners", latest, backend, check, check, check, capture, io.Discard)
			if (err == nil) != (mode == "accepted") {
				t.Fatal("unexpected native execution result", err)
			}
			journal, ok, err := latest.ReadExecution(store.Root)
			if err != nil || !ok || mode != "accepted" && mode != "accepted-allocated" && journal.Failed == "" {
				t.Fatal("no original child", err)
			}
			if mode != "accepted" && mode != "accepted-allocated" {
				journal.Completed = []string{id}
			}
			if mode == "partial-prefix" {
				journal.Failed = ""
			}
			raw, _ := json.Marshal(journal)
			path := filepath.Join(store.Root, "deployment", "rollback", latest.SHA256(), "journal.json")
			if err = os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "changed-baseline":
				deploymentplan.Map(prior.Resources[0].Desired["data"])["root"] = "unrecorded-prior"
			case "changed-owner":
				deploymentplan.Map(backend.objects[id]["metadata"])["uid"] = "replacement"
			case "changed-live":
				deploymentplan.Map(backend.objects[id]["data"])["root"] = "unrecorded-live"
			case "missing-child-write":
				journal.Completed = nil
				raw, _ = json.Marshal(journal)
				if err = os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			writes, err := completedApplicationOwnerWrites(t.Context(), store, e, backend)
			if err == nil {
				err = verifyAcceptedStageAfterRecordedWrites(t.Context(), prior, priorJournal, backend.Read, writes)
			}
			if (err == nil) != (mode == "valid" || mode == "accepted" || mode == "accepted-allocated" || mode == "partial-prefix") {
				t.Fatalf("mode %s: %v", mode, err)
			}
			current, readErr := os.ReadFile(path)
			if readErr != nil || string(current) != string(raw) {
				t.Fatal("qualification changed original journal", readErr)
			}
			if (mode == "valid" || mode == "accepted" || mode == "accepted-allocated" || mode == "partial-prefix") && (len(writes) != 1 || backend.writes[id] != 2) {
				t.Fatal("original resource was replayed")
			}
		})
	}
}

func TestApplicationWriteUsesOnlyAuthenticatedInterveningSuccessor(t *testing.T) {
	obj := func(value string) deploymentplan.Object {
		return deploymentplan.Object{"kind": "ConfigMap", "apiVersion": "v1", "metadata": deploymentplan.Object{"name": "original", "namespace": "test"}, "data": deploymentplan.Object{"root": value}}
	}
	id := "ConfigMap/test/original"
	prior := deploymentplan.Resource{ID: id, Desired: obj("old")}
	entry := environmentCreateJournalEntry{Key: "kubernetes/earlier", Phase: "earlier", State: "completed"}
	source := environmentCreateJournal{Entries: []environmentCreateJournalEntry{entry}}
	write := activeTransportRenewalResource{ID: id, UID: "original-uid", Before: obj("new-root"), Desired: obj("latest")}
	next := activeTransportRenewalResource{ID: id, UID: "original-uid", Before: obj("old"), Desired: obj("new-root")}
	if !applicationWriteMatchesSuccessor(prior, entry, write, []activeTransportRenewalResource{next}, source) {
		t.Fatal("authenticated intervening write rejected")
	}
	for _, mode := range []string{"wrong-uid", "wrong-before", "wrong-successor", "missing-source"} {
		changed, snapshot := next, source
		switch mode {
		case "wrong-uid":
			changed.UID = "replacement"
		case "wrong-before":
			changed.Before = obj("other")
		case "wrong-successor":
			changed.Desired = obj("other")
		case "missing-source":
			snapshot.Entries = nil
		}
		if applicationWriteMatchesSuccessor(prior, entry, write, []activeTransportRenewalResource{changed}, snapshot) {
			t.Fatalf("accepted %s", mode)
		}
	}
}
