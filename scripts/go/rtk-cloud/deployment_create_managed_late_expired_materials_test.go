package main

import (
	"encoding/json"
	"maps"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedLateExpiredMaterialFixture(t *testing.T) (*createServiceSessionRecovery, environmentReleaseImage, string, map[string]string) {
	t.Helper()
	_, _, late, historical := managedLateQualifiedPreviewsFixture(t)
	var prior createManagedLateSessionRecovery
	if json.Unmarshal(mustProviderRecoveryJSON(late), &prior) != nil {
		t.Fatal("isolated original descriptor")
	}
	late.SourceSHA = strings.Repeat("8", 64)
	late.NewSessionID = "33333333-3333-4333-8333-333333333333"
	late.NewCertificatePath = "/run/pki-bootstrap/renew-123456789abc/bootstrap.crt"
	late.OriginalKeySHA, late.OriginalCSRSHA = strings.Repeat("a", 64), strings.Repeat("b", 64)
	late.SeedCandidate.Resources[0].Desired = deploymentplan.Clone(late.SeedCandidate.Resources[0].Desired)
	deploymentplan.Map(late.SeedCandidate.Resources[0].Desired["metadata"])["name"] = "isolated-distinct-e2-seed"
	late.Renewal.CandidateSHA256 = late.SeedCandidate.SHA256()
	f := &createManagedLateExpiredRenewal{Schema: managedLateExpiredRenewalSchema, ParentSHA: late.ParentSHA, SourceSHA: late.SourceSHA, SourceOwnerSHA: late.SourceOwnerSHA, PriorDescriptorSHA: managedLateSessionSourceDigest(&prior), PriorCertificateSHA: strings.Repeat("c", 64), OriginalKeySHA: late.OriginalKeySHA, OriginalCSRSHA: late.OriginalCSRSHA, PriorLate: &prior, Candidate: late.SeedCandidate}
	f.NativeEvidence = map[string]string{"expired-session": strings.Repeat("d", 64), "source": f.SourceSHA, "prior-certificate": f.PriorCertificateSHA, "key": f.OriginalKeySHA, "csr": f.OriginalCSRSHA, "native-no-mint": strings.Repeat("e", 64), "prospective-certificate": deploymentplan.Digest(late.NewCertificatePath)}
	late.ExpiredRenewal = f
	image := environmentReleaseImage{Reference: "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:" + strings.Repeat("f", 64)}
	selection := strings.Repeat("9", 64)
	historical["accepted-service-core-late-session-image"] = strings.Repeat("1", 64)
	historical["accepted-service-core-late-session-image-selection"] = strings.Repeat("2", 64)
	historical["accepted-service-core-managed-active-registry"] = strings.Repeat("3", 64)
	return &createServiceSessionRecovery{ManagedLateSession: late, ManagedLateExpiredRenewal: f, GrantImage: image}, image, selection, historical
}

func TestManagedLateExpiredMaterialRecheckReplacesOnlyPriorQualifiedPins(t *testing.T) {
	r, image, selection, historical := managedLateExpiredMaterialFixture(t)
	before := deploymentplan.Digest(historical)
	pins, err := managedLateExpiredRenewalMaterialPins(r, image, selection)
	if err != nil {
		t.Fatal("complete typed current successor refused", err)
	}
	prepared := maps.Clone(historical)
	maps.Copy(prepared, pins)
	// Bootstrap starts with an empty map, then hydrates authentic ec453
	// context. Those old recipe pins cannot describe the new e2 candidates.
	observed := map[string]string{}
	maps.Copy(observed, historical)
	if firstManagedMaterialMismatchKey(prepared, observed) == "" {
		t.Fatal("fixture did not reproduce stale e1 vs qualified e2 comparison")
	}
	fresh, err := managedLateExpiredRenewalMaterialPins(r, image, selection)
	if err != nil {
		t.Fatal(err)
	}
	maps.Copy(observed, fresh)
	if key := firstManagedMaterialMismatchKey(prepared, observed); key != "" {
		t.Fatal("unchanged current qualification differs after pure reconstruction", key)
	}
	if observed["accepted-service-core-managed-active-registry"] != historical["accepted-service-core-managed-active-registry"] || deploymentplan.Digest(historical) != before {
		t.Fatal("current pin reconstruction changed historical native authority")
	}
	observed["accepted-service-core-managed-active-registry"] = strings.Repeat("4", 64)
	if firstManagedMaterialMismatchKey(prepared, observed) != "accepted-service-core-managed-active-registry" {
		t.Fatal("fresh native evidence drift was hidden by reconstructed preview pins")
	}
}

func TestManagedLateExpiredMaterialRecheckStillRejectsTypedCandidateDrift(t *testing.T) {
	original, image, selection, _ := managedLateExpiredMaterialFixture(t)
	expected, err := managedLateExpiredRenewalMaterialPins(original, image, selection)
	if err != nil {
		t.Fatal(err)
	}
	before := deploymentplan.Digest(original)
	for _, mode := range []string{"descriptor", "seed", "owners", "custody", "log-producer", "log-stop", "log-helper", "log-retire", "log-restore", "seed-authorization", "cleanup-authorization", "native", "image", "selection"} {
		t.Run(mode, func(t *testing.T) {
			var r createServiceSessionRecovery
			if json.Unmarshal(mustProviderRecoveryJSON(original), &r) != nil {
				t.Fatal("isolated successor clone")
			}
			late := r.ManagedLateSession
			r.ManagedLateExpiredRenewal = late.ExpiredRenewal
			currentImage, currentSelection := image, selection
			switch mode {
			case "descriptor":
				late.LogStateSHA = strings.Repeat("7", 64)
			case "seed":
				late.SeedCandidate.Resources[0].Desired["isolated-drift"] = true
				late.Renewal.CandidateSHA256 = late.SeedCandidate.SHA256()
				late.ExpiredRenewal.Candidate = late.SeedCandidate
			case "owners":
				late.OwnerCandidate.Resources[0].Desired["isolated-drift"] = true
			case "custody":
				late.CustodyCandidate.Resources[0].Desired["isolated-drift"] = true
			case "log-producer":
				late.LogProducer.SourcePodUID = "foreign"
			case "log-stop":
				late.LogStopCandidate.Resources[0].Desired["isolated-drift"] = true
			case "log-helper":
				late.LogHelperCandidate.Resources[0].Desired["isolated-drift"] = true
			case "log-retire":
				late.LogRetireCandidate.Resources[0].Desired["isolated-drift"] = true
			case "log-restore":
				late.LogRestoreCandidate.Resources[0].Desired["isolated-drift"] = true
			case "seed-authorization":
				late.SeedAuthorization["isolated-drift"] = true
			case "cleanup-authorization":
				late.CleanupAuthorization["isolated-drift"] = true
			case "native":
				late.ExpiredRenewal.NativeEvidence["native-no-mint"] = strings.Repeat("7", 64)
			case "image":
				currentImage.Reference = "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:" + strings.Repeat("7", 64)
				r.GrantImage = currentImage
			case "selection":
				currentSelection = strings.Repeat("7", 64)
			}
			observed, err := managedLateExpiredRenewalMaterialPins(&r, currentImage, currentSelection)
			if err == nil && firstManagedMaterialMismatchKey(expected, observed) == "" {
				t.Fatal("changed current source/preview/recipe evidence matched qualification", mode)
			}
		})
	}
	if deploymentplan.Digest(original) != before {
		t.Fatal("recheck mutation tests changed original typed proof")
	}
}

func TestManagedLateExpiredMaterialPinsRequireCompleteCurrentAuthority(t *testing.T) {
	original, image, selection, _ := managedLateExpiredMaterialFixture(t)
	for _, mode := range []string{"nil-recovery", "missing-expired", "missing-descriptor-link", "missing-seed", "wrong-seed", "wrong-source", "missing-native", "extra-native", "changed-key-proof", "foreign-image", "invalid-selection"} {
		t.Run(mode, func(t *testing.T) {
			var value createServiceSessionRecovery
			if json.Unmarshal(mustProviderRecoveryJSON(original), &value) != nil {
				t.Fatal("isolated successor clone")
			}
			r := &value
			r.ManagedLateExpiredRenewal = r.ManagedLateSession.ExpiredRenewal
			currentImage, currentSelection := image, selection
			switch mode {
			case "nil-recovery":
				r = nil
			case "missing-expired":
				r.ManagedLateExpiredRenewal = nil
			case "missing-descriptor-link":
				r.ManagedLateSession.ExpiredRenewal = nil
			case "missing-seed":
				r.ManagedLateSession.SeedCandidate = nil
			case "wrong-seed":
				r.ManagedLateExpiredRenewal.Candidate = &deploymentplan.Plan{}
			case "wrong-source":
				r.ManagedLateExpiredRenewal.SourceSHA = strings.Repeat("7", 64)
			case "missing-native":
				delete(r.ManagedLateExpiredRenewal.NativeEvidence, "native-no-mint")
			case "extra-native":
				r.ManagedLateExpiredRenewal.NativeEvidence["foreign"] = strings.Repeat("7", 64)
			case "changed-key-proof":
				r.ManagedLateExpiredRenewal.NativeEvidence["key"] = strings.Repeat("7", 64)
			case "foreign-image":
				currentImage.Reference = "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:" + strings.Repeat("7", 64)
			case "invalid-selection":
				currentSelection = "not-a-public-digest"
			}
			if _, err := managedLateExpiredRenewalMaterialPins(r, currentImage, currentSelection); err == nil {
				t.Fatal("incomplete or cross-pinned current authority accepted", mode)
			}
		})
	}
}
