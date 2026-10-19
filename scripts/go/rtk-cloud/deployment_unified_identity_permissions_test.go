package main

import (
	"encoding/json"
	"errors"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
)

const identityPermissionAlpine = "alpine@sha256:" + "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"

func identityPermissionFixture(environment string) deploymentplan.Resource {
	return deploymentplan.Resource{ID: "Deployment/video-cloud-" + environment + "-video-cloud/pki-controller", UID: "original-controller", Action: "update", Desired: deploymentplan.Object{
		"kind": "Deployment", "metadata": deploymentplan.Object{"name": "pki-controller", "namespace": "video-cloud-" + environment + "-video-cloud"},
		"spec": deploymentplan.Object{"replicas": float64(1), "template": deploymentplan.Object{"spec": deploymentplan.Object{
			"securityContext": deploymentplan.Object{"runAsUser": 10001},
			"containers":      []any{deploymentplan.Object{"name": "pki-controller", "env": []any{deploymentplan.Object{"name": "PKI_SERVICE_CLIENT_IDENTITY_STATE", "value": "/var/lib/identity/client.json"}}, "volumeMounts": []any{deploymentplan.Object{"name": "identity", "mountPath": "/var/lib/identity"}}}},
			"volumes":         []any{deploymentplan.Object{"name": "identity", "persistentVolumeClaim": deploymentplan.Object{"claimName": "original-identity"}}},
		}}},
	}}
}

func TestControllerIdentityTargetPermissionPolicy(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "production"} {
		t.Run(environment, func(t *testing.T) {
			for _, policy := range []string{"Always", "OnRootMismatch"} {
				r := identityPermissionFixture(environment)
				security := deploymentplan.Map(environmentPKIPod(r.Desired)["securityContext"])
				security["fsGroup"] = 10001
				security["fsGroupChangePolicy"] = policy
				err := controllerIdentityTargetPolicy(r, identityPermissionAlpine)
				var b *deploymentplan.Blocker
				if !errors.As(err, &b) || b.ID != "deployment.pki.identity-permissions" || b.Resource != r.ID || !strings.Contains(b.Cause, "fsGroup") {
					t.Fatalf("unsafe volume policy accepted: %v", err)
				}
			}
			r := identityPermissionFixture(environment)
			dirs, err := controllerIdentityDirectories(r)
			if err != nil || len(dirs) != 1 || dirs[0].claim != "original-identity" {
				t.Fatalf("safe explicit mapping rejected: %v", err)
			}
			deploymentplan.Map(r.Desired["spec"])["replicas"] = 0
			if dirs, err = controllerIdentityDirectories(r); err != nil || len(dirs) != 0 {
				t.Fatal("disabled controller probed")
			}
		})
	}
}

