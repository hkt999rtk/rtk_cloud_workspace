package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
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
	prefix    string // Explicit source prefix for storage migration.
	prefixSet bool
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

type provisionObjectStorageHTTPError struct {
	Method     string
	Target     string
	StatusCode int
}

func (e *provisionObjectStorageHTTPError) Error() string {
	return fmt.Sprintf("Object Storage %s %s failed: HTTP %d", e.Method, e.Target, e.StatusCode)
}

func provisionObjectStoreFromEnv(operator map[string]string) (provisionObjectStore, error) {
	store := provisionObjectStore{
		bucket:    operator["LINODE_OBJ_BUCKET"],
		endpoint:  strings.TrimRight(operator["LINODE_OBJ_ENDPOINT"], "/"),
		accessKey: firstNonEmpty(operator["LINODE_OBJ_ACCESS_KEY_ID"], operator["AWS_ACCESS_KEY_ID"]),
		secretKey: firstNonEmpty(operator["LINODE_OBJ_SECRET_ACCESS_KEY"], operator["AWS_SECRET_ACCESS_KEY"]),
		region:    operator["LINODE_OBJ_REGION"],
	}
	store.prefix, store.prefixSet = operator["LINODE_OBJ_PREFIX"]
	store.prefix = strings.Trim(store.prefix, "/")
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
	seenTokens := map[string]bool{}
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
		if !parsed.IsTruncated {
			break
		}
		if parsed.NextContinuationToken == "" || seenTokens[parsed.NextContinuationToken] {
			return nil, errors.New("truncated object inventory has missing or repeated continuation token")
		}
		seenTokens[parsed.NextContinuationToken] = true
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
	prefixRoot := filepath.Join(bucketRoot, filepath.FromSlash(prefix))
	entries := []provisionObjectEntry{}
	err = filepath.WalkDir(prefixRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if path == prefixRoot && errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			return walkErr
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

type storageObjectSnapshot struct {
	Body          []byte
	Headers       http.Header
	Tags          string
	MetadataKnown bool
}

// This migrator deliberately handles current, completed objects only. Refuse
// buckets with version history or unfinished uploads rather than silently
// declaring a partial bucket migration complete.
func validateStorageCopyBucket(client *http.Client, store provisionObjectStore) error {
	if strings.HasPrefix(store.endpoint, "file://") {
		return nil
	}
	for _, inspection := range []struct{ query, root string }{{"versioning", "VersioningConfiguration"}, {"versions", "ListVersionsResult"}, {"uploads", "ListMultipartUploadsResult"}} {
		query := url.Values{inspection.query: {""}}
		seen := map[string]bool{}
		for {
			body, err := provisionSignedObjectRequestWithClient(client, store, http.MethodGet, "", query, nil)
			if err != nil {
				return fmt.Errorf("inspect bucket %s %s before migration: %w", store.bucket, inspection.query, err)
			}
			var result struct {
				XMLName     xml.Name
				Status      string `xml:"Status"`
				Truncated   bool   `xml:"IsTruncated"`
				NextKey     string `xml:"NextKeyMarker"`
				NextVersion string `xml:"NextVersionIdMarker"`
				Versions    []struct {
					ID string `xml:"VersionId"`
				} `xml:"Version"`
				DeleteMarkers []struct{} `xml:"DeleteMarker"`
				Uploads       []struct{} `xml:"Upload"`
			}
			if err := xml.Unmarshal(body, &result); err != nil || result.XMLName.Local != inspection.root {
				return fmt.Errorf("bucket %s returned invalid %s inventory", store.bucket, inspection.query)
			}
			if result.Status != "" || len(result.DeleteMarkers) > 0 || len(result.Uploads) > 0 {
				return fmt.Errorf("bucket %s has versioning, delete markers, or incomplete multipart uploads; complete history migration/drain is required", store.bucket)
			}
			for _, version := range result.Versions {
				if version.ID != "" && version.ID != "null" {
					return fmt.Errorf("bucket %s contains version history; complete history migration is required", store.bucket)
				}
			}
			if !result.Truncated {
				break
			}
			marker := result.NextKey + "\x00" + result.NextVersion
			if inspection.query != "versions" || result.NextKey == "" || seen[marker] {
				return fmt.Errorf("bucket %s has incomplete or repeating %s inventory", store.bucket, inspection.query)
			}
			seen[marker] = true
			query.Set("key-marker", result.NextKey)
			query.Set("version-id-marker", result.NextVersion)
		}
	}
	return nil
}

// Object metadata participates in the copy proof. A byte-identical object with
// different content type, checksum metadata or tags is a migration conflict.
func (o storageObjectSnapshot) proof() storageObjectProof {
	proof := otaObjectProof(o.Body)
	if o.MetadataKnown {
		attributes, _ := json.Marshal(struct {
			Headers http.Header `json:"headers"`
			Tags    string      `json:"tags"`
		}{o.Headers, o.Tags})
		proof.AttributesSHA256 = provisionHexSHA256(attributes)
	}
	return proof
}

func readStorageObject(client *http.Client, store provisionObjectStore, key string) (storageObjectSnapshot, error) {
	if strings.HasPrefix(store.endpoint, "file://") {
		body, err := provisionReadObject(store, key)
		return storageObjectSnapshot{Body: body}, err
	}
	// Disable transparent gzip decoding: the stored bytes and their content
	// encoding must be copied together. Request provider checksum headers too.
	body, responseHeaders, err := provisionSignedObjectRequestHeaders(client, store, http.MethodGet, key, nil, nil, http.Header{
		"Accept-Encoding": {"identity"}, "X-Amz-Checksum-Mode": {"ENABLED"},
	})
	if err != nil {
		return storageObjectSnapshot{}, err
	}
	if strings.EqualFold(responseHeaders.Get("X-Amz-Checksum-Type"), "COMPOSITE") {
		return storageObjectSnapshot{}, fmt.Errorf("object %s has a multipart composite checksum; a metadata-preserving multipart migration is required", key)
	}
	if checksum := responseHeaders.Get("X-Amz-Checksum-Sha256"); checksum != "" {
		sum := sha256.Sum256(body)
		if checksum != base64.StdEncoding.EncodeToString(sum[:]) {
			return storageObjectSnapshot{}, fmt.Errorf("provider SHA-256 checksum mismatch for %s", key)
		}
	}
	headers := make(http.Header)
	for name, values := range responseHeaders {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "x-amz-meta-") || strings.HasPrefix(lower, "x-amz-checksum-") ||
			keySet("content-type", "cache-control", "content-disposition", "content-encoding", "content-language", "expires")[lower] {
			for _, value := range values {
				headers.Add(name, value)
			}
		}
	}
	tagQuery := url.Values{"tagging": {""}}
	if version := responseHeaders.Get("X-Amz-Version-Id"); version != "" {
		tagQuery.Set("versionId", version)
	}
	tagBody, err := provisionSignedObjectRequestWithClient(client, store, http.MethodGet, key, tagQuery, nil)
	if err != nil {
		return storageObjectSnapshot{}, fmt.Errorf("read object tags %s: %w", key, err)
	}
	var tagging struct {
		XMLName xml.Name `xml:"Tagging"`
		Tags    []struct {
			Key   string `xml:"Key"`
			Value string `xml:"Value"`
		} `xml:"TagSet>Tag"`
	}
	if err := xml.Unmarshal(tagBody, &tagging); err != nil {
		return storageObjectSnapshot{}, fmt.Errorf("decode object tags %s: %w", key, err)
	}
	tags := url.Values{}
	for _, tag := range tagging.Tags {
		tags.Add(tag.Key, tag.Value)
	}
	return storageObjectSnapshot{Body: body, Headers: headers, Tags: strings.ReplaceAll(tags.Encode(), "+", "%20"), MetadataKnown: true}, nil
}

func verifyStorageObject(source, destination storageObjectSnapshot, key string) error {
	// Providers may add a default checksum when writing an object which had
	// none. Existing checksums must survive; additional verified checksums do
	// not change application metadata and are safe to retain.
	if source.MetadataKnown {
		destination.Headers = destination.Headers.Clone()
		for name := range destination.Headers {
			if strings.HasPrefix(strings.ToLower(name), "x-amz-checksum-") && source.Headers.Get(name) == "" {
				destination.Headers.Del(name)
			}
		}
	}
	want, got := source.proof(), destination.proof()
	if want.SHA256 != got.SHA256 || want.Bytes != got.Bytes {
		return fmt.Errorf("checksum mismatch for %s", key)
	}
	if source.MetadataKnown && want.AttributesSHA256 != got.AttributesSHA256 {
		return fmt.Errorf("object metadata or tags mismatch for %s", key)
	}
	return nil
}

func copyStorageObject(client *http.Client, source storageObjectSnapshot, destination provisionObjectStore, key string) error {
	existing, err := readStorageObject(client, destination, key)
	if err == nil {
		return verifyStorageObject(source, existing, key)
	}
	var responseErr *provisionObjectStorageHTTPError
	if !errors.As(err, &responseErr) || responseErr.StatusCode != http.StatusNotFound {
		return err
	}
	headers := source.Headers.Clone()
	if headers == nil {
		headers = make(http.Header)
	}
	headers.Set("If-None-Match", "*")
	// PutObject creates a full-object checksum. Checksum-Type is a response
	// property, not a writable PutObject request header.
	headers.Del("X-Amz-Checksum-Type")
	if source.Tags != "" {
		headers.Set("X-Amz-Tagging", source.Tags)
	}
	_, _, putErr := provisionSignedObjectRequestHeaders(client, destination, http.MethodPut, key, nil, source.Body, headers)
	// Even an ambiguous PUT or a concurrent conditional-write failure is safe
	// only when a fresh read proves the entire intended object is present.
	written, readErr := readStorageObject(client, destination, key)
	if readErr != nil {
		return errors.Join(putErr, readErr)
	}
	return verifyStorageObject(source, written, key)
}

func provisionCreateObjectBucketWithClient(client *http.Client, store provisionObjectStore) error {
	if strings.HasPrefix(store.endpoint, "file://") {
		root, err := provisionFileObjectRoot(store)
		if err != nil {
			return err
		}
		return os.MkdirAll(filepath.Join(root, store.bucket), 0o700)
	}
	_, err := provisionSignedObjectRequestWithClient(client, store, http.MethodPut, "", nil, nil)
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
	return provisionSignedObjectRequestWithClient(http.DefaultClient, store, method, key, query, body)
}

func provisionSignedObjectRequestWithClient(client *http.Client, store provisionObjectStore, method, key string, query url.Values, body []byte) ([]byte, error) {
	data, _, err := provisionSignedObjectRequestHeaders(client, store, method, key, query, body, nil)
	return data, err
}

// Keep response metadata available to migration without changing the callers
// which only need an object body. Every supplied header is covered by SigV4.
func provisionSignedObjectRequestHeaders(client *http.Client, store provisionObjectStore, method, key string, query url.Values, body []byte, headers http.Header) ([]byte, http.Header, error) {
	endpointURL, err := url.Parse(store.endpoint)
	if err != nil {
		return nil, nil, err
	}
	canonicalURI := "/" + store.bucket
	if key != "" {
		canonicalURI += "/" + provisionEscapeObjectPath(key)
	}
	endpointURL.Path, err = url.PathUnescape(canonicalURI)
	if err != nil {
		return nil, nil, err
	}
	endpointURL.RawPath = canonicalURI
	endpointURL.RawQuery = provisionCanonicalQuery(query)
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	date := now.Format("20060102")
	payloadHash := provisionHexSHA256(body)
	req, err := http.NewRequest(method, endpointURL.String(), bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	for name, values := range headers {
		req.Header[name] = append([]string(nil), values...)
	}
	req.Header.Set("Host", endpointURL.Host)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	req.Header.Set("X-Amz-Date", amzDate)
	headerNames := make([]string, 0, len(req.Header))
	for name := range req.Header {
		headerNames = append(headerNames, strings.ToLower(name))
	}
	sort.Strings(headerNames)
	signedHeaders := strings.Join(headerNames, ";")
	var canonicalHeaders string
	for _, name := range headerNames {
		canonicalHeaders += name + ":" + strings.Join(strings.Fields(strings.Join(req.Header.Values(name), ",")), " ") + "\n"
	}
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
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		target := key
		if target == "" {
			target = "bucket"
		}
		return nil, resp.Header, &provisionObjectStorageHTTPError{Method: method, Target: target, StatusCode: resp.StatusCode}
	}
	return data, resp.Header, nil
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
			pairs = append(pairs, strings.ReplaceAll(url.QueryEscape(key), "+", "%20")+"="+strings.ReplaceAll(url.QueryEscape(value), "+", "%20"))
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
