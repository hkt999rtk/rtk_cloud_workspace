package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const productSessionRenewalSchema = "rtk-cloud.product-session-renewal/v1"
const productSessionRenewalSGIDDirectoryProfile = "private-directory-sgid-v1"

type productSessionRenewalSource struct {
	CertificateDirectoryProfile                                                                                   string `json:",omitempty"`
	Generation                                                                                                    int    `json:",omitempty"`
	PreviousRenewalSHA256                                                                                         string `json:",omitempty"`
	RuntimeSequence                                                                                               int    `json:",omitempty"`
	Schema, ParentSHA256, OwnerSHA256, ScopeSHA256, ProgramSHA256                                                 string
	OriginalInstalledSHA256, OriginalCredentialPlanSHA256, OriginalCredentialJournalSHA256                        string
	OriginalBootstrapPlanSHA256, CustodyPlanSHA256, CustodyJournalSHA256, ClaimUID, ServiceAccountUID, CustodyUID string
	OriginalForwardDescriptorSHA256, OriginalForwardPlanSHA256, OriginalForwardJournalSHA256                      string
	OriginalCertificateSHA256, OriginalFingerprint, KeySHA256, CSRSHA256, SignerPolicySHA256                      string
	OriginalSession                                                                                               productSessionInstallations
	SessionID, DeploymentID, CertificatePath, OriginalDeploymentID                                                string
	ControlSources                                                                                                []deploymentplan.Definition
	ControlOwners                                                                                                 map[string]string
	QualifiedAt                                                                                                   time.Time
}
type productSessionRenewalStageReceipt struct{ PlanSHA256, JournalSHA256 string }
type productSessionRenewal struct {
	Schema, ScopeSHA256, SourceSHA256 string
	Source                            productSessionRenewalSource
	Original                          environmentProductIdentitySession
	Installed                         environmentProductIdentitySession
	Stages                            map[string]productSessionRenewalStageReceipt
}
type productSessionRenewalObserved struct {
	SourceSHA256      string
	Installed         environmentProductIdentitySession
	CertificateSHA256 string
}

