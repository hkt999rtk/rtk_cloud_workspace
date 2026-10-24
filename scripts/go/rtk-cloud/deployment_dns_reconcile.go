package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type dnsReconciliationRequest struct {
	Confirm, ReviewSHA256, Report string
}

type dnsReconciliationInstance struct {
	ID     int    `json:"id"`
	Label  string `json:"label"`
	Region string `json:"region"`
	Role   string `json:"role"`
	IPv4   string `json:"ipv4"`
}

type dnsReconciliationMapping struct {
	Record  dnsRecordSet                `json:"record"`
	Targets []dnsReconciliationInstance `json:"targets"`
}

type dnsReconciliationExclusion struct {
	Name   string   `json:"hostname"`
	Type   string   `json:"type"`
	Values []string `json:"values"`
	Reason string   `json:"reason"`
}

type deploymentDNSReconciliation struct {
	Schema         int                          `json:"schema"`
	Method         string                       `json:"method"`
	Scope          string                       `json:"scope"`
	Environment    string                       `json:"environment"`
	Stack          string                       `json:"stack"`
	Adapter        string                       `json:"adapter"`
	RootDomain     string                       `json:"root_domain"`
	ZoneID         string                       `json:"hosted_zone_id,omitempty"`
	Cluster        lkeCluster                   `json:"cluster"`
	ConfigSHA256   string                       `json:"config_sha256"`
	MetadataSHA256 map[string]string            `json:"metadata_sha256"`
	Mappings       []dnsReconciliationMapping   `json:"mappings"`
	Excluded       []dnsReconciliationExclusion `json:"excluded"`
	ReviewSHA256   string                       `json:"review_sha256"`
	ApprovedAt     string                       `json:"approved_at,omitempty"`
}

func runDeploymentDNSReconcile(args []string) error {
	fs := flag.NewFlagSet("deployment dns-reconcile-state", flag.ContinueOnError)
	workspace := fs.String("workspace", "", "workspace root")
	environment := fs.String("environment", "", "selected maintained environment")
	confirm := fs.String("confirm", "", "exact stack; publishes reviewed current-state evidence only")
	review := fs.String("review-sha256", "", "exact SHA-256 from read-only review")
	report := fs.String("report", "", "optional absolute canonical deployment/reports review JSON")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 || !secretEnvironmentPattern.MatchString(*environment) {
		return errors.New("explicit --environment is required")
	}
	cfg, err := resolveDeploymentConfig(*workspace, *environment, "")
	if err != nil {
		return err
	}
	adapter, err := newDNSAdapter(cfg.DNSAdapter)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	return reconcileDeploymentDNSState(ctx, cfg, dnsReconciliationRequest{*confirm, *review, *report}, readRemovalRecoveryNative, adapter, os.Stdout)
}

func dnsReconciliationContext(cfg deploymentConfig) dnsAdapterContext {
	values := appendMap(cfg.Values, cfg.DNSValues)
	values["CLOUD_ENV_NAME"] = cfg.Environment
	return dnsContext(provisionPaths{EnvRoot: cfg.RuntimeRoot}, values)
}

func dnsReconciliationConfigSHA(cfg deploymentConfig) string {
	return deploymentplan.Digest(struct {
		Values, DNS, Adapter map[string]string
		Candidates           map[string]string
	}{deploymentEndpointValues(cfg), cfg.DNSValues, cfg.AdapterResolved, dnsReconciliationCandidates(cfg)})
}

func dnsReconciliationSHA(receipt deploymentDNSReconciliation) string {
	receipt.ReviewSHA256, receipt.ApprovedAt = "", ""
	return deploymentplan.Digest(receipt)
}

func dnsReconciliationMetadata(cfg deploymentConfig) (map[string]string, error) {
	local := dnsReconciliationContext(cfg)
	if err := validateDNSLocalState(local, cfg.DNSAdapter); err != nil {
		return nil, err
	}
	challenges, err := readDNSChallenges(local, cfg.DNSAdapter)
	if err != nil || len(challenges) != 0 {
		return nil, errors.New("original ACME cleanup evidence remains unresolved")
	}
	out := map[string]string{}
	for _, name := range []string{"challenges.json", "state.json", "dns.env"} {
		raw, err := readDNSLocalState(local, cfg.DNSAdapter, name)
		if errors.Is(err, os.ErrNotExist) {
			out[name] = "absent"
			continue
		}
		if err != nil {
			return nil, err
		}
		out[name] = deploymentplan.Digest(raw)
	}
	return out, nil
}

