package data

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Fl0rencess720/agentland/app/be/internal/biz"
	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func NewPublicationRepo() *runRepo {
	NewRunRepo()
	return sharedRunRepo
}

func (r *runRepo) CreatePublication(ctx context.Context, input *models.CreatePublicationInput) (*models.Publication, bool, error) {
	pool, err := r.ready(ctx)
	if err != nil {
		return nil, false, err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `select id from projects where id=$1 and owner_id=$2 for update`, input.ProjectID, input.OwnerID); err != nil {
		return nil, false, err
	}
	existing, err := scanPublication(tx.QueryRow(ctx, publicationSelect+` where owner_id=$1 and project_id=$2 and idempotency_key=$3`, input.OwnerID, input.ProjectID, input.IdempotencyKey))
	if err == nil {
		if existing.Context != input.Context || existing.Dockerfile != input.Dockerfile {
			return nil, false, biz.ErrIdempotencyConflict
		}
		return existing, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, err
	}
	_, err = tx.Exec(ctx, `insert into agent_runs
		(id,owner_id,project_id,idempotency_key,input_message_id,assistant_message_id,status,agent_run_id,agent_conversation_id,last_sequence,error_code,error_message,trace_parent,trace_state,created_at,updated_at,started_at)
		values($1,$2,$3,$4,$5,$6,$7,$1,$8,0,'','',$9,$10,$11,$11,$11)`,
		input.PreparationRunID, input.OwnerID, input.ProjectID, "publication:"+input.IdempotencyKey,
		input.PreparationInputMessageID, input.PreparationAssistantMessageID, models.RunStatusRunning,
		input.ID, input.TraceParent, input.TraceState, input.Now)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			_ = tx.Rollback(ctx)
			existing, lookupErr := scanPublication(pool.QueryRow(ctx, publicationSelect+` where owner_id=$1 and project_id=$2 and idempotency_key=$3`, input.OwnerID, input.ProjectID, input.IdempotencyKey))
			if lookupErr == nil {
				if existing.Context != input.Context || existing.Dockerfile != input.Dockerfile {
					return nil, false, biz.ErrIdempotencyConflict
				}
				return existing, true, nil
			}
			if pgErr.ConstraintName == "uq_agent_runs_project_running" || pgErr.ConstraintName == "uq_agent_runs_project_active" {
				return nil, false, biz.ErrActiveRun
			}
			if pgErr.ConstraintName == "uq_project_publications_active" || pgErr.ConstraintName == "uq_project_publications_active_v2" {
				return nil, false, biz.ErrActivePublication
			}
			return nil, false, biz.ErrIdempotencyConflict
		}
		return nil, false, err
	}
	_, err = tx.Exec(ctx, `insert into project_messages(id,project_id,owner_id,run_id,role,content,status,hidden,created_at,updated_at) values
		($1,$3,$4,$2,'user',$5,'completed',true,$6,$6),
		($7,$3,$4,$2,'assistant','','pending',true,$6 + interval '1 microsecond',$6 + interval '1 microsecond')`,
		input.PreparationInputMessageID, input.PreparationRunID, input.ProjectID, input.OwnerID,
		input.PreparationMessage, input.Now, input.PreparationAssistantMessageID)
	if err != nil {
		return nil, false, err
	}
	_, err = tx.Exec(ctx, `insert into project_publications
		(id,owner_id,project_id,idempotency_key,build_context,dockerfile,status,worker_id,image_ref,image_digest,build_logs,error_code,error_message,trace_parent,trace_state,preparation_run_id,created_at,updated_at)
		values($1,$2,$3,$4,$5,$6,$7,'','','','','','',$8,$9,$10,$11,$11)`,
		input.ID, input.OwnerID, input.ProjectID, input.IdempotencyKey, input.Context, input.Dockerfile, models.PublicationStatusPreparing,
		input.TraceParent, input.TraceState, input.PreparationRunID, input.Now)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && (pgErr.ConstraintName == "uq_project_publications_active" || pgErr.ConstraintName == "uq_project_publications_active_v2") {
			return nil, false, biz.ErrActivePublication
		}
		return nil, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	return &models.Publication{
		ID: input.ID, OwnerID: input.OwnerID, ProjectID: input.ProjectID, IdempotencyKey: input.IdempotencyKey,
		Context: input.Context, Dockerfile: input.Dockerfile, Status: models.PublicationStatusPreparing, PreparationRunID: input.PreparationRunID,
		TraceParent: input.TraceParent, TraceState: input.TraceState, CreatedAt: input.Now, UpdatedAt: input.Now,
	}, false, nil
}

