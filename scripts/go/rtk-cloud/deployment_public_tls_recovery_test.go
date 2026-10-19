package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func publicTLSAdmittedPublisher(t *testing.T, store secretStore, e *environmentCreateExecution, p *environmentPublicTLSPlan, backend *normalTailE2EBackend) func(context.Context, deploymentplan.Definition) (string, error) {
	t.Helper()
	program, err := readCreateNormalTailProgram(store, e)
	if err != nil {
		t.Fatal(err)
	}
	h := &createNormalTailHandoff{Execution: e, Pins: map[string]string{"public-tls": deploymentplan.Digest(p)}}
	var original *deploymentplan.Plan
	return func(ctx context.Context, d deploymentplan.Definition) (string, error) {
		if original == nil {
			original, err = deploymentplan.Compile(deploymentplan.Inputs{Operation: "scoped", Environment: e.Journal.Environment, Stack: e.Journal.Stack, ConfigurationSHA256: program.MaintainedConfigSHA256, ReleaseSHA256: deploymentplan.Digest(program.Release), BindingsSHA256: environmentCreateStageBindingsSHA("public-tls", []deploymentplan.Definition{d}), CreateParentSHA256: e.Journal.PlanSHA256, CreateHandoffSHA256: deploymentplan.Digest(h.Pins)}, []deploymentplan.Definition{d}, nil)
			if err != nil {
				return "", err
			}
		}
		if err = saveCreateNormalTailStagePlan(ctx, store, e, "public-tls", original); err != nil {
			return "", err
		}
		err = executeCreateNormalTailStageBackend(ctx, store, h, "public-tls", original, backend, func(ctx context.Context, p *deploymentplan.Plan) error {
			return p.AdmitResumable(ctx, backend, store.Root)
		}, func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }, func(ctx context.Context, p *deploymentplan.Plan) error { return p.Verify(ctx, backend) }, io.Discard, func(context.Context) error { return nil })
		if err != nil {
			return "", err
		}
		return deploymentplan.String(deploymentplan.Map(backend.objects[p.SecretID]["metadata"])["uid"]), nil
	}
}

func publicTLSBatchFixtureWait(p *environmentPublicTLSPlan, server *publicTLSACMEServer) func(context.Context, string, map[string]string, map[string]string, *environmentPublicTLSDNSPropagation, func(environmentPublicTLSDNSPropagation) error) error {
	return func(ctx context.Context, root string, expected map[string]string, _ map[string]string, prior *environmentPublicTLSDNSPropagation, save func(environmentPublicTLSDNSPropagation) error) error {
		if root != p.RootDomain || len(expected) != len(p.Hosts) {
			return errors.New("propagation lost the original complete order batch")
		}
		for name, value := range expected {
			if !slices.Contains(server.dns.values[strings.TrimPrefix(name, "_acme-challenge.")], value) {
				return errors.New("validation preceded complete provider publication")
			}
		}
		if prior != nil {
			if prior.SetSHA != deploymentplan.Digest(expected) {
				return errors.New("partial authorization reset the propagation proof")
			}
			return ctx.Err()
		}
		now := time.Now().UTC()
		return save(environmentPublicTLSDNSPropagation{Root: root, SetSHA: deploymentplan.Digest(expected), Nameservers: []string{"ns.example.test"}, ConvergedAt: now, ReadyAfter: now})
	}
}

func TestEnvironmentPublicTLSInvalidOrderRecoveryKeepsOriginalIdentityAndRealReader(t *testing.T) {
	for _, mode := range []string{"complete", "cleanup-interruption", "unknown-retry-response"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, e, defs, p, ops, server, backend, apply := publicTLSProtocolFixture(t)
			cfg.Values["MQTT_FOUNDATION_REGISTRATION_ENABLED"] = "true"
			p.ConfigurationSHA256 = deploymentplan.Digest(cfg)
			admission, read := admitPublicTLSReaderProgram(t, cfg, store, e, defs, p)
			apply = publicTLSAdmittedPublisher(t, store, e, p, backend)
			ops.WaitDNS01Set = publicTLSBatchFixtureWait(p, server)
			server.failValidation = true
			err := executeEnvironmentPublicTLSWithOps(t.Context(), cfg, store, e, p, apply, ops)
			if err == nil || !strings.Contains(err.Error(), "dns/NXDOMAIN") || strings.Contains(err.Error(), "private-challenge-canary") || server.status != "invalid" || read() != nil {
				t.Fatal("original CA problem was hidden, leaked, or lost its known pending order", err)
			}
			entry := createOpenBaoACKCompletionEntry(e.Journal, "public-tls/certificate")
			started, intent := entry.StartedAt, entry.IntentSHA256
			original := map[string]string{}
			for _, name := range []string{"keys.json", "account.json", "order.json"} {
				path, _ := store.safePath(filepath.Join(publicTLSBase(p), name))
				raw, _ := os.ReadFile(path)
				original[name] = environmentPreflightBytesSHA256(raw)
			}
			if mode == "cleanup-interruption" {
				server.dns.failCleanup = true
				if executeEnvironmentPublicTLSWithOps(t.Context(), cfg, store, e, p, apply, ops) == nil || server.orders != 1 {
					t.Fatal("replacement order preceded exact original challenge cleanup")
				}
			}
			server.failOrder = mode == "unknown-retry-response"
			err = executeEnvironmentPublicTLSWithOps(t.Context(), cfg, store, e, p, apply, ops)
			if mode == "unknown-retry-response" {
				if err == nil || server.orders != 2 || executeEnvironmentPublicTLSWithOps(t.Context(), cfg, store, e, p, apply, ops) == nil || server.orders != 2 || backend.writes[p.SecretID] != 0 {
					t.Fatal("unknown retry response opened another order or published unproved material", err)
				}
			} else {
				readErr := read()
				if err != nil || readErr != nil || server.orders != 2 || server.accounts != 1 || server.finalizes != 1 || backend.writes[p.SecretID] != 1 {
					t.Fatalf("known INVALID order failed continuation: producer=%v reader=%v orders=%d accounts=%d finalizes=%d writes=%d", err, readErr, server.orders, server.accounts, server.finalizes, backend.writes[p.SecretID])
				}
				var keys environmentPublicTLSKeys
				if publicTLSReadJSON(store, filepath.Join(publicTLSBase(p), "keys.json"), &keys) != nil {
					t.Fatal("original keys missing")
				}
				winner, chain, attempts, err := readEnvironmentPublicTLSOrderAttempts(store, p, deploymentplan.Digest(keys.CSR))
				if err != nil || len(attempts) != 1 || chain == "" || !strings.HasSuffix(winner.URI, "/order/2") {
					t.Fatal("winner lacks exact append-only attempt authority", err)
				}
				entry = createOpenBaoACKCompletionEntry(e.Journal, "public-tls/certificate")
				if entry.State != "completed" || !entry.StartedAt.Equal(started) || entry.IntentSHA256 != intent || !attempts[0].RequestedAt.Before(admission.Deadline) {
					t.Fatal("recovery replaced original parent intent or deadline")
				}
				for _, host := range p.Hosts {
					if !slices.Equal(server.dns.values[host], []string{"unrelated-txt"}) {
						t.Fatal("attempt cleanup leaked or removed unrelated DNS values")
					}
				}
			}
			for name, hash := range original {
				path, _ := store.safePath(filepath.Join(publicTLSBase(p), name))
				raw, _ := os.ReadFile(path)
				if environmentPreflightBytesSHA256(raw) != hash {
					t.Fatal("recovery rewrote original account, leaf key/CSR or first order", name)
				}
			}
		})
	}
}

