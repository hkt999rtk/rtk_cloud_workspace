package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strconv"
	"strings"
	"time"
)

const maintenanceWriterAccessScript = `directory=$(dirname "$1")
location=$directory
while test "$location" != /; do test ! -L "$location"; location=$(dirname "$location"); done
test -d "$directory"
writable=no
if test -w "$directory"; then writable=yes; fi
printf '%s %s %s %s %s\n' "$(id -u)" "$(id -g)" "$(stat -c '%a %u %g' "$directory")" "$writable" "$(id -G)"`

type maintenanceWriterAccess struct {
	UID, Group int64
	Writable   bool
}
type maintenanceWriterRepair struct {
	Consumer                                                       crlMaintenanceConsumer
	UID, Group                                                     int64
	Deployment, OwnerUID, Version, Claim, ClaimUID, Image, SpecSHA string
	Container                                                      int
	Patched                                                        bool
}

func parseMaintenanceWriterAccess(raw []byte) (maintenanceWriterAccess, error) {
	var out maintenanceWriterAccess
	fields := strings.Fields(string(raw))
	fail := func() (maintenanceWriterAccess, error) {
		return out, errors.New("CRL writer permissions do not match a missing original group")
	}
	if len(fields) < 7 {
		return fail()
	}
	uid, e1 := strconv.ParseInt(fields[0], 10, 64)
	gid, e2 := strconv.ParseInt(fields[1], 10, 64)
	mode, e3 := strconv.ParseUint(fields[2], 8, 16)
	owner, e4 := strconv.ParseInt(fields[3], 10, 64)
	group, e5 := strconv.ParseInt(fields[4], 10, 64)
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil || uid <= 0 || gid < 0 || group < 0 {
		return fail()
	}
	out = maintenanceWriterAccess{UID: uid, Group: group, Writable: fields[5] == "yes"}
	if out.Writable {
		return out, nil
	}
	if fields[5] != "no" || group != uid || group == gid || owner != 0 && owner != uid || mode&0030 != 0030 || mode&0002 != 0 || mode&05000 != 0 {
		return fail()
	}
	for _, g := range fields[6:] {
		if g == fields[4] {
			return fail()
		}
	}
	return out, nil
}

func (r *maintenanceRuntime) writerAccess(ctx context.Context, c crlMaintenanceConsumer) (maintenanceWriterAccess, error) {
	raw, err := r.command(ctx, nil, "--namespace", c.Namespace, "exec", "pod/"+c.Pod, "--container", c.Container, "--", "/bin/sh", "-ec", maintenanceWriterAccessScript, "crl-writer-access", c.StatePath)
	if err != nil {
		return maintenanceWriterAccess{}, fmt.Errorf("original CRL writer directory unavailable for %s/%s/%s", c.Namespace, c.Pod, c.Container)
	}
	return parseMaintenanceWriterAccess(raw)
}

func (r *maintenanceRuntime) consumerDeployment(ctx context.Context, c crlMaintenanceConsumer) (string, deploymentplan.Object, error) {
	object, err := r.read(ctx, "Pod/"+c.Namespace+"/"+c.Pod)
	if err != nil || deploymentplan.String(deploymentplan.Map(object["metadata"])["uid"]) != c.PodUID {
		return "", nil, errors.New("original CRL writer Pod changed")
	}
	for depth := 0; depth < 2; depth++ {
		var owner deploymentplan.Object
		for _, value := range certIssuerObjectList(deploymentplan.Map(object["metadata"])["ownerReferences"]) {
			ref := deploymentplan.Map(value)
			if ref["controller"] == true {
				if owner != nil {
					return "", nil, errors.New("ambiguous CRL writer owner")
				}
				owner = ref
			}
		}
		kind, name := deploymentplan.String(owner["kind"]), deploymentplan.String(owner["name"])
		if kind != "ReplicaSet" && kind != "Deployment" {
			return "", nil, errors.New("CRL writer correction requires original Deployment")
		}
		id := kind + "/" + c.Namespace + "/" + name
		parent, err := r.read(ctx, id)
		if err != nil || deploymentplan.Map(parent["metadata"])["uid"] != owner["uid"] {
			return "", nil, errors.New("original CRL writer owner changed")
		}
		if kind == "Deployment" {
			return id, parent, nil
		}
		object = parent
	}
	return "", nil, errors.New("original CRL writer Deployment unresolved")
}

