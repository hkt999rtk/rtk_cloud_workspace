package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"time"
)

type maintenanceRuntime struct {
	cfg             deploymentConfig
	store           secretStore
	backend         environmentKubernetesBackend
	helper          *crlMaintenanceHelper
	image           string
	kubeconfig      string
	lease           deploymentplan.ExecutionLease
	commandOverride func(context.Context, []byte, ...string) ([]byte, error)
	readOverride    func(context.Context, string) (deploymentplan.Object, error)
	ensureImage     func(context.Context) (string, error)
	trustBinary     []byte
	deferredWriters map[string]maintenanceWriterRepair
}

func (r *maintenanceRuntime) query(ctx context.Context, query string) ([]byte, error) {
	if !strings.HasPrefix(query, "SELECT ") || strings.ContainsAny(query, ";\r\n") {
		return nil, errors.New("bounded read-only registry query required")
	}
	raw, err := r.command(ctx, nil, "--namespace", r.cfg.Values["CLOUD_STACK_NAME"]+"-platform", "exec", "postgresql-0", "--container", "postgres", "--", "env", "PGOPTIONS=-c default_transaction_read_only=on", "/bin/sh", "-ec", `PGPASSWORD="$POSTGRES_PASSWORD" psql -U postgres -d video_cloud -v ON_ERROR_STOP=1 -At -c "$1"`, "crl-maintenance-read", query)
	if err != nil || len(raw) > 8<<20 {
		return nil, errors.New("selected native registry read failed")
	}
	return bytes.TrimSpace(raw), nil
}