func productSessionRenewalBlock(stage string) error {
	return environmentCreateBootstrapBlocker("product session renewal requires its preserved product identity and native evidence: stage=" + stage)
}
func productSessionRenewalBase(l *createNormalTailProductLifecycle) string {
	return filepath.Join(filepath.Dir(productSessionForwardPath(l)), "session-renewal")
}
func productSessionRenewalStage(l *createNormalTailProductLifecycle, s productSessionRenewalSource, suffix string) string {
	return "product-identities-" + productIdentityScope(l)[:12] + "-session-renewal-" + deploymentplan.Digest(s)[:12] + "-" + suffix
}
func productSessionRenewalIdentity(parent, scope, old string) (string, string, string) {
	binding := deploymentplan.Digest([]string{productSessionRenewalSchema, parent, scope, old})
	return sessionRenewalUUID(parent, binding, old), "product-" + scope[:20] + "-r" + binding[:12], "/run/pki-bootstrap/renew-" + binding[:12] + "/bootstrap.crt"
}
func readProductSessionRenewalJSON(store secretStore, l *createNormalTailProductLifecycle, name string, out any, generation ...int) (bool, error) {
	p, err := store.safePath(filepath.Join(productSessionRenewalSelectedBase(l, generation...), name))
	if err != nil {
		return false, err
	}
	if _, err = os.Lstat(p); os.IsNotExist(err) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if readEnvironmentPreflightJSON(p, out) != nil {
		return false, productSessionRenewalBlock(name)
	}
	return true, nil
}
func readProductSessionRenewalSourceForGeneration(store secretStore, e *environmentCreateExecution, l *createNormalTailProductLifecycle, generation int) (*productSessionRenewalSource, error) {
	if e == nil || l == nil || e.Store != store {
		return nil, productSessionRenewalBlock("original owner")
	}
	var s productSessionRenewalSource
	exists, err := readProductSessionRenewalJSON(store, l, "source.json", &s, generation)
	if err != nil || !exists {
		return nil, err
	}
	if s.Generation != generation {
		return nil, productSessionRenewalBlock("source generation path")
	}
	if err = validateProductSessionRenewalSource(store, e, l, s); err != nil {
		return nil, err
	}
	return &s, nil
}
func validateProductSessionRenewalDirectoryProfile(s productSessionRenewalSource) error {
	if s.CertificateDirectoryProfile != "" && (s.Generation != 0 || s.CertificateDirectoryProfile != productSessionRenewalSGIDDirectoryProfile) {
		return productSessionRenewalBlock("certificate directory recipe profile")
	}
	return nil
}
func validateProductSessionRenewalSource(store secretStore, e *environmentCreateExecution, l *createNormalTailProductLifecycle, s productSessionRenewalSource) error {
	if err := validateProductSessionRenewalDirectoryProfile(s); err != nil {
		return err
	}
	if s.Generation != 0 {
		return validateProductSessionRenewalNextSource(store, e, l, s)
	}
	if s.PreviousRenewalSHA256 != "" || s.RuntimeSequence != 0 {
		return productSessionRenewalBlock("legacy generation fields")
	}
	if e == nil || l == nil || e.Store != store || s.Schema != productSessionRenewalSchema || s.ParentSHA256 != e.Journal.PlanSHA256 || s.OwnerSHA256 != deploymentplan.Digest(e.owner) || s.ScopeSHA256 != productIdentityScope(l) {
		return productSessionRenewalBlock("parent and scope")
	}
	program, err := readCreateNormalTailProgram(store, e)
	if err != nil {
		return err
	}
	original, err := readProductInstalled(store, l)
	if err != nil {
		return err
	}
	if original == nil {
		return productSessionRenewalBlock("missing original installed")
	}
	sid, did, path := productSessionRenewalIdentity(s.ParentSHA256, s.ScopeSHA256, original.Ceremony.BootstrapSessionID)
	if s.ProgramSHA256 != deploymentplan.Digest(program) || s.OriginalInstalledSHA256 != deploymentplan.Digest(original) || s.SessionID != sid || s.DeploymentID != did || s.CertificatePath != path || s.QualifiedAt.IsZero() || s.QualifiedAt.After(time.Now().UTC()) || s.OriginalSession.Expires.IsZero() || s.OriginalSession.Expires.After(s.QualifiedAt) || s.OriginalSession.Session != original.Ceremony.BootstrapSessionID || s.OriginalSession.Issuer != original.Ceremony.Intermediate.ID || s.OriginalSession.Root != original.Ceremony.Root.RootFingerprint || s.OriginalSession.Status != "active" && s.OriginalSession.Status != "expired" || s.SignerPolicySHA256 != original.Ceremony.CleanupBinding.PolicySHA256 {
		return productSessionRenewalBlock("original installed and expired session")
	}
	var subjects []string
	for _, o := range l.Outputs {
		subjects = append(subjects, o.Subject)
	}
	slices.Sort(subjects)
	if len(subjects) != 5 || len(s.OriginalSession.Installed) >= len(subjects) || !slices.Equal(subjects, s.OriginalSession.Subjects) {
		return productSessionRenewalBlock("five original subjects")
	}
	known := map[string]bool{}
	for _, v := range s.OriginalSession.Installed {
		if known[v] || !slices.Contains(subjects, v) {
			return productSessionRenewalBlock("original ACK roster")
		}
		known[v] = true
	}
	for _, v := range []string{s.OriginalCertificateSHA256, s.OriginalFingerprint, s.KeySHA256, s.CSRSHA256, s.SignerPolicySHA256, s.OriginalForwardDescriptorSHA256, s.OriginalForwardPlanSHA256, s.OriginalForwardJournalSHA256} {
		if !environmentPreflightDigest.MatchString(v) {
			return productSessionRenewalBlock("source digests")
		}
	}
	cp, cj, err := acceptedCreateStageForProduct(store, e, l, "service-credential-secrets")
	if err != nil || cp.SHA256() != s.OriginalCredentialPlanSHA256 || deploymentplan.Digest(cj) != s.OriginalCredentialJournalSHA256 || len(cp.Resources) != 1 || cj.Owners[cp.Resources[0].ID] != original.Ceremony.CredentialUIDs[original.Ceremony.BootstrapCredentialNamespace] {
		return productSessionRenewalBlock("accepted original credential")
	}
	custody, j, err := acceptedCreateStageForProduct(store, e, l, "service-custody-reader")
	if err != nil || custody.SHA256() != s.CustodyPlanSHA256 || deploymentplan.Digest(j) != s.CustodyJournalSHA256 || len(custody.Resources) != 1 || j.Owners[custody.Resources[0].ID] != s.CustodyUID || !environmentBootstrapOwnerUUID.MatchString(s.CustodyUID) {
		return productSessionRenewalBlock("accepted original custody")
	}
	bp, err := readCreateNormalTailPreparationPlan(store, e, "product-identities-"+productIdentityScope(l)[:12]+"-service-session")
	if err != nil || bp == nil || bp.SHA256() != s.OriginalBootstrapPlanSHA256 || len(bp.Resources) != 3 {
		return productSessionRenewalBlock("original bootstrap plan")
	}
	bj, exists, err := bp.ReadExecution(store.Root)
	if err != nil || !exists || bj.InFlight != "" || len(bj.Completed) < 2 || bj.Owners[bp.Resources[0].ID] != s.ClaimUID || bj.Owners[bp.Resources[1].ID] != s.ServiceAccountUID || bp.Resources[0].ID != "PersistentVolumeClaim/"+original.Ceremony.BootstrapCredentialNamespace+"/"+original.Ceremony.BootstrapClaim || bp.Resources[1].ID != "ServiceAccount/"+original.Ceremony.BootstrapCredentialNamespace+"/pki-product-bootstrap-"+productIdentityScope(l)[:12] || !environmentBootstrapOwnerUUID.MatchString(s.ClaimUID) || !environmentBootstrapOwnerUUID.MatchString(s.ServiceAccountUID) {
		return productSessionRenewalBlock("original claim and service account")
	}
	originalJob := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(bp.Resources[2].Desired)["containers"])[0])
	if s.OriginalDeploymentID == "" || mqttFoundationEnv(originalJob, "PKI_BOOTSTRAP_DEPLOYMENT_ID") != s.OriginalDeploymentID {
		return productSessionRenewalBlock("original native deployment intent")
	}
	raw, err := readBoundedEnvironmentFile(createNormalTailWorkloadForwardPath(e), true)
	if err != nil || environmentPreflightBytesSHA256(raw) != s.OriginalForwardDescriptorSHA256 {
		return productSessionRenewalBlock("original forward descriptor")
	}
	var f createNormalTailWorkloadForward
	if decodeEnvironmentJSON(raw, &f) != nil || f.Target.Plan == nil || f.Target.Plan.SHA256() != s.OriginalForwardPlanSHA256 || f.ParentSHA256 != s.ParentSHA256 || f.OwnerSHA256 != s.OwnerSHA256 || f.ProgramSHA256 != s.ProgramSHA256 {
		return productSessionRenewalBlock("original forward source")
	}
	fj, exists, err := f.Target.Plan.ReadExecution(store.Root)
	if err != nil || !exists || deploymentplan.Digest(fj) != s.OriginalForwardJournalSHA256 || fj.Accepted || fj.AcceptanceStarted || len(fj.Completed) != 0 || len(fj.Owners) != 0 || fj.InFlight != "" || len(f.Target.Plan.Resources) == 0 || fj.Failed != f.Target.Plan.Resources[0].ID {
		return productSessionRenewalBlock("original forward has native effects")
	}
	want := map[string]deploymentplan.Object{}
	for _, v := range f.Target.Plan.Resources {
		if v.ID == "Deployment/"+f.Target.Plan.Stack+"-video-cloud/pki-controller" || v.ID == "Deployment/"+f.Target.Plan.Stack+"-video-cloud/certissuer" {
			o := deploymentplan.Clone(v.Desired)
			deploymentplan.Map(o["metadata"])["uid"] = v.UID
			want[v.ID] = o
		}
	}
	if len(s.ControlSources) != 2 || len(want) != 2 {
		return productSessionRenewalBlock("frozen control sources")
	}
	for _, d := range s.ControlSources {
		if deploymentplan.Digest(want[deploymentplan.ID(d.Object)]) != deploymentplan.Digest(d.Object) {
			return productSessionRenewalBlock("frozen control body")
		}
		delete(want, deploymentplan.ID(d.Object))
	}
	if len(want) != 0 {
		return productSessionRenewalBlock("frozen control sources")
	}
	controls, controlsJournal, er := acceptedCreateStageForProduct(store, e, l, "session-controls")
	if er != nil {
		return productSessionRenewalBlock("original cleanup owners")
	}
	expectedOwners := map[string]string{}
	for _, v := range controls.Resources {
		if v.Desired["kind"] == "Role" || v.Desired["kind"] == "RoleBinding" || v.Desired["kind"] == "ClusterRole" || v.Desired["kind"] == "ClusterRoleBinding" {
			expectedOwners[v.ID] = controlsJournal.Owners[v.ID]
		}
	}
	if len(expectedOwners) != 4 || deploymentplan.Digest(expectedOwners) != deploymentplan.Digest(s.ControlOwners) {
		return productSessionRenewalBlock("original cleanup RBAC owners")
	}
	return nil
}
func readProductSessionRenewalObserved(store secretStore, e *environmentCreateExecution, l *createNormalTailProductLifecycle, s productSessionRenewalSource) (*productSessionRenewalObserved, error) {
	var o productSessionRenewalObserved
	exists, err := readProductSessionRenewalJSON(store, l, "observed.json", &o, s.Generation)
	if err != nil || !exists {
		return nil, err
	}
	original, err := productSessionRenewalSourceInstalled(store, e, l, s)
	if err != nil {
		return nil, err
	}
	if o.SourceSHA256 != deploymentplan.Digest(s) || !environmentPreflightDigest.MatchString(o.CertificateSHA256) || !productSessionRenewalInstalledMatches(original, &o.Installed, s) {
		return nil, productSessionRenewalBlock("observed session")
	}
	return &o, nil
}
func productSessionRenewalInstalledMatches(old, new *environmentProductIdentitySession, s productSessionRenewalSource) bool {
	if old == nil || new == nil {
		return false
	}
	a, b := old.Ceremony, new.Ceremony
	enrollment := old.EnrollmentSessionID
	if enrollment == "" {
		enrollment = old.Ceremony.BootstrapSessionID
	}
	return new.EnrollmentSessionID == enrollment && new.Schema == old.Schema && new.ParentSHA256 == old.ParentSHA256 && new.PlanSHA256 == old.PlanSHA256 && new.OwnerSHA256 == old.OwnerSHA256 && deploymentplan.Digest(new.Outputs) == deploymentplan.Digest(old.Outputs) && deploymentplan.Digest(a.Root) == deploymentplan.Digest(b.Root) && deploymentplan.Digest(a.Intermediate) == deploymentplan.Digest(b.Intermediate) && a.BootstrapClaim == b.BootstrapClaim && a.CustodyReader == b.CustodyReader && a.BootstrapCredentialNamespace == b.BootstrapCredentialNamespace && slices.Equal(a.BootstrapCredentialNamespaces, b.BootstrapCredentialNamespaces) && b.BootstrapSessionID == s.SessionID && b.BootstrapCredentialSecret == "service-bootstrap-"+s.SessionID && len(b.CredentialUIDs) == 1 && environmentBootstrapOwnerUUID.MatchString(b.CredentialUIDs[b.BootstrapCredentialNamespace]) && b.CleanupBinding.SessionID == s.SessionID && b.CleanupBinding.SecretUID == b.CredentialUIDs[b.BootstrapCredentialNamespace] && b.CleanupBinding.PolicySHA256 == s.SignerPolicySHA256 && validateEnvironmentBootstrapCleanupBinding(&b.CleanupBinding, old.Ceremony.Root.Environment) == nil
}
func readProductSessionRenewalAt(store secretStore, e *environmentCreateExecution, l *createNormalTailProductLifecycle, generation int) (*productSessionRenewal, error) {
	if e == nil || l == nil {
		return nil, productSessionRenewalBlock("owner")
	}
	var r productSessionRenewal
	exists, err := readProductSessionRenewalJSON(store, l, "completed.json", &r, generation)
	if err != nil || !exists {
		return nil, err
	}
	s, err := readProductSessionRenewalSourceForGeneration(store, e, l, generation)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, productSessionRenewalBlock("missing source")
	}
	old, err := productSessionRenewalSourceInstalled(store, e, l, *s)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, productSessionRenewalBlock("missing source")
	}
	o, err := readProductSessionRenewalObserved(store, e, l, *s)
	if err != nil {
		return nil, err
	}
	if s == nil || o == nil || r.Schema != productSessionRenewalSchema || r.ScopeSHA256 != productIdentityScope(l) || r.SourceSHA256 != deploymentplan.Digest(s) || deploymentplan.Digest(r.Source) != deploymentplan.Digest(s) || deploymentplan.Digest(r.Original) != deploymentplan.Digest(old) || deploymentplan.Digest(r.Installed) != deploymentplan.Digest(o.Installed) || !productSessionRenewalInstalledMatches(old, &r.Installed, *s) || len(r.Stages) != 4 {
		return nil, productSessionRenewalBlock("completion source")
	}
	if _, err = productSessionRenewalDependencyResources(deploymentConfig{Environment: l.Bootstrap.Environment, Values: map[string]string{"CLOUD_STACK_NAME": l.Bootstrap.Stack}}, store, e, l, &r); err != nil {
		return nil, err
	}
	return &r, nil
}
func readEffectiveProductInstalled(store secretStore, e *environmentCreateExecution, l *createNormalTailProductLifecycle) (*environmentProductIdentitySession, error) {
	r, err := readProductSessionRenewal(store, e, l)
	if err != nil {
		return nil, err
	}
	if r != nil {
		return &r.Installed, nil
	}
	return readProductInstalled(store, l)
}
func productSessionRenewalWindow(ctx context.Context, e *environmentCreateExecution) error {
	if err := e.Check(ctx); err != nil {
		return err
	}
	a, err := readCreateNormalTailProgramAdmission(e.Store, e)
	if err != nil {
		return err
	}
	if !time.Now().UTC().Before(a.Deadline) {
		return environmentCreateBlocker("deployment.execution.deadline", "the original remaining-program deadline has expired")
	}
	return ctx.Err()
}
func productSessionRenewalWrite(store secretStore, l *createNormalTailProductLifecycle, name string, v any, generation ...int) error {
	_, err := writeProductArtifact(store, filepath.Join(productSessionRenewalSelectedBase(l, generation...), name), v)
	return err
}
