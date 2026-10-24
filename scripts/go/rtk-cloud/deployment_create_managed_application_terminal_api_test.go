package main

import (
	"context"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedApplicationTerminalAPIFixture() (*createServiceSessionRecovery, *createServiceSessionRecovery) {
	ceremony := environmentCreateServiceCeremony{BootstrapSessionID: "11111111-1111-4111-8111-111111111111", Root: environmentCreateRegisteredIssuer{ID: "22222222-2222-4222-8222-222222222222", Certificate: "original-root-public-pem"}, Intermediate: environmentCreateRegisteredIssuer{ID: "33333333-3333-4333-8333-333333333333", Certificate: "original-ica-public-pem"}}
	late := &createManagedLateSessionRecovery{ParentSHA: strings.Repeat("a", 64), SourceSHA: strings.Repeat("b", 64), OriginalEnrollmentSession: ceremony.BootstrapSessionID, SourceCeremony: ceremony}
	sourcePartial := &createManagedApplicationPartialRecovery{Review: strings.Repeat("c", 64), ExportProofSHAs: map[string]string{"video-cloud-api-pki": strings.Repeat("d", 64), "video-cloud-api-app-pki": strings.Repeat("e", 64)}}
	currentPartial := &createManagedApplicationPartialRecovery{Review: sourcePartial.Review, ExportProofSHAs: map[string]string{"video-cloud-api-pki": sourcePartial.ExportProofSHAs["video-cloud-api-pki"], "video-cloud-api-app-pki": sourcePartial.ExportProofSHAs["video-cloud-api-app-pki"]}}
	for _, partial := range []*createManagedApplicationPartialRecovery{sourcePartial, currentPartial} {
		partial.Exports = map[string]*createManagedTransportExportRetire{}
		partial.AcceptedPlans = map[string]*deploymentplan.Plan{}
		for _, name := range []string{"video-cloud-api-pki", "video-cloud-api-app-pki"} {
			partial.Exports[name] = &createManagedTransportExportRetire{}
			partial.AcceptedPlans["kubernetes/server-secret-"+name] = &deploymentplan.Plan{}
		}
	}
	copyLate := *late
	source := &createServiceSessionRecovery{Ceremony: ceremony, ManagedLateSession: &copyLate, ManagedApplicationPartial: sourcePartial}
	current := &createServiceSessionRecovery{Ceremony: ceremony, ManagedLateSession: late, ManagedApplicationPartial: currentPartial, ManagedLateRemaining: &createManagedLateRemaining{TerminalFinalizerCompletion: &createManagedLateTerminalFinalizerCompletion{SourceSHA: strings.Repeat("f", 64)}}}
	// The API issuance still belongs to original enrollment; the current
	// terminal ceremony's session is intentionally the separately minted e2.
	current.Ceremony.BootstrapSessionID = "44444444-4444-4444-8444-444444444444"
	return source, current
}

func TestManagedApplicationTerminalAPISourceBindsHistoricalEnrollment(t *testing.T) {
	for _, mode := range []string{"genuine", "runtime-volume-hydration", "nil-source", "nil-current", "missing-late", "missing-current-late", "missing-partial", "missing-current-partial", "missing-marker", "foreign-parent", "foreign-source", "foreign-enrollment", "foreign-export", "foreign-secret-plan", "foreign-secret-journal", "missing-export", "foreign-root", "foreign-ica"} {
		t.Run(mode, func(t *testing.T) {
			source, current := managedApplicationTerminalAPIFixture()
			switch mode {
			case "runtime-volume-hydration":
				source.ManagedApplicationPartial.NewTrustVolumes = map[string]environmentCreateVolumeOwner{"observed": {PVCUID: "77777777-7777-4777-8777-777777777777"}}
			case "nil-source":
				source = nil
			case "nil-current":
				current = nil
			case "missing-late":
				source.ManagedLateSession = nil
			case "missing-current-late":
				current.ManagedLateSession = nil
			case "missing-partial":
				source.ManagedApplicationPartial = nil
			case "missing-current-partial":
				current.ManagedApplicationPartial = nil
			case "missing-marker":
				current.ManagedLateRemaining.TerminalFinalizerCompletion = nil
			case "foreign-parent":
				source.ManagedLateSession.ParentSHA = strings.Repeat("9", 64)
			case "foreign-source":
				source.ManagedLateSession.SourceSHA = strings.Repeat("9", 64)
			case "foreign-enrollment":
				source.ManagedLateSession.OriginalEnrollmentSession = "99999999-9999-4999-8999-999999999999"
			case "foreign-export":
				source.ManagedApplicationPartial.ExportProofSHAs["video-cloud-api-pki"] = strings.Repeat("9", 64)
			case "foreign-secret-plan":
				source.ManagedApplicationPartial.AcceptedPlans["kubernetes/server-secret-video-cloud-api-pki"].Stack = "foreign"
			case "foreign-secret-journal":
				source.ManagedApplicationPartial.AcceptedJournals = map[string]deploymentplan.Journal{"kubernetes/server-secret-video-cloud-api-pki": {PlanSHA256: strings.Repeat("9", 64)}}
			case "missing-export":
				delete(source.ManagedApplicationPartial.Exports, "video-cloud-api-pki")
			case "foreign-root":
				source.Ceremony.Root.Certificate = "foreign-root-public-pem"
			case "foreign-ica":
				source.Ceremony.Intermediate.Certificate = "foreign-ica-public-pem"
			}
			before := deploymentplan.Digest([]any{source, current})
			if managedApplicationTerminalAPISourceMatches(source, current) != (mode == "genuine" || mode == "runtime-volume-hydration") || deploymentplan.Digest([]any{source, current}) != before {
				t.Fatal("terminal API ancestry accepted substitution or mutated source")
			}
		})
	}
}

func TestManagedApplicationTerminalAPIReaderRejectsUnprovedMarkerBeforeSQL(t *testing.T) {
	for _, mode := range []string{"unadmitted-marker", "nil-current", "extra-current", "foreign-source"} {
		t.Run(mode, func(t *testing.T) {
			source, current := managedApplicationTerminalAPIFixture()
			args := []*createServiceSessionRecovery{current}
			switch mode {
			case "nil-current":
				args[0] = nil
			case "extra-current":
				args = append(args, current)
			case "foreign-source":
				source.ManagedLateSession.ParentSHA = strings.Repeat("9", 64)
			}
			materials := map[string]string{"original": strings.Repeat("1", 64)}
			before := deploymentplan.Digest([]any{source, current, materials})
			// No execution/backend exists. A mere marker must be refused before
			// reaching the native SQL primitive or changing material evidence.
			err := verifyManagedApplicationCurrentAPIServers(context.Background(), deploymentConfig{}, nil, environmentKubernetesBackend{}, source, materials, args...)
			if err == nil || !strings.Contains(err.Error(), "terminal-original-api-enrollment-source") || deploymentplan.Digest([]any{source, current, materials}) != before {
				t.Fatal("unproved terminal marker reached native reader or mutated inputs")
			}
		})
	}
}
