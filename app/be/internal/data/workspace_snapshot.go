package data

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/jackc/pgx/v5"
	"github.com/spf13/viper"
)

var ErrSnapshotObjectNotFound = errors.New("workspace snapshot object not found")

type SnapshotObjectStore interface {
	Verify(context.Context) error
	PutIfAbsent(context.Context, string, []byte, string) error
	Get(context.Context, string, int64) ([]byte, error)
}

type snapshotStoreConfig struct {
	Endpoint, Region, Bucket, AccessKey, SecretKey, KeyPrefix string
	PathStyle                                                 bool
	MaxSnapshotBytes                                          int64
}

func snapshotConfigFromViper() snapshotStoreConfig {
	return snapshotStoreConfig{
		Endpoint:         strings.TrimSpace(viper.GetString("storage.s3.endpoint")),
		Region:           strings.TrimSpace(viper.GetString("storage.s3.region")),
		Bucket:           strings.TrimSpace(viper.GetString("storage.s3.bucket")),
		AccessKey:        strings.TrimSpace(viper.GetString("storage.s3.access_key")),
		SecretKey:        strings.TrimSpace(viper.GetString("storage.s3.secret_key")),
		PathStyle:        viper.GetBool("storage.s3.path_style"),
		KeyPrefix:        strings.Trim(viper.GetString("storage.s3.key_prefix"), "/ "),
		MaxSnapshotBytes: viper.GetInt64("storage.s3.max_snapshot_bytes"),
	}
}

func (c snapshotStoreConfig) validate() error {
	if c.Bucket == "" {
		return errors.New("storage.s3.bucket is required")
	}
	if c.Region == "" {
		return errors.New("storage.s3.region is required")
	}
	if (c.AccessKey == "") != (c.SecretKey == "") {
		return errors.New("storage.s3.access_key and storage.s3.secret_key must be set together")
	}
	if c.MaxSnapshotBytes <= 0 {
		return errors.New("storage.s3.max_snapshot_bytes must be positive")
	}
	if c.Endpoint != "" {
		parsed, err := url.Parse(c.Endpoint)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return errors.New("storage.s3.endpoint must be an absolute HTTP(S) URL")
		}
	}
	for _, segment := range strings.Split(c.KeyPrefix, "/") {
		if segment == "." || segment == ".." {
			return errors.New("storage.s3.key_prefix contains an invalid path segment")
		}
	}
	return nil
}

type s3SnapshotObjectStore struct {
	client *s3.Client
	bucket string
}

func newS3SnapshotObjectStore(ctx context.Context, config snapshotStoreConfig) (*s3SnapshotObjectStore, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	options := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(config.Region)}
	if config.AccessKey != "" {
		options = append(options, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(config.AccessKey, config.SecretKey, "")))
	}
	awsConfig, err := awsconfig.LoadDefaultConfig(ctx, options...)
	if err != nil {
		return nil, fmt.Errorf("load S3 configuration: %w", err)
	}
	client := s3.NewFromConfig(awsConfig, func(options *s3.Options) {
		options.UsePathStyle = config.PathStyle
		if config.Endpoint != "" {
			options.BaseEndpoint = aws.String(strings.TrimRight(config.Endpoint, "/"))
		}
	})
	return &s3SnapshotObjectStore{client: client, bucket: config.Bucket}, nil
}

func (s *s3SnapshotObjectStore) Verify(ctx context.Context) error {
	_, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(s.bucket)})
	if err != nil {
		return fmt.Errorf("access S3 bucket %q: %w", s.bucket, err)
	}
	return nil
}

func (s *s3SnapshotObjectStore) PutIfAbsent(ctx context.Context, key string, data []byte, sha string) error {
	head, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err == nil {
		return verifyStoredSnapshot(key, aws.ToInt64(head.ContentLength), head.Metadata, int64(len(data)), sha)
	}
	if !s3NotFound(err) {
		return fmt.Errorf("inspect S3 object %q: %w", key, err)
	}
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(key),
		Body:          bytes.NewReader(data),
		ContentLength: aws.Int64(int64(len(data))),
		ContentType:   aws.String("application/octet-stream"),
		Metadata:      map[string]string{"sha256": sha},
		IfNoneMatch:   aws.String("*"),
	})
	if s3ConditionConflict(err) {
		head, headErr := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
		if headErr != nil {
			return fmt.Errorf("verify concurrently uploaded S3 object %q: %w", key, headErr)
		}
		return verifyStoredSnapshot(key, aws.ToInt64(head.ContentLength), head.Metadata, int64(len(data)), sha)
	}
	if err != nil {
		return fmt.Errorf("upload S3 object %q: %w", key, err)
	}
	return nil
}

func verifyStoredSnapshot(key string, storedSize int64, metadata map[string]string, expectedSize int64, sha string) error {
	if storedSize != expectedSize || !strings.EqualFold(metadata["sha256"], sha) {
		return fmt.Errorf("S3 object %q conflicts with snapshot content", key)
	}
	return nil
}

