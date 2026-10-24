package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const environmentLogIngesterPVCWriterSchema = "rtk-cloud.log-ingester-pvc-writer/v1"

type environmentLogIngesterPVCWriterProfile struct {
	Schema, DeploymentUID, PodName, PodUID, ClaimName, ClaimUID, PVName, PVUID, NativeID string
	AppUID, AppGID, RootUID, RootGID                                                     int
	RootMode                                                                             string
}

// Set the native process group, without fsGroup's recursive volume mutation.
// The image's default UID10001/GID999 cannot atomically replace CRLs in the
// original root-owned2775 directory whose existing writer group is10001.
func bindEnvironmentLogIngesterPVCWriter(object, container deploymentplan.Object) error {
	if !environmentLogMQTTConsumer(object, container) {
		return logMQTTBlocker(object, "original LogIngester app container required")
	}
	security := deploymentplan.Map(container["securityContext"])
	if security == nil {
		security = deploymentplan.Object{}
		container["securityContext"] = security
	}
	if v := security["runAsUser"]; v != nil && v != float64(10001) {
		return logMQTTBlocker(object, "foreign LogIngester runtime user")
	}
	if v := security["runAsGroup"]; v != nil && v != float64(999) && v != float64(10001) {
		return logMQTTBlocker(object, "foreign LogIngester runtime group")
	}
	security["runAsUser"], security["runAsGroup"] = float64(10001), float64(10001)
	return nil
}

func environmentLogIngesterPVCWriterSource(cfg deploymentConfig, original []deploymentplan.Definition) (deploymentplan.Definition, deploymentplan.Object, string, error) {
	var selected deploymentplan.Definition
	count := 0
	for _, d := range original {
		if deploymentplan.ID(d.Object) == "Deployment/"+cfg.Values["CLOUD_STACK_NAME"]+"-video-cloud/video-cloud-logingester" {
			selected = d
			count++
		}
	}
	if count != 1 || selected.Retire {
		return selected, nil, "", fmt.Errorf("one original LogIngester declaration required")
	}
	var app deploymentplan.Object
	for _, raw := range certIssuerObjectList(environmentPKIPod(selected.Object)["containers"]) {
		c := deploymentplan.Map(raw)
		if environmentLogMQTTConsumer(selected.Object, c) {
			if app != nil {
				return selected, nil, "", fmt.Errorf("ambiguous LogIngester app")
			}
			app = c
		}
	}
	if app == nil || validateEnvironmentLogMQTTRollout(selected.Object, app) != nil || mqttFoundationEnv(app, logMQTTIdentityPrefix+"STATE") == "" {
		return selected, nil, "", fmt.Errorf("original managed LogIngester identity and single writer required")
	}
	claim := ""
	for _, raw := range certIssuerObjectList(app["volumeMounts"]) {
		m := deploymentplan.Map(raw)
		if m["mountPath"] != "/var/lib/mqtt-trust" {
			continue
		}
		if m["readOnly"] == true || m["subPath"] != nil || m["subPathExpr"] != nil || claim != "" {
			return selected, nil, "", fmt.Errorf("original writable MQTT trust mount required")
		}
		for _, raw := range certIssuerObjectList(environmentPKIPod(selected.Object)["volumes"]) {
			v := deploymentplan.Map(raw)
			if v["name"] == m["name"] {
				claim = deploymentplan.String(deploymentplan.Map(v["persistentVolumeClaim"])["claimName"])
			}
		}
	}
	if claim == "" {
		return selected, nil, "", fmt.Errorf("original MQTT trust PVC required")
	}
	return selected, app, claim, nil
}