func TestEnvironmentPublicTLSPartialAuthorizationKeepsOriginalBatchBarrier(t *testing.T) {
	cfg, store, e, _, p, ops, server, _, apply := publicTLSProtocolFixture(t)
	ops.WaitDNS01Set = publicTLSBatchFixtureWait(p, server)
	ctx, cancel := context.WithCancel(t.Context())
	server.afterAccept = func(i int) {
		if i == 0 {
			cancel()
		}
	}
	if executeEnvironmentPublicTLSWithOps(ctx, cfg, store, e, p, apply, ops) == nil || !server.authorized[0] || server.status != "pending" {
		t.Fatal("partial native validation interruption was not exercised")
	}
	path, _ := store.safePath(filepath.Join(publicTLSBase(p), "propagation.json"))
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	server.afterAccept = nil
	if err := executeEnvironmentPublicTLSWithOps(t.Context(), cfg, store, e, p, apply, ops); err != nil {
		t.Fatal("partially valid authorization changed complete barrier scope", err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) || server.orders != 1 || server.accepts[0] != 1 || server.accepts[1] != 1 {
		t.Fatal("retry rewrote propagation time, re-accepted a valid challenge or opened another order")
	}
}

func TestEnvironmentPublicTLSPropagationUpdatesOnlyExactMonotonicObservation(t *testing.T) {
	_, store, e, _, p, _, _, _, _ := publicTLSProtocolFixture(t)
	now := time.Now().UTC()
	path := filepath.Join(publicTLSBase(p), "propagation.json")
	first := environmentPublicTLSDNSPropagation{Root: p.RootDomain, SetSHA: deploymentplan.Digest("exact batch"), Nameservers: []string{"ns.example.test"}, ConvergedAt: now.Add(-2 * time.Minute), ReadyAfter: now.Add(8 * time.Minute)}
	if err := savePublicTLSDNSPropagation(t.Context(), e, path, nil, first); err != nil {
		t.Fatal(err)
	}
	advanced := first
	advanced.UnstableAt, advanced.ReadyAfter = now.Add(-time.Minute), now.Add(9*time.Minute)
	if err := savePublicTLSDNSPropagation(t.Context(), e, path, &first, advanced); err != nil {
		t.Fatal("observed authoritative regression could not be retained durably", err)
	}
	var saved environmentPublicTLSDNSPropagation
	if publicTLSReadJSON(store, path, &saved) != nil || deploymentplan.Digest(saved) != deploymentplan.Digest(advanced) {
		t.Fatal("restart would forget the renewed negative-cache horizon")
	}
	for _, mode := range []string{"stale-prior", "root", "set", "nameserver", "regressed-time", "future-observation"} {
		t.Run(mode, func(t *testing.T) {
			next, previous := advanced, advanced
			switch mode {
			case "stale-prior":
				previous = first
			case "root":
				next.Root = "foreign.test"
			case "set":
				next.SetSHA = deploymentplan.Digest("another batch")
			case "nameserver":
				next.Nameservers = []string{"foreign.test"}
			case "regressed-time":
				next.ReadyAfter = first.ReadyAfter
			case "future-observation":
				next.UnstableAt = now.Add(time.Minute)
			}
			if savePublicTLSDNSPropagation(t.Context(), e, path, &previous, next) == nil {
				t.Fatal("foreign or stale propagation observation replaced the exact original")
			}
			if publicTLSReadJSON(store, path, &saved) != nil || deploymentplan.Digest(saved) != deploymentplan.Digest(advanced) {
				t.Fatal("rejected update changed private observation state")
			}
		})
	}
}
