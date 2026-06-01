package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"flag"
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

type objectStore struct {
	bucket    string
	endpoint  string
	accessKey string
	secretKey string
	region    string
}

type listBucketResult struct {
	Contents              []objectEntry `xml:"Contents"`
	IsTruncated           bool          `xml:"IsTruncated"`
	NextContinuationToken string        `xml:"NextContinuationToken"`
}

type objectEntry struct {
	Key          string `xml:"Key"`
	LastModified string `xml:"LastModified"`
	Size         int64  `xml:"Size"`
}

func main() {
	if len(os.Args) < 2 {
		die("usage: linode-object-storage <list-manifests|cat|exists|download|upload-dir> [options]")
	}
	store, err := storeFromEnv()
	if err != nil {
		die(err.Error())
	}
	switch os.Args[1] {
	case "list-manifests":
		fs := flag.NewFlagSet("list-manifests", flag.ExitOnError)
		releasePrefix := fs.String("release-prefix", "", "release key prefix, for example rtk_video_cloud")
		_ = fs.Parse(os.Args[2:])
		if *releasePrefix == "" {
			die("--release-prefix is required")
		}
		if err := listManifests(store, *releasePrefix); err != nil {
			die(err.Error())
		}
	case "cat":
		fs := flag.NewFlagSet("cat", flag.ExitOnError)
		key := fs.String("key", "", "object key")
		_ = fs.Parse(os.Args[2:])
		if *key == "" {
			die("--key is required")
		}
		if err := writeObject(store, *key, os.Stdout); err != nil {
			die(err.Error())
		}
	case "exists":
		fs := flag.NewFlagSet("exists", flag.ExitOnError)
		key := fs.String("key", "", "object key")
		_ = fs.Parse(os.Args[2:])
		if *key == "" {
			die("--key is required")
		}
		if err := objectExists(store, *key); err != nil {
			die(err.Error())
		}
	case "download":
		fs := flag.NewFlagSet("download", flag.ExitOnError)
		key := fs.String("key", "", "object key")
		out := fs.String("out", "", "output path")
		_ = fs.Parse(os.Args[2:])
		if *key == "" || *out == "" {
			die("--key and --out are required")
		}
		fh, err := os.Create(*out)
		if err != nil {
			die(err.Error())
		}
		defer fh.Close()
		if err := writeObject(store, *key, fh); err != nil {
			die(err.Error())
		}
	case "upload-dir":
		fs := flag.NewFlagSet("upload-dir", flag.ExitOnError)
		source := fs.String("source", "", "source directory")
		prefix := fs.String("prefix", "", "destination key prefix")
		_ = fs.Parse(os.Args[2:])
		if *source == "" || *prefix == "" {
			die("--source and --prefix are required")
		}
		if err := uploadDir(store, *source, *prefix); err != nil {
			die(err.Error())
		}
	default:
		die("unknown command: " + os.Args[1])
	}
}

func die(message string) {
	fmt.Fprintln(os.Stderr, "error: "+message)
	os.Exit(1)
}

func storeFromEnv() (objectStore, error) {
	store := objectStore{
		bucket:    os.Getenv("LINODE_OBJ_BUCKET"),
		endpoint:  strings.TrimRight(os.Getenv("LINODE_OBJ_ENDPOINT"), "/"),
		accessKey: firstNonEmpty(os.Getenv("LINODE_OBJ_ACCESS_KEY_ID"), os.Getenv("AWS_ACCESS_KEY_ID")),
		secretKey: firstNonEmpty(os.Getenv("LINODE_OBJ_SECRET_ACCESS_KEY"), os.Getenv("AWS_SECRET_ACCESS_KEY")),
		region:    os.Getenv("LINODE_OBJ_REGION"),
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
		store.region = regionFromEndpoint(store.endpoint)
	}
	return store, nil
}

func listManifests(store objectStore, releasePrefix string) error {
	entries, err := listObjects(store, "releases/")
	if err != nil {
		return err
	}
	wantPrefix := "releases/" + releasePrefix + "-"
	rows := []objectEntry{}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Key, wantPrefix) && strings.HasSuffix(entry.Key, "/manifest.json") {
			rows = append(rows, entry)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].LastModified == rows[j].LastModified {
			return rows[i].Key < rows[j].Key
		}
		return rows[i].LastModified > rows[j].LastModified
	})
	for _, row := range rows {
		fmt.Printf("%s\t%s\n", displayModified(row.LastModified), row.Key)
	}
	return nil
}