func dnsReconciliationToken(cfg deploymentConfig) (string, error) {
	path, err := privateDeploymentStatePath(cfg.Environment, "operator/env/LINODE_TOKEN")
	if err != nil {
		return "", err
	}
	raw, err := readStorageEvidenceFile(path)
	token := strings.TrimSpace(string(raw))
	if err != nil || token == "" || strings.ContainsAny(token, "\r\n\x00") {
		return "", errors.New("canonical selected-environment Linode credentials are required")
	}
	return token, nil
}

func dnsReconciliationCandidates(cfg deploymentConfig) map[string]string {
	out := map[string]string{}
	// These registered role names use maintained resolved fields only. Legacy
	// deployment hostname helpers consult process overrides and cannot grant
	// reconciliation authority to an arbitrary same-zone hostname.
	values := deploymentEndpointValues(cfg)
	base := values["VIDEO_CLOUD_DOMAIN"]
	for _, name := range []string{base, firstNonEmpty(values["VIDEO_CLOUD_DEVICE_DOMAIN"], "device."+base), values["VIDEO_CLOUD_CERTISSUER_DOMAIN"], firstNonEmpty(values["LKE_TURN_REGISTRY_PUBLIC_DOMAIN"], "turnregistry."+base), values["ACCOUNT_MANAGER_DOMAIN"], firstNonEmpty(values["BILLING_DOMAIN"], "billing."+base), firstNonEmpty(values["PAYMENT_SIMULATOR_DOMAIN"], "payment-simulator."+base), values["CLOUD_ADMIN_DOMAIN"], firstNonEmpty(values["FRONTEND_DOMAIN"], "frontend."+base), values["CLOUD_LOGGER_DOMAIN"], values["FACTORY_ENROLL_DOMAIN"]} {
		if name != "" {
			out[name] = "edge-haproxy"
		}
	}
	count, _ := strconv.Atoi(firstNonEmpty(values["TURN_REPLICAS"], "1"))
	for i := 1; i <= count && i <= 100; i++ {
		name := dnsReconciliationTURNName(values, count, i)
		if old := out[name]; old != "" && old != "coturn-vm" {
			out[name] = "ambiguous-registered-role"
		} else {
			out[name] = "coturn-vm"
		}
	}
	return out
}

func dnsReconciliationTURNName(values map[string]string, count, index int) string {
	base := values["VIDEO_CLOUD_DOMAIN"]
	if count == 1 {
		return firstNonEmpty(values["LKE_COTURN_DOMAIN"], "turn."+base)
	}
	return fmt.Sprintf("%s%02d.%s", firstNonEmpty(values["LKE_COTURN_DOMAIN_PREFIX"], "turn"), index, base)
}

func dnsReconciliationVisibleCandidates(cfg deploymentConfig) map[string]string {
	candidates := dnsReconciliationCandidates(cfg)
	values := deploymentEndpointValues(cfg)
	count, _ := strconv.Atoi(firstNonEmpty(values["TURN_REPLICAS"], "1"))
	// Historical numbered aliases are visibility-only when outside the
	// configured replica count; their names never grant removal authority.
	for i := 1; i <= min(max(count, 2), 100); i++ {
		name := dnsReconciliationTURNName(values, max(count, 2), i)
		if _, selected := candidates[name]; !selected {
			candidates[name] = "outside-configured-turn-count"
		}
	}
	return candidates
}

func dnsReconciliationInstanceMatches(item linodeInstance, target dnsReconciliationInstance, stack string) bool {
	return item.ID == target.ID && item.Label == target.Label && item.Region == target.Region && slices.Contains(item.Tags, "rtk-cloud") && slices.Contains(item.Tags, stack) && slices.Contains(item.Tags, target.Role) && slices.Contains(item.IPv4, target.IPv4)
}

