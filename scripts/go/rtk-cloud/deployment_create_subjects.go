package main

import (
	"slices"
)

// Session targets represent installed owners. They are narrower than a reusable
// issuer policy and never include the one-time provisioner itself.
func environmentCreateBootstrapSubjects(cfg deploymentConfig) ([]string, error) {
	if cfg.Environment != "dev" && cfg.Environment != "staging" {
		return nil, environmentCreateBootstrapBlocker("fresh managed identity enrollment requires an explicit non-production environment")
	}
	values := cfg.Values
	if values["CLOUD_STACK_NAME"] != "video-cloud-"+cfg.Environment {
		return nil, environmentCreateBootstrapBlocker("bootstrap subjects require the selected exact stack")
	}
	subjects := []string{"service:account-manager", "service:certissuer", "service:factory-enroll", "service:pki-controller", "service:video-cloud-api", "service:video-cloud-logingester", "service:pkibroker", "service:emqx-pki", "service:openbao"}
	// The ordinary registered workers use their maintained private MQTT role
	// credentials. They have no native Service identity owner and cannot attest
	// a Service client installation merely because registration is enabled.
	slices.Sort(subjects)
	return subjects, nil
}
