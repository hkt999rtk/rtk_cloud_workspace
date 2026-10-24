package main

// Log acknowledgment recovery preserves the original source journal and
// validates distinct new-session authority after the prior Core execution closes.
// No prospective session or synthetic native ACK is accepted here.

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const (
	logACKNativeImage = "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:1e9b9820f619a39e4686f1f9260f3422b7d5c239e6a4dbfd8101e488d2472244"
	logACKStatePath   = "/var/lib/video-cloud-log-service/private/client.json"
	logACKSubject     = "service:video-cloud-logingester"
	logACKTTL         = 600 * time.Second
	logACKGrace       = 30 * time.Second
	logACKDueMargin   = time.Minute
)

type logACKNativeIdentity struct {
	Subject         string `json:"subject"`
	Fingerprint     string `json:"fingerprint"`
	PublicKeySHA256 string `json:"public_key_sha256"`
	RootSHA256      string `json:"root_sha256"`
	StateSHA256     string `json:"state_sha256"`
	Pending         bool   `json:"pending"`
}

type createLogACKHelperDraft struct {
	Parent, Review, SourceJournalSHA, SourceOwnerSHA string
	FreshReview                                      string `json:"FreshReview,omitempty"` // runtime copy only; never rewrite the admitted producer
	OriginalPlanSHA, OriginalJournalSHA              string
	Original                                         deploymentplan.Resource
	SourcePodUID, OriginalClaimUID                   string
	OriginalSession                                  string
	OriginalServiceIssuer                            string
	NewSessionAuthoritySHA                           string
	NewSession                                       managedSessionState
	Identity                                         logACKNativeIdentity
	// Public registry certificate only; never a copied private owner state.
	LeafPEM                                     string
	OriginalClientsSHA, OriginalServersSHA      string
	OriginalVolumesSHA, OriginalPrivateBytesSHA string
}

func logACKDraftBlock(reason string) error {
	return environmentCreateBootstrapBlocker("original installed Log ACK helper requires exact qualified source: " + reason)
}

func (a *createLogACKHelperDraft) helperName() string {
	return "pki-log-installed-ack-" + a.Review[:12]
}