func reviewDeploymentDNSReconciliation(ctx context.Context, cfg deploymentConfig, read removalRecoveryReader, adapter dnsAdapter) (deploymentDNSReconciliation, error) {
	receipt := deploymentDNSReconciliation{Schema: 1, Method: "current-linode-dns-reconciliation", Scope: "reviewed-current-managed-A-records", Environment: cfg.Environment, Stack: cfg.Values["CLOUD_STACK_NAME"], Adapter: cfg.DNSAdapter, RootDomain: cfg.Values["CLOUD_DNS_ROOT_DOMAIN"], ConfigSHA256: dnsReconciliationConfigSHA(cfg), Mappings: []dnsReconciliationMapping{}, Excluded: []dnsReconciliationExclusion{}}
	if cfg.Adapter != "lke" || adapter == nil || adapter.Name() != cfg.DNSAdapter || read == nil {
		return receipt, errors.New("selected LKE deployment and DNS adapter are required")
	}
	metadata, err := dnsReconciliationMetadata(cfg)
	if err != nil {
		return receipt, err
	}
	receipt.MetadataSHA256 = metadata
	token, err := dnsReconciliationToken(cfg)
	if err != nil {
		return receipt, err
	}
	clusters, _, err := completeRemovalRecoveryList(ctx, token, "/lke/clusters", nil, read, func(c lkeCluster) int { return c.ID })
	if err != nil {
		return receipt, err
	}
	for _, cluster := range clusters {
		if cluster.Label != receipt.Stack+"-lke" {
			continue
		}
		if receipt.Cluster.ID != 0 || cluster.Region != cfg.AdapterResolved["LKE_REGION"] {
			return receipt, errors.New("selected LKE cluster label or region is ambiguous")
		}
		cluster.K8sVersion = ""
		receipt.Cluster = cluster
	}
	if receipt.Cluster.ID <= 0 {
		return receipt, errors.New("a unique current configured LKE cluster is required for DNS reconciliation")
	}
	instances, _, err := completeRemovalRecoveryList(ctx, token, "/linode/instances", nil, read, func(i linodeInstance) int { return i.ID })
	if err != nil {
		return receipt, err
	}
	byRole := map[string][]dnsReconciliationInstance{}
	labels := map[string]string{receipt.Stack + "-edge-haproxy-01": "edge-haproxy"}
	turnCount, err := strconv.Atoi(firstNonEmpty(cfg.Values["TURN_REPLICAS"], "1"))
	if err != nil || turnCount < 0 || turnCount > 100 {
		return receipt, errors.New("bounded configured TURN count required")
	}
	if firstNonEmpty(cfg.Values["EDGE_REPLICAS"], "1") != "1" {
		return receipt, errors.New("DNS reconciliation supports the configured single edge only")
	}
	for i := 1; i <= turnCount; i++ {
		labels[fmt.Sprintf("%s-turn%02d", receipt.Stack, i)] = "coturn-vm"
	}
	for _, item := range instances {
		role := labels[item.Label]
		if role == "" {
			continue
		}
		if item.Region != receipt.Cluster.Region || !slices.Contains(item.Tags, "rtk-cloud") || !slices.Contains(item.Tags, receipt.Stack) || !slices.Contains(item.Tags, role) {
			receipt.Excluded = append(receipt.Excluded, dnsReconciliationExclusion{Name: item.Label, Type: "linode-instance", Values: item.IPv4, Reason: "name-only, foreign tags or region; numeric instance is outside reconciliation authority"})
			continue
		}
		var ips []string
		for _, value := range item.IPv4 {
			ip, err := netip.ParseAddr(value)
			if err == nil && ip.Is4() && ip.IsGlobalUnicast() && !ip.IsPrivate() {
				ips = append(ips, value)
			}
		}
		if len(ips) != 1 {
			return receipt, errors.New("selected role instance has ambiguous public IPv4")
		}
		for _, previous := range byRole[role] {
			if previous.Label == item.Label {
				return receipt, errors.New("duplicate current managed role labels are ambiguous")
			}
		}
		byRole[role] = append(byRole[role], dnsReconciliationInstance{item.ID, item.Label, item.Region, role, ips[0]})
	}
	for role := range byRole {
		sort.Slice(byRole[role], func(i, j int) bool { return byRole[role][i].ID < byRole[role][j].ID })
	}
	local := dnsReconciliationContext(cfg)
	if err := adapter.Validate(ctx, local); err != nil {
		return receipt, err
	}
	zone, err := adapter.DiscoverZone(ctx, local)
	if err != nil || zone.Name != receipt.RootDomain {
		return receipt, errors.New("authenticated selected DNS zone cannot be verified")
	}
	receipt.ZoneID = zone.ID
	candidates := dnsReconciliationVisibleCandidates(cfg)
	values := deploymentEndpointValues(cfg)
	names := make([]string, 0, len(candidates))
	for name := range candidates {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := validateManagedDNSHostname(name); err != nil {
			return receipt, err
		}
		if err := validateDNSRecord(zone.Name, dnsRecordSet{Name: name, Type: "A", Values: []string{"192.0.2.1"}, TTL: 600}); err != nil {
			return receipt, err
		}
		challenge, err := dns01RecordName(zone.Name, name)
		if err != nil {
			return receipt, err
		}
		pending, err := adapter.GetRecordSet(ctx, local, zone, challenge, "TXT")
		if err != nil || len(pending.Values) > 0 {
			return receipt, errors.New("selected environment ACME challenge values are unresolved or cannot be inspected")
		}
		current, err := adapter.GetRecordSet(ctx, local, zone, name, "A")
		if err != nil {
			return receipt, err
		}
		if len(current.Values) == 0 {
			continue
		}
		current.Name, current.Type = name, "A"
		current.Values = mergeDNSValues(current.Values)
		role := candidates[name]
		targets := byRole[role]
		expected := []string{}
		if role == "coturn-vm" {
			for i := 1; i <= turnCount; i++ {
				if dnsReconciliationTURNName(values, turnCount, i) == name {
					expectedLabel := fmt.Sprintf("%s-turn%02d", receipt.Stack, i)
					targets = nil
					for _, target := range byRole[role] {
						if target.Label == expectedLabel {
							targets = append(targets, target)
						}
					}
					break
				}
			}
		}
		for _, target := range targets {
			expected = append(expected, target.IPv4)
		}
		unsupported := false
		for _, kind := range []string{"CNAME", "AAAA"} {
			other, err := adapter.GetRecordSet(ctx, local, zone, name, kind)
			if err != nil {
				return receipt, err
			}
			if len(other.Values) > 0 {
				receipt.Excluded = append(receipt.Excluded, dnsReconciliationExclusion{name, kind, other.Values, "unsupported current DNS target; preserved outside reconciliation"})
				unsupported = true
			}
		}
		if unsupported || len(targets) == 0 || !slices.Equal(current.Values, mergeDNSValues(expected)) {
			receipt.Excluded = append(receipt.Excluded, dnsReconciliationExclusion{name, "A", current.Values, "no exact configured role-tagged numeric Linode ID/public IPv4 mapping; preserved outside reconciliation"})
			continue
		}
		current.Purpose = "reviewed-current-" + role
		receipt.Mappings = append(receipt.Mappings, dnsReconciliationMapping{current, targets})
	}
	if len(receipt.Mappings) == 0 {
		return receipt, errors.New("no exact current managed DNS mappings can be reconciled")
	}
	receipt.ReviewSHA256 = dnsReconciliationSHA(receipt)
	return receipt, nil
}

