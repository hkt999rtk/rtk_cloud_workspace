package main

import (
	"context"
	"slices"
	"time"
)

type productSessionInstallations struct {
	Session   string    `json:"session_id"`
	Issuer    string    `json:"issuer_id"`
	Root      string    `json:"root_sha256"`
	Subjects  []string  `json:"subjects"`
	Installed []string  `json:"installed"`
	Status    string    `json:"status"`
	Expires   time.Time `json:"expires_at"`
}

func productInstallationsComplete(ack productSessionInstallations, s environmentCreateServiceCeremony, subjects []string, now time.Time) (bool, error) {
	expected := slices.Clone(subjects)
	slices.Sort(expected)
	installed := slices.Clone(ack.Installed)
	slices.Sort(installed)
	if ack.Session != s.BootstrapSessionID || ack.Issuer != s.Intermediate.ID || ack.Root != s.Root.RootFingerprint || !slices.Equal(ack.Subjects, expected) || ack.Status != "active" && ack.Status != "sealed" && ack.Status != "expired" || (!ack.Expires.After(now) || ack.Status == "expired") && !((ack.Status == "sealed" || ack.Status == "expired") && slices.Equal(installed, expected)) || len(expected) == 0 {
		return false, environmentCreateBootstrapBlocker("product installation ACK belongs to another session, issuer, expiry or subject policy")
	}
	for i, subject := range expected {
		if subject == "service:deployment-bootstrap" || i > 0 && subject == expected[i-1] {
			return false, environmentCreateBootstrapBlocker("product installation policy has duplicate or bootstrap-caller subjects")
		}
	}
	for i, subject := range installed {
		if !slices.Contains(expected, subject) || i > 0 && subject == installed[i-1] {
			return false, environmentCreateBootstrapBlocker("native product ACK roster has an unknown or duplicate subject")
		}
	}
	if ack.Status == "sealed" && !slices.Equal(installed, expected) {
		return false, environmentCreateBootstrapBlocker("sealed product session has incomplete native workload ACKs")
	}
	return slices.Equal(installed, expected), nil
}

func waitProductSessionInstallations(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, s environmentCreateServiceCeremony, subjects []string) error {
	query := "SELECT json_build_object('session_id',session_id,'issuer_id',issuer_id,'root_sha256',root_sha256,'subjects',subjects,'installed',installed_subjects,'status',status,'expires_at',expires_at) FROM pki_deployment_bootstrap_sessions WHERE environment='" + cfg.Environment + "' AND session_id='" + s.BootstrapSessionID + "'"
	for {
		raw, err := readEnvironmentCreateRegistryJSON(ctx, cfg, runtime, query)
		var ack productSessionInstallations
		if err != nil || decodeEnvironmentJSON(raw, &ack) != nil {
			return environmentCreateBootstrapBlocker("native product installation ACKs cannot be read")
		}
		done, err := productInstallationsComplete(ack, s, subjects, time.Now().UTC())
		if err != nil {
			return err
		}
		if done {
			return runtime.Execution.Check(ctx)
		}
		timer := time.NewTimer(2 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
