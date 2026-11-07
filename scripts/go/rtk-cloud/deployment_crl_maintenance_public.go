package main

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"time"
)

type maintenancePublicCRL struct {
	Namespace, Pod, PodUID, Container, Path, Resource, UID, Version, Key, Original string
	Issuers                                                                        []string
	Producer                                                                       string
	Claims                                                                         map[string]string
}

func maintenanceBundleIssuers(raw []byte, known map[string]environmentCreateRegisteredIssuer) ([]string, error) {
	var ids []string
	seen := map[string]bool{}
	for len(bytes.TrimSpace(raw)) > 0 {
		block, rest := pem.Decode(raw)
		if block == nil || block.Type != "X509 CRL" || len(block.Headers) != 0 {
			return nil, errors.New("public CRL bundle contains invalid PEM")
		}
		raw = rest
		crl, err := x509.ParseRevocationList(block.Bytes)
		if err != nil {
			return nil, err
		}
		id := ""
		for candidate, issuer := range known {
			cert, e := parseFirstCertificatePEM([]byte(issuer.Certificate))
			if e == nil && crl.CheckSignatureFrom(cert) == nil && bytes.Equal(crl.RawIssuer, cert.RawSubject) {
				if id != "" {
					return nil, errors.New("ambiguous public CRL issuer")
				}
				id = candidate
			}
		}
		if id == "" || seen[id] {
			return nil, errors.New("public CRL bundle has unknown or duplicate authority")
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, errors.New("public CRL bundle is empty")
	}
	return ids, nil
}

func (r *maintenanceRuntime) publicProjection(ctx context.Context, pod, c deploymentplan.Object, path string, known map[string]environmentCreateRegisteredIssuer) (maintenancePublicCRL, error) {
	meta := deploymentplan.Map(pod["metadata"])
	out := maintenancePublicCRL{Namespace: deploymentplan.String(meta["namespace"]), Pod: deploymentplan.String(meta["name"]), PodUID: deploymentplan.String(meta["uid"]), Container: deploymentplan.String(c["name"]), Path: path}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return out, errors.New("public CRL path invalid")
	}
	for _, mv := range certIssuerObjectList(c["volumeMounts"]) {
		m := deploymentplan.Map(mv)
		mount := deploymentplan.String(m["mountPath"])
		if !strings.HasPrefix(path, mount+"/") {
			continue
		}
		if m["subPath"] != nil || m["subPathExpr"] != nil {
			return out, errors.New("public CRL subPath cannot refresh in place")
		}
		for _, vv := range certIssuerObjectList(deploymentplan.Map(pod["spec"])["volumes"]) {
			v := deploymentplan.Map(vv)
			if v["name"] != m["name"] {
				continue
			}
			kind := "Secret"
			source := deploymentplan.Map(v["secret"])
			name := deploymentplan.String(source["secretName"])
			if name == "" {
				kind = "ConfigMap"
				source = deploymentplan.Map(v["configMap"])
				name = deploymentplan.String(source["name"])
			}
			if name == "" {
				return r.runtimePublicProjection(ctx, pod, out, m, v, known)
			}
			key := strings.TrimPrefix(path, mount+"/")
			if items := certIssuerObjectList(source["items"]); len(items) > 0 {
				found := ""
				for _, iv := range items {
					i := deploymentplan.Map(iv)
					if i["path"] == key {
						if found != "" {
							return out, errors.New("ambiguous public CRL key")
						}
						found = deploymentplan.String(i["key"])
					}
				}
				key = found
			}
			if key == "" || strings.ContainsAny(key, "/\\") {
				return out, errors.New("public CRL source key invalid")
			}
			if out.Resource != "" {
				return out, errors.New("public CRL source mount ambiguous")
			}
			out.Resource = kind + "/" + out.Namespace + "/" + name
			object, err := r.read(ctx, out.Resource)
			if err != nil || object == nil {
				return out, errors.New("public CRL source unavailable")
			}
			metadata := deploymentplan.Map(object["metadata"])
			out.UID = deploymentplan.String(metadata["uid"])
			out.Version = deploymentplan.String(metadata["resourceVersion"])
			out.Key = key
			body := []byte(deploymentplan.String(deploymentplan.Map(object["data"])[key]))
			if kind == "Secret" {
				body, err = base64.StdEncoding.DecodeString(string(body))
				if err != nil {
					return out, errors.New("public CRL Secret field invalid")
				}
			}
			out.Original = string(body)
			out.Issuers, err = maintenanceBundleIssuers(body, known)
			if err != nil {
				return out, err
			}
		}
	}
	if out.Resource == "" || out.UID == "" || out.Version == "" {
		return out, errors.New("public CRL projection has no exact original owner")
	}
	return out, nil
}

func maintenancePublicBundle(p maintenancePublicCRL, records map[string]createRootCRLRecord) (string, error) {
	var b strings.Builder
	for _, id := range p.Issuers {
		record, ok := records[id]
		if !ok {
			return "", errors.New("public CRL projection authority missing")
		}
		b.WriteString(record.PEM)
	}
	return b.String(), nil
}

