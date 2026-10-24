package deploymentplan

import (
	"context"
	"errors"
	"fmt"
	"regexp"
)

// Qualification is scoped to a concrete plan, rather than the name of an
// environment. A callback supplies a digest only after checking real evidence;
// operator-authored JSON success flags are never a qualification adapter.
type QualificationOptions struct {
	Production     bool
	ExistingUpdate bool
}

type GateEvidence struct {
	SHA256               string
	PendingRolloutHealth []string
}
type GateCheck func(context.Context, *Plan) (GateEvidence, error)
type GateResult struct {
	CheckID              string   `json:"check_id,omitempty"`
	EvidenceSource       string   `json:"evidence_source,omitempty"`
	ID                   string   `json:"id"`
	Status               string   `json:"status"`
	Required             bool     `json:"required"`
	EvidenceSHA256       string   `json:"evidence_sha256,omitempty"`
	PendingRolloutHealth []string `json:"pending_rollout_health,omitempty"`
	Cause                string   `json:"cause,omitempty"`
	Next                 string   `json:"next,omitempty"`
	Impact               string   `json:"impact,omitempty"`
	Recheck              string   `json:"recheck,omitempty"`
}
type Qualification struct {
	PlanSHA256 string       `json:"plan_sha256"`
	Decision   string       `json:"decision"`
	Gates      []GateResult `json:"gates"`
}
type qualificationGate struct{ id, next, impact string }

var fullEnvironmentGates = []qualificationGate{
	{"deployment.target", "correct the complete component catalog, ownership and selected environment inputs", "the requested complete environment cannot be identified"},
	{"deployment.release", "publish every exact-source image through formal CI and select its immutable release manifest", "one or more affected consumers cannot run the selected release"},
	{"deployment.execution", "resolve tools, provider permissions, capacity, quota, scheduling, storage and exact server dry-run admission", "the approved resource bodies cannot be executed"},
	{"deployment.pki", "repair exact issuer and identity bindings, trust pins, valid chains, SANs, purposes, CRLs, consumer acknowledgments and durable private-key mounts", "issuance or authenticated service communication may stop"},
	{"deployment.compatibility", "run candidate startup and schema checks, or qualify the separate owner migration and recovery procedure", "candidate workloads may not start or may damage incompatible data"},
	{"deployment.network", "qualify the target Services, policy, DNS, TLS/mTLS and required authenticated routes", "candidate services may be unreachable or improperly authenticated"},
	{"deployment.recovery", "verify the complete durable-data inventory, current backups and independent security state; run the required restore drill", "partial execution cannot be recovered with verified state"},
}
var productionQualificationGate = qualificationGate{"deployment.production", "complete HA, independent custody/escrow, RPO/RTO restore and complete PKI lifecycle/failure qualification for this release", "normal production traffic and issuance remain closed"}
var evidenceSHA256 = regexp.MustCompile(`^[a-f0-9]{64}$`)

