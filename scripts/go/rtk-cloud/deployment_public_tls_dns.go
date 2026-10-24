package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// This records one coherent batch, rather than restarting a per-name delay on
// recovery. It contains no challenge values. The owning ACME attempt persists it.
type environmentPublicTLSDNSPropagation struct {
	Root        string    `json:"root"`
	SetSHA      string    `json:"set_sha256"`
	Nameservers []string  `json:"nameservers"`
	ConvergedAt time.Time `json:"converged_at"`
	ReadyAfter  time.Time `json:"ready_after"`
	UnstableAt  time.Time `json:"unstable_at,omitempty"`
}

type publicTLSDNSAnswer struct {
	status        string
	authoritative bool
	txt           []string
	ns            []string
	negativeTTL   time.Duration
	soa           bool
}

type publicTLSDNSQuery struct {
	name, kind, server string
	authoritative      bool
}

type publicTLSDNSWaitOps struct {
	query func(context.Context, publicTLSDNSQuery) (publicTLSDNSAnswer, error)
	now   func() time.Time
	wait  func(context.Context, time.Duration) error
}

var publicTLSDNSResolvers = []string{"8.8.8.8", "1.1.1.1", "9.9.9.9"}

func waitPublicTLSDNS01(ctx context.Context, root string, expected map[string]string, values map[string]string, prior *environmentPublicTLSDNSPropagation, save func(environmentPublicTLSDNSPropagation) error) error {
	return waitPublicTLSDNS01WithOps(ctx, root, expected, values, prior, save, publicTLSDNSWaitOps{query: queryPublicTLSDNS, now: time.Now, wait: waitPublicTLSDNSDuration})
}

func waitPublicTLSDNSDuration(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func waitPublicTLSDNS01WithOps(ctx context.Context, root string, expected map[string]string, values map[string]string, prior *environmentPublicTLSDNSPropagation, save func(environmentPublicTLSDNSPropagation) error, ops publicTLSDNSWaitOps) error {
	root = strings.TrimSuffix(root, ".")
	if validateManagedDNSHostname(root) != nil || len(expected) == 0 || ops.query == nil || ops.now == nil || ops.wait == nil || save == nil {
		return publicTLSBlock("DNS-01 propagation requires the complete original challenge set and selected zone")
	}
	names := make([]string, 0, len(expected))
	for name, value := range expected {
		host := strings.TrimPrefix(name, "_acme-challenge.")
		if host == name || validateManagedDNSHostname(host) != nil || (host != root && !strings.HasSuffix(host, "."+root)) || value == "" {
			return publicTLSBlock("DNS-01 propagation contains an invalid or foreign challenge name")
		}
		names = append(names, name)
	}
	sort.Strings(names)
	setSHA := deploymentplan.Digest(expected)
	interval, err := publicTLSDNSSeconds(values, "DNS_PROPAGATION_INTERVAL_SECONDS", 10)
	if err != nil {
		return err
	}
	limit, err := publicTLSDNSSeconds(values, "DNS_PROPAGATION_TIMEOUT_SECONDS", 900)
	if err != nil {
		return err
	}
	end := ops.now().Add(limit)
	var bound *environmentPublicTLSDNSPropagation
	if prior != nil {
		copy := *prior
		copy.Nameservers = append([]string(nil), prior.Nameservers...)
		bound = &copy
		if copy.Root != root || copy.SetSHA != setSHA || copy.ConvergedAt.IsZero() || copy.ConvergedAt.After(ops.now()) || copy.UnstableAt.After(ops.now()) || copy.ReadyAfter.Before(copy.ConvergedAt) || !copy.UnstableAt.IsZero() && copy.ReadyAfter.Before(copy.UnstableAt) || len(copy.Nameservers) == 0 || !slices.IsSorted(copy.Nameservers) || len(slices.Compact(slices.Clone(copy.Nameservers))) != len(copy.Nameservers) {
			return publicTLSBlock("the original DNS-01 propagation barrier differs from this order and zone")
		}
		if retryEnd := copy.ReadyAfter.Add(limit); retryEnd.After(end) {
			end = retryEnd
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("DNS-01 propagation interrupted: %w", err)
		}
		if !ops.now().Before(end) {
			return publicTLSBlock("DNS-01 propagation did not converge before the selected DNS timeout")
		}
		servers, discoveryErr := publicTLSDNSNameservers(ctx, root, ops)
		if discoveryErr == nil && bound != nil && !slices.Equal(servers, bound.Nameservers) {
			return publicTLSBlock("the original DNS-01 propagation nameservers changed")
		}
		if discoveryErr == nil {
			authoritative, recursive, ttl, observationErr := publicTLSDNSBatch(ctx, root, names, expected, servers, ops)
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("DNS-01 propagation interrupted: %w", err)
			}
			if observationErr == nil && bound != nil {
				if bound.ReadyAfter.Before(bound.ConvergedAt.Add(ttl)) {
					return publicTLSBlock("the original DNS-01 propagation barrier differs from this order and zone")
				}
				if !authoritative && (bound.UnstableAt.IsZero() || bound.UnstableAt.Before(bound.ConvergedAt)) {
					// Persist invalidation before waiting: a process interruption
					// must not let the next retry reuse an expired stable horizon.
					changed := *bound
					changed.UnstableAt = ops.now().UTC()
					if after := changed.UnstableAt.Add(ttl); after.After(changed.ReadyAfter) {
						changed.ReadyAfter = after
					}
					if err := save(changed); err != nil {
						return err
					}
					bound = &changed
				}
			}
			if observationErr == nil && authoritative {
				if bound == nil {
					now := ops.now().UTC()
					candidate := environmentPublicTLSDNSPropagation{Root: root, SetSHA: setSHA, Nameservers: append([]string(nil), servers...), ConvergedAt: now, ReadyAfter: now.Add(ttl)}
					if err := save(candidate); err != nil {
						return err
					}
					bound = &candidate
				}
				if !bound.UnstableAt.IsZero() && !bound.UnstableAt.Before(bound.ConvergedAt) {
					// An intermittent authoritative view is not a terminal order
					// failure. Require another real cache horizon after recovery.
					changed := *bound
					changed.ConvergedAt = ops.now().UTC()
					if after := changed.ConvergedAt.Add(ttl); after.After(changed.ReadyAfter) {
						changed.ReadyAfter = after
					}
					if err := save(changed); err != nil {
						return err
					}
					bound = &changed
				}
				readyAfter := bound.ReadyAfter
				// The configured timeout is the convergence allowance, not a
				// cap that makes a legitimate SOA cache horizon impossible.
				// The original caller deadline still bounds every query/wait.
				if convergenceEnd := readyAfter.Add(limit); convergenceEnd.After(end) {
					end = convergenceEnd
				}
				if recursive && !ops.now().Before(readyAfter) {
					return ctx.Err()
				}
				// The whole set shares one negative-cache horizon. Recheck all
				// views after it expires instead of accepting a cached observation.
				if recursive && ops.now().Before(readyAfter) {
					delay := readyAfter.Sub(ops.now())
					if remaining := end.Sub(ops.now()); delay > remaining {
						delay = remaining
					}
					if err := ops.wait(ctx, delay); err != nil {
						return fmt.Errorf("DNS-01 propagation interrupted: %w", err)
					}
					continue
				}
			}
		}
		delay := interval
		if remaining := end.Sub(ops.now()); delay > remaining {
			delay = remaining
		}
		if err := ops.wait(ctx, delay); err != nil {
			return fmt.Errorf("DNS-01 propagation interrupted: %w", err)
		}
	}
}

