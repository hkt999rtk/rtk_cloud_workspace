package main

import (
	"context"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func plannedEnvelopeStatefulSet(name string, replicas float64, templateNames ...string) deploymentplan.Object {
	templates := []any{}
	for _, name := range templateNames {
		templates = append(templates, deploymentplan.Object{"metadata": deploymentplan.Object{"name": name}, "spec": deploymentplan.Object{"accessModes": []any{"ReadWriteOnce"}, "storageClassName": "linode-block-storage-retain", "resources": deploymentplan.Object{"requests": deploymentplan.Object{"storage": "5Gi"}}}})
	}
	return deploymentplan.Object{"apiVersion": "apps/v1", "kind": "StatefulSet", "metadata": deploymentplan.Object{"namespace": "video-cloud-dev-platform", "name": name, "labels": deploymentplan.Object{"rtk.realtek.com/stack": "video-cloud-dev", "app.kubernetes.io/managed-by": "rtk-cloud"}}, "spec": deploymentplan.Object{"replicas": replicas, "serviceName": name, "selector": deploymentplan.Object{"matchLabels": deploymentplan.Object{"app": name}}, "template": deploymentplan.Object{"metadata": deploymentplan.Object{"labels": deploymentplan.Object{"app": name}}, "spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": name, "image": "docker.io/library/postgres@sha256:" + strings.Repeat("a", 64)}}}}, "volumeClaimTemplates": templates}}
}

func plannedEnvelopeNativeTemplates(object deploymentplan.Object) {
	for _, value := range certIssuerObjectList(deploymentplan.Map(object["spec"])["volumeClaimTemplates"]) {
		template := deploymentplan.Map(value)
		template["apiVersion"], template["kind"] = "v1", "PersistentVolumeClaim"
		template["status"] = deploymentplan.Object{"phase": "Pending"}
		deploymentplan.Map(template["metadata"])["creationTimestamp"] = nil
	}
}

func TestManagedLatePlannedVolumeClaimTemplatesPreserveExactAllocationIntent(t *testing.T) {
	for _, mode := range []string{"native-envelope", "both-explicit", "name", "storage-class", "capacity", "access-mode", "labels", "uid", "foreign-version", "foreign-kind", "explicit-null-version", "removed-template", "new-template"} {
		t.Run(mode, func(t *testing.T) {
			desired := plannedEnvelopeStatefulSet("postgresql", 1, "data")
			baseline := deploymentplan.Clone(desired)
			plannedEnvelopeNativeTemplates(baseline)
			want := certIssuerObjectList(deploymentplan.Map(desired["spec"])["volumeClaimTemplates"])
			old := certIssuerObjectList(deploymentplan.Map(baseline["spec"])["volumeClaimTemplates"])
			template := deploymentplan.Map(old[0])
			switch mode {
			case "both-explicit":
				plannedEnvelopeNativeTemplates(desired)
			case "name":
				deploymentplan.Map(template["metadata"])["name"] = "foreign"
			case "storage-class":
				deploymentplan.Map(template["spec"])["storageClassName"] = "foreign"
			case "capacity":
				deploymentplan.Map(deploymentplan.Map(template["spec"])["resources"])["requests"] = deploymentplan.Object{"storage": "99Gi"}
			case "access-mode":
				deploymentplan.Map(template["spec"])["accessModes"] = []any{"ReadWriteMany"}
			case "labels":
				deploymentplan.Map(template["metadata"])["labels"] = deploymentplan.Object{"owner": "foreign"}
			case "uid":
				deploymentplan.Map(template["metadata"])["uid"] = "foreign"
			case "foreign-version":
				template["apiVersion"] = "foreign/v1"
			case "foreign-kind":
				template["kind"] = "Secret"
			case "explicit-null-version":
				template["apiVersion"] = nil
			case "removed-template":
				old = nil
			case "new-template":
				old = append(old, deploymentplan.Clone(template))
			}
			before := deploymentplan.Digest([]any{old, want})
			got := managedLateVolumeClaimTemplatesEqual(old, want)
			if got != (mode == "native-envelope" || mode == "both-explicit") || deploymentplan.Digest([]any{old, want}) != before {
				t.Fatal("template comparison accepted changed allocation intent or mutated original inputs")
			}
		})
	}
}

