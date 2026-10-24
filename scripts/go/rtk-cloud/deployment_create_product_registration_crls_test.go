package main

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestProductRegistrationCRLsNewProgramPublishesBeforeFiveEnrollments(t *testing.T) {
	var originalSecret deploymentplan.Object
	setup := func(f *productProtocolFixture, p *createNormalTailProgram) {
		ns := f.p.Stack + "-account-manager"
		secret := environmentCreateSecret(ns, "account-manager-service-registration-tls", map[string]string{"client-ca.crt": f.l.Root.Certificate, "tls.crt": f.next.Certificate, "tls.key": "isolated-unchanged-server-key", "client.crl": "original-signed-root-and-old-issuer-crls"}).Object
		data := deploymentplan.Object{}
		for k, v := range deploymentplan.Map(secret["stringData"]) {
			data[k] = base64.StdEncoding.EncodeToString([]byte(deploymentplan.String(v)))
		}
		secret["data"] = data
		delete(secret, "stringData")
		deploymentplan.Map(secret["metadata"])["uid"] = "77777777-aaaa-4aaa-8aaa-111111111111"
		deploymentplan.Map(secret["metadata"])["labels"] = deploymentplan.Object{"rtk.realtek.com/stack": f.p.Stack, "app.kubernetes.io/managed-by": "rtk-cloud"}
		originalSecret = deploymentplan.Clone(secret)
		deploymentplan.Map(secret["metadata"])["resourceVersion"] = "1"
		f.b.objects[deploymentplan.ID(secret)] = secret
		f.l.SourceOwners[deploymentplan.ID(secret)] = deploymentplan.String(deploymentplan.Map(secret["metadata"])["uid"])
		f.l.SourceDefinitions = append(f.l.SourceDefinitions, deploymentplan.Definition{Object: deploymentplan.Clone(secret), Phase: "configuration", Retain: true})
		reader := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"namespace": ns, "name": "account-manager", "uid": "77777777-aaaa-4aaa-8aaa-222222222222", "labels": deploymentplan.Object{"rtk.realtek.com/stack": f.p.Stack, "app.kubernetes.io/managed-by": "rtk-cloud"}}, "spec": deploymentplan.Object{"replicas": float64(1), "selector": deploymentplan.Object{"matchLabels": deploymentplan.Object{"app": "account-manager"}}, "template": deploymentplan.Object{"metadata": deploymentplan.Object{"labels": deploymentplan.Object{"app": "account-manager"}}, "spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "account-manager", "image": f.p.AccountManagerImage, "volumeMounts": []any{deploymentplan.Object{"name": "registration", "mountPath": "/etc/rtk-account-manager/service-registration", "readOnly": true}}}}, "volumes": []any{environmentCreateServiceVolume("registration", "secret", "account-manager-service-registration-tls")}}}}}
		deploymentplan.Map(reader["metadata"])["resourceVersion"] = "1"
		f.b.objects[deploymentplan.ID(reader)] = reader
		f.l.SourceOwners[deploymentplan.ID(reader)] = deploymentplan.String(deploymentplan.Map(reader["metadata"])["uid"])
		f.l.SourceDefinitions = append(f.l.SourceDefinitions, deploymentplan.Definition{Object: deploymentplan.Clone(reader), Phase: "workload"})
		transport := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"namespace": f.p.Stack + "-video-cloud", "name": "pki-create-transport-trust", "uid": "77777777-aaaa-4aaa-8aaa-333333333333", "labels": deploymentplan.Object{"rtk.realtek.com/stack": f.p.Stack, "app.kubernetes.io/managed-by": "rtk-cloud"}}, "data": deploymentplan.Object{"ca.crt": f.runtime.TransportCAPEM}}
		deploymentplan.Map(transport["metadata"])["resourceVersion"] = "1"
		f.b.objects[deploymentplan.ID(transport)] = transport
		f.l.SourceOwners[deploymentplan.ID(transport)] = deploymentplan.String(deploymentplan.Map(transport["metadata"])["uid"])
		f.l.SourceDefinitions = append(f.l.SourceDefinitions, deploymentplan.Definition{Object: deploymentplan.Clone(transport), Phase: "configuration", Retain: true})
		var live []deploymentplan.Object
		for _, o := range f.b.objects {
			live = append(live, o)
		}
		if err := bindCreateNormalTailProductSourceDefinitions(f.l, f.l.SourceDefinitions, live, environmentIdentityBindings{Environment: "dev", Stack: f.p.Stack, Owners: f.l.SourceOwners}); err != nil {
			t.Fatal(err)
		}
	}
	f, p, _, start, apply := newProductOuterTargetFixtureWithSetup(t, true, setup)
	before := deploymentplan.Digest(&p)
	scope := productIdentityScope(f.l)
	if productRegistrationCRLSource(f.l) == nil {
		t.Fatal("new Program omitted its legacy CRL consumer")
	}
	base := f.server.Config.Handler
	f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/"+f.l.Intermediate.ID+"/crl") {
			now := time.Now().UTC().Truncate(time.Second)
			der, err := x509.CreateRevocationList(rand.Reader, &x509.RevocationList{Number: big.NewInt(1), ThisUpdate: now.Add(-time.Minute), NextUpdate: now.Add(time.Hour)}, f.ica, f.icaKey)
			if err != nil {
				t.Fatal(err)
			}
			json.NewEncoder(w).Encode(deploymentplan.Object{"issuer_id": f.l.Intermediate.ID, "crl_sha256": environmentPreflightBytesSHA256(der), "crl_number": "1", "crl_pem": productPEM("X509 CRL", der), "this_update": now.Add(-time.Minute), "next_update": now.Add(time.Hour)})
			return
		}
		base.ServeHTTP(w, r)
	})
	enrolled := 0
	checkedApply := func(ctx context.Context, stage string, defs []deploymentplan.Definition) error {
		if strings.HasPrefix(stage, "enroll-") {
			current := f.b.objects[createNormalTailAccountRegistrationCRLID(f.p.Stack)]
			raw, _ := base64.StdEncoding.DecodeString(deploymentplan.String(deploymentplan.Map(current["data"])["client.crl"]))
			if strings.Count(string(raw), "-----BEGIN X509 CRL-----") != 3 {
				t.Fatal("product enrollment started before legacy listener received current three-issuer CRLs")
			}
			enrolled++
		}
		if err := apply(ctx, stage, defs); err != nil {
			return err
		}
		if stage == "successor-crls" {
			writes := deploymentplan.Digest(f.b.writes)
			if err := apply(ctx, stage, defs); err != nil {
				return err
			}
			if writes != deploymentplan.Digest(f.b.writes) {
				t.Fatal("accepted CRL child retry rewrote a native owner")
			}
		}
		return nil
	}
	if err := executeProductIdentityProtocol(t.Context(), f.cfg, f.p, f.store, f.runtime, f.l, checkedApply); err != nil {
		t.Fatal("actual new Program product protocol", err)
	}
	if enrolled != 5 || deploymentplan.Digest(&p) != before || productIdentityScope(f.l) != scope {
		t.Fatal("frozen Program changed or five real enrollments missing")
	}
	id := createNormalTailAccountRegistrationCRLID(f.p.Stack)
	actual := f.b.objects[id]
	for key, value := range deploymentplan.Map(originalSecret["data"]) {
		if key != "client.crl" && deploymentplan.Map(actual["data"])[key] != value {
			t.Fatal("server identity or CA changed", key)
		}
	}
	if deploymentplan.Map(actual["metadata"])["uid"] != deploymentplan.Map(originalSecret["metadata"])["uid"] || f.b.writes[id] != 1 {
		t.Fatal("legacy listener owner replaced or update repeated")
	}
	consumed, _, err := validateCreateNormalTailProductStagesWithConfig(f.cfg, f.store, f.e, f.l, f.e.Journal.Entries[start:])
	if err != nil || consumed != len(f.e.Journal.Entries)-start {
		t.Fatal("actual chronological stage reader", consumed, err)
	}
	var fresh createNormalTailProductLifecycle
	json.Unmarshal(mustProviderRecoveryJSON(f.l), &fresh)
	installed, err := readProductInstalled(f.store, &fresh)
	if err != nil || installed == nil || fresh.registrationCRLs == nil {
		t.Fatal("restored current target lost public CRL source", err)
	}
	definitions, err := resolveCreateNormalTailProductDefinitions(f.cfg, &fresh, installed)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range definitions {
		if deploymentplan.ID(d.Object) == id {
			found = true
			if deploymentplan.Map(d.Object["data"])["client.crl"] != deploymentplan.Map(actual["data"])["client.crl"] {
				t.Fatal("target restored old CRLs")
			}
		}
	}
	if !found {
		t.Fatal("target omitted legacy registration Secret")
	}
	path, _ := f.store.safePath(productRegistrationCRLBase(f.l))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	mode, _ := os.Stat(path)
	if mode.Mode().Perm() != 0600 {
		t.Fatal("public operational source mode")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := prepareProductRegistrationCRLs(t.Context(), f.store, f.runtime, f.l, installed.Ceremony.Intermediate); err == nil {
		t.Fatal("missing public source after recorded child was regenerated")
	}
	rel, _ := filepath.Rel(f.store.Root, path)
	if err := f.store.write(rel, raw, false); err != nil {
		t.Fatal(err)
	}
	writes := deploymentplan.Digest(f.b.writes)
	if err := prepareProductRegistrationCRLs(t.Context(), f.store, f.runtime, f.l, installed.Ceremony.Intermediate); err != nil {
		t.Fatal("same public source retry", err)
	}
	if writes != deploymentplan.Digest(f.b.writes) {
		t.Fatal("protocol retry rewrote accepted native effects")
	}
}
func TestProductRegistrationCRLsHistoricalCatalogKeepsLiteralRecipes(t *testing.T) {
	f := newProductProtocolFixture(t, true)
	before := deploymentplan.Digest(f.l)
	if err := prepareProductRegistrationCRLs(t.Context(), f.store, f.runtime, f.l, f.next); err != nil {
		t.Fatal(err)
	}
	if productRegistrationCRLSource(f.l) != nil || f.l.registrationCRLs != nil || before != deploymentplan.Digest(f.l) {
		t.Fatal("historical catalog acquired new authority")
	}
	path, _ := f.store.safePath(productRegistrationCRLBase(f.l))
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("historical recipe generated an operational source")
	}
}
