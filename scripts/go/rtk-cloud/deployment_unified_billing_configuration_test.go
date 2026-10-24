package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func billingConfigurationFixture(t *testing.T, environment string) (deploymentConfig, secretStore, environmentRelease, environmentDeploymentSnapshot) {
	t.Helper()
	cfg, store, release, snapshot := runtimeDependencyFixture(t, environment)
	delete(cfg.Values, "PAYPAL_ENABLED")
	cfg.Values["TEST_LAB_ENABLED"] = "true"
	for id := range snapshot.Secrets {
		sum := sha256.Sum256([]byte(id))
		snapshot.Secrets[id] = hex.EncodeToString(sum[:])
	}
	snapshot.Secrets["payment-reference-encryption"] = base64.StdEncoding.EncodeToString(make([]byte, 32))
	snapshot.Operator = appendMap(snapshot.Operator, map[string]string{"PAYMENT_SIMULATOR_ENABLED": "false", "NEWEBPAY_ENABLED": "false", "PAYPAL_ENABLED": "false", "PAYPAL_ENVIRONMENT": "sandbox"})
	return cfg, store, release, snapshot
}
func billingRuntimeData(t *testing.T, defs []deploymentplan.Definition) map[string]string {
	t.Helper()
	for _, d := range defs {
		if d.Object["kind"] == "Secret" && deploymentplan.Map(d.Object["metadata"])["name"] == "billing-runtime" {
			data := map[string]string{}
			for k, v := range deploymentplan.Map(d.Object["data"]) {
				raw, e := base64.StdEncoding.DecodeString(deploymentplan.String(v))
				if e != nil {
					t.Fatal(e)
				}
				data[k] = string(raw)
			}
			return data
		}
	}
	t.Fatal("rendered Billing runtime missing")
	return nil
}
func billingConfigurationOriginal(t *testing.T, cfg deploymentConfig, store secretStore, release environmentRelease, snapshot environmentDeploymentSnapshot) []deploymentplan.Definition {
	t.Helper()
	var defs []deploymentplan.Definition
	err := withEnvironmentRenderInputs(cfg, store, release, snapshot, func(env map[string]string) error {
		var billing lkeWorkload
		for _, w := range k8sWorkloads(env) {
			if w.Key == "billing" {
				billing = w
			}
		}
		var err error
		defs, err = decodeEnvironmentCreateManifests(lkeBillingSecretManifest(env), lkeDeploymentManifest(env, billing, nil), lkeBillingPaymentWorkerManifest(env), lkePaymentSimulatorDeploymentManifest(env))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return defs
}
func TestBillingConfigurationUsesSelectedOperatorAndClearsAmbient(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			cfg, store, release, snapshot := billingConfigurationFixture(t, environment)
			t.Setenv("PAYPAL_ENABLED", "true")
			t.Setenv("PAYPAL_ENVIRONMENT", "foreign")
			t.Setenv("PAYMENT_SIMULATOR_ENABLED", "true")
			t.Setenv("NEWEBPAY_ENABLED", "true")
			t.Setenv("BILLING_DOMAIN", "host-leak.invalid")
			t.Setenv("PAYMENT_SIMULATOR_DOMAIN", "host-leak.invalid")
			produced, err := renderRuntimeDependencyFixture(t, cfg, store, release, snapshot)
			if err != nil {
				t.Fatal(err)
			}
			data := billingRuntimeData(t, produced)
			for _, key := range []string{"PAYPAL_ENABLED", "PAYMENT_SIMULATOR_ENABLED", "NEWEBPAY_ENABLED", "PAYMENT_WORKER_ENABLED"} {
				if data[key] != "false" {
					t.Fatal("explicit selected false lost", key)
				}
			}
			if data["PAYPAL_ENVIRONMENT"] != "sandbox" || strings.Contains(data["PAYPAL_RETURN_URL"], "host-leak") || strings.Contains(data["PAYMENT_SIMULATOR_PUBLIC_BASE_URL"], "host-leak") {
				t.Fatal("ambient payment input escaped scope")
			}
			original := billingConfigurationOriginal(t, cfg, store, release, snapshot)
			before := deploymentplan.Digest(original)
			corrected, err := environmentBillingRuntimeCorrectionDefinitions(cfg, store, release, snapshot, original)
			if err != nil {
				t.Fatal(err)
			}
			if deploymentplan.Digest(original) != before || len(corrected) != 4 {
				t.Fatal("finite correction mutated literal source or lost consumer")
			}
			for _, d := range corrected {
				name := deploymentplan.Map(d.Object["metadata"])["name"]
				if name == "billing-payment-worker" || name == "payment-simulator" {
					if deploymentplan.Map(d.Object["spec"])["replicas"] != float64(0) {
						t.Fatal("providerless worker is not quiescent", name)
					}
				}
			}
			again, err := environmentBillingRuntimeCorrectionDefinitions(cfg, store, release, snapshot, corrected)
			if err != nil || deploymentplan.Digest(again) != deploymentplan.Digest(corrected) {
				t.Fatal("finite corrected profile is not idempotent", err)
			}
		})
	}
}
func TestBillingConfigurationEnabledPayPalRequiresAllCanonicalInputs(t *testing.T) {
	for _, mode := range []string{"all-missing", "paypal-client-id", "paypal-client-secret", "paypal-webhook-id", "invalid-mode", "dev-production"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, release, snapshot := billingConfigurationFixture(t, "dev")
			snapshot.Operator["PAYPAL_ENABLED"] = "true"
			for _, key := range []string{"paypal-client-id", "paypal-client-secret", "paypal-webhook-id"} {
				snapshot.Secrets[key] = "synthetic-distinct-" + key
			}
			if mode == "all-missing" {
				for _, key := range []string{"paypal-client-id", "paypal-client-secret", "paypal-webhook-id"} {
					delete(snapshot.Secrets, key)
				}
			} else if strings.HasPrefix(mode, "paypal-") {
				delete(snapshot.Secrets, mode)
			} else if mode == "invalid-mode" {
				snapshot.Operator["PAYPAL_ENVIRONMENT"] = "foreign"
			} else {
				snapshot.Operator["PAYPAL_ENVIRONMENT"] = "production"
			}
			defs, err := renderRuntimeDependencyFixture(t, cfg, store, release, snapshot)
			var blocker *deploymentplan.Blocker
			if !errors.As(err, &blocker) || blocker.ID != "deployment.runtime.dependencies" || len(defs) != 0 {
				t.Fatal("enabled missing/invalid canonical PayPal escaped before render", err)
			}
		})
	}
}

