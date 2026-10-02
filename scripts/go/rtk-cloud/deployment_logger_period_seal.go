package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

func runDeploymentLoggerPeriodSeal(args []string) error {
	return runDeploymentLoggerPeriodSealWithOps(args, loggerPeriodSealOps{
		credentials: func(environment string) (func(), error) {
			previousCanonical := activeCanonicalSecretStore
			_, restore, err := configureProvisionSecretStore(environment)
			if err != nil {
				return nil, err
			}
			activeCanonicalSecretStore = true
			return func() { activeCanonicalSecretStore = previousCanonical; restore() }, nil
		},
		readyLogger: lkeRequireReadyLoggerPeriodSource, readyRetention: lkeRequireReadyLokiRetentionStorage, apply: kubectlApply,
	})
}

type loggerPeriodSealOps struct {
	credentials                 func(string) (func(), error)
	readyLogger, readyRetention func(map[string]string) error
	apply                       func(string) error
}

func runDeploymentLoggerPeriodSealWithOps(args []string, ops loggerPeriodSealOps) error {
	fs := flag.NewFlagSet("deployment logger-period-seal", flag.ContinueOnError)
	environment := fs.String("environment", "", "selected environment")
	workspace := fs.String("workspace", "", "workspace root")
	month := fs.String("month", "", "completed UTC month YYYY-MM")
	confirm := fs.String("confirm", "", "selected stack for a one-time source close Job")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *environment == "" {
		return errors.New("--environment and --month are required; positional arguments are not accepted")
	}
	if err := validateLoggerSealJobMonth(*month, time.Now().UTC()); err != nil {
		return err
	}
	cfg, err := resolveDeploymentConfig(*workspace, *environment, "")
	if err != nil {
		return err
	}
	if cfg.Adapter != "lke" {
		return errors.New("Logger period seal Job requires the LKE adapter")
	}
	store, err := newSecretStore("", cfg.Environment)
	if err != nil {
		return err
	}
	env, err := loggerPeriodSourceSelectedEnv(cfg, store)
	if err != nil {
		return err
	}
	if env["CLOUD_STACK_NAME"] != cfg.Values["CLOUD_STACK_NAME"] || env["CLOUD_ENV_NAME"] != cfg.Environment {
		return errors.New("Logger monthly close inputs differ from the selected environment")
	}
	if err := loadLKEImageManifestDefaults(store.Root, env); err != nil {
		return err
	}
	if *confirm == "" {
		fmt.Fprintf(os.Stdout, "Logger period seal plan: environment=%s stack=%s month=%s; full historical Cloud inventory, source freeze, facts, then seal; no invoice is issued\n", cfg.Environment, env["CLOUD_STACK_NAME"], *month)
		return nil
	}
	if *confirm != env["CLOUD_STACK_NAME"] {
		return errors.New("--confirm must match the selected stack")
	}
	restore, err := ops.credentials(cfg.Environment)
	if err != nil {
		return err
	}
	defer restore()
	if !lkeLoggerServiceRegistrationEnabled(env) || !lkeLoggerBillingFactsEnabled(env) {
		return errors.New("Logger source close requires the registered billable Logger")
	}
	if err := lkeRequireLoggerProducerSealToken(env); err != nil {
		return err
	}
	if !strings.Contains(lkeVideoCloudImage(env), "@sha256:") {
		return errors.New("Logger source close requires the pinned environment release image")
	}
	if err := ops.readyLogger(env); err != nil {
		return err
	}
	if err := ops.readyRetention(env); err != nil {
		return err
	}
	name := "logger-period-seal-" + strings.ReplaceAll(*month, "-", "") + "-" + time.Now().UTC().Format("20060102t150405")
	if err := ops.apply(lkeAllowAccountManagerHandoffBillingNetworkPolicyManifest(env)); err != nil {
		return err
	}
	if err := ops.apply(lkeLoggerPeriodSealJobManifest(env, *month, name)); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Logger source close Job started: %s/%s; verify Complete and immutable Billing seal before invoice close\n", lkeNamespaceName(env, "video-cloud"), name)
	return nil
}

func validateLoggerSealJobMonth(month string, now time.Time) error {
	start, err := time.Parse("2006-01", month)
	if err != nil || start.Format("2006-01") != month || now.UTC().Before(start.AddDate(0, 1, 0).Add(24*time.Hour)) {
		return errors.New("--month must be YYYY-MM and closed for at least 24 hours in UTC")
	}
	return nil
}
