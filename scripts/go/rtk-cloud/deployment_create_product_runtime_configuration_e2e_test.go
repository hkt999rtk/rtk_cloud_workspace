package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func installProductRuntimeBillingInputs(t *testing.T, f *productProtocolFixture) {
	t.Helper()
	for key, value := range map[string]string{"PAYPAL_ENABLED": "true", "NEWEBPAY_ENABLED": "false", "PAYMENT_SIMULATOR_ENABLED": "false", "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL": "operator@example.test", "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD": "isolated-original-password", "GHCR_PULL_USERNAME": "isolated-publisher", "GHCR_PULL_TOKEN": "isolated-registry-token", "LINODE_MEDIA_OBJ_ACCESS_KEY_ID": "isolated-media-access", "LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY": "isolated-media-secret", "LINODE_OTA_OBJ_ACCESS_KEY_ID": "isolated-ota-access", "LINODE_OTA_OBJ_SECRET_ACCESS_KEY": "isolated-ota-secret"} {
		if err := f.store.write(filepath.Join("operator/env", key), []byte(value), true); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.store.write("runtime/payment-reference-encryption", []byte(strings.Repeat("s", 32)), true); err != nil {
		t.Fatal(err)
	}
}

func installProductRuntimeBillingDefinitions(t *testing.T, f *productProtocolFixture, p *createNormalTailProgram, snapshot environmentDeploymentSnapshot, add func(deploymentplan.Definition, bool)) {
	t.Helper()
	ns := f.p.Stack + "-billing"
	for _, role := range []string{"LKE_ZAMMAD_ELASTICSEARCH_IMAGE", "LKE_ZAMMAD_POSTGRES_IMAGE"} {
		p.Release.Images[role] = environmentReleaseImage{Reference: f.cfg.Values[environmentProviderImageConfigKey(role)]}
		if p.Release.Images[role].Reference == "" {
			t.Fatal("missing maintained support image role", role)
		}
	}
	add(deploymentplan.Definition{Phase: "namespace", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "Namespace", "metadata": deploymentplan.Object{"name": ns}}}, true)
	defs := billingConfigurationOriginal(t, f.cfg, f.store, p.Release, snapshot)
	stsIDs := []string{"StatefulSet/" + f.p.Stack + "-support/zammad-elasticsearch-master", "StatefulSet/" + f.p.Stack + "-support/zammad-postgres"}
	for _, d := range defs {
		d.Phase = environmentResourcePhase(deploymentplan.String(d.Object["kind"]))
		if deploymentplan.ID(d.Object) == "Deployment/"+ns+"/billing" {
			// This synthetic historical source pins the old immediate-Wait
			// Billing failure before its successors. Use an explicit immediate
			// phase before Program freeze; current workload-block deferral is
			// exercised by the other controllers and the ordinary engine suite.
			d.Phase = "configuration"
			d.Dependencies = append(d.Dependencies, stsIDs...)
		}
		add(d, false)
	}
	for _, suffix := range []string{"support", "logger"} {
		add(deploymentplan.Definition{Phase: "namespace", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "Namespace", "metadata": deploymentplan.Object{"name": f.p.Stack + "-" + suffix}}}, true)
	}
	pull, err := newK8SDockerConfigSecretObject(ns, "ghcr-pull", "isolated-original-publisher", "isolated-original-registry-token")
	if err != nil {
		t.Fatal(err)
	}
	add(deploymentplan.Definition{Phase: "configuration", Object: pull}, true)
	for _, name := range []string{"cloud-logger-billing-inbox", "video-cloud-mqttusage-checkpoint", "video-cloud-prometheus-data"} {
		claimNS := f.p.Stack + "-video-cloud"
		if name == "cloud-logger-billing-inbox" {
			claimNS = f.p.Stack + "-logger"
		}
		if name == "video-cloud-prometheus-data" {
			claimNS = f.p.Stack + "-observability"
		}
		add(environmentCreateServiceClaimDefinition(f.cfg, claimNS, name), false)
	}
	for _, name := range []string{"zammad-elasticsearch-master", "zammad-postgres", "fleet-valkey"} {
		o := plannedEnvelopeStatefulSet(name, 1, "data")
		workloadNS, containerName, role := f.p.Stack+"-support", "postgres", "LKE_ZAMMAD_POSTGRES_IMAGE"
		serviceName := "zammad-postgres-headless"
		labels := deploymentplan.Object{"app.kubernetes.io/name": "postgres", "app.kubernetes.io/instance": "zammad"}
		if name == "zammad-elasticsearch-master" {
			containerName, role = "elasticsearch", "LKE_ZAMMAD_ELASTICSEARCH_IMAGE"
			serviceName = "zammad-elasticsearch-master-hl"
			labels = deploymentplan.Object{"app.kubernetes.io/name": "elasticsearch", "app.kubernetes.io/instance": "zammad", "app.kubernetes.io/component": "master"}
		}
		if name == "fleet-valkey" {
			workloadNS, containerName, role = f.p.Stack+"-platform", "valkey", "LKE_FLEET_VALKEY_IMAGE"
			serviceName, labels = "fleet-valkey", deploymentplan.Object{"app.kubernetes.io/name": "fleet-valkey"}
		}
		deploymentplan.Map(o["metadata"])["namespace"] = workloadNS
		spec := deploymentplan.Map(o["spec"])
		spec["serviceName"], spec["selector"] = serviceName, deploymentplan.Object{"matchLabels": deploymentplan.Clone(labels)}
		deploymentplan.Map(deploymentplan.Map(spec["template"])["metadata"])["labels"] = deploymentplan.Clone(labels)
		container := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(o)["containers"])[0])
		container["name"], container["image"] = containerName, p.Release.Images[role].Reference
		if err := validateZammadInventoryImages(o, p.Release); workloadNS == f.p.Stack+"-support" && err != nil {
			t.Fatal("maintained support fixture image cohort", err)
		}
		// The dependency graph keeps both support allocations before Billing and
		// leaves Fleet genuinely unwritten at the known Billing response.
		d := deploymentplan.Definition{Phase: "configuration", Object: o}
		if name == "fleet-valkey" {
			d.Phase = "workload"
			d.Dependencies = []string{"Deployment/" + ns + "/billing"}
		}
		add(d, false)
		port := float64(5432)
		if name == "zammad-elasticsearch-master" {
			port = 9200
		}
		if name == "fleet-valkey" {
			port = 6379
		}
		add(deploymentplan.Definition{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "Service", "metadata": deploymentplan.Object{"name": serviceName, "namespace": workloadNS}, "spec": deploymentplan.Object{"clusterIP": "None", "selector": deploymentplan.Clone(labels), "ports": []any{deploymentplan.Object{"name": "native", "port": port, "targetPort": port}}}}}, false)
	}
}

