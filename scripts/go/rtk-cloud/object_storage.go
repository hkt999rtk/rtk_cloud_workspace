package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type provisionObjectStore struct {
	bucket    string
	endpoint  string
	accessKey string
	secretKey string
	region    string
}

type provisionObjectEntry struct {
	Key          string `xml:"Key"`
	LastModified string `xml:"LastModified"`
	Size         int64  `xml:"Size"`
}

type provisionListBucketResult struct {
	Contents              []provisionObjectEntry `xml:"Contents"`
	IsTruncated           bool                   `xml:"IsTruncated"`
	NextContinuationToken string                 `xml:"NextContinuationToken"`
}

func provisionObjectStoreFromEnv(operator map[string]string) (provisionObjectStore, error) {
	store := provisionObjectStore{
		bucket:    firstNonEmpty(operator["LINODE_OBJ_BUCKET"], os.Getenv("LINODE_OBJ_BUCKET")),
		endpoint:  strings.TrimRight(firstNonEmpty(operator["LINODE_OBJ_ENDPOINT"], os.Getenv("LINODE_OBJ_ENDPOINT")), "/"),
		accessKey: firstNonEmpty(operator["LINODE_OBJ_ACCESS_KEY_ID"], operator["AWS_ACCESS_KEY_ID"], os.Getenv("LINODE_OBJ_ACCESS_KEY_ID"), os.Getenv("AWS_ACCESS_KEY_ID")),
		secretKey: firstNonEmpty(operator["LINODE_OBJ_SECRET_ACCESS_KEY"], operator["AWS_SECRET_ACCESS_KEY"], os.Getenv("LINODE_OBJ_SECRET_ACCESS_KEY"), os.Getenv("AWS_SECRET_ACCESS_KEY")),
		region:    firstNonEmpty(operator["LINODE_OBJ_REGION"], os.Getenv("LINODE_OBJ_REGION")),
	}
	if store.bucket == "" {
		return store, errors.New("LINODE_OBJ_BUCKET is required")
	}
	if store.endpoint == "" {
		return store, errors.New("LINODE_OBJ_ENDPOINT is required")
	}
	if strings.HasPrefix(store.endpoint, "file://") {
		return store, nil
	}
	if store.accessKey == "" || store.secretKey == "" {
		return store, errors.New("LINODE_OBJ_ACCESS_KEY_ID and LINODE_OBJ_SECRET_ACCESS_KEY are required")
	}
	if store.region == "" {
		store.region = provisionObjectRegionFromEndpoint(store.endpoint)
	}
	return store, nil
}

func provisionListObjects(store provisionObjectStore, prefix string) ([]provisionObjectEntry, error) {
	if strings.HasPrefix(store.endpoint, "file://") {
		return provisionListObjectsFromFile(store, prefix)
	}
	entries := []provisionObjectEntry{}
	token := ""
	for {
		query := url.Values{}
		query.Set("list-type", "2")
		query.Set("prefix", prefix)
		if token != "" {
			query.Set("continuation-token", token)
		}
		body, err := provisionSignedObjectRequest(store, http.MethodGet, "", query, nil)
		if err != nil {
			return nil, err
		}
		var parsed provisionListBucketResult
		if err := xml.Unmarshal(body, &parsed); err != nil {
			return nil, err
		}
		entries = append(entries, parsed.Contents...)
		if !parsed.IsTruncated || parsed.NextContinuationToken == "" {
			break
		}
		token = parsed.NextContinuationToken
	}
	return entries, nil
}

func provisionListObjectsFromFile(store provisionObjectStore, prefix string) ([]provisionObjectEntry, error) {
	root, err := provisionFileObjectRoot(store)
	if err != nil {
		return nil, err
	}
	bucketRoot := filepath.Join(root, store.bucket)
	entries := []provisionObjectEntry{}
	err = filepath.WalkDir(filepath.Join(bucketRoot, filepath.FromSlash(prefix)), func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(bucketRoot, path)
		if err != nil {
			return err
		}
		entries = append(entries, provisionObjectEntry{
			Key:          filepath.ToSlash(rel),
			LastModified: info.ModTime().UTC().Format(time.RFC3339),
			Size:         info.Size(),
		})
		return nil
	})
	return entries, err
}

