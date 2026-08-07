package data

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/Fl0rencess720/agentland/app/be/internal/biz"
	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/viper"
)

var (
	sharedRunRepoOnce sync.Once
	sharedRunRepo     *runRepo
)

type runRepo struct {
	poolOnce    sync.Once
	pool        *pgxpool.Pool
	poolErr     error
	schemaMu    sync.Mutex
	schemaReady bool
	schemaErr   error
}

func NewRunRepo() biz.RunWorkerRepo {
	sharedRunRepoOnce.Do(func() { sharedRunRepo = &runRepo{} })
	return sharedRunRepo
}

func (r *runRepo) CreateRun(ctx context.Context, input *models.CreateRunInput) (*models.Run, bool, error) {
	pool, err := r.ready(ctx)
	if err != nil {
		return nil, false, err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx)
	existing, err := scanRun(tx.QueryRow(ctx, runSelect+` where owner_id=$1 and project_id=$2 and idempotency_key=$3`, input.OwnerID, input.ProjectID, input.IdempotencyKey))
	if err == nil {
		var message string
		if err = tx.QueryRow(ctx, `select content from project_messages where id=$1`, existing.InputMessageID).Scan(&message); err != nil {
			return nil, false, err
		}
		if message != input.Message {
			return nil, false, biz.ErrIdempotencyConflict
		}
		return existing, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, err
	}
	_, err = tx.Exec(ctx, `insert into agent_runs
(id,owner_id,project_id,idempotency_key,input_message_id,assistant_message_id,status,agent_run_id,last_sequence,worker_id,error_code,error_message,trace_parent,trace_state,created_at,updated_at,started_at,heartbeat_at,completed_at,cancel_requested_at)
values($1,$2,$3,$4,$5,$6,$7,'',0,'','','',$8,$9,$10,$10,null,null,null,null)`, input.ID, input.OwnerID, input.ProjectID, input.IdempotencyKey, input.InputMessageID, input.AssistantMessageID, models.RunStatusQueued, input.TraceParent, input.TraceState, input.Now)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			_ = tx.Rollback(ctx)
			existing, message, lookupErr := r.getByIdempotency(ctx, pool, input.OwnerID, input.ProjectID, input.IdempotencyKey)
			if lookupErr == nil {
				if message != input.Message {
					return nil, false, biz.ErrIdempotencyConflict
				}
				return existing, true, nil
			}
			if !errors.Is(lookupErr, pgx.ErrNoRows) {
				return nil, false, lookupErr
			}
			if pgErr.ConstraintName == "uq_agent_runs_project_active" {
				return nil, false, biz.ErrActiveRun
			}
			return nil, false, biz.ErrIdempotencyConflict
		}
		return nil, false, err
	}
	_, err = tx.Exec(ctx, `insert into project_messages(id,project_id,owner_id,run_id,role,content,status,created_at,updated_at) values
($1,$3,$4,$2,'user',$5,'completed',$6,$6),
($7,$3,$4,$2,'assistant','','pending',$6 + interval '1 microsecond',$6 + interval '1 microsecond')`, input.InputMessageID, input.ID, input.ProjectID, input.OwnerID, input.Message, input.Now, input.AssistantMessageID)
	if err != nil {
		return nil, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	return &models.Run{ID: input.ID, OwnerID: input.OwnerID, ProjectID: input.ProjectID, IdempotencyKey: input.IdempotencyKey, InputMessageID: input.InputMessageID, AssistantMessageID: input.AssistantMessageID, InputMessage: input.Message, Status: models.RunStatusQueued, TraceParent: input.TraceParent, TraceState: input.TraceState, CreatedAt: input.Now, UpdatedAt: input.Now}, false, nil
}

func (r *runRepo) FindRunByIdempotency(ctx context.Context, ownerID, projectID, key, message string) (*models.Run, error) {
	pool, err := r.ready(ctx)
	if err != nil {
		return nil, err
	}
	run, storedMessage, err := r.getByIdempotency(ctx, pool, ownerID, projectID, key)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if storedMessage != message {
		return nil, biz.ErrIdempotencyConflict
	}
	return run, nil
}

