package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/envroot"
)

func runDNSHook(args []string) error {
	if len(args) == 0 || (args[0] != "present" && args[0] != "cleanup") {
		return errors.New("dns-hook requires present or cleanup")
	}
	action := args[0]
	fs := flag.NewFlagSet("dns-hook "+action, flag.ContinueOnError)
	envRoot := fs.String("env-root", "", "normalized environment runtime root")
	operatorEnv := fs.String("operator-env", "", "operator secret env file")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *envRoot == "" {
		return errors.New("--env-root is required")
	}
	env, err := envroot.Load(*envRoot, "")
	if err != nil {
		return err
	}
	paths := provisionPaths{EnvRoot: *envRoot, OperatorEnv: *operatorEnv}
	adapter, adapterCtx, zone, err := selectedDNSAdapter(paths, env.Values)
	if err != nil {
		return err
	}
	domain := os.Getenv("CERTBOT_DOMAIN")
	validation := os.Getenv("CERTBOT_VALIDATION")
	if domain == "" || validation == "" {
		return errors.New("CERTBOT_DOMAIN and CERTBOT_VALIDATION are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), dnsOperationTimeout(adapterCtx.Values))
	defer cancel()
	if action == "cleanup" {
		if err := adapter.CleanupDNS01Challenge(ctx, adapterCtx, zone, domain, validation); err != nil {
			return err
		}
		return forgetDNSChallenge(adapterCtx, adapter.Name(), domain, validation)
	}
	if err := adapter.PresentDNS01Challenge(ctx, adapterCtx, zone, domain, validation); err != nil {
		return err
	}
	if err := recordDNSChallenge(adapterCtx, adapter.Name(), domain, validation); err != nil {
		return err
	}
	name, err := dns01RecordName(zone.Name, domain)
	if err != nil {
		return err
	}
	if err := waitDNSTXT(ctx, name, validation, adapterCtx.Values); err != nil {
		return err
	}
	return waitACMEFinalDNSSettle(ctx, os.Getenv("CERTBOT_REMAINING_CHALLENGES"))
}

func waitACMEFinalDNSSettle(ctx context.Context, remaining string) error {
	if strings.TrimSpace(remaining) != "0" {
		return nil
	}
	seconds, _ := strconv.Atoi(firstNonEmpty(os.Getenv("DNS_ACME_FINAL_SETTLE_SECONDS"), "60"))
	if seconds <= 0 {
		return nil
	}
	timer := time.NewTimer(time.Duration(seconds) * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("ACME DNS final settle interrupted: %w", ctx.Err())
	case <-timer.C:
		return nil
	}
}

type dnsChallengeState struct {
	Domain string `json:"domain"`
	Value  string `json:"value"`
}

func dnsChallengeStatePath(ctx dnsAdapterContext, adapter string) string {
	return filepath.Join(ctx.RuntimeRoot, "dns", adapter, "challenges.json")
}

func readDNSChallenges(ctx dnsAdapterContext, adapter string) ([]dnsChallengeState, error) {
	body, err := os.ReadFile(dnsChallengeStatePath(ctx, adapter))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []dnsChallengeState
	return out, json.Unmarshal(body, &out)
}

func writeDNSChallenges(ctx dnsAdapterContext, adapter string, values []dnsChallengeState) error {
	path := dnsChallengeStatePath(ctx, adapter)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if len(values) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	body, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(body, '\n'), 0o600)
}

func recordDNSChallenge(ctx dnsAdapterContext, adapter, domain, value string) error {
	values, err := readDNSChallenges(ctx, adapter)
	if err != nil {
		return err
	}
	for _, item := range values {
		if item.Domain == domain && item.Value == value {
			return nil
		}
	}
	return writeDNSChallenges(ctx, adapter, append(values, dnsChallengeState{Domain: domain, Value: value}))
}

func forgetDNSChallenge(ctx dnsAdapterContext, adapter, domain, value string) error {
	values, err := readDNSChallenges(ctx, adapter)
	if err != nil {
		return err
	}
	remaining := values[:0]
	for _, item := range values {
		if item.Domain != domain || item.Value != value {
			remaining = append(remaining, item)
		}
	}
	return writeDNSChallenges(ctx, adapter, remaining)
}

func cleanupRecordedDNSChallenges(paths provisionPaths, env map[string]string) error {
	adapterName := firstNonEmpty(env["DNS_ADAPTER"], "godaddy")
	preflightCtx := dnsContext(paths, env)
	values, err := readDNSChallenges(preflightCtx, adapterName)
	if err != nil || len(values) == 0 {
		return err
	}
	adapter, adapterCtx, zone, err := selectedDNSAdapter(paths, env)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), dnsOperationTimeout(adapterCtx.Values))
	defer cancel()
	for _, item := range values {
		if err := adapter.CleanupDNS01Challenge(ctx, adapterCtx, zone, item.Domain, item.Value); err != nil {
			return err
		}
	}
	return writeDNSChallenges(adapterCtx, adapter.Name(), nil)
}

func dnsOperationTimeout(values map[string]string) time.Duration {
	seconds, _ := strconv.Atoi(firstNonEmpty(values["DNS_PROPAGATION_TIMEOUT_SECONDS"], "900"))
	return time.Duration(seconds+60) * time.Second
}

func waitDNSTXT(ctx context.Context, domain, value string, values map[string]string) error {
	interval, _ := strconv.Atoi(firstNonEmpty(values["DNS_PROPAGATION_INTERVAL_SECONDS"], "10"))
	ticker := time.NewTicker(time.Duration(interval) * time.Second)
	defer ticker.Stop()
	for {
		out, _ := execCommandContext(ctx, "dig", "+short", "TXT", domain, "@8.8.8.8")
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Trim(strings.TrimSpace(line), `"`) == value {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("DNS TXT did not converge for %s: %w", domain, ctx.Err())
		case <-ticker.C:
		}
	}
}

var execCommandContext = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

func certbotDNSHookScript(binary, envRoot, operatorEnv, action string) string {
	return fmt.Sprintf("#!/usr/bin/env bash\nset -euo pipefail\nexec %s dns-hook %s --env-root %s --operator-env %s\n", shellSingleQuote(binary), shellSingleQuote(action), shellSingleQuote(envRoot), shellSingleQuote(operatorEnv))
}