func environmentPostCleanupLogIngesterDefinitions(cfg deploymentConfig, original []deploymentplan.Definition, p environmentLogIngesterPVCWriterProfile) ([]deploymentplan.Definition, error) {
	d, _, claim, err := environmentLogIngesterPVCWriterSource(cfg, original)
	if err != nil {
		return nil, err
	}
	if p.Schema != environmentLogIngesterPVCWriterSchema || p.ClaimName != claim || p.AppUID != 10001 || p.AppGID != 999 || p.RootUID != 0 || p.RootGID != 10001 || p.RootMode != "2775" || p.PodName == "" || p.PVName == "" || p.NativeID == "" {
		return nil, logMQTTBlocker(d.Object, "original PVC writer metadata differs")
	}
	for _, uid := range []string{p.DeploymentUID, p.PodUID, p.ClaimUID, p.PVUID} {
		if !environmentBootstrapOwnerUUID.MatchString(uid) {
			return nil, logMQTTBlocker(d.Object, "original PVC writer owner unavailable")
		}
	}
	if uid := deploymentplan.String(deploymentplan.Map(d.Object["metadata"])["uid"]); uid != p.DeploymentUID {
		return nil, logMQTTBlocker(d.Object, "original LogIngester UID differs")
	}
	d.Object = deploymentplan.Clone(d.Object)
	for _, raw := range certIssuerObjectList(environmentPKIPod(d.Object)["containers"]) {
		c := deploymentplan.Map(raw)
		if environmentLogMQTTConsumer(d.Object, c) {
			if err := bindEnvironmentLogIngesterPVCWriter(d.Object, c); err != nil {
				return nil, err
			}
		}
	}
	d.Retain = false
	return []deploymentplan.Definition{d}, nil
}

