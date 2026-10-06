package main

import (
	"errors"
	"os"
	"path/filepath"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func dnsStateEnvironment(ctx dnsAdapterContext) (string, error) {
	environment := ctx.Values["CLOUD_ENV_NAME"]
	if !secretEnvironmentPattern.MatchString(environment) {
		return "", dnsStateBlocker("explicit configured CLOUD_ENV_NAME is required")
	}
	if ctx.RuntimeRoot != "" {
		values, err := readOptionalStrictEnv(filepath.Join(ctx.RuntimeRoot, "env", "stack.env"))
		if err != nil || values["CLOUD_ENV_NAME"] != "" && values["CLOUD_ENV_NAME"] != environment {
			return "", dnsStateBlocker("DNS environment differs from the selected configuration")
		}
	}
	return environment, nil
}

func dnsStateBlocker(cause string) error {
	return &deploymentplan.Blocker{ID: "deployment.dns.state", Cause: cause, Next: "restore original private deployment/dns/<adapter> state, or explicitly verify and import original records with deployment dns-import-state; do not discard ownership or ACME cleanup evidence"}
}

func dnsStateRelative(adapter, name string) (string, error) {
	if !keySet("godaddy", "route53")[adapter] || !keySet("ownership.json", "challenges.json", "state.json", "dns.env")[name] {
		return "", dnsStateBlocker("unknown DNS adapter or state file")
	}
	return filepath.Join("deployment", "dns", adapter, name), nil
}

func dnsLocalStatePath(ctx dnsAdapterContext, adapter, name string) (string, error) {
	environment, err := dnsStateEnvironment(ctx)
	if err != nil {
		return "", err
	}
	relative, err := dnsStateRelative(adapter, name)
	if err != nil {
		return "", err
	}
	path, err := privateDeploymentStatePath(environment, relative)
	if err != nil {
		return "", dnsStateBlocker("private DNS path or permissions cannot be verified")
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) && ctx.RuntimeRoot != "" {
		legacy := filepath.Join(ctx.RuntimeRoot, "dns", adapter, name)
		if name == "dns.env" {
			legacy = filepath.Join(ctx.RuntimeRoot, "state", name)
		}
		if _, err := os.Lstat(legacy); !errors.Is(err, os.ErrNotExist) {
			return "", dnsStateBlocker("legacy DNS state exists or cannot be inspected; it cannot be read as a fallback")
		}
	}
	return path, nil
}

func readDNSLocalState(ctx dnsAdapterContext, adapter, name string) ([]byte, error) {
	path, err := dnsLocalStatePath(ctx, adapter, name)
	if err != nil {
		return nil, err
	}
	raw, err := readStorageEvidenceFile(path)
	if err != nil {
		return nil, err
	}
	if len(raw) > 1<<20 {
		return nil, dnsStateBlocker("DNS state exceeds its bound")
	}
	return raw, nil
}

func writeDNSLocalState(ctx dnsAdapterContext, adapter, name string, raw []byte) error {
	if len(raw) > 1<<20 {
		return dnsStateBlocker("DNS state exceeds its bound")
	}
	if _, err := dnsLocalStatePath(ctx, adapter, name); err != nil {
		return err
	}
	environment, err := dnsStateEnvironment(ctx)
	if err != nil {
		return err
	}
	relative, err := dnsStateRelative(adapter, name)
	if err != nil {
		return err
	}
	return writePrivateDeploymentState(environment, relative, raw)
}

type dnsOwnership struct {
	Adapter     string         `json:"adapter"`
	RootDomain  string         `json:"root_domain"`
	Environment string         `json:"environment"`
	Records     []dnsRecordSet `json:"records"`
	UpdatedAt   string         `json:"updated_at"`
}

func readDNSOwnership(ctx dnsAdapterContext, adapter string) (dnsOwnership, error) {
	raw, err := readDNSLocalState(ctx, adapter, "ownership.json")
	if errors.Is(err, os.ErrNotExist) {
		return dnsOwnership{}, nil
	}
	if err != nil {
		return dnsOwnership{}, err
	}
	var owned dnsOwnership
	if decodeEnvironmentJSON(raw, &owned) != nil || owned.Adapter != adapter || owned.RootDomain != ctx.RootDomain || owned.Environment != ctx.Values["CLOUD_ENV_NAME"] || owned.Records == nil {
		return owned, dnsStateBlocker("DNS ownership does not match the selected environment, adapter and zone")
	}
	for _, record := range owned.Records {
		if err := validateDNSRecord(ctx.RootDomain, record); err != nil {
			return owned, dnsStateBlocker("DNS ownership contains invalid or foreign records")
		}
	}
	return owned, nil
}

func validateDNSLocalState(ctx dnsAdapterContext, adapter string) error {
	for _, name := range []string{"ownership.json", "challenges.json", "state.json", "dns.env"} {
		if _, err := dnsLocalStatePath(ctx, adapter, name); err != nil {
			return err
		}
	}
	if _, err := readDNSOwnership(ctx, adapter); err != nil {
		return err
	}
	if _, err := readDNSChallenges(ctx, adapter); err != nil {
		return err
	}
	for _, name := range []string{"state.json", "dns.env"} {
		raw, err := readDNSLocalState(ctx, adapter, name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if name == "state.json" {
			var state dnsProviderState
			if decodeEnvironmentJSON(raw, &state) != nil || state.Adapter != adapter || state.Zone != ctx.RootDomain {
				return dnsStateBlocker("DNS provider state belongs to another adapter or zone")
			}
		} else {
			values, err := parseStrictEnv(raw, "DNS metadata")
			if err != nil || values["DNS_ADAPTER"] != adapter || values["DNS_ROOT_DOMAIN"] != ctx.RootDomain {
				return dnsStateBlocker("DNS metadata belongs to another adapter or zone")
			}
		}
	}
	return nil
}

type dnsProviderState struct {
	Adapter string `json:"adapter"`
	Zone    string `json:"zone_name"`
	ID      string `json:"hosted_zone_id,omitempty"`
}

func environmentDNSStateDigest(cfg deploymentConfig) (string, error) {
	ctx := dnsContext(provisionPaths{EnvRoot: cfg.RuntimeRoot}, appendMap(cfg.Values, cfg.DNSValues))
	ctx.Values["CLOUD_ENV_NAME"] = cfg.Environment
	if err := validateDNSLocalState(ctx, cfg.DNSAdapter); err != nil {
		return "", err
	}
	files := map[string][]byte{}
	for _, name := range []string{"ownership.json", "challenges.json", "state.json", "dns.env"} {
		raw, err := readDNSLocalState(ctx, cfg.DNSAdapter, name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		files[name] = raw
	}
	return deploymentplan.Digest(files), nil
}