func TestManagedLatePlannedVolumeGraphIncludesEveryMaintainedStatefulSetVariant(t *testing.T) {
	p := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev"}}
	for _, entry := range []struct {
		name          string
		old, replicas float64
		templates     []string
		create        bool
	}{
		{"postgresql", 1, 1, []string{"data"}, false},
		{"openbao", 1, 1, []string{"data", "audit"}, false},
		{"zammad-elasticsearch-master", 0, 1, []string{"data"}, true},
		{"zammad-postgres", 0, 1, []string{"data"}, true},
		{"fleet-valkey", 0, 1, []string{"data"}, true},
		{"scaled-db", 1, 3, []string{"data", "audit"}, false},
	} {
		desired := plannedEnvelopeStatefulSet(entry.name, entry.replicas, entry.templates...)
		resource := deploymentplan.Resource{ID: deploymentplan.ID(desired), Desired: desired, Action: "create"}
		if entry.create {
			if entry.name == "zammad-elasticsearch-master" {
				plannedEnvelopeNativeTemplates(desired)
			}
		} else {
			resource.Action, resource.UID = "update", "00000001-1111-4111-8111-111111111111"
			resource.Baseline = deploymentplan.Clone(desired)
			deploymentplan.Map(resource.Baseline["spec"])["replicas"] = entry.old
			plannedEnvelopeNativeTemplates(resource.Baseline)
		}
		p.Resources = append(p.Resources, resource)
	}
	before := p.SHA256()
	claims, err := managedLatePlannedVolumeClaims(p)
	if err != nil || len(claims) != 7 || p.SHA256() != before {
		t.Fatal("frozen graph failed or changed", err)
	}
	for _, suffix := range []string{"data-zammad-elasticsearch-master-0", "data-zammad-postgres-0", "data-fleet-valkey-0", "data-scaled-db-1", "audit-scaled-db-1", "data-scaled-db-2", "audit-scaled-db-2"} {
		if _, ok := claims["PersistentVolumeClaim/video-cloud-dev-platform/"+suffix]; !ok {
			t.Fatal("missing exact planned allocation", suffix)
		}
	}
	for _, mode := range []string{"foreign-template", "foreign-owner", "ordinals", "fractional", "negative", "over-limit"} {
		t.Run(mode, func(t *testing.T) {
			q := *p
			q.Resources = slices.Clone(p.Resources)
			q.Resources[0].Desired = deploymentplan.Clone(p.Resources[0].Desired)
			spec := deploymentplan.Map(q.Resources[0].Desired["spec"])
			switch mode {
			case "foreign-template":
				deploymentplan.Map(deploymentplan.Map(certIssuerObjectList(spec["volumeClaimTemplates"])[0])["spec"])["storageClassName"] = "foreign"
			case "foreign-owner":
				q.Resources[0].UID = "foreign"
			case "ordinals":
				spec["ordinals"] = deploymentplan.Object{"start": float64(1)}
			case "fractional":
				spec["replicas"] = float64(1.5)
			case "negative":
				spec["replicas"] = float64(-1)
			case "over-limit":
				spec["replicas"] = float64(10001)
			}
			if _, err := managedLatePlannedVolumeClaims(&q); err == nil {
				t.Fatal("invalid frozen allocation graph accepted")
			}
		})
	}
}

type plannedEnvelopeWaitBackend struct {
	*normalTailE2EBackend
	plan          *deploymentplan.Plan
	failCapture   bool
	postgresWaits int
}

func (b *plannedEnvelopeWaitBackend) Wait(ctx context.Context, r deploymentplan.Resource) error {
	if err := b.normalTailE2EBackend.Wait(ctx, r); err != nil {
		return err
	}
	if deploymentplan.Map(r.Desired["metadata"])["name"] == "postgresql" {
		b.postgresWaits++
		if b.failCapture {
			b.failCapture = false
			return createOpenBaoACKBlock("planned-volume-graph")
		}
	}
	_, _, err := managedLateVolumeStageClaims([]createNormalTailVolumeStage{{Stage: "workload", Plan: b.plan}})
	return err
}