func (r *maintenanceRuntime) qualifyWriterRepair(ctx context.Context, c crlMaintenanceConsumer, access maintenanceWriterAccess) (maintenanceWriterRepair, error) {
	out := maintenanceWriterRepair{Consumer: c, UID: access.UID, Group: access.Group, Container: -1}
	fail := func() (maintenanceWriterRepair, error) {
		return out, errors.New("nonwritable CRL state lacks an exclusive original Deployment group repair")
	}
	if access.Writable || access.UID <= 0 || access.UID != access.Group {
		return fail()
	}
	id, owner, err := r.consumerDeployment(ctx, c)
	if err != nil {
		return out, err
	}
	spec := deploymentplan.Map(owner["spec"])
	if deploymentplan.Map(spec["strategy"])["type"] != "Recreate" || fmt.Sprint(spec["replicas"]) != "1" {
		return fail()
	}
	pod, err := r.read(ctx, "Pod/"+c.Namespace+"/"+c.Pod)
	if err != nil || deploymentplan.Map(pod["metadata"])["uid"] != c.PodUID {
		return fail()
	}
	ps := deploymentplan.Map(pod["spec"])
	app := managedNamedField(ps["containers"], c.Container)
	security := deploymentplan.Map(app["securityContext"])
	if fmt.Sprint(security["runAsUser"]) != strconv.FormatInt(access.UID, 10) || security["runAsGroup"] != nil || deploymentplan.Map(ps["securityContext"])["runAsGroup"] != nil || deploymentplan.Map(ps["securityContext"])["fsGroup"] != nil {
		return fail()
	}
	claim := ""
	var stateMount, stateVolume deploymentplan.Object
	for _, value := range certIssuerObjectList(app["volumeMounts"]) {
		mount := deploymentplan.Map(value)
		base := deploymentplan.String(mount["mountPath"])
		if !strings.HasPrefix(c.StatePath, base+"/") {
			continue
		}
		if mount["readOnly"] == true || mount["subPath"] != nil || mount["subPathExpr"] != nil || claim != "" {
			return fail()
		}
		volume := managedNamedField(ps["volumes"], deploymentplan.String(mount["name"]))
		if deploymentplan.Map(volume["persistentVolumeClaim"])["readOnly"] == true {
			return fail()
		}
		claim = deploymentplan.String(deploymentplan.Map(volume["persistentVolumeClaim"])["claimName"])
		stateMount, stateVolume = mount, volume
	}
	if claim == "" || !filepath.IsAbs(c.StatePath) || filepath.Clean(c.StatePath) != c.StatePath {
		return fail()
	}
	out.Claim = "PersistentVolumeClaim/" + c.Namespace + "/" + claim
	pvc, err := r.read(ctx, out.Claim)
	if err != nil {
		return out, err
	}
	out.ClaimUID = deploymentplan.String(deploymentplan.Map(pvc["metadata"])["uid"])
	if out.ClaimUID == "" {
		return fail()
	}
	raw, err := r.command(ctx, nil, "--namespace", c.Namespace, "get", "pods", "-o", "json")
	if err != nil {
		return out, err
	}
	var pods struct {
		Items []deploymentplan.Object `json:"items"`
	}
	if json.Unmarshal(raw, &pods) != nil {
		return fail()
	}
	for _, other := range pods.Items {
		phase := deploymentplan.String(deploymentplan.Map(other["status"])["phase"])
		if phase == "Succeeded" || phase == "Failed" || deploymentplan.Map(other["metadata"])["uid"] == c.PodUID {
			continue
		}
		for _, v := range certIssuerObjectList(deploymentplan.Map(other["spec"])["volumes"]) {
			if deploymentplan.Map(deploymentplan.Map(v)["persistentVolumeClaim"])["claimName"] == claim {
				return fail()
			}
		}
	}
	for index, value := range certIssuerObjectList(environmentPKIPod(owner)["containers"]) {
		container := deploymentplan.Map(value)
		if container["name"] != c.Container {
			continue
		}
		if deploymentplan.Digest(managedNamedField(environmentPKIPod(owner)["volumes"], deploymentplan.String(stateVolume["name"]))) != deploymentplan.Digest(stateVolume) || deploymentplan.Digest(managedNamedField(container["volumeMounts"], deploymentplan.String(stateMount["name"]))) != deploymentplan.Digest(stateMount) || deploymentplan.Digest(container["env"]) != deploymentplan.Digest(app["env"]) || deploymentplan.Digest(container["command"]) != deploymentplan.Digest(app["command"]) || deploymentplan.Digest(container["args"]) != deploymentplan.Digest(app["args"]) {
			return fail()
		}
		sc := deploymentplan.Map(container["securityContext"])
		if fmt.Sprint(sc["runAsUser"]) != strconv.FormatInt(access.UID, 10) || container["image"] != app["image"] || sc["runAsGroup"] != nil && fmt.Sprint(sc["runAsGroup"]) != strconv.FormatInt(access.Group, 10) {
			return fail()
		}
		out.Container = index
		out.Image = deploymentplan.String(container["image"])
		out.Patched = sc["runAsGroup"] != nil
	}
	meta := deploymentplan.Map(owner["metadata"])
	out.SpecSHA = deploymentplan.Digest(spec)
	out.Deployment, out.OwnerUID, out.Version = id, deploymentplan.String(meta["uid"]), deploymentplan.String(meta["resourceVersion"])
	if out.Container < 0 || out.Image == "" || out.OwnerUID == "" || out.Version == "" {
		return fail()
	}
	return out, nil
}

