package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// These checks cover known cross-repository decisions, not arbitrary prose.
// Paths are explicit so historical reports and migration evidence remain intact.
var docsConsistencyRules = []struct {
	name    string
	paths   []string
	pattern string
}{
	{"global app CSR identity", []string{"repos/rtk_cloud_contracts_doc/api_usage.md", "repos/rtk_cloud_contracts_doc/provision.md", "repos/rtk_video_cloud/docs/auth.md"}, `app-brand-cloud-user:`},
	{"global developer bundle target", []string{"repos/rtk_cloud_contracts_doc/certificate_bundle.md", "repos/rtk_account_manager/docs/developer-pki-test-bundles.md"}, "`target_type` is `brand_cloud_user`"},
	{"independent Billing ownership", []string{"repos/rtk_cloud_contracts_doc/contract_overview.md", "repos/rtk_cloud_contracts_doc/authorization.md"}, `Account Manager (is the phase-one owner|\x60/v1/orgs/\{orgId\}/billing/)`},
	{"API/outbox provisioning", []string{"repos/rtk_cloud_contracts_doc/contract_overview.md"}, `channel rather than a direct server-to-server`},
	{"broker-free current deployment", []string{"docs/cost/aws-service-mapping.md"}, `(Workspace|Current) default is NATS JetStream`},
	{"durable Shadow authority", []string{"docs/cost/aws-service-mapping.md"}, `with Postgres flush|outbox/inbox, shadow snapshots`},
	{"external SecretStore layout", []string{"docs/cloud-env-layout.md"}, `state/\{kubeconfig\.yaml|runtime/state/kubeconfig\.yaml| secrets/`},
	{"environment-local credentials", []string{"docs/storage-credential-lifecycle.md"}, `The shared profile normally contains`},
	{"device-scoped new SDK bootstrap", []string{"repos/rtk_cloud_client/docs/pki_device_auth.md"}, `must request \x60scope: "camera"|requests \x60scope: "camera"|Authorization: Bearer <current_camera_token>|POST /api/device/provision_certificate`},
	{"JWT claim vocabulary", []string{"repos/rtk_cloud_client/docs/auth.md"}, `claims include[^\n]*issued_at`},
	{"canonical clip wire authority", []string{"repos/rtk_video_cloud/docs/clip-direct-upload-design.md"}, `this document wins until|root of fact for clip upload behavior`},
}

func activeDesignText(text string) string {
	// Only an explicit level-two historical section exempts following prose.
	// Historical material in other files is excluded by the scoped path list.
	if before, _, ok := strings.Cut(text, "\n## Historical "); ok {
		text = before
	}
	return strings.Join(strings.Fields(text), " ")
}

func checkDocsConsistency(check *checkState, workspace string) {
	fmt.Fprintln(os.Stdout, "== design consistency guardrails ==")
	for _, rule := range docsConsistencyRules {
		pattern := regexp.MustCompile(rule.pattern)
		for _, path := range rule.paths {
			data, err := os.ReadFile(filepath.Join(workspace, filepath.FromSlash(path)))
			if err != nil {
				check.fail("cannot read design source: " + path)
			} else if pattern.MatchString(activeDesignText(string(data))) {
				check.fail(rule.name + ": obsolete guidance in " + path)
			} else {
				check.pass(rule.name + ": " + path)
			}
		}
	}
}
