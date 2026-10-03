package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"filippo.io/age"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/hkt999rtk/rtk_cloud_logger/billingarchive"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/billingbackup"
)

func runBillingLifecycle(args []string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Println("billing-lifecycle: status | operation-status | migrate | capture | verify | rehydrate | cache-evict | retire | resume | abort | compact | authority | restore-stage | recovery-status | recovery-admit | run")
		fmt.Println("Required: --environment ENV --config FILE. Mutations require --confirm-environment ENV --confirm-store STORE_ID.")
		fmt.Println("Verifier: --identity FILE.agekey --signing-key FILE.ed25519key --prefix SNAPSHOT_PREFIX. Authority: --role financial|recovery|controller --path PATH --request PRIVATE_JSON.")
		fmt.Println("No keys, buckets, scheduler or environment are provisioned by this command.")
		return nil
	}
	action := args[0]
	fs := flag.NewFlagSet("billing-lifecycle "+action, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	environment := fs.String("environment", "", "environment")
	configFile := fs.String("config", "", "controller config")
	configRoot := fs.String("config-root", "", "SecretStore parent")
	confirm := fs.String("confirm-environment", "", "confirm mutations")
	confirmStore := fs.String("confirm-store", "", "confirm logical identity")
	var identityFiles billingIdentityFlags
	fs.Var(&identityFiles, "identity", "absolute native identity path; repeat for retained encryption keys")
	signer := fs.String("signing-key", "", "absolute native signing key path")
	prefix := fs.String("prefix", "", "explicit snapshot prefix")
	requestFile := fs.String("request", "", "private JSON request")
	destination := fs.String("destination", "", "private offline recovery staging root; never a live database")
	role := fs.String("role", "controller", "authority role")
	method := fs.String("method", "POST", "authority method GET or POST")
	path := fs.String("path", "", "authority relative API path")
	op := fs.String("operation-id", "", "immutable operation ID")
	first := fs.Uint64("from", 0, "inclusive sequence start")
	last := fs.Uint64("through", 0, "inclusive sequence end")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
		return errors.New("invalid Billing lifecycle arguments")
	}
	allowed := map[string]bool{"status": true, "operation-status": true, "migrate": true, "capture": true, "verify": true, "rehydrate": true, "cache-evict": true, "retire": true, "resume": true, "abort": true, "compact": true, "authority": true, "restore-stage": true, "recovery-status": true, "recovery-admit": true, "run": true}
	if !allowed[action] {
		return errors.New("unknown lifecycle action")
	}
	f, err := os.Open(*configFile)
	if err != nil {
		return errors.New("controller configuration unavailable")
	}
	var cfg billingbackup.Config
	err = billingarchive.StrictDecode(f, 1<<20, &cfg)
	f.Close()
	if err != nil {
		return err
	}
	if cfg.Environment != *environment {
		return errors.New("controller environment mismatch")
	}
	if err = cfg.Validate(); err != nil {
		return err
	}
	readOnly := action == "status" || action == "recovery-status" || action == "operation-status" || (action == "authority" && *method == http.MethodGet)
	if !readOnly && (*confirm != cfg.Environment || *confirmStore != cfg.StoreID) {
		return errors.New("explicit environment and StoreID confirmation required")
	}
	store, err := newSecretStore(*configRoot, *environment)
	if err != nil {
		return err
	}
	readToken := func(id string) (string, error) {
		value, err := store.readRuntime(id)
		if err != nil {
			return "", errors.New("dedicated lifecycle credential unavailable: " + id)
		}
		return value, nil
	}
	var loggerToken string
	if action != "restore-stage" && action != "authority" {
		id := "cloud-logger-lifecycle-token"
		if action == "operation-status" {
			id = "cloud-logger-lifecycle-read-token"
		}
		loggerToken, err = readToken(id)
		if err != nil {
			return err
		}
	}
	loggerClient := billingbackup.Client{BaseURL: cfg.LoggerURL, Token: loggerToken}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	printJSON := func(value any) error { return json.NewEncoder(os.Stdout).Encode(value) }
	var response json.RawMessage
	switch action {
	case "status":
		err = loggerClient.Request(ctx, http.MethodGet, "/v1/internal/billing-lifecycle/status", nil, &response)
	case "operation-status":
		if !billingarchive.SafeID(*op) {
			return errors.New("operation ID required")
		}
		err = loggerClient.Request(ctx, http.MethodGet, "/v1/internal/billing-lifecycle/retire/"+*op, nil, &response)
	case "cache-evict":
		err = loggerClient.Request(ctx, http.MethodPost, "/v1/internal/billing-lifecycle/cache/evict", map[string]string{"through_sequence": fmt.Sprint(*last)}, &response)
	case "recovery-status":
		err = loggerClient.Request(ctx, http.MethodGet, "/v1/internal/billing-lifecycle/recovery", nil, &response)
	case "recovery-admit":
		body, err := readBillingLifecycleRequest(*requestFile)
		if err != nil {
			return err
		}
		// This command forwards the independently produced approval; it cannot
		// sign one with the ordinary archive verifier's identity.
		err = loggerClient.Request(ctx, http.MethodPost, "/v1/internal/billing-lifecycle/recovery/admit", body, &response)
	case "migrate", "capture", "compact":
		body := map[string]int{}
		if action == "migrate" {
			body["batch"] = 1000
		}
		err = loggerClient.Request(ctx, http.MethodPost, "/v1/internal/billing-lifecycle/"+action, body, &response)
	case "authority":
		if !strings.HasPrefix(*path, "/v1/internal/billing/raw-retention/") || strings.Contains(*path, "..") || strings.ContainsAny(*path, "#") || (*method != http.MethodGet && *method != http.MethodPost) || (*method == http.MethodPost && strings.Contains(*path, "?")) {
			return errors.New("invalid raw retention authority path")
		}
		id := map[string]string{"financial": "billing-raw-retention-financial", "recovery": "billing-raw-retention-recovery", "controller": "billing-raw-retention-controller"}[*role]
		if id == "" {
			return errors.New("unknown authority role")
		}
		token, err := readToken(id)
		if err != nil {
			return err
		}
		var body any
		if *method == http.MethodPost {
			body, err = readBillingLifecycleRequest(*requestFile)
			if err != nil {
				return err
			}
		}
		err = (billingbackup.Client{BaseURL: cfg.BillingURL, Token: token}).Request(ctx, *method, *path, body, &response)
	case "resume", "abort":
		if !billingarchive.SafeID(*op) {
			return errors.New("operation ID required")
		}
		token, err := readToken("billing-raw-retention-controller")
		if err != nil {
			return err
		}
		authority := billingbackup.Client{BaseURL: cfg.BillingURL, Token: token}
		err = billingbackup.ResumeRetirement(ctx, cfg, loggerClient, authority, *op, action == "abort")
		if action == "abort" && err != nil {
			var rejected *billingbackup.HTTPError
			if errors.As(err, &rejected) && rejected.Status == http.StatusNotFound {
				err = billingbackup.AbortSavedIntent(ctx, cfg, loggerClient, authority, *op)
			}
		}
		if err == nil {
			return printJSON(map[string]string{"operation_id": *op, "result": "authoritatively-resolved"})
		}
	default:
		keys, _ := cfg.PublicKeys()
		if len(identityFiles) == 0 {
			return errors.New("explicit custody identity required")
		}
		var ids []age.Identity
		for _, file := range identityFiles {
			parsed, err := billingbackup.ReadIdentity(store.Root, file)
			if err != nil {
				return err
			}
			ids = append(ids, parsed...)
		}
		var key ed25519.PrivateKey
		if action != "rehydrate" && action != "restore-stage" {
			key, err = billingbackup.ReadSigner(store.Root, *signer, keys[cfg.VerifierKeyID])
			if err != nil {
				return err
			}
		}
		operator, err := store.readOperator()
		if err != nil {
			return errors.New("verifier storage credentials unavailable")
		}
		objects, err := billingbackup.NewS3Store(cfg.Remote, operator["RTK_BILLING_VERIFIER_ACCESS_KEY_ID"], operator["RTK_BILLING_VERIFIER_SECRET_ACCESS_KEY"])
		if err != nil {
			return err
		}
		engine := billingbackup.Engine{Config: cfg, Objects: objects, Identities: ids, Signer: key}
		if action == "restore-stage" {
			staged, err := engine.StageRestore(ctx, *prefix, *destination)
			if err != nil {
				return err
			}
			return printJSON(staged)
		}
		if action == "verify" {
			verified, err := engine.Verify(ctx, *prefix)
			if err != nil {
				return err
			}
			if err = loggerClient.Request(ctx, http.MethodPost, "/v1/internal/billing-lifecycle/verify", map[string]any{"set_id": verified.Manifest.SetID, "completion": verified.Completion}, &response); err != nil {
				return err
			}
			return printJSON(verified)
		}
		if action == "rehydrate" {
			records, _, manifest, err := engine.ReadRange(ctx, *prefix, *first, *last)
			if err != nil {
				return err
			}
			err = loggerClient.Request(ctx, http.MethodPost, "/v1/internal/billing-lifecycle/rehydrate", map[string]any{"set_id": manifest.SetID, "records": records}, &response)
			break
		}
		token, err := readToken("billing-raw-retention-controller")
		if err != nil {
			return err
		}
		authority := billingbackup.Client{BaseURL: cfg.BillingURL, Token: token}
		if action == "retire" {
			err = billingbackup.RetireRange(ctx, engine, loggerClient, authority, *prefix, *first, *last)
			break
		}
		if action == "run" {
			return billingbackup.Run(ctx, engine, objects, loggerClient, authority, 12*time.Hour)
		}
	}
	if err != nil {
		return err
	}
	return printJSON(response)
}

type billingIdentityFlags []string

func (v *billingIdentityFlags) String() string { return "[explicit custody paths]" }
func (v *billingIdentityFlags) Set(value string) error {
	if len(*v) >= 16 || !filepath.IsAbs(value) {
		return errors.New("absolute bounded custody paths required")
	}
	*v = append(*v, value)
	return nil
}

func readBillingLifecycleRequest(file string) (json.RawMessage, error) {
	i, err := os.Lstat(file)
	if err != nil || !i.Mode().IsRegular() || i.Mode().Perm()&0077 != 0 || !filepath.IsAbs(file) {
		return nil, errors.New("private absolute request file required")
	}
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var value json.RawMessage
	if err = billingarchive.StrictDecode(f, 2<<20, &value); err != nil {
		return nil, err
	}
	return value, nil
}
