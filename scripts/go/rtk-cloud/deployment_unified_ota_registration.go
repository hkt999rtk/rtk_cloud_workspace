package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The formal image artifact supplies these SDK-owned fields. They are never
// inferred from a running OTA Pod or substituted with a deployment-owned version.
type environmentOTARegistrationManifest struct {
	ServiceID       string                             `json:"service_id"`
	ManifestVersion string                             `json:"manifest_version"`
	ProtocolVersion string                             `json:"protocol_version"`
	EndpointRef     string                             `json:"endpoint_ref"`
	Options         []environmentOTARegistrationOption `json:"options"`
}

type environmentOTARegistrationOption struct {
	Code             string   `json:"code"`
	DisplayName      string   `json:"display_name"`
	Description      string   `json:"description"`
	Requires         []string `json:"requires,omitempty"`
	LogRetentionDays []int    `json:"log_retention_days,omitempty"`
}

var otaRegistrationInstancePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func otaRegistrationBlocker(resource, cause string) error {
	return &deploymentplan.Blocker{ID: "deployment.compatibility.ota-registration", Resource: resource, Cause: cause, Next: "verify the exact-image OTA registration descriptor and original Account Manager immutable manifest/workload approval; preserve existing versions, publication and status, then repeat full preflight; actual registration, heartbeat and readiness are post-deploy checks"}
}

func (m environmentOTARegistrationManifest) validate() error {
	if m.ServiceID != "ota" || len(m.ManifestVersion) == 0 || len(m.ManifestVersion) > 64 || m.ProtocolVersion != "1" || len(m.EndpointRef) == 0 || len(m.EndpointRef) > 128 || len(m.Options) != 1 {
		return errors.New("invalid OTA registration descriptor")
	}
	o := m.Options[0]
	if o.Code != "ota" || len(o.DisplayName) == 0 || len(o.DisplayName) > 120 || len(o.Description) > 1000 || !slices.Equal(o.Requires, []string{"mqtt"}) || len(o.LogRetentionDays) != 0 {
		return errors.New("invalid OTA registration option descriptor")
	}
	return nil
}

// Match Account Manager's manifestDigest field order and normalization exactly.
// Description is retained even when empty; Requires and retention omit empties.
func (m environmentOTARegistrationManifest) digest() string {
	options := slices.Clone(m.Options)
	slices.SortFunc(options, func(a, b environmentOTARegistrationOption) int { return strings.Compare(a.Code, b.Code) })
	for i := range options {
		options[i].Requires = slices.Clone(options[i].Requires)
		slices.Sort(options[i].Requires)
	}
	raw, _ := json.Marshal(struct {
		ServiceID string                             `json:"service_id"`
		Version   string                             `json:"version"`
		Protocol  string                             `json:"protocol"`
		Options   []environmentOTARegistrationOption `json:"options"`
		Endpoint  string                             `json:"endpoint_ref"`
	}{m.ServiceID, m.ManifestVersion, m.ProtocolVersion, options, m.EndpointRef})
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

// Call only after verifyPublicationJSON has bound the complete artifact to the
// selected source, image digest and successful formal CI attempt.
func publishedOTARegistration(raw []byte, image environmentReleaseImage) (*environmentOTARegistrationManifest, error) {
	if environmentImageRepository(image.Reference) != "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api" {
		return nil, nil
	}
	var artifact struct {
		Manifests map[string]json.RawMessage `json:"service_registration_manifests"`
	}
	if json.Unmarshal(raw, &artifact) != nil {
		return nil, errors.New("invalid OTA registration publication")
	}
	encoded, exists := artifact.Manifests["ota"]
	if !exists {
		return nil, nil
	}
	var manifest environmentOTARegistrationManifest
	if decodeEnvironmentJSON(encoded, &manifest) != nil || manifest.validate() != nil {
		return nil, otaRegistrationBlocker("", "formal exact-image OTA registration descriptor is invalid")
	}
	return &manifest, nil
}

func validateEnvironmentOTARegistrationRelease(definitions []deploymentplan.Definition, release environmentRelease) error {
	for _, d := range definitions {
		if d.Retire {
			continue
		}
		c := otaStartupContainer(d.Object)
		if c == nil {
			continue
		}
		image := release.Images["LKE_VIDEO_CLOUD_IMAGE"]
		if image.OTARegistration == nil || image.OTARegistration.validate() != nil || !immutableEnvironmentImage.MatchString(image.Reference) || !deploymentUpgradeSHA.MatchString(image.SourceRevision) || c["image"] != image.Reference {
			return otaRegistrationBlocker(deploymentplan.ID(d.Object), "enabled OTA lacks a valid registration descriptor from its selected exact-source immutable image artifact")
		}
		if mqttFoundationEnv(c, "VIDEO_CLOUD_OTA_SERVICE_ENDPOINT_REF") != image.OTARegistration.EndpointRef {
			return otaRegistrationBlocker(deploymentplan.ID(d.Object), "candidate OTA endpoint reference differs from its exact-image registration descriptor")
		}
		host, port, err := otaDependencyService(definitions, release.Stack+"-account-manager", "account-manager", "service-reg")
		u, urlErr := url.Parse(mqttFoundationEnv(c, "VIDEO_CLOUD_OTA_SERVICE_REGISTRATION_URL"))
		if err != nil || urlErr != nil || u.Scheme != "https" || (u.Hostname() != host && u.Hostname() != host+".cluster.local") || u.Port() != strconv.Itoa(port) || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			return otaRegistrationBlocker(deploymentplan.ID(d.Object), "candidate OTA registration URL does not select the original AM Service listener and TLS hostname")
		}
	}
	return nil
}