func dnsReconciliationPath(environment, adapter string) (string, error) {
	if !keySet("godaddy", "route53")[adapter] {
		return "", dnsStateBlocker("unknown DNS reconciliation adapter")
	}
	return privateDeploymentStatePath(environment, filepath.Join("deployment", "dns", adapter, "reconciliation.json"))
}

func readDeploymentDNSReconciliation(local dnsAdapterContext, adapter string, owned dnsOwnership) (deploymentDNSReconciliation, error) {
	var receipt deploymentDNSReconciliation
	path, err := dnsReconciliationPath(local.Values["CLOUD_ENV_NAME"], adapter)
	if err != nil {
		return receipt, err
	}
	raw, err := readStorageEvidenceFile(path)
	if err != nil || len(raw) > 1<<20 || decodeEnvironmentJSON(raw, &receipt) != nil {
		return receipt, dnsStateBlocker("bounded private reviewed DNS reconciliation receipt is missing or invalid")
	}
	approved, err := time.Parse(time.RFC3339Nano, receipt.ApprovedAt)
	if receipt.Schema != 1 || receipt.Method != "current-linode-dns-reconciliation" || receipt.Scope != "reviewed-current-managed-A-records" || receipt.Environment != local.Values["CLOUD_ENV_NAME"] || receipt.Stack != local.Values["CLOUD_STACK_NAME"] || receipt.Adapter != adapter || receipt.RootDomain != local.RootDomain || receipt.Cluster.ID <= 0 || receipt.Cluster.Label != receipt.Stack+"-lke" || receipt.Cluster.Region == "" || len(receipt.Mappings) == 0 || len(receipt.Mappings) > 256 || receipt.Excluded == nil || !environmentPreflightDigest.MatchString(receipt.ConfigSHA256) || !environmentPreflightDigest.MatchString(receipt.ReviewSHA256) || dnsReconciliationSHA(receipt) != receipt.ReviewSHA256 || owned.ReconciliationSHA256 != receipt.ReviewSHA256 || err != nil || approved.IsZero() || approved.After(time.Now().Add(time.Minute)) {
		return receipt, dnsStateBlocker("DNS reconciliation provenance, digest or selected environment binding is invalid")
	}
	if len(receipt.MetadataSHA256) != 3 {
		return receipt, dnsStateBlocker("DNS reconciliation metadata provenance is incomplete")
	}
	for _, name := range []string{"challenges.json", "state.json", "dns.env"} {
		if hash := receipt.MetadataSHA256[name]; hash != "absent" && !environmentPreflightDigest.MatchString(hash) {
			return receipt, dnsStateBlocker("DNS reconciliation metadata provenance is invalid")
		}
	}
	records := make([]dnsRecordSet, 0, len(receipt.Mappings))
	seen := map[string]bool{}
	for _, mapping := range receipt.Mappings {
		if mapping.Record.Type != "A" || validateDNSRecord(local.RootDomain, mapping.Record) != nil || seen[mapping.Record.Name] || len(mapping.Targets) == 0 || len(mapping.Targets) > 100 {
			return receipt, dnsStateBlocker("DNS reconciliation contains duplicate or foreign mapping records")
		}
		seen[mapping.Record.Name] = true
		expected := []string{}
		for _, target := range mapping.Targets {
			ip, err := netip.ParseAddr(target.IPv4)
			if target.ID <= 0 || target.Region != receipt.Cluster.Region || !keySet("edge-haproxy", "coturn-vm")[target.Role] || !strings.HasPrefix(target.Label, receipt.Stack+"-") || err != nil || !ip.Is4() || !ip.IsGlobalUnicast() || ip.IsPrivate() {
				return receipt, dnsStateBlocker("DNS reconciliation contains invalid numeric provider mappings")
			}
			expected = append(expected, target.IPv4)
		}
		if !slices.Equal(mergeDNSValues(expected), mergeDNSValues(mapping.Record.Values)) {
			return receipt, dnsStateBlocker("DNS reconciliation record does not match its exact provider mappings")
		}
		records = append(records, mapping.Record)
	}
	if deploymentplan.Digest(records) != deploymentplan.Digest(owned.Records) {
		return receipt, dnsStateBlocker("DNS ownership differs from its reviewed reconciliation receipt")
	}
	return receipt, nil
}