// Qualify checks every required gate in stable order. Errors are represented by
// stable IDs and maintained repair instructions; raw backend errors can contain
// credentials, certificate data or private subprocess output and are suppressed.
func (p *Plan) Qualify(ctx context.Context, options QualificationOptions, checks map[string]GateCheck, observe ...func(GateResult)) (Qualification, error) {
	if p == nil || len(p.Resources) == 0 {
		return Qualification{}, errors.New("a complete compiled deployment plan is required")
	}
	result := Qualification{PlanSHA256: p.SHA256(), Decision: "NO-GO"}
	gates := append([]qualificationGate{}, fullEnvironmentGates...)
	if options.ExistingUpdate {
		// Ordinary updates retain identity and durable data. Activation and
		// restore qualification belong to their separate lifecycle operations.
		gates = gates[:6]
	} else if options.Production {
		gates = append(gates, productionQualificationGate)
	}
	passed := true
	for _, gate := range gates {
		row := GateResult{ID: gate.id, Status: "BLOCKED", Required: true, Next: gate.next, Impact: gate.impact, Recheck: "repeat full preflight for the same release and review the newly compiled plan SHA-256"}
		check := checks[gate.id]
		var interrupted error
		switch {
		case ctx.Err() != nil:
			interrupted = ctx.Err()
		case check == nil:
			row.Cause = "required capability has no complete qualification adapter"
		default:
			evidence, err := check(ctx, p)
			interrupted = ctx.Err()
			if interrupted == nil && (errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)) {
				interrupted = err
			}
			if interrupted == nil {
				if err != nil {
					row.Status, row.Cause = "FAIL", "required evidence could not be verified"
					var blocker *Blocker
					if errors.As(err, &blocker) {
						row.Cause, row.Next = blocker.Cause, blocker.Next
						if blocker.Resource != "" {
							row.Cause += " (" + blocker.Resource + ")"
						}
					}
				} else if p.SHA256() != result.PlanSHA256 {
					row.Status, row.Cause = "ERROR", "the selected plan changed during qualification"
				} else if !evidenceSHA256.MatchString(evidence.SHA256) {
					row.Cause = "required evidence is absent or incomplete"
				} else if !p.validPendingRolloutHealth(gate.id, options, evidence.PendingRolloutHealth) {
					row.Status, row.Cause = "ERROR", "pending rollout health is not bound to reviewed original workloads"
				} else {
					row.Status, row.EvidenceSHA256 = "PASS", evidence.SHA256
					row.PendingRolloutHealth = append([]string(nil), evidence.PendingRolloutHealth...)
					row.Next, row.Impact, row.Recheck = "", "", ""
				}
			}
		}
		switch {
		case errors.Is(interrupted, context.DeadlineExceeded):
			row.Status, row.Cause = "ERROR", "qualification deadline exceeded before this required gate completed"
			row.Next = "repeat full preflight for the same release with a timeout long enough to complete all required checks"
		case errors.Is(interrupted, context.Canceled):
			row.Status, row.Cause = "ERROR", "qualification was canceled before this required gate completed"
			row.Next = "repeat full preflight for the same release when qualification can run to completion"
		}
		if row.Status != "PASS" {
			passed = false
		}
		result.Gates = append(result.Gates, row)
		// Report only the finalized outcome, after evidence, plan and interruption
		// checks. Finishing a callback or all queries alone does not mean PASS.
		for _, observer := range observe {
			if observer != nil {
				observer(row)
			}
		}
	}
	if passed {
		result.Decision = "GO"
		return result, nil
	}
	return result, fmt.Errorf("NO-GO: required gates failed, errored, were blocked or lacked evidence for plan %s", result.PlanSHA256)
}

// RequiredGateIDs is also the contract used by runtime adapters and report
// tests. Protection changes the required evidence, not the deployment engine.
func RequiredGateIDs(production bool) []string {
	var ids []string
	for _, gate := range fullEnvironmentGates {
		ids = append(ids, gate.id)
	}
	if production {
		ids = append(ids, productionQualificationGate.id)
	}
	return ids
}

func (p *Plan) validPendingRolloutHealth(gate string, options QualificationOptions, ids []string) bool {
	if len(ids) == 0 {
		return true
	}
	if gate != "deployment.pki" || !(options.ExistingUpdate && p.Operation == "upgrade" || p.HasCreateHandoff()) {
		return false
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			return false
		}
		seen[id] = true
		found := false
		for _, r := range p.Resources {
			if r.ID == id {
				found = Map(r.Desired)["kind"] == "Deployment" && (r.Action == "update" || r.Action == "retain") && r.UID != ""
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// A completed-bootstrap create continues an existing target while retaining
// its original operation, parent ownership and every create qualification gate.
func (p *Plan) HasCreateHandoff() bool {
	return p != nil && p.Operation == "create" && evidenceSHA256.MatchString(p.CreateParentSHA256) && evidenceSHA256.MatchString(p.CreateHandoffSHA256)
}
