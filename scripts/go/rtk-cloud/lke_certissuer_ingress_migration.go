package main

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// This migration owns only the named, stack-labelled CertIssuer routes. It is
// deliberately separate from generic apply: renaming an Ingress does not remove
// its old hostname owner, and a broad prune could remove unrelated routes.
type certIssuerIngressObject map[string]any

type certIssuerIngressIO struct {
	list    func() ([]certIssuerIngressObject, error)
	read    func(string, string) (certIssuerIngressObject, error)
	write   func(string, certIssuerIngressObject) (certIssuerIngressObject, error)
	remove  func(certIssuerIngressObject) error
	serving func(lkeCertIssuerTLSConfig, bool, bool) error
}

type certIssuerIngressChange struct {
	Before certIssuerIngressObject `json:"before,omitempty"`
	Wanted certIssuerIngressObject `json:"wanted,omitempty"`
	After  certIssuerIngressObject `json:"after,omitempty"`
	Done   bool                    `json:"done"`
}

type lkeCertIssuerIngressMigration struct {
	config      lkeCertIssuerTLSConfig
	env         map[string]string
	paths       provisionPaths
	io          certIssuerIngressIO
	Changes     []certIssuerIngressChange `json:"changes"`
	ID          string                    `json:"id"`
	Status      string                    `json:"status"`
	RestorePath string                    `json:"-"`
}

func lkePlanCertIssuerIngressMigration(paths provisionPaths, env map[string]string) (*lkeCertIssuerIngressMigration, error) {
	return lkePlanCertIssuerIngressMigrationWithContext(context.Background(), paths, env)
}

func lkePlanCertIssuerIngressMigrationWithContext(ctx context.Context, paths provisionPaths, env map[string]string) (*lkeCertIssuerIngressMigration, error) {
	return lkePlanCertIssuerIngressMigrationWithIO(paths, env, newCertIssuerIngressIO(ctx))
}

func lkePlanCertIssuerIngressMigrationWithIO(paths provisionPaths, env map[string]string, access certIssuerIngressIO) (*lkeCertIssuerIngressMigration, error) {
	return lkePlanCertIssuerIngressMigrationIgnoringRecord(paths, env, access, "")
}

func lkePlanCertIssuerIngressMigrationIgnoringRecord(paths provisionPaths, env map[string]string, access certIssuerIngressIO, ignoreID string) (*lkeCertIssuerIngressMigration, error) {
	p := &lkeCertIssuerIngressMigration{config: lkeResolveCertIssuerTLSConfig(env), env: env, paths: paths, io: access, Status: "planned"}
	if err := p.config.Validate(); err != nil {
		return nil, err
	}
	if p.config.PublicHost == "" {
		return p, nil
	}
	if err := certIssuerCheckPendingIngressMigration(paths, ignoreID); err != nil {
		return nil, err
	}
	ingresses, err := access.list()
	if err != nil {
		return nil, fmt.Errorf("inspect CertIssuer ingress inventory: %w", err)
	}
	sort.Slice(ingresses, func(i, j int) bool {
		a, b := certIssuerObjectMap(ingresses[i]["metadata"]), certIssuerObjectMap(ingresses[j]["metadata"])
		return fmt.Sprint(a["namespace"], "/", a["name"]) < fmt.Sprint(b["namespace"], "/", b["name"])
	})
	canonical := certIssuerCanonicalIngress(env, p.config)
	var current certIssuerIngressObject
	for _, obj := range ingresses {
		meta := certIssuerObjectMap(obj["metadata"])
		name, namespace := certIssuerObjectString(meta["name"]), certIssuerObjectString(meta["namespace"])
		if namespace == p.config.Namespace && name == "certissuer-public-mtls" {
			if err := p.validateOwner(obj); err != nil {
				return nil, err
			}
			if err := p.validateCanonicalIdentity(obj); err != nil {
				return nil, err
			}
			current = obj
			continue
		}
		modified, relevant, err := p.removeLegacyRoute(obj)
		if err != nil {
			return nil, err
		}
		if relevant {
			p.Changes = append(p.Changes, certIssuerIngressChange{Before: certIssuerSanitizeIngress(obj), Wanted: modified})
		}
	}
	if current == nil || !certIssuerCanonicalIngressMatches(current, canonical) {
		wanted := canonical
		if current != nil {
			wanted = certIssuerSanitizeIngress(current)
			wanted["spec"] = canonical["spec"]
			annotations := certIssuerObjectMap(certIssuerObjectMap(wanted["metadata"])["annotations"])
			if annotations == nil {
				annotations = map[string]any{}
			}
			for key, value := range certIssuerObjectMap(certIssuerObjectMap(canonical["metadata"])["annotations"]) {
				annotations[key] = value
			}
			certIssuerObjectMap(wanted["metadata"])["annotations"] = annotations
		}
		p.Changes = append(p.Changes, certIssuerIngressChange{Before: certIssuerSanitizeIngress(current), Wanted: wanted})
	}
	// An entirely absent environment can be planned before workloads are created.
	// Every actual routing mutation still requires installed serving material.
	if err := access.serving(p.config, current == nil && len(p.Changes) == 1 && p.Changes[0].Before == nil, false); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *lkeCertIssuerIngressMigration) validateOwner(obj certIssuerIngressObject) error {
	meta := certIssuerObjectMap(obj["metadata"])
	labels := certIssuerObjectMap(meta["labels"])
	if certIssuerObjectString(labels["rtk.realtek.com/stack"]) != p.env["CLOUD_STACK_NAME"] || certIssuerObjectString(labels["rtk.realtek.com/provider"]) != "lke" || certIssuerObjectString(labels["app.kubernetes.io/part-of"]) != "rtk-cloud" || certIssuerObjectString(meta["uid"]) == "" || certIssuerObjectString(meta["resourceVersion"]) == "" {
		return fmt.Errorf("CertIssuer ingress %s/%s lacks exact stack ownership or API preconditions; refusing migration", meta["namespace"], meta["name"])
	}
	if certIssuerObjectString(certIssuerObjectMap(obj["spec"])["ingressClassName"]) != "nginx" {
		return fmt.Errorf("CertIssuer ingress %s/%s is not owned by the selected nginx class", meta["namespace"], meta["name"])
	}
	if meta["deletionTimestamp"] != nil || len(certIssuerObjectList(meta["finalizers"])) != 0 {
		return errors.New("CertIssuer ingress is deleting or has finalizers; review its owner before migration")
	}
	return nil
}