func (r *runRepo) GetRun(ctx context.Context, ownerID, runID string) (*models.Run, error) {
	pool, err := r.ready(ctx)
	if err != nil {
		return nil, err
	}
	run, err := scanRun(pool.QueryRow(ctx, runSelect+` where id=$1 and owner_id=$2`, runID, ownerID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, biz.ErrRunNotFound
	}
	return run, err
}

func (r *runRepo) GetProjectRunState(ctx context.Context, ownerID, projectID string) (*models.ProjectRunState, error) {
	pool, err := r.ready(ctx)
	if err != nil {
		return nil, err
	}
	state := &models.ProjectRunState{}
	var active, last string
	activeErr := pool.QueryRow(ctx, `select id from agent_runs where owner_id=$1 and project_id=$2 and status in ($3,$4) order by created_at desc limit 1`, ownerID, projectID, models.RunStatusQueued, models.RunStatusRunning).Scan(&active)
	if activeErr == nil {
		state.ActiveRunID = &active
	} else if !errors.Is(activeErr, pgx.ErrNoRows) {
		return nil, activeErr
	}
	lastErr := pool.QueryRow(ctx, `select id from agent_runs where owner_id=$1 and project_id=$2 order by created_at desc limit 1`, ownerID, projectID).Scan(&last)
	if lastErr == nil {
		state.LastRunID = &last
	} else if !errors.Is(lastErr, pgx.ErrNoRows) {
		return nil, lastErr
	}
	return state, nil
}

func (r *runRepo) ListMessages(ctx context.Context, ownerID, projectID, cursor string, limit int) ([]*models.Message, *string, error) {
	pool, err := r.ready(ctx)
	if err != nil {
		return nil, nil, err
	}
	args := []any{ownerID, projectID, limit + 1}
	query := `select id,project_id,owner_id,run_id,role,content,status,created_at,updated_at from project_messages where owner_id=$1 and project_id=$2`
	if cursor != "" {
		query += ` and (created_at,id)<(select created_at,id from project_messages where id=$4 and owner_id=$1 and project_id=$2)`
		args = append(args, cursor)
	}
	query += ` order by created_at desc,id desc limit $3`
	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	items := make([]*models.Message, 0, limit+1)
	for rows.Next() {
		message, scanErr := scanMessage(rows)
		if scanErr != nil {
			return nil, nil, scanErr
		}
		items = append(items, message)
	}
	if err = rows.Err(); err != nil {
		return nil, nil, err
	}
	return finalizeMessagePage(items, limit)
}

func (r *runRepo) RequestCancel(ctx context.Context, ownerID, runID string, now time.Time) (*models.Run, bool, error) {
	pool, err := r.ready(ctx)
	if err != nil {
		return nil, false, err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx)
	run, err := scanRun(tx.QueryRow(ctx, runSelect+` where id=$1 and owner_id=$2 for update`, runID, ownerID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, biz.ErrRunNotFound
	}
	if err != nil {
		return nil, false, err
	}
	transitioned := false
	if run.Status == models.RunStatusQueued {
		transitioned = true
		run.Status, run.CompletedAt, run.CancelRequestedAt, run.UpdatedAt = models.RunStatusCancelled, &now, &now, now
		run.LastSequence++
		_, err = tx.Exec(ctx, `update agent_runs set status=$2,last_sequence=$4,cancel_requested_at=$3,completed_at=$3,updated_at=$3 where id=$1`, run.ID, run.Status, now, run.LastSequence)
		if err == nil {
			_, err = tx.Exec(ctx, `update project_messages set status='cancelled',updated_at=$2 where id=$1`, run.AssistantMessageID, now)
		}
	} else if run.Status == models.RunStatusRunning {
		transitioned = run.CancelRequestedAt == nil
		if transitioned {
			run.CancelRequestedAt, run.UpdatedAt = &now, now
			_, err = tx.Exec(ctx, `update agent_runs set cancel_requested_at=$2,updated_at=$2 where id=$1`, run.ID, now)
		}
	}
	if err != nil {
		return nil, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	return run, transitioned, nil
}

func (r *runRepo) GetRuntime(ctx context.Context, ownerID, projectID string) (*models.ProjectRuntime, error) {
	pool, err := r.ready(ctx)
	if err != nil {
		return nil, err
	}
	runtime := &models.ProjectRuntime{}
	err = pool.QueryRow(ctx, `select project_id,owner_id,gateway_session_id,agent_conversation_id,status,created_at,last_active_at,expires_at,updated_at from project_runtimes where project_id=$1 and owner_id=$2`, projectID, ownerID).Scan(&runtime.ProjectID, &runtime.OwnerID, &runtime.GatewaySessionID, &runtime.AgentConversationID, &runtime.Status, &runtime.CreatedAt, &runtime.LastActiveAt, &runtime.ExpiresAt, &runtime.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return runtime, err
}

func (r *runRepo) ExpireRuntime(ctx context.Context, ownerID, projectID string, now time.Time) error {
	pool, err := r.ready(ctx)
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx, `update project_runtimes set status=$3,updated_at=$4 where project_id=$1 and owner_id=$2`, projectID, ownerID, models.RuntimeStatusExpired, now)
	return err
}

func (r *runRepo) UpsertRuntime(ctx context.Context, runtime *models.ProjectRuntime) error {
	pool, err := r.ready(ctx)
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx, `insert into project_runtimes(project_id,owner_id,gateway_session_id,agent_conversation_id,status,created_at,last_active_at,expires_at,updated_at)
	values($1,$2,$3,$4,$5,$6,$7,$8,$9)
	on conflict(project_id) do update set gateway_session_id=excluded.gateway_session_id,agent_conversation_id=excluded.agent_conversation_id,status=excluded.status,last_active_at=excluded.last_active_at,updated_at=excluded.updated_at`, runtime.ProjectID, runtime.OwnerID, runtime.GatewaySessionID, runtime.AgentConversationID, runtime.Status, runtime.CreatedAt, runtime.LastActiveAt, runtime.ExpiresAt, runtime.UpdatedAt)
	return err
}

func (r *runRepo) ClaimNextRun(ctx context.Context, workerID string, now time.Time) (*models.Run, error) {
	pool, err := r.ready(ctx)
	if err != nil {
		return nil, err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	run, err := scanRun(tx.QueryRow(ctx, `with candidate as (
	select id from agent_runs where status=$1 order by created_at for update skip locked limit 1
) update agent_runs r set status=$2,worker_id=$3,started_at=$4,heartbeat_at=$4,updated_at=$4
from candidate where r.id=candidate.id
returning r.id,r.owner_id,r.project_id,r.idempotency_key,r.input_message_id,r.assistant_message_id,r.status,r.agent_run_id,r.last_sequence,r.worker_id,r.error_code,r.error_message,r.trace_parent,r.trace_state,r.created_at,r.updated_at,r.started_at,r.heartbeat_at,r.completed_at,r.cancel_requested_at`, models.RunStatusQueued, models.RunStatusRunning, workerID, now))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err = tx.QueryRow(ctx, `select content from project_messages where id=$1`, run.InputMessageID).Scan(&run.InputMessage); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `update project_messages set status='pending',updated_at=$2 where id=$1`, run.AssistantMessageID, now); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return run, nil
}

func (r *runRepo) Heartbeat(ctx context.Context, runID, workerID string, now time.Time) (bool, error) {
	pool, err := r.ready(ctx)
	if err != nil {
		return false, err
	}
	tag, err := pool.Exec(ctx, `update agent_runs set heartbeat_at=$3,updated_at=$3 where id=$1 and worker_id=$2 and status=$4`, runID, workerID, now, models.RunStatusRunning)
	return err == nil && tag.RowsAffected() == 1, err
}

func (r *runRepo) SetAgentRun(ctx context.Context, runID, workerID, agentRunID string, sequence int64, now time.Time) (bool, error) {
	pool, err := r.ready(ctx)
	if err != nil {
		return false, err
	}
	tag, err := pool.Exec(ctx, `update agent_runs set agent_run_id=case when $3='' then agent_run_id else $3 end,last_sequence=greatest(last_sequence,$4),updated_at=$5 where id=$1 and worker_id=$2 and status=$6`, runID, workerID, agentRunID, sequence, now, models.RunStatusRunning)
	return err == nil && tag.RowsAffected() == 1, err
}

func (r *runRepo) AppendAssistantDelta(ctx context.Context, runID, workerID, delta string, sequence int64, now time.Time) (bool, error) {
	pool, err := r.ready(ctx)
	if err != nil {
		return false, err
	}
	tag, err := pool.Exec(ctx, `with updated as (
	update agent_runs set last_sequence=greatest(last_sequence,$3),updated_at=$4 where id=$1 and worker_id=$2 and status=$6 returning assistant_message_id
) update project_messages m set content=m.content||$5,status='pending',updated_at=$4 from updated where m.id=updated.assistant_message_id`, runID, workerID, sequence, now, delta, models.RunStatusRunning)
	return err == nil && tag.RowsAffected() == 1, err
}

func (r *runRepo) FinishRun(ctx context.Context, runID, workerID, status, errorCode, errorMessage string, sequence int64, now time.Time) (bool, error) {
	pool, err := r.ready(ctx)
	if err != nil {
		return false, err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var assistantID string
	tag, err := tx.Exec(ctx, `update agent_runs set status=$3,error_code=$4,error_message=$5,last_sequence=greatest(last_sequence,$6),completed_at=$7,heartbeat_at=$7,updated_at=$7 where id=$1 and worker_id=$2 and status=$8`, runID, workerID, status, errorCode, errorMessage, sequence, now, models.RunStatusRunning)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	if err = tx.QueryRow(ctx, `select assistant_message_id from agent_runs where id=$1`, runID).Scan(&assistantID); err != nil {
		return false, err
	}
	messageStatus := "completed"
	if status == models.RunStatusFailed {
		messageStatus = "failed"
	}
	if status == models.RunStatusCancelled {
		messageStatus = "cancelled"
	}
	if _, err = tx.Exec(ctx, `update project_messages set status=$2,updated_at=$3 where id=$1`, assistantID, messageStatus, now); err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func (r *runRepo) FailOrphanedRuns(ctx context.Context, heartbeatBefore, now time.Time) ([]models.RunSequence, error) {
	pool, err := r.ready(ctx)
	if err != nil {
		return nil, err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `update agent_runs set status=$1,error_code='WORKER_HEARTBEAT_LOST',error_message='run worker heartbeat expired',last_sequence=last_sequence+1,completed_at=$2,updated_at=$2
	where status=$3 and heartbeat_at<$4 returning id,assistant_message_id,last_sequence`, models.RunStatusFailed, now, models.RunStatusRunning, heartbeatBefore)
	if err != nil {
		return nil, err
	}
	ids := make([]models.RunSequence, 0)
	messageIDs := make([]string, 0)
	for rows.Next() {
		var id, messageID string
		var sequence int64
		if err = rows.Scan(&id, &messageID, &sequence); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, models.RunSequence{RunID: id, Sequence: sequence})
		messageIDs = append(messageIDs, messageID)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	for _, id := range messageIDs {
		if _, err = tx.Exec(ctx, `update project_messages set status='failed',updated_at=$2 where id=$1`, id, now); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return ids, nil
}

func (r *runRepo) IsCancelRequested(ctx context.Context, runID string) (bool, error) {
	pool, err := r.ready(ctx)
	if err != nil {
		return false, err
	}
	var requested bool
	err = pool.QueryRow(ctx, `select cancel_requested_at is not null from agent_runs where id=$1`, runID).Scan(&requested)
	return requested, err
}

func (r *runRepo) TouchRuntime(ctx context.Context, projectID string, now time.Time) error {
	pool, err := r.ready(ctx)
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx, `update project_runtimes set last_active_at=$2,updated_at=$2 where project_id=$1 and status=$3`, projectID, now, models.RuntimeStatusActive)
	return err
}

func (r *runRepo) TouchRuntimeByPreviewToken(ctx context.Context, previewToken string, now time.Time) error {
	pool, err := r.ready(ctx)
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx, `update project_runtimes runtime set last_active_at=$2,updated_at=$2
	from project_previews preview
	where preview.preview_token=$1 and preview.project_id=runtime.project_id and preview.expires_at>$2
	and runtime.status=$3 and runtime.last_active_at<$2-interval '1 minute'`, previewToken, now, models.RuntimeStatusActive)
	return err
}

func (r *runRepo) SavePreview(ctx context.Context, preview *models.ProjectPreview) error {
	pool, err := r.ready(ctx)
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx, `insert into project_previews(id,project_id,owner_id,status,preview_url,preview_token,port,created_at,last_active_at,expires_at,updated_at)
values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
on conflict(project_id) do update set id=excluded.id,status=excluded.status,preview_url=excluded.preview_url,preview_token=excluded.preview_token,port=excluded.port,last_active_at=excluded.last_active_at,expires_at=excluded.expires_at,updated_at=excluded.updated_at`, preview.ID, preview.ProjectID, preview.OwnerID, preview.Status, preview.PreviewURL, preview.PreviewToken, preview.Port, preview.CreatedAt, preview.LastActiveAt, preview.ExpiresAt, preview.UpdatedAt)
	return err
}

func (r *runRepo) GetPreview(ctx context.Context, ownerID, projectID string) (*models.ProjectPreview, error) {
	pool, err := r.ready(ctx)
	if err != nil {
		return nil, err
	}
	p := &models.ProjectPreview{}
	err = pool.QueryRow(ctx, `select id,project_id,owner_id,status,preview_url,preview_token,port,created_at,last_active_at,expires_at,updated_at from project_previews where project_id=$1 and owner_id=$2`, projectID, ownerID).Scan(&p.ID, &p.ProjectID, &p.OwnerID, &p.Status, &p.PreviewURL, &p.PreviewToken, &p.Port, &p.CreatedAt, &p.LastActiveAt, &p.ExpiresAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, biz.ErrPreviewNotFound
	}
	return p, err
}

func (r *runRepo) ready(ctx context.Context) (*pgxpool.Pool, error) {
	r.poolOnce.Do(func() {
		dsn := strings.TrimSpace(viper.GetString("database.url"))
		if dsn == "" {
			r.poolErr = errors.New("database.url is required")
			return
		}
		r.pool, r.poolErr = pgxpool.New(ctx, dsn)
	})
	if r.poolErr != nil {
		return nil, r.poolErr
	}
	r.schemaMu.Lock()
	defer r.schemaMu.Unlock()
	if r.schemaReady {
		return r.pool, nil
	}
	r.schemaErr = nil
	{
		statements := []string{
			`create table if not exists agent_runs (
				id text primary key,owner_id text not null references users(id),project_id text not null references projects(id),idempotency_key text not null,
				input_message_id text not null,assistant_message_id text not null,status text not null,agent_run_id text not null default '',last_sequence bigint not null default 0,
				worker_id text not null default '',error_code text not null default '',error_message text not null default '',trace_parent text not null default '',trace_state text not null default '',created_at timestamptz not null,updated_at timestamptz not null,
				started_at timestamptz,heartbeat_at timestamptz,completed_at timestamptz,cancel_requested_at timestamptz)`,
			`alter table agent_runs add column if not exists trace_parent text not null default ''`,
			`alter table agent_runs add column if not exists trace_state text not null default ''`,
			`create unique index if not exists uq_agent_runs_idempotency on agent_runs(owner_id,project_id,idempotency_key)`,
			`create unique index if not exists uq_agent_runs_project_active on agent_runs(project_id) where status in ('queued','running')`,
			`create index if not exists idx_agent_runs_queue on agent_runs(status,created_at)`,
			`create table if not exists project_messages (
				id text primary key,project_id text not null references projects(id),owner_id text not null references users(id),run_id text references agent_runs(id),role text not null,
				content text not null,status text not null,created_at timestamptz not null,updated_at timestamptz not null)`,
			`create index if not exists idx_project_messages_project_created on project_messages(project_id,created_at,id)`,
			`create table if not exists project_runtimes (
				project_id text primary key references projects(id),owner_id text not null references users(id),gateway_session_id text not null,agent_conversation_id text not null,status text not null,
				created_at timestamptz not null,last_active_at timestamptz not null,expires_at timestamptz not null,updated_at timestamptz not null)`,
			`create table if not exists project_previews (
				id text not null,project_id text primary key references projects(id),owner_id text not null references users(id),status text not null,preview_url text not null,preview_token text not null,
				port integer not null,created_at timestamptz not null,last_active_at timestamptz not null,expires_at timestamptz not null,updated_at timestamptz not null)`,
		}
		for _, statement := range statements {
			if _, err := r.pool.Exec(ctx, statement); err != nil {
				r.schemaErr = err
				break
			}
		}
	}
	if r.schemaErr == nil {
		r.schemaReady = true
	}
	return r.pool, r.schemaErr
}

const runSelect = `select id,owner_id,project_id,idempotency_key,input_message_id,assistant_message_id,status,agent_run_id,last_sequence,worker_id,error_code,error_message,trace_parent,trace_state,created_at,updated_at,started_at,heartbeat_at,completed_at,cancel_requested_at from agent_runs`

func scanRun(scanner rowScanner) (*models.Run, error) {
	run := &models.Run{}
	err := scanner.Scan(&run.ID, &run.OwnerID, &run.ProjectID, &run.IdempotencyKey, &run.InputMessageID, &run.AssistantMessageID, &run.Status, &run.AgentRunID, &run.LastSequence, &run.WorkerID, &run.ErrorCode, &run.ErrorMessage, &run.TraceParent, &run.TraceState, &run.CreatedAt, &run.UpdatedAt, &run.StartedAt, &run.HeartbeatAt, &run.CompletedAt, &run.CancelRequestedAt)
	return run, err
}

func scanMessage(scanner rowScanner) (*models.Message, error) {
	message := &models.Message{}
	err := scanner.Scan(&message.ID, &message.ProjectID, &message.OwnerID, &message.RunID, &message.Role, &message.Content, &message.Status, &message.CreatedAt, &message.UpdatedAt)
	return message, err
}

func finalizeMessagePage(items []*models.Message, limit int) ([]*models.Message, *string, error) {
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	for left, right := 0, len(items)-1; left < right; left, right = left+1, right-1 {
		items[left], items[right] = items[right], items[left]
	}
	if !hasMore || len(items) == 0 {
		return items, nil, nil
	}
	next := items[0].ID
	return items, &next, nil
}

func (r *runRepo) getByIdempotency(ctx context.Context, pool *pgxpool.Pool, ownerID, projectID, key string) (*models.Run, string, error) {
	run, err := scanRun(pool.QueryRow(ctx, runSelect+` where owner_id=$1 and project_id=$2 and idempotency_key=$3`, ownerID, projectID, key))
	if err != nil {
		return nil, "", err
	}
	var message string
	if err = pool.QueryRow(ctx, `select content from project_messages where id=$1`, run.InputMessageID).Scan(&message); err != nil {
		return nil, "", err
	}
	return run, message, nil
}
