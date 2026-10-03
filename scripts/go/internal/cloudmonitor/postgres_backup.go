package cloudmonitor

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"time"
)

// This projection intentionally includes only non-secret evidence. The runner
// owns the complete status schema and remote completion-marker verification.
type postgresBackupStatus struct {
	Version        int       `json:"version"`
	Environment    string    `json:"environment"`
	Stack          string    `json:"stack"`
	ClusterID      string    `json:"cluster_id"`
	GeneratedAt    time.Time `json:"generated_at"`
	CompletedCount *int      `json:"completed_count"`
	Latest         *struct {
		Manifest struct {
			Version     int       `json:"version"`
			Scope       string    `json:"scope"`
			Environment string    `json:"environment"`
			Stack       string    `json:"stack"`
			ClusterID   string    `json:"cluster_id"`
			BackupID    string    `json:"backup_id"`
			FinishedAt  time.Time `json:"finished_at"`
		} `json:"manifest"`
		Artifact struct {
			Path   string `json:"path"`
			Size   int64  `json:"size"`
			SHA256 string `json:"sha256"`
		} `json:"encrypted_artifact"`
	} `json:"latest"`
	LastAttempt *struct {
		StartedAt  time.Time `json:"started_at"`
		FinishedAt time.Time `json:"finished_at"`
		Status     string    `json:"status"`
	} `json:"last_attempt"`
	LatestDrill *struct {
		Version     int       `json:"version"`
		Environment string    `json:"environment"`
		Stack       string    `json:"stack"`
		ClusterID   string    `json:"cluster_id"`
		BackupID    string    `json:"backup_id"`
		FinishedAt  time.Time `json:"finished_at"`
		Success     *bool     `json:"success"`
	} `json:"latest_drill"`
}

// CollectPostgresBackups only reads one named ConfigMap per configured source.
// A recent status publication must never refresh an old backup's capture time.
func CollectPostgresBackups(ctx context.Context, cfg Config, rt Runtime, runner CommandRunner, now time.Time) Collection {
	var tasks []func(context.Context) Collection
	for _, check := range cfg.PostgresBackups {
		check := check
		tasks = append(tasks, func(parent context.Context) Collection {
			return collectPostgresBackup(parent, cfg, check, rt, runner, now)
		})
	}
	return parallelCollections(ctx, cfg, tasks)
}

