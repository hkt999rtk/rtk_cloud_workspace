package main

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type environmentCreateTransport struct {
	CACert     string                                        `json:"ca_cert"`
	Identities map[string]environmentCreateTransportIdentity `json:"identities"`
}
type environmentCreateTransportIdentity struct {
	Certificate string `json:"certificate"`
	Key         string `json:"key"`
}

func createEnvironmentTransport(store secretStore, planSHA, role string, names map[string][]string, now time.Time) (environmentCreateTransport, error) {
	material := environmentCreateTransport{Identities: map[string]environmentCreateTransportIdentity{}}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return material, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return material, err
	}
	root := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "rtk-create-" + store.Environment + "-" + role}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(4 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	der, err := x509.CreateCertificate(rand.Reader, root, root, &key.PublicKey, key)
	if err != nil {
		return material, err
	}
	material.CACert = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	root, err = x509.ParseCertificate(der)
	if err != nil {
		return material, err
	}
	identityNames := make([]string, 0, len(names))
	for name := range names {
		identityNames = append(identityNames, name)
	}
	sort.Strings(identityNames)
	for _, name := range identityNames {
		leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return material, err
		}
		serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
		if err != nil {
			return material, err
		}
		leaf := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: name}, DNSNames: names[name], NotBefore: now.Add(-time.Minute), NotAfter: root.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}}
		der, err := x509.CreateCertificate(rand.Reader, leaf, root, &leafKey.PublicKey, key)
		if err != nil {
			return material, err
		}
		private, err := x509.MarshalPKCS8PrivateKey(leafKey)
		if err != nil {
			return material, err
		}
		material.Identities[name] = environmentCreateTransportIdentity{string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}))}
	}
	base := filepath.Join("deployment", "create", "material", planSHA, role+"-transport")
	body, err := json.Marshal(material)
	if err != nil {
		return material, err
	}
	if err := store.write(filepath.Join(base, "identities.json"), body, false); err != nil {
		return material, environmentCreateBlocker("deployment.bootstrap.transport", "initial transport material already exists or cannot be saved; preserve the original attempt")
	}
	if err := store.write(filepath.Join(base, "ca.crt"), []byte(material.CACert), false); err != nil {
		return material, err
	}
	return material, nil
}

func ensureEnvironmentCreateAccountSigner(store secretStore) (map[string]string, error) {
	base := filepath.Join("pki", "account-manager-jwt")
	values := map[string]string{}
	existing := 0
	for _, name := range []string{"access.key", "access.pub", "refresh.key", "refresh.pub"} {
		path, err := privateDeploymentStatePath(store.Environment, filepath.Join(base, name))
		if err != nil {
			return nil, err
		}
		raw, err := readStorageEvidenceFile(path)
		if err == nil {
			values[name] = string(raw)
			existing++
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	if existing != 0 && existing != 4 {
		return nil, environmentCreateBlocker("deployment.bootstrap.account-signer", "recorded Account Manager signer is incomplete; missing identity cannot be regenerated")
	}
	if existing == 0 {
		for _, role := range []string{"access", "refresh"} {
			key, err := rsa.GenerateKey(rand.Reader, 3072)
			if err != nil {
				return nil, err
			}
			private := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
			publicDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
			if err != nil {
				return nil, err
			}
			public := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER})
			if err := store.write(filepath.Join(base, role+".key"), private, false); err != nil {
				return nil, err
			}
			if err := store.write(filepath.Join(base, role+".pub"), public, false); err != nil {
				return nil, err
			}
			values[role+".key"], values[role+".pub"] = string(private), string(public)
		}
	}
	for _, role := range []string{"access", "refresh"} {
		block, _ := pem.Decode([]byte(values[role+".key"]))
		pub, _ := pem.Decode([]byte(values[role+".pub"]))
		if block == nil || pub == nil {
			return nil, environmentCreateBlocker("deployment.bootstrap.account-signer", "recorded Account Manager signer cannot be decoded")
		}
		key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		public, err := x509.ParsePKIXPublicKey(pub.Bytes)
		rsaPublic, ok := public.(*rsa.PublicKey)
		if err != nil || !ok || key.N.Cmp(rsaPublic.N) != 0 || key.E != rsaPublic.E {
			return nil, environmentCreateBlocker("deployment.bootstrap.account-signer", "recorded Account Manager signer public/private identity differs")
		}
	}
	return values, nil
}

