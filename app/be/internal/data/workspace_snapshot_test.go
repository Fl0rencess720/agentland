package data

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/require"
)

type snapshotMetadataStub struct {
	allowed bool
	item    *workspaceSnapshotMetadata
	upserts int
	err     error
}

func (s *snapshotMetadataStub) CanSaveWorkspaceSnapshot(context.Context, string, string) (bool, error) {
	return s.allowed, s.err
}

func (s *snapshotMetadataStub) UpsertWorkspaceSnapshotMetadata(_ context.Context, _, _ string, item workspaceSnapshotMetadata) (bool, error) {
	if s.err != nil || !s.allowed {
		return false, s.err
	}
	copy := item
	s.item = &copy
	s.upserts++
	return true, nil
}

func (s *snapshotMetadataStub) GetWorkspaceSnapshotMetadata(context.Context, string) (*workspaceSnapshotMetadata, error) {
	return s.item, s.err
}

type snapshotObjectStoreStub struct {
	objects map[string][]byte
	puts    int
	putErr  error
	getErr  error
}

func (s *snapshotObjectStoreStub) Verify(context.Context) error { return nil }

func (s *snapshotObjectStoreStub) PutIfAbsent(_ context.Context, key string, data []byte, _ string) error {
	if s.putErr != nil {
		return s.putErr
	}
	if s.objects == nil {
		s.objects = make(map[string][]byte)
	}
	if _, exists := s.objects[key]; !exists {
		s.objects[key] = append([]byte(nil), data...)
		s.puts++
	}
	return nil
}

func (s *snapshotObjectStoreStub) Get(_ context.Context, key string, _ int64) ([]byte, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	data, ok := s.objects[key]
	if !ok {
		return nil, ErrSnapshotObjectNotFound
	}
	return append([]byte(nil), data...), nil
}

func TestWorkspaceSnapshotSaveUsesContentAddressedObject(t *testing.T) {
	metadata := &snapshotMetadataStub{allowed: true}
	objects := &snapshotObjectStoreStub{}
	artifacts := &workspaceSnapshotArtifacts{metadata: metadata, objects: objects, prefix: "tenant", maxBytes: 1024}
	data := []byte("compressed workspace")
	digest := sha256.Sum256(data)
	sha := hex.EncodeToString(digest[:])

	saved, err := artifacts.Save(context.Background(), "run-1", "worker-1", data, sha, "", time.Unix(1, 0))
	require.NoError(t, err)
	require.True(t, saved)
	require.Equal(t, "tenant/workspace-snapshots/sha256/"+sha[:2]+"/"+sha, metadata.item.ObjectKey)
	require.Equal(t, sha, metadata.item.SHA)
	require.Equal(t, int64(len(data)), metadata.item.SizeBytes)
	require.Nil(t, metadata.item.LegacyData)
	require.Equal(t, data, objects.objects[metadata.item.ObjectKey])

	_, err = artifacts.Save(context.Background(), "run-2", "worker-1", data, sha, "", time.Unix(2, 0))
	require.NoError(t, err)
	require.Equal(t, 1, objects.puts)
}

func TestWorkspaceSnapshotSaveValidatesSHAAndS3Errors(t *testing.T) {
	metadata := &snapshotMetadataStub{allowed: true}
	objects := &snapshotObjectStoreStub{}
	artifacts := &workspaceSnapshotArtifacts{metadata: metadata, objects: objects, maxBytes: 1024}

	_, err := artifacts.Save(context.Background(), "run", "worker", []byte("data"), "wrong", "", time.Now())
	require.ErrorContains(t, err, "SHA-256")
	require.Zero(t, metadata.upserts)

	digest := sha256.Sum256([]byte("data"))
	objects.putErr = errors.New("S3 unavailable")
	_, err = artifacts.Save(context.Background(), "run", "worker", []byte("data"), hex.EncodeToString(digest[:]), "", time.Now())
	require.ErrorContains(t, err, "S3 unavailable")
	require.Zero(t, metadata.upserts)
}

func TestWorkspaceSnapshotCaptureFailuresOnlyWriteMetadata(t *testing.T) {
	for name, testCase := range map[string]struct {
		data         []byte
		captureError string
	}{
		"capture error": {data: []byte("ignored"), captureError: "gateway failed"},
		"empty":         {},
	} {
		t.Run(name, func(t *testing.T) {
			metadata := &snapshotMetadataStub{allowed: true}
			objects := &snapshotObjectStoreStub{}
			artifacts := &workspaceSnapshotArtifacts{metadata: metadata, objects: objects, maxBytes: 1024}
			saved, err := artifacts.Save(context.Background(), "run", "worker", testCase.data, "", testCase.captureError, time.Now())
			require.NoError(t, err)
			require.True(t, saved)
			require.Empty(t, metadata.item.ObjectKey)
			require.NotEmpty(t, metadata.item.CaptureError)
			require.Zero(t, objects.puts)
		})
	}
}

