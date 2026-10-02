package main

import (
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/internal/storagepolicy"
)

type storageLifecycleRule struct {
	ID     string `xml:"ID"`
	Status string `xml:"Status"`
	Filter struct {
		Prefix string `xml:"Prefix"`
	} `xml:"Filter"`
	Expiration struct {
		Days int `xml:"Days"`
	} `xml:"Expiration"`
	Abort *storageLifecycleAbort `xml:"AbortIncompleteMultipartUpload,omitempty"`
}

type storageLifecycleAbort struct {
	Days int `xml:"DaysAfterInitiation"`
}

type storageLifecycleConfiguration struct {
	XMLName xml.Name               `xml:"LifecycleConfiguration"`
	XMLNS   string                 `xml:"xmlns,attr"`
	Rules   []storageLifecycleRule `xml:"Rule"`
}

type storageLifecyclePlan struct {
	Environment              string                  `json:"environment"`
	Purpose                  string                  `json:"purpose"`
	Target                   deploymentStorageTarget `json:"target"`
	ObservedAt               string                  `json:"observed_at"`
	Policy                   string                  `json:"policy"`
	CloudMutation            bool                    `json:"cloud_mutation"`
	ReplacementConfiguration bool                    `json:"replacement_configuration"`
	ExistingStatus           string                  `json:"existing_lifecycle_status"`
	ExistingXML              string                  `json:"existing_lifecycle_xml,omitempty"`
	ExistingSHA256           string                  `json:"existing_lifecycle_sha256,omitempty"`
	ProposedRulesXML         string                  `json:"proposed_rules_xml"`
	Prerequisites            []string                `json:"review_prerequisites"`
}

func plannedStorageLifecycleXML(purpose, prefix string, abortDays int) (string, error) {
	if abortDays < 0 || abortDays > 365 {
		return "", errors.New("--abort-incomplete-days must be between 1 and 365 when supplied")
	}
	defaults, err := storagepolicy.Expirations(purpose, prefix)
	if err != nil {
		return "", err
	}
	configuration := storageLifecycleConfiguration{XMLNS: "http://s3.amazonaws.com/doc/2006-03-01/"}
	for _, item := range defaults {
		rule := storageLifecycleRule{ID: item.ID, Status: "Enabled"}
		rule.Filter.Prefix, rule.Expiration.Days = item.Prefix, item.Days
		if abortDays != 0 {
			rule.Abort = &storageLifecycleAbort{Days: abortDays}
		}
		configuration.Rules = append(configuration.Rules, rule)
	}
	body, err := xml.MarshalIndent(configuration, "", "  ")
	return xml.Header + string(body) + "\n", err
}