func environmentCreateSecret(namespace, name string, data map[string]string) deploymentplan.Definition {
	stringsData := deploymentplan.Object{}
	for key, value := range data {
		stringsData[key] = value
	}
	return deploymentplan.Definition{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "Secret", "metadata": deploymentplan.Object{"name": name, "namespace": namespace}, "type": "Opaque", "stringData": stringsData}}
}

func decodeEnvironmentCreateManifests(manifests ...string) ([]deploymentplan.Definition, error) {
	var definitions []deploymentplan.Definition
	for _, manifest := range manifests {
		decoder := yaml.NewDecoder(strings.NewReader(manifest))
		for {
			var object deploymentplan.Object
			err := decoder.Decode(&object)
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, environmentCreateBlocker("deployment.bootstrap.renderer", "maintained foundation renderer produced invalid resource YAML")
			}
			if len(object) == 0 {
				continue
			}
			raw, err := json.Marshal(object)
			if err != nil || json.Unmarshal(raw, &object) != nil {
				return nil, err
			}
			definitions = append(definitions, deploymentplan.Definition{Object: object, Phase: environmentResourcePhase(deploymentplan.String(object["kind"]))})
		}
	}
	return definitions, nil
}

func prepareEnvironmentCreateBootstrap(ctx context.Context, cfg deploymentConfig, plan *environmentCreatePlan, store secretStore, operator map[string]string, execution *environmentCreateExecution, apply environmentCreateBootstrapApply) (environmentCreateBootstrapRuntime, func(), error) {
	runtime := environmentCreateBootstrapRuntime{ExecutionSHA256: execution.Journal.PlanSHA256}
	runtime.Execution = execution
	stop := func() {}
	if err := execution.Check(ctx); err != nil {
		return runtime, stop, err
	}
	if err := initializeEnvironmentCreateRuntime(store, operator); err != nil {
		return runtime, stop, err
	}
	raw, err := readBoundedEnvironmentFile(store.KubeconfigPath(), true)
	if err != nil {
		return runtime, stop, err
	}
	backend, err := newEnvironmentKubernetesBackend(string(raw), cfg.Environment)
	if err != nil {
		return runtime, stop, err
	}
	backend.RolloutTimeout, err = environmentRolloutTimeout(cfg.Values)
	if err != nil {
		return runtime, stop, err
	}
	runtime.Backend = backend
	if err := createEnvironmentExecutionNamespaces(ctx, cfg, backend, execution); err != nil {
		return runtime, stop, err
	}
	if err := createEnvironmentRetainStorageClass(ctx, cfg, backend, execution); err != nil {
		return runtime, stop, err
	}
	management, err := createEnvironmentTransport(store, execution.Journal.PlanSHA256, "management", map[string][]string{"account-manager": {"account-manager." + plan.Stack + "-account-manager.svc.cluster.local"}, "pki-controller": {"pki-controller." + plan.Stack + "-video-cloud.svc"}, "certissuer": {"certissuer." + plan.Stack + "-video-cloud.svc"}, "video-cloud-api": {"video-cloud-api-pki." + plan.Stack + "-video-cloud.svc"}, "video-cloud-api-app-pki": {"video-cloud-api-app-pki." + plan.Stack + "-video-cloud.svc"}}, time.Now().UTC())
	if err != nil {
		return runtime, stop, err
	}
	baoTransport, err := createEnvironmentTransport(store, execution.Journal.PlanSHA256, "openbao", map[string][]string{"openbao": lkeOpenBaoDNSNames(environmentCreateProviderValues(cfg))}, time.Now().UTC())
	if err != nil {
		return runtime, stop, err
	}
	signer, err := ensureEnvironmentCreateAccountSigner(store)
	if err != nil {
		return runtime, stop, err
	}
	runtimeSecrets, err := environmentRuntimeSecretSnapshot(store)
	if err != nil {
		return runtime, stop, err
	}
	// The configured initial administrator is a genuine prerequisite; the
	// canonical runtime mirror must agree before Account Manager seals bootstrap.
	if runtimeSecrets["platform-admin"] != operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"] {
		return runtime, stop, environmentCreateBlocker("deployment.bootstrap.operator", "canonical platform-admin credential differs from the explicit initial operator password")
	}
	values := environmentCreateProviderValues(cfg)
	for role, image := range plan.Release.Images {
		values[role] = image.Reference
	}
	snapshot := environmentDeploymentSnapshot{Operator: operator, Secrets: runtimeSecrets, Kubeconfig: string(raw)}
	if err := readEnvironmentCreateStorageRenderSnapshot(cfg, store, &snapshot); err != nil {
		return runtime, stop, err
	}
	var base []deploymentplan.Definition
	err = withEnvironmentRenderInputs(cfg, store, plan.Release, snapshot, func(env map[string]string) error {
		var renderErr error
		base, renderErr = renderEnvironmentCreateFoundationBase(env, plan.DurableFoundation)
		return renderErr
	})
	if err != nil {
		return runtime, stop, err
	}
	videoNS, accountNS, secretsNS := plan.Stack+"-video-cloud", plan.Stack+"-account-manager", plan.Stack+"-secrets"
	for _, namespace := range []string{videoNS, accountNS, secretsNS} {
		base = append(base, deploymentplan.Definition{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "pki-create-transport-trust", "namespace": namespace}, "data": deploymentplan.Object{"ca.crt": management.CACert}}})
		pull, err := newK8SDockerConfigSecretObject(namespace, lkeImagePullSecretName(values), operator["GHCR_PULL_USERNAME"], operator["GHCR_PULL_TOKEN"])
		if err != nil {
			return runtime, stop, err
		}
		base = append(base, deploymentplan.Definition{Object: pull, Phase: "configuration"})
	}
	for name, identity := range management.Identities {
		namespace := videoNS
		if name == "account-manager" {
			namespace = accountNS
		}
		base = append(base, environmentCreateSecret(namespace, "pki-create-"+name+"-transport", map[string]string{"tls.crt": identity.Certificate, "tls.key": identity.Key, "ca.crt": management.CACert}))
	}
	baoIdentity := baoTransport.Identities["openbao"]
	base = append(base, environmentCreateSecret(secretsNS, "openbao-transport-tls", map[string]string{"tls.crt": baoIdentity.Certificate, "tls.key": baoIdentity.Key, "ca.crt": baoTransport.CACert}), deploymentplan.Definition{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "pki-openbao-transport-ca", "namespace": videoNS}, "data": deploymentplan.Object{"ca.crt": baoTransport.CACert}}}, environmentCreateSecret(accountNS, "account-manager-jwt", signer))
	dbURL := "postgres://" + url.UserPassword("postgres", runtimeSecrets["postgres"]).String() + "@postgresql." + plan.Stack + "-platform.svc:5432/video_cloud?sslmode=disable"
	base = append(base, environmentCreateSecret(videoNS, "pki-migration-database", map[string]string{"url": dbURL}), environmentCreateSecret(videoNS, "pki-controller-database", map[string]string{"url": dbURL}), environmentCreateSecret(videoNS, "pki-platform-key", map[string]string{"public.pem": signer["access.pub"]}))
	openbao, err := renderEnvironmentCreateOpenBaoFoundation(cfg, plan.Release, "openbao-transport-tls", "pki-openbao-transport-ca")
	if err != nil {
		return runtime, stop, err
	}
	base = append(base, openbao...)
	if err := bindEnvironmentNewStorageClasses(base, cfg.Values["DURABLE_DATA_STORAGE_CLASS"], nil); err != nil {
		return runtime, stop, err
	}
	if err := apply(ctx, "foundation", base); err != nil {
		return runtime, stop, err
	}
	return continueEnvironmentCreateFoundation(ctx, cfg, plan, store, operator, execution, apply, runtime, management)
}