// Model only API allocation metadata: the definitions remain the original
// compiled recipes. Native bodies and journal owners are still read through
// the ordinary production boundary and shared storage checker.
func installProductRuntimeNativeVolumes(t *testing.T, f *productProtocolFixture) {
	t.Helper()
	for _, o := range f.b.objects {
		if o["kind"] != "StatefulSet" {
			continue
		}
		meta, spec := deploymentplan.Map(o["metadata"]), deploymentplan.Map(o["spec"])
		name, ns, uid := deploymentplan.String(meta["name"]), deploymentplan.String(meta["namespace"]), deploymentplan.String(meta["uid"])
		for _, raw := range certIssuerObjectList(spec["volumeClaimTemplates"]) {
			template := deploymentplan.Map(raw)
			prefix := deploymentplan.String(deploymentplan.Map(template["metadata"])["name"])
			replicas, _ := strconv.Atoi(fmt.Sprint(spec["replicas"]))
			for ordinal := 0; ordinal < replicas; ordinal++ {
				claimName := fmt.Sprintf("%s-%s-%d", prefix, name, ordinal)
				id := "PersistentVolumeClaim/" + ns + "/" + claimName
				if f.b.objects[id] == nil {
					claim := deploymentplan.Clone(template)
					claim["apiVersion"], claim["kind"] = "v1", "PersistentVolumeClaim"
					m := deploymentplan.Map(claim["metadata"])
					m["name"], m["namespace"], m["uid"], m["resourceVersion"] = claimName, ns, "12121212-aaaa-4aaa-8aaa-"+deploymentplan.Digest(id)[:12], "1"
					f.b.objects[id] = claim
				}
				podName := fmt.Sprintf("%s-%d", name, ordinal)
				podID := "Pod/" + ns + "/" + podName
				if f.b.objects[podID] == nil {
					pod := deploymentplan.Clone(deploymentplan.Map(spec["template"]))
					pod["apiVersion"], pod["kind"] = "v1", "Pod"
					m := deploymentplan.Map(pod["metadata"])
					m["name"], m["namespace"], m["uid"] = podName, ns, "13131313-aaaa-4aaa-8aaa-"+deploymentplan.Digest(podID)[:12]
					m["ownerReferences"] = []any{deploymentplan.Object{"apiVersion": "apps/v1", "kind": "StatefulSet", "name": name, "uid": uid, "controller": true}}
					s := deploymentplan.Map(pod["spec"])
					s["volumes"] = append(certIssuerObjectList(s["volumes"]), deploymentplan.Object{"name": prefix, "persistentVolumeClaim": deploymentplan.Object{"claimName": claimName}})
					f.b.objects[podID] = pod
				}
			}
		}
	}
	for id, o := range f.b.objects {
		if o["kind"] != "PersistentVolumeClaim" {
			continue
		}
		m, spec := deploymentplan.Map(o["metadata"]), deploymentplan.Map(o["spec"])
		name := "pv-" + deploymentplan.Digest(id)[:12]
		native, _ := strconv.ParseUint(deploymentplan.Digest(id)[:10], 16, 64)
		spec["volumeName"] = name
		o["status"] = deploymentplan.Object{"phase": "Bound"}
		f.b.objects["PersistentVolume//"+name] = deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolume", "metadata": deploymentplan.Object{"name": name, "uid": "14141414-aaaa-4aaa-8aaa-" + deploymentplan.Digest(name)[:12]}, "spec": deploymentplan.Object{"claimRef": deploymentplan.Object{"namespace": m["namespace"], "name": m["name"], "uid": m["uid"]}, "storageClassName": spec["storageClassName"], "persistentVolumeReclaimPolicy": "Retain", "csi": deploymentplan.Object{"driver": "linodebs.csi.linode.com", "volumeHandle": fmt.Sprintf("%d-pvc-original", native)}}, "status": deploymentplan.Object{"phase": "Bound"}}
	}
}