func (p *lkeCertIssuerIngressMigration) validateCanonicalIdentity(obj certIssuerIngressObject) error {
	spec := certIssuerObjectMap(obj["spec"])
	if spec["defaultBackend"] != nil {
		return errors.New("canonical CertIssuer ingress has an unrelated default backend; refusing overwrite")
	}
	rules := certIssuerObjectList(spec["rules"])
	if len(rules) != 1 || certIssuerObjectString(certIssuerObjectMap(rules[0])["host"]) != p.config.PublicHost {
		return errors.New("canonical CertIssuer ingress has a different host or extra routes; refusing overwrite")
	}
	paths := certIssuerObjectList(certIssuerObjectMap(certIssuerObjectMap(rules[0])["http"])["paths"])
	if len(paths) != 1 || !certIssuerRouteMatches(certIssuerObjectMap(paths[0]), "certissuer", p.config.HTTPSPort) {
		return errors.New("canonical CertIssuer ingress has a different backend or path; refusing overwrite")
	}
	for _, item := range certIssuerObjectList(spec["tls"]) {
		for _, host := range certIssuerObjectList(certIssuerObjectMap(item)["hosts"]) {
			if certIssuerObjectString(host) != p.config.PublicHost {
				return errors.New("canonical CertIssuer ingress has an unrelated TLS host; refusing overwrite")
			}
		}
	}
	return nil
}