// The plan pins the foundation's storage recipe. Historical attempts keep their
// original emptyDir body; current new generations allocate the maintained claim
// before starting Redis and need no data migration later.
func renderEnvironmentCreateFoundationBase(env map[string]string, durable bool) ([]deploymentplan.Definition, error) {
	definitions, err := decodeEnvironmentCreateManifests(lkePostgresSecretManifest(env), lkePostgresInitManifest(env), lkePostgresServiceManifest(env), lkePostgresStatefulSetManifest(env), lkeRedisDeploymentManifest(env), lkeRedisServiceManifest(env), lkeAccountManagerSecretManifest(env))
	if err == nil && durable {
		definitions, err = applyEnvironmentDurableComponents(definitions, env, []environmentDurableComponent{{"redis", "REDIS_DATA_STORAGE", "redis"}})
	}
	return definitions, err
}

// Forward recovery enters only after separately verifying the original
// foundation, transport material and runtime catalog; it does not recreate
// namespaces, signers, transport CAs or completed storage activation.
func continueEnvironmentCreateFoundation(ctx context.Context, cfg deploymentConfig, plan *environmentCreatePlan, store secretStore, operator map[string]string, execution *environmentCreateExecution, apply environmentCreateBootstrapApply, runtime environmentCreateBootstrapRuntime, management environmentCreateTransport) (environmentCreateBootstrapRuntime, func(), error) {
	stop := func() {}
	backend := runtime.Backend
	bao, err := initializeEnvironmentCreateOpenBao(ctx, cfg, store, backend, execution)
	if err != nil {
		return runtime, stop, err
	}
	runtime.OpenBao = bao
	return continueEnvironmentCreateAfterOpenBao(ctx, cfg, plan, store, operator, execution, apply, runtime, management)
}