func captureProductRuntimeVolumes(t *testing.T, f *productProtocolFixture) {
	t.Helper()
	// This is the real capture API, reading the fake native inventory. It pins
	// current PVC/PV identities rather than deriving allocation from desired names.
	if err := captureEnvironmentCreateVolumeOwners(t.Context(), f.cfg, f.runtime.Backend, f.e); err != nil {
		t.Fatal("capture current native volume allocations", err)
	}
	ledger, err := readEnvironmentCreateLedger(f.e)
	if err != nil {
		t.Fatal(err)
	}
	for id, o := range f.b.objects {
		if uid := deploymentplan.String(deploymentplan.Map(o["metadata"])["uid"]); uid != "" {
			ledger.Kubernetes[id] = uid
		}
	}
	if err := saveEnvironmentCreateLedger(t.Context(), f.e, ledger); err != nil {
		t.Fatal(err)
	}
}

type productRuntimeBillingBackend struct {
	*productOuterTargetBackend
	t             *testing.T
	f             *productProtocolFixture
	failed        bool
	runtimeStops  []string
	afterComplete func(deploymentplan.Resource)
}

func (b *productRuntimeBillingBackend) Write(ctx context.Context, action string, o deploymentplan.Object, dry bool) (deploymentplan.Object, error) {
	out, err := b.productOuterTargetBackend.Write(ctx, action, o, dry)
	if err == nil && !dry {
		installProductRuntimeNativeVolumes(b.t, b.f)
	}
	return out, err
}
func (b *productRuntimeBillingBackend) ExecutionResourceProgress(_ context.Context, _, _ int, r deploymentplan.Resource, step string) {
	if step == "complete" && b.afterComplete != nil {
		b.afterComplete(r)
	}
}

func (b *productRuntimeBillingBackend) Wait(ctx context.Context, r deploymentplan.Resource) error {
	if len(b.runtimeStops) > 0 && r.ID == b.runtimeStops[0] {
		b.runtimeStops = b.runtimeStops[1:]
		return errors.New("known runtime update response before readiness completion")
	}
	if r.ID == "Deployment/"+b.f.p.Stack+"-billing/billing" && !b.failed {
		b.failed = true
		return errors.New("known Billing startup configuration failure after native owner response")
	}
	return b.productOuterTargetBackend.Wait(ctx, r)
}