// Qualification and every execution boundary call this on fresh public native
// rows. The helper's entire process lifetime includes its watchdog kill grace.
// The executor must separately authenticate the complete original eleven-client
// registry inventory, original key/cert/CSR/state bytes, volumes and old e0 row.
func validateLogACKDraftSource(a *createLogACKHelperDraft, subjects []string, now time.Time) error {
	if a == nil || !environmentPreflightDigest.MatchString(a.Parent) || !environmentPreflightDigest.MatchString(a.Review) {
		return logACKDraftBlock("parent/review")
	}
	for _, hash := range []string{a.SourceJournalSHA, a.SourceOwnerSHA, a.OriginalPlanSHA, a.OriginalJournalSHA, a.NewSessionAuthoritySHA, a.OriginalClientsSHA, a.OriginalServersSHA, a.OriginalVolumesSHA, a.OriginalPrivateBytesSHA, a.Identity.Fingerprint, a.Identity.PublicKeySHA256, a.Identity.RootSHA256, a.Identity.StateSHA256} {
		if !environmentPreflightDigest.MatchString(hash) {
			return logACKDraftBlock("source/material digests")
		}
	}
	if a.Original.ID != "Deployment/video-cloud-dev-video-cloud/video-cloud-logingester" || !environmentBootstrapOwnerUUID.MatchString(a.Original.UID) || !environmentBootstrapOwnerUUID.MatchString(a.SourcePodUID) || !environmentBootstrapOwnerUUID.MatchString(a.OriginalClaimUID) || !environmentBootstrapOwnerUUID.MatchString(a.OriginalSession) || !environmentBootstrapOwnerUUID.MatchString(a.NewSession.Session) || a.NewSession.Session == a.OriginalSession {
		return logACKDraftBlock("original owner/distinct actual session")
	}
	if !environmentBootstrapOwnerUUID.MatchString(a.OriginalServiceIssuer) || a.NewSession.Issuer != a.OriginalServiceIssuer || a.NewSession.Status != "active" || a.NewSession.Root != a.Identity.RootSHA256 || a.NewSession.OtherActive != 0 || !slices.Equal(a.NewSession.Subjects, subjects) || !a.NewSession.Expires.After(now.Add(logACKTTL+logACKGrace+logACKDueMargin)) {
		return logACKDraftBlock("genuine new live session")
	}
	seen := map[string]bool{}
	for _, subject := range a.NewSession.Installed {
		if seen[subject] || !slices.Contains(subjects, subject) {
			return logACKDraftBlock("native installation set")
		}
		seen[subject] = true
	}
	if a.Identity.Subject != logACKSubject || a.Identity.Pending {
		return logACKDraftBlock("original current/no pending")
	}
	leaf, err := parseFirstCertificatePEM([]byte(a.LeafPEM))
	if err != nil || leaf.Subject.CommonName != logACKSubject || leaf.IsCA || leaf.KeyUsage != x509.KeyUsageDigitalSignature || !slices.Equal(leaf.ExtKeyUsage, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}) || environmentPreflightBytesSHA256(leaf.Raw) != a.Identity.Fingerprint || leaf.NotBefore.After(now) || !leaf.NotAfter.After(now) {
		return logACKDraftBlock("original signed current leaf")
	}
	spki := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
	if hex.EncodeToString(spki[:]) != a.Identity.PublicKeySHA256 {
		return logACKDraftBlock("original public key")
	}
	due := leaf.NotBefore.Add(leaf.NotAfter.Sub(leaf.NotBefore) * 2 / 3)
	if !due.After(now.Add(logACKTTL + logACKGrace + logACKDueMargin)) {
		return logACKDraftBlock("current leaf due before entire enforced helper lifetime")
	}
	pod := environmentPKIPod(a.Original.Desired)
	containers, err := environmentPKINamedFields(pod["containers"])
	if err != nil || len(containers) != 1 || containers["app"] == nil {
		return logACKDraftBlock("one original native owner")
	}
	c := containers["app"]
	if c["image"] != logACKNativeImage || deploymentplan.Digest(c["command"]) != deploymentplan.Digest([]any{"/app/logingester"}) || len(certIssuerObjectList(c["args"])) != 0 || mqttFoundationEnv(c, "VIDEO_CLOUD_LOG_INGESTER_MQTT_IDENTITY_STATE") != logACKStatePath || mqttFoundationEnv(c, "VIDEO_CLOUD_LOG_INGESTER_MQTT_IDENTITY_ROOT_SHA256") != a.Identity.RootSHA256 || mqttFoundationEnv(c, "PKI_BOOTSTRAP_SESSION_ID") != a.OriginalSession || a.OriginalPrivateBytesSHA != a.Identity.StateSHA256 {
		return logACKDraftBlock("original image/settings")
	}
	for _, suffix := range []string{"BOOTSTRAP_CERT", "BOOTSTRAP_KEY", "TRANSITION_FROM_STATE", "TRANSITION_FROM_ROOT_SHA256"} {
		if mqttFoundationEnv(c, "VIDEO_CLOUD_LOG_INGESTER_MQTT_IDENTITY_"+suffix) != "" {
			return logACKDraftBlock("enrollment/transition forbidden")
		}
	}
	// Source guard must additionally bind exact complete env/mount/CM/PVC
	// descriptors from the authenticated accepted original plan. No environment
	// values, path substitutions or extra private volumes are synthesized.
	return nil
}