func continueEnvironmentCreateAfterOpenBao(ctx context.Context, cfg deploymentConfig, plan *environmentCreatePlan, store secretStore, operator map[string]string, execution *environmentCreateExecution, apply environmentCreateBootstrapApply, runtime environmentCreateBootstrapRuntime, management environmentCreateTransport) (environmentCreateBootstrapRuntime, func(), error) {
	stop := func() {}
	migrations, err := renderEnvironmentCreatePKIPhase(ctx, cfg, plan.Bootstrap, "migrate")
	if err != nil {
		return runtime, stop, err
	}
	if err := apply(ctx, "pki-migrate", migrations); err != nil {
		return runtime, stop, err
	}
	return continueEnvironmentCreateAfterMigration(ctx, cfg, plan, store, operator, execution, apply, runtime, management)
}

func continueEnvironmentCreateAfterMigration(ctx context.Context, cfg deploymentConfig, plan *environmentCreatePlan, store secretStore, operator map[string]string, execution *environmentCreateExecution, apply environmentCreateBootstrapApply, runtime environmentCreateBootstrapRuntime, management environmentCreateTransport) (environmentCreateBootstrapRuntime, func(), error) {
	stop := func() {}
	grants, err := renderEnvironmentCreatePKIPhase(ctx, cfg, plan.Bootstrap, "grants")
	if err != nil {
		return runtime, stop, err
	}
	if err := apply(ctx, "pki-grants", grants); err != nil {
		return runtime, stop, err
	}
	if err := initializeEnvironmentCreateDatabaseRoles(ctx, cfg, plan, store, execution, apply); err != nil {
		return runtime, stop, err
	}
	return continueEnvironmentCreateAfterDatabase(ctx, cfg, plan, store, operator, execution, apply, runtime, management)
}

func continueEnvironmentCreateAfterDatabase(ctx context.Context, cfg deploymentConfig, plan *environmentCreatePlan, store secretStore, operator map[string]string, execution *environmentCreateExecution, apply environmentCreateBootstrapApply, runtime environmentCreateBootstrapRuntime, management environmentCreateTransport) (environmentCreateBootstrapRuntime, func(), error) {
	stop := func() {}
	migration, err := renderEnvironmentCreateAccountMigration(cfg, plan.Release)
	if err != nil {
		return runtime, stop, err
	}
	if err := apply(ctx, "account-migrate", migration); err != nil {
		return runtime, stop, err
	}
	account, err := renderEnvironmentCreateAccountFoundationWithRuntime(cfg, plan.Release, operator, store)
	if err != nil {
		return runtime, stop, err
	}
	if err := apply(ctx, "account-bootstrap", account); err != nil {
		return runtime, stop, err
	}
	return continueEnvironmentCreateAfterAccount(ctx, cfg, plan, store, operator, execution, apply, runtime, management)
}