func (r *maintenanceRuntime) applyWriterRepair(ctx context.Context, repair maintenanceWriterRepair) error {
	if err := r.lease.Check(ctx); err != nil {
		return err
	}
	access, err := r.writerAccess(ctx, repair.Consumer)
	if err != nil {
		return err
	}
	current, err := r.qualifyWriterRepair(ctx, repair.Consumer, access)
	freshVersion := current.Version
	current.Version = repair.Version // status-only resource version updates do not change authority
	if err != nil || deploymentplan.Digest(current) != deploymentplan.Digest(repair) {
		return errors.New("original CRL writer correction pins changed")
	}
	parts := strings.Split(repair.Deployment, "/")
	if !repair.Patched {
		prefix := "/spec/template/spec/containers/" + strconv.Itoa(repair.Container)
		patch, _ := json.Marshal([]map[string]any{{"op": "test", "path": "/metadata/uid", "value": repair.OwnerUID}, {"op": "test", "path": "/metadata/resourceVersion", "value": freshVersion}, {"op": "test", "path": prefix + "/name", "value": repair.Consumer.Container}, {"op": "test", "path": prefix + "/image", "value": repair.Image}, {"op": "add", "path": prefix + "/securityContext/runAsGroup", "value": repair.Group}})
		if _, err := r.command(ctx, nil, "--namespace", parts[1], "patch", "deployment", parts[2], "--type=json", "--patch", string(patch)); err != nil {
			return errors.New("original CRL writer group patch rejected")
		}
	}
	if _, err := r.command(ctx, nil, "--namespace", parts[1], "rollout", "status", "deployment/"+parts[2], "--timeout=180s"); err != nil {
		return errors.New("original CRL writer did not resume after group correction")
	}
	return nil
}

// Restarting a writer is safe only after its original upstream services have
// actual current state, native ACKs and readiness. Pod display names never stand
// in for authenticated consumer IDs.
var errMaintenanceWriterPending = errors.New("writer recovery upstream pending")

