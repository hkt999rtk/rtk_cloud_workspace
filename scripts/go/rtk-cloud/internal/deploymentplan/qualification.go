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
	Fast       bool
	Production bool
}

type GateEvidence struct {
	SHA256 string
}
type GateCheck func(context.Context, *Plan) (GateEvidence, error)
type GateResult struct {
	ID             string `json:"id"`
	Status         string `json:"status"`
	Required       bool   `json:"required"`
	EvidenceSHA256 string `json:"evidence_sha256,omitempty"`
	Cause          string `json:"cause,omitempty"`
	Next           string `json:"next,omitempty"`
	Impact         string `json:"impact,omitempty"`
	Recheck        string `json:"recheck,omitempty"`
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
func (p *Plan) Qualify(ctx context.Context, options QualificationOptions, checks map[string]GateCheck) (Qualification, error) {
	if p == nil || len(p.Resources) == 0 {
		return Qualification{}, errors.New("a complete compiled deployment plan is required")
	}
	result := Qualification{PlanSHA256: p.SHA256(), Decision: "NO-GO"}
	gates := append([]qualificationGate{}, fullEnvironmentGates...)
	if options.Production {
		gates = append(gates, productionQualificationGate)
	}
	passed := true
	for _, gate := range gates {
		row := GateResult{ID: gate.id, Status: "BLOCKED", Required: true, Next: gate.next, Impact: gate.impact, Recheck: "repeat full preflight for the same release and review the newly compiled plan SHA-256"}
		check := checks[gate.id]
		switch {
		case ctx.Err() != nil:
			row.Status, row.Cause = "ERROR", "qualification deadline or cancellation interrupted this required gate"
		case options.Fast:
			row.Status, row.Cause = "SKIPPED", "--fast cannot qualify a complete environment update"
		case check == nil:
			row.Cause = "required capability has no complete qualification adapter"
		default:
			evidence, err := check(ctx, p)
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
			} else {
				row.Status, row.EvidenceSHA256 = "PASS", evidence.SHA256
				row.Next, row.Impact, row.Recheck = "", "", ""
			}
		}
		if row.Status != "PASS" {
			passed = false
		}
		result.Gates = append(result.Gates, row)
	}
	if passed {
		result.Decision = "GO"
		return result, nil
	}
	return result, fmt.Errorf("NO-GO: required gates failed, errored, were blocked, skipped or lacked evidence for plan %s", result.PlanSHA256)
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