func continueEnvironmentCreateAfterAccount(ctx context.Context, cfg deploymentConfig, plan *environmentCreatePlan, store secretStore, operator map[string]string, execution *environmentCreateExecution, apply environmentCreateBootstrapApply, runtime environmentCreateBootstrapRuntime, management environmentCreateTransport) (environmentCreateBootstrapRuntime, func(), error) {
	stop := func() {}
	backend := runtime.Backend
	origin, accountClient, err := newEnvironmentCreateAccountClient(ctx, cfg, backend, execution)
	if err != nil {
		return runtime, stop, err
	}
	runtime.APIOrigin, runtime.APIClient = origin, accountClient
	binding, recorded, err := resolveInitialOperatorBinding(ctx, cfg, store, execution, backend, operator)
	if err != nil {
		stop()
		return runtime, func() {}, err
	}
	client, err := newEnvironmentCreatePKIClient(origin, runtime.APIClient, cfg.Environment, binding.UserID)
	if err != nil {
		stop()
		return runtime, func() {}, err
	}
	operatorID, err := client.Authenticate(ctx, operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"], operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"])
	if err != nil {
		stop()
		return runtime, func() {}, err
	}
	if !recorded {
		if err := recordInitialOperatorBinding(ctx, store, execution, binding); err != nil {
			stop()
			return runtime, func() {}, err
		}
	}
	generationOperator := map[string]string{}
	for key, value := range operator {
		generationOperator[key] = value
	}
	generationOperator["PKI_OPERATOR_USER_ID"] = binding.UserID
	listeners, err := renderEnvironmentCreateInitialPKIListeners(cfg, plan.Release, operatorID, generationOperator)
	if err != nil {
		stop()
		return runtime, func() {}, err
	}
	if len(runtime.InitialListeners) == 0 {
		if err := configureInitialListenerAuthentication(ctx, cfg, runtime); err != nil {
			stop()
			return runtime, func() {}, err
		}
		if err := apply(ctx, "pki-initial-listeners", listeners); err != nil {
			stop()
			return runtime, func() {}, err
		}
	} else {
		if runtime.InitialAuthSeparated {
			separated, _, err := initialCertIssuerAuthDefinitions(cfg, listeners)
			if err != nil {
				stop()
				return runtime, func() {}, err
			}
			listeners = separated
		}
		compiled, compileErr := deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: plan.Stack, Operation: "scoped", ConfigurationSHA256: plan.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(plan.Release), BindingsSHA256: environmentCreateStageBindingsSHA("pki-initial-listeners", listeners)}, listeners, nil)
		if compileErr != nil {
			stop()
			return runtime, func() {}, compileErr
		}
		var canonical []deploymentplan.Definition
		for _, r := range compiled.Resources {
			canonical = append(canonical, deploymentplan.Definition{Object: r.Desired, Phase: r.Phase})
		}
		if !sameEnvironmentCreateDefinitions(runtime.InitialListeners, canonical) {
			stop()
			return runtime, func() {}, createProviderRecoveryBlock("verified original initial listeners differ from their compiled generation recipe")
		}
	}
	runtime.InitialListeners = listeners
	runtime.PKIClient = client
	runtime.TransportTrustConfigMap = "pki-create-transport-trust"
	runtime.TransportCAPEM = management.CACert
	runtime.TransportSecrets = map[string]string{"account-manager": "pki-create-account-manager-transport", "pki-controller": "pki-create-pki-controller-transport", "certissuer": "pki-create-certissuer-transport", "video-cloud-api": "pki-create-video-cloud-api-transport", "video-cloud-api-app-pki": "pki-create-video-cloud-api-app-pki-transport"}
	rootCert, parseErr := parseFirstCertificatePEM([]byte(management.CACert))
	if parseErr != nil {
		stop()
		return runtime, func() {}, environmentCreateBlocker("deployment.bootstrap.transport", "initial transport root cannot be verified")
	}
	runtime.TransportRootSHA256 = environmentPreflightBytesSHA256(rootCert.Raw)
	return runtime, stop, nil
}

func sameEnvironmentCreateDefinitions(left, right []deploymentplan.Definition) bool {
	if len(left) != len(right) {
		return false
	}
	byID := map[string]string{}
	for _, d := range left {
		id := deploymentplan.ID(d.Object)
		if id == "" || byID[id] != "" {
			return false
		}
		byID[id] = deploymentplan.Digest(struct {
			Phase  string
			Object deploymentplan.Object
		}{d.Phase, d.Object})
	}
	for _, d := range right {
		id := deploymentplan.ID(d.Object)
		if byID[id] != deploymentplan.Digest(struct {
			Phase  string
			Object deploymentplan.Object
		}{d.Phase, d.Object}) {
			return false
		}
		delete(byID, id)
	}
	return len(byID) == 0
}