// Qualify both exact published image capabilities before mutation:
// `/usr/bin/timeout --version`, `/bin/bash --version`; no fallback.
// The outer watchdog bounds the entire shell/process group. This one-shot
// wrapper tests real native HTTP readiness, TERM-stops the provider and waits
// for its clean exit. No HUP, renewal signal, synthetic ACK or identity input.
const logACKDraftWatchdog = `/app/logingester --deployment-bootstrap-trust &
native_pid=$!
cleanup() {
  trap - EXIT TERM INT
  kill -TERM "$native_pid" 2>/dev/null || true
  wait "$native_pid" 2>/dev/null || true
}
trap cleanup EXIT
trap 'exit 1' TERM INT
while kill -0 "$native_pid" 2>/dev/null; do
  statusline=''
  if exec 3<>/dev/tcp/127.0.0.1/19310; then
    printf 'GET /readyz HTTP/1.0\r\nHost: localhost\r\nConnection: close\r\n\r\n' >&3
    IFS= read -r -t 2 statusline <&3 || true
    exec 3<&- 3>&-
    case "$statusline" in
      'HTTP/1.0 200 '*|'HTTP/1.1 200 '*)
        inspected_before=$(/app/serviceidentity-bootstrap inspect /var/lib/video-cloud-log-service/private/client.json service:video-cloud-logingester "${VIDEO_CLOUD_LOG_INGESTER_MQTT_IDENTITY_ROOT_SHA256:?}")
        kill -TERM "$native_pid"
        wait "$native_pid"
        trap - EXIT TERM INT
        inspected_after=$(/app/serviceidentity-bootstrap inspect /var/lib/video-cloud-log-service/private/client.json service:video-cloud-logingester "${VIDEO_CLOUD_LOG_INGESTER_MQTT_IDENTITY_ROOT_SHA256:?}")
        test "$inspected_before" = "$inspected_after"
        printf 'RTK_LOG_ACK_PUBLIC_STATE %s\n' "$inspected_after"
        printf '%s\n' 'RTK_LOG_ACK_OWNER_RELEASED'
        exit 0
        ;;
    esac
  fi
  sleep 1
done
exit 1`

func renderLogACKDraftHelper(a *createLogACKHelperDraft) (deploymentplan.Definition, error) {
	if a == nil || !environmentPreflightDigest.MatchString(a.Review) || a.NewSession.Session == a.OriginalSession {
		return deploymentplan.Definition{}, logACKDraftBlock("qualified producer")
	}
	pod := deploymentplan.Clone(environmentPKIPod(a.Original.Desired))
	containers, err := environmentPKINamedFields(pod["containers"])
	if err != nil || len(containers) != 1 || containers["app"] == nil {
		return deploymentplan.Definition{}, logACKDraftBlock("original helper pod")
	}
	c := containers["app"]
	c["command"] = []any{"/usr/bin/timeout", "--signal=TERM", "--kill-after=30s", "600s", "/bin/bash", "-ec", logACKDraftWatchdog}
	delete(c, "args")
	c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_SESSION_ID": a.NewSession.Session}))
	c["ports"] = []any{deploymentplan.Object{"name": "prepared", "containerPort": float64(19310)}}
	c["readinessProbe"] = deploymentplan.Object{"httpGet": deploymentplan.Object{"path": "/readyz", "port": "prepared"}, "initialDelaySeconds": float64(1), "periodSeconds": float64(2), "timeoutSeconds": float64(2), "failureThreshold": float64(3)}
	delete(c, "livenessProbe")
	delete(c, "startupProbe")
	pod["restartPolicy"] = "Never"
	pod["terminationGracePeriodSeconds"] = float64(30)
	// The qualified exclusive helper uses the maintained Controller peer label.
	// Authenticate original Log labels without claiming that this helper label
	// existed on its business Pod; keep the helper outside its Service selector.
	metadata := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(a.Original.Desired["spec"])["template"])["metadata"])
	sourceLabels := deploymentplan.Map(metadata["labels"])
	stack := deploymentplan.String(deploymentplan.Map(deploymentplan.Map(a.Original.Desired["metadata"])["labels"])["rtk.realtek.com/stack"])
	consumer, declared := sourceLabels["rtk.cloud/pki-consumer"]
	if a.Original.ID != "Deployment/video-cloud-dev-video-cloud/video-cloud-logingester" || deploymentplan.ID(a.Original.Desired) != a.Original.ID || !environmentBootstrapOwnerUUID.MatchString(a.Original.UID) || stack != "video-cloud-dev" || sourceLabels["rtk.realtek.com/stack"] != stack || sourceLabels["app.kubernetes.io/name"] != "video-cloud-logingester" || declared && consumer != "true" {
		return deploymentplan.Definition{}, logACKDraftBlock("original Controller network admission labels")
	}
	labels := deploymentplan.Object{"rtk.realtek.com/log-installed-ack": a.Review[:12], "rtk.cloud/pki-consumer": "true", "rtk.realtek.com/stack": stack}
	object := deploymentplan.Object{"apiVersion": "batch/v1", "kind": "Job", "metadata": deploymentplan.Object{"name": a.helperName(), "namespace": "video-cloud-dev-video-cloud", "labels": labels}, "spec": deploymentplan.Object{"backoffLimit": float64(0), "activeDeadlineSeconds": float64(600), "template": deploymentplan.Object{"metadata": deploymentplan.Object{"labels": labels}, "spec": pod}}}
	return deploymentplan.Definition{Phase: "bootstrap", Object: object}, nil
}