func (p *lkeCertIssuerIngressMigration) removeLegacyRoute(obj certIssuerIngressObject) (certIssuerIngressObject, bool, error) {
	meta := certIssuerObjectMap(obj["metadata"])
	name, namespace := certIssuerObjectString(meta["name"]), certIssuerObjectString(meta["namespace"])
	spec := certIssuerObjectMap(obj["spec"])
	relevant := false
	defaultBackend := certIssuerObjectString(certIssuerObjectMap(certIssuerObjectMap(spec["defaultBackend"])["service"])["name"])
	if strings.HasPrefix(namespace, p.env["CLOUD_STACK_NAME"]+"-") && (defaultBackend == "certissuer" || defaultBackend == "public-certissuer-video-cloud") {
		return nil, false, errors.New("CertIssuer is an ingress default backend; this unscoped route requires explicit owner review before migration")
	}
	for _, rule := range certIssuerObjectList(spec["rules"]) {
		r := certIssuerObjectMap(rule)
		if certIssuerObjectString(r["host"]) == p.config.PublicHost {
			relevant = true
			if len(certIssuerObjectList(certIssuerObjectMap(r["http"])["paths"])) == 0 {
				return nil, false, errors.New("public CertIssuer hostname owner has no valid HTTP path; refusing migration of malformed routing")
			}
		}
		for _, path := range certIssuerObjectList(certIssuerObjectMap(r["http"])["paths"]) {
			backend := certIssuerObjectString(certIssuerObjectMap(certIssuerObjectMap(certIssuerObjectMap(path)["backend"])["service"])["name"])
			if certIssuerObjectString(r["host"]) == p.config.PublicHost || (strings.HasPrefix(namespace, p.env["CLOUD_STACK_NAME"]+"-") && (backend == "certissuer" || backend == "public-certissuer-video-cloud")) {
				relevant = true
			}
		}
	}
	if !relevant {
		return nil, false, nil
	}
	if namespace != lkeIngressNamespace(p.env) || (name != "video-cloud-staging-certissuer" && name != "video-cloud-staging-https") {
		return nil, false, fmt.Errorf("public CertIssuer host/backend is owned by unrecognized ingress %s/%s; refusing migration", namespace, name)
	}
	if err := p.validateOwner(obj); err != nil {
		return nil, false, err
	}
	out := certIssuerSanitizeIngress(obj)
	keptRules := []any{}
	for _, rule := range certIssuerObjectList(certIssuerObjectMap(out["spec"])["rules"]) {
		r := certIssuerObjectMap(rule)
		if certIssuerObjectString(r["host"]) != p.config.PublicHost {
			for _, path := range certIssuerObjectList(certIssuerObjectMap(r["http"])["paths"]) {
				backend := certIssuerObjectString(certIssuerObjectMap(certIssuerObjectMap(certIssuerObjectMap(path)["backend"])["service"])["name"])
				if backend == "certissuer" || backend == "public-certissuer-video-cloud" {
					return nil, false, errors.New("legacy CertIssuer backend is bound to a different hostname; reconcile intent before migration")
				}
			}
			keptRules = append(keptRules, r)
			continue
		}
		for _, path := range certIssuerObjectList(certIssuerObjectMap(r["http"])["paths"]) {
			if !certIssuerRouteMatches(certIssuerObjectMap(path), "public-certissuer-video-cloud", p.config.HTTPSPort) {
				return nil, false, errors.New("legacy CertIssuer hostname has a different backend or additional path; refusing to consume unrelated traffic")
			}
		}
	}
	if len(keptRules) == 0 {
		return nil, true, nil
	}
	certIssuerObjectMap(out["spec"])["rules"] = keptRules
	keptTLS := []any{}
	for _, item := range certIssuerObjectList(certIssuerObjectMap(out["spec"])["tls"]) {
		tlsEntry := certIssuerObjectMap(item)
		hosts := []any{}
		for _, host := range certIssuerObjectList(tlsEntry["hosts"]) {
			if certIssuerObjectString(host) != p.config.PublicHost {
				hosts = append(hosts, host)
			}
		}
		if len(hosts) != 0 {
			tlsEntry["hosts"] = hosts
			keptTLS = append(keptTLS, tlsEntry)
		}
	}
	certIssuerObjectMap(out["spec"])["tls"] = keptTLS
	return out, true, nil
}

func (p *lkeCertIssuerIngressMigration) Apply() error {
	if p.config.PublicHost == "" {
		return nil
	}
	if err := p.io.serving(p.config, false, true); err != nil {
		return err
	}
	if len(p.Changes) == 0 {
		return p.Verify()
	}
	// Refresh the whole relevant inventory to detect a new foreign hostname owner.
	fresh, err := lkePlanCertIssuerIngressMigrationWithIO(p.paths, p.env, p.io)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(p.Changes, fresh.Changes) {
		return errors.New("CertIssuer ingress changed after planning; rerun the read-only deployment check")
	}
	p.ID = fmt.Sprintf("certissuer-%d", time.Now().UTC().UnixNano())
	p.Status = "applying"
	if err := p.save(); err != nil {
		return err
	}
	for i := range p.Changes {
		change := &p.Changes[i]
		var result certIssuerIngressObject
		if change.Wanted == nil {
			err = p.io.remove(change.Before)
		} else if change.Before != nil {
			result, err = p.io.write("replace", change.Wanted)
		} else {
			wanted := certIssuerSanitizeIngress(change.Wanted)
			certIssuerObjectMap(wanted["metadata"])["annotations"].(map[string]any)["rtk.realtek.com/certissuer-migration"] = p.ID
			result, err = p.io.write("create", wanted)
		}
		if err != nil {
			// A failed command can still have reached Kubernetes. Do not guess who
			// owns that outcome, or overwrite a resource that another actor changed.
			live, readErr := p.readChange(*change)
			if readErr != nil || !certIssuerObjectsSame(live, change.Before) {
				p.Status = "outcome-unknown"
				_ = p.save()
				return fmt.Errorf("CertIssuer ingress mutation outcome is uncertain; restore record %s: %w", p.RestorePath, err)
			}
			return p.failAndRollback(err)
		}
		change.After, change.Done = certIssuerSanitizeIngress(result), true
		if err := p.save(); err != nil {
			return p.failAndRollback(err)
		}
	}
	if err := p.Verify(); err != nil {
		return p.failAndRollback(err)
	}
	p.Status = "complete"
	return p.save()
}

