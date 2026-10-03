package main

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/postgresbackup"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/recovery"
)

type postgresBackupLoadMetrics struct {
	DurationSeconds int     `json:"duration_seconds"`
	Requests        int64   `json:"requests"`
	Errors          int64   `json:"errors"`
	P95MS           float64 `json:"p95_ms"`
	P99MS           float64 `json:"p99_ms"`
}

type postgresBackupQualification struct {
	Version             int                       `json:"version"`
	Environment         string                    `json:"environment"`
	Stack               string                    `json:"stack"`
	ClusterID           string                    `json:"cluster_id"`
	SourcePostgresImage string                    `json:"source_postgres_image"`
	RunnerImage         string                    `json:"runner_image"`
	SystemIdentifier    string                    `json:"system_identifier"`
	MaxRate             string                    `json:"max_rate"`
	BackupID            string                    `json:"backup_id"`
	WorkloadID          string                    `json:"workload_id"`
	ReviewedBy          string                    `json:"reviewed_by"`
	MeasuredAt          time.Time                 `json:"measured_at"`
	Baseline            postgresBackupLoadMetrics `json:"baseline"`
	DuringBackup        postgresBackupLoadMetrics `json:"during_backup"`
}

func postgresBackupValidateQualification(c postgresBackupDeployment, path string, status postgresbackup.Status) error {
	f, err := os.Open(path)
	if err != nil {
		return errors.New("enabling the schedule requires --qualification with reviewed performance evidence")
	}
	defer f.Close()
	var q postgresBackupQualification
	if err := recovery.Decode(f, &q); err != nil {
		return errors.New("invalid performance qualification evidence")
	}
	rate := c.Worker.Source.MaxRate
	if rate == "" {
		rate = "10M"
	}
	if q.Version != 1 || q.Environment != c.Environment || q.Stack != c.Stack || q.ClusterID != c.Worker.ClusterID || q.SourcePostgresImage != c.Worker.Source.PostgresImage || q.RunnerImage != c.RunnerImage || q.SystemIdentifier != c.Worker.Source.SystemIdentifier || q.MaxRate != rate || q.WorkloadID == "" || strings.TrimSpace(q.ReviewedBy) == "" || q.MeasuredAt.IsZero() || q.MeasuredAt.After(time.Now().UTC()) {
		return errors.New("performance evidence does not match the reviewed source, runner, rate and environment")
	}
	if status.Latest == nil || status.Latest.Manifest.PostgresImage != q.SourcePostgresImage || status.LatestDrill == nil || !status.LatestDrill.Success || status.LatestDrill.BackupID != q.BackupID || status.LatestDrill.PostgresImage != q.SourcePostgresImage {
		return errors.New("performance qualification must reference a successfully restored backup from the current source image")
	}
	valid := func(v postgresBackupLoadMetrics) bool {
		return v.DurationSeconds >= 300 && v.Requests > 0 && v.Errors >= 0 && v.Errors <= v.Requests && v.P95MS > 0 && v.P99MS >= v.P95MS && !math.IsInf(v.P99MS, 0) && !math.IsNaN(v.P99MS)
	}
	if !valid(q.Baseline) || !valid(q.DuringBackup) {
		return errors.New("qualification requires at least five minutes of measured request counts, errors, P95 and P99 for each comparable load run")
	}
	a, b := q.Baseline, q.DuringBackup
	if b.P95MS > a.P95MS*1.05 || b.P99MS > a.P99MS*1.05 || float64(b.Errors)/float64(b.Requests) > float64(a.Errors)/float64(a.Requests) {
		return errors.New("backup qualification exceeded the 5% latency or baseline error-rate limit")
	}
	ratio := (float64(b.Requests) / float64(b.DurationSeconds)) / (float64(a.Requests) / float64(a.DurationSeconds))
	if ratio < 0.95 || ratio > 1.05 {
		return errors.New("baseline and backup traffic rates must be comparable within 5%")
	}
	return nil
}

func (m *postgresBackupManager) validateSourceNetworkPolicy(ctx context.Context) error {
	c := m.Config
	b, err := m.kube(ctx, nil, "-n", c.Namespace, "get", "networkpolicy", "allow-postgres-clients", "-o", "json")
	if err != nil {
		return errors.New("existing managed PostgreSQL client NetworkPolicy required before adding backup access")
	}
	var p struct {
		Metadata struct{ Labels map[string]string }
		Spec     struct {
			PodSelector struct{ MatchLabels map[string]string }
			Ingress     []struct {
				From []struct {
					NamespaceSelector struct{ MatchLabels map[string]string }
				}
				Ports []struct {
					Port     int
					Protocol string
				}
			}
		}
	}
	if json.Unmarshal(b, &p) != nil || p.Metadata.Labels["rtk.realtek.com/stack"] != c.Stack || p.Spec.PodSelector.MatchLabels["app.kubernetes.io/name"] != "postgresql" {
		return errors.New("existing PostgreSQL client NetworkPolicy ownership or selector differs from managed layout")
	}
	allowed := map[string]bool{}
	for _, rule := range p.Spec.Ingress {
		portOK := false
		for _, port := range rule.Ports {
			if port.Port == 5432 && (port.Protocol == "" || port.Protocol == "TCP") {
				portOK = true
			}
		}
		if portOK {
			for _, from := range rule.From {
				allowed[from.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"]] = true
			}
		}
	}
	for _, suffix := range []string{"video-cloud", "account-manager", "billing"} {
		if !allowed[c.Stack+"-"+suffix] {
			return errors.New("existing NetworkPolicy does not retain all required PostgreSQL application clients")
		}
	}
	return nil
}
