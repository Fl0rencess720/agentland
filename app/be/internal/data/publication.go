package data

import (
	"context"
	"errors"
	"time"

	"github.com/Fl0rencess720/agentland/app/be/internal/biz"
	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func NewPublicationRepo() biz.PublicationWorkerRepo {
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
	_, err = tx.Exec(ctx, `insert into project_publications
		(id,owner_id,project_id,idempotency_key,build_context,dockerfile,status,worker_id,image_ref,image_digest,build_logs,error_code,error_message,trace_parent,trace_state,created_at,updated_at)
		values($1,$2,$3,$4,$5,$6,$7,'','','','','','',$8,$9,$10,$10)`,
		input.ID, input.OwnerID, input.ProjectID, input.IdempotencyKey, input.Context, input.Dockerfile, models.PublicationStatusQueued,
		input.TraceParent, input.TraceState, input.Now)
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
			if pgErr.ConstraintName == "uq_project_publications_active" {
				return nil, false, biz.ErrActivePublication
			}
			return nil, false, biz.ErrIdempotencyConflict
		}
		return nil, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	return &models.Publication{
		ID: input.ID, OwnerID: input.OwnerID, ProjectID: input.ProjectID, IdempotencyKey: input.IdempotencyKey,
		Context: input.Context, Dockerfile: input.Dockerfile, Status: models.PublicationStatusQueued,
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
	if item.Status == models.PublicationStatusQueued {
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

func (r *runRepo) ClaimNextPublication(ctx context.Context, workerID string, now time.Time) (*models.Publication, error) {
	pool, err := r.ready(ctx)
	if err != nil {
		return nil, err
	}
	item, err := scanPublication(pool.QueryRow(ctx, `with candidate as (
		select id from project_publications where status=$1 order by created_at for update skip locked limit 1
	) update project_publications p set status=$2,worker_id=$3,started_at=$4,heartbeat_at=$4,updated_at=$4
	from candidate where p.id=candidate.id
	returning p.id,p.owner_id,p.project_id,p.idempotency_key,p.build_context,p.dockerfile,p.status,p.worker_id,p.image_ref,p.image_digest,p.build_logs,p.error_code,p.error_message,p.trace_parent,p.trace_state,p.created_at,p.updated_at,p.started_at,p.heartbeat_at,p.completed_at,p.cancel_requested_at`,
		models.PublicationStatusQueued, models.PublicationStatusRunning, workerID, now))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return item, err
}

func (r *runRepo) HeartbeatPublication(ctx context.Context, publicationID, workerID string, now time.Time) (bool, error) {
	pool, err := r.ready(ctx)
	if err != nil {
		return false, err
	}
	tag, err := pool.Exec(ctx, `update project_publications set heartbeat_at=$3,updated_at=$3 where id=$1 and worker_id=$2 and status=$4`, publicationID, workerID, now, models.PublicationStatusRunning)
	return err == nil && tag.RowsAffected() == 1, err
}

func (r *runRepo) FinishPublication(ctx context.Context, input *models.FinishPublicationInput) (bool, error) {
	pool, err := r.ready(ctx)
	if err != nil {
		return false, err
	}
	tag, err := pool.Exec(ctx, `update project_publications set status=$3,image_ref=$4,image_digest=$5,build_logs=$6,error_code=$7,error_message=$8,completed_at=$9,heartbeat_at=$9,updated_at=$9
		where id=$1 and worker_id=$2 and status=$10`, input.ID, input.WorkerID, input.Status, input.ImageRef, input.Digest, input.Logs, input.ErrorCode, input.ErrorMessage, input.Now, models.PublicationStatusRunning)
	return err == nil && tag.RowsAffected() == 1, err
}

func (r *runRepo) FailOrphanedPublications(ctx context.Context, heartbeatBefore, now time.Time) (int64, error) {
	pool, err := r.ready(ctx)
	if err != nil {
		return 0, err
	}
	tag, err := pool.Exec(ctx, `update project_publications set status=$1,error_code='WORKER_LOST',error_message='publication worker heartbeat expired; build was not replayed',completed_at=$3,updated_at=$3
		where status=$2 and heartbeat_at<$4`, models.PublicationStatusFailed, models.PublicationStatusRunning, now, heartbeatBefore)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
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

const publicationSelect = `select id,owner_id,project_id,idempotency_key,build_context,dockerfile,status,worker_id,image_ref,image_digest,build_logs,error_code,error_message,trace_parent,trace_state,created_at,updated_at,started_at,heartbeat_at,completed_at,cancel_requested_at from project_publications`

func scanPublication(scanner rowScanner) (*models.Publication, error) {
	item := &models.Publication{}
	err := scanner.Scan(
		&item.ID, &item.OwnerID, &item.ProjectID, &item.IdempotencyKey, &item.Context, &item.Dockerfile,
		&item.Status, &item.WorkerID, &item.ImageRef, &item.Digest, &item.Logs, &item.ErrorCode, &item.ErrorMessage,
		&item.TraceParent, &item.TraceState, &item.CreatedAt, &item.UpdatedAt, &item.StartedAt, &item.HeartbeatAt, &item.CompletedAt, &item.CancelRequestedAt,
	)
	return item, err
}