func executeProductRuntimeBillingRecovery(t *testing.T, x *productExpiryE2EFixture, c *compiledEnvironmentDeployment, prior *createNormalTailProductSessionForward) *compiledEnvironmentDeployment {
	t.Helper()
	f := x.f
	ctx := withCreateNormalTailHandoff(t.Context(), f.h)
	boundary := &productRuntimeBillingBackend{productOuterTargetBackend: &productOuterTargetBackend{productProtocolBackend: f.b}, t: t, f: f}
	qualify := func(ctx context.Context, p *deploymentplan.Plan) error {
		return p.AdmitResumable(ctx, boundary, f.store.Root)
	}
	phase := func(ctx context.Context, p *deploymentplan.Plan) (map[string]deploymentplan.Object, error) {
		started := time.Now()
		observed, err := c.FenceInventory(ctx, p)
		if x.secondRenewal {
			t.Logf("phase timing: full runtime input fence duration=%s observations=%d failed=%t", time.Since(started), len(observed), err != nil)
		}
		return observed, err
	}
	fence := func(ctx context.Context, p *deploymentplan.Plan) error { _, err := phase(ctx, p); return err }
	accept := func(ctx context.Context, p *deploymentplan.Plan) error { return p.Verify(ctx, boundary) }
	if err := executeCreateNormalTailProductSessionForwardBackend(ctx, f.cfg, f.store, f.h, c, prior, boundary, qualify, fence, accept, io.Discard, func(context.Context) error { return nil }, phase); err == nil {
		t.Fatal("missing known partial Billing interruption")
	}
	installProductRuntimeNativeVolumes(t, f)
	captureProductRuntimeVolumes(t, f)
	literal, exists, err := c.Plan.ReadExecution(f.store.Root)
	if err != nil || !exists || literal.Accepted || literal.InFlight != "" || literal.Failed != "Deployment/"+f.p.Stack+"-billing/billing" || literal.Owners[literal.Failed] == "" {
		t.Fatal("genuine partial renewed workload", err)
	}
	source, err := captureCreateNormalTailRuntimeSource(ctx, f.cfg, f.store, f.e)
	if err != nil || source == nil {
		t.Fatal("actual pre-change runtime source capture", err)
	}
	for _, key := range []string{"PAYPAL_ENABLED", "NEWEBPAY_ENABLED", "PAYMENT_SIMULATOR_ENABLED"} {
		if err := f.store.write(filepath.Join("operator/env", key), []byte("false"), true); err != nil {
			t.Fatal(err)
		}
	}
	inventory := func(context.Context, string) ([]deploymentplan.Object, error) {
		var rows []deploymentplan.Object
		for _, o := range f.b.objects {
			rows = append(rows, o)
		}
		return rows, nil
	}
	c, err = prepareCreateNormalTailRuntimeForwardWithInventory(ctx, f.cfg, f.h, c, inventory, environmentBillingRuntimeLegacyProfile)
	if err != nil {
		t.Fatal("actual runtime forward prepare", err)
	}
	d, err := readCreateNormalTailRuntimeForward(f.store, f.e)
	if err != nil || d == nil {
		t.Fatal("authenticated runtime descriptor", err)
	}
	if err := checkEnvironmentManagedMQTTConsumers(ctx, c.Plan, f.b.Read); err != nil {
		t.Fatal("runtime target original MQTT placement", err)
	}
	if _, err := c.FenceInventory(ctx, c.Plan); err != nil {
		t.Fatal("complete runtime successor input fence", err)
	}
	// Each UPDATE is a genuine same-UID known response. Retry resumes the
	// ordinary journal without issuing a second Secret or Deployment update.
	secretID := "Secret/" + f.p.Stack + "-billing/billing-runtime"
	billingID := "Deployment/" + f.p.Stack + "-billing/billing"
	beforeSecret, beforeBilling := f.b.writes[secretID], f.b.writes[billingID]
	// Preserve a real legacy0002 write, then stop in a non-Billing phase fence.
	boundary.runtimeStops = []string{secretID}
	if err := executeCreateNormalTailRuntimeForwardBackend(ctx, f.cfg, f.store, f.h, c, d, boundary, qualify, fence, accept, io.Discard, func(context.Context) error { return nil }, phase); err == nil {
		t.Fatal("missing legacy known Secret response")
	}
	// Stop through the real progress/context boundary immediately after the
	// original Secret effect is durably completed. The synthetic historical
	// Billing phase is immediate, so waiting for the next phase would also
	// update Billing and would no longer model actual0002's earlier stop.
	cancelled := false
	partialCtx, cancelPartial := context.WithCancel(ctx)
	boundary.afterComplete = func(r deploymentplan.Resource) {
		if r.ID == secretID {
			cancelled = true
			cancelPartial()
		}
	}
	partialErr := executeCreateNormalTailRuntimeForwardBackend(partialCtx, f.cfg, f.store, f.h, c, d, boundary, qualify, fence, accept, io.Discard, func(context.Context) error { return nil }, phase)
	boundary.afterComplete = nil
	cancelPartial()
	if partialErr == nil || !errors.Is(partialErr, context.Canceled) {
		t.Fatal("missing non-Billing native-effect cancellation", partialErr)
	}
	legacyPlan := c.Plan
	legacyJournal, exists, err := legacyPlan.ReadExecution(f.store.Root)
	if err != nil || !exists || !cancelled || legacyJournal.Failed == billingID || !createNormalTailRuntimePartial(legacyPlan, legacyJournal) {
		t.Fatal("genuine legacy known partial state", err)
	}
	legacyRawPath := filepath.Join(f.store.Root, "deployment/rollback", legacyPlan.SHA256(), "journal.json")
	legacyRaw, err := os.ReadFile(legacyRawPath)
	if err != nil {
		t.Fatal(err)
	}
	legacyDescriptorPath := createNormalTailRuntimeForwardPath(f.e)
	legacyDescriptorRaw, err := os.ReadFile(legacyDescriptorPath)
	if err != nil {
		t.Fatal(err)
	}
	x.assertCurrentPrefix(t)
	c, err = prepareCreateNormalTailRuntimeForwardWithInventory(ctx, f.cfg, f.h, c, inventory)
	if err != nil {
		t.Fatal("actual numbered encoding successor prepare", err)
	}
	d, err = readCreateNormalTailRuntimeForward(f.store, f.e)
	if err != nil || d == nil || d.Sequence != 3 || d.Profile != environmentBillingRuntimeKeyProfile {
		t.Fatal("exact numbered correction profile", err)
	}
	if _, err := c.FenceInventory(ctx, c.Plan); err != nil {
		t.Fatal("full numbered successor input fence", err)
	}
	for _, id := range []string{secretID, billingID} {

		boundary.runtimeStops = []string{id}
		if err := executeCreateNormalTailRuntimeForwardBackend(ctx, f.cfg, f.store, f.h, c, d, boundary, qualify, fence, accept, io.Discard, func(context.Context) error { return nil }, phase); err == nil {
			t.Fatal("missing numbered known UPDATE response", id)
		}
		journal, exists, err := c.Plan.ReadExecution(f.store.Root)
		if err != nil || !exists || journal.InFlight != "" || journal.Failed != id || journal.Owners[id] != literal.Owners[id] {
			t.Fatal("numbered UPDATE original UID changed", id, err)
		}
		x.assertCurrentPrefix(t)
	}
	// A transient phase-fence failure after the corrected Secret must
	// resume this same immutable0003 child through the ordinary fence.
	// The scaled graph selects its actual workload boundary by phase;
	// the actual329-resource read-only probe covers its260 prefix.
	planSHA, descriptorSHA := c.Plan.SHA256(), deploymentplan.Digest(d)
	var productOwners []string
	if x.secondRenewal {
		outputs := map[string]environmentCreateProductIdentity{}
		for _, output := range f.l.Outputs {
			outputs[output.ID] = environmentCreateProductIdentity{ID: output.ID, Subject: output.Subject}
		}
		seen := map[string]int{}
		for _, resource := range c.Plan.Resources {
			local := map[string]int{}
			if err := resolveCreateNormalTailProductACK(deploymentplan.Clone(resource.Desired), outputs, local, x.installed.Ceremony, true); err != nil {
				t.Fatal("exact frozen product consumer selection", err)
			}
			if len(local) != 0 {
				productOwners = append(productOwners, resource.ID)
			}
			for id, n := range local {
				seen[id] += n
			}
		}
		if len(productOwners) != 5 || len(seen) != 5 {
			t.Fatal("late partial requires exact five maintained consumers", len(productOwners), len(seen))
		}
		for _, n := range seen {
			if n != 1 {
				t.Fatal("ambiguous original product consumer")
			}
		}
	}
	stopped, failedID := false, ""
	transientFence := func(ctx context.Context, p *deploymentplan.Plan) (map[string]deploymentplan.Object, error) {
		observed, err := phase(ctx, p)
		if err != nil {
			return nil, err
		}
		journal, exists, err := p.ReadExecution(f.store.Root)
		if err != nil || !exists || len(journal.Completed) >= len(p.Resources) {
			return nil, errors.New("missing genuine partial phase boundary")
		}
		next := p.Resources[len(journal.Completed)]
		shouldStop := next.Phase == "workload"
		if x.secondRenewal {
			// Match the actual late partial boundary: all five native product
			// controllers have genuine completed owners before the next block.
			shouldStop = next.Phase != "workload"
			for _, id := range productOwners {
				if journal.Owners[id] == "" {
					shouldStop = false
				}
			}
		}
		if !stopped && shouldStop {
			stopped, failedID = true, next.ID
			return nil, errors.New("isolated transient workload phase fence")
		}
		return observed, nil
	}
	if err := executeCreateNormalTailRuntimeForwardBackend(ctx, f.cfg, f.store, f.h, c, d, boundary, qualify, fence, accept, io.Discard, func(context.Context) error { return nil }, transientFence); err == nil {
		t.Fatal("missing numbered workload phase-fence interruption")
	}
	journal, exists, err := c.Plan.ReadExecution(f.store.Root)
	if err != nil || !exists || !stopped || journal.Failed != failedID || journal.InFlight != "" || journal.Accepted || journal.AcceptanceStarted || len(journal.Owners) != len(journal.Completed) || f.b.held {
		t.Fatal("workload phase-fence failure lost its exact known prefix or lease cleanup", err)
	}
	if f.b.writes[secretID] != beforeSecret+2 || f.b.writes[billingID] != beforeBilling+1 || c.Plan.SHA256() != planSHA || deploymentplan.Digest(d) != descriptorSHA {
		t.Fatal("workload phase-fence stop changed target or repeated prior native effects")
	}
	selected, err := readCreateNormalTailRuntimeForward(f.store, f.e)
	if err != nil || selected == nil || selected.Sequence != 3 || selected.Target.Plan.SHA256() != planSHA {
		t.Fatal("transient phase-fence failure selected another numbered target", err)
	}
	for _, path := range []string{createNormalTailRuntimeSequenceSourcePath(f.e, 4), createNormalTailRuntimeDescriptorPath(f.e, 4)} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatal("same-target retry published another successor")
		}
	}
	x.assertCurrentPrefix(t)
	t.Logf("milestone: genuine0003 workload-phase fence stop at known prefix%d, no in-flight effect, lease released, same target retained", len(journal.Completed))

	var encoded string
	for _, resource := range c.Plan.Resources {
		if resource.ID == secretID {
			outer, err := base64.StdEncoding.DecodeString(deploymentplan.String(deploymentplan.Map(resource.Desired["data"])["PAYMENT_REFERENCE_ENCRYPTION_KEY"]))
			if err != nil {
				t.Fatal(err)
			}
			encoded = string(outer)
		}
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	expectedKey := sha256.Sum256([]byte(strings.Repeat("s", 32)))
	if err != nil || string(key) != string(expectedKey[:]) {
		t.Fatal("exact original seed-derived native AES32 projection")
	}
	thirdRawPath := filepath.Join(f.store.Root, "deployment/rollback", c.Plan.SHA256(), "journal.json")
	thirdRaw, err := os.ReadFile(thirdRawPath)
	if err != nil {
		t.Fatal(err)
	}
	if x.secondRenewal {
		c = executeProductRuntimeRegistrationRecovery(t, x, c, inventory)
		d, err = readCreateNormalTailRuntimeForward(f.store, f.e)
		if err != nil || d == nil || d.Sequence != 4 || d.SessionRenewalSHA256 == "" || d.RegistrationCRLSHA256 == "" {
			t.Fatal("exact fourth target renewal and CRL proof", err)
		}
	}
	// A known accepted child can be retried before logical aliases complete.
	capture := func(context.Context) error {
		captureProductRuntimeVolumes(t, f)
		return errors.New("accepted runtime child before logical parent completion")
	}
	t.Logf("milestone: executing numbered target %d through accepted-child interruption", createNormalTailRuntimeSequence(d))
	if err := executeCreateNormalTailRuntimeForwardBackend(ctx, f.cfg, f.store, f.h, c, d, boundary, qualify, fence, accept, io.Discard, capture, phase); err == nil {
		t.Fatal("missing accepted runtime interruption")
	}
	x.assertCurrentPrefix(t)
	if x.secondRenewal {
		x.resumeLinkedCRLsInFreshProcess(t, c)
	}
	t.Log("milestone: numbered target accepted; resuming the same child to complete logical aliases")
	writes := deploymentplan.Digest(f.b.writes)
	if err := executeCreateNormalTailRuntimeForwardBackend(ctx, f.cfg, f.store, f.h, c, d, boundary, qualify, fence, accept, io.Discard, func(context.Context) error { return nil }, phase); err != nil {
		t.Fatal("actual runtime same-child resume", err)
	}
	if f.b.writes[secretID] != beforeSecret+2 || f.b.writes[billingID] != beforeBilling+1 {
		t.Fatal("known runtime UPDATE was repeated or recreated")
	}
	if deploymentplan.Digest(f.b.writes) != writes {
		t.Fatal("accepted runtime retry repeated native effects")
	}
	if raw, err := os.ReadFile(legacyRawPath); err != nil || string(raw) != string(legacyRaw) {
		t.Fatal("literal legacy0002 journal changed", err)
	}
	if raw, err := os.ReadFile(legacyDescriptorPath); err != nil || string(raw) != string(legacyDescriptorRaw) {
		t.Fatal("literal legacy0002 descriptor changed", err)
	}
	after, exists, err := prior.Target.Plan.ReadExecution(f.store.Root)
	if err != nil || !exists || deploymentplan.Digest(after) != deploymentplan.Digest(literal) || after.Accepted {
		t.Fatal("literal failed renewed workload changed", err)
	}
	stages, err := readCreateNormalTailWorkloadSources(f.store, f.e, c.Plan)
	expectedSources := 4
	if x.secondRenewal {
		expectedSources = 5
	}
	if err != nil || len(stages) != expectedSources {
		t.Fatal("truthful workload allocation sources", len(stages), err)
	}
	if x.secondRenewal {
		if raw, er := os.ReadFile(thirdRawPath); er != nil || string(raw) != string(thirdRaw) {
			t.Fatal("literal stopped0003 journal changed", er)
		}
	}
	ledger, err := readEnvironmentCreateLedger(f.e)
	if err != nil {
		t.Fatal(err)
	}
	index, claims, err := managedLateVolumeStageClaims(stages)
	if err != nil {
		t.Fatal(err)
	}
	proved := 0
	for _, volume := range ledger.Volumes {
		id := "PersistentVolumeClaim/" + volume.Namespace + "/" + volume.PVCName
		n, ok := index[id]
		if !ok {
			continue
		}
		if _, err := observeManagedLatePlannedVolume(ctx, f.runtime.Backend, volume, claims[n][id], stages[n].Plan, stages[n].Journal); err != nil {
			t.Fatal("current four-source allocation proof", id, err)
		}
		proved++
	}
	if proved != 6 {
		t.Fatal("old direct, intermediate support and final Fleet allocation proof missing", proved)
	}
	// Allocation-specific owner/spec negatives are exercised by the focused
	// three-source tests; this path proves the genuine descriptors select them.
	if len(ledger.Volumes) == 0 {
		t.Fatal("native allocation ledger missing")
	}
	x.assertCurrentPrefix(t)
	t.Log("milestone: genuine Billing partial response, captured input correction, full phase-fenced runtime child and truthful four-source aliases")
	return c
}