func (p *lkeCertIssuerIngressMigration) Verify() error {
	if p.config.PublicHost == "" {
		return nil
	}
	fresh, err := lkePlanCertIssuerIngressMigrationIgnoringRecord(p.paths, p.env, p.io, p.ID)
	if err != nil {
		return err
	}
	if len(fresh.Changes) != 0 {
		return errors.New("public CertIssuer routing did not converge to the canonical passthrough ingress")
	}
	return p.io.serving(p.config, false, true)
}

func (p *lkeCertIssuerIngressMigration) readChange(change certIssuerIngressChange) (certIssuerIngressObject, error) {
	obj := change.Before
	if obj == nil {
		obj = change.Wanted
	}
	meta := certIssuerObjectMap(obj["metadata"])
	return p.io.read(certIssuerObjectString(meta["namespace"]), certIssuerObjectString(meta["name"]))
}

func (p *lkeCertIssuerIngressMigration) failAndRollback(cause error) error {
	p.Status = "rolling-back"
	_ = p.save()
	for i := len(p.Changes) - 1; i >= 0; i-- {
		change := &p.Changes[i]
		if !change.Done {
			continue
		}
		live, err := p.readChange(*change)
		if err != nil || !certIssuerObjectsSame(live, change.After) {
			p.Status = "rollback-blocked"
			_ = p.save()
			return fmt.Errorf("%w; rollback refused concurrent ingress drift; restore record %s", cause, p.RestorePath)
		}
		if change.Before == nil {
			err = p.io.remove(live)
		} else if live == nil {
			restore := certIssuerSanitizeIngress(change.Before)
			meta := certIssuerObjectMap(restore["metadata"])
			delete(meta, "uid")
			delete(meta, "resourceVersion")
			_, err = p.io.write("create", restore)
		} else {
			restore := certIssuerSanitizeIngress(change.Before)
			meta := certIssuerObjectMap(restore["metadata"])
			meta["uid"] = certIssuerObjectMap(live["metadata"])["uid"]
			meta["resourceVersion"] = certIssuerObjectMap(live["metadata"])["resourceVersion"]
			_, err = p.io.write("replace", restore)
		}
		if err != nil {
			p.Status = "rollback-blocked"
			_ = p.save()
			return fmt.Errorf("%w; ingress rollback failed: %v; restore record %s", cause, err, p.RestorePath)
		}
		change.Done = false
		if err := p.save(); err != nil {
			return fmt.Errorf("%w; cannot record ingress rollback: %v", cause, err)
		}
	}
	p.Status = "rolled-back"
	_ = p.save()
	return fmt.Errorf("%w; prior ingress routes restored; restore record %s", cause, p.RestorePath)
}

func (p *lkeCertIssuerIngressMigration) save() error {
	if p.RestorePath == "" {
		dir := certIssuerIngressJournalDir(p.paths)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		p.RestorePath = filepath.Join(dir, p.ID+".json")
	}
	raw, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p.RestorePath), ".ingress-restore-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(raw); err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err = errors.Join(err, closeErr); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p.RestorePath)
}

func certIssuerIngressJournalDir(paths provisionPaths) string {
	dir := paths.ArtifactsDir
	if dir == "" {
		dir = filepath.Join(paths.EnvRoot, "artifacts")
	}
	return filepath.Join(dir, "certissuer-ingress")
}

func certIssuerCheckPendingIngressMigration(paths provisionPaths, ignoreID string) error {
	dir := certIssuerIngressJournalDir(paths)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "certissuer-") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		raw, err := os.ReadFile(path)
		var record struct{ ID, Status string }
		if err != nil || json.Unmarshal(raw, &record) != nil {
			return fmt.Errorf("cannot inspect prior CertIssuer ingress restore record %s", path)
		}
		if record.ID == "" || entry.Name() != record.ID+".json" {
			return fmt.Errorf("invalid prior CertIssuer ingress restore record %s", path)
		}
		if record.ID != ignoreID && record.Status != "complete" && record.Status != "rolled-back" {
			return fmt.Errorf("unfinished CertIssuer ingress migration requires review before retrying: %s", path)
		}
	}
	return nil
}