func publicTLSDNSSeconds(values map[string]string, key string, fallback int) (time.Duration, error) {
	raw := firstNonEmpty(values[key], strconv.Itoa(fallback))
	seconds, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || seconds <= 0 {
		return 0, publicTLSBlock("DNS-01 propagation requires a positive selected " + key)
	}
	return time.Duration(seconds) * time.Second, nil
}

func publicTLSDNSNameservers(ctx context.Context, root string, ops publicTLSDNSWaitOps) ([]string, error) {
	queries := make([]publicTLSDNSQuery, 0, len(publicTLSDNSResolvers))
	for _, server := range publicTLSDNSResolvers {
		queries = append(queries, publicTLSDNSQuery{root, "NS", server, false})
	}
	answers, err := publicTLSDNSQueries(ctx, queries, ops)
	if err != nil {
		return nil, err
	}
	groups := map[string]int{}
	var servers []string
	for _, answer := range answers {
		if answer.status != "NOERROR" || len(answer.ns) == 0 {
			continue
		}
		current := append([]string(nil), answer.ns...)
		sort.Strings(current)
		current = slices.Compact(current)
		for _, name := range current {
			if validateManagedDNSHostname(name) != nil {
				return nil, errors.New("selected DNS nameserver name is invalid")
			}
		}
		key := strings.Join(current, " ")
		groups[key]++
		if groups[key] >= 2 {
			servers = current
		}
	}
	if len(servers) == 0 {
		return nil, errors.New("selected DNS nameservers have not converged in two independent recursive views")
	}
	return servers, nil
}

