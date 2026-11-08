package main

import (
	"context"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// This resolves the expected ACK subject from original admitted enrollment
// intent. It is not evidence of current key possession, leaf health or an ACK.
// Discovery must precede recovery of the managed OpenBao TLS transport.
func (r *maintenanceRuntime) custodyConsumerID(ctx context.Context, ns, name, container string, source deploymentplan.Object) (string, error) {
	bad := func() (string, error) {
		return "", environmentCreateBootstrapBlocker("CRL consumer differs from its original admitted custody enrollment purpose")
	}
	parts := strings.Split(mqttFoundationEnv(source, "PKI_STATE_RECORD_PREFIX"), "/")
	if len(parts) != 3 || parts[0] != r.cfg.Environment || mqttFoundationEnv(source, "PKI_STATE_KV_MOUNT") != r.cfg.Values["PKI_STATE_KV_MOUNT"] || mqttFoundationEnv(source, "PKI_STATE_OPENBAO_ROLE") != environmentCustodyRoleName(strings.Join(parts, "/")) || mqttFoundationEnv(source, "PKI_ENVIRONMENT") != r.cfg.Environment {
		return bad()
	}
	binding, err := readEnvironmentCustodyBootstrapBinding(r.cfg, r.store, parts[1])
	if err != nil {
		return "", err
	}
	pod, err := r.read(ctx, "Pod/"+ns+"/"+name)
	meta, spec := deploymentplan.Map(pod["metadata"]), deploymentplan.Map(pod["spec"])
	uid := deploymentplan.String(meta["uid"])
	if err != nil || uid == "" || meta["namespace"] != ns || meta["name"] != name || meta["deletionTimestamp"] != nil || deploymentplan.Digest(managedNamedField(spec["containers"], container)) != deploymentplan.Digest(source) {
		return bad()
	}
	parentID, _, err := r.consumerDeployment(ctx, crlMaintenanceConsumer{Namespace: ns, Pod: name, PodUID: uid})
	if err != nil || parentID != "Deployment/"+ns+"/"+parts[2] {
		return bad()
	}
	var owner *environmentCustodyOwnerBinding
	for _, candidate := range binding.Owners {
		if candidate.ID == parts[2] && candidate.Namespace == ns && spec["serviceAccountName"] == candidate.ServiceAccount {
			if owner != nil {
				return bad()
			}
			copy := candidate
			owner = &copy
		}
	}
	if owner == nil {
		return bad()
	}
	subject := ""
	for _, value := range certIssuerObjectList(source["env"]) {
		e := deploymentplan.Map(value)
		if !strings.HasSuffix(deploymentplan.String(e["name"]), "_IDENTITY_STATE") {
			continue
		}
		for _, purpose := range owner.Clients {
			if e["value"] != purpose.StatePath {
				continue
			}
			if subject != "" && subject != purpose.Subject {
				return bad()
			}
			subject = purpose.Subject
		}
	}
	if !strings.HasPrefix(subject, "service:") || !environmentCreateConsumerID.MatchString(strings.TrimPrefix(subject, "service:")) {
		return bad()
	}
	return strings.TrimPrefix(subject, "service:"), nil
}
