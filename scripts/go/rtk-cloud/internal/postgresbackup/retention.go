package postgresbackup

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type PrunePlan struct {
	Candidates []string `json:"candidates"`
	Kept       []string `json:"kept"`
	Deleted    []string `json:"deleted"`
	Reason     string   `json:"reason,omitempty"`
}

// Prune must run under the same cluster lock as capture and core maintenance.
// Bucket lifecycle must not expire these objects independently of this policy.
func Prune(ctx context.Context, c Config, dryRun bool) (PrunePlan, error) {
	return (Engine{}).Prune(ctx, c, dryRun)
}

func (e Engine) Prune(ctx context.Context, c Config, dryRun bool) (PrunePlan, error) {
	client, err := e.repository(c)
	if err != nil {
		return PrunePlan{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.TimeoutSeconds)*time.Second)
	defer cancel()
	return prune(ctx, client, c, dryRun, time.Now().UTC())
}

func planRetention(c Config, sets []Completion, drills []Drill, keys []string, now time.Time) PrunePlan {
	p := PrunePlan{Candidates: []string{}, Kept: []string{}, Deleted: []string{}}
	pinned := ""
	for _, d := range drills {
		if d.Success {
			pinned = d.BackupID
			break
		}
	}
	verifiedExists := false
	for _, m := range sets {
		if m.Manifest.ID == pinned {
			verifiedExists = true
		}
	}
	if !verifiedExists {
		p.Reason = "automatic retention requires a retained backup with successful remote restore evidence"
	}
	holds := map[string]bool{}
	for _, key := range keys {
		name := strings.TrimPrefix(key, remotePrefix(c))
		if strings.HasSuffix(name, ".hold.json") {
			holds[strings.TrimSuffix(name, ".hold.json")] = true
		}
	}
	cutoff := now.Add(-time.Duration(c.RetentionDays) * 24 * time.Hour)
	for i, m := range sets {
		id := m.Manifest.ID
		if !verifiedExists || i < c.MinimumBackups || !m.Manifest.FinishedAt.Before(cutoff) || id == pinned || holds[id] {
			p.Kept = append(p.Kept, id)
		} else {
			p.Candidates = append(p.Candidates, id)
		}
	}
	sort.Strings(p.Candidates)
	sort.Strings(p.Kept)
	return p
}

func retentionSnapshot(ctx context.Context, client objectStore, c Config, now time.Time) (PrunePlan, error) {
	keys, err := listKeys(ctx, client, c)
	if err != nil {
		return PrunePlan{}, err
	}
	sets, err := listCompleted(ctx, client, c, keys)
	if err != nil {
		return PrunePlan{}, err
	}
	drills, err := listDrills(ctx, client, c, keys)
	if err != nil {
		return PrunePlan{}, err
	}
	return planRetention(c, sets, drills, keys, now), nil
}

func prune(ctx context.Context, client objectStore, c Config, dryRun bool, now time.Time) (PrunePlan, error) {
	p, err := retentionSnapshot(ctx, client, c, now)
	if err != nil {
		return p, err
	}
	if dryRun || len(p.Candidates) == 0 {
		return p, nil
	}
	versioning, err := client.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{Bucket: aws.String(c.Remote.Bucket)})
	if err != nil || versioning == nil {
		return p, errors.New("cannot verify bucket versioning; no backups deleted")
	}
	if versioning.Status != "" {
		p.Reason = "versioned or previously versioned bucket requires a reviewed object-version cleanup policy"
		return p, errors.New(p.Reason)
	}
	// Recheck the retained set and holds immediately before deletion. Other
	// cooperating writers are excluded by the caller's cluster lock.
	again, err := retentionSnapshot(ctx, client, c, now)
	if err != nil {
		return p, err
	}
	if strings.Join(p.Candidates, "\n") != strings.Join(again.Candidates, "\n") || strings.Join(p.Kept, "\n") != strings.Join(again.Kept, "\n") {
		return p, errors.New("backup retention inputs changed; no backups deleted")
	}
	for _, id := range p.Candidates {
		if _, err := readCompletion(ctx, client, c, id); err != nil {
			return p, err
		}
		// Invalidate the completion first. An interrupted delete may leave an
		// orphan archive but can never leave a marker advertising missing data.
		if _, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(c.Remote.Bucket), Key: aws.String(remoteKey(c, id, ".complete.json"))}); err != nil {
			return p, errors.New("backup completion deletion failed")
		}
		if _, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(c.Remote.Bucket), Key: aws.String(remoteKey(c, id, ".age"))}); err != nil {
			return p, errors.New("backup archive deletion failed; unadvertised orphan retained")
		}
		p.Deleted = append(p.Deleted, id)
	}
	return p, nil
}