func publicTLSDNSBatch(ctx context.Context, root string, names []string, expected map[string]string, servers []string, ops publicTLSDNSWaitOps) (bool, bool, time.Duration, error) {
	var queries []publicTLSDNSQuery
	for _, server := range servers {
		queries = append(queries, publicTLSDNSQuery{root, "SOA", server, true})
		for _, name := range names {
			queries = append(queries, publicTLSDNSQuery{name, "TXT", server, true})
		}
	}
	for _, server := range publicTLSDNSResolvers {
		for _, name := range names {
			queries = append(queries, publicTLSDNSQuery{name, "TXT", server, false})
		}
	}
	answers, err := publicTLSDNSQueries(ctx, queries, ops)
	if err != nil {
		return false, false, 0, err
	}
	authoritative := true
	resolverReady := map[string]bool{}
	for _, server := range publicTLSDNSResolvers {
		resolverReady[server] = true
	}
	var ttl time.Duration
	for i, q := range queries {
		answer := answers[i]
		if q.kind == "SOA" {
			if answer.status != "NOERROR" || !answer.authoritative || !answer.soa || answer.negativeTTL < 0 {
				return false, false, 0, errors.New("the selected authoritative SOA negative-cache lifetime is unavailable")
			}
			if answer.negativeTTL > ttl {
				ttl = answer.negativeTTL
			}
			continue
		}
		visible := answer.status == "NOERROR" && slices.Contains(answer.txt, expected[q.name])
		if q.authoritative {
			if answer.status == "" {
				return false, false, 0, errors.New("an authoritative DNS-01 query is temporarily unavailable")
			}
			authoritative = authoritative && answer.authoritative && visible
		} else {
			resolverReady[q.server] = resolverReady[q.server] && visible
		}
	}
	ready := 0
	for _, visible := range resolverReady {
		if visible {
			ready++
		}
	}
	return authoritative, ready >= 2, ttl, nil
}

func publicTLSDNSQueries(ctx context.Context, queries []publicTLSDNSQuery, ops publicTLSDNSWaitOps) ([]publicTLSDNSAnswer, error) {
	answers := make([]publicTLSDNSAnswer, len(queries))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, q := range queries {
		wg.Add(1)
		go func(i int, q publicTLSDNSQuery) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			answer, err := ops.query(ctx, q)
			if err == nil {
				answers[i] = answer
			}
		}(i, q)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return answers, nil
}

func queryPublicTLSDNS(ctx context.Context, q publicTLSDNSQuery) (publicTLSDNSAnswer, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	args := []string{"+time=2", "+tries=1", "+noall", "+answer", "+authority", "+comments"}
	if q.authoritative {
		args = append(args, "+norecurse")
	}
	args = append(args, q.name, q.kind, "@"+q.server)
	raw, err := exec.CommandContext(ctx, "dig", args...).Output()
	if err != nil {
		if ctx.Err() != nil {
			return publicTLSDNSAnswer{}, ctx.Err()
		}
		return publicTLSDNSAnswer{}, errors.New("DNS-01 propagation query failed")
	}
	return parsePublicTLSDNS(raw, q)
}

func parsePublicTLSDNS(raw []byte, q publicTLSDNSQuery) (publicTLSDNSAnswer, error) {
	var result publicTLSDNSAnswer
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, ";;") {
			if i := strings.Index(line, "status: "); i >= 0 {
				result.status = strings.SplitN(line[i+8:], ",", 2)[0]
			}
			if strings.HasPrefix(line, ";; flags: ") {
				flags := strings.Fields(strings.SplitN(strings.TrimPrefix(line, ";; flags: "), ";", 2)[0])
				result.authoritative = slices.Contains(flags, "aa")
			}
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 || strings.TrimSuffix(fields[0], ".") != q.name || fields[2] != "IN" {
			continue
		}
		switch fields[3] {
		case "TXT":
			var value strings.Builder
			for _, part := range fields[4:] {
				decoded, err := strconv.Unquote(part)
				if err != nil {
					return result, errors.New("DNS-01 TXT answer is malformed")
				}
				value.WriteString(decoded)
			}
			result.txt = append(result.txt, value.String())
		case "NS":
			result.ns = append(result.ns, strings.TrimSuffix(fields[4], "."))
		case "SOA":
			if len(fields) != 11 {
				return result, errors.New("DNS-01 SOA answer is malformed")
			}
			ttl, err1 := strconv.ParseUint(fields[1], 10, 32)
			minimum, err2 := strconv.ParseUint(fields[10], 10, 32)
			if err1 != nil || err2 != nil {
				return result, errors.New("DNS-01 SOA lifetime is malformed")
			}
			result.negativeTTL = time.Duration(min(ttl, minimum)) * time.Second
			result.soa = true
		}
	}
	if result.status == "" {
		return result, errors.New("DNS-01 query returned no DNS response status")
	}
	return result, nil
}
