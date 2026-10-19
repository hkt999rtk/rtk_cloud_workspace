package deploymentplan

import "testing"

func TestNativeOmittedWritableMountKeepsReadOnlyAndIdentityGuards(t *testing.T) {
	for _, mode := range []string{"omitted-false", "explicit-false", "readonly-omitted", "readonly-injected", "mount-source-changed", "image-changed"} {
		t.Run(mode, func(t *testing.T) {
			body := Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": Object{"namespace": "video-cloud-dev-account-manager", "name": "account-manager"}, "spec": Object{"template": Object{"spec": Object{"containers": []any{Object{"name": "app", "image": "original@sha256:approved", "volumeMounts": []any{Object{"name": "original-state", "mountPath": "/var/lib/state", "readOnly": false}}}}, "initContainers": []any{Object{"name": "prepare", "image": "original@sha256:approved", "volumeMounts": []any{Object{"name": "original-state", "mountPath": "/var/lib/state", "readOnly": false}}}}}}}}
			admitted := Clone(body)
			pod := Map(Map(Map(admitted["spec"])["template"])["spec"])
			container := Map(pod["containers"].([]any)[0])
			mount := Map(container["volumeMounts"].([]any)[0])
			switch mode {
			case "omitted-false":
				delete(mount, "readOnly")
				delete(Map(Map(pod["initContainers"].([]any)[0])["volumeMounts"].([]any)[0]), "readOnly")
			case "readonly-omitted":
				Map(Map(Map(Map(Map(body["spec"])["template"])["spec"])["containers"].([]any)[0])["volumeMounts"].([]any)[0])["readOnly"] = true
				delete(mount, "readOnly")
			case "readonly-injected":
				mount["readOnly"] = true
			case "mount-source-changed":
				mount["name"] = "foreign-state"
			case "image-changed":
				container["image"] = "foreign@sha256:other"
			}
			oldBody, oldAdmitted := Digest(body), Digest(admitted)
			want := mode == "omitted-false" || mode == "explicit-false"
			if Matches(body, admitted) != want || matchesAdmission(body, admitted) != want || (len(admissionMismatchPaths(body, admitted)) == 0) != want {
				t.Fatal("native writable default or drift guard differs", mode)
			}
			if Digest(body) != oldBody || Digest(admitted) != oldAdmitted {
				t.Fatal("frozen source objects mutated")
			}
		})
	}
}