func (r *maintenanceRuntime) publishPublicCRLs(ctx context.Context, projections []maintenancePublicCRL, records map[string]createRootCRLRecord) error {
	seen := map[string]bool{}
	for _, p := range projections {
		// Native public exports are owned by the running producer. Installing
		// its signed durable state lets its normal refresh publish the bundle.
		if p.Producer != "" {
			continue
		}
		key := p.Resource + "/" + p.Key
		if seen[key] {
			continue
		}
		seen[key] = true
		body, err := maintenancePublicBundle(p, records)
		if err != nil {
			return err
		}
		if body == p.Original {
			continue
		}
		if err := r.lease.Check(ctx); err != nil {
			return err
		}
		parts := strings.Split(p.Resource, "/")
		value := body
		if parts[0] == "Secret" {
			value = base64.StdEncoding.EncodeToString([]byte(body))
		}
		patch, _ := json.Marshal([]map[string]any{{"op": "test", "path": "/metadata/uid", "value": p.UID}, {"op": "test", "path": "/metadata/resourceVersion", "value": p.Version}, {"op": "replace", "path": "/data/" + strings.ReplaceAll(strings.ReplaceAll(p.Key, "~", "~0"), "/", "~1"), "value": value}})
		if _, err := r.command(ctx, nil, "--namespace", p.Namespace, "patch", strings.ToLower(parts[0]), parts[2], "--type=json", "--patch", string(patch)); err != nil {
			return errors.New("original public CRL projection changed; rediscover before retry")
		}
	}
	return nil
}

func (r *maintenanceRuntime) verifyPublicCRLs(ctx context.Context, projections []maintenancePublicCRL, records map[string]createRootCRLRecord) error {
	for _, p := range projections {
		expected, err := maintenancePublicBundle(p, records)
		if err != nil {
			return err
		}
		raw, err := r.command(ctx, nil, "--namespace", p.Namespace, "exec", "pod/"+p.Pod, "--container", p.Container, "--", "cat", p.Path)
		if err != nil || string(raw) != expected {
			return fmt.Errorf("public CRL projection %s/%s/%s not reloaded", p.Namespace, p.Pod, p.Path)
		}
		pod, err := r.read(ctx, "Pod/"+p.Namespace+"/"+p.Pod)
		if err != nil || deploymentplan.String(deploymentplan.Map(pod["metadata"])["uid"]) != p.PodUID {
			return errors.New("public CRL consumer owner changed")
		}
		for claim, uid := range p.Claims {
			actual, err := r.read(ctx, claim)
			if err != nil || deploymentplan.String(deploymentplan.Map(actual["metadata"])["uid"]) != uid {
				return errors.New("public CRL producer durable volume changed")
			}
		}
		ready, producerReady := false, p.Producer == ""
		for _, cv := range certIssuerObjectList(deploymentplan.Map(pod["status"])["containerStatuses"]) {
			c := deploymentplan.Map(cv)
			if c["name"] == p.Container {
				ready = c["ready"] == true
			}
			if c["name"] == p.Producer {
				producerReady = c["ready"] == true
			}
		}
		if !ready || !producerReady {
			return errors.New("public CRL consumer or producer is not ready")
		}
	}
	return nil
}

// ACK identities come from the workload's current authenticated Service state,
// never from a Kubernetes display name. Private fields stay in process memory.
func (r *maintenanceRuntime) consumerID(ctx context.Context, ns, pod, container string, c deploymentplan.Object) (string, error) {
	subject := ""
	for _, ev := range certIssuerObjectList(c["env"]) {
		e := deploymentplan.Map(ev)
		name := deploymentplan.String(e["name"])
		path := deploymentplan.String(e["value"])
		managed := strings.HasSuffix(name, "_IDENTITY_STATE")
		static := strings.HasSuffix(name, "_CLIENT_CERT") || strings.HasSuffix(name, "_CLIENT_CERT_PATH")
		if (!managed && !static) || !filepath.IsAbs(path) {
			continue
		}
		raw, err := r.command(ctx, nil, "--namespace", ns, "exec", "pod/"+pod, "--container", container, "--", "cat", path)
		if err != nil || len(raw) > 256<<10 {
			continue
		}
		var state struct {
			Subject string `json:"subject"`
			Current struct {
				Chain string `json:"certificate_chain_pem"`
			} `json:"current"`
		}
		if managed {
			if json.Unmarshal(raw, &state) != nil {
				continue
			}
		} else {
			cert, err := parseFirstCertificatePEM(raw)
			if err != nil {
				return "", fmt.Errorf("native client certificate is invalid for %s/%s/%s", ns, pod, container)
			}
			state.Subject = cert.Subject.CommonName
			state.Current.Chain = string(raw)
		}
		if !strings.HasPrefix(state.Subject, "service:") {
			continue
		}
		id := strings.TrimPrefix(state.Subject, "service:")
		if !environmentCreateConsumerID.MatchString(id) {
			return "", errors.New("native consumer subject invalid")
		}
		cert, err := parseFirstCertificatePEM([]byte(state.Current.Chain))
		if err != nil || cert.Subject.CommonName != state.Subject || time.Now().Before(cert.NotBefore) || !time.Now().Before(cert.NotAfter) {
			return "", fmt.Errorf("CRL consumer %s/%s has an expired or invalid Service leaf", ns, pod)
		}
		fingerprint := environmentPreflightBytesSHA256(cert.Raw)
		receipt, err := r.query(ctx, "SELECT EXISTS(SELECT 1 FROM pki_service_client_issuances WHERE environment='"+r.cfg.Environment+"' AND subject='"+state.Subject+"' AND status='succeeded' AND revoked_at IS NULL AND fingerprint='"+fingerprint+"')")
		if err != nil || string(receipt) != "t" {
			return "", fmt.Errorf("CRL consumer %s/%s/%s identity %s has no matching live Service issuance", ns, pod, container, path)
		}
		if subject != "" && subject != id {
			return "", errors.New("ambiguous native CRL consumer identity")
		}
		subject = id
	}
	if subject == "" {
		return "", fmt.Errorf("actual Service identity unavailable for CRL consumer %s/%s/%s", ns, pod, container)
	}
	return subject, nil
}
