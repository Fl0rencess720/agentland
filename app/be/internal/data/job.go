package data

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/Fl0rencess720/agentland/app/be/internal/biz"
	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	"github.com/Fl0rencess720/agentland/app/be/internal/pkgs/autherr"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/viper"
)

var (
	sharedJobRepoOnce sync.Once
	sharedJobRepo     *jobRepo
)

type jobRepo struct {
	poolOnce   sync.Once
	pool       *pgxpool.Pool
	poolErr    error
	schemaOnce sync.Once
	schemaErr  error
}

func NewJobRepo() biz.JobRepo {
	sharedJobRepoOnce.Do(func() {
		sharedJobRepo = &jobRepo{}
	})
	return sharedJobRepo
}

func (r *jobRepo) CreateJob(ctx context.Context, input *models.CreateJobInput) (*models.Job, error) {
	pool, err := r.ensurePool(ctx)
	if err != nil {
		return nil, err
	}
	if err = r.ensureSchema(ctx); err != nil {
		return nil, err
	}
	query := `insert into jobs (
		id, owner_id, project_id, type, status, progress, logs, result, request_payload,
		gateway_session_id, agent_session_id, workspace_path, error_message,
		created_at, updated_at, started_at, completed_at
	) values (
		$1,$2,$3,$4,$5,$6,$7::jsonb,$8::jsonb,$9::jsonb,$10,$11,$12,$13,$14,$14,null,null
	)
	returning id, owner_id, project_id, type, status, progress, logs, result, request_payload,
		gateway_session_id, agent_session_id, workspace_path, error_message,
		created_at, updated_at, started_at, completed_at`
	return r.scanJob(pool.QueryRow(ctx, query,
		input.ID,
		input.OwnerID,
		input.ProjectID,
		input.Type,
		input.Status,
		input.Progress,
		marshalJSONValue(input.Logs, []byte("[]")),
		marshalJSONValue(input.Result, []byte("null")),
		marshalJSONValue(input.RequestPayload, []byte("{}")),
		input.GatewaySessionID,
		input.AgentSessionID,
		input.WorkspacePath,
		input.ErrorMessage,
		input.Now,
	))
}

func (r *jobRepo) GetJobByID(ctx context.Context, ownerID, jobID string) (*models.Job, error) {
	pool, err := r.ensurePool(ctx)
	if err != nil {
		return nil, err
	}
	if err = r.ensureSchema(ctx); err != nil {
		return nil, err
	}
	query := `select id, owner_id, project_id, type, status, progress, logs, result, request_payload,
		gateway_session_id, agent_session_id, workspace_path, error_message,
		created_at, updated_at, started_at, completed_at
	from jobs where id = $1 and owner_id = $2`
	job, err := r.scanJob(pool.QueryRow(ctx, query, jobID, ownerID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, autherr.ErrJobNotFound
		}
		return nil, err
	}
	return job, nil
}

func (r *jobRepo) GetLatestProjectRuntime(ctx context.Context, ownerID, projectID string) (*models.Job, error) {
	pool, err := r.ensurePool(ctx)
	if err != nil {
		return nil, err
	}
	if err = r.ensureSchema(ctx); err != nil {
		return nil, err
	}
	query := `select id, owner_id, project_id, type, status, progress, logs, result, request_payload,
		gateway_session_id, agent_session_id, workspace_path, error_message,
		created_at, updated_at, started_at, completed_at
	from jobs
	where owner_id = $1 and project_id = $2 and type = 'APP_GENERATION' and gateway_session_id <> ''
	order by
		case status
			when 'RUNNING' then 1
			when 'STARTING' then 2
			when 'SUCCESS' then 3
			when 'FAILED' then 4
			else 5
		end,
		updated_at desc
	limit 1`
	job, err := r.scanJob(pool.QueryRow(ctx, query, ownerID, projectID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, autherr.ErrJobNotFound
		}
		return nil, err
	}
	return job, nil
}

