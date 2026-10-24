package main

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/acme"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const environmentPublicTLSSchema = "rtk-cloud.public-https/v1"

// This is a private, frozen producer intent. Existing material is an observed
// input, never a placeholder for an identity that has not been issued.
type environmentPublicTLSPlan struct {
	Schema              string                        `json:"schema"`
	Environment         string                        `json:"environment"`
	Stack               string                        `json:"stack"`
	ConfigurationSHA256 string                        `json:"configuration_sha256"`
	SecretID            string                        `json:"secret_id"`
	Hosts               []string                      `json:"hosts"`
	Consumers           map[string]string             `json:"consumers"`
	DNSAdapter          string                        `json:"dns_adapter"`
	RootDomain          string                        `json:"root_domain"`
	DirectoryURL        string                        `json:"directory_url"`
	Email               string                        `json:"email,omitempty"`
	MinValidDays        int                           `json:"min_valid_days"`
	OwnerUID            string                        `json:"owner_uid,omitempty"`
	Source              *environmentPublicTLSMaterial `json:"source,omitempty"`
}

type environmentPublicTLSMaterial struct {
	Certificate string `json:"certificate"`
	PrivateKey  string `json:"private_key"`
}

type environmentPublicTLSKeys struct {
	PlanSHA256 string `json:"plan_sha256"`
	AccountKey string `json:"account_key"`
	LeafKey    string `json:"leaf_key"`
	CSR        string `json:"csr"`
}

type environmentPublicTLSReceipt struct {
	PlanSHA256     string `json:"plan_sha256"`
	MaterialSHA256 string `json:"material_sha256"`
	SecretUID      string `json:"secret_uid"`
}

type environmentPublicTLSOps struct {
	HTTP         *http.Client
	DNS          func(deploymentConfig, map[string]string) (dnsAdapter, dnsAdapterContext, error)
	WaitTXT      func(context.Context, string, string, map[string]string) error
	WaitDNS01Set func(context.Context, string, map[string]string, map[string]string, *environmentPublicTLSDNSPropagation, func(environmentPublicTLSDNSPropagation) error) error
}

func defaultEnvironmentPublicTLSOps() environmentPublicTLSOps {
	return environmentPublicTLSOps{HTTP: http.DefaultClient, DNS: environmentCreateDNSAdapter, WaitTXT: waitDNSTXT, WaitDNS01Set: waitPublicTLSDNS01}
}

func publicTLSBlock(cause string) error {
	return environmentCreateBlocker("deployment.public-tls", cause)
}

// Only already reviewed HTTP TLS references can be projected to the retained
// original Secret name. This creates neither material nor a native owner.
func bindEnvironmentPublicTLSReferences(p *environmentPublicTLSPlan, defs []deploymentplan.Definition) ([]deploymentplan.Definition, error) {
	if p == nil {
		return defs, nil
	}
	if _, err := environmentPublicTLSOutputContract(p); err != nil {
		return nil, err
	}
	consumers, hosts, _, err := environmentPublicTLSConsumers(defs)
	if err != nil || deploymentplan.Digest(consumers) != deploymentplan.Digest(p.Consumers) || !slices.Equal(hosts, p.Hosts) {
		return nil, publicTLSBlock("public TLS references differ from their original complete consumer contract")
	}
	result := append([]deploymentplan.Definition(nil), defs...)
	name := strings.Split(p.SecretID, "/")[2]
	for i, d := range result {
		if p.Consumers[deploymentplan.ID(d.Object)] == "" {
			continue
		}
		result[i].Object = deploymentplan.Clone(d.Object)
		rows, _ := deploymentplan.Map(result[i].Object["spec"])["tls"].([]any)
		for _, row := range rows {
			deploymentplan.Map(row)["secretName"] = name
		}
	}
	return result, nil
}

// SSL passthrough endpoints keep the native server's certificate and client
// authentication. Only ingress-terminated HTTPS is a public ACME consumer.
func environmentPublicTLSConsumers(defs []deploymentplan.Definition) (map[string]string, []string, string, error) {
	consumers, hosts, secret := map[string]string{}, map[string]bool{}, ""
	for _, d := range defs {
		if deploymentplan.String(d.Object["kind"]) != "Ingress" {
			continue
		}
		meta := deploymentplan.Map(d.Object["metadata"])
		if deploymentplan.String(deploymentplan.Map(meta["annotations"])["nginx.ingress.kubernetes.io/ssl-passthrough"]) == "true" {
			continue
		}
		spec := deploymentplan.Map(d.Object["spec"])
		rows, _ := spec["tls"].([]any)
		if len(rows) == 0 {
			continue
		}
		var ownHosts []string
		for _, row := range rows {
			t := deploymentplan.Map(row)
			name := deploymentplan.String(t["secretName"])
			id := "Secret/" + deploymentplan.String(meta["namespace"]) + "/" + name
			if name == "" || secret != "" && id != secret {
				return nil, nil, "", publicTLSBlock("public HTTP routes must share one exact selected TLS Secret")
			}
			secret = id
			values, _ := t["hosts"].([]any)
			for _, value := range values {
				host := deploymentplan.String(value)
				if validateManagedDNSHostname(host) != nil {
					return nil, nil, "", publicTLSBlock("public TLS host is invalid")
				}
				hosts[host], ownHosts = true, append(ownHosts, host)
			}
		}
		if len(ownHosts) == 0 {
			return nil, nil, "", publicTLSBlock("public TLS route has no exact DNS names")
		}
		sort.Strings(ownHosts)
		consumers[deploymentplan.ID(d.Object)] = deploymentplan.Digest(ownHosts)
	}
	var names []string
	for host := range hosts {
		names = append(names, host)
	}
	sort.Strings(names)
	return consumers, names, secret, nil
}