func certIssuerCanonicalIngress(env map[string]string, cfg lkeCertIssuerTLSConfig) certIssuerIngressObject {
	return certIssuerIngressObject{"apiVersion": "networking.k8s.io/v1", "kind": "Ingress", "metadata": map[string]any{"name": "certissuer-public-mtls", "namespace": cfg.Namespace, "labels": map[string]any{"app.kubernetes.io/name": "certissuer-public-mtls", "app.kubernetes.io/part-of": "rtk-cloud", "rtk.realtek.com/provider": "lke", "rtk.realtek.com/stack": env["CLOUD_STACK_NAME"]}, "annotations": map[string]any{"nginx.ingress.kubernetes.io/ssl-passthrough": "true", "nginx.ingress.kubernetes.io/backend-protocol": "HTTPS"}}, "spec": map[string]any{"ingressClassName": "nginx", "tls": []any{map[string]any{"hosts": []any{cfg.PublicHost}}}, "rules": []any{map[string]any{"host": cfg.PublicHost, "http": map[string]any{"paths": []any{map[string]any{"path": "/", "pathType": "Prefix", "backend": map[string]any{"service": map[string]any{"name": "certissuer", "port": map[string]any{"number": cfg.HTTPSPort}}}}}}}}}}
}

func certIssuerCanonicalIngressMatches(actual, wanted certIssuerIngressObject) bool {
	annotations := certIssuerObjectMap(certIssuerObjectMap(actual["metadata"])["annotations"])
	return certIssuerObjectString(annotations["nginx.ingress.kubernetes.io/ssl-passthrough"]) == "true" && certIssuerObjectString(annotations["nginx.ingress.kubernetes.io/backend-protocol"]) == "HTTPS" && reflect.DeepEqual(certIssuerSanitizeIngress(actual)["spec"], certIssuerSanitizeIngress(wanted)["spec"])
}

func certIssuerRouteMatches(path map[string]any, backend string, port int) bool {
	service := certIssuerObjectMap(certIssuerObjectMap(path["backend"])["service"])
	return certIssuerObjectString(path["path"]) == "/" && certIssuerObjectString(path["pathType"]) == "Prefix" && certIssuerObjectString(service["name"]) == backend && fmt.Sprint(certIssuerObjectMap(service["port"])["number"]) == fmt.Sprint(port)
}

func certIssuerSanitizeIngress(obj certIssuerIngressObject) certIssuerIngressObject {
	if obj == nil {
		return nil
	}
	raw, _ := json.Marshal(obj)
	var copy certIssuerIngressObject
	_ = json.Unmarshal(raw, &copy)
	delete(copy, "status")
	meta := certIssuerObjectMap(copy["metadata"])
	for _, key := range []string{"managedFields", "creationTimestamp", "generation"} {
		delete(meta, key)
	}
	delete(certIssuerObjectMap(meta["annotations"]), "kubectl.kubernetes.io/last-applied-configuration")
	return copy
}

func certIssuerObjectsSame(a, b certIssuerIngressObject) bool {
	return reflect.DeepEqual(certIssuerSanitizeIngress(a), certIssuerSanitizeIngress(b))
}

func certIssuerObjectMap(value any) map[string]any {
	result, _ := value.(map[string]any)
	return result
}
func certIssuerObjectList(value any) []any    { result, _ := value.([]any); return result }
func certIssuerObjectString(value any) string { result, _ := value.(string); return result }

type certIssuerIngressCommand func(io.Reader, ...string) ([]byte, error)

func newCertIssuerIngressIO(ctx context.Context) certIssuerIngressIO {
	command := func(stdin io.Reader, args ...string) ([]byte, error) {
		if stdin == nil {
			// Bypass snapshot caching (postcheck needs fresh API state) and Secret
			// inventory substitution (static certificates require one public key).
			return newDeploymentCheckRuntime(ctx, "").run(false, lkeKubectlArgs(args...)...)
		}
		// Mutation commands are never retried: a lost response is an unknown
		// outcome handled by the durable restore journal and fresh API reads.
		callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(callCtx, lkeKubectl(), lkeKubectlArgs(args...)...)
		cmd.Stdin = stdin
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error {
			if cmd.Process == nil {
				return os.ErrProcessDone
			}
			err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			if errors.Is(err, syscall.ESRCH) {
				return os.ErrProcessDone
			}
			return err
		}
		cmd.WaitDelay = time.Second
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			return nil, classifyDeploymentRuntimeError(callCtx, stderr.Bytes(), err, false)
		}
		return stdout.Bytes(), nil
	}
	return newCertIssuerIngressIOWithCommand(command)
}

