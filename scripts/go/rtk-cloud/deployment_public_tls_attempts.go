package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"golang.org/x/crypto/acme"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// DNS observations may advance their cache horizon; identity and the order's
// admitted lifetime cannot. Replacement is conditional on the exact prior
// private record and uses the existing fsynced atomic store writer.
func savePublicTLSDNSPropagation(ctx context.Context, e *environmentCreateExecution, path string, prior *environmentPublicTLSDNSPropagation, next environmentPublicTLSDNSPropagation) error {
	if e == nil || e.Check(ctx) != nil {
		return publicTLSBlock("DNS propagation observation lost its original execution")
	}
	if prior == nil {
		return publicTLSWriteOwned(ctx, e, path, next)
	}
	var actual environmentPublicTLSDNSPropagation
	now := time.Now().UTC()
	if publicTLSReadJSON(e.Store, path, &actual) != nil || deploymentplan.Digest(actual) != deploymentplan.Digest(*prior) || next.Root != prior.Root || next.SetSHA != prior.SetSHA || !slices.Equal(next.Nameservers, prior.Nameservers) || next.ConvergedAt.Before(prior.ConvergedAt) || next.ReadyAfter.Before(prior.ReadyAfter) || next.UnstableAt.Before(prior.UnstableAt) || next.ConvergedAt.After(now) || next.UnstableAt.After(now) || next.ReadyAfter.Before(next.ConvergedAt) || next.ReadyAfter.Before(next.UnstableAt) {
		return publicTLSBlock("DNS propagation observation changed scope, regressed, or lost its original record")
	}
	if deploymentplan.Digest(next) == deploymentplan.Digest(actual) {
		return e.Check(ctx)
	}
	raw, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if err = e.Check(ctx); err != nil {
		return err
	}
	invalidateManagedLateSourceReuse(e)
	if err = e.Store.write(path, raw, true); err != nil {
		return err
	}
	full, err := e.Store.safePath(path)
	if err != nil {
		return err
	}
	return syncEnvironmentPreflightDirectory(filepath.Dir(full))
}

const environmentPublicTLSMaxAttempts = 3

// The original order and its identity remain immutable. A subsequent request
// records the exact known terminal result before it can contact newOrder.
type environmentPublicTLSOrderAttempt struct {
	Plan, CSR, PreviousOrder, PreviousSHA256 string
	Invalid                                  acme.Order
	RequestedAt                              time.Time
}

func publicTLSAttemptDirectory(p *environmentPublicTLSPlan, n int) string {
	if n == 0 {
		return publicTLSBase(p)
	}
	return filepath.Join(publicTLSBase(p), "attempts", fmt.Sprintf("%04d", n))
}

func readEnvironmentPublicTLSOrderAttempts(store secretStore, p *environmentPublicTLSPlan, csr string) (acme.Order, string, []environmentPublicTLSOrderAttempt, error) {
	var order acme.Order
	var attempts []environmentPublicTLSOrderAttempt
	bad := func() (acme.Order, string, []environmentPublicTLSOrderAttempt, error) {
		return order, "", nil, publicTLSBlock("original ACME attempt chain is incomplete, changed or has an unknown order response")
	}
	if err := publicTLSReadJSON(store, filepath.Join(publicTLSBase(p), "order.json"), &order); err != nil {
		return order, "", nil, err
	}
	if validatePublicTLSOrderIdentity(p, order, order) != nil {
		return bad()
	}
	path, err := store.safePath(filepath.Join(publicTLSBase(p), "attempts"))
	if err != nil {
		return bad()
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return order, "", nil, nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
		return bad()
	}
	files, err := os.ReadDir(path)
	if err != nil || len(files) > environmentPublicTLSMaxAttempts {
		return bad()
	}
	chain := []string{deploymentplan.Digest(order)}
	seen := map[string]bool{order.URI: true}
	for i, file := range files {
		if !file.IsDir() || file.Type()&os.ModeSymlink != 0 || file.Name() != fmt.Sprintf("%04d", i+1) {
			return bad()
		}
		var intent environmentPublicTLSOrderAttempt
		var next acme.Order
		base := publicTLSAttemptDirectory(p, i+1)
		if publicTLSReadJSON(store, filepath.Join(base, "intent.json"), &intent) != nil || publicTLSReadJSON(store, filepath.Join(base, "order.json"), &next) != nil || intent.Plan != deploymentplan.Digest(p) || intent.CSR != csr || intent.PreviousOrder != order.URI || intent.PreviousSHA256 != deploymentplan.Digest(order) || intent.Invalid.Status != acme.StatusInvalid || validatePublicTLSOrderIdentity(p, order, intent.Invalid) != nil || intent.RequestedAt.IsZero() || intent.RequestedAt.After(time.Now().UTC()) || i > 0 && intent.RequestedAt.Before(attempts[i-1].RequestedAt) || seen[next.URI] || validatePublicTLSOrderIdentity(p, next, next) != nil {
			return bad()
		}
		attempts = append(attempts, intent)
		chain = append(chain, deploymentplan.Digest(intent), deploymentplan.Digest(next))
		seen[next.URI] = true
		order = next
	}
	if len(attempts) == 0 {
		return order, "", attempts, nil
	}
	return order, deploymentplan.Digest(chain), attempts, nil
}
