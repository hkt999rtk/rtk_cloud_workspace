package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type publicTLSDNSFixture struct {
	mu       sync.Mutex
	now      time.Time
	start    time.Time
	expected map[string]string
	answers  func(publicTLSDNSQuery, time.Duration) (publicTLSDNSAnswer, error)
	queries  int
	waits    []time.Duration
}

func newPublicTLSDNSFixture() *publicTLSDNSFixture {
	now := time.Date(2026, 10, 18, 7, 0, 0, 0, time.UTC)
	f := &publicTLSDNSFixture{now: now, start: now, expected: map[string]string{}}
	for i := range 9 {
		f.expected[fmt.Sprintf("_acme-challenge.host%d.example.test", i)] = fmt.Sprintf("private-test-token-%d", i)
	}
	return f
}

func (f *publicTLSDNSFixture) answer(q publicTLSDNSQuery) (publicTLSDNSAnswer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries++
	if f.answers != nil {
		return f.answers(q, f.now.Sub(f.start))
	}
	return f.ready(q), nil
}

func (f *publicTLSDNSFixture) ready(q publicTLSDNSQuery) publicTLSDNSAnswer {
	a := publicTLSDNSAnswer{status: "NOERROR", authoritative: q.authoritative}
	switch q.kind {
	case "NS":
		a.ns = []string{"ns1.example.test", "ns2.example.test"}
	case "SOA":
		a.soa, a.negativeTTL = true, 600*time.Second
	case "TXT":
		a.txt = []string{"unrelated-original-record", f.expected[q.name]}
	}
	return a
}

func (f *publicTLSDNSFixture) ops() publicTLSDNSWaitOps {
	return publicTLSDNSWaitOps{query: func(ctx context.Context, q publicTLSDNSQuery) (publicTLSDNSAnswer, error) {
		if err := ctx.Err(); err != nil {
			return publicTLSDNSAnswer{}, err
		}
		return f.answer(q)
	}, now: func() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.now }, wait: func(ctx context.Context, d time.Duration) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.waits = append(f.waits, d)
		f.now = f.now.Add(d)
		return nil
	}}
}

func TestPublicTLSDNSBatchWaitsForAllViewsAndOneNegativeCacheHorizon(t *testing.T) {
	f := newPublicTLSDNSFixture()
	f.answers = func(q publicTLSDNSQuery, elapsed time.Duration) (publicTLSDNSAnswer, error) {
		a := f.ready(q)
		if q.kind == "TXT" && q.server == "ns2.example.test" && elapsed < 20*time.Second {
			a.status, a.txt = "NXDOMAIN", nil
		}
		if q.kind == "TXT" && q.server == "1.1.1.1" && elapsed < 70*time.Second {
			a.status, a.txt = "NXDOMAIN", nil
		}
		return a, nil
	}
	var saved []environmentPublicTLSDNSPropagation
	err := waitPublicTLSDNS01WithOps(t.Context(), "example.test", f.expected, nil, nil, func(p environmentPublicTLSDNSPropagation) error { saved = append(saved, p); return nil }, f.ops())
	if err != nil {
		t.Fatal(err)
	}
	if len(saved) != 1 || saved[0].ConvergedAt != f.start.Add(20*time.Second) || saved[0].ReadyAfter != f.start.Add(620*time.Second) || f.now != saved[0].ReadyAfter {
		t.Fatalf("complete nine-name barrier was not one coherent 600-second batch: saved=%+v elapsed=%v", saved, f.now.Sub(f.start))
	}
	// A retry reuses the same durable horizon, but still observes every view.
	before := f.queries
	f.waits = nil
	err = waitPublicTLSDNS01WithOps(t.Context(), "example.test", f.expected, nil, &saved[0], func(environmentPublicTLSDNSPropagation) error { t.Fatal("replaced original barrier"); return nil }, f.ops())
	if err != nil || len(f.waits) != 0 || f.queries-before != 3+2*(9+1)+3*9 {
		t.Fatalf("retry did not freshly validate without another 600-second wait: err=%v waits=%v queries=%d", err, f.waits, f.queries-before)
	}
}

