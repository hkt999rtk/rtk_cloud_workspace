package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type postCleanupCorrectionFixtureState struct {
	public          environmentCertIssuerPublicProfile
	log             environmentLogIngesterPVCWriterProfile
	key             *ecdsa.PrivateKey
	originalState   []byte
	originalLeaf    []byte
	newLeaf         []byte
	proof           environmentCertIssuerPublicProof
	controllerImage string
	failKnown       bool
	applyWrites     map[string]int
}

// Add the maintained historical cohort before Program freezing. Native signing,
// filesystem permissions and the controller worker have separate real native
// tests; this fixture models their read-only transport boundaries explicitly.
func installPostCleanupCorrectionFixture(t *testing.T, f *productProtocolFixture, p *createNormalTailProgram, add func(deploymentplan.Definition, bool)) *postCleanupCorrectionFixtureState {
	t.Helper()
	_, catalog, profile := certIssuerPublicCorrectionFixture(t)
	ns := f.p.Stack + "-video-cloud"
	ids := keySet("IngressClass//"+f.p.Stack+"-nginx", "Service/"+ns+"/certissuer", "Ingress/"+ns+"/certissuer-public-mtls", "ServiceAccount/"+ns+"/certissuer", "ServiceAccount/"+ns+"/pki-controller", "PersistentVolumeClaim/"+ns+"/certissuer-service-state")
	for _, d := range catalog {
		if ids[deploymentplan.ID(d.Object)] {
			d.Object = deploymentplan.Clone(d.Object)
			if d.Object["kind"] == "Ingress" {
				rules := certIssuerObjectList(deploymentplan.Map(d.Object["spec"])["rules"])
				if len(rules) != 1 {
					t.Fatal("one maintained public route required")
				}
				deploymentplan.Map(rules[0])["host"] = lkeResolveCertIssuerTLSConfig(deploymentEndpointValues(f.cfg)).PublicHost
				if err := environmentCertIssuerPublicIngress(d.Object, lkeResolveCertIssuerTLSConfig(deploymentEndpointValues(f.cfg)).PublicHost, 9443, true); err != nil {
					t.Fatal(err)
				}
			}
			if d.Object["kind"] == "Service" {
				id := deploymentplan.ID(d.Object)
				if old := f.b.objects[id]; old != nil {
					// The generic MQTT donor historically uses9443. This cohort
					// selects the maintained native bootstrap Service18445 before
					// any Program/plan is frozen, preserving its original owner.
					o := deploymentplan.Clone(d.Object)
					m := deploymentplan.Map(o["metadata"])
					m["uid"], m["resourceVersion"] = deploymentplan.Map(old["metadata"])["uid"], "1"
					m["labels"] = deploymentplan.Object{"rtk.realtek.com/stack": f.p.Stack, "app.kubernetes.io/managed-by": "rtk-cloud"}
					f.b.objects[id] = o
					for i := range f.l.SourceDefinitions {
						if deploymentplan.ID(f.l.SourceDefinitions[i].Object) == id {
							f.l.SourceDefinitions[i] = deploymentplan.Definition{Object: deploymentplan.Clone(o), Phase: "configuration", Retain: true}
						}
					}
					continue
				}
			}
			add(d, true)
		}
	}
	// Preserve the original native CertIssuer controls, including its one-name
	// identity profile. The finite source later binds their genuine accepted UID.
	profile.RootSHA256 = f.l.Root.RootFingerprint
	profile.HelperImage = p.Release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference
	profile.RequestID = "post-cleanup-" + f.e.Journal.PlanSHA256[:32]
	profile.NewStatePath = filepath.Join(filepath.Dir(profile.StatePath), "public-"+environmentPreflightBytesSHA256([]byte(profile.RequestID))[:12], "server.json")
	profile.DNSNames = []string{profile.Name, lkeResolveCertIssuerTLSConfig(deploymentEndpointValues(f.cfg)).PublicHost}
	slices.Sort(profile.DNSNames)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(8881), Subject: pkix.Name{CommonName: profile.Name}, DNSNames: profile.OriginalDNSNames, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(6 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, leaf, f.ica, &key.PublicKey, f.icaKey)
	if err != nil {
		t.Fatal(err)
	}
	pub, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	state := mustProviderRecoveryJSON(deploymentplan.Object{"certificate": productPEM("CERTIFICATE", der), "original_private_key": productPEM("EC PRIVATE KEY", mustPostCleanupECKey(t, key))})
	profile.OriginalStateSHA256 = environmentPreflightBytesSHA256(state)
	profile.OriginalCertificateSHA256 = environmentPreflightBytesSHA256(der)
	profile.OriginalPublicKeySHA256 = environmentPreflightBytesSHA256(pub)
	_, logDefs, logProfile := logPVCWriterFixture(t)
	log := deploymentplan.Clone(logDefs[0].Object)
	app := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(log)["containers"])[0])
	app["image"] = p.Release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference
	// A real workload selector/labels are part of the maintained controller body.
	labels := deploymentplan.Object{"app.kubernetes.io/name": "video-cloud-logingester"}
	deploymentplan.Map(log["spec"])["selector"] = deploymentplan.Object{"matchLabels": deploymentplan.Clone(labels)}
	deploymentplan.Map(deploymentplan.Map(log["spec"])["template"])["metadata"] = deploymentplan.Object{"labels": deploymentplan.Clone(labels)}
	for _, raw := range certIssuerObjectList(app["env"]) {
		v := deploymentplan.Map(raw)
		if v["name"] == "VIDEO_CLOUD_ENV" {
			v["value"] = "dev"
		}
	}
	if mqttFoundationEnv(app, "VIDEO_CLOUD_ENV") == "" {
		app["env"] = append(certIssuerObjectList(app["env"]), deploymentplan.Object{"name": "VIDEO_CLOUD_ENV", "value": "dev"})
	}
	// Reuse the already selected original consumer's independent pins. A fresh
	// standalone fixture creates a different random CA, so its pins/public maps
	// cannot be merged into this existing cohort by resource ID.
	var core deploymentplan.Object
	for _, d := range f.l.SourceDefinitions {
		for _, raw := range certIssuerObjectList(environmentPKIPod(d.Object)["containers"]) {
			c := deploymentplan.Map(raw)
			if environmentCoreMQTTConsumer(d.Object, c) {
				core = c
			}
		}
	}
	if core == nil {
		t.Fatal("original core consumer with independent trust pins required")
	}
	mqttPin := mqttFoundationEnv(core, "VIDEO_CLOUD_MQTT_SERVER_PKI_ROOT_SHA256")
	servicePin := mqttFoundationEnv(core, "VIDEO_CLOUD_CONTROLLER_IDENTITY_ROOT_SHA256")
	app["env"] = mergeEnvironmentNamedFields(app["env"], []any{
		deploymentplan.Object{"name": "VIDEO_CLOUD_MQTT_SERVER_PKI_ROOT_SHA256", "value": mqttPin},
		deploymentplan.Object{"name": logMQTTIdentityPrefix + "ROOT_SHA256", "value": servicePin},
		deploymentplan.Object{"name": logMQTTIdentityPrefix + "SERVER_PKI_ROOT_SHA256", "value": servicePin},
		deploymentplan.Object{"name": logMQTTIdentityPrefix + "RENEWAL_SERVER_PKI_ROOT_SHA256", "value": servicePin},
	})
	for _, role := range []struct{ volume, domain, rootCM, rootKey, pin, statePath string }{
		{"mqtt-pki-crls", "mqtt", "mqtt-pki-bundles", "roots.pem", mqttPin, "/var/lib/mqtt-pki/crls/original.json"},
		{"service-crls", "service", "service-root", "root.pem", servicePin, "/var/lib/mqtt-pki/identity/service-crls/original.json"},
	} {
		rootSource := f.b.objects["ConfigMap/"+ns+"/"+role.rootCM]
		root := deploymentplan.String(deploymentplan.Map(rootSource["data"])[role.rootKey])
		if !coreMQTTRootMatches(root, role.pin) {
			t.Fatal("original selected Log transport trust", role.domain)
		}
		name := "post-cleanup-log-" + role.domain + "-crls"
		for _, raw := range certIssuerObjectList(environmentPKIPod(log)["volumes"]) {
			v := deploymentplan.Map(raw)
			if v["name"] == role.volume {
				deploymentplan.Map(v["configMap"])["name"] = name
			}
		}
		manifest := mustProviderRecoveryJSON([]any{deploymentplan.Object{"issuer": deploymentplan.Object{"issuer_id": "64062c4a-2bdc-474c-bb11-ab1b61026574", "environment": f.cfg.Environment, "trust_domain": role.domain, "status": "active", "chain_pem": root, "certificate_fingerprint_sha256": role.pin}, "state_path": role.statePath}})
		add(deploymentplan.Definition{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": name, "namespace": ns}, "data": deploymentplan.Object{"crls.json": string(manifest)}}}, true)
	}
	add(deploymentplan.Definition{Object: log, Phase: "workload"}, true)
	// Bind transport once against the complete selected Services before freezing
	// history. Standalone donors use9443; this native cohort selects18445. Keep
	// the original image-default Log writer group so the later typed correction
	// still exercises its genuine10001:999 permission defect.
	merged := make([]deploymentplan.Definition, len(f.l.SourceDefinitions))
	var oldLogSecurity any
	for i, d := range f.l.SourceDefinitions {
		merged[i] = d
		merged[i].Object = deploymentplan.Clone(d.Object)
		for _, raw := range certIssuerObjectList(environmentPKIPod(d.Object)["containers"]) {
			c := deploymentplan.Map(raw)
			if environmentLogMQTTConsumer(d.Object, c) {
				oldLogSecurity = c["securityContext"]
			}
		}
	}
	if err := deriveEnvironmentCoreMQTTTransport(nil, merged); err != nil {
		t.Fatal("merged native core transport before Program freeze", err)
	}
	if err := deriveEnvironmentLogMQTTTransport(map[string]string{"CLOUD_ENV_NAME": f.cfg.Environment}, merged); err != nil {
		t.Fatal("merged native log transport before Program freeze", err)
	}
	for i := range merged {
		for _, raw := range certIssuerObjectList(environmentPKIPod(merged[i].Object)["containers"]) {
			c := deploymentplan.Map(raw)
			if environmentLogMQTTConsumer(merged[i].Object, c) {
				if oldLogSecurity == nil {
					delete(c, "securityContext")
				} else {
					c["securityContext"] = oldLogSecurity
				}
			}
		}
		if deploymentplan.Digest(merged[i].Object) == deploymentplan.Digest(f.l.SourceDefinitions[i].Object) {
			continue
		}
		id := deploymentplan.ID(merged[i].Object)
		if actual := f.b.objects[id]; actual != nil {
			o := deploymentplan.Clone(merged[i].Object)
			m := deploymentplan.Map(o["metadata"])
			m["uid"], m["resourceVersion"] = deploymentplan.Map(actual["metadata"])["uid"], deploymentplan.Map(actual["metadata"])["resourceVersion"]
			f.b.objects[id] = o
		}
		f.l.SourceDefinitions[i] = merged[i]
	}
	if err := validateEnvironmentCoreMQTTTransport(f.l.SourceDefinitions); err != nil {
		t.Fatal("complete selected core transport", err)
	}
	if err := validateEnvironmentLogMQTTTransport(f.l.SourceDefinitions, f.cfg.Environment); err != nil {
		t.Fatal("complete selected log transport", err)
	}
	// The standalone maintained recipe helper owns a separate isolated store;
	// resume the outer fixture's explicit canonical root before qualification.
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", f.store.ConfigRoot)
	return &postCleanupCorrectionFixtureState{public: profile, log: logProfile, key: key, originalState: state, originalLeaf: der, controllerImage: "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:" + strings.Repeat("c", 64), failKnown: true}
}
func mustPostCleanupECKey(t *testing.T, key *ecdsa.PrivateKey) []byte {
	t.Helper()
	b, e := x509.MarshalECPrivateKey(key)
	if e != nil {
		t.Fatal(e)
	}
	return b
}

func TestPostCleanupCorrectionRecipesCompileMaintainedCatalog(t *testing.T) {
	cfg, defs, profile := certIssuerPublicCorrectionFixture(t)
	live := []deploymentplan.Object{}
	for i := range defs {
		o := deploymentplan.Clone(defs[i].Object)
		deploymentplan.Map(o["metadata"])["uid"] = fmt.Sprintf("%08d-aaaa-4aaa-8aaa-aaaaaaaaaaaa", i+1)
		deploymentplan.Map(o["metadata"])["resourceVersion"] = "1"
		labels := deploymentplan.Map(deploymentplan.Map(o["metadata"])["labels"])
		if labels == nil {
			labels = deploymentplan.Object{}
			deploymentplan.Map(o["metadata"])["labels"] = labels
		}
		labels["rtk.realtek.com/stack"] = profile.Stack
		live = append(live, o)
		deploymentplan.Map(defs[i].Object["metadata"])["uid"] = deploymentplan.Map(o["metadata"])["uid"]
	}
	prep, err := environmentPostCleanupCertIssuerPreparationDefinitions(cfg, defs, profile)
	if err != nil {
		t.Fatal(err)
	}
	in := deploymentplan.Inputs{Operation: "scoped", Environment: "dev", Stack: profile.Stack, ConfigurationSHA256: strings.Repeat("a", 64), ReleaseSHA256: strings.Repeat("b", 64), BindingsSHA256: strings.Repeat("c", 64)}
	p, err := deploymentplan.Compile(in, prep, live)
	if err != nil {
		t.Fatal("real preparation graph", err)
	}
	if len(p.Resources) != 3 || p.Resources[2].Desired["kind"] != "Job" {
		t.Fatal("native helper prerequisites/order")
	}
	changes, err := environmentPostCleanupCertIssuerPublicDefinitions(cfg, defs, profile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = deploymentplan.Compile(in, changes, live); err != nil {
		t.Fatal("real public correction graph", err)
	}
}

func TestPostCleanupCorrectionFixtureQualification(t *testing.T) {
	x := newProductExpiryE2EFixture(t, true, true, true)
	if x.postCleanup == nil {
		t.Fatal("missing maintained correction cohort")
	}
	x.qualifyInstalledPostCleanupFixture(t)
	t.Log("real original protocol and both entire installed correction recipes qualify before long lifecycle")
}

func TestProductPostCleanupCorrectionOuterCompletesOriginalAndPreservesAllSources(t *testing.T) {
	runProductSessionExpiryOuter(t, true, true, true)
}

type postCleanupKnownResponseBackend struct {
	*productOuterTargetBackend
	state  *postCleanupCorrectionFixtureState
	failID string
}

func (b *postCleanupKnownResponseBackend) Wait(ctx context.Context, r deploymentplan.Resource) error {
	if err := b.productOuterTargetBackend.Wait(ctx, r); err != nil {
		return err
	}
	if r.ID == b.failID && b.state.failKnown {
		b.state.failKnown = false
		return errors.New("known same-UID corrective rollout interruption")
	}
	return nil
}

func (x *productExpiryE2EFixture) exercisePostCleanupCorrection(t *testing.T, boundary deploymentplan.Backend) {
	t.Helper()
	f, s := x.f, x.postCleanup
	ctx := withCreateNormalTailHandoff(t.Context(), f.h)
	if s == nil {
		t.Fatal("missing pre-frozen correction cohort")
	}
	_, w, j, err := postCleanupCorrectionOriginal(f.store, f.e, nil)
	if err != nil {
		t.Fatal("genuine postcleanup original authority", err)
	}
	frozen := map[string][]byte{}
	if err = filepath.WalkDir(f.store.Root, func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if !d.IsDir() && (strings.Contains(path, "/rollback/") || strings.HasPrefix(d.Name(), "normal-tail-") || d.Name() == "source.json" || d.Name() == "completed.json") {
			b, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			frozen[path] = b
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	prefix := deploymentplan.Digest(f.e.Journal.Entries)
	ns := f.p.Stack + "-video-cloud"
	logID := "Deployment/" + ns + "/video-cloud-logingester"
	original := postCleanupCorrectionOriginalDefinitions(w, j)
	s.log.DeploymentUID = j.Owners[logID]
	s.log.ClaimUID = j.Owners["PersistentVolumeClaim/"+ns+"/"+s.log.ClaimName]
	pvc := f.b.objects["PersistentVolumeClaim/"+ns+"/"+s.log.ClaimName]
	s.log.PVName = deploymentplan.String(deploymentplan.Map(pvc["spec"])["volumeName"])
	pv := f.b.objects["PersistentVolume//"+s.log.PVName]
	s.log.PVUID = deploymentplan.String(deploymentplan.Map(pv["metadata"])["uid"])
	s.log.NativeID = deploymentplan.String(deploymentplan.Map(deploymentplan.Map(pv["spec"])["csi"])["volumeHandle"])
	if err = s.public.validate(f.cfg, original); err != nil {
		t.Fatal("pre-frozen public native profile", err)
	}
	if _, err = environmentPostCleanupLogIngesterDefinitions(f.cfg, original, s.log); err != nil {
		t.Fatal("pre-frozen native writer profile", err)
	}
	c := &compiledEnvironmentDeployment{Store: f.store, Configuration: f.cfg, Plan: w, Release: x.program.Release, Backend: f.runtime.Backend}
	verifyCalls := 0
	preparedCalls := 0
	qualifyCalls := 0
	verify := func(ctx context.Context, r *createNormalTailPostCleanupCorrection) error {
		if r == nil || !r.Journals["apply"].Accepted {
			return errors.New("full acceptance: historical public listener/SAN and native MQTT writer are not ready")
		}
		verifyCalls++
		app := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(f.b.objects[logID])["containers"])[0])
		sec := deploymentplan.Map(app["securityContext"])
		if sec["runAsUser"] != float64(10001) || sec["runAsGroup"] != float64(10001) {
			return errors.New("native atomic CRL writer identity is not corrected")
		}
		if err := validateEnvironmentCertIssuerPublicTransport(deploymentEndpointValues(f.cfg), objectsToPostCleanupDefinitions(f.b.objects)); err != nil {
			return err
		}
		return exercisePostCleanupPublicTLS(t, f, s)
	}
	if verify(ctx, nil) == nil {
		t.Fatal("missing genuine pre-correction functional failure")
	}
	ops := postCleanupCorrectionOperations{
		Qualify: func(context.Context, *createNormalTailProgram, *deploymentplan.Plan, deploymentplan.Journal) (*environmentCertIssuerPublicProfile, *environmentLogIngesterPVCWriterProfile, error) {
			qualifyCalls++
			return &s.public, &s.log, nil
		},
		Prepared: func(ctx context.Context, p environmentCertIssuerPublicProfile) (environmentCertIssuerPublicProof, error) {
			preparedCalls++
			leaf := &x509.Certificate{SerialNumber: big.NewInt(8882), Subject: pkix.Name{CommonName: p.Name}, DNSNames: p.DNSNames, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(6 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
			der, e := x509.CreateCertificate(rand.Reader, leaf, f.ica, &s.key.PublicKey, f.icaKey)
			if e != nil {
				return environmentCertIssuerPublicProof{}, e
			}
			s.newLeaf = der
			state := mustProviderRecoveryJSON(deploymentplan.Object{"certificate": productPEM("CERTIFICATE", der), "original_private_key": productPEM("EC PRIVATE KEY", mustPostCleanupECKey(t, s.key))})
			s.proof = environmentCertIssuerPublicProof{Schema: "rtk-cloud.certissuer-public-profile-proof/v1", IntentSHA256: deploymentplan.Digest(p), RequestID: p.RequestID, StatePath: p.NewStatePath, OriginalCertificateSHA256: p.OriginalCertificateSHA256, CertificateSHA256: environmentPreflightBytesSHA256(der), PublicKeySHA256: p.OriginalPublicKeySHA256, RootSHA256: p.RootSHA256, StateSHA256: environmentPreflightBytesSHA256(state), DNSNames: p.DNSNames}
			return s.proof, nil
		},
		Verify: verify,
	}
	ops.Apply = func(ctx context.Context, stage string, defs []deploymentplan.Definition) error {
		p := f.plans[stage]
		if p == nil {
			var live []deploymentplan.Object
			for _, o := range f.b.objects {
				live = append(live, o)
			}
			var e error
			p, e = deploymentplan.Compile(deploymentplan.Inputs{Operation: "scoped", Environment: "dev", Stack: f.p.Stack, ConfigurationSHA256: deploymentplan.Digest(f.cfg), ReleaseSHA256: deploymentplan.Digest(x.program.Release), BindingsSHA256: environmentCreateStageBindingsSHA(stage, defs), CreateParentSHA256: f.e.Journal.PlanSHA256, CreateHandoffSHA256: deploymentplan.Digest(f.h.Pins)}, defs, live)
			if e != nil {
				return e
			}
			if e = validatePostCleanupStageBeforeExecution(f.cfg, f.store, f.e, stage, p); e != nil {
				return e
			}
			f.plans[stage] = p
		}
		if e := saveCreateNormalTailStagePlan(ctx, f.store, f.e, stage, p); e != nil {
			return e
		}
		b := &postCleanupKnownResponseBackend{productOuterTargetBackend: &productOuterTargetBackend{productProtocolBackend: f.b}, state: s, failID: logID}
		check := func(ctx context.Context, _ *deploymentplan.Plan) error { return f.e.Check(ctx) }
		q := func(ctx context.Context, p *deploymentplan.Plan) error { return p.AdmitResumable(ctx, b, f.store.Root) }
		accept := func(ctx context.Context, p *deploymentplan.Plan) error { return p.Verify(ctx, b) }
		return executeCreateNormalTailStageBackend(ctx, f.store, f.h, stage, p, b, q, check, accept, io.Discard, func(context.Context) error { return nil })
	}
	err = ensureCreateNormalTailPostCleanupCorrectionWithOperations(ctx, f.cfg, f.h, c, s.public.HelperImage, s.controllerImage, ops)
	if err == nil || !strings.Contains(err.Error(), "known same-UID") {
		t.Fatal("known corrective write response interruption", err)
	}
	r, err := readCreateNormalTailPostCleanupCorrection(f.cfg, f.store, f.e)
	if err != nil || r == nil || r.Journals["apply"].Accepted || r.Journals["apply"].InFlight != "" {
		t.Fatal("truthful partial correction", err)
	}
	planSHA := r.Plans["apply"].SHA256()
	x.assertPostCleanupCorrectionRefusals(t, r)
	// Preserve writes that are already owned by the interrupted child. A later
	// routing phase can still be unwritten when its workload readiness fails.
	knownApplyOwners := map[string]string{}
	for id, uid := range r.Journals["apply"].Owners {
		knownApplyOwners[id] = uid
	}
	s.applyWrites = map[string]int{}
	for id, n := range f.b.writes {
		s.applyWrites[id] = n
	}
	if err = ensureCreateNormalTailPostCleanupCorrectionWithOperations(ctx, f.cfg, f.h, c, s.public.HelperImage, s.controllerImage, ops); err != nil {
		t.Fatal("same correction resume", err)
	}
	r, err = readCreateNormalTailPostCleanupCorrection(f.cfg, f.store, f.e)
	if err != nil || r.Completion == nil || r.Plans["apply"].SHA256() != planSHA {
		t.Fatal("actual accepted correction/completion", err)
	}
	assertPostCleanupCorrectionWriteCounts(t, r.Plans["apply"], knownApplyOwners, s.applyWrites, f.b.writes)
	if qualifyCalls != 1 || preparedCalls != 1 || verifyCalls < 2 {
		t.Fatal("correction requalified/reissued original profile", qualifyCalls, preparedCalls, verifyCalls)
	}
	if deploymentplan.Digest(f.e.Journal.Entries[:r.Source.PrefixEntries]) != prefix || !bytes.Equal(s.originalState, mustProviderRecoveryJSON(deploymentplan.Object{"certificate": productPEM("CERTIFICATE", s.originalLeaf), "original_private_key": productPEM("EC PRIVATE KEY", mustPostCleanupECKey(t, s.key))})) {
		t.Fatal("postcleanup correction changed historical evidence or private identity")
	}
	for path, raw := range frozen {
		now, e := os.ReadFile(path)
		if e != nil || !bytes.Equal(raw, now) {
			t.Fatal("correction changed prior immutable history", filepath.Base(path), e)
		}
	}
	if err = r.Plans["apply"].Verify(ctx, boundary); err != nil {
		t.Fatal("corrected full finite target", err)
	}
	t.Log("milestone: postcleanup failed health, ordinary same-UID repair interruption/resume, public mTLS and exact original history accepted")
}
func objectsToPostCleanupDefinitions(objects map[string]deploymentplan.Object) []deploymentplan.Definition {
	var out []deploymentplan.Definition
	for _, o := range objects {
		out = append(out, deploymentplan.Definition{Object: o})
	}
	return out
}
func exercisePostCleanupPublicTLS(t *testing.T, f *productProtocolFixture, s *postCleanupCorrectionFixtureState) error {
	t.Helper()
	leaf, e := x509.ParseCertificate(s.newLeaf)
	if e != nil {
		return e
	}
	if leaf.CheckSignatureFrom(f.ica) != nil || !slices.Equal(leaf.DNSNames, s.public.DNSNames) {
		return errors.New("wrong same-key native serving profile")
	}
	pub, _ := x509.MarshalPKIXPublicKey(leaf.PublicKey)
	if environmentPreflightBytesSHA256(pub) != s.public.OriginalPublicKeySHA256 {
		return errors.New("native serving key changed")
	}
	pair := tls.Certificate{Certificate: [][]byte{s.newLeaf, f.ica.Raw, f.root.Raw}, PrivateKey: s.key}
	roots := x509.NewCertPool()
	roots.AddCert(f.root)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	server.Config.ErrorLog = nil
	server.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	defer server.Close()
	clientPair, e := tls.X509KeyPair([]byte(f.bootstrap["bootstrap.crt"]), []byte(f.bootstrap["bootstrap.key"]))
	if e == nil && len(clientPair.Certificate) > 0 {
		return errors.New("erased bootstrap credential unexpectedly available")
	}
	// Use one original permanent client key, preserved by all renewal/cleanup.
	for _, raw := range f.states {
		var state struct {
			Current struct {
				Chain string `json:"certificate_chain_pem"`
				Key   string `json:"private_key_pem"`
			} `json:"current"`
		}
		if json.Unmarshal(raw, &state) == nil {
			clientPair, e = tls.X509KeyPair([]byte(state.Current.Chain), []byte(state.Current.Key))
			if e == nil {
				break
			}
		}
	}
	if e != nil {
		return e
	}
	call := func(auth bool) error {
		cfg := &tls.Config{RootCAs: roots, ServerName: lkeResolveCertIssuerTLSConfig(deploymentEndpointValues(f.cfg)).PublicHost, MinVersion: tls.VersionTLS12}
		if auth {
			cfg.Certificates = []tls.Certificate{clientPair}
		}
		tr := &http.Transport{TLSClientConfig: cfg}
		defer tr.CloseIdleConnections()
		resp, err := (&http.Client{Transport: tr, Timeout: time.Second}).Get(server.URL)
		if resp != nil {
			resp.Body.Close()
		}
		return err
	}
	if err := call(true); err != nil {
		return err
	}
	if call(false) == nil {
		return errors.New("anonymous public client accepted")
	}
	return nil
}

// These negatives alter only isolated saved input bytes and restore the exact
// bytes before the genuine ordinary retry. None may cause another native write.
func (x *productExpiryE2EFixture) assertPostCleanupCorrectionRefusals(t *testing.T, r *createNormalTailPostCleanupCorrection) {
	t.Helper()
	f := x.f
	before := deploymentplan.Digest(f.b.writes)
	sourcePath := postCleanupCorrectionPath(f.e, "source")
	original, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"missing-source-after-intent", "changed-source", "premature-completion", "unknown-child-response"} {
		t.Run(name, func(t *testing.T) {
			switch name {
			case "missing-source-after-intent":
				hidden := sourcePath + ".negative"
				if err := os.Rename(sourcePath, hidden); err != nil {
					t.Fatal(err)
				}
				defer os.Rename(hidden, sourcePath)
			case "changed-source":
				changed := r.Source
				changed.WorkloadPlanSHA256 = strings.Repeat("e", 64)
				if err := os.WriteFile(sourcePath, mustProviderRecoveryJSON(changed), 0600); err != nil {
					t.Fatal(err)
				}
				defer os.WriteFile(sourcePath, original, 0600)
			case "premature-completion":
				path := postCleanupCorrectionPath(f.e, "completed")
				if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
				defer os.Remove(path)
			case "unknown-child-response":
				path := filepath.Join(f.store.Root, "deployment/rollback", r.Plans["apply"].SHA256(), "journal.json")
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var j deploymentplan.Journal
				if json.Unmarshal(raw, &j) != nil {
					t.Fatal("saved child")
				}
				j.InFlight = r.Plans["apply"].Resources[len(r.Plans["apply"].Resources)-1].ID
				if err := os.WriteFile(path, mustProviderRecoveryJSON(j), 0600); err != nil {
					t.Fatal(err)
				}
				defer os.WriteFile(path, raw, 0600)
			}
			if _, err := readCreateNormalTailPostCleanupCorrection(f.cfg, f.store, f.e); err == nil {
				t.Fatal("foreign or unknown correction authority accepted")
			}
			if deploymentplan.Digest(f.b.writes) != before {
				t.Fatal("read-only refusal wrote native state")
			}
		})
	}
	if _, err := readCreateNormalTailPostCleanupCorrection(f.cfg, f.store, f.e); err != nil {
		t.Fatal("exact source restoration", err)
	}
}

func (x *productExpiryE2EFixture) qualifyInstalledPostCleanupFixture(t *testing.T) {
	t.Helper()
	f, s := x.f, x.postCleanup
	ns := f.p.Stack + "-video-cloud"
	// Every UID here is from the existing selected fixture inventory or frozen
	// plan, never a manufactured accepted response for a future child.
	j := deploymentplan.Journal{Owners: map[string]string{}}
	for _, r := range x.previous.Resources {
		if r.UID != "" {
			j.Owners[r.ID] = r.UID
		}
	}
	original := postCleanupCorrectionOriginalDefinitions(x.previous, j)
	if err := validateEnvironmentCoreMQTTTransport(original); err != nil {
		t.Fatal("entire frozen core transport", err)
	}
	if err := validateEnvironmentLogMQTTTransport(original, f.cfg.Environment); err != nil {
		t.Fatal("entire frozen log transport", err)
	}
	if err := checkEnvironmentLogMQTTTransport(t.Context(), x.previous, f.b.Read); err != nil {
		t.Fatal("entire frozen Log mounted trust/CRL owner and scope", err)
	}
	s.log.DeploymentUID = j.Owners["Deployment/"+ns+"/video-cloud-logingester"]
	s.log.ClaimUID = j.Owners["PersistentVolumeClaim/"+ns+"/"+s.log.ClaimName]
	claim := f.b.objects["PersistentVolumeClaim/"+ns+"/"+s.log.ClaimName]
	s.log.PVName = deploymentplan.String(deploymentplan.Map(claim["spec"])["volumeName"])
	pv := f.b.objects["PersistentVolume//"+s.log.PVName]
	s.log.PVUID = deploymentplan.String(deploymentplan.Map(pv["metadata"])["uid"])
	s.log.NativeID = deploymentplan.String(deploymentplan.Map(deploymentplan.Map(pv["spec"])["csi"])["volumeHandle"])
	if err := s.public.validate(f.cfg, original); err != nil {
		t.Fatal("entire selected native public profile", err)
	}
	if _, err := environmentPostCleanupLogIngesterDefinitions(f.cfg, original, s.log); err != nil {
		t.Fatal("entire selected managed log writer profile", err)
	}
	source := createNormalTailPostCleanupCorrectionSource{CertIssuerPublic: &s.public, LogIngester: &s.log, ControllerImage: s.controllerImage}
	var live []deploymentplan.Object
	for _, o := range f.b.objects {
		live = append(live, o)
	}
	for _, part := range []string{"prepare", "apply"} {
		defs, err := postCleanupCorrectionDefinitions(f.cfg, source, x.previous, j, part, nil, deploymentplan.Journal{})
		if err != nil {
			t.Fatal("entire finite installed recipe", part, err)
		}
		p, err := deploymentplan.Compile(deploymentplan.Inputs{Operation: "scoped", Environment: "dev", Stack: f.p.Stack, ConfigurationSHA256: deploymentplan.Digest(f.cfg), ReleaseSHA256: deploymentplan.Digest(x.program.Release), BindingsSHA256: environmentCreateStageBindingsSHA(createNormalTailPostCleanupStage+"-"+part, defs), CreateParentSHA256: f.e.Journal.PlanSHA256, CreateHandoffSHA256: deploymentplan.Digest(f.h.Pins)}, defs, live)
		if err != nil || p == nil {
			t.Fatal("all finite installed owners/kinds/phase dependencies", part, err)
		}
		if part == "apply" {
			all := certIssuerPublicOverlay(original, defs)
			if err := validateEnvironmentCertIssuerPublicTransport(deploymentEndpointValues(f.cfg), all); err != nil {
				t.Fatal("complete selected public Service/Ingress/DNS/policy graph", err)
			}
		}
	}
}

// Known native writes must never repeat; an unwritten suffix must execute once.
func assertPostCleanupCorrectionWriteCounts(t *testing.T, p *deploymentplan.Plan, known map[string]string, before, after map[string]int) {
	t.Helper()
	for _, resource := range p.Resources {
		want := before[resource.ID]
		if known[resource.ID] == "" && resource.Action != "retain" {
			want++
		}
		if after[resource.ID] != want {
			t.Fatal("corrective prefix/suffix write count", resource.ID, "known", known[resource.ID] != "", "before", before[resource.ID], "after", after[resource.ID], "want", want)
		}
	}
}

type postCleanupPhaseWaitBackend struct {
	*normalTailPhaseBackend
	failID string
	failed bool
}

func (b *postCleanupPhaseWaitBackend) Wait(ctx context.Context, r deploymentplan.Resource) error {
	if err := b.normalTailPhaseBackend.Wait(ctx, r); err != nil {
		return err
	}
	if r.ID == b.failID && !b.failed {
		b.failed = true
		return errors.New("known corrective workload barrier interruption")
	}
	return nil
}

func TestPostCleanupCorrectionRecordedPrefixAndUnwrittenRoutingExecuteOnce(t *testing.T) {
	f := newNormalTailPhaseFixture(t, false)
	cfg, original, profile := certIssuerPublicCorrectionFixture(t)
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", f.store.ConfigRoot)
	var live []deploymentplan.Object
	for i := range original {
		o := deploymentplan.Clone(original[i].Object)
		uid := fmt.Sprintf("%08d-aaaa-4aaa-8aaa-aaaaaaaaaaaa", i+100)
		deploymentplan.Map(o["metadata"])["uid"] = uid
		deploymentplan.Map(o["metadata"])["resourceVersion"] = "1"
		labels := deploymentplan.Map(deploymentplan.Map(o["metadata"])["labels"])
		if labels == nil {
			labels = deploymentplan.Object{}
			deploymentplan.Map(o["metadata"])["labels"] = labels
		}
		labels["rtk.realtek.com/stack"], labels["app.kubernetes.io/managed-by"] = cfg.Values["CLOUD_STACK_NAME"], "rtk-cloud"
		original[i].Object = o
		live = append(live, o)
		f.b.objects[deploymentplan.ID(o)] = deploymentplan.Clone(o)
	}
	defs, err := environmentPostCleanupCertIssuerPublicDefinitions(cfg, original, profile)
	if err != nil {
		t.Fatal("maintained finite recipe", err)
	}
	p, err := deploymentplan.Compile(deploymentplan.Inputs{Operation: "scoped", Environment: "dev", Stack: cfg.Values["CLOUD_STACK_NAME"], ConfigurationSHA256: f.p.ConfigurationSHA256, ReleaseSHA256: f.p.ReleaseSHA256, BindingsSHA256: environmentCreateStageBindingsSHA("finite-prefix-test", defs), CreateParentSHA256: f.h.Execution.Journal.PlanSHA256, CreateHandoffSHA256: deploymentplan.Digest(f.h.Pins)}, defs, live)
	if err != nil {
		t.Fatal("real finite phase plan", err)
	}
	deploymentID := "Deployment/" + profile.Stack + "-video-cloud/certissuer"
	ingressID := "Ingress/" + profile.Stack + "-video-cloud/certissuer-public-mtls"
	b := &postCleanupPhaseWaitBackend{normalTailPhaseBackend: f.b, failID: deploymentID}
	check := func(ctx context.Context, _ *deploymentplan.Plan) error { return f.h.Execution.Check(ctx) }
	qualify := func(ctx context.Context, p *deploymentplan.Plan) error { return p.AdmitResumable(ctx, b, f.store.Root) }
	run := func() error {
		return executeCreateNormalTailStageBackend(t.Context(), f.store, f.h, "finite-prefix-test", p, b, qualify, check, check, io.Discard, func(context.Context) error { return nil })
	}
	if err := run(); err == nil || !strings.Contains(err.Error(), "known corrective") {
		t.Fatal("missing genuine known workload barrier", err)
	}
	partial, exists, err := p.ReadExecution(f.store.Root)
	if err != nil || !exists || partial.Accepted || partial.InFlight != "" || partial.Owners[deploymentID] == "" || partial.Owners[ingressID] != "" || f.b.writes[deploymentID] != 1 || f.b.writes[ingressID] != 0 {
		t.Fatal("recorded workload prefix/unwritten routing proof", err)
	}
	before := map[string]int{}
	for id, n := range f.b.writes {
		before[id] = n
	}
	if err := run(); err != nil {
		t.Fatal("same plan ordinary retry", err)
	}
	after, exists, err := p.ReadExecution(f.store.Root)
	if err != nil || !exists || !after.Accepted || after.InFlight != "" || after.Owners[deploymentID] != partial.Owners[deploymentID] || after.Owners[ingressID] == "" {
		t.Fatal("accepted same owners and new routing response", err)
	}
	assertPostCleanupCorrectionWriteCounts(t, p, partial.Owners, before, f.b.writes)
	if f.b.writes[deploymentID] != 1 || f.b.writes[ingressID] != 1 {
		t.Fatal("known prefix repeated or unwritten suffix skipped")
	}
	before = map[string]int{}
	for id, n := range f.b.writes {
		before[id] = n
	}
	if err := run(); err != nil || deploymentplan.Digest(before) != deploymentplan.Digest(f.b.writes) {
		t.Fatal("accepted retry repeated a write", err)
	}
}
