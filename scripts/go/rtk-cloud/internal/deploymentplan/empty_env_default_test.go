package deploymentplan

import "testing"

func TestNativeOmittedEmptyEnvironmentKeepsSourceAndImageGuards(t *testing.T) {
	for _, mode := range []string{"omitted", "explicit-empty", "injected-value", "injected-secret", "image-drift"} {
		t.Run(mode, func(t *testing.T) {
			body := Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": Object{"name": "account-manager", "namespace": "video-cloud-dev-account-manager"}, "spec": Object{"template": Object{"spec": Object{"containers": []any{Object{"name": "app", "image": "approved@sha256:original", "env": []any{Object{"name": "DISABLED_LISTENER", "value": ""}}}}}}}}
			admitted := Clone(body)
			container := Map(Map(Map(Map(admitted["spec"])["template"])["spec"])["containers"].([]any)[0])
			env := Map(container["env"].([]any)[0])
			switch mode {
			case "omitted":
				delete(env, "value")
			case "injected-value":
				env["value"] = "unexpected-enabled-setting"
			case "injected-secret":
				delete(env, "value")
				env["valueFrom"] = Object{"secretKeyRef": Object{"name": "foreign", "key": "password"}}
			case "image-drift":
				container["image"] = "different@sha256:foreign"
			}
			beforeBody, beforeAdmitted := Digest(body), Digest(admitted)
			wanted := mode == "omitted" || mode == "explicit-empty"
			if Matches(body, admitted) != wanted || matchesAdmission(body, admitted) != wanted || (len(admissionMismatchPaths(body, admitted)) == 0) != wanted {
				t.Fatal("default or mutation guard differs", mode)
			}
			if Digest(body) != beforeBody || Digest(admitted) != beforeAdmitted {
				t.Fatal("frozen objects changed in place")
			}
		})
	}
}

func TestNativeEmptyEnvironmentWireFormsRemainSourceFenced(t *testing.T) {
	setting := func(form string) Object {
		entry := Object{"name": "DISABLED_LISTENER"}
		switch form {
		case "null":
			entry["value"], entry["valueFrom"] = nil, nil
		case "empty":
			entry["value"] = ""
		case "nonempty":
			entry["value"] = "enabled"
		case "secret", "config":
			key := "secretKeyRef"
			if form == "config" {
				key = "configMapKeyRef"
			}
			entry["valueFrom"] = Object{key: Object{"name": "foreign", "key": "setting"}}
		case "boolean":
			entry["value"] = false
		}
		return entry
	}
	workload := func(env Object) Object {
		return Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": Object{"name": "owned", "namespace": "video-cloud-dev-platform", "uid": "original"}, "spec": Object{"template": Object{"spec": Object{"containers": []any{Object{"name": "app", "image": "approved@sha256:original", "env": []any{env}}}}}}}
	}
	for _, desired := range []string{"absent", "null", "empty"} {
		for _, observed := range []string{"absent", "null", "empty", "nonempty", "secret", "config", "boolean"} {
			t.Run(desired+"/"+observed, func(t *testing.T) {
				body, admitted := workload(setting(desired)), workload(setting(observed))
				bodySHA, admittedSHA := Digest(body), Digest(admitted)
				pass := observed == "absent" || observed == "null" || observed == "empty"
				if Matches(body, admitted) != pass || matchesAdmission(body, admitted) != pass || (len(admissionMismatchPaths(body, admitted)) == 0) != pass {
					t.Fatal("native empty value wire equivalence changed source or literal guards")
				}
				if Digest(body) != bodySHA || Digest(admitted) != admittedSHA {
					t.Fatal("native admission comparison changed frozen input")
				}
			})
		}
	}
	// A real source remains mandatory when the desired value is source-bound.
	for _, observed := range []string{"absent", "null", "empty", "nonempty", "config", "secret"} {
		body, admitted := workload(setting("secret")), workload(setting(observed))
		if matchesAdmission(body, admitted) != (observed == "secret") {
			t.Fatal("native empty default erased an explicit credential source", observed)
		}
	}
}