func (r *jobRepo) UpdateJob(ctx context.Context, input *models.UpdateJobInput) error {
	pool, err := r.ensurePool(ctx)
	if err != nil {
		return err
	}
	if err = r.ensureSchema(ctx); err != nil {
		return err
	}
	result, err := pool.Exec(ctx, `update jobs set
		status = $2,
		progress = $3,
		logs = $4::jsonb,
		result = $5::jsonb,
		gateway_session_id = $6,
		agent_session_id = $7,
		workspace_path = $8,
		error_message = $9,
		started_at = $10,
		completed_at = $11,
		updated_at = $12
	where id = $1`,
		input.JobID,
		input.Status,
		input.Progress,
		marshalJSONValue(input.Logs, []byte("[]")),
		marshalJSONValue(input.Result, []byte("null")),
		input.GatewaySessionID,
		input.AgentSessionID,
		input.WorkspacePath,
		input.ErrorMessage,
		input.StartedAt,
		input.CompletedAt,
		input.UpdatedAt,
	)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return autherr.ErrJobNotFound
	}
	return nil
}

func (r *jobRepo) ensurePool(ctx context.Context) (*pgxpool.Pool, error) {
	r.poolOnce.Do(func() {
		dsn := strings.TrimSpace(viper.GetString("database.url"))
		if dsn == "" {
			r.poolErr = fmt.Errorf("database.url is required")
			return
		}
		r.pool, r.poolErr = pgxpool.New(ctx, dsn)
	})
	return r.pool, r.poolErr
}

func (r *jobRepo) ensureSchema(ctx context.Context) error {
	r.schemaOnce.Do(func() {
		pool, err := r.ensurePool(ctx)
		if err != nil {
			r.schemaErr = err
			return
		}
		statements := []string{
			`create table if not exists jobs (
				id text primary key,
				owner_id text not null references users(id),
				project_id text not null references projects(id),
				type text not null,
				status text not null,
				progress integer not null default 0,
				logs jsonb not null default '[]'::jsonb,
				result jsonb not null default 'null'::jsonb,
				request_payload jsonb not null default '{}'::jsonb,
				gateway_session_id text not null default '',
				agent_session_id text not null default '',
				workspace_path text not null default '',
				error_message text not null default '',
				created_at timestamptz not null,
				updated_at timestamptz not null,
				started_at timestamptz,
				completed_at timestamptz
			)`,
			`create index if not exists idx_jobs_owner_updated on jobs (owner_id, updated_at desc)`,
			`create index if not exists idx_jobs_project_updated on jobs (project_id, updated_at desc)`,
		}
		for _, stmt := range statements {
			if _, err = pool.Exec(ctx, stmt); err != nil {
				r.schemaErr = err
				return
			}
		}
	})
	return r.schemaErr
}

type jobScanner interface {
	Scan(dest ...any) error
}

func (r *jobRepo) scanJob(scanner jobScanner) (*models.Job, error) {
	var job models.Job
	var logsBytes []byte
	var resultBytes []byte
	var requestPayloadBytes []byte
	if err := scanner.Scan(
		&job.ID,
		&job.OwnerID,
		&job.ProjectID,
		&job.Type,
		&job.Status,
		&job.Progress,
		&logsBytes,
		&resultBytes,
		&requestPayloadBytes,
		&job.GatewaySessionID,
		&job.AgentSessionID,
		&job.WorkspacePath,
		&job.ErrorMessage,
		&job.CreatedAt,
		&job.UpdatedAt,
		&job.StartedAt,
		&job.CompletedAt,
	); err != nil {
		return nil, err
	}
	if len(logsBytes) > 0 {
		if err := json.Unmarshal(logsBytes, &job.Logs); err != nil {
			return nil, err
		}
	}
	if len(resultBytes) > 0 && string(resultBytes) != "null" {
		if err := json.Unmarshal(resultBytes, &job.Result); err != nil {
			return nil, err
		}
	}
	if len(requestPayloadBytes) > 0 && string(requestPayloadBytes) != "null" {
		if err := json.Unmarshal(requestPayloadBytes, &job.RequestPayload); err != nil {
			return nil, err
		}
	}
	if job.Logs == nil {
		job.Logs = []string{}
	}
	return &job, nil
}

func marshalJSONValue(value any, fallback []byte) string {
	if value == nil {
		return string(fallback)
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return string(fallback)
	}
	return string(payload)
}