func TestManagedLatePlannedVolumesResumeKnownPostgresWaitWithoutAnotherWrite(t *testing.T) {
	for _, mode := range []string{"resume", "replaced-owner", "changed-body"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, operator, artifact := createStateFixture(t, "dev")
			e, err := startEnvironmentCreateExecution(t.Context(), cfg, artifact, operator, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			h := &createNormalTailHandoff{Execution: e, Pins: map[string]string{"terminal": deploymentplan.Digest("original bootstrap")}}
			postgres := plannedEnvelopeStatefulSet("postgresql", 1, "data")
			baseline := deploymentplan.Clone(postgres)
			plannedEnvelopeNativeTemplates(baseline)
			deploymentplan.Map(baseline["metadata"])["uid"] = "00000001-1111-4111-8111-111111111111"
			deploymentplan.Map(baseline["metadata"])["resourceVersion"] = "1"
			podSpec := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(postgres["spec"])["template"])["spec"])
			deploymentplan.Map(certIssuerObjectList(podSpec["containers"])[0])["image"] = "docker.io/library/postgres@sha256:" + strings.Repeat("b", 64)
			fleet := plannedEnvelopeStatefulSet("fleet-valkey", 1, "data")
			p, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: e.Journal.Stack, Operation: "create", ConfigurationSHA256: deploymentplan.Digest(cfg), ReleaseSHA256: deploymentplan.Digest("frozen release"), BindingsSHA256: deploymentplan.Digest("original bindings"), CreateParentSHA256: e.Journal.PlanSHA256, CreateHandoffSHA256: deploymentplan.Digest(h.Pins)}, []deploymentplan.Definition{{Object: postgres, Phase: "configuration"}, {Object: fleet, Phase: "workload", Dependencies: []string{deploymentplan.ID(postgres)}}}, []deploymentplan.Object{baseline})
			if err != nil {
				t.Fatal(err)
			}
			b := &plannedEnvelopeWaitBackend{normalTailE2EBackend: &normalTailE2EBackend{e: e, objects: map[string]deploymentplan.Object{deploymentplan.ID(baseline): baseline}, writes: map[string]int{}}, plan: p, failCapture: true}
			check := func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }
			run := func() error {
				return executeCreateNormalTailWorkloadBackend(t.Context(), store, h, p, b, check, check, check, io.Discard, func(context.Context) error { return nil })
			}
			if err := run(); err == nil {
				t.Fatal("post-Wait capture failure missing")
			}
			j, exists, err := p.ReadExecution(store.Root)
			if err != nil || !exists || len(j.Completed) != 0 || j.InFlight != "" || j.Owners[p.Resources[0].ID] != p.Resources[0].UID || b.writes[p.Resources[0].ID] != 1 {
				t.Fatal("genuine successful write lost before failed capture", err)
			}
			switch mode {
			case "replaced-owner":
				deploymentplan.Map(b.objects[p.Resources[0].ID]["metadata"])["uid"] = "00000002-2222-4222-8222-222222222222"
			case "changed-body":
				deploymentplan.Map(b.objects[p.Resources[0].ID]["spec"])["replicas"] = float64(9)
			}
			err = run()
			if mode != "resume" {
				if err == nil || b.writes[p.Resources[0].ID] != 1 || b.writes[p.Resources[1].ID] != 0 {
					t.Fatal("replaced owner/body was adopted or rewritten", err)
				}
				return
			}
			j, exists, readErr := p.ReadExecution(store.Root)
			if err != nil || readErr != nil || !exists || !j.Accepted || len(j.Completed) != 2 || b.writes[p.Resources[0].ID] != 1 || b.writes[p.Resources[1].ID] != 1 || b.postgresWaits < 2 {
				t.Fatal("known successful write did not resume through capture/remaining stage", err, readErr)
			}
		})
	}
}