// This runs the real public-source producer, ordinary Secret child, second
// renewal and numbered target builder. Kubernetes, signing and registry calls
// remain the existing isolated native boundary; the separate native AM test
// proves the actual public five-chain CRL failure and correction.
func executeProductRuntimeRegistrationRecovery(t *testing.T, x *productExpiryE2EFixture, c *compiledEnvironmentDeployment, inventory func(context.Context, string) ([]deploymentplan.Object, error)) *compiledEnvironmentDeployment {
	t.Helper()
	f := x.f
	ctx := withCreateNormalTailHandoff(t.Context(), f.h)
	installProductRuntimeNativeVolumes(t, f)
	captureProductRuntimeVolumes(t, f)
	id := createNormalTailAccountRegistrationCRLID(f.p.Stack)
	old := deploymentplan.Clone(f.b.objects[id])
	if old == nil {
		t.Fatal("prequalified original AM registration Secret missing")
	}
	check := func(c context.Context, _ *deploymentplan.Plan) error { return f.e.Check(c) }
	apply := func(ctx context.Context, defs []deploymentplan.Definition) error {
		stage := createNormalTailAccountRegistrationCRLStage
		p, err := readCreateNormalTailPreparationPlan(f.store, f.e, stage)
		if err != nil {
			return err
		}
		if p == nil {
			live, err := inventory(ctx, f.p.Stack)
			if err != nil {
				return err
			}
			p, err = deploymentplan.Compile(deploymentplan.Inputs{Operation: "scoped", Environment: f.cfg.Environment, Stack: f.p.Stack, ConfigurationSHA256: x.program.MaintainedConfigSHA256, ReleaseSHA256: deploymentplan.Digest(x.program.Release), BindingsSHA256: environmentCreateStageBindingsSHA(stage, defs), CreateParentSHA256: f.e.Journal.PlanSHA256, CreateHandoffSHA256: x.program.HandoffSHA256}, defs, live)
			if err != nil {
				return err
			}
		}
		if err = saveCreateNormalTailStagePlan(ctx, f.store, f.e, stage, p); err != nil {
			return err
		}
		return executeCreateNormalTailStageBackend(ctx, f.store, f.h, stage, p, f.b, check, check, check, io.Discard, func(context.Context) error { return nil })
	}
	fetch := func(_ context.Context, issuer environmentCreateRegisteredIssuer) (string, error) {
		cert, err := parseFirstCertificatePEM([]byte(issuer.Certificate))
		if err != nil {
			return "", err
		}
		key := f.icaKey
		if issuer.ID == f.l.Root.ID {
			key = f.rootKey
		}
		now := time.Now().UTC().Truncate(time.Second)
		number, next := int64(7), now.Add(time.Hour)
		if issuer.ID == f.l.Root.ID {
			// Sign a genuinely short historical Root2. Its source is current at
			// capture and then expires naturally; no saved time or CRL is edited.
			number, next = 2, now.Add(90*time.Second)
		}
		der, err := x509.CreateRevocationList(rand.Reader, &x509.RevocationList{Number: big.NewInt(number), ThisUpdate: now.Add(-time.Minute), NextUpdate: next}, cert, key)
		return productPEM("X509 CRL", der), err
	}
	if err := ensureCreateNormalTailAccountRegistrationCRLsWithApply(ctx, f.cfg, f.h, c, f.b.Read, fetch, apply, func(context.Context) error { return nil }); err != nil {
		t.Fatal("genuine early CRL source and standard child", err)
	}
	x.publishCurrentRootCRLSuccessor(t, c)
	x.assertCurrentPrefix(t)
	current := f.b.objects[id]
	if deploymentplan.Map(current["metadata"])["uid"] != deploymentplan.Map(old["metadata"])["uid"] || f.b.writes[id] != 1 {
		t.Fatal("AM original owner replaced or native update repeated")
	}
	for _, key := range []string{"tls.crt", "tls.key", "client-ca.crt"} {
		if deploymentplan.Map(current["data"])[key] != deploymentplan.Map(old["data"])[key] {
			t.Fatal("AM original server credential or trust changed", key)
		}
	}
	first, err := readProductSessionRenewalFirst(f.store, f.e, f.l)
	if err != nil || first == nil {
		t.Fatal("first immutable completed renewal", err)
	}
	var before = map[string][]byte{}
	for _, name := range []string{"source.json", "observed.json", "completed.json"} {
		path := filepath.Join(f.store.Root, productSessionRenewalBase(f.l), name)
		raw, er := os.ReadFile(path)
		if er != nil {
			t.Fatal(er)
		}
		before[path] = raw
	}
	if wait := time.Until(f.expires.Add(time.Millisecond)); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	priorEntries := append([]environmentCreateJournalEntry(nil), f.e.Journal.Entries...)
	priorJournal := f.e.Journal
	priorJournal.Entries = priorEntries
	priorJournal.KubernetesLeases = append(priorJournal.KubernetesLeases[:0:0], f.e.Journal.KubernetesLeases...)
	second, err := executeProductSessionRenewal(ctx, f.cfg, f.store, f.runtime, f.l, x.apply)
	if err != nil || second == nil || second.Source.Generation != 2 || x.renewals != 2 || second.Source.PreviousRenewalSHA256 != deploymentplan.Digest(first) {
		t.Fatal("genuine second expired-session renewal", err)
	}
	if second.Installed.EnrollmentSessionID != first.Installed.EnrollmentSessionID || second.Installed.Ceremony.BootstrapSessionID == first.Installed.Ceremony.BootstrapSessionID {
		t.Fatal("new ACK session changed original placement")
	}
	for path, raw := range before {
		after, er := os.ReadFile(path)
		if er != nil || string(raw) != string(after) {
			t.Fatal("first renewal bytes changed", er)
		}
	}
	if string(x.sourceKey) != string(x.files["/run/pki-bootstrap/bootstrap.key"]) || string(x.sourceCSR) != string(x.files["/run/pki-bootstrap/bootstrap.csr"]) {
		t.Fatal("second renewal regenerated original key or CSR")
	}
	for subject, raw := range x.certificates {
		if string(raw) != string(f.states[subject]) {
			t.Fatal("second renewal reissued permanent identity", subject)
		}
	}
	x.assertCurrentPrefix(t)
	prefix := *f.e
	prefix.Journal = priorJournal
	if n, _, er := validateCreateNormalTailProductStagesWithConfig(f.cfg, f.store, &prefix, f.l, priorEntries[x.start:]); er != nil || n != len(priorEntries)-x.start {
		t.Fatal("future second-renewal artifacts changed genuine earlier prefix", n, er)
	}
	next, err := prepareCreateNormalTailRuntimeForwardWithInventory(ctx, f.cfg, f.h, c, inventory)
	if err != nil {
		t.Fatal("actual fourth target with two renewal and early CRL proofs", err)
	}
	if err := checkEnvironmentManagedMQTTConsumers(ctx, next.Plan, f.b.Read); err != nil {
		t.Fatal("fourth target original MQTT placement", err)
	}
	if _, err := next.FenceInventory(ctx, next.Plan); err != nil {
		t.Fatal("complete fourth target input fence", err)
	}
	t.Log("milestone: exact early AM CRL child, naturally expired first renewal, same-key second renewal and full fenced0004")
	return next
}