func collectPostgresBackup(ctx context.Context, cfg Config, check PostgresBackupCheck, rt Runtime, runner CommandRunner, now time.Time) Collection {
	checks := []Result{
		result("postgres", "postgres-backup/"+check.Name+"/freshness", "backup", check.Required, now),
		result("postgres", "postgres-backup/"+check.Name+"/last-attempt", "backup", check.Required, now),
		result("postgres", "postgres-backup/"+check.Name+"/restore-drill", "recovery", check.Required, now),
	}
	for i := range checks {
		checks[i].Source = "Kubernetes backup status ConfigMap"
		checks[i].Reason = "備份狀態證據無法讀取、格式錯誤或不屬於選定環境。"
	}
	if !check.Enabled {
		for i := range checks {
			checks[i].Status = NotApplicable
			checks[i].Reason = "此 PostgreSQL 備份來源已明確停用。"
		}
		return Collection{Results: checks}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout(cfg))
	defer cancel()
	raw, code, err := kube(ctx, rt, runner, "-n", check.Namespace, "get", "configmap", check.StatusConfigMap, "-o", "json")
	if err != nil || code != 0 || len(raw) > 1<<20 {
		return Collection{Results: checks}
	}
	var object struct {
		Metadata struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"metadata"`
		Data map[string]string `json:"data"`
	}
	if json.Unmarshal(raw, &object) != nil || object.Metadata.Name != check.StatusConfigMap || object.Metadata.Namespace != check.Namespace {
		return Collection{Results: checks}
	}
	var status postgresBackupStatus
	if json.Unmarshal([]byte(object.Data["status.json"]), &status) != nil || status.Version != 1 || !backupIdentityMatches(cfg, check, status.Environment, status.Stack, status.ClusterID) || !validBackupTime(status.GeneratedAt, now) || status.CompletedCount == nil || *status.CompletedCount < 0 {
		return Collection{Results: checks}
	}
	warnHours, failHours := check.WarnAgeHours, check.FailAgeHours
	if warnHours == 0 {
		warnHours = 26
	}
	if failHours == 0 {
		failHours = 28
	}
	freshness := &checks[0]
	freshness.Threshold = fmt.Sprintf("WARN > %.0fh; FAIL > %.0fh", warnHours, failHours)
	if status.Latest == nil {
		if *status.CompletedCount == 0 {
			freshness.Status = Fail
			freshness.Reason = "備份已啟用，但沒有成功完成且已驗證遠端封存檔的備份證據。"
		}
	} else {
		m := status.Latest.Manifest
		a := status.Latest.Artifact
		if m.Version == 1 && m.Scope == "postgres-physical" && backupEvidenceID.MatchString(m.BackupID) && a.Path == m.BackupID+".age" && a.Size > 0 && backupEvidenceSHA256.MatchString(a.SHA256) && backupIdentityMatches(cfg, check, m.Environment, m.Stack, m.ClusterID) && validBackupTime(m.FinishedAt, now) && !m.FinishedAt.After(status.GeneratedAt) && *status.CompletedCount > 0 {
			age := now.Sub(m.FinishedAt).Hours()
			freshness.Value, freshness.Unit = numeric(age), "hours"
			freshness.Status = Pass
			freshness.Reason = "已完成備份的擷取時間仍在允許範圍；上傳或重新發布狀態不會重設備份年齡。"
			if age > failHours {
				freshness.Status, freshness.Reason = Fail, "最後成功備份的擷取時間已超過失敗門檻。"
			} else if age > warnHours {
				freshness.Status, freshness.Reason = Warn, "最後成功備份的擷取時間已超過警告門檻。"
			}
		}
	}
	attempt := &checks[1]
	attempt.Reason = "尚無最近一次備份執行結果的證據。"
	if a := status.LastAttempt; a != nil && validBackupTime(a.StartedAt, now) && !a.StartedAt.After(status.GeneratedAt) {
		ended := validBackupTime(a.FinishedAt, now) && !a.FinishedAt.Before(a.StartedAt) && !a.FinishedAt.After(status.GeneratedAt)
		switch a.Status {
		case "running":
			if a.FinishedAt.IsZero() {
				attempt.Status, attempt.Reason = Pass, "備份執行中；可用備份時間由已完成備份另外判定。"
				if now.Sub(a.StartedAt) > 4*time.Hour {
					attempt.Status, attempt.Reason = Fail, "備份執行中狀態超過四小時，需檢查逾時與狀態發布。"
				}
			}
		case "succeeded":
			if ended {
				attempt.Status, attempt.Reason = Pass, "最近一次備份執行成功。"
			}
		case "failed":
			if ended {
				attempt.Status, attempt.Reason = Fail, "最近一次備份執行失敗；先前可用備份的年齡仍獨立計算。"
			}
		case "skipped":
			if ended {
				attempt.Status, attempt.Reason = Warn, "最近一次備份未執行；請檢查維護或執行鎖狀態。"
			}
		}
	}
	drill := &checks[2]
	drill.Reason = "尚無獨立還原演練結果；備份上傳成功無法證明可還原。"
	if d := status.LatestDrill; d != nil && d.Version == 1 && backupEvidenceID.MatchString(d.BackupID) && d.Success != nil && backupIdentityMatches(cfg, check, d.Environment, d.Stack, d.ClusterID) && validBackupTime(d.FinishedAt, now) && !d.FinishedAt.After(status.GeneratedAt) {
		drill.Value, drill.Unit = numeric(now.Sub(d.FinishedAt).Hours()), "hours"
		drill.Status, drill.Reason = Fail, "最近一次獨立還原演練失敗。"
		if *d.Success {
			drill.Status, drill.Reason = Pass, "最近一次獨立還原演練成功；這不代表每份後續備份皆已演練。"
		}
	}
	return Collection{Results: checks}
}

var backupEvidenceID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
var backupEvidenceSHA256 = regexp.MustCompile(`^[a-f0-9]{64}$`)

func backupIdentityMatches(cfg Config, check PostgresBackupCheck, environment, stack, clusterID string) bool {
	return environment == cfg.Environment && stack == cfg.Stack && clusterID == check.ClusterID
}

func validBackupTime(t, now time.Time) bool { return !t.IsZero() && !t.After(now) }