func renderLogACKDraftQuiesce(a *createLogACKHelperDraft) deploymentplan.Definition {
	object := deploymentplan.Clone(a.Original.Desired)
	deploymentplan.Map(object["spec"])["replicas"] = float64(0)
	return deploymentplan.Definition{Phase: "bootstrap", Object: object}
}

func renderLogACKDraftRestore(a *createLogACKHelperDraft) deploymentplan.Definition {
	object := deploymentplan.Clone(a.Original.Desired)
	c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(object)["containers"])[0])
	c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_SESSION_ID": a.NewSession.Session}))
	return deploymentplan.Definition{Phase: "workload", Object: object}
}

// Inspection is the published native read-only command: no owner acquisition,
// directory creation, key output, CSR, renewal or enrollment. Decode only its
// public record; never emit raw stdout on a decode/command error.
func readLogACKDraftIdentity(ctx context.Context, b environmentKubernetesBackend, podName, root string) (logACKNativeIdentity, error) {
	var identity logACKNativeIdentity
	if podName == "" || strings.ContainsAny(podName, " /\\\n\r") || !environmentPreflightDigest.MatchString(root) {
		return identity, logACKDraftBlock("native inspector arguments")
	}
	raw, err := b.command(ctx, nil, "--namespace", "video-cloud-dev-video-cloud", "exec", podName, "--container", "app", "--", "/app/serviceidentity-bootstrap", "inspect", logACKStatePath, logACKSubject, root)
	if err != nil {
		return identity, logACKDraftBlock("native original state unavailable")
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&identity) != nil || identity.Pending || identity.Subject != logACKSubject || identity.RootSHA256 != root {
		return identity, logACKDraftBlock("native inspector proof")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return identity, logACKDraftBlock("native inspector trailing content")
	}
	return identity, nil
}

func verifyLogACKDraftIdentity(got, want logACKNativeIdentity) error {
	if got.Pending || deploymentplan.Digest(got) != deploymentplan.Digest(want) {
		return logACKDraftBlock("original identity bytes/fingerprint changed")
	}
	return nil
}