func (s *s3SnapshotObjectStore) Get(ctx context.Context, key string, maxBytes int64) ([]byte, error) {
	output, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		if s3NotFound(err) {
			return nil, fmt.Errorf("%w: %s", ErrSnapshotObjectNotFound, key)
		}
		return nil, fmt.Errorf("download S3 object %q: %w", key, err)
	}
	defer output.Body.Close()
	if aws.ToInt64(output.ContentLength) > maxBytes {
		return nil, fmt.Errorf("workspace snapshot exceeds %d bytes", maxBytes)
	}
	data, err := io.ReadAll(io.LimitReader(output.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read S3 object %q: %w", key, err)
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("workspace snapshot exceeds %d bytes", maxBytes)
	}
	return data, nil
}

func s3NotFound(err error) bool {
	var responseError *smithyhttp.ResponseError
	if errors.As(err, &responseError) && responseError.HTTPStatusCode() == 404 {
		return true
	}
	var apiError smithy.APIError
	if !errors.As(err, &apiError) {
		return false
	}
	return apiError.ErrorCode() == "NotFound" || apiError.ErrorCode() == "NoSuchKey"
}

func s3ConditionConflict(err error) bool {
	if err == nil {
		return false
	}
	var responseError *smithyhttp.ResponseError
	if errors.As(err, &responseError) {
		return responseError.HTTPStatusCode() == 409 || responseError.HTTPStatusCode() == 412
	}
	var apiError smithy.APIError
	if !errors.As(err, &apiError) {
		return false
	}
	return apiError.ErrorCode() == "ConditionalRequestConflict" || apiError.ErrorCode() == "PreconditionFailed"
}

type workspaceSnapshotMetadata struct {
	RunID, ObjectKey, SHA, CaptureError string
	SizeBytes                           int64
	LegacyData                          []byte
	CreatedAt                           time.Time
}

type workspaceSnapshotMetadataStore interface {
	CanSaveWorkspaceSnapshot(context.Context, string, string) (bool, error)
	UpsertWorkspaceSnapshotMetadata(context.Context, string, string, workspaceSnapshotMetadata) (bool, error)
	GetWorkspaceSnapshotMetadata(context.Context, string) (*workspaceSnapshotMetadata, error)
}

type workspaceSnapshotArtifacts struct {
	metadata workspaceSnapshotMetadataStore
	objects  SnapshotObjectStore
	prefix   string
	maxBytes int64
}

func (a *workspaceSnapshotArtifacts) Save(ctx context.Context, runID, workerID string, data []byte, suppliedSHA, captureError string, now time.Time) (bool, error) {
	metadata := workspaceSnapshotMetadata{RunID: runID, CaptureError: captureError, CreatedAt: now}
	if captureError != "" || len(data) == 0 {
		if metadata.CaptureError == "" {
			metadata.CaptureError = "workspace snapshot is empty"
		}
		return a.metadata.UpsertWorkspaceSnapshotMetadata(ctx, runID, workerID, metadata)
	}
	if int64(len(data)) > a.maxBytes {
		return false, fmt.Errorf("workspace snapshot exceeds %d bytes", a.maxBytes)
	}
	digest := sha256.Sum256(data)
	actualSHA := hex.EncodeToString(digest[:])
	if !strings.EqualFold(strings.TrimSpace(suppliedSHA), actualSHA) {
		return false, errors.New("workspace snapshot SHA-256 does not match its content")
	}
	allowed, err := a.metadata.CanSaveWorkspaceSnapshot(ctx, runID, workerID)
	if err != nil || !allowed {
		return false, err
	}
	metadata.ObjectKey = snapshotObjectKey(a.prefix, actualSHA)
	metadata.SHA = actualSHA
	metadata.SizeBytes = int64(len(data))
	if err = a.objects.PutIfAbsent(ctx, metadata.ObjectKey, data, actualSHA); err != nil {
		return false, err
	}
	return a.metadata.UpsertWorkspaceSnapshotMetadata(ctx, runID, workerID, metadata)
}

