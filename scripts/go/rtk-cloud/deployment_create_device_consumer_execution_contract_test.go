package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// This fixture supplies external Kubernetes, OCI and registry protocol data.
// The consumer target parser, current Pod/volume proof, CRL signature and ACK
// roster validation remain the actual production implementations.
func crlDeviceConsumerExecutionFixture(t *testing.T) (*crlExecutorFixture, createDeviceRootCRLPlan, createRootCRLRecord, *createServiceSessionRecovery) {
	t.Helper()
	f := newCRLExecutorFixture(t)
	p, key, now := deviceRootCRLFixture(t)
	p.Configuration = f.cfg
	p.Root.Document = deploymentplan.Object{"issuer_id": p.Root.ID, "environment": "dev", "trust_domain": "device", "kind": "root", "status": "active", "certificate_pem": p.Root.Certificate, "chain_pem": p.Root.ChainPEM, "certificate_fingerprint_sha256": p.Root.RootFingerprint, "trust_bundle_version": p.Root.BundleVersion, "signer_provider": "openbao", "signer_reference": p.Root.Reference}
	entries, err := deviceRootCRLUnion(p.Root, p.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	signed := signedRootCRLFixture(t, p.Root, key, createRootCRLRequest{IssuerID: p.Root.ID, Fingerprint: p.Root.RootFingerprint, Number: "4", ThisUpdate: now.Add(-time.Minute), NextUpdate: now.Add(24 * time.Hour), Entries: entries})
	oci := []byte(`{"schemaVersion":2,"config":{"digest":"sha256:` + strings.Repeat("c", 64) + `"}}`)
	imageDigest := "sha256:" + environmentPreflightBytesSHA256(oci)
	dir := t.TempDir()
	docker := `#!/usr/bin/env python3
import json,os,pathlib,sys
if sys.argv[1:]==['info','--format','{{json .ClientInfo.Plugins}}']:
 print(json.dumps([{'Name':'buildx','Path':'/isolated/plugins/docker-buildx'}]))
elif sys.argv[1]=='--config' and sys.argv[3:7]==['buildx','imagetools','inspect','--raw']:
 sys.stdout.buffer.write(pathlib.Path(os.environ['RTK_CRL_DEVICE_OCI']).read_bytes())
else:raise AssertionError('unexpected OCI protocol')
`
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(docker), 0700); err != nil {
		t.Fatal(err)
	}
	ociPath := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(ociPath, oci, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CRL_DEVICE_OCI", ociPath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ns := f.cfg.Values["CLOUD_STACK_NAME"] + "-video-cloud"
	state := f.state(t)
	state.Objects = map[string]deploymentplan.Object{}
	state.Caches = map[string]createRootCRLRecord{}
	state.Fingerprint = p.Root.RootFingerprint
	objects := []deploymentplan.Object{{"apiVersion": "v1", "kind": "Namespace", "metadata": deploymentplan.Object{"name": ns}}}
	owners := map[string]deploymentplan.Resource{}
	cm := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "pki-create-device-trust", "namespace": ns, "uid": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}, "data": deploymentplan.Object{"roots.pem": p.Root.Certificate}}
	controller := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": "pki-controller", "namespace": ns, "uid": "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "pki-controller", "env": environmentCreateEnv(map[string]string{"PKI_REQUIRED_CONSUMERS_DEVICE": "pkibroker,video-cloud-api"})}}}}}}
	for i, name := range []string{"video-cloud-api-pki", "mqtt-pki"} {
		_, native, oldDigest := targetWorkloadFixture(t, "dev", "Deployment")
		uid := func(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", 100*i+n) }
		for j, object := range native {
			raw := mustProviderRecoveryJSON(object)
			text := strings.NewReplacer("video-cloud-dev-application", ns, "original-controller", uid(1), "original-pod", uid(2), "original-replicaset", uid(3), "application-selected", name+"-selected", "application-0", name+"-0", `"application"`, `"`+name+`"`, oldDigest, imageDigest).Replace(string(raw))
			if err := json.Unmarshal([]byte(text), &native[j]); err != nil {
				t.Fatal(err)
			}
		}
		c, pod := native[0], native[1]
		container, manifest, path, claim, setting := "app", "api-crls.json", "/run/pki-state/device-root-crl.json", "video-cloud-api-pki-trust", "VIDEO_CLOUD_AUTH_CRL_MANIFEST"
		if name == "mqtt-pki" {
			container, manifest, path, claim, setting = "pkibroker", "broker-crls.json", "/run/pki-state/device-crl.json", "mqtt-pki-trust", "PKI_BROKER_DEVICE_CRL_MANIFEST"
		}
		spec := environmentPKIPod(c)
		app := managedNamedField(spec["containers"], "app")
		app["name"] = container
		app["env"] = environmentCreateEnv(map[string]string{setting: "/run/pki-device/" + manifest})
		app["volumeMounts"] = []any{environmentCreateServiceMount("pki-device", "/run/pki-device", true), environmentCreateServiceMount("pki-state", "/run/pki-state", false)}
		spec["volumes"] = []any{environmentCreateServiceVolume("pki-device", "configMap", "pki-create-device-trust"), environmentCreateServiceVolume("pki-state", "persistentVolumeClaim", claim)}
		pod["spec"] = deploymentplan.Clone(spec)
		deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(pod["status"])["containerStatuses"])[0])["name"] = container
		owners[deploymentplan.ID(c)] = deploymentplan.Resource{ID: deploymentplan.ID(c), UID: uid(1), Desired: deploymentplan.Projection(c)}
		deploymentplan.Map(cm["data"])[manifest] = environmentCreateServiceCRLManifest(p.Root, path)
		owner := environmentCreateVolumeOwner{Namespace: ns, PVCName: claim, PVCUID: uid(4), PVName: name + "-original-pv", PVUID: uid(5), NativeID: fmt.Sprint(100 + i)}
		claimObject := deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": deploymentplan.Object{"name": claim, "namespace": ns, "uid": owner.PVCUID}, "spec": deploymentplan.Object{"volumeName": owner.PVName}, "status": deploymentplan.Object{"phase": "Bound"}}
		volume := deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolume", "metadata": deploymentplan.Object{"name": owner.PVName, "uid": owner.PVUID}, "spec": deploymentplan.Object{"claimRef": deploymentplan.Object{"namespace": ns, "name": claim, "uid": owner.PVCUID}, "csi": deploymentplan.Object{"driver": "linodebs.csi.linode.com", "volumeHandle": owner.NativeID + "-pvc-original"}}, "status": deploymentplan.Object{"phase": "Bound"}}
		p.Volumes[deploymentplan.ID(claimObject)] = owner
		objects = append(objects, native...)
		objects = append(objects, claimObject, volume)
		state.Caches[name+"-0:"+path] = signed
	}
	objects = append(objects, cm, controller)
	for _, o := range []deploymentplan.Object{cm, controller} {
		owners[deploymentplan.ID(o)] = deploymentplan.Resource{ID: deploymentplan.ID(o), UID: deploymentplan.String(deploymentplan.Map(o["metadata"])["uid"]), Desired: deploymentplan.Projection(o)}
	}
	recovery := &createServiceSessionRecovery{ManagedLateExpiredRenewal: &createManagedLateExpiredRenewal{PriorOwners: owners}, ManagedTransportRecovery: &createManagedTransportRecovery{Device: p.Root}}
	p.Controller, p.ConfigMap, p.Consumers, err = deviceRootCRLTargets(f.cfg, recovery, objects)
	if err != nil {
		t.Fatal("genuine Device target binding", err)
	}
	for _, o := range objects {
		state.Objects[deploymentplan.ID(o)] = o
	}
	state.DeviceACKs = []managedLatePriorACK{{p.Root.ID, signed.Digest, "pkibroker", now}, {p.Root.ID, signed.Digest, "video-cloud-api", now}}
	f.writeState(t, state)
	return f, p, signed, recovery
}