func (r *runRepo) GetPublication(ctx context.Context, ownerID, publicationID string) (*models.Publication, error) {
	pool, err := r.ready(ctx)
	if err != nil {
		return nil, err
	}
	item, err := scanPublication(pool.QueryRow(ctx, publicationSelect+` where id=$1 and owner_id=$2`, publicationID, ownerID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, biz.ErrPublicationNotFound
	}
	return item, err
}

func (r *runRepo) FindPublicationByIdempotency(ctx context.Context, ownerID, projectID, key, buildContext, dockerfile string) (*models.Publication, error) {
	pool, err := r.ready(ctx)
	if err != nil {
		return nil, err
	}
	item, err := scanPublication(pool.QueryRow(ctx, publicationSelect+` where owner_id=$1 and project_id=$2 and idempotency_key=$3`, ownerID, projectID, key))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if item.Context != buildContext || item.Dockerfile != dockerfile {
		return nil, biz.ErrIdempotencyConflict
	}
	return item, nil
}

func (r *runRepo) FindPublicationByPreparationRun(ctx context.Context, runID string) (*models.Publication, error) {
	pool, err := r.ready(ctx)
	if err != nil {
		return nil, err
	}
	item, err := scanPublication(pool.QueryRow(ctx, publicationSelect+` where preparation_run_id=$1`, runID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return item, err
}

func (r *runRepo) HasPreparingPublication(ctx context.Context, ownerID, projectID string) (bool, error) {
	pool, err := r.ready(ctx)
	if err != nil {
		return false, err
	}
	var active bool
	err = pool.QueryRow(ctx, `select exists(select 1 from project_publications where owner_id=$1 and project_id=$2 and status=$3)`,
		ownerID, projectID, models.PublicationStatusPreparing).Scan(&active)
	return active, err
}

func (r *runRepo) PreparationSkillUsed(ctx context.Context, runID, skillName string) (bool, error) {
	pool, err := r.ready(ctx)
	if err != nil {
		return false, err
	}
	rows, err := pool.Query(ctx, `select record from run_trajectory_records where run_id=$1 order by sequence`, runID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return false, err
		}
		var record models.RunTrajectoryRecord
		if json.Unmarshal(raw, &record) != nil || record.Type != "tool.result" {
			continue
		}
		var payload struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
			Error     string `json:"error"`
		}
		if json.Unmarshal(record.Payload, &payload) != nil || payload.Name != "read_skill" || payload.Error != "" {
			continue
		}
		var arguments struct {
			Name string `json:"name"`
		}
		if json.Unmarshal([]byte(payload.Arguments), &arguments) == nil && arguments.Name == skillName {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (r *runRepo) PreparationRunProjected(ctx context.Context, runID string, sequence int64) (bool, error) {
	pool, err := r.ready(ctx)
	if err != nil {
		return false, err
	}
	var projected bool
	err = pool.QueryRow(ctx, `select status in ('completed','failed','cancelled') and last_sequence >= $2 from agent_runs where id=$1`, runID, sequence).Scan(&projected)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return projected, err
}

func (r *runRepo) CompletePublicationPreparation(ctx context.Context, input *models.CompletePublicationPreparationInput) (bool, error) {
	artifacts, err := r.snapshotArtifacts(ctx)
	if err != nil {
		return false, err
	}
	if len(input.Snapshot) == 0 || int64(len(input.Snapshot)) > artifacts.maxBytes {
		return false, fmt.Errorf("%w: snapshot must be between 1 and %d bytes", biz.ErrPublicationSnapshotInvalid, artifacts.maxBytes)
	}
	digest := sha256.Sum256(input.Snapshot)
	actualSHA := hex.EncodeToString(digest[:])
	objectKey := snapshotObjectKey(artifacts.prefix, actualSHA)
	if err = artifacts.objects.PutIfAbsent(ctx, objectKey, input.Snapshot, actualSHA); err != nil {
		return false, err
	}
	pool, err := r.ready(ctx)
	if err != nil {
		return false, err
	}
	tag, err := pool.Exec(ctx, `update project_publications set status=$3,snapshot_object_key=$4,snapshot_sha256=$5,snapshot_size_bytes=$6,updated_at=$7
		where id=$1 and preparation_run_id=$2 and status=$8 and cancel_requested_at is null`, input.ID, input.PreparationRunID, models.PublicationStatusQueued,
		objectKey, actualSHA, len(input.Snapshot), input.Now, models.PublicationStatusPreparing)
	return err == nil && tag.RowsAffected() == 1, err
}

func (r *runRepo) FailPublicationPreparation(ctx context.Context, publicationID, preparationRunID, status, code, message string, now time.Time) (bool, error) {
	pool, err := r.ready(ctx)
	if err != nil {
		return false, err
	}
	message = strings.ReplaceAll(strings.ToValidUTF8(message, "\uFFFD"), "\x00", "\uFFFD")
	if len(message) > 1<<20 {
		message = strings.ToValidUTF8(message[len(message)-(1<<20):], "\uFFFD")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `update project_publications set status=$3,error_code=$4,error_message=$5,completed_at=$6,updated_at=$6
		where id=$1 and preparation_run_id=$2 and status=$7`, publicationID, preparationRunID, status, code, message, now, models.PublicationStatusPreparing)
	if err != nil || tag.RowsAffected() == 0 {
		return false, err
	}
	if _, err = tx.Exec(ctx, `update agent_runs set status=$2,error_code=$3,error_message=$4,completed_at=$5,updated_at=$5 where id=$1 and status=$6`,
		preparationRunID, status, code, message, now, models.RunStatusRunning); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `update project_messages message set status=$2,updated_at=$3
		from agent_runs run where run.id=$1 and message.id=run.assistant_message_id and message.status='pending'`, preparationRunID, status, now); err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func (r *runRepo) MarkPublicationDispatched(ctx context.Context, publicationID string, now time.Time) (bool, error) {
	pool, err := r.ready(ctx)
	if err != nil {
		return false, err
	}
	tag, err := pool.Exec(ctx, `update project_publications set build_dispatched_at=$2,updated_at=$2 where id=$1 and status=$3 and build_dispatched_at is null`, publicationID, now, models.PublicationStatusQueued)
	return err == nil && tag.RowsAffected() == 1, err
}

func (r *runRepo) LoadPublicationSnapshot(ctx context.Context, publicationID string) (*models.WorkspaceSnapshot, error) {
	pool, err := r.ready(ctx)
	if err != nil {
		return nil, err
	}
	var objectKey, sha string
	var size int64
	if err = pool.QueryRow(ctx, `select snapshot_object_key,snapshot_sha256,snapshot_size_bytes from project_publications where id=$1`, publicationID).Scan(&objectKey, &sha, &size); err != nil {
		return nil, err
	}
	artifacts, err := r.snapshotArtifacts(ctx)
	if err != nil {
		return nil, err
	}
	if objectKey == "" || sha == "" || size <= 0 {
		return nil, errors.New("publication snapshot is missing")
	}
	data, err := artifacts.objects.Get(ctx, objectKey, artifacts.maxBytes)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(data)
	if int64(len(data)) != size || !strings.EqualFold(hex.EncodeToString(digest[:]), sha) {
		return nil, errors.New("publication snapshot does not match metadata")
	}
	return &models.WorkspaceSnapshot{Data: data, ObjectKey: objectKey, SHA: sha, SizeBytes: size}, nil
}

func (r *runRepo) ListPublications(ctx context.Context, ownerID, projectID string, limit int) ([]*models.Publication, error) {
	pool, err := r.ready(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := pool.Query(ctx, publicationSelect+` where owner_id=$1 and project_id=$2 order by created_at desc limit $3`, ownerID, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]*models.Publication, 0)
	for rows.Next() {
		item, scanErr := scanPublication(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *runRepo) RequestPublicationCancel(ctx context.Context, ownerID, publicationID string, now time.Time) (*models.Publication, error) {
	pool, err := r.ready(ctx)
	if err != nil {
		return nil, err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	item, err := scanPublication(tx.QueryRow(ctx, publicationSelect+` where id=$1 and owner_id=$2 for update`, publicationID, ownerID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, biz.ErrPublicationNotFound
	}
	if err != nil {
		return nil, err
	}
	if item.Status == models.PublicationStatusPreparing {
		item.CancelRequestedAt, item.UpdatedAt = &now, now
		_, err = tx.Exec(ctx, `update project_publications set cancel_requested_at=$2,updated_at=$2 where id=$1 and cancel_requested_at is null`, item.ID, now)
	} else if item.Status == models.PublicationStatusQueued {
		item.Status, item.CancelRequestedAt, item.CompletedAt, item.UpdatedAt = models.PublicationStatusCancelled, &now, &now, now
		_, err = tx.Exec(ctx, `update project_publications set status=$2,cancel_requested_at=$3,completed_at=$3,updated_at=$3 where id=$1`, item.ID, item.Status, now)
	} else if item.Status == models.PublicationStatusRunning && item.CancelRequestedAt == nil {
		item.CancelRequestedAt, item.UpdatedAt = &now, now
		_, err = tx.Exec(ctx, `update project_publications set cancel_requested_at=$2,updated_at=$2 where id=$1`, item.ID, now)
	}
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return item, nil
}

func (r *runRepo) FailPublicationDispatch(ctx context.Context, publicationID string, now time.Time, cause error) error {
	pool, err := r.ready(ctx)
	if err != nil {
		return err
	}
	message := ""
	if cause != nil {
		message = cause.Error()
	}
	_, err = pool.Exec(ctx, `update project_publications set status=$2,error_code='KAFKA_PUBLISH_FAILED',error_message=$3,completed_at=$4,updated_at=$4
		where id=$1 and status=$5`, publicationID, models.PublicationStatusFailed, message, now, models.PublicationStatusQueued)
	return err
}

func (r *runRepo) ClaimPublication(ctx context.Context, publicationID, workerID string, now time.Time) (*models.Publication, error) {
	pool, err := r.ready(ctx)
	if err != nil {
		return nil, err
	}
	leases, err := r.leases()
	if err != nil {
		return nil, err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	leaseAcquired, committed := false, false
	var claimedID string
	defer func() {
		if leaseAcquired && !committed {
			releaseLeaseBestEffort(ctx, leases, publicationLeaseKind, claimedID, workerID)
		}
	}()
	item, err := scanPublication(tx.QueryRow(ctx, `update project_publications p set status=$2,worker_id=$3,started_at=$4,heartbeat_at=$4,updated_at=$4
	where p.id=$5 and p.status=$1
	returning p.id,p.owner_id,p.project_id,p.idempotency_key,p.build_context,p.dockerfile,p.status,p.worker_id,p.image_ref,p.image_digest,p.deployment_url,p.deployment_hostname,p.deployment_name,p.build_logs,p.error_code,p.error_message,p.trace_parent,p.trace_state,p.preparation_run_id,p.snapshot_object_key,p.snapshot_sha256,p.snapshot_size_bytes,p.build_dispatched_at,p.created_at,p.updated_at,p.started_at,p.heartbeat_at,p.completed_at,p.cancel_requested_at`,
		models.PublicationStatusQueued, models.PublicationStatusRunning, workerID, now, publicationID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	claimedID = item.ID
	leaseAcquired, err = leases.Acquire(ctx, publicationLeaseKind, item.ID, workerID, workerLeaseTTL(publicationLeaseKind))
	if err != nil {
		return nil, err
	}
	if !leaseAcquired {
		return nil, biz.ErrWorkerLeaseBusy
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	committed = true
	return item, nil
}

func (r *runRepo) HeartbeatPublication(ctx context.Context, publicationID, workerID string, now time.Time) (bool, error) {
	leases, err := r.leases()
	if err != nil {
		return false, err
	}
	return leases.Renew(ctx, publicationLeaseKind, publicationID, workerID, workerLeaseTTL(publicationLeaseKind))
}

func (r *runRepo) FinishPublication(ctx context.Context, input *models.FinishPublicationInput) (bool, error) {
	pool, err := r.ready(ctx)
	if err != nil {
		return false, err
	}
	leases, err := r.leases()
	if err != nil {
		return false, err
	}
	owned, leaseErr := leases.Renew(ctx, publicationLeaseKind, input.ID, input.WorkerID, workerLeaseTTL(publicationLeaseKind))
	if leaseErr == nil && !owned {
		return false, nil
	}
	tag, err := pool.Exec(ctx, `update project_publications set status=$3,image_ref=$4,image_digest=$5,deployment_url=$6,deployment_hostname=$7,deployment_name=$8,build_logs=$9,error_code=$10,error_message=$11,completed_at=$12,heartbeat_at=$12,updated_at=$12
		where id=$1 and worker_id=$2 and status=$13`, input.ID, input.WorkerID, input.Status, input.ImageRef, input.Digest,
		input.DeploymentURL, input.DeploymentHostname, input.DeploymentName, input.Logs, input.ErrorCode, input.ErrorMessage, input.Now, models.PublicationStatusRunning)
	if err != nil || tag.RowsAffected() == 0 {
		return false, err
	}
	releaseLeaseBestEffort(ctx, leases, publicationLeaseKind, input.ID, input.WorkerID)
	return true, nil
}

func (r *runRepo) FailOrphanedPublications(ctx context.Context, heartbeatBefore, now time.Time) (int64, error) {
	pool, err := r.ready(ctx)
	if err != nil {
		return 0, err
	}
	leases, err := r.leases()
	if err != nil {
		return 0, err
	}
	recoveryOwner := recoveryLeaseOwner(publicationLeaseKind)
	var recovered int64
	for cursor := ""; ; {
		rows, queryErr := pool.Query(ctx, `select id,worker_id from project_publications
			where status=$1 and coalesce(heartbeat_at,started_at,updated_at)<$2 and id>$3
			order by id limit 100`, models.PublicationStatusRunning, heartbeatBefore, cursor)
		if queryErr != nil {
			return recovered, queryErr
		}
		candidates := make([]leaseCandidate, 0, 100)
		for rows.Next() {
			var candidate leaseCandidate
			if err = rows.Scan(&candidate.ID, &candidate.OwnerID); err != nil {
				rows.Close()
				return recovered, err
			}
			candidates = append(candidates, candidate)
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return recovered, err
		}
		rows.Close()
		if len(candidates) == 0 {
			return recovered, nil
		}
		cursor = candidates[len(candidates)-1].ID
		claimed, claimErr := leases.AcquireRecovery(ctx, publicationLeaseKind, candidates, recoveryOwner, workerLeaseTTL(publicationLeaseKind))
		if claimErr != nil {
			return recovered, claimErr
		}
		for _, candidate := range candidates {
			if !claimed[candidate.ID] {
				continue
			}
			tag, updateErr := pool.Exec(ctx, `update project_publications set status=$1,error_code='WORKER_LOST',error_message='publication worker lease expired; build was not replayed',completed_at=$2,updated_at=$2
			where id=$3 and worker_id=$4 and status=$5`, models.PublicationStatusFailed, now, candidate.ID, candidate.OwnerID, models.PublicationStatusRunning)
			if updateErr != nil {
				return recovered, updateErr
			}
			if tag.RowsAffected() == 0 {
				releaseLeaseBestEffort(ctx, leases, publicationLeaseKind, candidate.ID, recoveryOwner)
				continue
			}
			recovered++
		}
		if len(candidates) < 100 {
			return recovered, nil
		}
	}
}

func (r *runRepo) IsPublicationCancelRequested(ctx context.Context, publicationID string) (bool, error) {
	pool, err := r.ready(ctx)
	if err != nil {
		return false, err
	}
	var requested bool
	err = pool.QueryRow(ctx, `select cancel_requested_at is not null from project_publications where id=$1`, publicationID).Scan(&requested)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, biz.ErrPublicationNotFound
	}
	return requested, err
}

const publicationSelect = `select id,owner_id,project_id,idempotency_key,build_context,dockerfile,status,worker_id,image_ref,image_digest,deployment_url,deployment_hostname,deployment_name,build_logs,error_code,error_message,trace_parent,trace_state,coalesce(preparation_run_id,''),snapshot_object_key,snapshot_sha256,snapshot_size_bytes,build_dispatched_at,created_at,updated_at,started_at,heartbeat_at,completed_at,cancel_requested_at from project_publications`

func scanPublication(scanner rowScanner) (*models.Publication, error) {
	item := &models.Publication{}
	err := scanner.Scan(
		&item.ID, &item.OwnerID, &item.ProjectID, &item.IdempotencyKey, &item.Context, &item.Dockerfile,
		&item.Status, &item.WorkerID, &item.ImageRef, &item.Digest, &item.DeploymentURL, &item.DeploymentHostname, &item.DeploymentName,
		&item.Logs, &item.ErrorCode, &item.ErrorMessage,
		&item.TraceParent, &item.TraceState, &item.PreparationRunID, &item.SnapshotObjectKey, &item.SnapshotSHA, &item.SnapshotSize, &item.BuildDispatchedAt,
		&item.CreatedAt, &item.UpdatedAt, &item.StartedAt, &item.HeartbeatAt, &item.CompletedAt, &item.CancelRequestedAt,
	)
	return item, err
}