func prepareEnvironmentPublicTLS(ctx context.Context, cfg deploymentConfig, store secretStore, defs []deploymentplan.Definition, live []deploymentplan.Object, bindings environmentIdentityBindings) (*environmentPublicTLSPlan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	consumers, hosts, id, err := environmentPublicTLSConsumers(defs)
	if err != nil || len(consumers) == 0 {
		return nil, err
	}
	if store.Environment != cfg.Environment || cfg.Values["CLOUD_STACK_NAME"] != "video-cloud-"+cfg.Environment || bindings.Environment != cfg.Environment || bindings.Stack != cfg.Values["CLOUD_STACK_NAME"] {
		return nil, publicTLSBlock("selected environment, stack and original ownership bindings are required")
	}
	operator, err := store.readOperator()
	if err != nil {
		return nil, err
	}
	values := appendMap(deploymentEndpointValues(cfg), operator)
	// An established, owned Ingress keeps its original TLS name. New catalogs use
	// the selected stack default; no ambient staging value selects another key.
	var existingID string
	for _, o := range live {
		if consumers[deploymentplan.ID(o)] == "" {
			continue
		}
		uid := deploymentplan.String(deploymentplan.Map(o["metadata"])["uid"])
		if bindings.Owners[deploymentplan.ID(o)] != uid || uid == "" {
			return nil, publicTLSBlock("an existing public Ingress has no exact original UID")
		}
		c, _, candidate, e := environmentPublicTLSConsumers([]deploymentplan.Definition{{Object: o}})
		if e != nil || len(c) != 1 || candidate == "" || existingID != "" && existingID != candidate {
			return nil, publicTLSBlock("existing public Ingress TLS ownership is inconsistent")
		}
		existingID = candidate
	}
	selectedID := "Secret/" + cfg.Values["CLOUD_STACK_NAME"] + "-ingress/" + lkePublicHTTPSTLSSecretName(values)
	if existingID != "" {
		selectedID = existingID
	}
	if selectedID != id {
		parts := strings.Split(selectedID, "/")
		if len(parts) != 3 || parts[1] != cfg.Values["CLOUD_STACK_NAME"]+"-ingress" {
			return nil, publicTLSBlock("existing public TLS Secret is outside the selected ingress namespace")
		}
		for i := range defs {
			if consumers[deploymentplan.ID(defs[i].Object)] != "" {
				defs[i].Object = deploymentplan.Clone(defs[i].Object)
				rows, _ := deploymentplan.Map(defs[i].Object["spec"])["tls"].([]any)
				for _, r := range rows {
					deploymentplan.Map(r)["secretName"] = parts[2]
				}
			}
		}
		id = selectedID
	}
	days := 30
	if raw := values["LKE_PUBLIC_HTTPS_MIN_VALID_DAYS"]; raw != "" {
		days, err = strconv.Atoi(raw)
		if err != nil {
			return nil, publicTLSBlock("public TLS minimum validity is invalid")
		}
	}
	p := &environmentPublicTLSPlan{Schema: environmentPublicTLSSchema, Environment: cfg.Environment, Stack: cfg.Values["CLOUD_STACK_NAME"], ConfigurationSHA256: deploymentplan.Digest(cfg), SecretID: id, Hosts: hosts, Consumers: consumers, DNSAdapter: cfg.DNSAdapter, RootDomain: strings.TrimSuffix(values["CLOUD_DNS_ROOT_DOMAIN"], "."), DirectoryURL: firstNonEmpty(values["LKE_PUBLIC_HTTPS_ACME_SERVER"], acme.LetsEncryptURL), Email: values["LKE_PUBLIC_HTTPS_ISSUE_EMAIL"], MinValidDays: days, OwnerUID: bindings.Owners[id]}
	if existingID != "" && p.OwnerUID == "" {
		return nil, publicTLSBlock("an existing Ingress's original TLS Secret has no recorded owner; do not replace its identity")
	}
	for _, o := range live {
		if deploymentplan.ID(o) != id {
			continue
		}
		uid := deploymentplan.String(deploymentplan.Map(o["metadata"])["uid"])
		if uid == "" || uid != p.OwnerUID {
			return nil, publicTLSBlock("existing public TLS Secret is unrecorded or its UID changed")
		}
		m, e := publicTLSSecretMaterial(o)
		if e != nil {
			return nil, e
		}
		p.Source = &m
		if !deploymentplan.Matches(publicTLSDefinition(p, m, uid).Object, o) {
			return nil, publicTLSBlock("existing public TLS Secret metadata differs from its maintained owned producer")
		}
	}
	if p.OwnerUID != "" && p.Source == nil {
		return nil, publicTLSBlock("recorded public TLS Secret is missing; do not regenerate its identity")
	}
	if p.Source == nil {
		m, ok, e := readEnvironmentPublicTLSLegacyCache(store, p)
		if e != nil {
			return nil, e
		}
		if ok {
			p.Source = &m
		}
	}
	if _, err = environmentPublicTLSOutputContract(p); err != nil {
		return nil, err
	}
	if p.Source != nil {
		if err = validateEnvironmentPublicTLSMaterial(p, *p.Source, time.Now()); err != nil {
			return nil, err
		}
	}
	return p, nil
}

func environmentPublicTLSOutputContract(p *environmentPublicTLSPlan) (map[string]string, error) {
	if p == nil {
		return map[string]string{}, nil
	}
	parts := strings.Split(p.SecretID, "/")
	u, err := url.Parse(p.DirectoryURL)
	if p.Schema != environmentPublicTLSSchema || p.Stack != "video-cloud-"+p.Environment || !secretEnvironmentPattern.MatchString(p.Environment) || !environmentPreflightDigest.MatchString(p.ConfigurationSHA256) || len(parts) != 3 || parts[0] != "Secret" || parts[1] != p.Stack+"-ingress" || !secretEnvironmentPattern.MatchString(parts[2]) || len(p.Hosts) == 0 || len(p.Hosts) > 100 || !slices.IsSorted(p.Hosts) || len(slices.Compact(slices.Clone(p.Hosts))) != len(p.Hosts) || len(p.Consumers) == 0 || p.MinValidDays < 1 || p.MinValidDays > 60 || (p.DNSAdapter != "godaddy" && p.DNSAdapter != "route53") || validateManagedDNSHostname(p.RootDomain) != nil || err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" || p.OwnerUID != "" && !environmentBootstrapOwnerUUID.MatchString(p.OwnerUID) || p.OwnerUID != "" && p.Source == nil {
		return nil, publicTLSBlock("public HTTPS producer intent is incomplete or outside its selected environment")
	}
	for _, h := range p.Hosts {
		if validateManagedDNSHostname(h) != nil || !strings.HasSuffix(h, "."+p.RootDomain) {
			return nil, publicTLSBlock("public TLS host is outside its selected authoritative zone")
		}
	}
	for id, sha := range p.Consumers {
		if !strings.HasPrefix(id, "Ingress/"+p.Stack+"-ingress/") || !environmentPreflightDigest.MatchString(sha) {
			return nil, publicTLSBlock("public TLS consumer contract is invalid")
		}
	}
	return map[string]string{p.SecretID: deploymentplan.Digest(p)}, nil
}

