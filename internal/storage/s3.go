package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type S3 struct {
	client *s3.Client
	bucket string
	prefix string
}

func NewS3(ctx context.Context, bucket, prefix, region string) (*S3, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	client := s3.NewFromConfig(cfg)
	return &S3{client: client, bucket: bucket, prefix: prefix}, nil
}

func (s *S3) objectKey(key string) string {
	return s.prefix + key
}

func (s *S3) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.objectKey(key)),
	})
	if err != nil {
		var nsk *types.NoSuchKey
		if errors.As(err, &nsk) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, key)
		}
		return nil, fmt.Errorf("getting %s from S3: %w", key, err)
	}
	return out.Body, nil
}

func (s *S3) Put(ctx context.Context, key string, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("reading data: %w", err)
	}
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.objectKey(key)),
		Body:   bytes.NewReader(data),
	})
	if err != nil {
		return fmt.Errorf("putting %s to S3: %w", key, err)
	}
	return nil
}

func (s *S3) Exists(ctx context.Context, key string) (bool, error) {
	_, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.objectKey(key)),
	})
	if err != nil {
		var nsk *types.NotFound
		if errors.As(err, &nsk) {
			return false, nil
		}
		return false, fmt.Errorf("checking %s in S3: %w", key, err)
	}
	return true, nil
}

func (s *S3) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.objectKey(key)),
	})
	if err != nil {
		return fmt.Errorf("deleting %s from S3: %w", key, err)
	}
	return nil
}

func (s *S3) List(ctx context.Context, prefix string) ([]string, error) {
	fullPrefix := s.objectKey(prefix)
	out, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(fullPrefix),
	})
	if err != nil {
		return nil, fmt.Errorf("listing S3 objects: %w", err)
	}
	var keys []string
	for _, obj := range out.Contents {
		key := strings.TrimPrefix(*obj.Key, s.prefix)
		keys = append(keys, key)
	}
	return keys, nil
}
