package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func runDeploymentDNSImport(args []string) error {
	fs := flag.NewFlagSet("deployment dns-import-state", flag.ContinueOnError)
	environment := fs.String("environment", "", "selected maintained environment")
	workspace := fs.String("workspace", "", "workspace root")
	root := fs.String("environment-root", "", "maintained environment root")
	source := fs.String("source-runtime", "", "absolute original runtime; never searched automatically")
	confirm := fs.String("confirm", "", "exact selected stack")
	if err := fs.Parse(args); errors.Is(err, flag.ErrHelp) {
		return nil
	} else if err != nil {
		return exitCode(2)
	}
	if fs.NArg() != 0 || !secretEnvironmentPattern.MatchString(*environment) || !filepath.IsAbs(*source) {
		return exitCode(2)
	}
	cfg, err := resolveDeploymentConfig(*workspace, *environment, *root)
	if err != nil || *confirm != cfg.Values["CLOUD_STACK_NAME"] {
		return exitCode(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	adapter, err := newDNSAdapter(cfg.DNSAdapter)
	if err != nil {
		return exitCode(2)
	}
	count, err := importDeploymentDNSState(ctx, cfg, *source, adapter)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: original DNS state or current zone/record ownership could not be verified; retain the source, repair access or drift, and retry; private destinations are never overwritten")
		return exitCode(1)
	}
	fmt.Fprintf(os.Stdout, "IMPORTED environment=%s files=%d location=~/.config/rtk_cloud/%s/deployment/dns/%s\n", cfg.Environment, count, cfg.Environment, cfg.DNSAdapter)
	fmt.Fprintln(os.Stdout, "Original source bytes are archived. No DNS records or credentials were changed. Repeat full preflight; import is not GO.")
	return nil
}

func importDeploymentDNSState(ctx context.Context, cfg deploymentConfig, source string, adapter dnsAdapter) (int, error) {
	configPath := filepath.Join(source, "env", "stack.env")
	configRaw, err := os.ReadFile(configPath)
	if err != nil {
		return 0, err
	}
	config, err := parseStrictEnv(configRaw, "original DNS configuration")
	if err != nil || config["CLOUD_ENV_NAME"] != cfg.Environment || config["CLOUD_STACK_NAME"] != cfg.Values["CLOUD_STACK_NAME"] || config["CLOUD_DNS_ROOT_DOMAIN"] != cfg.Values["CLOUD_DNS_ROOT_DOMAIN"] {
		return 0, errors.New("original DNS configuration belongs to another environment, stack or zone")
	}
	store, err := newSecretStore("", cfg.Environment)
	if err != nil {
		return 0, err
	}
	if _, err := os.Lstat(filepath.Join(store.Root, "deployment", ".execution-active")); !errors.Is(err, os.ErrNotExist) {
		return 0, errors.New("execution fence prevents DNS import")
	}
	if adapter == nil || adapter.Name() != cfg.DNSAdapter {
		return 0, errors.New("selected DNS adapter mismatch")
	}
	// Explicit import does not select legacy files for ordinary adapter execution.
	local := dnsContext(provisionPaths{}, appendMap(cfg.Values, cfg.DNSValues))
	local.Values["CLOUD_ENV_NAME"] = cfg.Environment
	if err := adapter.Validate(ctx, local); err != nil {
		return 0, err
	}
	zone, err := adapter.DiscoverZone(ctx, local)
	if err != nil {
		return 0, err
	}
	if _, err := adapter.GetRecordSet(ctx, local, zone, "_rtk-cloud-state-import-"+cfg.Environment+"."+zone.Name, "TXT"); err != nil {
		return 0, errors.New("authenticated selected zone read required")
	}
	originals, outputs, sources := map[string][]byte{}, map[string][]byte{}, map[string]string{}
	for _, name := range []string{"ownership.json", "challenges.json", "state.json", "dns.env"} {
		path := filepath.Join(source, "dns", adapter.Name(), name)
		if name == "dns.env" {
			path = filepath.Join(source, "state", name)
		}
		raw, err := readStorageEvidenceFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || len(raw) > 1<<20 {
			return 0, errors.New("bounded private original DNS source required")
		}
		originals[name], outputs[name], sources[name] = raw, raw, path
		switch name {
		case "ownership.json":
			var owned dnsOwnership
			if decodeEnvironmentJSON(raw, &owned) != nil || owned.Adapter != adapter.Name() || owned.RootDomain != zone.Name || owned.Environment != "" && owned.Environment != cfg.Environment || owned.Records == nil {
				return 0, errors.New("original DNS ownership is invalid")
			}
			for _, record := range owned.Records {
				if err := validateDNSRecord(zone.Name, record); err != nil {
					return 0, err
				}
				current, err := adapter.GetRecordSet(ctx, local, zone, record.Name, record.Type)
				if err != nil || strings.Join(mergeDNSValues(current.Values), "\x00") != strings.Join(mergeDNSValues(record.Values), "\x00") {
					return 0, errors.New("original DNS record has drifted")
				}
			}
			var fields map[string]any
			if json.Unmarshal(raw, &fields) != nil {
				return 0, errors.New("invalid original DNS object")
			}
			fields["environment"] = cfg.Environment
			outputs[name], err = json.MarshalIndent(fields, "", "  ")
			if err != nil {
				return 0, err
			}
			outputs[name] = append(outputs[name], '\n')
		case "challenges.json":
			var values []dnsChallengeState
			if decodeEnvironmentJSON(raw, &values) != nil || values == nil {
				return 0, errors.New("invalid original DNS challenges")
			}
			for _, value := range values {
				name, err := dns01RecordName(zone.Name, value.Domain)
				if err != nil || value.Value == "" {
					return 0, errors.New("invalid original DNS challenge")
				}
				current, err := adapter.GetRecordSet(ctx, local, zone, name, "TXT")
				if err != nil || !slices.Contains(current.Values, value.Value) {
					return 0, errors.New("original DNS challenge value cannot be verified")
				}
			}
		case "state.json":
			var state dnsProviderState
			if decodeEnvironmentJSON(raw, &state) != nil || state.Adapter != adapter.Name() || state.Zone != zone.Name || state.ID != zone.ID {
				return 0, errors.New("original DNS provider zone binding changed")
			}
		case "dns.env":
			state, err := parseStrictEnv(raw, "original DNS metadata")
			if err != nil || state["DNS_ADAPTER"] != adapter.Name() || state["DNS_ROOT_DOMAIN"] != zone.Name {
				return 0, errors.New("original DNS metadata is invalid")
			}
			for key := range state {
				if !keySet("DNS_ADAPTER", "DNS_ROOT_DOMAIN", "DNS_RECORDS_APPLIED", "DNS_UPDATED_AT")[key] {
					return 0, errors.New("original DNS metadata contains unrelated fields")
				}
			}
		}
	}
	if len(originals) == 0 {
		return 0, errors.New("no supported original DNS state")
	}
	if current, err := os.ReadFile(configPath); err != nil || !bytes.Equal(current, configRaw) {
		return 0, errors.New("original DNS configuration changed")
	}
	for name, raw := range originals {
		if current, err := readStorageEvidenceFile(sources[name]); err != nil || !bytes.Equal(current, raw) {
			return 0, errors.New("original DNS source changed")
		}
		relative, _ := dnsStateRelative(adapter.Name(), name)
		path, err := privateDeploymentStatePath(cfg.Environment, relative)
		if err != nil {
			return 0, err
		}
		if current, err := readStorageEvidenceFile(path); err == nil {
			if !bytes.Equal(current, outputs[name]) {
				return 0, errors.New("private DNS destination conflicts with original source")
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return 0, err
		}
	}
	archive := filepath.Join("migration-backup", "dns-state", deploymentplan.Digest(originals))
	for _, name := range []string{"ownership.json", "challenges.json", "state.json", "dns.env"} {
		if originals[name] == nil {
			continue
		}
		for _, target := range []struct {
			Relative string
			Raw      []byte
		}{{filepath.Join(archive, name), originals[name]}, {filepath.Join("deployment", "dns", adapter.Name(), name), outputs[name]}} {
			path, err := privateDeploymentStatePath(cfg.Environment, target.Relative)
			if err != nil {
				return 0, err
			}
			if err := ensurePrivateDirectory(filepath.Dir(path)); err != nil {
				return 0, err
			}
			if err := publishProviderImportFile(path, target.Raw); err != nil {
				return 0, err
			}
		}
	}
	return len(outputs), nil
}