// Read-only qualification binds the real original owner and allocation ledger;
// the returned public metadata cannot authorize a new identity or volume.
func qualifyEnvironmentPostCleanupLogIngesterProfile(ctx context.Context, cfg deploymentConfig, b environmentKubernetesBackend, original []deploymentplan.Definition, ledger environmentCreateResourceLedger) (environmentLogIngesterPVCWriterProfile, error) {
	p := environmentLogIngesterPVCWriterProfile{}
	d, app, claimName, err := environmentLogIngesterPVCWriterSource(cfg, original)
	if err != nil {
		return p, err
	}
	bad := func() (environmentLogIngesterPVCWriterProfile, error) {
		return p, logMQTTBlocker(d.Object, "current original LogIngester/PVC/native writer metadata differs")
	}
	id := deploymentplan.ID(d.Object)
	ns := deploymentplan.String(deploymentplan.Map(d.Object["metadata"])["namespace"])
	if b.Store.Environment != cfg.Environment || ledger.Environment != cfg.Environment || ledger.Stack != cfg.Values["CLOUD_STACK_NAME"] {
		return bad()
	}
	live, err := b.Read(ctx, id)
	uid := deploymentplan.String(deploymentplan.Map(d.Object["metadata"])["uid"])
	// The input definitions carry literal accepted workload owners. The
	// resource ledger is intentionally sparse until final ledger capture.
	if recorded := ledger.Kubernetes[id]; recorded != "" && recorded != uid {
		return bad()
	}
	if err != nil || !environmentBootstrapOwnerUUID.MatchString(uid) || deploymentplan.Map(live["metadata"])["uid"] != uid || deploymentplan.Map(live["metadata"])["deletionTimestamp"] != nil || !deploymentplan.Matches(d.Object, live) {
		return bad()
	}
	claimID := "PersistentVolumeClaim/" + ns + "/" + claimName
	var owner *environmentCreateVolumeOwner
	for i := range ledger.Volumes {
		v := &ledger.Volumes[i]
		if v.Namespace == ns && v.PVCName == claimName {
			if owner != nil {
				return bad()
			}
			owner = v
		}
	}
	if owner == nil {
		return bad()
	}
	acceptedClaimUID, acceptedClaims := "", 0
	for _, definition := range original {
		if deploymentplan.ID(definition.Object) == claimID && !definition.Retire {
			acceptedClaimUID = deploymentplan.String(deploymentplan.Map(definition.Object["metadata"])["uid"])
			acceptedClaims++
		}
	}
	if acceptedClaims != 1 || !environmentBootstrapOwnerUUID.MatchString(acceptedClaimUID) || acceptedClaimUID != owner.PVCUID {
		return bad()
	}
	if recorded := ledger.Kubernetes[claimID]; recorded != "" && recorded != acceptedClaimUID {
		return bad()
	}
	claim, err := b.Read(ctx, claimID)
	if err != nil {
		return bad()
	}
	pv, err := b.Read(ctx, "PersistentVolume//"+owner.PVName)
	if err != nil {
		return bad()
	}
	cm, cs := deploymentplan.Map(claim["metadata"]), deploymentplan.Map(claim["spec"])
	pm, ps := deploymentplan.Map(pv["metadata"]), deploymentplan.Map(pv["spec"])
	ref, csi := deploymentplan.Map(ps["claimRef"]), deploymentplan.Map(ps["csi"])
	handle := deploymentplan.String(csi["volumeHandle"])
	if prefix, suffix, ok := strings.Cut(handle, "-"); ok && strings.HasPrefix(suffix, "pvc-") {
		handle = prefix
	}
	if cm["uid"] != owner.PVCUID || cm["deletionTimestamp"] != nil || cs["volumeName"] != owner.PVName || deploymentplan.Map(claim["status"])["phase"] != "Bound" || pm["uid"] != owner.PVUID || pm["deletionTimestamp"] != nil || deploymentplan.Map(pv["status"])["phase"] != "Bound" || ref["uid"] != owner.PVCUID || ref["name"] != claimName || ref["namespace"] != ns || csi["driver"] != "linodebs.csi.linode.com" || handle != owner.NativeID || ps["persistentVolumeReclaimPolicy"] != "Retain" || ps["storageClassName"] != cs["storageClassName"] {
		return bad()
	}
	raw, err := b.command(ctx, nil, "--namespace", ns, "get", "pods", "-l", "app.kubernetes.io/name=video-cloud-logingester", "-o", "json")
	var pods struct {
		Items []deploymentplan.Object `json:"items"`
	}
	if err != nil || json.Unmarshal(raw, &pods) != nil || len(pods.Items) != 1 {
		return bad()
	}
	pod := pods.Items[0]
	meta := deploymentplan.Map(pod["metadata"])
	if meta["deletionTimestamp"] != nil || deploymentplan.Map(pod["status"])["phase"] != "Running" {
		return bad()
	}
	refs := certIssuerObjectList(meta["ownerReferences"])
	if len(refs) != 1 {
		return bad()
	}
	r := deploymentplan.Map(refs[0])
	if r["kind"] != "ReplicaSet" || r["controller"] != true {
		return bad()
	}
	rs, err := b.Read(ctx, "ReplicaSet/"+ns+"/"+deploymentplan.String(r["name"]))
	if err != nil || deploymentplan.Map(rs["metadata"])["uid"] != r["uid"] {
		return bad()
	}
	rsrefs := certIssuerObjectList(deploymentplan.Map(rs["metadata"])["ownerReferences"])
	if len(rsrefs) != 1 || deploymentplan.Map(rsrefs[0])["controller"] != true || deploymentplan.Map(rsrefs[0])["kind"] != "Deployment" || deploymentplan.Map(rsrefs[0])["uid"] != uid {
		return bad()
	}
	// Native admission injects one standard API token volume/mount. Validate
	// that exact public projection before comparing the declared Pod fields.
	podSpec := deploymentplan.Clone(deploymentplan.Map(pod["spec"]))
	if !logIngesterPVCWriterAdmissionProjection(podSpec) {
		return bad()
	}
	found := 0
	for _, v := range certIssuerObjectList(podSpec["containers"]) {
		c := deploymentplan.Map(v)
		if c["name"] == "app" {
			if !deploymentplan.Matches(deploymentplan.Object{"kind": "Pod", "spec": deploymentplan.Object{"containers": []any{app}}}, deploymentplan.Object{"kind": "Pod", "spec": deploymentplan.Object{"containers": []any{c}}}) {
				return bad()
			}
			found++
		}
	}
	if found != 1 || !deploymentplan.Matches(deploymentplan.Object{"kind": "Pod", "spec": deploymentplan.Object{"volumes": environmentPKIPod(d.Object)["volumes"]}}, deploymentplan.Object{"kind": "Pod", "spec": podSpec}) {
		return bad()
	}
	p = environmentLogIngesterPVCWriterProfile{Schema: environmentLogIngesterPVCWriterSchema, DeploymentUID: uid, PodName: deploymentplan.String(meta["name"]), PodUID: deploymentplan.String(meta["uid"]), ClaimName: claimName, ClaimUID: owner.PVCUID, PVName: owner.PVName, PVUID: owner.PVUID, NativeID: owner.NativeID}
	raw, err = b.command(ctx, nil, "--namespace", ns, "exec", "pod/"+p.PodName, "--container", "app", "--", "/bin/sh", "-ec", `id -u; id -g; id -G; test -d "$1" && test ! -L "$1"; stat -c '%a %u %g' "$1"`, "log-ingester-writer-metadata", "/var/lib/mqtt-trust")
	fields := strings.Fields(string(raw))
	if err != nil || len(fields) != 6 || fields[2] != "999" {
		return bad()
	}
	if _, err = fmt.Sscan(fields[0], &p.AppUID); err != nil {
		return bad()
	}
	if _, err = fmt.Sscan(fields[1], &p.AppGID); err != nil {
		return bad()
	}
	p.RootMode = fields[3]
	if _, err = fmt.Sscan(fields[4], &p.RootUID); err != nil {
		return bad()
	}
	if _, err = fmt.Sscan(fields[5], &p.RootGID); err != nil {
		return bad()
	}
	closing, closeErr := b.Read(ctx, "Pod/"+ns+"/"+p.PodName)
	if closeErr != nil || deploymentplan.Map(closing["metadata"])["uid"] != p.PodUID || deploymentplan.Map(closing["metadata"])["deletionTimestamp"] != nil {
		return bad()
	}
	if _, err := environmentPostCleanupLogIngesterDefinitions(cfg, original, p); err != nil {
		return p, err
	}
	return p, ctx.Err()
}