func newCertIssuerIngressIOWithCommand(command certIssuerIngressCommand) certIssuerIngressIO {
	access := certIssuerIngressIO{}
	access.list = func() ([]certIssuerIngressObject, error) {
		raw, err := command(nil, "get", "ingresses", "--all-namespaces", "-o", "json")
		var list struct {
			Items json.RawMessage `json:"items"`
		}
		if err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &list); err != nil {
			return nil, err
		}
		if len(bytes.TrimSpace(list.Items)) == 0 || bytes.TrimSpace(list.Items)[0] != '[' {
			return nil, errors.New("CertIssuer ingress inventory has no valid items array")
		}
		var objects []certIssuerIngressObject
		if err := json.Unmarshal(list.Items, &objects); err != nil {
			return nil, err
		}
		return objects, nil
	}
	access.read = func(namespace, name string) (certIssuerIngressObject, error) {
		return certIssuerReadPublicObject(command, "ingress", namespace, name)
	}
	access.write = func(verb string, obj certIssuerIngressObject) (certIssuerIngressObject, error) {
		raw, err := json.Marshal(obj)
		if err != nil {
			return nil, err
		}
		out, err := command(bytes.NewReader(raw), verb, "-f", "-", "-o", "json")
		if err != nil {
			return nil, err
		}
		var result certIssuerIngressObject
		if err = json.Unmarshal(out, &result); err != nil {
			return nil, err
		}
		meta, expected := certIssuerObjectMap(result["metadata"]), certIssuerObjectMap(obj["metadata"])
		if meta["name"] != expected["name"] || meta["namespace"] != expected["namespace"] || certIssuerObjectString(meta["uid"]) == "" || certIssuerObjectString(meta["resourceVersion"]) == "" {
			return nil, errors.New("CertIssuer ingress mutation returned invalid public metadata; outcome requires review")
		}
		return result, nil
	}
	access.remove = func(obj certIssuerIngressObject) error {
		meta := certIssuerObjectMap(obj["metadata"])
		options, _ := json.Marshal(map[string]any{"apiVersion": "v1", "kind": "DeleteOptions", "preconditions": map[string]any{"uid": meta["uid"], "resourceVersion": meta["resourceVersion"]}})
		uri := "/apis/networking.k8s.io/v1/namespaces/" + certIssuerObjectString(meta["namespace"]) + "/ingresses/" + certIssuerObjectString(meta["name"])
		if _, err := command(bytes.NewReader(options), "delete", "--raw="+uri, "-f", "-"); err != nil {
			return err
		}
		remaining, err := access.read(certIssuerObjectString(meta["namespace"]), certIssuerObjectString(meta["name"]))
		if err != nil {
			return err
		}
		if remaining != nil {
			return errors.New("conditionally deleted CertIssuer ingress remains; outcome requires review")
		}
		return nil
	}
	access.serving = func(cfg lkeCertIssuerTLSConfig, allowAbsent, requireAvailable bool) error {
		return certIssuerValidateInstalledServingPolicy(command, cfg, allowAbsent, requireAvailable)
	}
	return access
}

func certIssuerReadPublicObject(command certIssuerIngressCommand, kind, namespace, name string) (certIssuerIngressObject, error) {
	raw, err := command(nil, "-n", namespace, "get", kind, name, "--ignore-not-found=true", "-o", "json")
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	var obj certIssuerIngressObject
	if err = json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	return obj, nil
}