func TestPublicTLSDNSCacheHorizonFitsShortConvergenceBudgetAndTransientAuthority(t *testing.T) {
	for _, retry := range []bool{false, true} {
		t.Run(fmt.Sprint("prior=", retry), func(t *testing.T) {
			f := newPublicTLSDNSFixture()
			var prior *environmentPublicTLSDNSPropagation
			if retry {
				prior = &environmentPublicTLSDNSPropagation{Root: "example.test", SetSHA: deploymentplan.Digest(f.expected), Nameservers: []string{"ns1.example.test", "ns2.example.test"}, ConvergedAt: f.start.Add(-10 * time.Minute), ReadyAfter: f.start}
				f.answers = func(q publicTLSDNSQuery, elapsed time.Duration) (publicTLSDNSAnswer, error) {
					a := f.ready(q)
					if q.kind == "TXT" && q.server == "ns2.example.test" && elapsed < 20*time.Second {
						a.status, a.txt = "NXDOMAIN", nil
					}
					return a, nil
				}
			}
			saves := 0
			err := waitPublicTLSDNS01WithOps(t.Context(), "example.test", f.expected, map[string]string{"DNS_PROPAGATION_TIMEOUT_SECONDS": "30"}, prior, func(environmentPublicTLSDNSPropagation) error { saves++; return nil }, f.ops())
			wantElapsed, wantSaves := 600*time.Second, 1
			if retry {
				wantElapsed, wantSaves = 620*time.Second, 2
			}
			if err != nil || f.now.Sub(f.start) != wantElapsed || saves != wantSaves {
				t.Fatalf("SOA horizon was treated as an impossible convergence timeout or transient reply was terminal: err=%v elapsed=%v saves=%d", err, f.now.Sub(f.start), saves)
			}
		})
	}
}

func TestPublicTLSDNSInterruptedAuthoritativeFlapPersistsBeforeRetry(t *testing.T) {
	f := newPublicTLSDNSFixture()
	f.answers = func(q publicTLSDNSQuery, _ time.Duration) (publicTLSDNSAnswer, error) {
		a := f.ready(q)
		if q.kind == "TXT" && q.server == "ns2.example.test" {
			a.status, a.txt = "NXDOMAIN", nil
		}
		return a, nil
	}
	original := environmentPublicTLSDNSPropagation{Root: "example.test", SetSHA: deploymentplan.Digest(f.expected), Nameservers: []string{"ns1.example.test", "ns2.example.test"}, ConvergedAt: f.start.Add(-10 * time.Minute), ReadyAfter: f.start}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var persisted environmentPublicTLSDNSPropagation
	err := waitPublicTLSDNS01WithOps(ctx, "example.test", f.expected, nil, &original, func(p environmentPublicTLSDNSPropagation) error {
		persisted = p
		cancel() // interruption after durable invalidation, before recovery
		return nil
	}, f.ops())
	if !errors.Is(err, context.Canceled) || persisted.UnstableAt.IsZero() || !persisted.ReadyAfter.After(original.ReadyAfter) {
		t.Fatalf("flap was not durably invalidated before interruption: err=%v proof=%+v", err, persisted)
	}
	f.answers = nil
	f.now = f.start.Add(time.Second)
	saves := 0
	err = waitPublicTLSDNS01WithOps(t.Context(), "example.test", f.expected, nil, &persisted, func(p environmentPublicTLSDNSPropagation) error {
		saves++
		persisted = p
		return nil
	}, f.ops())
	if err != nil || saves != 1 || f.now != f.start.Add(601*time.Second) || !persisted.ConvergedAt.After(persisted.UnstableAt) || persisted.SetSHA != original.SetSHA || persisted.Root != original.Root {
		t.Fatalf("retry forgot the original interrupted flap or changed scope: err=%v saves=%d elapsed=%v proof=%+v", err, saves, f.now.Sub(f.start), persisted)
	}
}

