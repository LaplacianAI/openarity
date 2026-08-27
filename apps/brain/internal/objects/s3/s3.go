package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/LaplacianAI/openarity/apps/brain/internal/objects"
)

const maxObjectSize = 32 << 20

type Config struct {
	Endpoint  string
	Region    string
	Bucket    string
	AccessKey string
	SecretKey string
}

type store struct {
	client *awss3.Client
	bucket string
}

var (
	_ objects.Store  = (*store)(nil)
	_ objects.Writer = (*store)(nil)
)

func New(cfg Config) (objects.Store, error) {
	switch {
	case cfg.Endpoint == "":
		return nil, errors.New("object store endpoint is empty")
	case cfg.Bucket == "":
		return nil, errors.New("object store bucket is empty")
	}

	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}

	var creds aws.CredentialsProvider
	if cfg.AccessKey != "" {
		creds = credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, "")
	}

	client := awss3.New(awss3.Options{
		BaseEndpoint: aws.String(cfg.Endpoint),
		Region:       region,
		Credentials:  creds,
		UsePathStyle: true,
	})

	return &store{client: client, bucket: cfg.Bucket}, nil
}

func (s *store) Get(ctx context.Context, key string) ([]byte, error) {
	out, err := s.client.GetObject(ctx, &awss3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, s.wrap("reading", key, err)
	}
	defer func() { _ = out.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(out.Body, maxObjectSize))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", key, err)
	}
	return body, nil
}

func (s *store) Put(ctx context.Context, key string, body []byte) error {
	_, err := s.client.PutObject(ctx, &awss3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(body),
	})
	if err != nil {
		return s.wrap("writing", key, err)
	}
	return nil
}

func (s *store) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &awss3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return s.wrap("deleting", key, err)
	}
	return nil
}

func (s *store) wrap(verb, key string, err error) error {
	var noSuchKey *types.NoSuchKey
	if errors.As(err, &noSuchKey) {
		return fmt.Errorf("%w: %s", objects.ErrNotFound, key)
	}

	var resp interface{ HTTPStatusCode() int }
	if errors.As(err, &resp) && resp.HTTPStatusCode() == http.StatusNotFound {
		return fmt.Errorf("%w: %s", objects.ErrNotFound, key)
	}

	var api smithy.APIError
	if errors.As(err, &api) && api.ErrorCode() == "NoSuchKey" {
		return fmt.Errorf("%w: %s", objects.ErrNotFound, key)
	}

	return fmt.Errorf("%s %s: %w", verb, key, err)
}