func publicTLSSecretMaterial(o deploymentplan.Object) (environmentPublicTLSMaterial, error) {
	data := deploymentplan.Map(o["data"])
	cert, e1 := base64.StdEncoding.DecodeString(deploymentplan.String(data["tls.crt"]))
	key, e2 := base64.StdEncoding.DecodeString(deploymentplan.String(data["tls.key"]))
	if deploymentplan.String(o["type"]) != "kubernetes.io/tls" || e1 != nil || e2 != nil || len(data) != 2 {
		return environmentPublicTLSMaterial{}, publicTLSBlock("existing public TLS Secret must be the exact certificate/key pair")
	}
	return environmentPublicTLSMaterial{string(cert), string(key)}, nil
}

func validateEnvironmentPublicTLSMaterial(p *environmentPublicTLSPlan, m environmentPublicTLSMaterial, now time.Time) error {
	pair, err := tls.X509KeyPair([]byte(m.Certificate), []byte(m.PrivateKey))
	if err != nil || len(pair.Certificate) == 0 {
		return publicTLSBlock("public TLS certificate and private key do not match")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || leaf.IsCA || now.Before(leaf.NotBefore) || !leaf.NotAfter.After(now.Add(time.Duration(p.MinValidDays)*24*time.Hour)) {
		return publicTLSBlock("public TLS leaf is invalid or within its selected renewal reserve")
	}
	if len(leaf.IPAddresses) != 0 || len(leaf.URIs) != 0 || len(leaf.EmailAddresses) != 0 {
		return publicTLSBlock("public TLS leaf has unrelated identities")
	}
	if !slices.Contains(leaf.ExtKeyUsage, x509.ExtKeyUsageServerAuth) {
		return publicTLSBlock("public TLS leaf is not a server authentication certificate")
	}
	for i := 1; i < len(pair.Certificate); i++ {
		issuer, err := x509.ParseCertificate(pair.Certificate[i])
		previous, parseErr := x509.ParseCertificate(pair.Certificate[i-1])
		if err != nil || parseErr != nil || previous.CheckSignatureFrom(issuer) != nil || now.Before(issuer.NotBefore) || !now.Before(issuer.NotAfter) {
			return publicTLSBlock("public TLS certificate chain is invalid")
		}
	}
	names := slices.Clone(leaf.DNSNames)
	sort.Strings(names)
	if !slices.Equal(names, p.Hosts) {
		return publicTLSBlock("public TLS leaf DNS names differ from its complete selected consumer set")
	}
	for _, h := range p.Hosts {
		if leaf.VerifyHostname(h) != nil {
			return publicTLSBlock("public TLS certificate does not cover every declared consumer")
		}
	}
	return nil
}

func checkEnvironmentPublicTLSPrerequisites(ctx context.Context, cfg deploymentConfig, store secretStore, p *environmentPublicTLSPlan) error {
	return checkEnvironmentPublicTLSPrerequisitesWithOps(ctx, cfg, store, p, defaultEnvironmentPublicTLSOps())
}

func checkEnvironmentPublicTLSPrerequisitesWithOps(ctx context.Context, cfg deploymentConfig, store secretStore, p *environmentPublicTLSPlan, ops environmentPublicTLSOps) error {
	if p == nil {
		return nil
	}
	if err := publicTLSPlanCurrent(cfg, store, p); err != nil {
		return err
	}
	if p.Source != nil {
		return validateEnvironmentPublicTLSMaterial(p, *p.Source, time.Now())
	}
	operator, err := store.readOperator()
	if err != nil {
		return err
	}
	adapter, local, err := ops.DNS(cfg, operator)
	if err != nil {
		return err
	}
	if local.RootDomain != p.RootDomain || adapter.Name() != p.DNSAdapter {
		return publicTLSBlock("DNS provider or selected authoritative zone changed")
	}
	if err = adapter.Validate(ctx, local); err != nil {
		return publicTLSBlock("DNS-01 credentials or permissions cannot be verified")
	}
	zone, err := adapter.DiscoverZone(ctx, local)
	if err != nil || zone.Name != p.RootDomain {
		return publicTLSBlock("DNS-01 authoritative zone cannot be verified")
	}
	client := &acme.Client{DirectoryURL: p.DirectoryURL, HTTPClient: ops.HTTP}
	if _, err = client.Discover(ctx); err != nil {
		return publicTLSBlock("the selected ACME directory is unavailable")
	}
	return ctx.Err()
}

func publicTLSPlanCurrent(cfg deploymentConfig, store secretStore, p *environmentPublicTLSPlan) error {
	if _, err := environmentPublicTLSOutputContract(p); err != nil {
		return err
	}
	if p.Environment != cfg.Environment || p.Stack != cfg.Values["CLOUD_STACK_NAME"] || p.Environment != store.Environment || p.ConfigurationSHA256 != deploymentplan.Digest(cfg) {
		return publicTLSBlock("public TLS producer no longer matches its frozen configuration")
	}
	return nil
}

func publicTLSBase(p *environmentPublicTLSPlan) string {
	return filepath.Join("pki", "public-https", strings.Split(p.SecretID, "/")[2], "deployment", deploymentplan.Digest(p))
}

func publicTLSRead(store secretStore, relative string) ([]byte, error) {
	path, err := store.safePath(relative)
	if err != nil {
		return nil, err
	}
	for d := filepath.Dir(path); ; d = filepath.Dir(d) {
		info, e := os.Lstat(d)
		if e != nil {
			return nil, e
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
			return nil, publicTLSBlock("public TLS state requires private real directories")
		}
		if d == store.ConfigRoot {
			break
		}
		if filepath.Dir(d) == d {
			return nil, publicTLSBlock("public TLS path escapes the selected canonical store")
		}
	}
	if _, err = os.Lstat(path); err != nil {
		return nil, err
	}
	return readBoundedEnvironmentFile(path, true)
}

func publicTLSWrite(store secretStore, relative string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	path, err := store.safePath(relative)
	if err != nil {
		return err
	}
	var dirs []string
	for d := filepath.Dir(path); ; d = filepath.Dir(d) {
		dirs = append(dirs, d)
		if d == store.ConfigRoot {
			break
		}
		if filepath.Dir(d) == d {
			return publicTLSBlock("public TLS path escapes the selected canonical store")
		}
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		info, e := os.Lstat(dirs[i])
		if errors.Is(e, os.ErrNotExist) {
			e = os.Mkdir(dirs[i], 0700)
		}
		if e != nil {
			return e
		}
		if info != nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700) {
			return publicTLSBlock("public TLS output directory is unsafe")
		}
	}
	if err = writeExclusivePrivateDeploymentJSON(path, raw); err != nil {
		return err
	}
	return syncEnvironmentPreflightDirectory(filepath.Dir(path))
}

