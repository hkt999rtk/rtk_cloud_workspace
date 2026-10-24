package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedLateLogNativeContractFixture(t *testing.T) (*createLogACKHelperDraft, *createManagedLateSessionRecovery) {
	t.Helper()
	a, _, _, state, now := managedLateLogTerminalFixture(t)
	pin := strings.Repeat("e", 64)
	a.SourceJournalSHA, a.SourceOwnerSHA, a.OriginalPlanSHA, a.OriginalJournalSHA = pin, pin, pin, pin
	a.NewSessionAuthoritySHA, a.OriginalServersSHA, a.OriginalVolumesSHA = pin, pin, pin
	a.SourcePodUID = "55555555-5555-4555-8555-555555555555"
	a.OriginalClaimUID = "77777777-7777-4777-8777-777777777777"
	a.OriginalServiceIssuer = state.Issuer
	container := managedNamedField(environmentPKIPod(a.Original.Desired)["containers"], "app")
	container["command"] = []any{"/app/logingester"}
	container["env"] = environmentCreateEnv(map[string]string{
		"VIDEO_CLOUD_LOG_INGESTER_MQTT_IDENTITY_STATE":       logACKStatePath,
		"VIDEO_CLOUD_LOG_INGESTER_MQTT_IDENTITY_ROOT_SHA256": a.Identity.RootSHA256,
		"PKI_BOOTSTRAP_SESSION_ID":                           a.OriginalSession,
	})
	// Match the actual maintained business template: its original selector and
	// stack exist, while the separately qualified helper peer label is absent.
	labels := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(a.Original.Desired["spec"])["template"])["metadata"])
	delete(deploymentplan.Map(labels["labels"]), "rtk.cloud/pki-consumer")
	leaf, err := parseFirstCertificatePEM([]byte(a.LeafPEM))
	if err != nil {
		t.Fatal(err)
	}
	prospective := *a
	prospective.NewSession = managedSessionState{Session: state.Session, Issuer: state.Issuer, Root: state.Root, Status: "qualified-not-issued", Subjects: state.Subjects}
	late := &createManagedLateSessionRecovery{ParentSHA: a.Parent, SourceSHA: a.SourceJournalSHA, SourceOwnerSHA: a.SourceOwnerSHA, HelperBindingSHA: a.Review, OriginalSession: a.OriginalSession, NewSessionID: state.Session, OriginalClientsSHA: a.OriginalClientsSHA, LogNotDueUntil: leaf.NotBefore.Add(leaf.NotAfter.Sub(leaf.NotBefore) * 2 / 3)}
	if err := validateLogACKDraftSource(a, state.Subjects, now); err != nil {
		t.Fatal("current native source fixture", err)
	}
	if err := validateManagedLateLogProducer(late, &prospective, now); err != nil {
		t.Fatal("prospective original native contract fixture", err)
	}
	return a, late
}

func TestManagedLateLogUsesMaintainedIdentityRootEnvironmentContract(t *testing.T) {
	a, late := managedLateLogNativeContractFixture(t)
	source, err := os.ReadFile(filepath.Join("..", "..", "..", "repos", "rtk_video_cloud", "internal", "config", "config.go"))
	if err != nil || !strings.Contains(string(source), `MQTTIdentityRootSHA256:               os.Getenv("VIDEO_CLOUD_LOG_INGESTER_MQTT_IDENTITY_ROOT_SHA256")`) {
		t.Fatal("independent maintained native getter differs", err)
	}
	const nativeRoot = "VIDEO_CLOUD_LOG_INGESTER_MQTT_IDENTITY_ROOT_SHA256"
	const unmaintainedRoot = "VIDEO_CLOUD_LOG_INGESTER_MQTT_ROOT_SHA256"
	if strings.Count(logACKDraftWatchdog, "${"+nativeRoot+":?}") != 2 || strings.Contains(logACKDraftWatchdog, unmaintainedRoot) {
		t.Fatal("before/after native inspection does not use the actual original root variable")
	}
	before := deploymentplan.Digest(a.Original.Desired)
	helper, err := renderLogACKDraftHelper(a)
	if err != nil || deploymentplan.Digest(a.Original.Desired) != before {
		t.Fatal("helper changed original native contract", err)
	}
	container := managedNamedField(environmentPKIPod(helper.Object)["containers"], "app")
	if mqttFoundationEnv(container, nativeRoot) != a.Identity.RootSHA256 || mqttFoundationEnv(container, unmaintainedRoot) != "" {
		t.Fatal("helper changed the original root environment instead of consuming it")
	}
	for _, mode := range []string{"missing actual field", "wrong actual root", "only unmaintained alias"} {
		t.Run(mode, func(t *testing.T) {
			changed := *a
			changed.Original.Desired = deploymentplan.Clone(a.Original.Desired)
			c := managedNamedField(environmentPKIPod(changed.Original.Desired)["containers"], "app")
			switch mode {
			case "missing actual field":
				c["env"] = environmentCreateServiceWithoutEnv(c["env"], nativeRoot)
			case "wrong actual root":
				c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{nativeRoot: strings.Repeat("f", 64)}))
			case "only unmaintained alias":
				c["env"] = environmentCreateServiceWithoutEnv(c["env"], nativeRoot)
				c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{unmaintainedRoot: a.Identity.RootSHA256}))
			}
			if validateLogACKDraftSource(&changed, changed.NewSession.Subjects, time.Now().UTC()) == nil {
				t.Fatal("invalid current native root contract accepted")
			}
			changed.NewSession.Status = "qualified-not-issued"
			changed.NewSession.Expires = time.Time{}
			changed.NewSession.Fingerprint = ""
			changed.NewSession.Installed = nil
			if validateManagedLateLogProducer(late, &changed, time.Now().UTC()) == nil {
				t.Fatal("invalid prospective native root contract accepted")
			}
		})
	}
}

func TestManagedLateLogFixedFailureReasonRemainsTypedPreflightBlocker(t *testing.T) {
	err := logACKDraftBlock("original image/settings")
	var blocker *deploymentplan.Blocker
	if !errors.As(err, &blocker) || blocker.ID != "deployment.bootstrap.pki" || blocker.Cause != "original installed Log ACK helper requires exact qualified source: original image/settings" || blocker.Next == "" {
		t.Fatal("fixed safe failure reason would disappear from public preflight", err)
	}
}