func (c deploymentCredentialChecker) planStorageLifecycle(cfg deploymentConfig, values map[string]string, purpose string, abortDays int) (storageLifecyclePlan, error) {
	target := cfg.Storage.RuntimeMedia
	credentialPrefix := "LINODE_MEDIA_OBJ_"
	switch purpose {
	case "artifacts":
		target, credentialPrefix = cfg.Storage.ReleaseArtifacts, "LINODE_ARTIFACT_OBJ_"
	case "ota":
		if cfg.Storage.OTAMode != "dedicated" {
			return storageLifecyclePlan{}, errors.New("OTA storage is not configured as dedicated")
		}
		target, credentialPrefix = cfg.Storage.OTAFirmware, "LINODE_OTA_OBJ_"
	case "media":
	default:
		return storageLifecyclePlan{}, errors.New("--purpose must be artifacts, media, or ota")
	}
	proposed, err := plannedStorageLifecycleXML(purpose, target.Prefix, abortDays)
	if err != nil {
		return storageLifecyclePlan{}, err
	}
	plan := storageLifecyclePlan{
		Environment: cfg.Environment, Purpose: purpose, Target: target,
		ObservedAt: time.Now().UTC().Format(time.RFC3339), Policy: "docs/object-storage-policy.md",
		ProposedRulesXML: proposed,
		Prerequisites: []string{
			"Classify existing objects and move held evidence outside every matching expiration prefix before approval.",
			"Merge reviewed defaults with existing lifecycle; preserve existing protections and resolve overlapping filters. Proposed XML is not a replacement configuration.",
			"Inventory noncurrent versions, delete markers and unfinished uploads; this plan includes no noncurrent-version expiry.",
			"Review original object ages and intended expiry after copying; a new destination timestamp can extend retention.",
			"Approve the exact bucket and rules, then verify provider readback and later deletion evidence using the policy's separate execution workflow.",
		},
	}
	if abortDays == 0 {
		plan.Prerequisites = append(plan.Prerequisites, "No multipart abort rule proposed; review each writer's maximum upload and retry duration before selecting --abort-incomplete-days.")
	} else {
		plan.Prerequisites = append(plan.Prerequisites, fmt.Sprintf("Confirm %d days exceeds every matching multipart writer's supported upload and retry duration; abort is limited to the proposed disposable prefixes.", abortDays))
	}
	if strings.TrimSpace(values["LINODE_TOKEN"]) == "" {
		return plan, errors.New("LINODE_TOKEN is required for read-only bucket identity discovery")
	}
	bucket, err := c.resolveStorageBucket(values["LINODE_TOKEN"], target)
	if err != nil {
		plan.ExistingStatus = "unresolved bucket identity: " + err.Error()
		return plan, nil
	}
	endpoint, err := normalizeLinodeS3Endpoint(bucket.S3Endpoint)
	if err != nil {
		return plan, err
	}
	plan.Target.Endpoint = endpoint
	store := provisionObjectStore{bucket: target.Bucket, region: target.Region, endpoint: endpoint,
		accessKey: values[credentialPrefix+"ACCESS_KEY_ID"], secretKey: values[credentialPrefix+"SECRET_ACCESS_KEY"]}
	if purpose == "media" {
		store.accessKey = firstNonEmpty(store.accessKey, values["LINODE_OBJ_ACCESS_KEY_ID"])
		store.secretKey = firstNonEmpty(store.secretKey, values["LINODE_OBJ_SECRET_ACCESS_KEY"])
	}
	if store.accessKey == "" || store.secretKey == "" {
		plan.ExistingStatus = "unresolved: purpose-scoped credentials are unavailable"
		return plan, nil
	}
	body, err := provisionSignedObjectRequestWithClient(c.client, store, http.MethodGet, "", url.Values{"lifecycle": {""}}, nil)
	if err != nil {
		plan.ExistingStatus = "unresolved lifecycle readback: " + err.Error()
		return plan, nil
	}
	var configuration struct{ XMLName xml.Name }
	if xml.Unmarshal(body, &configuration) != nil || configuration.XMLName.Local != "LifecycleConfiguration" {
		plan.ExistingStatus = "unresolved: invalid lifecycle XML"
		return plan, nil
	}
	plan.ExistingStatus, plan.ExistingXML, plan.ExistingSHA256 = "retrieved; review required", string(body), provisionHexSHA256(body)
	return plan, nil
}

func runObjectStorageLifecyclePlan(args []string) error {
	fs := flag.NewFlagSet("object-storage-lifecycle-plan", flag.ContinueOnError)
	environment := fs.String("environment", "", "registered environment")
	candidate := fs.String("candidate-profile", "", "optional isolated destination credentials")
	purpose := fs.String("purpose", "", "artifacts, media, or ota")
	out := fs.String("out", "", "private JSON review plan path")
	abortDays := fs.Int("abort-incomplete-days", 0, "optional reviewed abort period (1..365 days), restricted to disposable prefixes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *environment == "" || *out == "" {
		return errors.New("--environment, --purpose and --out are required; positional arguments are unsupported")
	}
	if *purpose != "artifacts" && *purpose != "media" && *purpose != "ota" {
		return errors.New("--purpose must be artifacts, media, or ota")
	}
	explicitAbort := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "abort-incomplete-days" {
			explicitAbort = true
		}
	})
	if *abortDays < 0 || *abortDays > 365 || explicitAbort && *abortDays == 0 {
		return errors.New("--abort-incomplete-days must be between 1 and 365 when supplied")
	}
	cfg, err := resolveDeploymentConfig("", *environment, "")
	if err != nil {
		return err
	}
	values, check := deploymentCredentialProfileValues(*environment, *candidate, "")
	if !check.Passed {
		return errors.New(check.Detail)
	}
	if *candidate != "" && values["RTK_STORAGE_CANDIDATE_ENVIRONMENT"] != *environment {
		return errors.New("candidate profile does not belong to this environment")
	}
	c := defaultDeploymentCredentialChecker()
	c.readOnly = true
	plan, err := c.planStorageLifecycle(cfg, values, *purpose, *abortDays)
	if err != nil {
		return err
	}
	if err := writeStorageState(*out, plan); err != nil {
		return err
	}
	// Print only the local path. Existing lifecycle can contain private filters.
	_, err = fmt.Fprintln(os.Stdout, "Lifecycle review plan saved:", *out)
	return err
}