func TestPublicTLSDNSIndependentResolverQuorumToleratesOneOutage(t *testing.T) {
	for _, outages := range []int{1, 2} {
		t.Run(fmt.Sprint(outages), func(t *testing.T) {
			f := newPublicTLSDNSFixture()
			f.answers = func(q publicTLSDNSQuery, _ time.Duration) (publicTLSDNSAnswer, error) {
				if q.server == "9.9.9.9" || outages == 2 && q.server == "1.1.1.1" {
					return publicTLSDNSAnswer{}, errors.New("untrusted private resolver output")
				}
				return f.ready(q), nil
			}
			p := environmentPublicTLSDNSPropagation{Root: "example.test", SetSHA: deploymentplan.Digest(f.expected), Nameservers: []string{"ns1.example.test", "ns2.example.test"}, ConvergedAt: f.start.Add(-10 * time.Minute), ReadyAfter: f.start}
			err := waitPublicTLSDNS01WithOps(t.Context(), "example.test", f.expected, map[string]string{"DNS_PROPAGATION_TIMEOUT_SECONDS": "30"}, &p, func(environmentPublicTLSDNSPropagation) error { t.Fatal("rewrote barrier"); return nil }, f.ops())
			if (err == nil) != (outages == 1) || err != nil && strings.Contains(err.Error(), "private resolver output") {
				t.Fatalf("two independent complete views required: outages=%d err=%v", outages, err)
			}
		})
	}
}

func TestPublicTLSDNSRejectsStaleOrForeignBarrierWithoutAuthorization(t *testing.T) {
	for _, mode := range []string{"foreign-set", "foreign-zone", "future-convergence", "short-barrier", "changed-authority", "wrong-authoritative-value", "recursive-negative", "foreign-name", "invalid-interval", "save-failure"} {
		t.Run(mode, func(t *testing.T) {
			f := newPublicTLSDNSFixture()
			p := environmentPublicTLSDNSPropagation{Root: "example.test", SetSHA: deploymentplan.Digest(f.expected), Nameservers: []string{"ns1.example.test", "ns2.example.test"}, ConvergedAt: f.start.Add(-10 * time.Minute), ReadyAfter: f.start}
			values := map[string]string{"DNS_PROPAGATION_TIMEOUT_SECONDS": "30"}
			prior := &p
			writes := 0
			save := func(environmentPublicTLSDNSPropagation) error { writes++; return nil }
			switch mode {
			case "foreign-set":
				p.SetSHA = strings.Repeat("0", 64)
			case "foreign-zone":
				p.Root = "foreign.test"
			case "future-convergence":
				p.ConvergedAt = f.start.Add(time.Second)
			case "short-barrier":
				p.ConvergedAt = f.start.Add(-time.Minute)
			case "changed-authority":
				p.Nameservers[1] = "ns3.example.test"
			case "wrong-authoritative-value":
				f.answers = func(q publicTLSDNSQuery, _ time.Duration) (publicTLSDNSAnswer, error) {
					a := f.ready(q)
					if q.authoritative && q.kind == "TXT" {
						a.txt = []string{"foreign-private-value"}
					}
					return a, nil
				}
			case "recursive-negative":
				f.answers = func(q publicTLSDNSQuery, _ time.Duration) (publicTLSDNSAnswer, error) {
					a := f.ready(q)
					if !q.authoritative && q.kind == "TXT" && q.server != "8.8.8.8" {
						a.status, a.txt = "NXDOMAIN", nil
					}
					return a, nil
				}
			case "foreign-name":
				f.expected["_acme-challenge.foreign.test"] = "foreign-private-value"
			case "invalid-interval":
				values["DNS_PROPAGATION_INTERVAL_SECONDS"] = "0"
			case "save-failure":
				prior = nil
				save = func(environmentPublicTLSDNSPropagation) error {
					writes++
					return errors.New("exclusive propagation receipt write failed")
				}
			}
			err := waitPublicTLSDNS01WithOps(t.Context(), "example.test", f.expected, values, prior, save, f.ops())
			if err == nil || strings.Contains(err.Error(), "private-value") || strings.Contains(err.Error(), "private-test-token") || writes != map[bool]int{true: 1, false: 0}[mode == "save-failure" || mode == "wrong-authoritative-value"] {
				t.Fatalf("invalid propagation was accepted or leaked values: err=%v writes=%d", err, writes)
			}
		})
	}
}

