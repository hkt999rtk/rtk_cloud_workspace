package main

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/internal/storagepolicy"
)

// Account inventory is intentionally read-only. Unknown access or ownership is
// reported as unresolved; empty current-object counts never authorize deletion.
type storageBucketAudit struct {
	linodeStorageBucket
	Size          int64                         `json:"size"`
	Objects       int64                         `json:"objects"`
	Prefixes      map[string]storagePrefixAudit `json:"prefixes,omitempty"`
	Configuration map[string]string             `json:"configuration,omitempty"`
	Access        string                        `json:"access"`
}
type storagePrefixAudit struct {
	Objects                     int64  `json:"objects"`
	Bytes                       int64  `json:"bytes"`
	Oldest                      string `json:"oldest"`
	Newest                      string `json:"newest"`
	PastDefaultRetentionObjects int64  `json:"past_default_retention_objects,omitempty"`
	PastDefaultRetentionBytes   int64  `json:"past_default_retention_bytes,omitempty"`
}
type storageAuditReport struct {
	Environment string               `json:"credential_environment"`
	ObservedAt  string               `json:"observed_at"`
	Buckets     []storageBucketAudit `json:"buckets"`
}

func (c deploymentCredentialChecker) storageAccountInventory(token, resource string, result any) error {
	if resource != "buckets" && resource != "keys" {
		return errors.New("unsupported storage inventory resource")
	}
	return c.cachedStorageInventory(resource, result, func() ([]byte, error) {
		var combined []json.RawMessage
		for page := 1; ; page++ {
			if err := c.checkContext().Err(); err != nil {
				return nil, err
			}
			body, err := c.linodeAuthorizedRequest(token, http.MethodGet, fmt.Sprintf("/object-storage/%s?page_size=500&page=%d", resource, page), nil)
			if err != nil {
				return nil, fmt.Errorf("storage %s inventory page %d: %w", resource, page, err)
			}
			var response struct {
				Data  []json.RawMessage `json:"data"`
				Page  int               `json:"page"`
				Pages int               `json:"pages"`
			}
			if err := json.Unmarshal(body, &response); err != nil || response.Data == nil {
				return nil, errors.New("storage inventory returned invalid JSON data")
			}
			if response.Page != 0 && response.Page != page {
				return nil, errors.New("storage inventory returned an unexpected page")
			}
			combined = append(combined, response.Data...)
			if response.Pages <= page {
				break
			}
		}
		return json.Marshal(combined)
	})
}

func storageAuditCredentials(values map[string]string, keys []linodeStorageKey, bucket linodeStorageBucket) (provisionObjectStore, bool) {
	for _, prefix := range []string{"LINODE_MEDIA_OBJ_", "LINODE_MEDIA_ROLLBACK_OBJ_", "LINODE_OTA_OBJ_", "LINODE_ARTIFACT_OBJ_", "LINODE_OBJ_"} {
		access, secret := values[prefix+"ACCESS_KEY_ID"], values[prefix+"SECRET_ACCESS_KEY"]
		if access == "" || secret == "" {
			continue
		}
		for _, key := range keys {
			if key.AccessKey != access {
				continue
			}
			for _, grant := range key.BucketAccess {
				if grant.BucketName == bucket.Label && grant.Region == bucket.Region {
					endpoint, err := normalizeLinodeS3Endpoint(bucket.S3Endpoint)
					if err != nil {
						return provisionObjectStore{}, false
					}
					return provisionObjectStore{bucket: bucket.Label, region: bucket.Region, endpoint: endpoint, accessKey: access, secretKey: secret}, true
				}
			}
		}
	}
	return provisionObjectStore{}, false
}

func summarizeStoragePrefixes(entries []provisionObjectEntry) map[string]storagePrefixAudit {
	result := map[string]storagePrefixAudit{}
	for _, entry := range entries {
		prefix, _, found := strings.Cut(entry.Key, "/")
		if found {
			prefix += "/"
		} else {
			prefix = "(root objects)"
		}
		row := result[prefix]
		row.Objects++
		row.Bytes += entry.Size
		if days := storagepolicy.RetentionDays(prefix); days > 0 {
			if modified, err := time.Parse(time.RFC3339Nano, entry.LastModified); err == nil && modified.Before(time.Now().UTC().AddDate(0, 0, -days)) {
				row.PastDefaultRetentionObjects++
				row.PastDefaultRetentionBytes += entry.Size
			}
		}
		if row.Oldest == "" || entry.LastModified < row.Oldest {
			row.Oldest = entry.LastModified
		}
		if entry.LastModified > row.Newest {
			row.Newest = entry.LastModified
		}
		result[prefix] = row
	}
	return result
}