// Reader-only boundary: expected Job/Pod UIDs come from the actual accepted
// child owner receipt and observed native Pod proof, never a prospective UUID.
// PrivateBytesSHA is produced by an authenticated digest-only owner inspection;
// no private bytes enter this proof or get copied out of their original PVC.
func verifyLogACKDraftCompleted(a *createLogACKHelperDraft, expectedJobUID, expectedPodUID string, job deploymentplan.Object, pods []deploymentplan.Object, state managedSessionState, identity logACKNativeIdentity, clientsSHA, privateBytesSHA string, now time.Time) (deploymentplan.Object, error) {
	if a == nil || !environmentBootstrapOwnerUUID.MatchString(expectedJobUID) || !environmentBootstrapOwnerUUID.MatchString(expectedPodUID) || len(pods) != 1 || clientsSHA != a.OriginalClientsSHA || privateBytesSHA != a.OriginalPrivateBytesSHA {
		return nil, logACKDraftBlock("actual accepted helper/unchanged native eleven identities")
	}
	definition, err := renderLogACKDraftHelper(a)
	if err != nil || job["kind"] != "Job" || deploymentplan.Map(job["metadata"])["uid"] != expectedJobUID || !deploymentplan.Matches(definition.Object, job) {
		return nil, logACKDraftBlock("accepted exact helper Job")
	}
	status := deploymentplan.Map(job["status"])
	complete := false
	for _, value := range certIssuerObjectList(status["conditions"]) {
		condition := deploymentplan.Map(value)
		if condition["type"] == "Complete" && condition["status"] == "True" {
			complete = true
		}
		if condition["type"] == "Failed" && condition["status"] == "True" {
			return nil, logACKDraftBlock("helper failed/watchdog deadline")
		}
	}
	if !complete || status["succeeded"] != float64(1) || status["active"] != nil && status["active"] != float64(0) || status["failed"] != nil && status["failed"] != float64(0) {
		return nil, logACKDraftBlock("one genuine completed helper process")
	}
	pod := pods[0]
	meta := deploymentplan.Map(pod["metadata"])
	if meta["uid"] != expectedPodUID || meta["deletionTimestamp"] != nil || len(certIssuerObjectList(meta["ownerReferences"])) != 1 {
		return nil, logACKDraftBlock("one exact helper descendant")
	}
	owner := deploymentplan.Map(certIssuerObjectList(meta["ownerReferences"])[0])
	if owner["kind"] != "Job" || owner["uid"] != expectedJobUID || owner["name"] != a.helperName() || owner["controller"] != true {
		return nil, logACKDraftBlock("helper Pod owner")
	}
	wantPod := environmentPKIPod(definition.Object)
	actualPod := deploymentplan.Map(pod["spec"])
	if len(certIssuerObjectList(actualPod["containers"])) != len(certIssuerObjectList(wantPod["containers"])) || len(certIssuerObjectList(actualPod["initContainers"])) != len(certIssuerObjectList(wantPod["initContainers"])) || !deploymentplan.Matches(deploymentplan.Object{"kind": "Pod", "spec": wantPod}, deploymentplan.Object{"kind": "Pod", "spec": actualPod}) {
		return nil, logACKDraftBlock("helper exact normalized Pod spec")
	}
	podStatus := deploymentplan.Map(pod["status"])
	containerStatus := certIssuerObjectList(podStatus["containerStatuses"])
	if podStatus["phase"] != "Succeeded" || len(containerStatus) != 1 {
		return nil, logACKDraftBlock("helper native process stopped")
	}
	cs := deploymentplan.Map(containerStatus[0])
	terminated := deploymentplan.Map(deploymentplan.Map(cs["state"])["terminated"])
	if cs["name"] != "app" || cs["restartCount"] != float64(0) || terminated["exitCode"] != float64(0) || terminated["reason"] != "Completed" || terminated["startedAt"] == nil || terminated["finishedAt"] == nil {
		return nil, logACKDraftBlock("helper restarted or did not cleanly release its owner")
	}
	start, startErr := time.Parse(time.RFC3339Nano, deploymentplan.String(terminated["startedAt"]))
	finish, finishErr := time.Parse(time.RFC3339Nano, deploymentplan.String(terminated["finishedAt"]))
	leaf, leafErr := parseFirstCertificatePEM([]byte(a.LeafPEM))
	if startErr != nil || finishErr != nil || leafErr != nil || !finish.After(start) || finish.Sub(start) > logACKTTL+logACKGrace || finish.After(now) || !leaf.NotBefore.Add(leaf.NotAfter.Sub(leaf.NotBefore)*2/3).After(start.Add(logACKTTL+logACKGrace+logACKDueMargin)) {
		return nil, logACKDraftBlock("actual process lifetime/not-due margin")
	}
	if verifyLogACKDraftIdentity(identity, a.Identity) != nil || state.Session != a.NewSession.Session || state.Issuer != a.NewSession.Issuer || state.Root != a.NewSession.Root || state.Fingerprint != a.NewSession.Fingerprint || state.Status != "active" || !state.Expires.Equal(a.NewSession.Expires) || !state.Expires.After(now) || state.OtherActive != 0 || !slices.Equal(state.Subjects, a.NewSession.Subjects) || !slices.Contains(state.Installed, logACKSubject) {
		return nil, logACKDraftBlock("genuine distinct-session Log ACK/current original identity")
	}
	seen := map[string]bool{}
	for _, subject := range state.Installed {
		if seen[subject] || !slices.Contains(state.Subjects, subject) {
			return nil, logACKDraftBlock("native installation set changed")
		}
		seen[subject] = true
	}
	for _, subject := range a.NewSession.Installed {
		if !seen[subject] {
			return nil, logACKDraftBlock("native installation regressed")
		}
	}
	if !environmentPreflightDigest.MatchString(a.FreshReview) {
		return nil, logACKDraftBlock("actual fresh review")
	}
	return deploymentplan.Object{"schema": "rtk-cloud.create-log-installed-ack-proof/v1", "parent_plan_sha256": a.Parent, "review_sha256": a.FreshReview, "helper_binding_sha256": a.Review, "session_id": state.Session, "subject": logACKSubject, "original_deployment_uid": a.Original.UID, "helper_job_uid": expectedJobUID, "helper_pod_uid": expectedPodUID, "native_started_at": start.Format(time.RFC3339Nano), "native_finished_at": finish.Format(time.RFC3339Nano), "native_session_sha256": deploymentplan.Digest(state), "native_clients_sha256": clientsSHA, "private_bytes_sha256": privateBytesSHA, "identity_sha256": deploymentplan.Digest(identity)}, nil
}

