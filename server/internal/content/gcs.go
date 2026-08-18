package content

import (
	"context"
	"fmt"
	"io"

	"cloud.google.com/go/storage"
)

const maxContentObjectBytes = 8 << 20

type GCSObjectSource struct {
	client *storage.Client
}

func NewGCSObjectSource(ctx context.Context) (*GCSObjectSource, error) {
	client, err := storage.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("content: GCS client 생성 실패: %w", err)
	}
	return &GCSObjectSource{client: client}, nil
}

func (s *GCSObjectSource) Close() error { return s.client.Close() }

func (s *GCSObjectSource) Read(ctx context.Context, bucket, object string) ([]byte, error) {
	reader, err := s.client.Bucket(bucket).Object(object).NewReader(ctx)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(reader, maxContentObjectBytes+1))
	closeErr := reader.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if len(data) > maxContentObjectBytes {
		return nil, fmt.Errorf("content object가 %d bytes를 넘는다", maxContentObjectBytes)
	}
	return data, nil
}
