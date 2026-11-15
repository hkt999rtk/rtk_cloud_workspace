package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type ciRecoveryInspectionFixture struct {
	runtime                    *maintenanceRuntime
	consumer                   crlMaintenanceConsumer
	pod, parent, claim, helper deploymentplan.Object
	creates, deletes, execs    int
	deleteWaitError            error
}

func newCIRecoveryInspectionFixture(t *testing.T) *ciRecoveryInspectionFixture {
	t.Helper()
	original := maintenanceRuntimeTestFixture(t)
	f := &ciRecoveryInspectionFixture{runtime: original.r, pod: original.pod}
	f.runtime.lease = nil
	f.runtime.image = "example.invalid/native@sha256:" + strings.Repeat("a", 64)
	f.runtime.cfg.Values["DEPLOYMENT_ROLLOUT_TIMEOUT_SECONDS"] = "60"
	f.runtime.cfg.Values["DEPLOYMENT_UPGRADE_WINDOW_SECONDS"] = "300"
	meta, spec := deploymentplan.Map(f.pod["metadata"]), deploymentplan.Map(f.pod["spec"])
	f.consumer = crlMaintenanceConsumer{Namespace: deploymentplan.String(meta["namespace"]), Pod: deploymentplan.String(meta["name"]), PodUID: deploymentplan.String(meta["uid"]), Container: "app"}
	spec["nodeName"] = "original-node"
	spec["securityContext"] = deploymentplan.Object{"runAsUser": float64(10123), "runAsGroup": float64(10456), "fsGroup": float64(10456), "fsGroupChangePolicy": "OnRootMismatch"}
	meta["ownerReferences"] = []any{deploymentplan.Object{"kind": "StatefulSet", "name": "original", "uid": "original-parent", "controller": true}}
	f.parent = deploymentplan.Object{"metadata": deploymentplan.Object{"uid": "original-parent"}}
	f.claim = deploymentplan.Object{"metadata": deploymentplan.Object{"uid": "original-claim"}, "status": deploymentplan.Object{"phase": "Bound"}}
	f.runtime.readOverride = func(_ context.Context, id string) (deploymentplan.Object, error) {
		switch {
		case id == "Pod/"+f.consumer.Namespace+"/"+f.consumer.Pod:
			return f.pod, nil
		case id == "StatefulSet/"+f.consumer.Namespace+"/original":
			return f.parent, nil
		case id == "PersistentVolumeClaim/"+f.consumer.Namespace+"/original-state":
			return f.claim, nil
		case strings.Contains(id, "/crl-pvc-recovery-"):
			return f.helper, nil
		default:
			return nil, errors.New("unrelated resource")
		}
	}
	f.runtime.commandOverride = func(_ context.Context, input []byte, args ...string) ([]byte, error) {
		command := strings.Join(args, " ")
		switch {
		case strings.Contains(command, "create -f -"):
			f.creates++
			if err := json.Unmarshal(input, &f.helper); err != nil {
				t.Fatal(err)
			}
			hm, hs := deploymentplan.Map(f.helper["metadata"]), deploymentplan.Map(f.helper["spec"])
			security := deploymentplan.Map(hs["securityContext"])
			if hs["nodeName"] != "original-node" || hs["automountServiceAccountToken"] != false || security["fsGroup"] != nil || security["fsGroupChangePolicy"] != nil || security["runAsUser"] != float64(10123) {
				t.Fatal("inspection changed ownership or gained ambient authority")
			}
			hm["name"], hm["uid"] = "crl-pvc-recovery-original", "helper-owned"
			for _, v := range certIssuerObjectList(hs["volumes"]) {
				pvc := deploymentplan.Map(deploymentplan.Map(v)["persistentVolumeClaim"])
				if pvc != nil && (pvc["claimName"] != "original-state" || pvc["readOnly"] != true) {
					t.Fatal("inspection gained writable or foreign claim")
				}
			}
			return json.Marshal(f.helper)
		case strings.Contains(command, "wait --for=condition=Ready"):
			return nil, nil
		case strings.Contains(command, "exec"):
			if !strings.Contains(command, "pod/crl-pvc-recovery-original --container maintenance") {
				t.Fatal("inspection executed inside original consumer")
			}
			f.execs++
			return []byte("original-public-state"), nil
		case strings.HasPrefix(command, "delete --raw "):
			var options deploymentplan.Object
			if json.Unmarshal(input, &options) != nil || deploymentplan.Map(options["preconditions"])["uid"] != "helper-owned" || !strings.HasSuffix(args[2], "/crl-pvc-recovery-original") {
				t.Fatal("cleanup lacked the exact helper UID")
			}
			f.deletes++
			return nil, nil
		case strings.Contains(command, "wait --for=delete"):
			return nil, f.deleteWaitError
		default:
			return nil, errors.New("unexpected native mutation")
		}
	}
	return f
}