func (a *workspaceSnapshotArtifacts) Load(ctx context.Context, runID string) (*models.WorkspaceSnapshot, error) {
	metadata, err := a.metadata.GetWorkspaceSnapshotMetadata(ctx, runID)
	if err != nil || metadata == nil {
		return nil, err
	}
	snapshot := &models.WorkspaceSnapshot{
		ObjectKey: metadata.ObjectKey, SHA: metadata.SHA, SizeBytes: metadata.SizeBytes,
		Error: metadata.CaptureError, CreatedAt: metadata.CreatedAt,
	}
	if metadata.CaptureError != "" {
		return snapshot, nil
	}
	if metadata.ObjectKey != "" {
		snapshot.Data, err = a.objects.Get(ctx, metadata.ObjectKey, a.maxBytes)
	} else {
		snapshot.Data = append([]byte(nil), metadata.LegacyData...)
		if int64(len(snapshot.Data)) > a.maxBytes {
			return nil, fmt.Errorf("workspace snapshot exceeds %d bytes", a.maxBytes)
		}
	}
	if err != nil {
		return nil, err
	}
	if len(snapshot.Data) == 0 {
		return nil, errors.New("workspace snapshot content is missing")
	}
	if metadata.SizeBytes > 0 && metadata.SizeBytes != int64(len(snapshot.Data)) {
		return nil, errors.New("workspace snapshot size does not match metadata")
	}
	digest := sha256.Sum256(snapshot.Data)
	if !strings.EqualFold(hex.EncodeToString(digest[:]), metadata.SHA) {
		return nil, errors.New("workspace snapshot SHA-256 does not match metadata")
	}
	snapshot.SizeBytes = int64(len(snapshot.Data))
	return snapshot, nil
}

func snapshotObjectKey(prefix, sha string) string {
	return path.Join(prefix, "workspace-snapshots", "sha256", sha[:2], sha)
}

func (r *runRepo) snapshotArtifacts(ctx context.Context) (*workspaceSnapshotArtifacts, error) {
	r.snapshotOnce.Do(func() {
		config := snapshotConfigFromViper()
		if r.snapshotStore == nil {
			r.snapshotStore, r.snapshotErr = newS3SnapshotObjectStore(ctx, config)
		}
		if r.snapshotErr == nil {
			if config.MaxSnapshotBytes <= 0 {
				r.snapshotErr = errors.New("storage.s3.max_snapshot_bytes must be positive")
			} else {
				r.snapshotArtifactsStore = &workspaceSnapshotArtifacts{
					metadata: r, objects: r.snapshotStore, prefix: config.KeyPrefix, maxBytes: config.MaxSnapshotBytes,
				}
			}
		}
	})
	return r.snapshotArtifactsStore, r.snapshotErr
}

func (r *runRepo) VerifySnapshotStore(ctx context.Context) error {
	artifacts, err := r.snapshotArtifacts(ctx)
	if err != nil {
		return err
	}
	return artifacts.objects.Verify(ctx)
}

func (r *runRepo) SaveWorkspaceSnapshot(ctx context.Context, runID, workerID string, data []byte, sha, captureError string, now time.Time) (bool, error) {
	artifacts, err := r.snapshotArtifacts(ctx)
	if err != nil {
		return false, err
	}
	return artifacts.Save(ctx, runID, workerID, data, sha, captureError, now)
}

func (r *runRepo) LoadWorkspaceSnapshot(ctx context.Context, runID string) (*models.WorkspaceSnapshot, error) {
	artifacts, err := r.snapshotArtifacts(ctx)
	if err != nil {
		return nil, err
	}
	return artifacts.Load(ctx, runID)
}

func (r *runRepo) CanSaveWorkspaceSnapshot(ctx context.Context, runID, workerID string) (bool, error) {
	pool, err := r.ready(ctx)
	if err != nil {
		return false, err
	}
	var allowed bool
	err = pool.QueryRow(ctx, `select exists(select 1 from agent_runs where id=$1 and worker_id=$2 and status=$3)`, runID, workerID, models.RunStatusRunning).Scan(&allowed)
	return allowed, err
}

func (r *runRepo) UpsertWorkspaceSnapshotMetadata(ctx context.Context, runID, workerID string, metadata workspaceSnapshotMetadata) (bool, error) {
	pool, err := r.ready(ctx)
	if err != nil {
		return false, err
	}
	tag, err := pool.Exec(ctx, `insert into run_workspace_snapshots(run_id,object_key,sha256,size_bytes,capture_error,created_at)
		select $1,$4,$5,$6,$7,$8 where exists(select 1 from agent_runs where id=$1 and worker_id=$2 and status=$3)
		on conflict(run_id) do update set content=null,object_key=excluded.object_key,sha256=excluded.sha256,size_bytes=excluded.size_bytes,capture_error=excluded.capture_error,created_at=excluded.created_at`,
		runID, workerID, models.RunStatusRunning, metadata.ObjectKey, metadata.SHA, metadata.SizeBytes, metadata.CaptureError, metadata.CreatedAt)
	return err == nil && tag.RowsAffected() == 1, err
}

func (r *runRepo) GetWorkspaceSnapshotMetadata(ctx context.Context, runID string) (*workspaceSnapshotMetadata, error) {
	pool, err := r.ready(ctx)
	if err != nil {
		return nil, err
	}
	metadata := &workspaceSnapshotMetadata{RunID: runID}
	err = pool.QueryRow(ctx, `select object_key,sha256,size_bytes,capture_error,content,created_at from run_workspace_snapshots where run_id=$1`, runID).
		Scan(&metadata.ObjectKey, &metadata.SHA, &metadata.SizeBytes, &metadata.CaptureError, &metadata.LegacyData, &metadata.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return metadata, err
}
