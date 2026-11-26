package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/LaplacianAI/openarity/apps/brain/internal/objects"
)

const (
	// maxObjectSize bounds Get, which reads an object whole; Open does not.
	maxObjectSize = 32 << 20
	// partSize is what PutStream holds at once: one part of a multipart
	// upload. Over S3's 5 MiB least, and few parts for the largest file.
	partSize = 8 << 20
	// abortTimeout bounds cleaning up an upload that failed.
	abortTimeout = 30 * time.Second
)

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
		// Checksums only where an operation needs one: not every S3 clone
		// takes the SDK's default CRC32 on a part, and GCM already
		// authenticates every chunk.
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
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

	body, err := io.ReadAll(io.LimitReader(out.Body, maxObjectSize+1))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", key, err)
	}
	if len(body) > maxObjectSize {
		return nil, fmt.Errorf("%w: %s is over %d bytes; Open reads it", objects.ErrTooLarge, key, maxObjectSize)
	}
	return body, nil
}

func (s *store) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	out, err := s.client.GetObject(ctx, &awss3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, s.wrap("reading", key, err)
	}
	return out.Body, nil
}

// PutStream holds one part at a time, and a small object only its own
// bytes. An object that fits one part is one PutObject; a larger one is a
// multipart upload, aborted if anything fails, so a failed write leaves no
// object. An upload the brain dies in the middle of is the bucket's lifecycle
// rule's to abort.
func (s *store) PutStream(ctx context.Context, key string, r io.Reader) error {
	r = objects.Strict(r)
	buf, err := io.ReadAll(io.LimitReader(r, partSize))
	if err != nil {
		return fmt.Errorf("reading %s: %w", key, err)
	}
	if len(buf) < partSize {
		return s.Put(ctx, key, buf)
	}

	up, err := s.client.CreateMultipartUpload(ctx, &awss3.CreateMultipartUploadInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return s.wrap("writing", key, err)
	}
	if err := s.upload(ctx, key, up.UploadId, r, buf[:partSize]); err != nil {
		aborting, cancel := context.WithTimeout(context.WithoutCancel(ctx), abortTimeout)
		defer cancel()
		if _, abortErr := s.client.AbortMultipartUpload(aborting, &awss3.AbortMultipartUploadInput{
			Bucket: aws.String(s.bucket), Key: aws.String(key), UploadId: up.UploadId,
		}); abortErr != nil {
			// The parts stay until the bucket's lifecycle rule aborts them.
			// Not wrap: an upload already gone is not the object missing.
			return errors.Join(err, fmt.Errorf("aborting the upload of %s: %w", key, abortErr))
		}
		return err
	}
	return nil
}

// upload sends buf, which holds a full first part, and every part after it,
// then completes the upload.
func (s *store) upload(ctx context.Context, key string, id *string, r io.Reader, buf []byte) error {
	var parts []types.CompletedPart
	n, last := len(buf), false
	for num := int32(1); ; num++ {
		got, err := s.client.UploadPart(ctx, &awss3.UploadPartInput{
			Bucket: aws.String(s.bucket), Key: aws.String(key), UploadId: id,
			PartNumber: aws.Int32(num), Body: bytes.NewReader(buf[:n]),
		})
		if err != nil {
			return s.wrap("writing", key, err)
		}
		parts = append(parts, types.CompletedPart{
			ETag: got.ETag, PartNumber: aws.Int32(num),
		})
		if last {
			break
		}
		n, err = io.ReadFull(r, buf)
		if errors.Is(err, io.EOF) {
			break
		}
		if errors.Is(err, io.ErrUnexpectedEOF) {
			last = true
		} else if err != nil {
			return fmt.Errorf("reading %s: %w", key, err)
		}
	}
	_, err := s.client.CompleteMultipartUpload(ctx, &awss3.CompleteMultipartUploadInput{
		Bucket: aws.String(s.bucket), Key: aws.String(key), UploadId: id,
		MultipartUpload: &types.CompletedMultipartUpload{Parts: parts},
	})
	if err != nil {
		return s.wrap("writing", key, err)
	}
	return nil
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