func provisionReadObject(store provisionObjectStore, key string) ([]byte, error) {
	if strings.HasPrefix(store.endpoint, "file://") {
		root, err := provisionFileObjectRoot(store)
		if err != nil {
			return nil, err
		}
		return os.ReadFile(filepath.Join(root, store.bucket, filepath.FromSlash(key)))
	}
	return provisionSignedObjectRequest(store, http.MethodGet, key, nil, nil)
}

func provisionObjectExists(store provisionObjectStore, key string) error {
	if strings.HasPrefix(store.endpoint, "file://") {
		root, err := provisionFileObjectRoot(store)
		if err != nil {
			return err
		}
		_, err = os.Stat(filepath.Join(root, store.bucket, filepath.FromSlash(key)))
		return err
	}
	_, err := provisionSignedObjectRequest(store, http.MethodHead, key, nil, nil)
	return err
}

func provisionWriteObjectToFile(store provisionObjectStore, key, out string) error {
	data, err := provisionReadObject(store, key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	return os.WriteFile(out, data, 0o600)
}

func provisionSignedObjectRequest(store provisionObjectStore, method, key string, query url.Values, body []byte) ([]byte, error) {
	endpointURL, err := url.Parse(store.endpoint)
	if err != nil {
		return nil, err
	}
	canonicalURI := "/" + store.bucket
	if key != "" {
		canonicalURI += "/" + provisionEscapeObjectPath(key)
	}
	endpointURL.Path = canonicalURI
	endpointURL.RawQuery = provisionCanonicalQuery(query)
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	date := now.Format("20060102")
	payloadHash := provisionHexSHA256(body)
	req, err := http.NewRequest(method, endpointURL.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Host", endpointURL.Host)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	req.Header.Set("X-Amz-Date", amzDate)
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	canonicalHeaders := "host:" + endpointURL.Host + "\n" +
		"x-amz-content-sha256:" + payloadHash + "\n" +
		"x-amz-date:" + amzDate + "\n"
	canonicalRequest := strings.Join([]string{
		method,
		canonicalURI,
		endpointURL.RawQuery,
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")
	scope := date + "/" + store.region + "/s3/aws4_request"
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		provisionHexSHA256([]byte(canonicalRequest)),
	}, "\n")
	signature := hex.EncodeToString(provisionHMACSHA256(provisionSigningKey(store.secretKey, date, store.region), []byte(stringToSign)))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+store.accessKey+"/"+scope+", SignedHeaders="+signedHeaders+", Signature="+signature)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Object Storage %s %s failed: HTTP %d", method, key, resp.StatusCode)
	}
	return data, nil
}

func provisionFileObjectRoot(store provisionObjectStore) (string, error) {
	u, err := url.Parse(store.endpoint)
	if err != nil {
		return "", err
	}
	return u.Path, nil
}

func provisionObjectRegionFromEndpoint(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "us-east-1"
	}
	host := u.Hostname()
	if strings.HasSuffix(host, ".linodeobjects.com") {
		parts := strings.Split(host, ".")
		if len(parts) > 0 && parts[0] != "" {
			return parts[0]
		}
	}
	return "us-east-1"
}

func provisionEscapeObjectPath(path string) string {
	parts := strings.Split(path, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func provisionCanonicalQuery(values url.Values) string {
	if len(values) == 0 {
		return ""
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	pairs := []string{}
	for _, key := range keys {
		vals := append([]string{}, values[key]...)
		sort.Strings(vals)
		for _, value := range vals {
			pairs = append(pairs, url.QueryEscape(key)+"="+url.QueryEscape(value))
		}
	}
	return strings.Join(pairs, "&")
}

func provisionHexSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func provisionHMACSHA256(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}

func provisionSigningKey(secret, date, region string) []byte {
	kDate := provisionHMACSHA256([]byte("AWS4"+secret), []byte(date))
	kRegion := provisionHMACSHA256(kDate, []byte(region))
	kService := provisionHMACSHA256(kRegion, []byte("s3"))
	return provisionHMACSHA256(kService, []byte("aws4_request"))
}
