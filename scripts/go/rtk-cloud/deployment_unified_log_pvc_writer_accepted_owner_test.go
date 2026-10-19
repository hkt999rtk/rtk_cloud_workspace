package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestLogIngesterPVCWriterQualifierUsesAcceptedOwnersAndRecordedVolume(t *testing.T) {
	for _, mode := range []string{"sparse", "consistent-map", "conflict-deployment", "conflict-claim", "missing-accepted-deployment", "missing-accepted-claim", "conflict-accepted-claim", "replacement-deployment", "replacement-claim", "replacement-pv", "foreign-csi", "foreign-claimref", "missing-volume", "duplicate-volume", "native-defaults", "app-env-drift", "app-image-drift", "foreign-token-source", "foreign-token-mount", "foreign-extra-volume", "missing-declared-mount"} {
		t.Run(mode, func(t *testing.T) {
			cfg, defs, p := logPVCWriterFixture(t)
			dir := t.TempDir()
			store := secretStore{Environment: "dev", ConfigRoot: dir, Root: filepath.Join(dir, "dev")}
			if err := os.Mkdir(store.Root, 0700); err != nil {
				t.Fatal(err)
			}
			ns := cfg.Values["CLOUD_STACK_NAME"] + "-video-cloud"
			claim := deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": deploymentplan.Object{"namespace": ns, "name": p.ClaimName, "uid": p.ClaimUID}, "spec": deploymentplan.Object{"volumeName": p.PVName, "storageClassName": "linode-block-storage-retain"}, "status": deploymentplan.Object{"phase": "Bound"}}
			defs = append(defs, deploymentplan.Definition{Object: deploymentplan.Clone(claim), Phase: "storage", Retain: true})
			pv := deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolume", "metadata": deploymentplan.Object{"name": p.PVName, "uid": p.PVUID}, "spec": deploymentplan.Object{"claimRef": deploymentplan.Object{"uid": p.ClaimUID, "name": p.ClaimName, "namespace": ns}, "csi": deploymentplan.Object{"driver": "linodebs.csi.linode.com", "volumeHandle": p.NativeID + "-pvc-original"}, "persistentVolumeReclaimPolicy": "Retain", "storageClassName": "linode-block-storage-retain"}, "status": deploymentplan.Object{"phase": "Bound"}}
			originalApp := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(defs[0].Object)["containers"])[0])
			originalApp["env"] = append(certIssuerObjectList(originalApp["env"]), deploymentplan.Object{"name": "EMPTY_NATIVE_DEFAULT", "value": "", "valueFrom": nil})
			dep := deploymentplan.Clone(defs[0].Object)
			rsUID := "00000005-1111-4111-8111-111111111111"
			rs := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "ReplicaSet", "metadata": deploymentplan.Object{"namespace": ns, "name": "original-log-rs", "uid": rsUID, "ownerReferences": []any{deploymentplan.Object{"kind": "Deployment", "name": "video-cloud-logingester", "uid": p.DeploymentUID, "controller": true}}}}
			pod := deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": deploymentplan.Object{"namespace": ns, "name": p.PodName, "uid": p.PodUID, "ownerReferences": []any{deploymentplan.Object{"kind": "ReplicaSet", "name": "original-log-rs", "uid": rsUID, "controller": true}}}, "spec": deploymentplan.Clone(environmentPKIPod(dep)), "status": deploymentplan.Object{"phase": "Running"}}
			ledger := environmentCreateResourceLedger{Environment: "dev", Stack: cfg.Values["CLOUD_STACK_NAME"], Kubernetes: map[string]string{}, Volumes: []environmentCreateVolumeOwner{{Namespace: ns, PVCName: p.ClaimName, PVCUID: p.ClaimUID, PVName: p.PVName, PVUID: p.PVUID, NativeID: p.NativeID}}}
			depID, claimID := deploymentplan.ID(dep), deploymentplan.ID(claim)
			foreign := "99999999-9999-4999-8999-999999999999"
			switch mode {
			case "consistent-map":
				ledger.Kubernetes[depID] = p.DeploymentUID
				ledger.Kubernetes[claimID] = p.ClaimUID
			case "conflict-deployment":
				ledger.Kubernetes[depID] = foreign
			case "conflict-claim":
				ledger.Kubernetes[claimID] = foreign
			case "missing-accepted-deployment":
				delete(deploymentplan.Map(defs[0].Object["metadata"]), "uid")
			case "missing-accepted-claim":
				delete(deploymentplan.Map(defs[len(defs)-1].Object["metadata"]), "uid")
			case "conflict-accepted-claim":
				deploymentplan.Map(defs[len(defs)-1].Object["metadata"])["uid"] = foreign
			case "replacement-deployment":
				deploymentplan.Map(dep["metadata"])["uid"] = foreign
			case "replacement-claim":
				deploymentplan.Map(claim["metadata"])["uid"] = foreign
			case "replacement-pv":
				deploymentplan.Map(pv["metadata"])["uid"] = foreign
			case "foreign-csi":
				deploymentplan.Map(deploymentplan.Map(pv["spec"])["csi"])["volumeHandle"] = "9999-pvc-foreign"
			case "foreign-claimref":
				deploymentplan.Map(deploymentplan.Map(pv["spec"])["claimRef"])["uid"] = foreign
			case "missing-volume":
				ledger.Volumes = nil
			case "duplicate-volume":
				ledger.Volumes = append(ledger.Volumes, ledger.Volumes[0])
			}
			podSpec := deploymentplan.Map(pod["spec"])
			podApp := deploymentplan.Map(certIssuerObjectList(podSpec["containers"])[0])
			if mode == "native-defaults" || mode == "foreign-token-source" || mode == "foreign-token-mount" {
				for _, raw := range certIssuerObjectList(podApp["env"]) {
					e := deploymentplan.Map(raw)
					if e["name"] == "EMPTY_NATIVE_DEFAULT" {
						delete(e, "value")
						delete(e, "valueFrom")
					}
				}
				podSpec["volumes"] = append(certIssuerObjectList(podSpec["volumes"]), deploymentplan.Object{"name": "kube-api-access-abc12", "projected": deploymentplan.Object{"defaultMode": float64(0644), "sources": []any{
					deploymentplan.Object{"serviceAccountToken": deploymentplan.Object{"expirationSeconds": float64(3607), "path": "token"}},
					deploymentplan.Object{"configMap": deploymentplan.Object{"name": "kube-root-ca.crt", "items": []any{deploymentplan.Object{"key": "ca.crt", "path": "ca.crt"}}}},
					deploymentplan.Object{"downwardAPI": deploymentplan.Object{"items": []any{deploymentplan.Object{"fieldRef": deploymentplan.Object{"apiVersion": "v1", "fieldPath": "metadata.namespace"}, "path": "namespace"}}}},
				}}})
				podApp["volumeMounts"] = append(certIssuerObjectList(podApp["volumeMounts"]), deploymentplan.Object{"name": "kube-api-access-abc12", "mountPath": "/var/run/secrets/kubernetes.io/serviceaccount", "readOnly": true})
			}
			switch mode {
			case "app-env-drift":
				deploymentplan.Map(certIssuerObjectList(podApp["env"])[0])["value"] = "foreign"
			case "app-image-drift":
				podApp["image"] = "foreign"
			case "foreign-token-source":
				vs := certIssuerObjectList(podSpec["volumes"])
				projection := deploymentplan.Map(deploymentplan.Map(vs[len(vs)-1])["projected"])
				deploymentplan.Map(deploymentplan.Map(certIssuerObjectList(projection["sources"])[1])["configMap"])["name"] = "foreign"
			case "foreign-token-mount":
				ms := certIssuerObjectList(podApp["volumeMounts"])
				deploymentplan.Map(ms[len(ms)-1])["mountPath"] = "/var/lib/mqtt-trust"
			case "foreign-extra-volume":
				podSpec["volumes"] = append(certIssuerObjectList(podSpec["volumes"]), deploymentplan.Object{"name": "foreign", "emptyDir": deploymentplan.Object{}})
			case "missing-declared-mount":
				podApp["volumeMounts"] = certIssuerObjectList(podApp["volumeMounts"])[1:]
			}
			objects := map[string]deploymentplan.Object{}
			for _, o := range []deploymentplan.Object{dep, claim, pv, pod, rs} {
				objects[deploymentplan.ID(o)] = o
			}
			raw, _ := json.Marshal(objects)
			objpath := filepath.Join(dir, "objects.json")
			if err := os.WriteFile(objpath, raw, 0600); err != nil {
				t.Fatal(err)
			}
			tool := filepath.Join(dir, "kubectl")
			script := `#!/usr/bin/env python3
import json,os,sys
args=sys.argv[1:];o=json.load(open(os.environ['RTK_LOG_WRITER_OBJECTS']))
with open(os.environ['RTK_LOG_WRITER_TRACE'],'a') as f:f.write(json.dumps(args)+'\n')
ns=args[args.index('--namespace')+1] if '--namespace' in args else ''
if 'exec' in args:
 print('10001\n999\n999\n2775 0 10001');sys.exit(0)
if 'get' not in args:sys.exit(71)
i=args.index('get');kind=args[i+1]
if kind=='pods':print(json.dumps({'items':[v for k,v in o.items() if k.startswith('Pod/')]}));sys.exit(0)
names={'deployments':'Deployment','deployment':'Deployment','persistentvolumeclaims':'PersistentVolumeClaim','persistentvolumeclaim':'PersistentVolumeClaim','persistentvolumes':'PersistentVolume','persistentvolume':'PersistentVolume','replicasets':'ReplicaSet','replicaset':'ReplicaSet','pods':'Pod','pod':'Pod'}
key=names.get(kind.lower(),kind)+'/'+ns+'/'+args[i+2]
print(json.dumps(o[key]) if key in o else '')
`
			if err := os.WriteFile(tool, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("RTK_CLOUD_KUBECTL", tool)
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("RTK_LOG_WRITER_OBJECTS", objpath)
			t.Setenv("RTK_LOG_WRITER_TRACE", filepath.Join(dir, "trace"))
			before := deploymentplan.Digest(defs)
			volumeBefore := deploymentplan.Digest(ledger)
			got, err := qualifyEnvironmentPostCleanupLogIngesterProfile(t.Context(), cfg, environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("synthetic frozen selected identity")}, defs, ledger)
			if mode == "sparse" || mode == "consistent-map" || mode == "native-defaults" {
				if err != nil || got != p {
					trace, _ := os.ReadFile(filepath.Join(dir, "trace"))
					t.Log(string(trace))
					t.Fatal("accepted owner and recorded allocation qualification", err)
				}
			} else if err == nil {
				t.Fatal("foreign/missing accepted owner or allocation was accepted")
			}
			if deploymentplan.Digest(defs) != before || deploymentplan.Digest(ledger) != volumeBefore {
				t.Fatal("qualifier changed original authority")
			}
		})
	}
}
