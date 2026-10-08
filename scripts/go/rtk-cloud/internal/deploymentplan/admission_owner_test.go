package deploymentplan

import "testing"

func TestOriginalAdmissionNameRequiresUIDAndExactEnvironmentEndpoints(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			in, _, _ := fixture(t, environment, "Deployment")
			wanted := Object{"apiVersion": "admissionregistration.k8s.io/v1", "kind": "ValidatingWebhookConfiguration", "metadata": Object{"name": "ingress-nginx-admission"}, "webhooks": []any{Object{"clientConfig": Object{"service": Object{"namespace": in.Stack + "-ingress", "name": "ingress-nginx-controller-admission"}}}}}
			current := Clone(wanted)
			Map(current["metadata"])["uid"] = "original-webhook"
			Map(current["metadata"])["resourceVersion"] = "1"
			definition := Definition{Object: wanted, Phase: "configuration"}
			owners := map[string]string{ID(wanted): "original-webhook"}
			plan, err := Compile(in, []Definition{definition}, []Object{current}, owners)
			if err != nil || plan.Resources[0].UID != "original-webhook" || plan.Resources[0].Action != "update" {
				t.Fatal("original admission could not be retained", err)
			}
			for _, scenario := range []string{"missing-uid", "foreign-live", "foreign-target", "new-unowned"} {
				desired, live := Clone(wanted), Clone(current)
				adoption := map[string]string{ID(wanted): "original-webhook"}
				switch scenario {
				case "missing-uid":
					adoption[ID(wanted)] = "another-owner"
				case "foreign-live":
					Map(Map(Map(live["webhooks"].([]any)[0])["clientConfig"])["service"])["namespace"] = "foreign-ingress"
				case "foreign-target":
					Map(Map(Map(desired["webhooks"].([]any)[0])["clientConfig"])["service"])["namespace"] = "foreign-ingress"
				case "new-unowned":
					live = nil
				}
				if _, err := Compile(in, []Definition{{Object: desired, Phase: "configuration"}}, []Object{live}, adoption); err == nil {
					t.Fatal("unverified admission allowed", scenario)
				}
			}
		})
	}
}