func renderEnvironmentCreateAccountFoundationWithRuntime(cfg deploymentConfig, release environmentRelease, operator map[string]string, store secretStore) ([]deploymentplan.Definition, error) {
	secrets, err := environmentRuntimeSecretSnapshot(store)
	if err != nil {
		return nil, err
	}
	snapshot := environmentDeploymentSnapshot{Operator: operator, Secrets: secrets}
	if err := readEnvironmentCreateStorageRenderSnapshot(cfg, store, &snapshot); err != nil {
		return nil, err
	}
	var definitions []deploymentplan.Definition
	err = withEnvironmentRenderInputs(cfg, store, release, snapshot, func(map[string]string) error {
		var renderErr error
		definitions, renderErr = renderEnvironmentCreateAccountFoundation(cfg, release, operator)
		return renderErr
	})
	return definitions, err
}

func renderEnvironmentCreateAccountFoundation(cfg deploymentConfig, release environmentRelease, operator map[string]string) ([]deploymentplan.Definition, error) {
	values := environmentCreateProviderValues(cfg)
	for key, image := range release.Images {
		values[key] = image.Reference
	}
	var manifest string
	for _, workload := range k8sWorkloads(values) {
		if workload.Key == "account-manager" {
			manifest = lkeDeploymentManifest(values, workload, nil)
		}
	}
	if manifest == "" {
		return nil, environmentCreateBlocker("deployment.bootstrap.account", "maintained Account Manager foundation is unavailable")
	}
	definitions, err := decodeEnvironmentCreateManifests(manifest, lkeServiceManifest(values, environmentCreateAccountWorkload(values)))
	if err != nil {
		return nil, err
	}
	for _, definition := range definitions {
		if definition.Object["kind"] != "Deployment" {
			continue
		}
		pod := environmentPKIPod(definition.Object)
		container := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
		// Initial Account Manager owns password/JWT authentication only. The
		// former static App issuer and registration TLS owners do not exist in
		// a fresh target; managed routes/listeners are installed later.
		pod["volumes"] = environmentCreateServiceWithoutNamed(pod["volumes"], "account-manager-certissuer-client", "account-manager-service-registration-tls")
		container["volumeMounts"] = environmentCreateServiceWithoutNamed(container["volumeMounts"], "account-manager-certissuer-client", "account-manager-service-registration-tls")
		container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(map[string]string{"APP_CERT_ISSUER_BASE_URL": "", "APP_CERT_ISSUER_SOCKET": "", "APP_CERT_ISSUER_CLIENT_CERT": "", "APP_CERT_ISSUER_CLIENT_KEY": "", "APP_CERT_ISSUER_CA_FILE": "", "ACCOUNT_MANAGER_SERVICE_REGISTRATION_PORT": "", "ACCOUNT_MANAGER_SERVICE_REGISTRATION_SERVER_CERT": "", "ACCOUNT_MANAGER_SERVICE_REGISTRATION_SERVER_KEY": "", "ACCOUNT_MANAGER_SERVICE_REGISTRATION_CLIENT_CA": "", "ACCOUNT_MANAGER_SERVICE_REGISTRATION_CLIENT_CRL": ""}))
		container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(map[string]string{"JWT_SIGNER_PROVIDER": "pem", "JWT_ACCESS_PRIVATE_KEY_PATH": "/run/account-jwt/access.key", "JWT_ACCESS_PUBLIC_KEY_PATH": "/run/account-jwt/access.pub", "JWT_REFRESH_PRIVATE_KEY_PATH": "/run/account-jwt/refresh.key", "JWT_REFRESH_PUBLIC_KEY_PATH": "/run/account-jwt/refresh.pub", "PKI_ENVIRONMENT": cfg.Environment, "PKI_REQUIRE_USER_MFA": cfg.Values["PKI_REQUIRE_USER_MFA"], "PKI_CONTROLLER_URL": "https://pki-controller." + values["CLOUD_STACK_NAME"] + "-video-cloud.svc:18446", "PKI_CONTROLLER_CLIENT_CERT": "/run/pki-create/tls.crt", "PKI_CONTROLLER_CLIENT_KEY": "/run/pki-create/tls.key", "PKI_CONTROLLER_CA": "/run/pki-create/ca.crt"}))
		pod["volumes"] = mergeEnvironmentNamedFields(pod["volumes"], []any{deploymentplan.Object{"name": "account-jwt", "secret": deploymentplan.Object{"secretName": "account-manager-jwt", "defaultMode": float64(0400)}}, deploymentplan.Object{"name": "pki-create-transport", "secret": deploymentplan.Object{"secretName": "pki-create-account-manager-transport", "defaultMode": float64(0400)}}})
		container["volumeMounts"] = mergeEnvironmentNamedFields(container["volumeMounts"], []any{deploymentplan.Object{"name": "account-jwt", "mountPath": "/run/account-jwt", "readOnly": true}, deploymentplan.Object{"name": "pki-create-transport", "mountPath": "/run/pki-create", "readOnly": true}})
	}
	return definitions, nil
}

