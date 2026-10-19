package deploymentplan

import (
	"context"
	"reflect"
	"testing"
)

func TestMatchesOnlyDeclaredTopLevelMetadataKeys(t *testing.T) {
	for _, field := range []string{"labels", "annotations"} {
		for _, test := range []struct {
			name string
			want any
			have any
			pass bool
		}{
			{"null-preserves-existing", nil, Object{"unrelated": "original"}, true},
			{"empty-preserves-existing", Object{}, Object{"unrelated": "original"}, true},
			{"empty-allows-absent", Object{}, nil, true},
			{"declared-key-with-extra", Object{"policy": "approved"}, Object{"policy": "approved", "unrelated": "original"}, true},
			{"declared-key-changed", Object{"policy": "approved"}, Object{"policy": "changed"}, false},
			{"declared-key-absent", Object{"policy": "approved"}, nil, false},
			{"declared-null-is-still-a-value", Object{"policy": nil}, Object{"policy": "changed"}, false},
			{"invalid-metadata-is-not-empty", "invalid", Object{}, false},
		} {
			t.Run(field+"/"+test.name, func(t *testing.T) {
				want := Object{"metadata": Object{field: test.want}}
				have := Object{"metadata": Object{field: test.have}}
				if Matches(want, have) != test.pass {
					t.Fatal("metadata matching disagrees with declared ownership")
				}
			})
		}
	}
	for _, field := range []string{"data", "spec"} {
		if Matches(Object{field: nil}, Object{field: Object{"value": "changed"}}) {
			t.Fatal("top-level metadata handling weakened configuration matching")
		}
	}
	want := Object{"spec": Object{"template": Object{"metadata": Object{"annotations": nil}}}}
	have := Object{"spec": Object{"template": Object{"metadata": Object{"annotations": Object{"policy": "changed"}}}}}
	if Matches(want, have) {
		t.Fatal("top-level metadata handling weakened Pod template matching")
	}
}

func TestCompletedConfigMapPreservesHelmMetadataAcrossPhaseFences(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, metadataMode := range []string{"null", "empty", "declared"} {
			for _, drift := range []string{"none", "data", "uid", "declared-annotation"} {
				if drift == "declared-annotation" && metadataMode != "declared" {
					continue
				}
				t.Run(environment+"/"+metadataMode+"/"+drift, func(t *testing.T) {
					f := phaseObservationProfile(t, environment, false)
					annotations := Object{"meta.helm.sh/release-name": "zammad", "meta.helm.sh/release-namespace": f.plan.Stack + "-video-cloud"}
					Map(f.backend.objects[f.first]["metadata"])["annotations"] = Clone(annotations)
					var desiredAnnotations any
					if metadataMode == "empty" {
						desiredAnnotations = Object{}
					} else if metadataMode == "declared" {
						desiredAnnotations = Object{"policy.example/identity": "approved"}
					}
					definitions := make([]Definition, 0, len(f.plan.Resources))
					live := make([]Object, 0, len(f.plan.Resources))
					for _, resource := range f.plan.Resources {
						if resource.ID == f.first {
							Map(resource.Desired["metadata"])["annotations"] = desiredAnnotations
						}
						definitions = append(definitions, Definition{Object: resource.Desired, Phase: resource.Phase, Dependencies: resource.Dependencies, Retain: resource.Action == "retain"})
						live = append(live, f.backend.objects[resource.ID])
					}
					var err error
					f.plan, err = Compile(f.plan.Inputs, definitions, live)
					if err != nil {
						t.Fatal(err)
					}
					sha, phases := f.plan.SHA256(), 0
					phaseFence := func(context.Context, *Plan) (map[string]Object, error) {
						phases++
						if phases == 2 {
							current := f.backend.objects[f.first]
							switch drift {
							case "data":
								Map(current["data"])["value"] = "concurrent"
							case "uid":
								Map(current["metadata"])["uid"] = "replacement"
							case "declared-annotation":
								Map(Map(current["metadata"])["annotations"])["policy.example/identity"] = "concurrent"
							}
						}
						return f.backend.snapshot(), nil
					}
					check := func(context.Context, *Plan) error { return nil }
					err = f.plan.Execute(context.Background(), f.backend, sha, f.root, check, check, check, phaseFence)
					if drift != "none" {
						assertPhaseExecutionStopped(t, f, err, f.first, 2, 1)
					} else {
						journal := readExecutionJournal(t, f.root, sha)
						if err != nil || !journal.Accepted || len(journal.Completed) != 3 || len(f.backend.written) != 2 || f.backend.leaseHeld {
							t.Fatal("preserved Helm metadata blocked dependent execution or acceptance", err)
						}
					}
					if len(f.backend.written) == 0 {
						t.Fatal("ConfigMap update was not exercised")
					}
					written := Map(Map(f.backend.written[0]["metadata"])["annotations"])
					for key, value := range annotations {
						if written[key] != value {
							t.Fatal("update removed original Helm metadata")
						}
					}
					if !reflect.DeepEqual(Map(f.backend.written[0]["data"]), Object{"value": "desired"}) || f.plan.SHA256() != sha {
						t.Fatal("metadata matching changed desired configuration or the approved plan")
					}
				})
			}
		}
	}
}