func TestControllerIdentityReadOnlyEvidence(t *testing.T) {
	for _, failure := range []string{"", "mode", "owner", "unavailable", "foreign-owner", "not-ready", "not-running", "changed-pod", "wrong-pvc", "symlink", "private-stderr"} {
		t.Run(failure, func(t *testing.T) {
			r := identityPermissionFixture("dev")
			podSpec := environmentPKIPod(r.Desired)
			podSpecCopy := deploymentplan.Object{}
			for k, v := range podSpec {
				podSpecCopy[k] = v
			}
			podSpecCopy["securityContext"] = deploymentplan.Object{"runAsUser": 10001, "fsGroup": 10001}
			if failure == "wrong-pvc" {
				podSpecCopy["volumes"] = []any{deploymentplan.Object{"name": "identity", "persistentVolumeClaim": deploymentplan.Object{"claimName": "foreign"}}}
			}
			uid := "original-controller"
			if failure == "foreign-owner" {
				uid = "foreign-controller"
			}
			ready := failure != "not-ready"
			pod := deploymentplan.Object{"metadata": deploymentplan.Object{"name": "original-pod", "uid": "original-pod-uid", "ownerReferences": []any{deploymentplan.Object{"kind": "ReplicaSet", "uid": "original-rs", "controller": true}}}, "spec": podSpecCopy, "status": deploymentplan.Object{"containerStatuses": []any{deploymentplan.Object{"name": "pki-controller", "ready": ready, "state": deploymentplan.Object{"running": deploymentplan.Object{}}}}}}
			if failure == "not-running" {
				deploymentplan.Map(identityPermissionList(deploymentplan.Map(pod["status"])["containerStatuses"])[0])["state"] = deploymentplan.Object{}
			}
			calls := 0
			read := func(args ...string) ([]byte, error) {
				command := strings.Join(args, " ")
				if strings.Contains(command, " exec ") {
					calls++
					if !strings.HasSuffix(command, "-- stat -c %a:%u /var/lib/identity") {
						t.Fatalf("unexpected executable: %s", command)
					}
					switch failure {
					case "mode":
						return []byte("770:10001\n"), nil
					case "owner":
						return []byte("700:0\n"), nil
					case "symlink":
						return []byte("777:10001\n"), nil
					case "unavailable", "private-stderr":
						return nil, errors.New("private-token-must-not-leak")
					}
					return []byte("700:10001\n"), nil
				}
				var response deploymentplan.Object
				switch {
				case strings.Contains(command, "get replicasets"):
					response = deploymentplan.Object{"items": []any{deploymentplan.Object{"metadata": deploymentplan.Object{"uid": "original-rs", "ownerReferences": []any{deploymentplan.Object{"kind": "Deployment", "uid": uid, "controller": true}}}}}}
				case strings.Contains(command, "get pods"):
					response = deploymentplan.Object{"items": []any{pod}}
				case strings.Contains(command, "get pod original-pod"):
					if failure == "changed-pod" {
						response = deploymentplan.Object{"metadata": deploymentplan.Object{"uid": "replacement"}}
					} else {
						response = pod
					}
				default:
					t.Fatalf("unexpected read: %s", command)
				}
				return json.Marshal(response)
			}
			err := checkControllerIdentityPermissions(&deploymentplan.Plan{Resources: []deploymentplan.Resource{r}}, "/isolated/dev/kubeconfig", identityPermissionAlpine, read)
			if failure == "" || failure == "not-ready" {
				if err != nil || calls != 1 {
					t.Fatalf("private evidence rejected: %v", err)
				}
			} else {
				var b *deploymentplan.Blocker
				if !errors.As(err, &b) || b.ID != "deployment.pki.identity-permissions" || strings.Contains(err.Error(), "private-token") {
					t.Fatalf("invalid evidence not safely blocked: %v", err)
				}
			}
		})
	}
}

func TestControllerIdentityUnsafeTargetDoesNotExecuteProbe(t *testing.T) {
	r := identityPermissionFixture("dev")
	deploymentplan.Map(environmentPKIPod(r.Desired)["securityContext"])["fsGroup"] = 10001
	err := checkControllerIdentityPermissions(&deploymentplan.Plan{Resources: []deploymentplan.Resource{r}}, "isolated", identityPermissionAlpine, func(...string) ([]byte, error) { t.Fatal("unsafe target reached exec"); return nil, nil })
	if err == nil {
		t.Fatal("unsafe target passed")
	}
}

func TestControllerIdentityTargetMappingCannotBeAmbiguous(t *testing.T) {
	for _, problem := range []string{"missing-state", "relative-state", "secret-volume", "read-only", "sub-path", "missing-owner", "bad-replicas"} {
		t.Run(problem, func(t *testing.T) {
			r := identityPermissionFixture("dev")
			pod := environmentPKIPod(r.Desired)
			c := deploymentplan.Map(identityPermissionList(pod["containers"])[0])
			mount := deploymentplan.Map(identityPermissionList(c["volumeMounts"])[0])
			switch problem {
			case "missing-state":
				c["env"] = nil
			case "relative-state":
				deploymentplan.Map(identityPermissionList(c["env"])[0])["value"] = "relative/client.json"
			case "secret-volume":
				deploymentplan.Map(identityPermissionList(pod["volumes"])[0])["persistentVolumeClaim"] = nil
			case "read-only":
				mount["readOnly"] = true
			case "sub-path":
				mount["subPath"] = "other"
			case "missing-owner":
				delete(deploymentplan.Map(pod["securityContext"]), "runAsUser")
			case "bad-replicas":
				deploymentplan.Map(r.Desired["spec"])["replicas"] = "invalid"
			}
			if _, err := controllerIdentityDirectories(r); err == nil {
				t.Fatal("unverified identity mapping accepted")
			}
		})
	}
	r := identityPermissionFixture("dev")
	delete(deploymentplan.Map(r.Desired["spec"]), "replicas")
	if dirs, err := controllerIdentityDirectories(r); err != nil || len(dirs) != 1 {
		t.Fatalf("default nonzero replicas skipped: %v", err)
	}
}