func readLogACKDraftMarkers(raw []byte) (logACKNativeIdentity, error) {
	var identity logACKNativeIdentity
	if len(raw) == 0 || len(raw) > 256<<10 {
		return identity, logACKDraftBlock("bounded actual helper logs")
	}
	inspected, released := false, false
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "RTK_LOG_ACK_PUBLIC_STATE ") {
			if inspected || released || decodeEnvironmentJSON([]byte(strings.TrimPrefix(line, "RTK_LOG_ACK_PUBLIC_STATE ")), &identity) != nil {
				return identity, logACKDraftBlock("exact native public marker")
			}
			inspected = true
		} else if line == "RTK_LOG_ACK_OWNER_RELEASED" {
			if !inspected || released {
				return identity, logACKDraftBlock("exact clean owner-release marker")
			}
			released = true
		} else if strings.HasPrefix(line, "RTK_LOG_ACK_") {
			return identity, logACKDraftBlock("unknown helper proof marker")
		}
	}
	if !inspected || !released || identity.Pending || identity.Subject != logACKSubject {
		return identity, logACKDraftBlock("native helper did not prove installed state and release")
	}
	return identity, nil
}

// Source-specific journal integration is intentionally not invented here.
// Required sequence, each bound to an admitted review and per-resource receipt:
// 1. Authenticate actual post-Core accepted source + genuine distinct session.
// 2. Recheck exact original Deployment UID/body/Pod/PVC/11 clients/private bytes.
// 3. Apply only replicas=0 to original UID; wait ALL original-owned descendants
//    gone, including terminating Pods. Reject any foreign holder of the PVC.
// 4. Apply the reviewed one-helper producer; native .owner LOCK_NB must succeed.
// 5. Require helper owned UID/Pod exact normalized Kind=Pod spec, native HTTP
//    /readyz:19310 observed by its reviewed wrapper, genuine NEW session Log ACK,
//    terminal Job success/native exit0 and unchanged native11 + identity bytes.
// 6. Retire ONLY the helper's accepted UID, using the common owner-qualified
//    deletion journal. Wait helper Job/Pods/process all gone; a timed
//    out or uncertain deletion is a stopped recovery, NOT business restoration.
// 7. Recheck unchanged private/native11 state from qualified original claim and
//    restored owner. Apply original business body with ONLY global new SID delta.
// 8. Require original Deployment UID, restored business Ready, unchanged native11,
//    complete NEW-session native9 ACK set, then genuine finalization/cleanup.
// Resume accepts only exact admitted intent + actual accepted child receipts.
// A helper restart/extra Pod is not a successful proof and must not reset TTL.