func publicTLSReadJSON(store secretStore, path string, target any) error {
	raw, err := publicTLSRead(store, path)
	if err != nil {
		return err
	}
	return decodeEnvironmentJSON(raw, target)
}

func publicTLSWriteOwned(ctx context.Context, e *environmentCreateExecution, relative string, value any) error {
	if err := e.Check(ctx); err != nil {
		return err
	}
	invalidateManagedLateSourceReuse(e)
	return publicTLSWrite(e.Store, relative, value)
}

func readEnvironmentPublicTLSLegacyCache(store secretStore, p *environmentPublicTLSPlan) (environmentPublicTLSMaterial, bool, error) {
	base := filepath.Join("pki", "public-https", strings.Split(p.SecretID, "/")[2])
	cert, e1 := publicTLSRead(store, filepath.Join(base, "fullchain.pem"))
	key, e2 := publicTLSRead(store, filepath.Join(base, "privkey.pem"))
	if errors.Is(e1, os.ErrNotExist) && errors.Is(e2, os.ErrNotExist) {
		return environmentPublicTLSMaterial{}, false, nil
	}
	if e1 != nil || e2 != nil {
		return environmentPublicTLSMaterial{}, false, publicTLSBlock("original public TLS cache is partial or unsafe; preserve and reconcile it")
	}
	return environmentPublicTLSMaterial{string(cert), string(key)}, true, nil
}

func publicTLSDefinition(p *environmentPublicTLSPlan, m environmentPublicTLSMaterial, uid string) deploymentplan.Definition {
	parts := strings.Split(p.SecretID, "/")
	meta := deploymentplan.Object{"name": parts[2], "namespace": parts[1], "labels": deploymentplan.Object{"app.kubernetes.io/name": parts[2], "app.kubernetes.io/part-of": "rtk-cloud", "rtk.realtek.com/provider": "lke", "rtk.realtek.com/stack": p.Stack}}
	if uid != "" {
		meta["uid"] = uid
	}
	return deploymentplan.Definition{Phase: "configuration", Retain: uid != "", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "Secret", "metadata": meta, "type": "kubernetes.io/tls", "data": deploymentplan.Object{"tls.crt": base64.StdEncoding.EncodeToString([]byte(m.Certificate)), "tls.key": base64.StdEncoding.EncodeToString([]byte(m.PrivateKey))}}}
}

func executeEnvironmentPublicTLS(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, p *environmentPublicTLSPlan, apply func(context.Context, deploymentplan.Definition) (string, error)) error {
	return executeEnvironmentPublicTLSWithOps(ctx, cfg, store, e, p, apply, defaultEnvironmentPublicTLSOps())
}

