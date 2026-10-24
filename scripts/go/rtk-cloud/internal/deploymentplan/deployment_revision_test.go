package deploymentplan

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func revisionAnnotations(object Object) Object {
	metadata := Map(object["metadata"])
	if Map(metadata["annotations"]) == nil {
		metadata["annotations"] = Object{}
	}
	return Map(metadata["annotations"])
}

func TestDeploymentRevisionNormalizationPreservesEveryOtherDeclaredField(t *testing.T) {
	for _, mode := range []string{"increment", "appeared", "omitted", "user-annotation", "template-annotation", "image", "secret-ref", "replacement", "statefulset", "daemonset", "other-api"} {
		t.Run(mode, func(t *testing.T) {
			_, d, current := fixture(t, "dev", "Deployment")
			want := Clone(current)
			delete(want, "status")
			revisionAnnotations(want)[deploymentRevisionAnnotation] = "7"
			revisionAnnotations(current)[deploymentRevisionAnnotation] = "8"
			revisionAnnotations(want)["rtk.cloud/reviewed"] = "original"
			revisionAnnotations(current)["rtk.cloud/reviewed"] = "original"
			pod := Map(Map(Map(want["spec"])["template"])["spec"])
			Map(pod["containers"].([]any)[0])["env"] = []any{Object{"name": "DATABASE_URL", "valueFrom": Object{"secretKeyRef": Object{"name": "original-secret", "key": "url"}}}}
			Map(Map(Map(current["spec"])["template"])["spec"])["containers"] = Clone(Object{"list": pod["containers"]})["list"]
			switch mode {
			case "appeared":
				delete(revisionAnnotations(want), deploymentRevisionAnnotation)
			case "omitted":
				delete(revisionAnnotations(current), deploymentRevisionAnnotation)
			case "user-annotation":
				revisionAnnotations(current)["rtk.cloud/reviewed"] = "changed"
			case "template-annotation":
				for _, o := range []Object{want, current} {
					Map(Map(o["spec"])["template"])["metadata"] = Object{"annotations": Object{deploymentRevisionAnnotation: "7"}}
				}
				Map(Map(Map(Map(current["spec"])["template"])["metadata"])["annotations"])[deploymentRevisionAnnotation] = "8"
			case "image":
				Map(Map(Map(Map(current["spec"])["template"])["spec"])["containers"].([]any)[0])["image"] = "foreign@sha256:changed"
			case "secret-ref":
				container := Map(Map(Map(Map(current["spec"])["template"])["spec"])["containers"].([]any)[0])
				Map(Map(Map(container["env"].([]any)[0])["valueFrom"])["secretKeyRef"])["name"] = "foreign-secret"
			case "replacement":
				Map(current["metadata"])["uid"] = "replacement"
			case "statefulset", "daemonset":
				kind := map[string]string{"statefulset": "StatefulSet", "daemonset": "DaemonSet"}[mode]
				want["kind"], current["kind"] = kind, kind
			case "other-api":
				want["apiVersion"], current["apiVersion"] = "extensions/v1beta1", "extensions/v1beta1"
			}
			beforeWant, beforeCurrent := Digest(want), Digest(current)
			pass := mode == "increment" || mode == "appeared" || mode == "omitted"
			resource := Resource{ID: ID(d.Object), Action: "update", UID: "owner", Desired: want}
			if (recordedResourceMatches(resource, "owner", current) == nil) != pass {
				t.Fatal("controller revision normalization changed another declared field or owner", mode)
			}
			if mode != "replacement" && (Matches(want, current) != pass || matchesAdmission(want, current) != pass || (len(admissionMismatchPaths(want, current)) == 0) != pass) {
				t.Fatal("shared desired matcher differs from recorded-write matcher", mode)
			}
			if Digest(want) != beforeWant || Digest(current) != beforeCurrent {
				t.Fatal("comparison changed original desired or native input")
			}
		})
	}
}