func TestPublicTLSDNSCancellationReapsQueriesAndDoesNotResetHorizon(t *testing.T) {
	for _, boundary := range []string{"already-cancelled", "query", "negative-cache-wait"} {
		t.Run(boundary, func(t *testing.T) {
			f := newPublicTLSDNSFixture()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			ops := f.ops()
			var mu sync.Mutex
			active := 0
			if boundary == "already-cancelled" {
				cancel()
			}
			if boundary == "query" {
				ops.query = func(ctx context.Context, q publicTLSDNSQuery) (publicTLSDNSAnswer, error) {
					mu.Lock()
					active++
					mu.Unlock()
					defer func() { mu.Lock(); active--; mu.Unlock() }()
					cancel()
					<-ctx.Done()
					return publicTLSDNSAnswer{}, ctx.Err()
				}
			}
			if boundary == "negative-cache-wait" {
				ops.wait = func(ctx context.Context, d time.Duration) error {
					if d != 600*time.Second {
						t.Errorf("batch delay=%v", d)
					}
					cancel()
					return ctx.Err()
				}
			}
			saves := 0
			err := waitPublicTLSDNS01WithOps(ctx, "example.test", f.expected, nil, nil, func(environmentPublicTLSDNSPropagation) error { saves++; return nil }, ops)
			if !errors.Is(err, context.Canceled) || active != 0 || saves != map[bool]int{true: 1, false: 0}[boundary == "negative-cache-wait"] {
				t.Fatalf("cancellation did not stop all DNS work: err=%v active=%d saves=%d", err, active, saves)
			}
		})
	}
}

func TestPublicTLSDNSParsesExactAnswersAndSOANegativeMinimum(t *testing.T) {
	q := publicTLSDNSQuery{"example.test", "SOA", "ns1.example.test", true}
	answer, err := parsePublicTLSDNS([]byte(";; ->>HEADER<<- opcode: QUERY, status: NOERROR, id: 1\n;; flags: qr aa; QUERY: 1, ANSWER: 1\nexample.test. 900 IN SOA ns1.example.test. hostmaster.example.test. 1 28800 7200 604800 600\n"), q)
	if err != nil || !answer.authoritative || !answer.soa || answer.negativeTTL != 600*time.Second {
		t.Fatalf("SOA parse: %+v %v", answer, err)
	}
	q.name, q.kind = "_acme-challenge.example.test", "TXT"
	answer, err = parsePublicTLSDNS([]byte(";; ->>HEADER<<- opcode: QUERY, status: NOERROR, id: 1\n;; flags: qr aa; QUERY: 1, ANSWER: 2\n_acme-challenge.foreign.test. 600 IN TXT \"foreign\"\n_acme-challenge.example.test. 600 IN TXT \"one\" \"-two\"\n"), q)
	if err != nil || len(answer.txt) != 1 || answer.txt[0] != "one-two" {
		t.Fatalf("TXT parse: %+v %v", answer, err)
	}
	for _, raw := range []string{"no header", ";; status: NOERROR,\n_acme-challenge.example.test. 600 IN TXT unquoted-secret"} {
		if _, err := parsePublicTLSDNS([]byte(raw), q); err == nil || strings.Contains(err.Error(), "unquoted-secret") {
			t.Fatal("malformed answer accepted or leaked")
		}
	}
}