const billingNativeConfigProbe = `package main
import (
 "context"
 "crypto/ed25519"
 "encoding/json"
 "os"
 "testing"
 "github.com/jackc/pgx/v5/pgxpool"
 "github.com/hkt999rtk/rtk_billing/internal/accessstore"
 "github.com/hkt999rtk/rtk_billing/internal/api"
 "github.com/hkt999rtk/rtk_billing/internal/auditstore"
 "github.com/hkt999rtk/rtk_billing/internal/billingidentity"
 "github.com/hkt999rtk/rtk_billing/internal/billingservice"
 "github.com/hkt999rtk/rtk_billing/internal/billingstore"
 "github.com/hkt999rtk/rtk_billing/internal/config"
 "github.com/hkt999rtk/rtk_billing/internal/otagrant"
 "github.com/hkt999rtk/rtk_billing/internal/payment"
 "github.com/hkt999rtk/rtk_billing/internal/paymentcrypto"
 "github.com/hkt999rtk/rtk_billing/internal/paymentprovider/newebpay"
 "github.com/hkt999rtk/rtk_billing/internal/paymentprovider/paypal"
 paymentSimulator "github.com/hkt999rtk/rtk_billing/internal/paymentprovider/simulator"
 "github.com/hkt999rtk/rtk_billing/internal/paymentstore"
 "github.com/hkt999rtk/rtk_billing/internal/rawretention"
)
func TestDeploymentRenderedBillingAPIConfiguration(t *testing.T) {
 cases:=[]struct{Name string;Data map[string]string}{{Name:"actual"}}
 if path:=os.Getenv("RTK_RENDERED_BILLING_PROBE");path!="" {raw,e:=os.ReadFile(path);if e!=nil||json.Unmarshal(raw,&cases)!=nil{t.Fatal("invalid synthetic rendered cases")}}
 keys:=map[string]bool{};for _,c:=range cases{for k:=range c.Data{keys[k]=true}}
 for _,c:=range cases{t.Run(c.Name,func(t *testing.T){
  for k:=range keys{t.Setenv(k,"")};for k,v:=range c.Data{t.Setenv(k,v)}
  cfg,e:=config.Load();if e!=nil{t.Fatal("config.Load:",e)}
  protector,e:=paymentcrypto.New(cfg.PaymentReferenceEncryptionKey)
  if os.Getenv("RTK_EXPECT_BILLING_CRYPTO_REJECT")=="1" {if e==nil{t.Fatal("invalid original input unexpectedly accepted")};t.Log("original crypto format rejected");return}
  if e!=nil{t.Fatal("paymentcrypto.New:",e)}
  ciphertext,e:=protector.EncryptMethodReference("isolated-synthetic-reference");if e!=nil{t.Fatal("reference encryption failed")};reference,e:=protector.ResolveMethodReference(context.Background(),ciphertext);if e!=nil||reference!="isolated-synthetic-reference"{t.Fatal("reference encryption round trip failed")}
  // A nonnil disconnected pool proves constructor validation without opening a
  // database connection or making any provider, payment or HTTP request.
  db:=&pgxpool.Pool{}
  server,e:=api.New(api.Options{ServiceToken:cfg.ServiceToken,InternalToken:cfg.InternalToken,Audit:api.AuditAdapter{Store:auditstore.New(db)},Access:accessstore.New(db),Ownership:billingidentity.New(db)});if e!=nil{t.Fatal("api.New:",e)}
  paymentStore:=paymentstore.New(db);providers:=[]payment.PaymentProvider{}
  if cfg.SimulatorEnabled{p,e:=paymentSimulator.New(paymentSimulator.Config{BaseURL:cfg.SimulatorBaseURL,SharedSecret:cfg.SimulatorSharedSecret,RunID:cfg.SimulatorRunID,Scenario:cfg.SimulatorScenario,Timeout:cfg.RequestTimeout});if e!=nil{t.Fatal("simulator.New:",e)};providers=append(providers,p)}
  if cfg.NewebPayEnabled{p,e:=newebpay.New(newebpay.Config{Enabled:true,Environment:cfg.NewebPayEnvironment,MerchantID:cfg.NewebPayMerchantID,HashKey:cfg.NewebPayHashKey,HashIV:cfg.NewebPayHashIV,EndpointBaseURL:cfg.NewebPayEndpointBaseURL,Timeout:cfg.RequestTimeout});if e!=nil{t.Fatal("newebpay.New:",e)};providers=append(providers,p)}
  if cfg.PayPalEnabled{p,e:=paypal.New(paypal.Config{Environment:cfg.PayPalEnvironment,ClientID:cfg.PayPalClientID,ClientSecret:cfg.PayPalClientSecret,WebhookID:cfg.PayPalWebhookID,ReturnURL:cfg.PayPalReturnURL,CancelURL:cfg.PayPalCancelURL});if e!=nil{t.Fatal("paypal.New:",e)};providers=append(providers,p)}
  if e:=server.ConfigurePayments(api.PaymentAPIOptions{Store:paymentStore,Providers:providers,ReferenceProtector:protector,BillingDebitToken:cfg.BillingDebitToken,BillingDebitSource:cfg.BillingDebitSource,SimulatorCallbackSecret:cfg.SimulatorCallbackSecret,HostedChargeNotifyURL:cfg.NewebPayNotifyURL,HostedChargeReturnURL:cfg.NewebPayReturnURL,PayPalAfterReturnURL:cfg.PayPalAfterReturnURL});e!=nil{t.Fatal("ConfigurePayments:",e)}
  if cfg.HandoffToken!=""{if e:=server.ConfigureHandoff(api.HandoffAPIOptions{Token:cfg.HandoffToken,Store:paymentStore});e!=nil{t.Fatal("ConfigureHandoff:",e)}}
  if cfg.CloudCreationToken!=""{if e:=server.ConfigureCloudCreation(api.CloudCreationAPIOptions{Token:cfg.CloudCreationToken,Store:paymentStore});e!=nil{t.Fatal("ConfigureCloudCreation:",e)}}
  billingStore:=billingstore.New(db)
  if cfg.OTAGrantHistoryBaseURL!=""{v,e:=otagrant.NewClient(cfg.OTAGrantHistoryBaseURL,cfg.OTAGrantHistoryToken,nil);if e!=nil{t.Fatal("otagrant.NewClient:",e)};billingStore.SetOTAGrantVerifier(v);billingStore.SetOTATierVerifier(v)}
  if cfg.OTAPlatformSealToken!=""{if e:=server.ConfigureOTAPeriodSeals(api.OTAPeriodSealAPIOptions{PlatformToken:cfg.OTAPlatformSealToken,ProducerToken:cfg.OTAProducerSealToken,Store:billingStore});e!=nil{t.Fatal("ConfigureOTAPeriodSeals:",e)}}
  service,e:=billingservice.New(billingservice.Options{Store:billingStore,PaymentStore:paymentStore});if e!=nil{t.Fatal("billingservice.New:",e)}
  if e:=server.ConfigureBilling(api.BillingAPIOptions{Store:billingStore,Service:service});e!=nil{t.Fatal("ConfigureBilling:",e)}
  if cfg.RawRetention.Enabled{r:=cfg.RawRetention;evidence:=&rawretention.EvidenceClient{Consumers:map[string]rawretention.Consumer{r.ConsumerID:{ID:r.ConsumerID,BaseURL:r.ConsumerBaseURL,Token:r.ConsumerToken}},LoggerBaseURL:r.LoggerBaseURL,LoggerToken:r.LoggerToken};authority,e:=rawretention.New(db,evidence,map[string]ed25519.PublicKey{r.VerifierKeyID:r.VerifierPublicKey},r.Environment);if e!=nil{t.Fatal("rawretention.New:",e)};if e:=server.ConfigureRawRetention(api.RawRetentionAPIOptions{FinancialToken:r.FinancialToken,RecoveryToken:r.RecoveryToken,ControllerToken:r.ControllerToken,AuthorityReadToken:r.AuthorityReadToken,Store:authority});e!=nil{t.Fatal("ConfigureRawRetention:",e)}}
  t.Log("all selected API startup constructors and crypto round trip passed without network")
 })}
}
`
const billingNativeWorkerProbe = `package main
import("encoding/json";"os";"testing";"github.com/hkt999rtk/rtk_billing/internal/config";"github.com/hkt999rtk/rtk_billing/internal/paymentcrypto")
func TestDeploymentRenderedBillingWorkerConfiguration(t *testing.T){cases:=[]struct{Name string;Data map[string]string}{{Name:"actual"}};if path:=os.Getenv("RTK_RENDERED_BILLING_PROBE");path!=""{raw,e:=os.ReadFile(path);if e!=nil||json.Unmarshal(raw,&cases)!=nil{t.Fatal("invalid synthetic rendered inputs")}};keys:=map[string]bool{};for _,c:=range cases{for k:=range c.Data{keys[k]=true}};for _,c:=range cases{t.Run(c.Name,func(t *testing.T){for k:=range keys{t.Setenv(k,"")};for k,v:=range c.Data{t.Setenv(k,v)};cfg,e:=config.Load();if e!=nil{t.Fatal("worker config.Load:",e)};providers,_,e:=buildPaymentProviders(cfg);if e!=nil{t.Fatal("buildPaymentProviders:",e)};enabled:=truthy(os.Getenv("PAYMENT_WORKER_ENABLED"));if enabled!=(len(providers)>0){t.Fatal("worker activation differs from actual native providers")};_,e=paymentcrypto.New(cfg.PaymentReferenceEncryptionKey);if os.Getenv("RTK_EXPECT_BILLING_CRYPTO_REJECT")=="1"{if e==nil{t.Fatal("invalid original crypto input accepted")};t.Log("original crypto format rejected");return};if e!=nil{t.Fatal("paymentcrypto.New:",e)};t.Log("actual worker provider construction passed without network")})}}
`