func TestCILocalRecoveryInspectionRejectsForeignScopeBeforeCreatingHelper(t *testing.T) {
	for _, mode := range []string{"wrong-pod", "duplicate-controller", "unsupported-controller", "replacement-parent", "unavailable-image", "expired-deadline", "custody-consumer", "unbound-claim", "subpath", "unknown-volume", "no-claim"} {
		t.Run(mode, func(t *testing.T) {
			f := newCIRecoveryInspectionFixture(t)
			meta, spec := deploymentplan.Map(f.pod["metadata"]), deploymentplan.Map(f.pod["spec"])
			container := managedNamedField(spec["containers"], "app")
			ctx := t.Context()
			switch mode {
			case "wrong-pod":
				f.consumer.PodUID = "replacement"
			case "duplicate-controller":
				meta["ownerReferences"] = append(certIssuerObjectList(meta["ownerReferences"]), deploymentplan.Clone(deploymentplan.Map(certIssuerObjectList(meta["ownerReferences"])[0])))
			case "unsupported-controller":
				deploymentplan.Map(certIssuerObjectList(meta["ownerReferences"])[0])["kind"] = "Job"
			case "replacement-parent":
				deploymentplan.Map(f.parent["metadata"])["uid"] = "replacement"
			case "unavailable-image":
				f.runtime.image = ""
				f.runtime.ensureImage = func(context.Context) (string, error) { return "", errors.New("no reviewed helper") }
			case "expired-deadline":
				bounded, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer cancel()
				ctx = bounded
			case "custody-consumer":
				container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(map[string]string{"PKI_STATE_BACKEND": "openbao"}))
			case "unbound-claim":
				deploymentplan.Map(f.claim["status"])["phase"] = "Pending"
			case "subpath":
				deploymentplan.Map(certIssuerObjectList(container["volumeMounts"])[0])["subPath"] = "private"
			case "unknown-volume":
				deploymentplan.Map(certIssuerObjectList(container["volumeMounts"])[0])["name"] = "foreign"
			case "no-claim":
				volume := managedNamedField(spec["volumes"], "state")
				delete(volume, "persistentVolumeClaim")
				volume["emptyDir"] = deploymentplan.Object{}
			}
			before := deploymentplan.Digest([]any{f.pod, f.parent, f.claim})
			if _, err := f.runtime.createLocalRecovery(ctx, f.consumer, f.pod); err == nil {
				t.Fatal("unqualified original claim gained inspection authority")
			}
			if f.creates != 0 || f.execs != 0 || f.deletes != 0 || deploymentplan.Digest([]any{f.pod, f.parent, f.claim}) != before {
				t.Fatal("refused inspection wrote or changed original resources")
			}
		})
	}
}

func TestCILocalRecoveryRechecksEveryOriginalOwnerBeforeReading(t *testing.T) {
	for _, mode := range []string{"pod", "pod-spec", "parent", "claim", "helper", "helper-spec"} {
		t.Run(mode, func(t *testing.T) {
			f := newCIRecoveryInspectionFixture(t)
			if _, err := f.runtime.createLocalRecovery(t.Context(), f.consumer, f.pod); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "pod":
				deploymentplan.Map(f.pod["metadata"])["uid"] = "replacement"
			case "pod-spec":
				deploymentplan.Map(f.pod["spec"])["nodeName"] = "replacement-node"
			case "parent":
				deploymentplan.Map(f.parent["metadata"])["uid"] = "replacement"
			case "claim":
				deploymentplan.Map(f.claim["metadata"])["uid"] = "replacement"
			case "helper":
				deploymentplan.Map(f.helper["metadata"])["uid"] = "replacement"
			case "helper-spec":
				deploymentplan.Map(f.helper["spec"])["nodeName"] = "replacement-node"
			}
			before := deploymentplan.Digest([]any{f.pod, f.parent, f.claim, f.helper})
			if _, err := f.runtime.localConsumerCommand(t.Context(), f.consumer, f.pod, nil, "cat", "/state/root-crl.json"); err == nil || !strings.Contains(err.Error(), "owner, helper or claim changed") {
				t.Fatal("inspection read after its observed owner changed", err)
			}
			if f.execs != 0 || f.creates != 1 || f.deletes != 0 || deploymentplan.Digest([]any{f.pod, f.parent, f.claim, f.helper}) != before {
				t.Fatal("owner recheck changed native state or attempted an exec")
			}
		})
	}
}

func TestCILocalRecoveryCleanupRetainsUnconfirmedHelperForRetry(t *testing.T) {
	f := newCIRecoveryInspectionFixture(t)
	before := deploymentplan.Digest([]any{f.pod, f.parent, f.claim})
	if _, err := f.runtime.createLocalRecovery(t.Context(), f.consumer, f.pod); err != nil {
		t.Fatal(err)
	}
	f.deleteWaitError = errors.New("original deletion not confirmed")
	if err := f.runtime.closeLocalRecovery(t.Context()); err == nil || len(f.runtime.localRecovery) != 1 || f.deletes != 1 {
		t.Fatal("unconfirmed helper was forgotten", err)
	}
	f.deleteWaitError = nil
	if err := f.runtime.closeLocalRecovery(t.Context()); err != nil || len(f.runtime.localRecovery) != 0 || f.deletes != 2 {
		t.Fatal("exact helper cleanup could not resume", err)
	}
	if err := f.runtime.closeLocalRecovery(t.Context()); err != nil || f.deletes != 2 || deploymentplan.Digest([]any{f.pod, f.parent, f.claim}) != before {
		t.Fatal("cleanup replayed or changed permanent resources", err)
	}
}
