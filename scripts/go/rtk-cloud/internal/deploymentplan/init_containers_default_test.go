package deploymentplan

import "testing"

func TestAdmissionAcceptsOmittedExplicitlyEmptyInitContainers(t *testing.T) {
	body := Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": Object{"name": "pki-controller", "namespace": "video-cloud-dev-video-cloud"},
		"spec": Object{"template": Object{"spec": Object{"containers": []any{Object{"name": "controller", "image": "approved"}}, "initContainers": []any{}}}}}
	for _, mode := range []string{"omitted", "null", "empty", "injected", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			current := Clone(body)
			pod := Map(Map(Map(current["spec"])["template"])["spec"])
			switch mode {
			case "omitted":
				delete(pod, "initContainers")
			case "null":
				pod["initContainers"] = nil
			case "injected":
				pod["initContainers"] = []any{Object{"name": "unexpected", "image": "unapproved"}}
			case "invalid":
				pod["initContainers"] = Object{}
			}
			want := mode != "injected" && mode != "invalid"
			if matchesAdmission(body, current) != want || Matches(body, current) != want {
				t.Fatal("admission and rollout must agree about the explicit empty init container list")
			}
			if (len(admissionMismatchPaths(body, current)) == 0) != want {
				t.Fatal("admission diagnostics disagree with the normalized result")
			}
		})
	}
	current := Clone(body)
	wantPod := Map(Map(Map(body["spec"])["template"])["spec"])
	wantPod["initContainers"] = []any{Object{"name": "required", "image": "approved"}}
	delete(Map(Map(Map(current["spec"])["template"])["spec"]), "initContainers")
	if Matches(body, current) || matchesAdmission(body, current) {
		t.Fatal("omission of a required init container was accepted")
	}
}

func TestDriftTreatsOnlyNativeEmptyInitFormsAsEquivalent(t *testing.T) {
	for _, before := range []string{"omitted", "null", "empty"} {
		for _, after := range []string{"omitted", "null", "empty", "injected", "invalid", "foreign-uid"} {
			t.Run(before+"/"+after, func(t *testing.T) {
				body := Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": Object{"name": "controller", "namespace": "video-cloud-dev-video-cloud", "uid": "original"}, "spec": Object{"template": Object{"spec": Object{"containers": []any{Object{"name": "controller", "image": "approved"}}}}}}
				set := func(o Object, mode string) {
					p := workloadPodSpec(o)
					switch mode {
					case "null":
						p["initContainers"] = nil
					case "empty":
						p["initContainers"] = []any{}
					case "injected":
						p["initContainers"] = []any{Object{"name": "unexpected", "image": "unapproved"}}
					case "invalid":
						p["initContainers"] = Object{}
					case "foreign-uid":
						Map(o["metadata"])["uid"] = "foreign"
					}
				}
				baseline, current := Clone(body), Clone(body)
				set(baseline, before)
				set(current, after)
				r := Resource{ID: ID(body), Action: "retain", UID: "original", Desired: body, Baseline: Projection(baseline)}
				savedBase, savedCurrent := Digest(baseline), Digest(current)
				_, err := r.Body(current)
				want := after == "omitted" || after == "null" || after == "empty"
				if (err == nil) != want {
					t.Fatalf("empty API form drift mismatch: accepted=%v want=%v err=%v", err == nil, want, err)
				}
				if Digest(baseline) != savedBase || Digest(current) != savedCurrent {
					t.Fatal("drift normalization mutated caller state")
				}
			})
		}
	}
}