// This is an installed-policy/identity qualification, not proof that a public
// authenticated request succeeds. The strict post-deployment check retains that
// separate responsibility. Private identity material never leaves its owner.
func certIssuerValidateInstalledServingPolicy(command certIssuerIngressCommand, cfg lkeCertIssuerTLSConfig, allowAbsent, requireAvailable bool) error {
	service, err := certIssuerReadPublicObject(command, "service", cfg.Namespace, "certissuer")
	if err != nil {
		return err
	}
	deployment, err := certIssuerReadPublicObject(command, "deployment", cfg.Namespace, "certissuer")
	if err != nil {
		return err
	}
	if allowAbsent && service == nil && deployment == nil {
		return nil
	}
	if service == nil || deployment == nil {
		return errors.New("CertIssuer Service and Deployment must exist before public ingress migration")
	}
	spec := certIssuerObjectMap(service["spec"])
	if certIssuerObjectString(spec["type"]) == "ExternalName" || certIssuerObjectString(spec["clusterIP"]) == "" || certIssuerObjectString(spec["clusterIP"]) == "None" {
		return errors.New("CertIssuer passthrough requires its concrete ClusterIP Service")
	}
	validPort := false
	var targetPort any
	for _, port := range certIssuerObjectList(spec["ports"]) {
		entry := certIssuerObjectMap(port)
		if fmt.Sprint(entry["port"]) == fmt.Sprint(cfg.HTTPSPort) {
			if protocol := certIssuerObjectString(entry["protocol"]); protocol != "" && protocol != "TCP" {
				return errors.New("CertIssuer HTTPS Service port is not TCP")
			}
			validPort = true
			targetPort = entry["targetPort"]
		}
	}
	if !validPort {
		return errors.New("CertIssuer Service does not expose the resolved HTTPS port")
	}
	status := certIssuerObjectMap(deployment["status"])
	available := status["availableReplicas"] != nil && fmt.Sprint(status["availableReplicas"]) != "0"
	if requireAvailable && !available {
		return errors.New("CertIssuer Deployment has no available replica; qualify serving identity before routing")
	}
	if requireAvailable {
		replicas := certIssuerObjectMap(deployment["spec"])["replicas"]
		if replicas == nil {
			replicas = float64(1)
		}
		if certIssuerObjectNumber(status["observedGeneration"]) < certIssuerObjectNumber(certIssuerObjectMap(deployment["metadata"])["generation"]) || certIssuerObjectNumber(status["updatedReplicas"]) != certIssuerObjectNumber(replicas) || certIssuerObjectNumber(status["availableReplicas"]) < certIssuerObjectNumber(replicas) || certIssuerObjectNumber(status["unavailableReplicas"]) > 0 {
			return errors.New("CertIssuer Deployment has not converged to its current serving generation")
		}
	}
	pod := certIssuerObjectMap(certIssuerObjectMap(certIssuerObjectMap(deployment["spec"])["template"])["spec"])
	podLabels := certIssuerObjectMap(certIssuerObjectMap(certIssuerObjectMap(certIssuerObjectMap(deployment["spec"])["template"])["metadata"])["labels"])
	selector := certIssuerObjectMap(spec["selector"])
	if certIssuerObjectString(selector["app.kubernetes.io/name"]) != "certissuer" {
		return errors.New("CertIssuer Service selector does not target its workload")
	}
	for key, value := range selector {
		if podLabels[key] != value {
			return errors.New("CertIssuer Service selector differs from its Deployment labels")
		}
	}
	var container map[string]any
	for _, item := range certIssuerObjectList(pod["containers"]) {
		if certIssuerObjectString(certIssuerObjectMap(item)["name"]) == "certissuer" {
			container = certIssuerObjectMap(item)
		}
	}
	if container == nil {
		return errors.New("CertIssuer Deployment has no identifiable TLS serving container")
	}
	if targetPort == nil {
		targetPort = cfg.HTTPSPort
	}
	if name, named := targetPort.(string); named {
		resolved := false
		for _, item := range certIssuerObjectList(container["ports"]) {
			entry := certIssuerObjectMap(item)
			if certIssuerObjectString(entry["name"]) == name && certIssuerObjectNumber(entry["containerPort"]) == float64(cfg.HTTPSPort) && (certIssuerObjectString(entry["protocol"]) == "" || certIssuerObjectString(entry["protocol"]) == "TCP") {
				resolved = true
			}
		}
		if !resolved {
			return errors.New("CertIssuer Service named targetPort does not resolve to its TLS container port")
		}
	} else if certIssuerObjectNumber(targetPort) != float64(cfg.HTTPSPort) {
		return errors.New("CertIssuer Service targetPort differs from the resolved TLS listener")
	}
	settings := map[string]string{}
	for _, item := range certIssuerObjectList(container["env"]) {
		entry := certIssuerObjectMap(item)
		settings[certIssuerObjectString(entry["name"])] = certIssuerObjectString(entry["value"])
	}
	if address := settings["CERT_ISSUER_LISTEN_ADDR"]; address != "" {
		_, port, err := net.SplitHostPort(address)
		if err != nil || port != strconv.Itoa(cfg.HTTPSPort) {
			return errors.New("CertIssuer configured TLS listener differs from the resolved Service port")
		}
	}
	if settings["CERT_ISSUER_TRUSTED_HEADER_ENABLED"] == "true" {
		return errors.New("CertIssuer public passthrough cannot adopt trusted-header identity mode")
	}
	if state := settings["CERT_ISSUER_HOST_IDENTITY_STATE"]; state != "" {
		if !slices.Contains(strings.Split(settings["CERT_ISSUER_HOST_DNS_NAMES"], ","), cfg.PublicHost) {
			return errors.New("managed CertIssuer serving DNS policy omits public host; approve and install its identity before migration")
		}
		if !filepath.IsAbs(state) || filepath.Clean(state) != state || settings["CERT_ISSUER_HOST_NAME"] == "" || settings["CERT_ISSUER_HOST_ROOT_SHA256"] == "" {
			return errors.New("managed CertIssuer serving identity policy is incomplete")
		}
		// A broken old workload is repairable by deployment. Its immutable DNS
		// policy is checked now; owner-state inspection is deferred until it runs.
		if !requireAvailable && !available {
			return nil
		}
		raw, err := command(nil, "-n", cfg.Namespace, "exec", "deployment/certissuer", "-c", "certissuer", "--", "/app/serviceidentity-bootstrap", "inspect-server", state, "service", settings["CERT_ISSUER_HOST_NAME"], settings["CERT_ISSUER_HOST_ROOT_SHA256"], settings["CERT_ISSUER_HOST_DNS_NAMES"])
		if err != nil {
			return fmt.Errorf("managed CertIssuer public serving identity is not installed: %w", err)
		}
		var report struct {
			Fingerprint string `json:"fingerprint"`
			Root        string `json:"root_sha256"`
		}
		if json.Unmarshal(raw, &report) != nil || len(report.Fingerprint) != 64 || report.Root != settings["CERT_ISSUER_HOST_ROOT_SHA256"] {
			return errors.New("managed CertIssuer public serving identity inspection is invalid")
		}
		return nil
	}
	chain, err := certIssuerReadMountedPublicChain(command, cfg, pod, container, settings["CERT_ISSUER_SERVER_CERT"])
	if err != nil {
		return err
	}
	leaf, now := chain[0], time.Now()
	if leaf.IsCA || leaf.KeyUsage&x509.KeyUsageDigitalSignature == 0 || !slices.Contains(leaf.ExtKeyUsage, x509.ExtKeyUsageServerAuth) || leaf.VerifyHostname(cfg.PublicHost) != nil || now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return errors.New("installed CertIssuer serving certificate lacks server purpose, public host coverage or validity; update it through the approved identity lifecycle before migration")
	}
	issuers := chain[1:]
	if settings["CERT_ISSUER_CLIENT_CA"] != "" {
		publicCA, err := certIssuerReadMountedPublicChain(command, cfg, pod, container, settings["CERT_ISSUER_CLIENT_CA"])
		if err != nil {
			return err
		}
		issuers = append(issuers, publicCA...)
	}
	roots, intermediates := x509.NewCertPool(), x509.NewCertPool()
	for _, issuer := range issuers {
		if !issuer.IsCA {
			continue
		}
		if issuer.CheckSignatureFrom(issuer) == nil {
			roots.AddCert(issuer)
		} else {
			intermediates.AddCert(issuer)
		}
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, DNSName: cfg.PublicHost, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		return errors.New("installed CertIssuer serving certificate does not verify against its mounted public issuer chain")
	}
	return nil
}

