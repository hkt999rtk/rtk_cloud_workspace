package cloudmonitor

import (
	"net"
	"strings"
	"time"
)

// ConfigurationCoverage prevents an empty adapter list from silently removing a
// required check. The inventory is the expectation; live discovery is evidence.
func ConfigurationCoverage(cfg Config, inv Inventory, now time.Time) Collection {
	var out Collection
	enabled := map[string]bool{}
	for _, t := range inv.Targets {
		if t.Enabled && t.Required {
			enabled[t.Service] = true
		}
	}
	gap := func(service, id, reason string) { out = Merge(out, sourceGap(service, id, reason, true, now)) }
	if enabled["postgres"] && len(cfg.Postgres) == 0 {
		gap("postgres", "coverage/postgres", "尚未配置 PostgreSQL 唯讀統計與實際 filesystem 用量來源。")
	}
	if enabled["postgres"] && len(cfg.PostgresBackups) == 0 {
		gap("postgres", "coverage/postgres-backup", "尚未配置 PostgreSQL 備份完成與還原演練的唯讀證據來源。")
	}
	redisRoles := map[string]bool{}
	for _, r := range cfg.Redis {
		redisRoles[r.Role] = true
	}
	if enabled["redis"] && !redisRoles["shadow"] {
		gap("redis", "coverage/shadow", "尚未配置 Shadow Redis 的 INFO、持久化與實際容量來源。")
	}
	if enabled["fleet-valkey"] && !redisRoles["fleet"] {
		gap("fleet-valkey", "coverage/fleet", "尚未配置獨立 Fleet Valkey 的 INFO、持久化與實際容量來源。")
	}
	tokens := map[string]bool{}
	for _, t := range cfg.Tokens {
		if t.Required {
			tokens[t.Service] = true
		}
	}
	for _, service := range []string{"account-manager", "video-cloud", "billing", "openbao", "fleet-valkey"} {
		if enabled[service] && !tokens[service] {
			gap(service, "coverage/token", "尚未配置使用中 token 的線上有效性、權限與到期資訊來源。")
		}
	}
	tls := map[string]bool{}
	for _, t := range cfg.TLS {
		if t.Required && t.Address != "" {
			tls[t.Service] = true
		}
	}
	for _, service := range []string{"mqtt", "certissuer", "factoryenroll", "pkiturn", "openbao"} {
		if enabled[service] && !tls[service] {
			gap(service, "coverage/active-identity", "尚未配置目前載入的 TLS/mTLS 身分、信任鏈與撤銷檢查。")
		}
	}
	for _, e := range inv.Endpoints {
		semantic := false
		for _, h := range cfg.HTTP {
			if h.Service == e.Service && h.Required && !h.LivenessOnly && h.Contains != "" {
				semantic = true
			}
		}
		if !semantic {
			gap(e.Service, "coverage/readiness", "預設 HTTP 只證明存活；需設定有語意的依賴與授權檢查。")
		}
	}
	if len(cfg.Metrics) == 0 {
		gap("performance", "coverage/metrics", "尚未配置實際 throughput、延遲與資源飽和度指標；無法判定可接受範圍。")
	}
	metricServices := map[string]bool{}
	for _, m := range cfg.Metrics {
		if m.Required {
			metricServices[m.Service] = true
		}
	}
	for _, e := range inv.Endpoints {
		if !metricServices[e.Service] {
			gap(e.Service, "coverage/performance", "此服務尚未配置必要的實際效能指標；其他服務的指標不代表本服務。")
		}
	}
	for _, target := range inv.Targets {
		if !target.Enabled || !target.Required || !strings.EqualFold(target.Kind, "external") {
			continue
		}
		if strings.EqualFold(target.Service, "turn") {
			if !hasTURNProbeForHost(cfg, target.Name) {
				gap(target.Service, "coverage/external-turn/"+target.Name, "此外部 TURN host 尚未配置相同 host 的專用 TURN probe。")
			}
			continue
		}
		if !hasSemanticRequiredHTTP(cfg, target.Service) {
			gap(target.Service, "coverage/external-http/"+target.Name, "此外部服務尚未配置必要的語意 HTTP 證據；Kubernetes 不管理此 target。")
		}
	}
	for i := range inv.CoverageNotes {
		gap("inventory", "coverage/intent/"+strconvInt(i), "預期 workload 的啟用意圖尚未完整宣告。")
	}
	return out
}

func hasSemanticRequiredHTTP(cfg Config, service string) bool {
	for _, check := range cfg.HTTP {
		if check.Service == service && check.Required && !check.LivenessOnly && check.Contains != "" {
			return true
		}
	}
	return false
}

func hasTURNProbeForHost(cfg Config, host string) bool {
	want := normalizeMonitorHost(host)
	for _, probe := range cfg.Synthetic.TURN {
		probeHost, _, err := net.SplitHostPort(strings.TrimSpace(probe.Address))
		if err == nil && normalizeMonitorHost(probeHost) == want {
			return true
		}
	}
	return false
}

func normalizeMonitorHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}