func TestWorkspaceSnapshotLoadVerifiesObject(t *testing.T) {
	data := []byte("snapshot")
	digest := sha256.Sum256(data)
	sha := hex.EncodeToString(digest[:])
	key := snapshotObjectKey("agentland", sha)
	metadata := &snapshotMetadataStub{allowed: true, item: &workspaceSnapshotMetadata{ObjectKey: key, SHA: sha, SizeBytes: int64(len(data))}}
	objects := &snapshotObjectStoreStub{objects: map[string][]byte{key: data}}
	artifacts := &workspaceSnapshotArtifacts{metadata: metadata, objects: objects, maxBytes: 1024}

	snapshot, err := artifacts.Load(context.Background(), "run")
	require.NoError(t, err)
	require.Equal(t, data, snapshot.Data)
	require.Equal(t, key, snapshot.ObjectKey)

	objects.objects[key] = []byte("snopshot")
	_, err = artifacts.Load(context.Background(), "run")
	require.ErrorContains(t, err, "SHA-256")

	delete(objects.objects, key)
	_, err = artifacts.Load(context.Background(), "run")
	require.ErrorIs(t, err, ErrSnapshotObjectNotFound)
}

func TestWorkspaceSnapshotLoadSupportsLegacyRows(t *testing.T) {
	data := []byte("legacy snapshot")
	digest := sha256.Sum256(data)
	sha := hex.EncodeToString(digest[:])
	metadata := &snapshotMetadataStub{allowed: true, item: &workspaceSnapshotMetadata{SHA: sha, LegacyData: data}}
	artifacts := &workspaceSnapshotArtifacts{metadata: metadata, objects: &snapshotObjectStoreStub{}, maxBytes: 1024}

	snapshot, err := artifacts.Load(context.Background(), "run")
	require.NoError(t, err)
	require.Equal(t, data, snapshot.Data)
	require.Equal(t, int64(len(data)), snapshot.SizeBytes)
}

func TestS3SnapshotPutUsesConditionalCreateAndDeduplicatesConflict(t *testing.T) {
	const callers = 2
	data := []byte("snapshot")
	digest := sha256.Sum256(data)
	sha := hex.EncodeToString(digest[:])
	var putCount atomic.Int64
	var headCount atomic.Int64
	var objectExists atomic.Bool
	var missingConditionHeader atomic.Bool
	headBarrier := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodHead:
			count := headCount.Add(1)
			if !objectExists.Load() {
				if count == callers {
					close(headBarrier)
				}
				<-headBarrier
				writer.WriteHeader(http.StatusNotFound)
				return
			}
			writer.Header().Set("Content-Length", "8")
			writer.Header().Set("x-amz-meta-sha256", sha)
			writer.WriteHeader(http.StatusOK)
		case http.MethodPut:
			putCount.Add(1)
			if request.Header.Get("If-None-Match") != "*" {
				missingConditionHeader.Store(true)
			}
			_, _ = io.Copy(io.Discard, request.Body)
			if objectExists.Swap(true) {
				writer.WriteHeader(http.StatusPreconditionFailed)
				return
			}
			writer.WriteHeader(http.StatusOK)
		default:
			writer.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(server.Close)
	store, err := newS3SnapshotObjectStore(context.Background(), snapshotStoreConfig{
		Endpoint: server.URL, Region: "us-east-1", Bucket: "snapshots", AccessKey: "key", SecretKey: "secret", PathStyle: true, MaxSnapshotBytes: 1024,
	})
	require.NoError(t, err)

	start := make(chan struct{})
	errorsSeen := make(chan error, callers)
	var workers sync.WaitGroup
	for range callers {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			errorsSeen <- store.PutIfAbsent(context.Background(), "snapshots/key", data, sha)
		}()
	}
	close(start)
	workers.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		require.NoError(t, err)
	}
	require.GreaterOrEqual(t, headCount.Load(), int64(2))
	require.GreaterOrEqual(t, putCount.Load(), int64(1))
	require.LessOrEqual(t, putCount.Load(), int64(2))
	require.False(t, missingConditionHeader.Load())
}

func TestS3SnapshotObjectStoreIntegration(t *testing.T) {
	endpoint := os.Getenv("TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("TEST_S3_ENDPOINT is not set")
	}
	config := snapshotStoreConfig{
		Endpoint: endpoint, Region: "us-east-1", Bucket: fmt.Sprintf("agentland-snapshot-test-%d", time.Now().UnixNano()),
		AccessKey: os.Getenv("TEST_S3_ACCESS_KEY"), SecretKey: os.Getenv("TEST_S3_SECRET_KEY"), PathStyle: true, MaxSnapshotBytes: 1024,
	}
	store, err := newS3SnapshotObjectStore(context.Background(), config)
	require.NoError(t, err)
	_, err = store.client.CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String(config.Bucket)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = store.client.DeleteObject(context.Background(), &s3.DeleteObjectInput{Bucket: aws.String(config.Bucket), Key: aws.String("snapshots/object")})
		_, _ = store.client.DeleteBucket(context.Background(), &s3.DeleteBucketInput{Bucket: aws.String(config.Bucket)})
	})
	require.NoError(t, store.Verify(context.Background()))
	data := []byte("snapshot")
	digest := sha256.Sum256(data)
	sha := hex.EncodeToString(digest[:])
	require.NoError(t, store.PutIfAbsent(context.Background(), "snapshots/object", data, sha))
	require.NoError(t, store.PutIfAbsent(context.Background(), "snapshots/object", data, sha))
	loaded, err := store.Get(context.Background(), "snapshots/object", 1024)
	require.NoError(t, err)
	require.Equal(t, data, loaded)
	require.ErrorContains(t, store.PutIfAbsent(context.Background(), "snapshots/object", []byte("different"), sha), "conflicts")
	_, err = store.Get(context.Background(), "snapshots/missing", 1024)
	require.ErrorIs(t, err, ErrSnapshotObjectNotFound)
}