func certIssuerObjectNumber(value any) float64 {
	switch number := value.(type) {
	case float64:
		return number
	case int:
		return float64(number)
	case int32:
		return float64(number)
	case int64:
		return float64(number)
	}
	return 0
}

func certIssuerReadMountedPublicChain(command certIssuerIngressCommand, cfg lkeCertIssuerTLSConfig, pod, container map[string]any, certPath string) ([]*x509.Certificate, error) {
	if !filepath.IsAbs(certPath) || filepath.Clean(certPath) != certPath {
		return nil, errors.New("CertIssuer public certificate path is missing or invalid")
	}
	volumeName, relative, longest := "", "", 0
	for _, item := range certIssuerObjectList(container["volumeMounts"]) {
		mount := certIssuerObjectMap(item)
		prefix := certIssuerObjectString(mount["mountPath"])
		if prefix != "" && strings.HasPrefix(certPath, prefix+"/") && len(prefix) > longest {
			if certIssuerObjectString(mount["subPath"]) != "" || certIssuerObjectString(mount["subPathExpr"]) != "" {
				return nil, errors.New("CertIssuer public certificate uses a subPath mount; qualify its exact mapping before migration")
			}
			volumeName, relative, longest = certIssuerObjectString(mount["name"]), strings.TrimPrefix(certPath, prefix+"/"), len(prefix)
		}
	}
	secretName, dataKey := "", relative
	for _, item := range certIssuerObjectList(pod["volumes"]) {
		volume := certIssuerObjectMap(item)
		if certIssuerObjectString(volume["name"]) != volumeName {
			continue
		}
		secret := certIssuerObjectMap(volume["secret"])
		secretName = certIssuerObjectString(secret["secretName"])
		if len(certIssuerObjectList(secret["items"])) != 0 {
			dataKey = ""
		}
		for _, projection := range certIssuerObjectList(secret["items"]) {
			entry := certIssuerObjectMap(projection)
			if certIssuerObjectString(entry["path"]) == relative {
				dataKey = certIssuerObjectString(entry["key"])
			}
		}
	}
	if secretName == "" || dataKey == "" || strings.ContainsAny(dataKey, "/'{}[]\\") || !strings.HasSuffix(strings.ToLower(dataKey), ".crt") {
		return nil, errors.New("CertIssuer serving certificate must be a specific public .crt key in its mounted Secret")
	}
	raw, err := command(nil, "-n", cfg.Namespace, "get", "secret", secretName, "-o", "jsonpath={.data."+strings.ReplaceAll(dataKey, ".", "\\.")+"}")
	if err != nil {
		return nil, err
	}
	pem, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, errors.New("CertIssuer public serving certificate encoding is invalid")
	}
	chain, err := parsePEMCertificates(pem)
	if err != nil || len(chain) == 0 {
		return nil, errors.New("CertIssuer mounted public certificate is invalid")
	}
	return chain, nil
}
