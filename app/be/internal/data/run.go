package data

import (
	"context"
	"encoding/json"
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
	poolOnce               sync.Once
	pool                   *pgxpool.Pool
	poolErr                error
	leaseOnce              sync.Once
	leaseStore             workerLeaseStore
	leaseErr               error
	snapshotOnce           sync.Once
	snapshotStore          SnapshotObjectStore
	snapshotArtifactsStore *workspaceSnapshotArtifacts
	snapshotErr            error
	schemaMu               sync.Mutex
	schemaReady            bool
	schemaErr              error
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
	if _, err = tx.Exec(ctx, `select id from projects where id=$1 and owner_id=$2 for update`, input.ProjectID, input.OwnerID); err != nil {
		return nil, false, err
	}
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
	var preparationActive bool
	if err = tx.QueryRow(ctx, `select exists(select 1 from project_publications where project_id=$1 and status=$2)`, input.ProjectID, models.PublicationStatusPreparing).Scan(&preparationActive); err != nil {
		return nil, false, err
	}
	if preparationActive {
		return nil, false, biz.ErrActivePublication
	}
	_, err = tx.Exec(ctx, `insert into agent_runs
(id,owner_id,project_id,idempotency_key,input_message_id,assistant_message_id,status,agent_run_id,agent_conversation_id,last_sequence,error_code,error_message,trace_parent,trace_state,created_at,updated_at,started_at,completed_at,cancel_requested_at)
values($1,$2,$3,$4,$5,$6,$7,$1,$8,0,'','',$9,$10,$11,$11,$11,null,null)`, input.ID, input.OwnerID, input.ProjectID, input.IdempotencyKey, input.InputMessageID, input.AssistantMessageID, models.RunStatusRunning, input.AgentConversationID, input.TraceParent, input.TraceState, input.Now)
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
			if pgErr.ConstraintName == "uq_agent_runs_project_running" || pgErr.ConstraintName == "uq_agent_runs_project_active" {
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
	started := input.Now
	return &models.Run{ID: input.ID, OwnerID: input.OwnerID, ProjectID: input.ProjectID, IdempotencyKey: input.IdempotencyKey, AgentConversationID: input.AgentConversationID, InputMessageID: input.InputMessageID, AssistantMessageID: input.AssistantMessageID, InputMessage: input.Message, Status: models.RunStatusRunning, AgentRunID: input.ID, TraceParent: input.TraceParent, TraceState: input.TraceState, CreatedAt: input.Now, UpdatedAt: input.Now, StartedAt: &started}, false, nil
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

func (r *runRepo) GetRunForExecution(ctx context.Context, runID string) (*models.Run, error) {
	pool, err := r.ready(ctx)
	if err != nil {
		return nil, err
	}
	run, err := scanRun(pool.QueryRow(ctx, runSelect+` where id=$1`, runID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err = pool.QueryRow(ctx, `select content from project_messages where id=$1`, run.InputMessageID).Scan(&run.InputMessage); err != nil {
		return nil, err
	}
	return run, nil
}

func (r *runRepo) AcquireRunOwnership(ctx context.Context, runID, owner string) (bool, error) {
	leases, err := r.leases()
	if err != nil {
		return false, err
	}
	return leases.Acquire(ctx, runLeaseKind, runID, owner, workerLeaseTTL(runLeaseKind))
}

func (r *runRepo) RenewRunOwnership(ctx context.Context, runID, owner string) (bool, error) {
	leases, err := r.leases()
	if err != nil {
		return false, err
	}
	return leases.Renew(ctx, runLeaseKind, runID, owner, workerLeaseTTL(runLeaseKind))
}

func (r *runRepo) ReleaseRunOwnership(ctx context.Context, runID, owner string) (bool, error) {
	leases, err := r.leases()
	if err != nil {
		return false, err
	}
	return leases.Release(ctx, runLeaseKind, runID, owner)
}

func (r *runRepo) ExpiredRunOwnerships(ctx context.Context, before time.Time, limit int64) ([]models.WorkerOwnership, error) {
	leases, err := r.leases()
	if err != nil {
		return nil, err
	}
	candidates, err := leases.Expired(ctx, runLeaseKind, before, limit)
	if err != nil {
		return nil, err
	}
	result := make([]models.WorkerOwnership, 0, len(candidates))
	for _, candidate := range candidates {
		result = append(result, models.WorkerOwnership{ID: candidate.ID, OwnerID: candidate.OwnerID})
	}
	return result, nil
}

func (r *runRepo) TakeoverRunOwnership(ctx context.Context, runID, previousOwner, owner string) (bool, error) {
	leases, err := r.leases()
	if err != nil {
		return false, err
	}
	result, err := leases.AcquireRecovery(ctx, runLeaseKind, []leaseCandidate{{ID: runID, OwnerID: previousOwner}}, owner, workerLeaseTTL(runLeaseKind))
	return result[runID], err
}

func (r *runRepo) GetProjectRunState(ctx context.Context, ownerID, projectID string) (*models.ProjectRunState, error) {
	pool, err := r.ready(ctx)
	if err != nil {
		return nil, err
	}
	state := &models.ProjectRunState{}
	var active, last string
	activeErr := pool.QueryRow(ctx, `select id from agent_runs where owner_id=$1 and project_id=$2 and status=$3 order by created_at desc limit 1`, ownerID, projectID, models.RunStatusRunning).Scan(&active)
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
	query := `select id,project_id,owner_id,run_id,role,content,status,created_at,updated_at from project_messages where owner_id=$1 and project_id=$2 and hidden=false`
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
	if run.Status == models.RunStatusRunning {
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

func (r *runRepo) FailRunDispatch(ctx context.Context, runID string, now time.Time, cause error) error {
	pool, err := r.ready(ctx)
	if err != nil {
		return err
	}
	message := ""
	if cause != nil {
		message = cause.Error()
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var assistantID string
	err = tx.QueryRow(ctx, `update agent_runs set status=$2,error_code='KAFKA_PUBLISH_FAILED',error_message=$3,completed_at=$4,updated_at=$4
		where id=$1 and status=$5 returning assistant_message_id`, runID, models.RunStatusFailed, message, now, models.RunStatusRunning).Scan(&assistantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `update project_messages set status='failed',updated_at=$2 where id=$1`, assistantID, now); err != nil {
		return err
	}
	return tx.Commit(ctx)
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

func (r *runRepo) LoadTrajectoryRecords(ctx context.Context, runID string) ([]models.RunTrajectoryRecord, error) {
	pool, err := r.ready(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := pool.Query(ctx, `select record from run_trajectory_records where run_id=$1 order by sequence`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := make([]models.RunTrajectoryRecord, 0)
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var record models.RunTrajectoryRecord
		if err := json.Unmarshal(data, &record); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
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
				input_message_id text not null,assistant_message_id text not null,status text not null,agent_run_id text not null default '',agent_conversation_id text not null default '',last_sequence bigint not null default 0,
				error_code text not null default '',error_message text not null default '',trace_parent text not null default '',trace_state text not null default '',created_at timestamptz not null,updated_at timestamptz not null,
				started_at timestamptz,completed_at timestamptz,cancel_requested_at timestamptz)`,
			`alter table agent_runs add column if not exists trace_parent text not null default ''`,
			`alter table agent_runs add column if not exists trace_state text not null default ''`,
			`alter table agent_runs add column if not exists agent_conversation_id text not null default ''`,
			`alter table agent_runs drop column if exists worker_id`,
			`alter table agent_runs drop column if exists heartbeat_at`,
			`update agent_runs set status='failed',error_code='LEGACY_QUEUED_RUN',error_message='run predates direct Kafka dispatch',completed_at=coalesce(completed_at,now()),updated_at=now() where status='queued'`,
			`create unique index if not exists uq_agent_runs_idempotency on agent_runs(owner_id,project_id,idempotency_key)`,
			`create unique index if not exists uq_agent_runs_project_running on agent_runs(project_id) where status='running'`,
			`drop index if exists uq_agent_runs_project_active`,
			`drop index if exists idx_agent_runs_queue`,
			`drop index if exists idx_agent_runs_recovery`,
			`create table if not exists project_messages (
				id text primary key,project_id text not null references projects(id),owner_id text not null references users(id),run_id text references agent_runs(id),role text not null,
				content text not null,status text not null,hidden boolean not null default false,created_at timestamptz not null,updated_at timestamptz not null)`,
			`alter table project_messages add column if not exists hidden boolean not null default false`,
			`create index if not exists idx_project_messages_project_created on project_messages(project_id,created_at,id)`,
			`create table if not exists project_runtimes (
				project_id text primary key references projects(id),owner_id text not null references users(id),gateway_session_id text not null,agent_conversation_id text not null,status text not null,
				created_at timestamptz not null,last_active_at timestamptz not null,expires_at timestamptz not null,updated_at timestamptz not null)`,
			`create table if not exists project_previews (
				id text not null,project_id text primary key references projects(id),owner_id text not null references users(id),status text not null,preview_url text not null,preview_token text not null,
				port integer not null,created_at timestamptz not null,last_active_at timestamptz not null,expires_at timestamptz not null,updated_at timestamptz not null)`,
			`create table if not exists run_workspace_snapshots (
				run_id text primary key references agent_runs(id) on delete cascade,content bytea,object_key text not null default '',sha256 text not null,size_bytes bigint not null default 0,capture_error text not null default '',created_at timestamptz not null)`,
			`alter table run_workspace_snapshots add column if not exists object_key text not null default ''`,
			`alter table run_workspace_snapshots add column if not exists size_bytes bigint not null default 0`,
			`alter table run_workspace_snapshots alter column content drop not null`,
			`create table if not exists run_trajectory_records (
				run_id text not null references agent_runs(id) on delete cascade,sequence bigint not null,record_hash text not null,record bytea not null,created_at timestamptz not null,primary key(run_id,sequence))`,
			`create unique index if not exists uq_run_trajectory_hash on run_trajectory_records(run_id,record_hash)`,
			`create table if not exists project_publications (
				id text primary key,owner_id text not null references users(id),project_id text not null references projects(id),idempotency_key text not null,
				build_context text not null,dockerfile text not null,status text not null,worker_id text not null default '',image_ref text not null default '',image_digest text not null default '',
				deployment_url text not null default '',deployment_hostname text not null default '',deployment_name text not null default '',
				build_logs text not null default '',error_code text not null default '',error_message text not null default '',trace_parent text not null default '',trace_state text not null default '',
				preparation_run_id text references agent_runs(id),snapshot_object_key text not null default '',snapshot_sha256 text not null default '',snapshot_size_bytes bigint not null default 0,
				build_dispatched_at timestamptz,created_at timestamptz not null,updated_at timestamptz not null,started_at timestamptz,heartbeat_at timestamptz,completed_at timestamptz,cancel_requested_at timestamptz)`,
			`alter table project_publications add column if not exists preparation_run_id text references agent_runs(id)`,
			`alter table project_publications add column if not exists snapshot_object_key text not null default ''`,
			`alter table project_publications add column if not exists snapshot_sha256 text not null default ''`,
			`alter table project_publications add column if not exists snapshot_size_bytes bigint not null default 0`,
			`alter table project_publications add column if not exists build_dispatched_at timestamptz`,
			`alter table project_publications add column if not exists deployment_url text not null default ''`,
			`alter table project_publications add column if not exists deployment_hostname text not null default ''`,
			`alter table project_publications add column if not exists deployment_name text not null default ''`,
			`create unique index if not exists uq_project_publications_idempotency on project_publications(owner_id,project_id,idempotency_key)`,
			`create unique index if not exists uq_project_publications_active_v2 on project_publications(project_id) where status in ('preparing','queued','running')`,
			`drop index if exists uq_project_publications_active`,
			`create unique index if not exists uq_project_publications_preparation_run on project_publications(preparation_run_id) where preparation_run_id is not null`,
			`create index if not exists idx_project_publications_queue on project_publications(status,created_at)`,
			`create index if not exists idx_project_publications_recovery on project_publications(status,id)`,
			`create index if not exists idx_project_publications_project_created on project_publications(project_id,created_at desc)`,
			`drop table if exists kafka_outbox`,
			`create table if not exists run_events (
				run_id text not null references agent_runs(id) on delete cascade,sequence bigint not null,event_type text not null,data bytea not null,
				created_at timestamptz not null,expires_at timestamptz,primary key(run_id,sequence))`,
			`create index if not exists idx_run_events_expiry on run_events(expires_at) where expires_at is not null`,
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

const runSelect = `select id,owner_id,project_id,idempotency_key,input_message_id,assistant_message_id,status,agent_run_id,agent_conversation_id,last_sequence,error_code,error_message,trace_parent,trace_state,created_at,updated_at,started_at,completed_at,cancel_requested_at from agent_runs`

func scanRun(scanner rowScanner) (*models.Run, error) {
	run := &models.Run{}
	err := scanner.Scan(&run.ID, &run.OwnerID, &run.ProjectID, &run.IdempotencyKey, &run.InputMessageID, &run.AssistantMessageID, &run.Status, &run.AgentRunID, &run.AgentConversationID, &run.LastSequence, &run.ErrorCode, &run.ErrorMessage, &run.TraceParent, &run.TraceState, &run.CreatedAt, &run.UpdatedAt, &run.StartedAt, &run.CompletedAt, &run.CancelRequestedAt)
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