func listObjects(store objectStore, prefix string) ([]objectEntry, error) {
	if strings.HasPrefix(store.endpoint, "file://") {
		return listObjectsFromFile(store, prefix)
	}
	entries := []objectEntry{}
	token := ""
	for {
		query := url.Values{}
		query.Set("list-type", "2")
		query.Set("prefix", prefix)
		if token != "" {
			query.Set("continuation-token", token)
		}
		body, err := signedRequest(store, http.MethodGet, "", query, nil)
		if err != nil {
			return nil, err
		}
		var parsed listBucketResult
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

func listObjectsFromFile(store objectStore, prefix string) ([]objectEntry, error) {
	root, err := fileRoot(store)
	if err != nil {
		return nil, err
	}
	bucketRoot := filepath.Join(root, store.bucket)
	entries := []objectEntry{}
	err = filepath.WalkDir(filepath.Join(bucketRoot, prefix), func(path string, entry os.DirEntry, walkErr error) error {
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
		entries = append(entries, objectEntry{
			Key:          filepath.ToSlash(rel),
			LastModified: info.ModTime().UTC().Format(time.RFC3339),
			Size:         info.Size(),
		})
		return nil
	})
	return entries, err
}

func writeObject(store objectStore, key string, out io.Writer) error {
	if strings.HasPrefix(store.endpoint, "file://") {
		root, err := fileRoot(store)
		if err != nil {
			return err
		}
		fh, err := os.Open(filepath.Join(root, store.bucket, filepath.FromSlash(key)))
		if err != nil {
			return err
		}
		defer fh.Close()
		_, err = io.Copy(out, fh)
		return err
	}
	body, err := signedRequest(store, http.MethodGet, key, nil, nil)
	if err != nil {
		return err
	}
	_, err = out.Write(body)
	return err
}

func objectExists(store objectStore, key string) error {
	if strings.HasPrefix(store.endpoint, "file://") {
		root, err := fileRoot(store)
		if err != nil {
			return err
		}
		_, err = os.Stat(filepath.Join(root, store.bucket, filepath.FromSlash(key)))
		return err
	}
	_, err := signedRequest(store, http.MethodHead, key, nil, nil)
	return err
}

func uploadDir(store objectStore, source, prefix string) error {
	prefix = strings.Trim(prefix, "/")
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		key := strings.TrimPrefix(prefix+"/"+filepath.ToSlash(rel), "/")
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.HasPrefix(store.endpoint, "file://") {
			root, err := fileRoot(store)
			if err != nil {
				return err
			}
			dest := filepath.Join(root, store.bucket, filepath.FromSlash(key))
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return err
			}
			return os.WriteFile(dest, data, 0o644)
		}
		_, err = signedRequest(store, http.MethodPut, key, nil, data)
		return err
	})
}

func signedRequest(store objectStore, method, key string, query url.Values, body []byte) ([]byte, error) {
	endpointURL, err := url.Parse(store.endpoint)
	if err != nil {
		return nil, err
	}
	canonicalURI := "/" + store.bucket
	if key != "" {
		canonicalURI += "/" + escapePath(key)
	}
	endpointURL.Path = canonicalURI
	endpointURL.RawQuery = canonicalQuery(query)
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	date := now.Format("20060102")
	payloadHash := hexSHA256(body)
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
		hexSHA256([]byte(canonicalRequest)),
	}, "\n")
	signature := hex.EncodeToString(hmacSHA256(signingKey(store.secretKey, date, store.region), []byte(stringToSign)))
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

func fileRoot(store objectStore) (string, error) {
	u, err := url.Parse(store.endpoint)
	if err != nil {
		return "", err
	}
	return u.Path, nil
}

func regionFromEndpoint(endpoint string) string {
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

func escapePath(path string) string {
	parts := strings.Split(path, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func canonicalQuery(values url.Values) string {
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

func displayModified(value string) string {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return value
	}
	return parsed.UTC().Format("2006-01-02 15:04:05")
}

func hexSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}

func signingKey(secret, date, region string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), []byte(date))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte("s3"))
	return hmacSHA256(kService, []byte("aws4_request"))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