type otaRegistrationStoredManifest struct {
	Version  string                             `json:"manifest_version"`
	Digest   string                             `json:"digest_sha256"`
	Protocol string                             `json:"protocol_version"`
	Endpoint string                             `json:"endpoint_ref"`
	Options  []environmentOTARegistrationOption `json:"options"`
}

func (m otaRegistrationStoredManifest) validDigest() bool {
	return m.Digest != "" && m.Digest == (environmentOTARegistrationManifest{ServiceID: "ota", ManifestVersion: m.Version, ProtocolVersion: m.Protocol, EndpointRef: m.Endpoint, Options: m.Options}).digest()
}

func otaRegistrationQuery(environment, version, subject, issuer string) string {
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }
	env, ver := quote(environment), quote(version)
	fields := "json_build_object('manifest_version',m.manifest_version,'digest_sha256',m.digest_sha256,'protocol_version',m.protocol_version,'endpoint_ref',m.endpoint_ref,'options',m.options)"
	return "BEGIN TRANSACTION READ ONLY; SET LOCAL statement_timeout='10s'; SELECT json_build_object('schema',current_schema()," +
		"'candidate',(SELECT " + fields + " FROM platform_service_manifests m WHERE m.environment=" + env + " AND m.service_id='ota' AND m.manifest_version=" + ver + ")," +
		"'publication',(SELECT json_build_object('published_version',s.published_version,'status',s.status,'manifest'," + fields + ") FROM platform_services s LEFT JOIN platform_service_manifests m ON m.environment=s.environment AND m.service_id=s.service_id AND m.manifest_version=s.published_version WHERE s.environment=" + env + " AND s.service_id='ota')," +
		"'workload',(SELECT json_build_object('service_id',service_id,'instance_id',instance_id,'allowed_option_codes',allowed_option_codes,'revoked',revoked_at IS NOT NULL) FROM platform_service_workloads WHERE environment=" + env + " AND certificate_subject=" + quote(subject) + " AND issuer_fingerprint_sha256=" + quote(issuer) + ")," +
		"'mqtt_defined',EXISTS(SELECT 1 FROM platform_services s JOIN platform_service_manifests m ON m.environment=s.environment AND m.service_id=s.service_id AND m.manifest_version=s.published_version WHERE s.environment=" + env + " AND s.service_id='mqtt' AND m.options @> '[{\"code\":\"mqtt\"}]'::jsonb)," +
		"'ota_option_conflict',EXISTS(SELECT 1 FROM platform_services s JOIN platform_service_manifests m ON m.environment=s.environment AND m.service_id=s.service_id AND m.manifest_version=s.published_version WHERE s.environment=" + env + " AND s.service_id<>'ota' AND m.options @> '[{\"code\":\"ota\"}]'::jsonb)); COMMIT;"
}

