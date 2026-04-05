package filestore

import (
	"context"
	"fmt"
	"io"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

const bucket = "stash"

type minioStore struct {
	client *minio.Client
}

func NewMinio(endpoint, accessKey, secretKey string, useSSL bool) (FileStore, error) {
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: useSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("minio client: %w", err)
	}
	exist, err := client.BucketExists(context.TODO(), bucket)
	if err != nil {
		return nil, fmt.Errorf("minio bucket exists: %w", err)
	}
	if !exist {
		err = client.MakeBucket(context.TODO(), bucket, minio.MakeBucketOptions{})
		if err != nil {
			return nil, fmt.Errorf("minio make bucket: %w", err)
		}
	}
	return &minioStore{client: client}, nil
}

func (s *minioStore) Put(ctx context.Context, itemID, fileName string, r io.Reader, size int64, contentType string) (string, error) {
	path := fmt.Sprintf("%s/%s", itemID, fileName)
	_, err := s.client.PutObject(ctx, bucket, path, r, size, minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return "", fmt.Errorf("minio put: %w", err)
	}
	return path, nil
}

func (s *minioStore) Get(ctx context.Context, path string) (io.ReadCloser, error) {
	obj, err := s.client.GetObject(ctx, bucket, path, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("minio get: %w", err)
	}
	return obj, nil
}

func (s *minioStore) Delete(ctx context.Context, path string) error {
	err := s.client.RemoveObject(ctx, bucket, path, minio.RemoveObjectOptions{})
	if err != nil {
		return fmt.Errorf("minio delete: %w", err)
	}
	return nil
}