func TestDeploymentRevisionCompileAndFrozenBodyKeepCurrentCounter(t *testing.T) {
	in, d, current := fixture(t, "dev", "Deployment")
	d.Object = Clone(current)
	delete(d.Object, "status")
	revisionAnnotations(d.Object)[deploymentRevisionAnnotation] = "7"
	revisionAnnotations(current)[deploymentRevisionAnnotation] = "7"
	revisionAnnotations(d.Object)["rtk.cloud/reviewed"] = "original"
	revisionAnnotations(current)["rtk.cloud/reviewed"] = "original"
	Map(d.Object["spec"])["replicas"] = float64(2)
	desiredSHA, currentSHA := Digest(d.Object), Digest(current)
	// Persisted public projections keep historical fingerprint semantics.
	historical := Clone(current)
	defaultEmptyPodEnvironment(historical)
	delete(historical, "status")
	for _, key := range []string{"resourceVersion", "generation", "creationTimestamp", "managedFields"} {
		delete(Map(historical["metadata"]), key)
	}
	if Digest(Projection(current)) != Digest(historical) || revisionAnnotations(Projection(current))[deploymentRevisionAnnotation] != "7" {
		t.Fatal("public historical projection or its fingerprints changed")
	}
	p, err := Compile(in, []Definition{d}, []Object{current})
	if err != nil || len(p.Resources) != 1 || p.Resources[0].Action != "update" || p.Resources[0].UID != "owner" {
		t.Fatal("future plan lost its original update owner", err)
	}
	if Map(Map(p.Resources[0].Desired["metadata"])["annotations"])[deploymentRevisionAnnotation] != nil || Map(Map(p.Resources[0].Baseline["metadata"])["annotations"])[deploymentRevisionAnnotation] != "7" || Map(Map(p.Resources[0].Desired["metadata"])["annotations"])["rtk.cloud/reviewed"] != "original" || Digest(d.Object) != desiredSHA || Digest(current) != currentSHA {
		t.Fatal("future compile captured a server counter or changed frozen inputs")
	}
	// Reconstruct the exact old compiled wire form. It is not rewritten to the
	// new compiler output: its hash and saved evidence must remain unchanged.
	old := p.Resources[0]
	old.Desired, old.Baseline = Clone(old.Desired), Clone(old.Baseline)
	revisionAnnotations(old.Desired)[deploymentRevisionAnnotation] = "7"
	revisionAnnotations(old.Baseline)[deploymentRevisionAnnotation] = "7"
	oldSHA := Digest(old)
	revisionAnnotations(current)[deploymentRevisionAnnotation] = "8"
	body, err := old.Body(current)
	if err != nil || revisionAnnotations(body)[deploymentRevisionAnnotation] != "8" || Map(body["spec"])["replicas"] != float64(2) || revisionAnnotations(body)["rtk.cloud/reviewed"] != "original" || Digest(old) != oldSHA {
		t.Fatal("old frozen update reset the native counter, changed its input or lost declared fields", err)
	}
	foreign := Clone(current)
	Map(foreign["metadata"])["uid"] = "replacement"
	if _, err := old.Body(foreign); err == nil {
		t.Fatal("counter normalization bypassed original Body UID")
	}
	if _, err := Compile(in, []Definition{d}, []Object{foreign}); err == nil {
		t.Fatal("future compile bypassed selected original UID")
	}
}

type nativeRevisionBackend struct {
	*memoryBackend
	interrupted bool
}

func (b *nativeRevisionBackend) Wait(ctx context.Context, resource Resource) error {
	if !b.interrupted {
		b.interrupted = true
		object := b.objects[resource.ID]
		revisionAnnotations(object)[deploymentRevisionAnnotation] = "8"
		Map(object["metadata"])["generation"] = float64(2)
		object["status"] = Object{"observedGeneration": float64(2), "readyReplicas": float64(1)}
		// Native JSON readback, rather than retaining shared mutable fixture maps.
		raw, _ := json.Marshal(object)
		var readback Object
		_ = json.Unmarshal(raw, &readback)
		b.objects[resource.ID] = readback
		return errors.New("isolated interruption after controller revision update")
	}
	return b.memoryBackend.Wait(ctx, resource)
}