func reconcileDeploymentDNSState(ctx context.Context, cfg deploymentConfig, request dnsReconciliationRequest, read removalRecoveryReader, adapter dnsAdapter, out io.Writer) error {
	stack := cfg.Values["CLOUD_STACK_NAME"]
	if request.Confirm != "" && request.Confirm != stack || request.Confirm != "" && !environmentPreflightDigest.MatchString(request.ReviewSHA256) || request.Confirm == "" && request.ReviewSHA256 != "" {
		return errors.New("publication requires exact --confirm STACK and --review-sha256 from read-only review")
	}
	if request.Report != "" {
		if err := validateEnvironmentDeploymentReportPath(cfg.Environment, request.Report); err != nil {
			return err
		}
	}
	store, err := newSecretStore("", cfg.Environment)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(filepath.Join(store.Root, "deployment", ".execution-active")); !errors.Is(err, os.ErrNotExist) {
		return errors.New("active execution prevents DNS reconciliation")
	}
	ownershipPath, err := privateDeploymentStatePath(cfg.Environment, filepath.Join("deployment", "dns", cfg.DNSAdapter, "ownership.json"))
	if err != nil {
		return err
	}
	if _, err := readStorageEvidenceFile(ownershipPath); err == nil {
		return errors.New("original or earlier DNS ownership already exists; reconciliation never overwrites it")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	receipt, err := reviewDeploymentDNSReconciliation(ctx, cfg, read, adapter)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "[REVIEW] environment=%s method=%s managed_A_records=%d excluded=%d\n", cfg.Environment, receipt.Method, len(receipt.Mappings), len(receipt.Excluded))
	fmt.Fprintf(out, "  Current-state evidence only; no original deployment ownership was recovered.\nreview-sha256: %s\n", receipt.ReviewSHA256)
	for _, mapping := range receipt.Mappings {
		for _, target := range mapping.Targets {
			fmt.Fprintf(out, "  INCLUDE A %s -> %s instance=%d label=%s region=%s role=%s\n", mapping.Record.Name, target.IPv4, target.ID, target.Label, target.Region, target.Role)
		}
	}
	for _, excluded := range receipt.Excluded {
		fmt.Fprintf(out, "  PRESERVE %s %s values=%s\n    %s\n", excluded.Type, excluded.Name, strings.Join(excluded.Values, ","), excluded.Reason)
	}
	if request.Report != "" {
		raw, err := json.MarshalIndent(receipt, "", "  ")
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(store.Root, request.Report)
		if err != nil || writePrivateDeploymentState(cfg.Environment, relative, append(raw, '\n')) != nil {
			return errors.New("private DNS reconciliation review report could not be saved")
		}
	}
	if request.Confirm == "" {
		fmt.Fprintln(out, "Read-only review; no DNS ownership was published and no DNS or provider resource was changed.")
		return nil
	}
	if receipt.ReviewSHA256 != request.ReviewSHA256 {
		return errors.New("current DNS/provider mappings differ from the reviewed digest; no ownership was published")
	}
	fresh, err := reviewDeploymentDNSReconciliation(ctx, cfg, read, adapter)
	if err != nil || fresh.ReviewSHA256 != receipt.ReviewSHA256 {
		return errors.New("current DNS/provider evidence changed before publication")
	}
	if _, err := os.Lstat(filepath.Join(store.Root, "deployment", ".execution-active")); !errors.Is(err, os.ErrNotExist) {
		return errors.New("active execution prevents DNS reconciliation publication")
	}
	receipt.ApprovedAt = time.Now().UTC().Format(time.RFC3339Nano)
	receiptPath, err := dnsReconciliationPath(cfg.Environment, cfg.DNSAdapter)
	if err != nil {
		return err
	}
	if old, err := readStorageEvidenceFile(receiptPath); err == nil {
		var earlier deploymentDNSReconciliation
		if decodeEnvironmentJSON(old, &earlier) != nil || earlier.ReviewSHA256 != receipt.ReviewSHA256 || dnsReconciliationSHA(earlier) != receipt.ReviewSHA256 {
			return errors.New("earlier DNS reconciliation receipt conflicts and cannot be overwritten")
		}
		receipt.ApprovedAt = earlier.ApprovedAt
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	receiptRaw, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	if err := ensurePrivateDirectory(filepath.Dir(receiptPath)); err != nil {
		return err
	}
	if err := publishProviderImportFile(receiptPath, append(receiptRaw, '\n')); err != nil {
		return err
	}
	records := make([]dnsRecordSet, 0, len(receipt.Mappings))
	for _, mapping := range receipt.Mappings {
		records = append(records, mapping.Record)
	}
	owned := dnsOwnership{Adapter: cfg.DNSAdapter, RootDomain: receipt.RootDomain, Environment: cfg.Environment, Records: records, UpdatedAt: receipt.ApprovedAt, ReconciliationSHA256: receipt.ReviewSHA256}
	ownedRaw, err := json.MarshalIndent(owned, "", "  ")
	if err != nil {
		return err
	}
	if err := publishProviderImportFile(ownershipPath, append(ownedRaw, '\n')); err != nil {
		return err
	}
	fmt.Fprintln(out, "PUBLISHED explicit reviewed current-state DNS reconciliation. No DNS records, provider resources or credentials were changed. Run remove with the selected stack to consume this evidence.")
	return nil
}

func validateDeploymentDNSReconciliation(ctx context.Context, cfg deploymentConfig, read removalRecoveryReader, adapter dnsAdapter) error {
	local := dnsReconciliationContext(cfg)
	owned, err := readDNSOwnership(local, cfg.DNSAdapter)
	if err != nil || owned.ReconciliationSHA256 == "" {
		return err
	}
	receipt, err := readDeploymentDNSReconciliation(local, cfg.DNSAdapter, owned)
	if err != nil {
		return err
	}
	if cfg.Adapter != "lke" || receipt.ConfigSHA256 != dnsReconciliationConfigSHA(cfg) || receipt.Cluster.Region != cfg.AdapterResolved["LKE_REGION"] || read == nil || adapter == nil || adapter.Name() != cfg.DNSAdapter {
		return dnsStateBlocker("reviewed DNS reconciliation configuration changed")
	}
	metadata, err := dnsReconciliationMetadata(cfg)
	if err != nil || deploymentplan.Digest(metadata) != deploymentplan.Digest(receipt.MetadataSHA256) {
		return dnsStateBlocker("reviewed original DNS metadata bytes changed")
	}
	token, err := dnsReconciliationToken(cfg)
	if err != nil {
		return err
	}
	clusters, _, err := completeRemovalRecoveryList(ctx, token, "/lke/clusters", nil, read, func(c lkeCluster) int { return c.ID })
	if err != nil {
		return err
	}
	for _, cluster := range clusters {
		if cluster.Label == receipt.Cluster.Label && (cluster.ID != receipt.Cluster.ID || cluster.Region != receipt.Cluster.Region) {
			return dnsStateBlocker("a different LKE generation now owns the reviewed cluster label")
		}
	}
	instances, _, err := completeRemovalRecoveryList(ctx, token, "/linode/instances", nil, read, func(i linodeInstance) int { return i.ID })
	if err != nil {
		return err
	}
	for _, mapping := range receipt.Mappings {
		for _, target := range mapping.Targets {
			for _, item := range instances {
				if item.Label == target.Label && item.ID != target.ID {
					return dnsStateBlocker("a different numeric Linode generation now owns the reviewed role label")
				}
			}
		}
	}
	raw, err := read(ctx, token, "/lke/clusters/"+strconv.Itoa(receipt.Cluster.ID), nil)
	if err == nil {
		var cluster lkeCluster
		if json.Unmarshal(raw, &cluster) != nil || cluster.ID != receipt.Cluster.ID || cluster.Label != receipt.Cluster.Label || cluster.Region != receipt.Cluster.Region {
			return dnsStateBlocker("reviewed numeric cluster identity changed")
		}
	} else if !errors.Is(err, errRemovalRecoveryNativeNotFound) {
		return err
	}
	if err := adapter.Validate(ctx, local); err != nil {
		return err
	}
	zone, err := adapter.DiscoverZone(ctx, local)
	if err != nil || zone.Name != receipt.RootDomain || zone.ID != receipt.ZoneID {
		return dnsStateBlocker("reviewed DNS hosted-zone identity changed")
	}
	candidates := dnsReconciliationCandidates(cfg)
	for name := range dnsReconciliationVisibleCandidates(cfg) {
		challenge, err := dns01RecordName(zone.Name, name)
		if err != nil {
			return err
		}
		pending, err := adapter.GetRecordSet(ctx, local, zone, challenge, "TXT")
		if err != nil || len(pending.Values) > 0 {
			return dnsStateBlocker("ACME challenge values remain unresolved before reconciled removal")
		}
	}
	for _, mapping := range receipt.Mappings {
		role := candidates[mapping.Record.Name]
		if role == "" {
			return dnsStateBlocker("reviewed DNS name is outside current registered endpoint roles")
		}
		current, err := adapter.GetRecordSet(ctx, local, zone, mapping.Record.Name, "A")
		if err != nil {
			return err
		}
		if len(current.Values) > 0 && !slices.Equal(mergeDNSValues(current.Values), mergeDNSValues(mapping.Record.Values)) {
			return dnsStateBlocker("reviewed DNS record drift prevents removal")
		}
		for _, kind := range []string{"CNAME", "AAAA"} {
			other, err := adapter.GetRecordSet(ctx, local, zone, mapping.Record.Name, kind)
			if err != nil || len(other.Values) > 0 {
				return dnsStateBlocker("unsupported DNS target appeared after reconciliation review")
			}
		}
		for _, target := range mapping.Targets {
			if target.Role != role {
				return dnsStateBlocker("reviewed numeric role differs from registered DNS role")
			}
			raw, err := read(ctx, token, "/linode/instances/"+strconv.Itoa(target.ID), nil)
			if errors.Is(err, errRemovalRecoveryNativeNotFound) {
				if len(current.Values) > 0 {
					return dnsStateBlocker("reviewed numeric instance is absent while DNS values remain; explicit reconciliation is required")
				}
				continue
			}
			if err != nil {
				return err
			}
			var item linodeInstance
			if json.Unmarshal(raw, &item) != nil || !dnsReconciliationInstanceMatches(item, target, receipt.Stack) {
				return dnsStateBlocker("reviewed numeric Linode identity, tags, region or IPv4 changed")
			}
		}
	}
	return nil
}