func (r *maintenanceRuntime) writerUpstreamReady(ctx context.Context, inventory crlMaintenanceInventory, records map[string]createRootCRLRecord) error {
	stack := r.cfg.Values["CLOUD_STACK_NAME"]
	for _, target := range []string{"Deployment/" + stack + "-video-cloud/pki-controller", "Deployment/" + stack + "-account-manager/account-manager"} {
		owner, err := r.read(ctx, target)
		if err != nil {
			return err
		}
		selector := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(owner["spec"])["selector"])["matchLabels"])
		if len(selector) == 0 {
			return errors.New("writer recovery upstream identity is unavailable")
		}
		found := false
		for _, c := range inventory.Consumers {
			if c.Namespace != strings.Split(target, "/")[1] {
				continue
			}
			pod, err := r.read(ctx, "Pod/"+c.Namespace+"/"+c.Pod)
			if err != nil {
				return err
			}
			match := true
			labels := deploymentplan.Map(deploymentplan.Map(pod["metadata"])["labels"])
			for key, value := range selector {
				if labels[key] != value {
					match = false
				}
			}
			if !match {
				continue
			}
			id, actual, err := r.consumerDeployment(ctx, c)
			if err != nil || id != target || deploymentplan.Map(actual["metadata"])["uid"] != deploymentplan.Map(owner["metadata"])["uid"] {
				return errors.New("writer recovery upstream owner changed")
			}
			found = true
			ready := false
			for _, value := range certIssuerObjectList(deploymentplan.Map(pod["status"])["containerStatuses"]) {
				s := deploymentplan.Map(value)
				if s["name"] == c.Container {
					ready = s["ready"] == true
				}
			}
			if !ready {
				return fmt.Errorf("%w: readiness", errMaintenanceWriterPending)
			}
			var issuer environmentCreateRegisteredIssuer
			for _, item := range inventory.Issuers {
				if item.Issuer.ID == c.IssuerID {
					issuer = item.Issuer
				}
			}
			state, err := r.command(ctx, nil, "--namespace", c.Namespace, "exec", "pod/"+c.Pod, "--container", c.Container, "--", "cat", c.StatePath)
			if err != nil {
				return err
			}
			var installed struct {
				Fingerprint string              `json:"issuer_fingerprint"`
				Terminal    string              `json:"terminal_issuer_id"`
				CRL         createRootCRLRecord `json:"crl"`
			}
			if json.Unmarshal(state, &installed) != nil || installed.Terminal != "" || installed.Fingerprint != deploymentplan.String(issuer.Document["certificate_fingerprint_sha256"]) {
				return errors.New("writer upstream CRL authority changed")
			}
			if _, err := validateMaintenanceCRL(r.cfg.Environment, issuer, installed.CRL, false, time.Now()); err != nil {
				return err
			}
			if installed.CRL.Digest != records[c.IssuerID].Digest {
				return fmt.Errorf("%w: current CRL state", errMaintenanceWriterPending)
			}
			ack, err := r.query(ctx, "SELECT EXISTS(SELECT 1 FROM pki_crl_acknowledgments WHERE issuer_id='"+c.IssuerID+"' AND digest='"+records[c.IssuerID].Digest+"' AND consumer_id='"+c.ConsumerID+"')")
			if err != nil {
				return err
			}
			if string(ack) == "f" {
				return fmt.Errorf("%w: real CRL ACK", errMaintenanceWriterPending)
			}
			if string(ack) != "t" {
				return errors.New("writer upstream ACK observation invalid")
			}
		}
		if !found {
			return errors.New("writer recovery upstream CRL coverage unavailable")
		}
	}
	for _, p := range inventory.Public {
		if p.Namespace == stack+"-account-manager" {
			if err := r.verifyPublicCRLs(ctx, []maintenancePublicCRL{p}, records); err != nil {
				if strings.Contains(err.Error(), "not reloaded") || strings.Contains(err.Error(), "not ready") {
					return fmt.Errorf("%w: %v", errMaintenanceWriterPending, err)
				}
				return err
			}
		}
	}
	return nil
}

func (r *maintenanceRuntime) repairDeferredWriters(ctx context.Context, inventory crlMaintenanceInventory, records map[string]createRootCRLRecord) (crlMaintenanceInventory, error) {
	if len(r.deferredWriters) == 0 {
		return inventory, nil
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		if err := r.lease.Check(ctx); err != nil {
			return inventory, err
		}
		if err := r.writerUpstreamReady(ctx, inventory, records); err != nil {
			if !errors.Is(err, errMaintenanceWriterPending) {
				return inventory, err
			}
			select {
			case <-ctx.Done():
				return inventory, fmt.Errorf("writer repair prerequisite: %w", err)
			case <-ticker.C:
				continue
			}
		}
		break
	}
	for _, repair := range r.deferredWriters {
		if err := r.applyWriterRepair(ctx, repair); err != nil {
			return inventory, err
		}
	}
	r.deferredWriters = nil
	refreshed, err := r.observe(ctx)
	if err != nil {
		return inventory, err
	}
	refreshed.Restart = inventory.Restart
	for _, c := range refreshed.Consumers {
		var issuer environmentCreateRegisteredIssuer
		for _, item := range refreshed.Issuers {
			if item.Issuer.ID == c.IssuerID {
				issuer = item.Issuer
			}
		}
		if err := r.install(ctx, c, issuer, records[c.IssuerID]); err != nil {
			return inventory, err
		}
	}
	if len(r.deferredWriters) > 0 {
		return inventory, errors.New("original CRL writer remains nonwritable after group correction")
	}
	return refreshed, nil
}