func executeEnvironmentPublicTLSWithOps(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, p *environmentPublicTLSPlan, apply func(context.Context, deploymentplan.Definition) (string, error), ops environmentPublicTLSOps) error {
	if p == nil {
		return nil
	}
	if err := publicTLSPlanCurrent(cfg, store, p); err != nil {
		return err
	}
	if e == nil || e.Store != store || e.Journal.Environment != p.Environment || e.Journal.Stack != p.Stack || apply == nil {
		return publicTLSBlock("public TLS publication requires the held original execution and standard child publisher")
	}
	if err := e.Check(ctx); err != nil {
		return err
	}
	base := publicTLSBase(p)
	var material environmentPublicTLSMaterial
	err := publicTLSReadJSON(store, filepath.Join(base, "material.json"), &material)
	if errors.Is(err, os.ErrNotExist) {
		if p.Source != nil {
			material = *p.Source
		} else {
			material, err = issueEnvironmentPublicTLS(ctx, cfg, store, e, p, ops)
			if err != nil {
				return err
			}
		}
		if err = validateEnvironmentPublicTLSMaterial(p, material, time.Now()); err != nil {
			return err
		}
		if err = e.Check(ctx); err != nil {
			return err
		}
		if err = publicTLSWriteOwned(ctx, e, filepath.Join(base, "material.json"), material); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if err = validateEnvironmentPublicTLSMaterial(p, material, time.Now()); err != nil {
		return err
	}
	var completed environmentPublicTLSReceipt
	completedErr := publicTLSReadJSON(store, filepath.Join(base, "completed.json"), &completed)
	if completedErr != nil && !errors.Is(completedErr, os.ErrNotExist) {
		return completedErr
	}
	if completedErr == nil && (completed.PlanSHA256 != deploymentplan.Digest(p) || completed.MaterialSHA256 != deploymentplan.Digest(material) || !environmentBootstrapOwnerUUID.MatchString(completed.SecretUID)) {
		return publicTLSBlock("public TLS completion does not bind its original material and native owner")
	}
	uid := p.OwnerUID
	if completedErr == nil {
		uid = completed.SecretUID
	}
	// Re-enter the standard child even after completion: it verifies original
	// write receipts and fresh native UID/body; a file alone never proves health.
	observed, err := apply(ctx, publicTLSDefinition(p, material, p.OwnerUID))
	if err != nil {
		return err
	}
	if !environmentBootstrapOwnerUUID.MatchString(observed) || uid != "" && uid != observed {
		return publicTLSBlock("public TLS Secret publication changed its original native UID")
	}
	if err = e.Check(ctx); err != nil {
		return err
	}
	if completedErr == nil {
		return nil
	}
	return publicTLSWriteOwned(ctx, e, filepath.Join(base, "completed.json"), environmentPublicTLSReceipt{deploymentplan.Digest(p), deploymentplan.Digest(material), observed})
}

func resolveEnvironmentPublicTLSDefinitions(store secretStore, p *environmentPublicTLSPlan, defs []deploymentplan.Definition) ([]deploymentplan.Definition, error) {
	if p == nil {
		return defs, nil
	}
	if _, err := environmentPublicTLSOutputContract(p); err != nil {
		return nil, err
	}
	var material environmentPublicTLSMaterial
	var receipt environmentPublicTLSReceipt
	base := publicTLSBase(p)
	if publicTLSReadJSON(store, filepath.Join(base, "material.json"), &material) != nil || publicTLSReadJSON(store, filepath.Join(base, "completed.json"), &receipt) != nil || receipt.PlanSHA256 != deploymentplan.Digest(p) || receipt.MaterialSHA256 != deploymentplan.Digest(material) || !environmentBootstrapOwnerUUID.MatchString(receipt.SecretUID) || p.OwnerUID != "" && p.OwnerUID != receipt.SecretUID {
		return nil, publicTLSBlock("real public TLS material and original publication receipt are required")
	}
	if err := validateEnvironmentPublicTLSMaterial(p, material, time.Now()); err != nil {
		return nil, err
	}
	consumers, hosts, id, err := environmentPublicTLSConsumers(defs)
	if err != nil || id != p.SecretID || deploymentplan.Digest(consumers) != deploymentplan.Digest(p.Consumers) || !slices.Equal(hosts, p.Hosts) {
		return nil, publicTLSBlock("public TLS consumers differ from the frozen complete catalog")
	}
	result := append([]deploymentplan.Definition(nil), defs...)
	found := false
	for i, d := range result {
		if deploymentplan.ID(d.Object) == p.SecretID {
			if found {
				return nil, publicTLSBlock("duplicate public TLS output")
			}
			found = true
			result[i] = publicTLSDefinition(p, material, receipt.SecretUID)
		}
	}
	if !found {
		result = append(result, publicTLSDefinition(p, material, receipt.SecretUID))
	}
	return result, nil
}

func issueEnvironmentPublicTLS(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, p *environmentPublicTLSPlan, ops environmentPublicTLSOps) (environmentPublicTLSMaterial, error) {
	var empty environmentPublicTLSMaterial
	base := publicTLSBase(p)
	keyPath := filepath.Join(base, "keys.json")
	var keys environmentPublicTLSKeys
	err := publicTLSReadJSON(store, keyPath, &keys)
	if errors.Is(err, os.ErrNotExist) {
		// Only an untouched producer directory may establish these keys.
		// Missing keys from a retained attempt are lost identity, even if its
		// other cache files were also removed; never replace its ACME signer.
		path, pathErr := store.safePath(base)
		if pathErr != nil {
			return empty, pathErr
		}
		if _, stateErr := os.Lstat(path); !errors.Is(stateErr, os.ErrNotExist) || createOpenBaoACKCompletionEntry(e.Journal, "public-tls/certificate") != nil {
			return empty, publicTLSBlock("original public TLS keys are missing from a retained attempt; restore its exact account key, leaf key and CSR")
		}
		account, e1 := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		leaf, e2 := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if e1 != nil || e2 != nil {
			return empty, publicTLSBlock("public TLS key generation failed")
		}
		csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: p.Hosts[0]}, DNSNames: p.Hosts}, leaf)
		if err != nil {
			return empty, err
		}
		accountDER, _ := x509.MarshalPKCS8PrivateKey(account)
		leafDER, _ := x509.MarshalPKCS8PrivateKey(leaf)
		keys = environmentPublicTLSKeys{deploymentplan.Digest(p), string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: accountDER})), string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: leafDER})), base64.StdEncoding.EncodeToString(csr)}
		if err = e.Check(ctx); err != nil {
			return empty, err
		}
		if err = publicTLSWriteOwned(ctx, e, keyPath, keys); err != nil {
			return empty, err
		}
	} else if err != nil {
		return empty, err
	}
	accountBlock, _ := pem.Decode([]byte(keys.AccountKey))
	leafBlock, _ := pem.Decode([]byte(keys.LeafKey))
	csrDER, err := base64.StdEncoding.DecodeString(keys.CSR)
	if keys.PlanSHA256 != deploymentplan.Digest(p) || accountBlock == nil || leafBlock == nil || err != nil {
		return empty, publicTLSBlock("original public TLS key/CSR intent is invalid")
	}
	accountKey, e1 := x509.ParsePKCS8PrivateKey(accountBlock.Bytes)
	leafKey, e2 := x509.ParsePKCS8PrivateKey(leafBlock.Bytes)
	csr, e3 := x509.ParseCertificateRequest(csrDER)
	account, ok := accountKey.(crypto.Signer)
	leaf, leafOK := leafKey.(crypto.Signer)
	if e1 != nil || e2 != nil || e3 != nil || !ok || !leafOK || csr.CheckSignature() != nil || !slices.Equal(csr.DNSNames, p.Hosts) || deploymentplan.Digest(csr.PublicKey) != deploymentplan.Digest(leaf.Public()) {
		return empty, publicTLSBlock("original public TLS CSR does not match its durable key and exact names")
	}
	client := &acme.Client{Key: account, DirectoryURL: p.DirectoryURL, HTTPClient: ops.HTTP}
	var registration acme.Account
	if err = publicTLSReadJSON(store, filepath.Join(base, "account.json"), &registration); errors.Is(err, os.ErrNotExist) {
		contact := []string{}
		if p.Email != "" {
			contact = append(contact, "mailto:"+p.Email)
		}
		if err = e.Check(ctx); err != nil {
			return empty, err
		}
		registered, err := client.Register(ctx, &acme.Account{Contact: contact}, acme.AcceptTOS)
		if err != nil && !errors.Is(err, acme.ErrAccountAlreadyExists) {
			return empty, publicTLSBlock("ACME account registration is unresolved; the original account key is retained")
		}
		if registered == nil {
			registered, err = client.GetReg(ctx, "")
			if err != nil {
				return empty, publicTLSBlock("original ACME account cannot be read")
			}
		}
		registration = *registered
		if err = publicTLSWriteOwned(ctx, e, filepath.Join(base, "account.json"), registration); err != nil {
			return empty, err
		}
	} else if err != nil {
		return empty, err
	}
	if registration.URI == "" || registration.Status != acme.StatusValid {
		return empty, publicTLSBlock("original ACME account is not valid")
	}
	accountURL, accountErr := url.Parse(registration.URI)
	directoryURL, _ := url.Parse(p.DirectoryURL)
	if accountErr != nil || accountURL.Scheme != "https" || accountURL.Host != directoryURL.Host || accountURL.User != nil || accountURL.Fragment != "" {
		return empty, publicTLSBlock("original ACME account is outside its selected directory")
	}
	client.KID = acme.KeyID(registration.URI)
	const journalKey = "public-tls/certificate"
	intent, _ := json.Marshal(struct{ Plan, CSR string }{deploymentplan.Digest(p), deploymentplan.Digest(keys.CSR)})
	entry := createOpenBaoACKCompletionEntry(e.Journal, journalKey)
	var original acme.Order
	orderErr := publicTLSReadJSON(store, filepath.Join(base, "order.json"), &original)
	if errors.Is(orderErr, os.ErrNotExist) {
		if entry != nil {
			return empty, publicTLSBlock("an ACME order response is unknown; retain its key/CSR and reconcile the original request")
		}
		if err = e.BeginEntry(journalKey, "public-tls", intent, time.Now().UTC()); err != nil {
			return empty, err
		}
		order, err := client.AuthorizeOrder(ctx, acme.DomainIDs(p.Hosts...))
		if err != nil {
			return empty, publicTLSBlock("ACME order response is unknown; the original key and CSR are retained")
		}
		original = *order
		if err = publicTLSWriteOwned(ctx, e, filepath.Join(base, "order.json"), original); err != nil {
			return empty, err
		}
	} else if orderErr != nil {
		return empty, orderErr
	}
	entry = createOpenBaoACKCompletionEntry(e.Journal, journalKey)
	intentSHA, _ := environmentCreatePlanSHA256(intent)
	if entry == nil || entry.IntentSHA256 != intentSHA || original.URI == "" {
		return empty, publicTLSBlock("original ACME order has no matching durable parent intent")
	}
	if err = validatePublicTLSOrder(p, original, original); err != nil {
		return empty, err
	}
	original, attemptsSHA, attempts, err := readEnvironmentPublicTLSOrderAttempts(store, p, deploymentplan.Digest(keys.CSR))
	if err != nil {
		return empty, err
	}
	order, err := client.GetOrder(ctx, original.URI)
	if err != nil {
		return empty, publicTLSBlock("original ACME order cannot be observed")
	}
	if order.URI == "" {
		order.URI = original.URI
	} // POST-as-GET need not repeat Location.
	if err = validatePublicTLSOrder(p, original, *order); err != nil {
		return empty, err
	}
	operator, err := store.readOperator()
	if err != nil {
		return empty, err
	}
	adapter, local, err := ops.DNS(cfg, operator)
	if err != nil {
		return empty, err
	}
	zone, err := adapter.DiscoverZone(ctx, local)
	if err != nil || local.RootDomain != p.RootDomain || zone.Name != p.RootDomain || adapter.Name() != p.DNSAdapter {
		return empty, publicTLSBlock("original DNS-01 authority changed")
	}
	for order.Status == acme.StatusInvalid {
		if len(attempts) >= environmentPublicTLSMaxAttempts {
			return empty, publicTLSBlock("ACME validation attempts are exhausted; retain every original order and DNS diagnostic")
		}
		// Only a fresh signed read of a known terminal INVALID order permits
		// another request. Unknown outcomes never reach this branch.
		if err = cleanupEnvironmentPublicTLSChallenges(ctx, e, client, adapter, local, zone, *order); err != nil {
			return empty, err
		}
		attempt := environmentPublicTLSOrderAttempt{Plan: deploymentplan.Digest(p), CSR: deploymentplan.Digest(keys.CSR), PreviousOrder: original.URI, PreviousSHA256: deploymentplan.Digest(original), Invalid: *order, RequestedAt: time.Now().UTC()}
		attemptBase := publicTLSAttemptDirectory(p, len(attempts)+1)
		if err = publicTLSWriteOwned(ctx, e, filepath.Join(attemptBase, "intent.json"), attempt); err != nil {
			return empty, err
		}
		created, createErr := client.AuthorizeOrder(ctx, acme.DomainIDs(p.Hosts...))
		if createErr != nil {
			return empty, publicTLSBlock("ACME retry response is unknown; retain the attempt intent and original key/CSR without reissuing")
		}
		if err = validatePublicTLSOrder(p, *created, *created); err != nil {
			return empty, err
		}
		if err = publicTLSWriteOwned(ctx, e, filepath.Join(attemptBase, "order.json"), created); err != nil {
			return empty, err
		}
		original, attemptsSHA, attempts, err = readEnvironmentPublicTLSOrderAttempts(store, p, deploymentplan.Digest(keys.CSR))
		if err != nil {
			return empty, err
		}
		order, err = client.GetOrder(ctx, original.URI)
		if err != nil {
			return empty, publicTLSBlock("original ACME retry order cannot be observed")
		}
		if order.URI == "" {
			order.URI = original.URI
		}
		if err = validatePublicTLSOrder(p, original, *order); err != nil {
			return empty, err
		}
	}
	if order.Status == acme.StatusPending {
		type pendingDNS01 struct {
			URL, Domain, Name, Value string
			Challenge                *acme.Challenge
			Valid                    bool
		}
		var pending []pendingDNS01
		expected := map[string]string{}
		propagationPath := filepath.Join(publicTLSAttemptDirectory(p, len(attempts)), "propagation.json")
		var propagation environmentPublicTLSDNSPropagation
		propagationErr := publicTLSReadJSON(store, propagationPath, &propagation)
		if propagationErr != nil && !errors.Is(propagationErr, os.ErrNotExist) {
			return empty, propagationErr
		}
		owned, err := readDNSChallenges(local, adapter.Name())
		if err != nil {
			return empty, err
		}
		for _, authURL := range order.AuthzURLs {
			auth, err := client.GetAuthorization(ctx, authURL)
			if err != nil || auth.Wildcard || auth.Identifier.Type != "dns" || !slices.Contains(p.Hosts, auth.Identifier.Value) {
				return empty, publicTLSBlock("ACME authorization differs from the exact public names")
			}
			if auth.Status != acme.StatusPending && auth.Status != acme.StatusValid {
				return empty, publicTLSBlock("original ACME authorization is no longer usable")
			}
			var challenge *acme.Challenge
			for _, c := range auth.Challenges {
				if c.Type == "dns-01" {
					challenge = c
					break
				}
			}
			if challenge == nil {
				if auth.Status == acme.StatusValid {
					continue
				}
				return empty, publicTLSBlock("the original ACME order has no DNS-01 challenge")
			}
			value, err := client.DNS01ChallengeRecord(challenge.Token)
			if err != nil {
				return empty, err
			}
			if auth.Status == acme.StatusValid {
				ours := false
				for _, record := range owned {
					ours = ours || record.Domain == auth.Identifier.Value && record.Value == value
				}
				if !ours {
					continue
				}
			}
			name, err := dns01RecordName(zone.Name, auth.Identifier.Value)
			if err != nil {
				return empty, err
			}
			expected[name] = value
			pending = append(pending, pendingDNS01{authURL, auth.Identifier.Value, name, value, challenge, auth.Status == acme.StatusValid})
		}
		// Publish the complete order before querying public resolvers or asking
		// the CA to validate any name. This avoids serial negative-cache races.
		for _, item := range pending {
			if err = e.Check(ctx); err != nil {
				return empty, err
			}
			// Record ownership before the remote TXT write. Cleanup is idempotent and
			// removes only this exact value, including an interrupted write response.
			if err = recordDNSChallenge(local, adapter.Name(), item.Domain, item.Value); err != nil {
				return empty, err
			}
			if propagationErr == nil {
				current, err := adapter.GetRecordSet(ctx, local, zone, item.Name, "TXT")
				if err != nil || !slices.Contains(current.Values, item.Value) {
					return empty, publicTLSBlock("original DNS-01 propagation proof lost its exact provider TXT value")
				}
			} else if err = adapter.PresentDNS01Challenge(ctx, local, zone, item.Domain, item.Value); err != nil {
				return empty, publicTLSBlock("DNS-01 publication is unresolved; exact challenge ownership is retained")
			}
		}
		if len(expected) > 0 && ops.WaitDNS01Set != nil {
			var prior *environmentPublicTLSDNSPropagation
			if propagationErr == nil {
				prior = &propagation
			}
			measure := beginDeploymentMeasurement(ctx, "deployment.public-tls.dns-propagation")
			err = ops.WaitDNS01Set(ctx, p.RootDomain, expected, local.Values, prior, func(proof environmentPublicTLSDNSPropagation) error {
				if err := savePublicTLSDNSPropagation(ctx, e, propagationPath, prior, proof); err != nil {
					return err
				}
				propagation = proof
				prior = &propagation
				return nil
			})
			measure(err)
			if err != nil {
				return empty, err
			}
		} else {
			for _, item := range pending {
				if ops.WaitTXT == nil {
					return empty, publicTLSBlock("DNS-01 propagation verifier is unavailable")
				}
				if err = ops.WaitTXT(ctx, item.Name, item.Value, local.Values); err != nil {
					return empty, err
				}
			}
		}
		for _, item := range pending {
			if err = e.Check(ctx); err != nil {
				return empty, err
			}
			if item.Valid || item.Challenge.Status == acme.StatusProcessing {
				continue
			}
			if _, err = client.Accept(ctx, item.Challenge); err != nil {
				return empty, publicTLSBlock("original DNS-01 acknowledgement is unresolved")
			}
		}
		for _, item := range pending {
			if item.Valid {
				continue
			}
			if _, err = client.WaitAuthorization(ctx, item.URL); err != nil {
				return empty, publicTLSAuthorizationFailure(p, item.Domain, err)
			}
			if err = e.Check(ctx); err != nil {
				return empty, err
			}
		}
		order, err = client.WaitOrder(ctx, original.URI)
		if err != nil {
			return empty, publicTLSBlock("original ACME order is not ready")
		}
	}
	if order.Status == acme.StatusProcessing {
		order, err = client.WaitOrder(ctx, original.URI)
		if err != nil {
			return empty, err
		}
	}
	if order.URI == "" {
		order.URI = original.URI
	}
	if err = validatePublicTLSOrder(p, original, *order); err != nil {
		return empty, err
	}
	if err = cleanupEnvironmentPublicTLSChallenges(ctx, e, client, adapter, local, zone, *order); err != nil {
		return empty, err
	}
	if err = e.Check(ctx); err != nil {
		return empty, err
	}
	var chain [][]byte
	if order.Status == acme.StatusValid {
		chain, err = client.FetchCert(ctx, order.CertURL, true)
	} else if order.Status == acme.StatusReady {
		chain, _, err = client.CreateOrderCert(ctx, order.FinalizeURL, csrDER, true)
	} else {
		return empty, publicTLSBlock("original ACME order cannot issue the frozen CSR")
	}
	if err != nil {
		return empty, publicTLSBlock("original ACME finalization response is unresolved; retry observes the same order")
	}
	var certificate []byte
	for _, der := range chain {
		certificate = append(certificate, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	}
	material := environmentPublicTLSMaterial{string(certificate), keys.LeafKey}
	if err = validateEnvironmentPublicTLSMaterial(p, material, time.Now()); err != nil {
		return empty, err
	}
	// An interrupted completed remote order remains recoverable by its URL. The
	// parent receipt is public hashes, never the private key or challenge value.
	receipt, _ := json.Marshal(struct {
		Plan, Order, CSR, Material string
		Attempts                   string `json:",omitempty"`
	}{deploymentplan.Digest(p), original.URI, deploymentplan.Digest(keys.CSR), deploymentplan.Digest(material), attemptsSHA})
	if entry.State == "completed" {
		canonical, err := canonicalEnvironmentCreateJSON(receipt)
		if err != nil || string(entry.Receipt) != string(canonical) {
			return empty, publicTLSBlock("original ACME completion receipt changed")
		}
	} else {
		if err = e.RecordReceipt(journalKey, receipt); err != nil {
			return empty, err
		}
		if err = e.CompleteEntry(journalKey, time.Now().UTC()); err != nil {
			return empty, err
		}
	}
	return material, nil
}