func environmentCreateAccountWorkload(values map[string]string) k8sWorkload {
	for _, workload := range k8sWorkloads(values) {
		if workload.Key == "account-manager" {
			return workload
		}
	}
	return k8sWorkload{}
}

func environmentCreateEnv(values map[string]string) []any {
	var env []any
	for _, key := range sortedMapKeys(values) {
		env = append(env, deploymentplan.Object{"name": key, "value": values[key]})
	}
	return env
}

func renderEnvironmentCreateInitialPKIListeners(cfg deploymentConfig, release environmentRelease, operatorID string, operator map[string]string) ([]deploymentplan.Definition, error) {
	values := environmentCreateProviderValues(cfg)
	for key, image := range release.Images {
		values[key] = image.Reference
	}
	if operatorID == "" || operator["PKI_OPERATOR_USER_ID"] != "" && operator["PKI_OPERATOR_USER_ID"] != operatorID {
		return nil, environmentCreateBlocker("deployment.bootstrap.operator", "authenticated initial operator does not match retained configured operator ownership")
	}
	namespace := values["CLOUD_STACK_NAME"] + "-video-cloud"
	var definitions []deploymentplan.Definition
	for _, name := range []string{"pki-controller", "certissuer"} {
		binding := environmentPodBindings{Kind: "Deployment", Name: name, NamespaceSuffix: "video-cloud", Component: environmentManagedComponents[name], Containers: []environmentContainerBindings{{Name: name, ImageKey: "LKE_VIDEO_CLOUD_IMAGE"}}}
		object, err := renderManagedEnvironmentComponent(values, binding)
		if err != nil {
			return nil, err
		}
		pod := environmentPKIPod(object)
		container := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
		container["image"] = release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference
		// Initial trust installation uses the native controller ServiceListener
		// for each named owner; no Device, App or runtime leaf signer is enabled.
		container["command"] = []any{"/app/pkicontroller"}
		port := 18446
		if name == "certissuer" {
			port = 18445
		}
		container["ports"] = []any{deploymentplan.Object{"name": "tls", "containerPort": float64(port)}}
		container["readinessProbe"] = deploymentplan.Object{"tcpSocket": deploymentplan.Object{"port": "tls"}, "periodSeconds": float64(5)}
		pod["imagePullSecrets"] = []any{deploymentplan.Object{"name": "ghcr-pull"}}
		pod["automountServiceAccountToken"] = false
		pod["serviceAccountName"] = name
		pod["securityContext"] = deploymentplan.Object{"runAsNonRoot": true, "runAsUser": float64(10001), "runAsGroup": float64(10001), "fsGroup": float64(10001), "seccompProfile": deploymentplan.Object{"type": "RuntimeDefault"}}
		pod["volumes"] = []any{deploymentplan.Object{"name": "transport", "secret": deploymentplan.Object{"secretName": "pki-create-" + name + "-transport", "defaultMode": float64(0440)}}, deploymentplan.Object{"name": "platform-key", "secret": deploymentplan.Object{"secretName": "pki-platform-key", "defaultMode": float64(0444)}}, deploymentplan.Object{"name": "openbao-ca", "configMap": deploymentplan.Object{"name": "pki-openbao-transport-ca"}}, deploymentplan.Object{"name": "openbao-auth", "projected": deploymentplan.Object{"defaultMode": float64(0440), "sources": []any{deploymentplan.Object{"serviceAccountToken": deploymentplan.Object{"audience": "openbao", "expirationSeconds": float64(3600), "path": "token"}}}}}}
		container["volumeMounts"] = []any{deploymentplan.Object{"name": "transport", "mountPath": "/run/pki-create", "readOnly": true}, deploymentplan.Object{"name": "platform-key", "mountPath": "/run/platform-key", "readOnly": true}, deploymentplan.Object{"name": "openbao-ca", "mountPath": "/run/openbao-ca", "readOnly": true}, deploymentplan.Object{"name": "openbao-auth", "mountPath": "/run/openbao-auth", "readOnly": true}}
		settings := map[string]string{"PKI_ENVIRONMENT": cfg.Environment, "PKI_REQUIRE_USER_MFA": cfg.Values["PKI_REQUIRE_USER_MFA"], "PKI_OPERATOR_USER_ID": operatorID, "PKI_OPERATOR_SIGNER_REF": operator["PKI_OPERATOR_SIGNER_REF"], "PKI_ACCOUNT_MANAGER_PUBLIC_KEY": "/run/platform-key/public.pem", "PKI_SERVER_CERT": "/run/pki-create/tls.crt", "PKI_SERVER_KEY": "/run/pki-create/tls.key", "PKI_CLIENT_CA": "/run/pki-create/ca.crt", "PKI_LISTEN_ADDR": fmt.Sprintf(":%d", port), "PKI_APP_CRL_WORKER_ENABLED": "false", "PKI_SERVER_CRL_DOMAIN": "", "OPENBAO_ADDR": "https://openbao." + values["CLOUD_STACK_NAME"] + "-secrets.svc:8200", "OPENBAO_CACERT": "/run/openbao-ca/ca.crt", "OPENBAO_AUTH_METHOD": "kubernetes", "OPENBAO_KUBERNETES_ROLE": "pki-controller-" + cfg.Environment, "OPENBAO_SERVICE_ACCOUNT_TOKEN_FILE": "/run/openbao-auth/token"}
		for _, domain := range managedPKIDomains {
			for _, prefix := range []string{"PKI_REQUIRED_CONSUMERS_", "PKI_REQUIRED_BUNDLE_CONSUMERS_"} {
				key := prefix + strings.ToUpper(domain)
				settings[key] = cfg.Values[key]
			}
		}
		container["env"] = environmentCreateEnv(settings)
		container["env"] = append(certIssuerObjectList(container["env"]), deploymentplan.Object{"name": "PKI_DATABASE_URL", "valueFrom": deploymentplan.Object{"secretKeyRef": deploymentplan.Object{"name": "pki-controller-database", "key": "url"}}})
		definitions = append(definitions, deploymentplan.Definition{Object: object, Phase: "workload"})
		definitions = append(definitions, deploymentplan.Definition{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ServiceAccount", "metadata": deploymentplan.Object{"name": name, "namespace": namespace}, "automountServiceAccountToken": false}})
		definitions = append(definitions, deploymentplan.Definition{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "Service", "metadata": deploymentplan.Object{"name": name, "namespace": namespace}, "spec": deploymentplan.Object{"selector": deploymentplan.Object{"app.kubernetes.io/name": name}, "ports": []any{deploymentplan.Object{"name": "https", "port": float64(port), "targetPort": float64(port)}}}}})
	}
	return definitions, nil
}

func openEnvironmentCreateTunnel(ctx context.Context, backend environmentKubernetesBackend, namespace, resource string, remotePort int) (string, func(), error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", func() {}, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	kubeconfig, cleanup, err := backend.privateKubeconfig()
	if err != nil {
		return "", func() {}, err
	}
	childCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(childCtx, "kubectl", "--kubeconfig", kubeconfig, "--namespace", namespace, "port-forward", "--address=127.0.0.1", resource, fmt.Sprintf("%d:%d", port, remotePort))
	cmd.Stderr = io.Discard
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cleanup()
		cancel()
		return "", func() {}, err
	}
	if err := cmd.Start(); err != nil {
		cleanup()
		cancel()
		return "", func() {}, environmentCreateBlocker("deployment.bootstrap.tunnel", "selected native Account Manager tunnel cannot be started")
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	ready := make(chan bool, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), fmt.Sprintf("Forwarding from 127.0.0.1:%d ->", port)) {
				select {
				case ready <- true:
				default:
				}
			}
		}
		select {
		case ready <- false:
		default:
		}
	}()
	stop := func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = cmd.Process.Kill()
		}
		cleanup()
	}
	select {
	case ok := <-ready:
		if ok {
			return fmt.Sprintf("http://127.0.0.1:%d", port), stop, nil
		}
	case <-done:
	case <-ctx.Done():
	case <-time.After(30 * time.Second):
	}
	stop()
	return "", func() {}, environmentCreateBlocker("deployment.bootstrap.tunnel", "selected native Account Manager tunnel did not become ready")
}
