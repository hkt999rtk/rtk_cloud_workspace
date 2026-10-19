package main

import (
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestProductSessionRenewalCopiesUseExactPKIConditionalDelete(t *testing.T) {
	for _, kind := range []string{"Secret", "ConfigMap"} {
		for _, mode := range []string{"new", "begin-before-delete", "lost-before-delete", "lost-after-delete", "unrecorded-absence", "foreign-uid", "foreign-body", "completed-replacement"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				f := newProductForwardFixture(t)
				uid := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
				o := deploymentplan.Object{"apiVersion": "v1", "kind": kind, "metadata": deploymentplan.Object{"namespace": f.p.Stack + "-video-cloud", "name": "expired-product-copy", "uid": uid, "resourceVersion": "1", "labels": deploymentplan.Object{"rtk.realtek.com/stack": f.p.Stack}}, "data": deploymentplan.Object{"proof": "isolated-original-content"}}
				d := deploymentplan.Definition{Phase: "retirement", Retire: true, Object: deploymentplan.Clone(o)}
				id := deploymentplan.ID(o)
				f.b.objects[id] = deploymentplan.Clone(o)
				r := &productSessionRenewal{SourceSHA256: deploymentplan.Digest("exact-renewal-source"), Source: productSessionRenewalSource{SessionID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", OriginalSession: productSessionInstallations{Session: f.service.BootstrapSessionID}}}
				spec := productSessionRenewalCopyRetireSpec(r, d)
				if mode == "begin-before-delete" {
					if err := f.e.BeginEntry(spec.Key, spec.Phase, mustProviderRecoveryJSON(spec.Intent), time.Now().UTC()); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "unrecorded-absence" {
					delete(f.b.objects, id)
				}
				if mode == "foreign-uid" {
					deploymentplan.Map(f.b.objects[id]["metadata"])["uid"] = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
				}
				if mode == "foreign-body" {
					deploymentplan.Map(f.b.objects[id]["data"])["proof"] = "foreign"
				}
				calls, effects := 0, 0
				f.nativeOverride = func(args []string) ([]byte, int, bool) {
					joined := strings.Join(args, " ")
					if strings.Contains(joined, "delete --raw=/api/v1/namespaces/") {
						calls++
						plural := "secrets"
						if kind == "ConfigMap" {
							plural = "configmaps"
						}
						if !strings.Contains(joined, "/"+plural+"/expired-product-copy -f -") {
							t.Fatal("wrong native conditional-delete path")
						}
						if mode == "lost-before-delete" && calls == 1 {
							return nil, 1, true
						}
						if f.b.objects[id] != nil {
							delete(f.b.objects, id)
							effects++
						}
						if mode == "lost-after-delete" && calls == 1 {
							return nil, 1, true
						}
						return []byte(`{"kind":"Status","status":"Success"}`), 0, true
					}
					return nil, 0, false
				}
				err := retireProductSessionRenewalCopy(t.Context(), f.runtime, d, spec)
				if mode == "lost-before-delete" || mode == "lost-after-delete" {
					if err == nil {
						t.Fatal("lost response claimed completion")
					}
					err = retireProductSessionRenewalCopy(t.Context(), f.runtime, d, spec)
				}
				if mode == "unrecorded-absence" || mode == "foreign-uid" || mode == "foreign-body" {
					if err == nil || calls != 0 || effects != 0 {
						t.Fatal("unowned copy retired", err, calls, effects)
					}
					return
				}
				if err != nil || f.b.objects[id] != nil || effects != 1 {
					t.Fatal("exact conditional delete failed", err, calls, effects)
				}
				if mode == "completed-replacement" {
					f.b.objects[id] = deploymentplan.Clone(o)
					if err = retireProductSessionRenewalCopy(t.Context(), f.runtime, d, spec); err == nil || effects != 1 {
						t.Fatal("replacement retired after completed absence")
					}
					return
				}
				before := calls
				if err = retireProductSessionRenewalCopy(t.Context(), f.runtime, d, spec); err != nil || calls != before {
					t.Fatal("completed delete repeated", err)
				}
			})
		}
	}
}