func TestBillingConfigurationRenderedInputsPassExactNativeAPIAndWorkerStartup(t *testing.T) {
	var cases []struct {
		Name string
		Data map[string]string
	}
	for _, mode := range []string{"disabled", "paypal", "simulator-newebpay", "all-enabled"} {
		cfg, store, release, snapshot := billingConfigurationFixture(t, "dev")
		if mode == "paypal" || mode == "all-enabled" {
			snapshot.Operator["PAYPAL_ENABLED"] = "true"
			for _, key := range []string{"paypal-client-id", "paypal-client-secret", "paypal-webhook-id"} {
				snapshot.Secrets[key] = "synthetic-distinct-" + key
			}
		}
		if mode == "simulator-newebpay" || mode == "all-enabled" {
			snapshot.Operator["PAYMENT_SIMULATOR_ENABLED"] = "true"
			snapshot.Operator["NEWEBPAY_ENABLED"] = "true"
		}
		defs, err := renderRuntimeDependencyFixture(t, cfg, store, release, snapshot)
		if err != nil {
			t.Fatal(err)
		}
		cases = append(cases, struct {
			Name string
			Data map[string]string
		}{mode, billingRuntimeData(t, defs)})
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(cases)
	input := filepath.Join(dir, "rendered.json")
	if err := os.WriteFile(input, raw, 0600); err != nil {
		t.Fatal(err)
	}
	native := filepath.Join(mustWorkspaceRoot(t), "repos", "rtk_billing")
	configProbe := filepath.Join(dir, "config_probe_test.go")
	workerProbe := filepath.Join(dir, "worker_probe_test.go")
	os.WriteFile(configProbe, []byte(billingNativeConfigProbe), 0600)
	os.WriteFile(workerProbe, []byte(billingNativeWorkerProbe), 0600)
	overlay := map[string]any{"Replace": map[string]string{filepath.Join(native, "cmd", "server", "deployment_rendered_probe_test.go"): configProbe, filepath.Join(native, "cmd", "payment-worker", "deployment_rendered_probe_test.go"): workerProbe}}
	for from, to := range overlay["Replace"].(map[string]string) {
		if real, e := filepath.EvalSymlinks(filepath.Dir(from)); e == nil {
			overlay["Replace"].(map[string]string)[filepath.Join(real, filepath.Base(from))] = to
		}
	}
	raw, _ = json.Marshal(overlay)
	overlayPath := filepath.Join(dir, "overlay.json")
	os.WriteFile(overlayPath, raw, 0600)
	cmd := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "test", "-json", "-overlay", overlayPath, "./cmd/server", "./cmd/payment-worker", "-run", "^TestDeploymentRenderedBilling", "-count=1", "-timeout=90s")
	cmd.Dir = native
	for _, setting := range os.Environ() {
		key, _, _ := strings.Cut(setting, "=")
		if strings.HasPrefix(key, "BILLING_") || strings.HasPrefix(key, "PAYMENT_") || strings.HasPrefix(key, "PAYPAL_") || strings.HasPrefix(key, "NEWEBPAY_") || key == "ENVIRONMENT" || key == "DATABASE_URL" || key == "PORT" || key == "GOWORK" || key == "GOFLAGS" || key == "GOTOOLCHAIN" {
			continue
		}
		cmd.Env = append(cmd.Env, setting)
	}
	cmd.Env = append(cmd.Env, "GOWORK=off", "GOTOOLCHAIN=local", "RTK_RENDERED_BILLING_PROBE="+input)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("exact native config.Load/crypto/provider/API startup failed: %s", output)
	}
	requireBillingNativeRuns(t, output, []string{"TestDeploymentRenderedBillingAPIConfiguration", "TestDeploymentRenderedBillingWorkerConfiguration"}, []string{"disabled", "paypal", "simulator-newebpay", "all-enabled"})
}
func TestBillingConfigurationProviderlessHealthStillRequiresBillingAPI(t *testing.T) {
	items := []any{}
	for _, name := range []string{"billing", "billing-payment-worker", "payment-simulator"} {
		replicas := 0
		status := map[string]any{"observedGeneration": 1}
		if name == "billing" {
			replicas = 1
			status = map[string]any{"observedGeneration": 1, "updatedReplicas": 1, "availableReplicas": 1, "readyReplicas": 1}
		}
		items = append(items, map[string]any{"kind": "Deployment", "metadata": map[string]any{"name": name, "namespace": "video-cloud-dev-billing", "generation": 1}, "spec": map[string]any{"replicas": replicas}, "status": status})
	}
	items = append(items, map[string]any{"kind": "Pod", "metadata": map[string]any{"name": "billing-owned", "namespace": "video-cloud-dev-billing"}, "status": map[string]any{"phase": "Running", "conditions": []any{map[string]any{"type": "Ready", "status": "True"}}, "containerStatuses": []any{map[string]any{"name": "app", "ready": true, "state": map[string]any{"running": map[string]any{}}}}}})
	raw, _ := json.Marshal(map[string]any{"items": items})
	if err := validateDeploymentWorkloadHealth("dev", raw); err != nil {
		t.Fatal("quiescent unused providers blocked Billing health", err)
	}
	items[0].(map[string]any)["status"] = map[string]any{"observedGeneration": 1, "unavailableReplicas": 1}
	raw, _ = json.Marshal(map[string]any{"items": items})
	if err := validateDeploymentWorkloadHealth("dev", raw); err == nil {
		t.Fatal("BillingAPI health was skipped with disabled providers")
	}
}