func checkEnvironmentOTARegistration(ctx context.Context, p *deploymentplan.Plan, release environmentRelease, store secretStore, read func(context.Context, string) (deploymentplan.Object, error), command func(context.Context, ...string) ([]byte, error)) error {
	definitions, resources := []deploymentplan.Definition{}, map[string]deploymentplan.Resource{}
	ota := deploymentplan.Resource{}
	for _, r := range p.Resources {
		resources[r.ID] = r
		if r.Action != "retire" {
			definitions = append(definitions, deploymentplan.Definition{Object: r.Desired})
			if otaStartupContainer(r.Desired) != nil {
				ota = r
			}
		}
	}
	if err := validateEnvironmentOTARegistrationRelease(definitions, release); err != nil || ota.ID == "" {
		return err
	}
	fail := func(cause string) error { return otaRegistrationBlocker(ota.ID, cause) }
	if release.Environment != p.Environment || release.Stack != p.Stack || store.Environment != p.Environment || p.ReleaseSHA256 != deploymentplan.Digest(release) {
		return fail("candidate OTA registration is not bound to the reviewed release and selected environment")
	}
	if p.Operation != "upgrade" {
		return nil
	}
	if ctx.Err() != nil {
		return fail("original OTA registration qualification was interrupted")
	}
	if p.Stack != "video-cloud-"+p.Environment || !keySet("dev", "staging", "prod")[p.Environment] {
		return fail("candidate OTA registration belongs to another environment")
	}
	readOriginal := func(id string) (deploymentplan.Object, error) {
		r, exists := resources[id]
		if !exists || r.UID == "" || r.Action == "retire" {
			return nil, fail("original registration prerequisite owner is unavailable")
		}
		o, err := read(ctx, id)
		if err != nil {
			return nil, fail(secretCheckFailure(err, "original registration prerequisite cannot be read").Error())
		}
		if o == nil || deploymentplan.Map(o["metadata"])["uid"] != r.UID || deploymentplan.Map(o["metadata"])["deletionTimestamp"] != nil {
			return nil, fail("original registration prerequisite owner changed or is unavailable")
		}
		return o, nil
	}
	c := otaStartupContainer(ota.Desired)
	file, err := coreMQTTFile(ota.Desired, c, "platform-service-identity", "client.crt")
	if err != nil || file.kind != "Secret" {
		return fail("original OTA public registration identity mapping is unavailable")
	}
	identityID := "Secret/" + p.Stack + "-video-cloud/" + file.reference
	identity, err := readOriginal(identityID)
	if err != nil {
		return err
	}
	encoded := deploymentplan.String(deploymentplan.Map(identity["data"])[file.key])
	if encoded != deploymentplan.Map(resources[identityID].Baseline["data"])[file.key] {
		return fail("original OTA public registration identity changed")
	}
	certBytes, err := base64.StdEncoding.DecodeString(encoded)
	chain, certErr := pemCertificates(certBytes)
	if err != nil || len(certBytes) > 1<<20 || certErr != nil || len(chain) < 2 || chain[0].Subject.CommonName != "service:ota" || chain[0].CheckSignatureFrom(chain[1]) != nil {
		return fail("original OTA public certificate cannot identify its approved Service role and issuer")
	}
	issuer := sha256.Sum256(chain[1].Raw)
	instance := mqttFoundationEnv(c, "VIDEO_CLOUD_OTA_SERVICE_INSTANCE_ID")
	if !otaRegistrationInstancePattern.MatchString(instance) || instance != mqttFoundationEnv(otaStartupContainer(ota.Baseline), "VIDEO_CLOUD_OTA_SERVICE_INSTANCE_ID") {
		return fail("candidate OTA does not retain its original registration instance")
	}
	// Resolve the original AM database through its retained runtime Secret,
	// without copying the credential into a query or using another environment.
	amID := "Deployment/" + p.Stack + "-account-manager/account-manager"
	if _, err := readOriginal("Service/" + p.Stack + "-account-manager/account-manager"); err != nil {
		return err
	}
	am, err := readOriginal(amID)
	if err != nil {
		return err
	}
	var amApp deploymentplan.Object
	for _, raw := range certIssuerObjectList(environmentPKIPod(am)["containers"]) {
		if cc := deploymentplan.Map(raw); cc["name"] == "app" {
			amApp = cc
		}
	}
	amSettings, settingsErr := environmentPKINamedFields(amApp["env"])
	if amApp == nil || settingsErr != nil || amSettings["DATABASE_URL"] != nil {
		return fail("original AM database credential reference is unavailable or ambiguous")
	}
	var candidateAM deploymentplan.Object
	for _, raw := range certIssuerObjectList(environmentPKIPod(resources[amID].Desired)["containers"]) {
		if cc := deploymentplan.Map(raw); cc["name"] == "app" {
			candidateAM = cc
		}
	}
	candidateSettings, settingsErr := environmentPKINamedFields(candidateAM["env"])
	if candidateAM == nil || settingsErr != nil || candidateSettings["DATABASE_URL"] != nil || deploymentplan.Digest(candidateAM["envFrom"]) != deploymentplan.Digest(amApp["envFrom"]) {
		return fail("candidate AM does not retain the original registration database credential reference")
	}
	serverFile, err := coreMQTTFile(am, amApp, "account-manager-service-registration-tls", "tls.crt")
	if err != nil || serverFile.kind != "Secret" || serverFile.file != mqttFoundationEnv(amApp, "ACCOUNT_MANAGER_SERVICE_REGISTRATION_SERVER_CERT") {
		return fail("original AM registration server certificate mapping is unavailable")
	}
	listenerID := "Secret/" + p.Stack + "-account-manager/" + serverFile.reference
	listener, err := readOriginal(listenerID)
	if err != nil {
		return err
	}
	if deploymentplan.Map(listener["data"])[serverFile.key] != deploymentplan.Map(resources[listenerID].Baseline["data"])[serverFile.key] {
		return fail("original AM registration server certificate changed")
	}
	serverBytes, err := kubernetesSecretBytes(listener, serverFile.key)
	serverChain, certErr := pemCertificates(serverBytes)
	registrationURL, _ := url.Parse(mqttFoundationEnv(c, "VIDEO_CLOUD_OTA_SERVICE_REGISTRATION_URL"))
	if err != nil || certErr != nil || len(serverBytes) > 1<<20 || len(serverChain) == 0 || serverChain[0].VerifyHostname(registrationURL.Hostname()) != nil {
		return fail("candidate OTA registration hostname is not an alias authorized by the original AM server certificate")
	}
	refs := certIssuerObjectList(amApp["envFrom"])
	if len(refs) != 1 || deploymentplan.Map(refs[0])["configMapRef"] != nil {
		return fail("original AM database credential reference is unavailable or ambiguous")
	}
	ref := deploymentplan.Map(deploymentplan.Map(refs[0])["secretRef"])
	if ref["optional"] == true || deploymentplan.String(ref["name"]) == "" {
		return fail("original AM database credential reference is unavailable or ambiguous")
	}
	runtime, err := readOriginal("Secret/" + p.Stack + "-account-manager/" + deploymentplan.String(ref["name"]))
	if err != nil {
		return err
	}
	dsn, err := kubernetesSecretBytes(runtime, "DATABASE_URL")
	u, urlErr := url.Parse(string(dsn))
	host := "postgresql." + p.Stack + "-platform.svc"
	if err != nil || urlErr != nil || u.User == nil || u.User.Username() != "postgres" || (u.Scheme != "postgres" && u.Scheme != "postgresql") || (u.Hostname() != host && u.Hostname() != host+".cluster.local") || u.Port() != "5432" || u.Path != "/rtk_account_manager" {
		return fail("original AM credential does not identify the selected original PostgreSQL database")
	}
	// The bounded native query uses the original database's default public
	// schema. It cannot qualify an AM DSN that redirects schema/session state.
	queryOptions, queryErr := url.ParseQuery(u.RawQuery)
	if queryErr != nil || u.Fragment != "" {
		return fail("original AM database configuration has invalid or unsupported schema/session overrides")
	}
	for key := range queryOptions {
		if key != "sslmode" {
			return fail("original AM database configuration has unsupported schema/session overrides")
		}
	}
	ns := p.Stack + "-platform"
	ownerID := "StatefulSet/" + ns + "/postgresql"
	owner, err := readOriginal(ownerID)
	if err != nil {
		return err
	}
	podID := "Pod/" + ns + "/postgresql-0"
	pod, err := read(ctx, podID)
	if err != nil || pod == nil || !identityPermissionOwnedBy(deploymentplan.Map(pod["metadata"]), "StatefulSet", deploymentplan.String(deploymentplan.Map(owner["metadata"])["uid"])) || deploymentplan.Map(pod["metadata"])["deletionTimestamp"] != nil {
		return fail("original PostgreSQL registration query target cannot be verified")
	}
	queryCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	manifest := release.Images["LKE_VIDEO_CLOUD_IMAGE"].OTARegistration
	raw, err := command(queryCtx, "--namespace", ns, "exec", "postgresql-0", "--", "env", "PGOPTIONS=-c statement_timeout=10000", "psql", "-U", "postgres", "-d", "rtk_account_manager", "-X", "-qAt", "-v", "ON_ERROR_STOP=1", "-c", otaRegistrationQuery(p.Environment, manifest.ManifestVersion, chain[0].Subject.CommonName, hex.EncodeToString(issuer[:])))
	if err != nil {
		return fail(secretCheckFailure(err, "original AM registration prerequisites cannot be read").Error())
	}
	var state struct {
		Schema      string                         `json:"schema"`
		Candidate   *otaRegistrationStoredManifest `json:"candidate"`
		Publication *struct {
			Version  string                         `json:"published_version"`
			Status   string                         `json:"status"`
			Manifest *otaRegistrationStoredManifest `json:"manifest"`
		} `json:"publication"`
		Workload *struct {
			Service  string   `json:"service_id"`
			Instance string   `json:"instance_id"`
			Allowed  []string `json:"allowed_option_codes"`
			Revoked  bool     `json:"revoked"`
		} `json:"workload"`
		MQTTDefined *bool `json:"mqtt_defined"`
		Conflict    *bool `json:"ota_option_conflict"`
	}
	if len(raw) > 2<<20 || decodeEnvironmentJSON(raw, &state) != nil || state.Schema != "public" || state.MQTTDefined == nil || state.Conflict == nil {
		return fail("original AM registration prerequisite response is invalid")
	}
	if state.Workload == nil || state.Workload.Revoked || state.Workload.Service != "ota" || state.Workload.Instance != instance || !slices.Contains(state.Workload.Allowed, "ota") {
		return fail("original OTA Service certificate and instance lack an applicable unrevoked AM workload approval")
	}
	if state.Candidate != nil && (!state.Candidate.validDigest() || state.Candidate.Version != manifest.ManifestVersion || state.Candidate.Digest != manifest.digest()) {
		return fail("candidate exact-image OTA manifest conflicts with the existing immutable AM definition for the same version")
	}
	if state.Publication != nil && (state.Publication.Manifest == nil || state.Publication.Version != state.Publication.Manifest.Version || !state.Publication.Manifest.validDigest() || !keySet("active", "suspended", "retired")[state.Publication.Status]) {
		return fail("original OTA published definition or status is invalid")
	}
	if state.Candidate == nil && (!*state.MQTTDefined || *state.Conflict) {
		return fail("new candidate OTA definition conflicts with the original published option ownership or lacks its existing MQTT definition")
	}
	after, err := read(ctx, podID)
	if err != nil || after == nil || deploymentplan.Map(after["metadata"])["uid"] != deploymentplan.Map(pod["metadata"])["uid"] || deploymentplan.Map(after["metadata"])["deletionTimestamp"] != nil {
		return fail("original PostgreSQL registration query target changed during qualification")
	}
	return nil
}
