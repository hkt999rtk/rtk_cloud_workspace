package deploymentplan

import (
	"strings"
	"testing"
)

func TestWorkloadLeaseRuntimeRenewalPreservesReviewedOwner(t *testing.T) {
	in, _, _ := fixture(t, "dev", "Deployment")
	desired := Object{"apiVersion": "coordination.k8s.io/v1", "kind": "Lease", "metadata": Object{"name": "pki-state-" + strings.Repeat("a", 32), "namespace": in.Stack + "-video-cloud"}}
	live := Clone(desired)
	Map(live["metadata"])["uid"] = "original-lease-uid"
	Map(live["metadata"])["resourceVersion"] = "4"
	Map(live["metadata"])["labels"] = Object{"rtk.realtek.com/stack": in.Stack, "app.kubernetes.io/managed-by": "rtk-cloud"}
	live["spec"] = Object{"holderIdentity": "original-native-process", "leaseDurationSeconds": float64(30), "renewTime": "2026-11-09T00:00:00.000000Z"}
	plan, err := Compile(in, []Definition{{Object: desired, Phase: "bootstrap"}}, []Object{live})
	if err != nil {
		t.Fatal(err)
	}
	renewed := Clone(live)
	Map(renewed["spec"])["renewTime"] = "2026-11-09T00:00:03.000000Z"
	Map(renewed["spec"])["holderIdentity"] = "replacement-native-process"
	if body, err := plan.Resources[0].Body(renewed); err != nil || body != nil {
		t.Fatal("native ownership renewal blocked a retained Lease", err)
	}
	for _, change := range []func(Object){
		func(o Object) { Map(o["metadata"])["uid"] = "foreign-lease-uid" },
		func(o Object) { Map(o["metadata"])["labels"] = Object{"rtk.realtek.com/stack": "video-cloud-staging"} },
		func(o Object) { Map(o["spec"])["preferredHolder"] = "foreign-controller" },
	} {
		foreign := Clone(renewed)
		change(foreign)
		if _, err := plan.Resources[0].Body(foreign); err == nil {
			t.Fatal("foreign lease ownership/configuration accepted")
		}
	}
}