// Exit status alone cannot prove an overlay-added test was discovered through
// a symlinked workspace. Require explicit run AND pass for every exact case.
func requireBillingNativeRuns(t *testing.T, output []byte, tests, cases []string) {
	t.Helper()
	if !billingNativeRunsPassed(output, tests, cases) {
		t.Fatal("native startup tests require explicit run and pass for every exact test/subcase")
	}
}
func billingNativeRunsPassed(output []byte, tests, cases []string) bool {
	seen := map[string]map[string]bool{}
	decoder := json.NewDecoder(bytes.NewReader(output))
	for {
		var event struct{ Action, Test string }
		err := decoder.Decode(&event)
		if err == io.EOF {
			break
		}
		if err != nil {
			return false
		}
		if event.Test != "" {
			if seen[event.Test] == nil {
				seen[event.Test] = map[string]bool{}
			}
			seen[event.Test][event.Action] = true
		}
	}
	for _, name := range tests {
		for _, suffix := range append([]string{""}, cases...) {
			key := name
			if suffix != "" {
				key += "/" + suffix
			}
			if !seen[key]["run"] || !seen[key]["pass"] {
				return false
			}
		}
	}
	return true
}
func TestBillingConfigurationNativeProbeRequiresRunAndPass(t *testing.T) {
	good := []byte("{\"Action\":\"run\",\"Test\":\"TestNative\"}\n{\"Action\":\"run\",\"Test\":\"TestNative/case\"}\n{\"Action\":\"pass\",\"Test\":\"TestNative/case\"}\n{\"Action\":\"pass\",\"Test\":\"TestNative\"}\n")
	if !billingNativeRunsPassed(good, []string{"TestNative"}, []string{"case"}) {
		t.Fatal("genuine test proof rejected")
	}
	for _, output := range [][]byte{nil, []byte(`{"Action":"pass","Package":"native"}`), bytes.ReplaceAll(good, []byte(`"pass"`), []byte(`"skip"`)), bytes.ReplaceAll(good, []byte(`TestNative/case`), []byte(`TestForeign/case`)), []byte("not-json")} {
		if billingNativeRunsPassed(output, []string{"TestNative"}, []string{"case"}) {
			t.Fatal("undiscovered/skipped/foreign native test falsely passed")
		}
	}
}