// Ignore only the native service-account admission projection, never an extra
// identity, configuration, PVC or application mount.
func logIngesterPVCWriterAdmissionProjection(pod deploymentplan.Object) bool {
	name := ""
	var volumes []any
	for _, raw := range certIssuerObjectList(pod["volumes"]) {
		v := deploymentplan.Map(raw)
		n := deploymentplan.String(v["name"])
		if !strings.HasPrefix(n, "kube-api-access-") {
			volumes = append(volumes, raw)
			continue
		}
		if name != "" || len(n) != len("kube-api-access-")+5 {
			return false
		}
		expected := deploymentplan.Object{"name": n, "projected": deploymentplan.Object{"defaultMode": float64(0644), "sources": []any{
			deploymentplan.Object{"serviceAccountToken": deploymentplan.Object{"expirationSeconds": float64(3607), "path": "token"}},
			deploymentplan.Object{"configMap": deploymentplan.Object{"name": "kube-root-ca.crt", "items": []any{deploymentplan.Object{"key": "ca.crt", "path": "ca.crt"}}}},
			deploymentplan.Object{"downwardAPI": deploymentplan.Object{"items": []any{deploymentplan.Object{"fieldRef": deploymentplan.Object{"apiVersion": "v1", "fieldPath": "metadata.namespace"}, "path": "namespace"}}}},
		}}}
		if deploymentplan.Digest(v) != deploymentplan.Digest(expected) {
			return false
		}
		name = n
	}
	if name == "" {
		return true
	}
	pod["volumes"] = volumes
	for _, raw := range certIssuerObjectList(pod["containers"]) {
		c := deploymentplan.Map(raw)
		count := 0
		var mounts []any
		for _, raw := range certIssuerObjectList(c["volumeMounts"]) {
			m := deploymentplan.Map(raw)
			if m["name"] != name {
				mounts = append(mounts, raw)
				continue
			}
			expected := deploymentplan.Object{"name": name, "mountPath": "/var/run/secrets/kubernetes.io/serviceaccount", "readOnly": true}
			if deploymentplan.Digest(m) != deploymentplan.Digest(expected) {
				return false
			}
			count++
		}
		if count != 1 {
			return false
		}
		c["volumeMounts"] = mounts
	}
	return true
}