func TestCRLExecutionContractDeviceConsumersRequireNativeProof(t *testing.T) {
	for _, mode := range []string{"complete", "wrong-admitted-target", "replaced-Pod", "unready-Pod", "wrong-imageID", "changed-OCI", "changed-controller", "changed-CM", "changed-volume", "missing-volume-owner", "old-cache", "missing-ACK", "foreign-ACK", "duplicate-ACK", "stale-ACK", "owner-lost"} {
		t.Run(mode, func(t *testing.T) {
			f, p, signed, _ := crlDeviceConsumerExecutionFixture(t)
			// Qualify the same complete protocol before every negative mutation, so a
			// fixture or protocol defect cannot masquerade as a fail-closed assertion.
			for _, target := range p.Consumers {
				evidence, err := observeDeviceRootCRLConsumer(t.Context(), f.cfg, f.store, f.execution, f.backend, p, target, signed)
				if err != nil || len(evidence) != 5 {
					t.Fatal("original native consumer rejected", target.ID, err)
				}
			}
			state := f.state(t)
			originalJournal := deploymentplan.Digest(f.execution.Journal)
			target := p.Consumers[0]
			pod := state.Objects["Pod/"+f.cfg.Values["CLOUD_STACK_NAME"]+"-video-cloud/"+target.PodName]
			switch mode {
			case "wrong-admitted-target":
				target.UID = "foreign"
			case "replaced-Pod":
				deploymentplan.Map(pod["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
			case "unready-Pod":
				deploymentplan.Map(pod["status"])["conditions"] = []any{deploymentplan.Object{"type": "Ready", "status": "False"}}
			case "wrong-imageID":
				deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(pod["status"])["initContainerStatuses"])[0])["imageID"] = "containerd://sha256:" + strings.Repeat("f", 64)
			case "changed-OCI":
				if err := os.WriteFile(os.Getenv("RTK_CRL_DEVICE_OCI"), []byte(`{"schemaVersion":2}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "changed-controller":
				deploymentplan.Map(state.Objects[p.Controller.ID]["metadata"])["uid"] = "foreign"
			case "changed-CM":
				deploymentplan.Map(state.Objects[p.ConfigMap.ID]["data"])["roots.pem"] = "foreign"
			case "changed-volume":
				deploymentplan.Map(deploymentplan.Map(state.Objects["PersistentVolume//"+p.Volumes[target.ClaimID].PVName]["spec"])["csi"])["volumeHandle"] = "999-pvc-foreign"
			case "missing-volume-owner":
				delete(p.Volumes, target.ClaimID)
			case "old-cache":
				state.Caches[target.PodName+":"+target.Paths[0]] = p.Snapshot.Prior
			case "missing-ACK":
				state.DeviceACKs = state.DeviceACKs[:1]
			case "foreign-ACK":
				state.DeviceACKs[0].Digest = p.Snapshot.Prior.Digest
			case "duplicate-ACK":
				state.DeviceACKs[1] = state.DeviceACKs[0]
			case "stale-ACK":
				state.DeviceACKs[0].At = signed.ThisUpdate.Add(-time.Second)
			case "owner-lost":
				f.execution.Close()
			}
			f.writeState(t, state)
			before := deploymentplan.Digest(state)
			evidence, err := observeDeviceRootCRLConsumer(t.Context(), f.cfg, f.store, f.execution, f.backend, p, target, signed)
			if mode == "complete" {
				if err != nil || len(evidence) != 5 {
					t.Fatal("current signed consumer rejected", err)
				}
			} else if err == nil {
				t.Fatal("unproven Device consumer accepted", mode)
			}
			if deploymentplan.Digest(f.state(t)) != before || deploymentplan.Digest(f.execution.Journal) != originalJournal {
				t.Fatal("observation changed original state")
			}
		})
	}
}

func TestCRLExecutionContractDeviceTargetBindingRejectsForeignManifest(t *testing.T) {
	for _, mode := range []string{"no-source", "wrong-roster", "unknown-CM-owner", "wrong-private-claim", "wrong-manifest-path", "foreign-issuer", "foreign-root"} {
		t.Run(mode, func(t *testing.T) {
			f, p, _, recovery := crlDeviceConsumerExecutionFixture(t)
			state := f.state(t)
			switch mode {
			case "no-source":
				recovery.ManagedLateExpiredRenewal = nil
			case "wrong-roster":
				managedNamedField(managedNamedField(environmentPKIPod(recovery.ManagedLateExpiredRenewal.PriorOwners[p.Controller.ID].Desired)["containers"], "pki-controller")["env"], "PKI_REQUIRED_CONSUMERS_DEVICE")["value"] = "pkibroker"
			case "unknown-CM-owner":
				r := recovery.ManagedLateExpiredRenewal.PriorOwners[p.ConfigMap.ID]
				r.UID = ""
				recovery.ManagedLateExpiredRenewal.PriorOwners[p.ConfigMap.ID] = r
			case "wrong-private-claim":
				deploymentplan.Map(managedNamedField(environmentPKIPod(recovery.ManagedLateExpiredRenewal.PriorOwners[p.Consumers[0].ID].Desired)["volumes"], "pki-state")["persistentVolumeClaim"])["claimName"] = "foreign"
			default:
				data := deploymentplan.Map(recovery.ManagedLateExpiredRenewal.PriorOwners[p.ConfigMap.ID].Desired["data"])
				if mode == "foreign-root" {
					data["roots.pem"] = "foreign"
				} else {
					var entries []deploymentplan.Object
					if err := json.Unmarshal([]byte(deploymentplan.String(data["api-crls.json"])), &entries); err != nil {
						t.Fatal(err)
					}
					if mode == "wrong-manifest-path" {
						entries[0]["state_path"] = "/foreign"
					} else {
						deploymentplan.Map(entries[0]["issuer"])["issuer_id"] = "99999999-9999-4999-8999-999999999999"
					}
					data["api-crls.json"] = string(mustProviderRecoveryJSON(entries))
				}
			}
			objects := []deploymentplan.Object{}
			for _, o := range state.Objects {
				objects = append(objects, o)
			}
			if _, _, _, err := deviceRootCRLTargets(f.cfg, recovery, objects); err == nil {
				t.Fatal("unbound Device target accepted")
			}
		})
	}
}