func (r *maintenanceRuntime) observe(ctx context.Context) (crlMaintenanceInventory, error) {
	var out crlMaintenanceInventory
	raw, err := r.query(ctx, "SELECT COALESCE(json_agg(document ORDER BY id),'[]'::json) FROM pki_issuers WHERE environment='"+r.cfg.Environment+"' AND status IN ('active','retiring')")
	if err != nil {
		return out, err
	}
	var docs []map[string]any
	if json.Unmarshal(raw, &docs) != nil {
		return out, errors.New("native issuer inventory is invalid")
	}
	known := map[string]environmentCreateRegisteredIssuer{}
	for _, doc := range docs {
		issuer, err := environmentCreateRegisteredIssuerFromMap(doc, deploymentplan.String(doc["certificate_fingerprint_sha256"]))
		if err != nil {
			return out, err
		}
		if issuer.Environment != r.cfg.Environment {
			return out, errors.New("foreign issuer returned by selected registry")
		}
		snapshot, err := r.snapshot(ctx, issuer)
		if err != nil {
			return out, err
		}
		known[issuer.ID] = issuer
		out.Issuers = append(out.Issuers, crlMaintenanceIssuer{Issuer: issuer, Snapshot: snapshot})
	}
	raw, err = r.command(ctx, nil, "get", "pods", "--all-namespaces", "-o", "json")
	if err != nil {
		return out, err
	}
	var pods struct {
		Items []deploymentplan.Object `json:"items"`
	}
	if json.Unmarshal(raw, &pods) != nil {
		return out, errors.New("native Pod inventory invalid")
	}
	seen := map[string]bool{}
	identityCache := map[string]string{}
	for _, pod := range pods.Items {
		meta := deploymentplan.Map(pod["metadata"])
		ns := deploymentplan.String(meta["namespace"])
		if !strings.HasPrefix(ns, r.cfg.Values["CLOUD_STACK_NAME"]+"-") || meta["deletionTimestamp"] != nil {
			continue
		}
		phase := deploymentplan.String(deploymentplan.Map(pod["status"])["phase"])
		if phase == "Succeeded" || phase == "Failed" {
			continue
		}
		name, uid := deploymentplan.String(meta["name"]), deploymentplan.String(meta["uid"])
		// Adapt the actual Pod spec to the shared mount resolver; no historical
		// create checkpoint or rendered Deployment serves as consumer inventory.
		adapted := deploymentplan.Object{"metadata": meta, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": pod["spec"]}}}
		encoded, _ := json.Marshal(adapted)
		var typed liveDeployment
		if json.Unmarshal(encoded, &typed) != nil {
			return out, errors.New("Pod mount inventory invalid")
		}
		for _, cv := range certIssuerObjectList(deploymentplan.Map(pod["spec"])["containers"]) {
			c := deploymentplan.Map(cv)
			container := deploymentplan.String(c["name"])
			for _, ev := range certIssuerObjectList(c["env"]) {
				e := deploymentplan.Map(ev)
				setting := deploymentplan.String(e["name"])
				if !strings.HasSuffix(setting, "_CRL_MANIFEST") {
					if value := deploymentplan.String(e["value"]); strings.HasPrefix(value, "/") && (strings.HasSuffix(setting, "_CRL") || strings.HasSuffix(setting, "_CRL_BUNDLE")) {
						projection, err := r.publicProjection(ctx, pod, c, value, known)
						if err != nil {
							return out, fmt.Errorf("public CRL %s/%s/%s %s: %w", ns, name, container, value, err)
						}
						out.Public = append(out.Public, projection)
					}
					continue
				}
				path := deploymentplan.String(e["value"])
				if path == "" || e["valueFrom"] != nil {
					return out, fmt.Errorf("%s/%s has indirect CRL manifest", ns, name)
				}
				cm, key, err := mountedPKICRLConfigMap(typed, container, path)
				if err != nil {
					return out, err
				}
				object, err := r.read(ctx, "ConfigMap/"+ns+"/"+cm)
				if err != nil || object == nil {
					return out, errors.New("actual CRL manifest ConfigMap unavailable")
				}
				manifest := deploymentplan.String(deploymentplan.Map(object["data"])[key])
				var entries []livePKICRLManifestEntry
				if json.Unmarshal([]byte(manifest), &entries) != nil || len(entries) == 0 {
					return out, errors.New("mounted CRL manifest is empty or malformed")
				}
				for _, entry := range entries {
					issuer, ok := known[entry.Issuer.ID]
					if !ok || entry.Issuer.Environment != r.cfg.Environment || entry.Issuer.Certificate != issuer.Certificate || entry.Issuer.Fingerprint != deploymentplan.String(issuer.Document["certificate_fingerprint_sha256"]) || !livePKICRLStateOnPVC(typed, container, entry.StatePath) {
						return out, errors.New("mounted CRL authority or durable state differs from selected registry")
					}
					id := ns + "/" + uid + "/" + container + "/" + entry.StatePath
					if seen[id] {
						continue
					}
					seen[id] = true
					identityKey := ns + "/" + uid + "/" + container
					consumer := identityCache[identityKey]
					if consumer == "" {
						consumer, err = r.consumerID(ctx, ns, name, container, c)
						if err != nil {
							return out, err
						}
						identityCache[identityKey] = consumer
					}
					if !environmentCreateConsumerID.MatchString(consumer) {
						return out, errors.New("CRL consumer identity cannot be resolved")
					}
					out.Consumers = append(out.Consumers, crlMaintenanceConsumer{Namespace: ns, Pod: name, PodUID: uid, Container: container, StatePath: entry.StatePath, IssuerID: issuer.ID, ConsumerID: consumer})
				}
			}
		}
	}
	controller, err := r.read(ctx, "Deployment/"+r.cfg.Values["CLOUD_STACK_NAME"]+"-video-cloud/pki-controller")
	if err != nil || controller == nil {
		return out, errors.New("native controller consumer policy unavailable")
	}

	out.Required, err = maintenanceRequiredConsumers(controller, out.Issuers)
	if err != nil {
		return out, err
	}

	return out, nil
}

func (r *maintenanceRuntime) snapshot(ctx context.Context, issuer environmentCreateRegisteredIssuer) (createRootCRLNativeSnapshot, error) {
	var snapshot createRootCRLNativeSnapshot
	id := issuer.ID
	if !environmentBootstrapOwnerUUID.MatchString(id) {
		return snapshot, errors.New("invalid registry issuer")
	}
	query := "SELECT json_build_object('prior',(SELECT json_build_object('issuer_id',issuer_id,'crl_sha256',digest,'crl_number',number::text,'crl_pem',pem,'this_update',this_update,'next_update',next_update) FROM pki_crls WHERE issuer_id='" + id + "' ORDER BY number DESC LIMIT 1),'bindings',COALESCE((SELECT json_agg(json_build_object('serial_hex',serial,'revoked_at',revoked_at,'reason_code',0) ORDER BY serial) FROM pki_certificate_bindings WHERE issuer_id='" + id + "' AND revoked_at IS NOT NULL),'[]'::json),'certificates',COALESCE((SELECT json_agg(json_build_object('certificate_pem',certificate_pem,'revoked_at',revoked_at) ORDER BY certificate_pem) FROM (SELECT certificate_pem,revoked_at FROM pki_service_client_issuances WHERE issuer_id='" + id + "' AND revoked_at IS NOT NULL UNION ALL SELECT certificate_pem,revoked_at FROM pki_server_issuances WHERE issuer_id='" + id + "' AND revoked_at IS NOT NULL UNION ALL SELECT certificate_pem,revoked_at FROM pki_app_issuances WHERE issuer_id='" + id + "' AND revoked_at IS NOT NULL UNION ALL SELECT i.document->>'certificate_pem',(SELECT min(a.created_at) FROM pki_audit a WHERE a.issuer_id=i.id AND a.event IN ('issuer_revoked','issuer_compromised')) FROM pki_issuers i WHERE i.parent_id='" + id + "' AND i.status IN ('revoked','compromised')) revoked),'[]'::json))"
	raw, err := r.query(ctx, query)
	if err != nil {
		return snapshot, err
	}
	if json.Unmarshal(raw, &snapshot) != nil {
		return snapshot, errors.New("native revocation snapshot invalid")
	}
	return snapshot, nil
}

func (r *maintenanceRuntime) publish(ctx context.Context, item crlMaintenanceIssuer, request createRootCRLRequest, signed createRootCRLRecord) error {
	if _, err := r.nativeTrustBinary(ctx); err != nil {
		return err
	}
	if r.lease == nil {
		return errors.New("CRL mutation requires environment execution lease")
	}
	if err := r.lease.Check(ctx); err != nil {
		return err
	}
	if r.helper == nil {
		if r.image == "" {
			image, err := r.ensureImage(ctx)
			if err != nil {
				return err
			}
			r.image = image
		}
		helper, err := newCRLMaintenanceHelper(ctx, r.cfg, r.kubeconfig, r.image)
		if err != nil {
			return err
		}
		r.helper = helper
	}
	body, _ := json.Marshal(request)
	args := []string{"recovery-import-root-crl", r.cfg.Environment, item.Issuer.ID, item.Issuer.RootFingerprint, item.Snapshot.Prior.Digest, "/work/request.json", deploymentplan.Digest(request), "/work/crl.pem"}
	files := map[string][]byte{"request.json": body, "crl.pem": []byte(signed.PEM)}
	if _, err := r.helper.Run(ctx, args, files); err != nil {
		return err
	}
	if err := r.lease.Check(ctx); err != nil {
		return err
	}
	raw, err := r.helper.Run(ctx, append(args, "--confirm", r.cfg.Environment), files)
	if err != nil {
		return err
	}
	var result struct {
		Environment string              `json:"environment"`
		CRL         createRootCRLRecord `json:"crl"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Environment != r.cfg.Environment || result.CRL.Digest != signed.Digest {
		return errors.New("native imported CRL readback differs")
	}
	return nil
}

func (r *maintenanceRuntime) install(ctx context.Context, c crlMaintenanceConsumer, issuer environmentCreateRegisteredIssuer, record createRootCRLRecord) error {
	if err := r.lease.Check(ctx); err != nil {
		return err
	}
	pod, err := r.read(ctx, "Pod/"+c.Namespace+"/"+c.Pod)
	if err != nil || deploymentplan.String(deploymentplan.Map(pod["metadata"])["uid"]) != c.PodUID {
		return errors.New("original CRL consumer Pod changed; rediscover before retry")
	}
	// Identical valid native state needs no binary transfer or image lookup.
	state, readErr := r.command(ctx, nil, "--namespace", c.Namespace, "exec", "pod/"+c.Pod, "--container", c.Container, "--", "cat", c.StatePath)
	if readErr == nil && maintenanceInstalledMatches(r.cfg.Environment, state, issuer, record) {
		return nil
	}
	access, err := r.writerAccess(ctx, c)
	if err != nil {
		return err
	}
	if !access.Writable {
		repair, err := r.qualifyWriterRepair(ctx, c, access)
		if err != nil {
			return err
		}
		if r.deferredWriters == nil {
			r.deferredWriters = map[string]maintenanceWriterRepair{}
		}
		r.deferredWriters[c.Namespace+"/"+c.PodUID+"/"+c.Container] = repair
		return nil // completion still requires installation and real convergence below
	}
	tool, err := r.nativeTrustBinary(ctx)
	if err != nil {
		return err
	}
	// Image qualification may take time; recheck the original live owner and lease.
	if err := r.lease.Check(ctx); err != nil {
		return err
	}
	pod, err = r.read(ctx, "Pod/"+c.Namespace+"/"+c.Pod)
	if err != nil || deploymentplan.String(deploymentplan.Map(pod["metadata"])["uid"]) != c.PodUID {
		return errors.New("original CRL consumer changed during installer qualification")
	}
	issuerRaw, _ := json.Marshal(issuer.Document)
	recordRaw, _ := json.Marshal(record)
	input := []byte(base64.StdEncoding.EncodeToString(tool) + "\n" + base64.StdEncoding.EncodeToString(issuerRaw) + "\n" + base64.StdEncoding.EncodeToString(recordRaw) + "\n")
	_, err = r.command(ctx, input, "--namespace", c.Namespace, "exec", "-i", "pod/"+c.Pod, "--container", c.Container, "--", "/bin/sh", "-ec", maintenanceInstallScript, "crl-maintenance", c.StatePath, environmentPreflightBytesSHA256(tool))
	if err != nil {
		return fmt.Errorf("native CRL installation failed for %s/%s/%s", c.Namespace, c.Pod, c.Container)
	}
	return nil
}

func (r *maintenanceRuntime) converge(ctx context.Context, inventory crlMaintenanceInventory, records map[string]createRootCRLRecord) error {
	issuers := map[string]environmentCreateRegisteredIssuer{}
	for _, i := range inventory.Issuers {
		issuers[i.Issuer.ID] = i.Issuer
	}
	// Offline roots first break transport-expiry cycles. Online issuers remain
	// under their original controller/provider; never mint their keys locally.
	for _, c := range inventory.Consumers {
		issuer := issuers[c.IssuerID]
		if issuer.Kind == "root" && issuer.Document["signer_provider"] == "offline" {
			if err := r.install(ctx, c, issuer, records[c.IssuerID]); err != nil {
				return err
			}
		}
	}
	if len(inventory.Consumers) == 0 {
		return errors.New("no actual native CRL consumers discovered")
	}
	if err := r.recoverOnline(ctx, inventory, records); err != nil {
		return err
	}
	if err := r.publishPublicCRLs(ctx, inventory.Public, records); err != nil {
		return err
	}
	var err error
	inventory, err = r.repairDeferredWriters(ctx, inventory, records)
	if err != nil {
		return err
	}
	reloadStarted := time.Now()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		if err := r.lease.Check(ctx); err != nil {
			return err
		}
		complete := true
		var pending []string
		var pendingConsumers []crlMaintenanceConsumer
		for id, issuer := range issuers {
			current, err := r.snapshot(ctx, issuer)
			if err != nil {
				return err
			}
			if _, err := validateMaintenanceCRL(r.cfg.Environment, issuer, current.Prior, true, time.Now()); err != nil {
				complete = false
				pending = append(pending, "issuer "+id+" native renewal")
				continue
			}
			if issuer.Kind == "root" && issuer.Document["signer_provider"] == "offline" && current.Prior.Digest != records[id].Digest {
				return errors.New("offline registry CRL changed during consumer convergence")
			}
			records[id] = current.Prior
		}
		for _, c := range inventory.Consumers {
			record := records[c.IssuerID]
			state, err := r.command(ctx, nil, "--namespace", c.Namespace, "exec", "pod/"+c.Pod, "--container", c.Container, "--", "cat", c.StatePath)
			var installed struct {
				Fingerprint string              `json:"issuer_fingerprint"`
				Terminal    string              `json:"terminal_issuer_id"`
				CRL         createRootCRLRecord `json:"crl"`
			}
			if err != nil || json.Unmarshal(state, &installed) != nil || installed.Terminal != "" || installed.CRL.Digest != record.Digest {
				complete = false
				pendingConsumers = append(pendingConsumers, c)
				pending = append(pending, c.Namespace+"/"+c.Pod+"/"+c.Container+" "+c.IssuerID+" state")
				continue
			}
			if installed.Fingerprint != deploymentplan.String(issuers[c.IssuerID].Document["certificate_fingerprint_sha256"]) {
				return errors.New("consumer CRL state authority changed")
			}
			if _, err := validateMaintenanceCRL(r.cfg.Environment, issuers[c.IssuerID], installed.CRL, true, time.Now()); err != nil {
				complete = false
				pendingConsumers = append(pendingConsumers, c)
				pending = append(pending, c.Namespace+"/"+c.Pod+"/"+c.Container+" "+c.IssuerID+" state")
				continue
			}
			raw, err := r.query(ctx, "SELECT EXISTS(SELECT 1 FROM pki_crl_acknowledgments WHERE issuer_id='"+c.IssuerID+"' AND digest='"+record.Digest+"' AND consumer_id='"+c.ConsumerID+"')")
			if err != nil {
				return err
			}
			if string(raw) != "t" {
				complete = false
				pendingConsumers = append(pendingConsumers, c)
				pending = append(pending, c.ConsumerID+" "+c.IssuerID+" ACK")
			}
		}
		for id, issuer := range issuers {
			for _, consumer := range inventory.Required[issuer.Domain] {
				found := false
				for _, c := range inventory.Consumers {
					if c.IssuerID == id && c.ConsumerID == consumer {
						found = true
						break
					}
				}
				if !found {
					return fmt.Errorf("required %s consumer %s has no current mounted CRL state for issuer %s", issuer.Domain, consumer, id)
				}
			}
		}
		if complete {
			if err := r.verifyPublicCRLs(ctx, inventory.Public, records); err == nil {
				return nil
			} else {
				pending = append(pending, err.Error())
			}
		}

		if inventory.Restart && len(pendingConsumers) > 0 && time.Since(reloadStarted) >= 75*time.Second {
			pendingInventory := inventory
			pendingInventory.Consumers = pendingConsumers
			if err := r.restartExpiredConsumers(ctx, pendingInventory, records); err != nil {
				return err
			}
			refreshed, err := r.observe(ctx)
			if err != nil {
				return err
			}
			inventory.Consumers = refreshed.Consumers
			inventory.Public = refreshed.Public
			inventory.Required = refreshed.Required
			inventory.Restart = false
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("CRL convergence deadline: %s", strings.Join(pending, ", "))
		case <-ticker.C:
		}
	}
}

func (r *maintenanceRuntime) command(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	if r.commandOverride != nil {
		return r.commandOverride(ctx, input, args...)
	}
	return r.backend.command(ctx, input, args...)
}
func (r *maintenanceRuntime) read(ctx context.Context, id string) (deploymentplan.Object, error) {
	if r.readOverride != nil {
		return r.readOverride(ctx, id)
	}
	return r.backend.Read(ctx, id)
}
func (r *maintenanceRuntime) prepare(ctx context.Context, item crlMaintenanceIssuer) (crlMaintenanceIssuer, error) {
	if r.helper == nil {
		if r.image == "" {
			image, err := r.ensureImage(ctx)
			if err != nil {
				return item, err
			}
			r.image = image
		}
		helper, err := newCRLMaintenanceHelper(ctx, r.cfg, r.kubeconfig, r.image)
		if err != nil {
			return item, err
		}
		r.helper = helper
	}
	raw, err := r.helper.Run(ctx, []string{"recovery-export-root-crl", r.cfg.Environment, item.Issuer.ID, item.Issuer.RootFingerprint}, nil)
	if err != nil {
		return item, err
	}
	var exported struct {
		Environment string                    `json:"environment"`
		Issuer      map[string]any            `json:"issuer"`
		Prior       createRootCRLRecord       `json:"prior"`
		Revocations []createRootCRLRevocation `json:"revocations"`
	}
	if json.Unmarshal(raw, &exported) != nil || exported.Environment != r.cfg.Environment || deploymentplan.Digest(exported.Issuer) != deploymentplan.Digest(item.Issuer.Document) || exported.Prior.Digest != item.Snapshot.Prior.Digest {
		return item, errors.New("native Root export differs from selected registry snapshot")
	}
	item.Snapshot = createRootCRLNativeSnapshot{Prior: exported.Prior, Bindings: exported.Revocations}
	return item, nil
}

func maintenanceRequiredConsumers(controller deploymentplan.Object, issuers []crlMaintenanceIssuer) (map[string][]string, error) {
	values := map[string]string{}
	for _, cv := range certIssuerObjectList(environmentPKIPod(controller)["containers"]) {
		c := deploymentplan.Map(cv)
		if c["name"] != "pki-controller" {
			continue
		}
		for _, ev := range certIssuerObjectList(c["env"]) {
			e := deploymentplan.Map(ev)
			name := deploymentplan.String(e["name"])
			if name != "PKI_REQUIRED_CONSUMERS" && !strings.HasPrefix(name, "PKI_REQUIRED_CONSUMERS_") {
				continue
			}
			if e["valueFrom"] != nil {
				return nil, errors.New("native consumer policy must be explicit")
			}
			if _, ok := values[name]; ok {
				return nil, errors.New("duplicate native consumer policy")
			}
			values[name] = deploymentplan.String(e["value"])
		}
	}
	global := values["PKI_REQUIRED_CONSUMERS"]
	if global != "" && len(values) > 1 {
		return nil, errors.New("mixed global and domain consumer policy")
	}
	result := map[string][]string{}
	for _, item := range issuers {
		domain := item.Issuer.Domain
		raw := global
		if raw == "" {
			raw = values["PKI_REQUIRED_CONSUMERS_"+strings.ToUpper(domain)]
		}
		if raw == "" {
			return nil, fmt.Errorf("native required consumer policy missing for %s", domain)
		}
		if _, ok := result[domain]; ok {
			continue
		}
		seen := map[string]bool{}
		for _, value := range strings.Split(raw, ",") {
			value = strings.TrimSpace(value)
			if !environmentCreateConsumerID.MatchString(value) || seen[value] {
				return nil, errors.New("native required consumer identity invalid or duplicate")
			}
			seen[value] = true
			result[domain] = append(result[domain], value)
		}
	}
	return result, nil
}

func maintenanceInstalledMatches(environment string, raw []byte, issuer environmentCreateRegisteredIssuer, record createRootCRLRecord) bool {
	var installed struct {
		Fingerprint string              `json:"issuer_fingerprint"`
		Terminal    string              `json:"terminal_issuer_id"`
		CRL         createRootCRLRecord `json:"crl"`
	}
	if json.Unmarshal(raw, &installed) != nil || installed.Terminal != "" || installed.Fingerprint != deploymentplan.String(issuer.Document["certificate_fingerprint_sha256"]) || installed.CRL.Digest != record.Digest {
		return false
	}
	_, err := validateMaintenanceCRL(environment, issuer, installed.CRL, true, time.Now())
	return err == nil
}