func auditStorageConfiguration(c deploymentCredentialChecker, store provisionObjectStore) map[string]string {
	result := map[string]string{}
	for _, name := range []string{"versioning", "versions", "uploads", "lifecycle", "acl", "cors", "policy", "encryption", "object-lock"} {
		query := url.Values{name: {""}}
		if name == "versions" || name == "uploads" {
			query.Set("max-keys", "1")
			if name == "uploads" {
				query.Del("max-keys")
				query.Set("max-uploads", "1")
			}
		}
		body, err := provisionSignedObjectRequestWithClient(c.client, store, http.MethodGet, "", query, nil)
		if err != nil {
			var response *provisionObjectStorageHTTPError
			if errors.As(err, &response) {
				result[name] = fmt.Sprintf("HTTP %d; verify capability or absence", response.StatusCode)
			} else {
				result[name] = "unresolved request failure"
			}
			continue
		}
		if name == "versions" || name == "uploads" {
			var history struct {
				XMLName   xml.Name
				Versions  []struct{} `xml:"Version"`
				Markers   []struct{} `xml:"DeleteMarker"`
				Uploads   []struct{} `xml:"Upload"`
				Truncated bool       `xml:"IsTruncated"`
			}
			root := map[string]string{"versions": "ListVersionsResult", "uploads": "ListMultipartUploadsResult"}[name]
			if xml.Unmarshal(body, &history) != nil || history.XMLName.Local != root {
				result[name] = "unresolved invalid XML"
				continue
			}
			if len(history.Versions)+len(history.Markers)+len(history.Uploads) > 0 || history.Truncated {
				result[name] = "present; enumerate before migration or retirement"
			} else {
				result[name] = "empty"
			}
		} else {
			if name == "policy" {
				if !json.Valid(body) {
					result[name] = "unresolved invalid policy JSON"
					continue
				}
			} else {
				var configuration struct{ XMLName xml.Name }
				root := map[string]string{"versioning": "VersioningConfiguration", "lifecycle": "LifecycleConfiguration", "acl": "AccessControlPolicy", "cors": "CORSConfiguration", "encryption": "ServerSideEncryptionConfiguration", "object-lock": "ObjectLockConfiguration"}[name]
				if xml.Unmarshal(body, &configuration) != nil || configuration.XMLName.Local != root {
					result[name] = "unresolved invalid configuration XML"
					continue
				}
			}
			// Configuration JSON/XML may include principals and private policy details.
			// Record its fingerprint only; retrieve the exact source privately for migration.
			result[name] = "retrieved sha256:" + provisionHexSHA256(body)
		}
	}
	return result
}

func runObjectStorageAudit(args []string) error {
	fs := flag.NewFlagSet("object-storage-audit", flag.ContinueOnError)
	environment := fs.String("environment", "", "environment-local credentials")
	out := fs.String("out", "", "sanitized JSON report path")
	inspect := fs.Bool("inspect", false, "inspect object namespaces and configuration using available scoped keys")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("object-storage-audit does not accept positional arguments")
	}
	if *environment == "" {
		return errors.New("--environment is required")
	}
	values, check := deploymentCredentialProfileValues(*environment, "", "")
	if !check.Passed {
		return errors.New(check.Detail)
	}
	if values["LINODE_TOKEN"] == "" {
		return errors.New("LINODE_TOKEN is required")
	}
	c := defaultDeploymentCredentialChecker()
	c.readOnly = true
	report := storageAuditReport{Environment: *environment, ObservedAt: time.Now().UTC().Format(time.RFC3339)}
	if err := c.storageAccountInventory(values["LINODE_TOKEN"], "buckets", &report.Buckets); err != nil {
		return err
	}
	var keys []linodeStorageKey
	if *inspect {
		if err := c.storageAccountInventory(values["LINODE_TOKEN"], "keys", &keys); err != nil {
			return err
		}
	}
	for i := range report.Buckets {
		bucket := &report.Buckets[i]
		bucket.Access = "not inspected"
		if !*inspect {
			continue
		}
		store, ok := storageAuditCredentials(values, keys, bucket.linodeStorageBucket)
		if !ok {
			bucket.Access = "no verified environment-local scoped key; ownership unresolved"
			continue
		}
		entries, err := provisionListObjects(store, "")
		if err != nil {
			bucket.Access = "scoped list failed; ownership unresolved"
			continue
		}
		bucket.Access = "scoped list verified"
		bucket.Prefixes = summarizeStoragePrefixes(entries)
		bucket.Configuration = auditStorageConfiguration(c, store)
	}
	sort.Slice(report.Buckets, func(i, j int) bool { return report.Buckets[i].Label < report.Buckets[j].Label })
	body, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if *out != "" {
		return writeStorageState(*out, report)
	}
	fmt.Print(string(body))
	return nil
}