// A recovered ready/valid order may have completed its authorization just
// before cancellation. Remove only values recorded for this exact order's
// challenges; never clear another order's TXT values or the whole record set.
func cleanupEnvironmentPublicTLSChallenges(ctx context.Context, e *environmentCreateExecution, client *acme.Client, adapter dnsAdapter, local dnsAdapterContext, zone dnsZone, order acme.Order) error {
	owned, err := readDNSChallenges(local, adapter.Name())
	if err != nil {
		return err
	}
	if len(owned) == 0 {
		return nil
	}
	for _, authURL := range order.AuthzURLs {
		auth, err := client.GetAuthorization(ctx, authURL)
		if err != nil {
			return publicTLSBlock("original ACME challenge cleanup cannot be observed")
		}
		for _, challenge := range auth.Challenges {
			if challenge.Type != "dns-01" {
				continue
			}
			value, err := client.DNS01ChallengeRecord(challenge.Token)
			if err != nil {
				return err
			}
			for _, item := range owned {
				if item.Domain != auth.Identifier.Value || item.Value != value {
					continue
				}
				if err = e.Check(ctx); err != nil {
					return err
				}
				if err = adapter.CleanupDNS01Challenge(ctx, local, zone, item.Domain, item.Value); err != nil {
					return publicTLSBlock("original DNS-01 challenge cleanup is incomplete")
				}
				if err = forgetDNSChallenge(local, adapter.Name(), item.Domain, item.Value); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// Keep CA diagnostics useful without exposing challenge values, account keys or
// arbitrary provider text. All returned labels come from this finite vocabulary.
func publicTLSAuthorizationFailure(p *environmentPublicTLSPlan, domain string, err error) error {
	cause := "original DNS-01 authorization did not complete"
	if slices.Contains(p.Hosts, domain) {
		cause += " for " + domain
	}
	var authorization *acme.AuthorizationError
	if errors.As(err, &authorization) {
		for _, problem := range authorization.Errors {
			var detail *acme.Error
			if !errors.As(problem, &detail) {
				continue
			}
			kind := strings.TrimPrefix(detail.ProblemType, "urn:ietf:params:acme:error:")
			if !slices.Contains([]string{"dns", "unauthorized", "connection", "rateLimited", "malformed", "rejectedIdentifier", "caa", "serverInternal"}, kind) {
				kind = "unavailable"
			}
			cause += "; CA problem=" + kind
			for _, code := range []string{"NXDOMAIN", "SERVFAIL"} {
				if strings.Contains(detail.Detail, code) {
					cause += "/" + code
				}
			}
			break
		}
	}
	return publicTLSBlock(cause)
}

func validatePublicTLSOrder(p *environmentPublicTLSPlan, original, current acme.Order) error {
	if !current.Expires.IsZero() && !time.Now().Before(current.Expires) {
		return publicTLSBlock("original ACME order identity or validity changed")
	}
	return validatePublicTLSOrderIdentity(p, original, current)
}

func validatePublicTLSOrderIdentity(p *environmentPublicTLSPlan, original, current acme.Order) error {
	if current.URI == "" || current.FinalizeURL == "" || current.URI != original.URI || current.FinalizeURL != original.FinalizeURL || !slices.Equal(current.AuthzURLs, original.AuthzURLs) || len(current.AuthzURLs) != len(p.Hosts) || len(current.Identifiers) != len(p.Hosts) {
		return publicTLSBlock("original ACME order identity or validity changed")
	}
	var names []string
	for _, id := range current.Identifiers {
		if id.Type != "dns" {
			return publicTLSBlock("ACME order contains an unrelated identifier")
		}
		names = append(names, id.Value)
	}
	sort.Strings(names)
	if !slices.Equal(names, p.Hosts) {
		return publicTLSBlock("ACME order DNS names differ from the complete public consumer contract")
	}
	directory, _ := url.Parse(p.DirectoryURL)
	for _, endpoint := range append([]string{current.URI, current.FinalizeURL, current.CertURL}, current.AuthzURLs...) {
		if endpoint == "" {
			continue
		}
		u, err := url.Parse(endpoint)
		if err != nil || u.Scheme != "https" || u.Host != directory.Host || u.User != nil || u.Fragment != "" {
			return publicTLSBlock("ACME order endpoint is outside its selected authenticated directory")
		}
	}
	return nil
}
