package billingbackup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/hkt999rtk/rtk_cloud_logger/billingarchive"
)

var ErrObjectMissing = errors.New("Billing object missing")

type S3Store struct {
	Client *s3.Client
	Bucket string
}

func NewS3Store(remote Remote, accessKey, secret string) (*S3Store, error) {
	if accessKey == "" || secret == "" {
		return nil, errors.New("dedicated Billing verifier storage credentials required")
	}
	u, err := url.Parse(remote.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || !billingarchive.SafeID(remote.SigningRegion) || !billingarchive.SafeID(remote.Region) || !billingarchive.SafeID(remote.Bucket) {
		return nil, errors.New("invalid pinned Billing object endpoint")
	}
	h := &http.Client{Timeout: 10 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	c := s3.New(s3.Options{Region: remote.SigningRegion, BaseEndpoint: aws.String(remote.Endpoint), UsePathStyle: true, HTTPClient: h, Credentials: credentials.NewStaticCredentialsProvider(accessKey, secret, ""), RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired, ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired})
	return &S3Store{c, remote.Bucket}, nil
}
func (s *S3Store) Read(ctx context.Context, key string) (io.ReadCloser, error) {
	r, err := s.Client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.Bucket), Key: aws.String(key)})
	if err != nil {
		var api smithy.APIError
		if errors.As(err, &api) && (api.ErrorCode() == "NoSuchKey" || api.ErrorCode() == "NotFound") {
			return nil, ErrObjectMissing
		}
		return nil, errors.New("Billing object read failed")
	}
	return r.Body, nil
}
func (s *S3Store) PutImmutable(ctx context.Context, key string, body []byte) error {
	if len(body) > billingarchive.MaxManifestBytes+32768 {
		return errors.New("publication exceeds metadata bound")
	}
	_, putErr := s.Client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(s.Bucket), Key: aws.String(key), Body: bytes.NewReader(body), IfNoneMatch: aws.String("*"), ContentLength: aws.Int64(int64(len(body))), ContentType: aws.String("application/json")})
	r, err := s.Read(ctx, key)
	if err != nil {
		return errors.New("immutable publication not confirmed")
	}
	defer r.Close()
	got, err := io.ReadAll(io.LimitReader(r, int64(len(body))+1))
	if err != nil || !bytes.Equal(got, body) {
		return errors.New("immutable publication conflicts with existing bytes")
	}
	_ = putErr // lost PUT response is resolved only by exact readback.
	return nil
}
func (s *S3Store) ListManifests(ctx context.Context, prefix string) ([]string, error) {
	var out []string
	var token *string
	for {
		r, err := s.Client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(s.Bucket), Prefix: aws.String(prefix), ContinuationToken: token})
		if err != nil {
			return nil, errors.New("Billing catalog listing failed")
		}
		for _, obj := range r.Contents {
			if obj.Key != nil && strings.HasSuffix(*obj.Key, "/manifest.json") {
				out = append(out, strings.TrimSuffix(*obj.Key, "/manifest.json"))
			}
		}
		if len(out) > 10000 {
			return nil, errors.New("catalog listing bound exceeded; select explicit archive prefix")
		}
		if !aws.ToBool(r.IsTruncated) {
			break
		}
		token = r.NextContinuationToken
		if token == nil {
			return nil, errors.New("invalid listing continuation")
		}
	}
	return out, nil
}