func TestDeploymentRevisionRealChildResumesOriginalPendingReadinessWithoutWrite(t *testing.T) {
	for _, mode := range []string{"revision-only", "user-annotation", "spec", "owner"} {
		t.Run(mode, func(t *testing.T) {
			in, d, current := fixture(t, "dev", "Deployment")
			d.Object = Clone(current)
			delete(d.Object, "status")
			revisionAnnotations(d.Object)["rtk.cloud/reviewed"] = "original"
			revisionAnnotations(current)["rtk.cloud/reviewed"] = "original"
			Map(Map(d.Object["spec"])["template"])["metadata"] = Object{"annotations": Object{"rtk.cloud/service-bundles": "new-authenticated-bundle"}}
			p, err := Compile(in, []Definition{d}, []Object{current})
			if err != nil {
				t.Fatal(err)
			}
			// Existing retained journal uses the original pre-fix plan bytes.
			revisionAnnotations(p.Resources[0].Desired)[deploymentRevisionAnnotation] = "7"
			revisionAnnotations(p.Resources[0].Baseline)[deploymentRevisionAnnotation] = "7"
			revisionAnnotations(current)[deploymentRevisionAnnotation] = "7"
			planSHA := p.SHA256()
			backend := &nativeRevisionBackend{memoryBackend: &memoryBackend{objects: map[string]Object{ID(current): current}}}
			root := t.TempDir()
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			check := func(context.Context, *Plan) error { return nil }
			if err := p.ExecuteResumable(t.Context(), backend, planSHA, root, check, check, check); err == nil {
				t.Fatal("native rollout interruption was not retained")
			}
			journal, exists, err := p.ReadExecution(root)
			if err != nil || !exists || len(journal.Owners) != 1 || len(journal.Completed) != 1 || journal.Completed[0] != p.Resources[0].ID || journal.Failed != p.Resources[0].ID || journal.InFlight != "" || journal.AcceptanceStarted || journal.Accepted || len(backend.written) != 1 {
				t.Fatal("not the original written-owner/pending-readiness boundary", err)
			}
			planPath := filepath.Join(root, "deployment/rollback", planSHA, "plan.json")
			before, err := os.ReadFile(planPath)
			if err != nil {
				t.Fatal(err)
			}
			object := backend.objects[p.Resources[0].ID]
			switch mode {
			case "user-annotation":
				revisionAnnotations(object)["rtk.cloud/reviewed"] = "changed"
			case "spec":
				Map(object["spec"])["replicas"] = float64(9)
			case "owner":
				Map(object["metadata"])["uid"] = "replacement"
			}
			admitErr := p.AdmitResumable(t.Context(), backend, root)
			err = p.ExecuteResumable(t.Context(), backend, planSHA, root, check, check, check)
			if (admitErr == nil) != (mode == "revision-only") || (err == nil) != (mode == "revision-only") || len(backend.written) != 1 {
				t.Fatal("known original child was replayed or drift accepted", mode, admitErr, err)
			}
			after, err := os.ReadFile(planPath)
			if err != nil || !bytes.Equal(before, after) || p.SHA256() != planSHA {
				t.Fatal("original plan was rewritten during resume", err)
			}
			if mode == "revision-only" {
				journal, _, err = p.ReadExecution(root)
				if err != nil || !journal.Accepted || len(journal.Completed) != 1 || journal.Owners[p.Resources[0].ID] != "owner" {
					t.Fatal("same original child did not genuinely finish", err)
				}
			}
		})
	}
}
