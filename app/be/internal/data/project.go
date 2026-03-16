package data

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Fl0rencess720/agentland/app/be/internal/biz"
	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	"github.com/Fl0rencess720/agentland/app/be/internal/pkgs/autherr"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/viper"
)

var (
	sharedProjectRepoOnce sync.Once
	sharedProjectRepo     *projectRepo
)

type projectRepo struct {
	poolOnce   sync.Once
	pool       *pgxpool.Pool
	poolErr    error
	schemaOnce sync.Once
	schemaErr  error
}

func NewProjectRepo() biz.ProjectRepo {
	sharedProjectRepoOnce.Do(func() {
		sharedProjectRepo = &projectRepo{}
	})
	return sharedProjectRepo
}

func (r *projectRepo) CreateProject(ctx context.Context, input *models.CreateProjectInput) (*models.Project, error) {
	pool, err := r.ensurePool(ctx)
	if err != nil {
		return nil, err
	}
	if err = r.ensureSchema(ctx); err != nil {
		return nil, err
	}

	metadata := mustMarshalProjectMetadata(models.ProjectMetadata{})
	query := `insert into projects (id, owner_id, name, template, status, thumbnail_url, metadata, last_opened_at, created_at, updated_at, deleted_at)
values ($1,$2,$3,$4,$5,'',$6,null,$7,$7,null)
returning id, owner_id, name, template, status, thumbnail_url, metadata, last_opened_at, created_at, updated_at, deleted_at`
	return r.scanProject(pool.QueryRow(ctx, query, input.ID, input.OwnerID, input.Name, input.Template, input.Status, metadata, input.Now))
}

func (r *projectRepo) ListProjects(ctx context.Context, filter *models.ProjectListFilter) ([]*models.Project, int, error) {
	pool, err := r.ensurePool(ctx)
	if err != nil {
		return nil, 0, err
	}
	if err = r.ensureSchema(ctx); err != nil {
		return nil, 0, err
	}

	whereClauses := []string{"owner_id = $1", "deleted_at is null"}
	args := []any{filter.OwnerID}
	argIndex := 2
	if filter.Keyword != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("lower(name) like $%d", argIndex))
		args = append(args, "%"+strings.ToLower(filter.Keyword)+"%")
		argIndex++
	}
	if filter.Status != "" {
		whereClauses = append(whereClauses, fmt.Sprintf("status = $%d", argIndex))
		args = append(args, filter.Status)
		argIndex++
	}
	whereSQL := strings.Join(whereClauses, " and ")

	countQuery := "select count(*) from projects where " + whereSQL
	var total int
	if err = pool.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	orderBy := "updated_at desc"
	if filter.View == "recent" {
		orderBy = "coalesce(last_opened_at, updated_at) desc"
	} else {
		column := map[string]string{
			"updated_at": "updated_at",
			"created_at": "created_at",
			"name":       "name",
		}[filter.SortBy]
		if column == "" {
			column = "updated_at"
		}
		orderBy = column + " " + strings.ToUpper(filter.SortOrder)
	}
	limitArg := argIndex
	offsetArg := argIndex + 1
	args = append(args, filter.PageSize, (filter.Page-1)*filter.PageSize)
	listQuery := fmt.Sprintf(`select id, owner_id, name, template, status, thumbnail_url, metadata, last_opened_at, created_at, updated_at, deleted_at
from projects
where %s
order by %s
limit $%d offset $%d`, whereSQL, orderBy, limitArg, offsetArg)

	rows, err := pool.Query(ctx, listQuery, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	projects := make([]*models.Project, 0)
	for rows.Next() {
		project, scanErr := r.scanProject(rows)
		if scanErr != nil {
			return nil, 0, scanErr
		}
		projects = append(projects, project)
	}
	if err = rows.Err(); err != nil {
		return nil, 0, err
	}
	return projects, total, nil
}

func (r *projectRepo) GetProjectByID(ctx context.Context, ownerID, projectID string) (*models.Project, error) {
	pool, err := r.ensurePool(ctx)
	if err != nil {
		return nil, err
	}
	if err = r.ensureSchema(ctx); err != nil {
		return nil, err
	}
	query := `select id, owner_id, name, template, status, thumbnail_url, metadata, last_opened_at, created_at, updated_at, deleted_at
from projects
where id = $1 and owner_id = $2 and deleted_at is null`
	project, err := r.scanProject(pool.QueryRow(ctx, query, projectID, ownerID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, autherr.ErrProjectNotFound
		}
		return nil, err
	}
	return project, nil
}

func (r *projectRepo) GetProjectAndTouch(ctx context.Context, ownerID, projectID string, now time.Time) (*models.Project, error) {
	pool, err := r.ensurePool(ctx)
	if err != nil {
		return nil, err
	}
	if err = r.ensureSchema(ctx); err != nil {
		return nil, err
	}
	query := `update projects
set last_opened_at = $3
where id = $1 and owner_id = $2 and deleted_at is null
returning id, owner_id, name, template, status, thumbnail_url, metadata, last_opened_at, created_at, updated_at, deleted_at`
	project, err := r.scanProject(pool.QueryRow(ctx, query, projectID, ownerID, now))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, autherr.ErrProjectNotFound
		}
		return nil, err
	}
	return project, nil
}

func (r *projectRepo) UpdateProject(ctx context.Context, input *models.UpdateProjectInput) (*models.Project, error) {
	pool, err := r.ensurePool(ctx)
	if err != nil {
		return nil, err
	}
	if err = r.ensureSchema(ctx); err != nil {
		return nil, err
	}
	metadata := mustMarshalProjectMetadata(input.Metadata)
	query := `update projects
set name = $3, metadata = $4, updated_at = $5
where id = $1 and owner_id = $2 and deleted_at is null
returning id, owner_id, name, template, status, thumbnail_url, metadata, last_opened_at, created_at, updated_at, deleted_at`
	project, err := r.scanProject(pool.QueryRow(ctx, query, input.ProjectID, input.OwnerID, input.Name, metadata, input.Now))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, autherr.ErrProjectNotFound
		}
		return nil, err
	}
	return project, nil
}

func (r *projectRepo) UpdateProjectStatus(ctx context.Context, ownerID, projectID, status string, now time.Time) error {
	pool, err := r.ensurePool(ctx)
	if err != nil {
		return err
	}
	if err = r.ensureSchema(ctx); err != nil {
		return err
	}
	result, err := pool.Exec(ctx, `update projects set status = $3, updated_at = $4 where id = $1 and owner_id = $2 and deleted_at is null`, projectID, ownerID, status, now)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return autherr.ErrProjectNotFound
	}
	return nil
}

func (r *projectRepo) SoftDeleteProject(ctx context.Context, ownerID, projectID string, now time.Time) error {
	pool, err := r.ensurePool(ctx)
	if err != nil {
		return err
	}
	if err = r.ensureSchema(ctx); err != nil {
		return err
	}
	result, err := pool.Exec(ctx, `update projects set deleted_at = $3, updated_at = $3 where id = $1 and owner_id = $2 and deleted_at is null`, projectID, ownerID, now)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return autherr.ErrProjectNotFound
	}
	return nil
}

func (r *projectRepo) CountActiveProjectsByOwner(ctx context.Context, ownerID string) (int, error) {
	pool, err := r.ensurePool(ctx)
	if err != nil {
		return 0, err
	}
	if err = r.ensureSchema(ctx); err != nil {
		return 0, err
	}
	var total int
	if err = pool.QueryRow(ctx, `select count(*) from projects where owner_id = $1 and deleted_at is null`, ownerID).Scan(&total); err != nil {
		return 0, err
	}
	return total, nil
}

func (r *projectRepo) GetUserPlan(ctx context.Context, userID string) (string, error) {
	pool, err := r.ensurePool(ctx)
	if err != nil {
		return "", err
	}
	var plan string
	if err = pool.QueryRow(ctx, `select plan from users where id = $1`, userID).Scan(&plan); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", autherr.ErrUserNotFound
		}
		return "", err
	}
	return plan, nil
}

func (r *projectRepo) GetProjectChatSession(ctx context.Context, ownerID, projectID string) (*models.ProjectChatSession, error) {
	pool, err := r.ensurePool(ctx)
	if err != nil {
		return nil, err
	}
	if err = r.ensureSchema(ctx); err != nil {
		return nil, err
	}
	query := `select project_id, owner_id, gateway_session_id, agent_chat_session_id, workspace_path, created_at, updated_at, last_message_at
from project_chat_sessions
where project_id = $1 and owner_id = $2`
	session, err := r.scanProjectChatSession(pool.QueryRow(ctx, query, projectID, ownerID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return session, nil
}

func (r *projectRepo) UpsertProjectChatSession(ctx context.Context, input *models.UpsertProjectChatSessionInput) (*models.ProjectChatSession, error) {
	pool, err := r.ensurePool(ctx)
	if err != nil {
		return nil, err
	}
	if err = r.ensureSchema(ctx); err != nil {
		return nil, err
	}
	query := `insert into project_chat_sessions (project_id, owner_id, gateway_session_id, agent_chat_session_id, workspace_path, created_at, updated_at, last_message_at)
values ($1,$2,$3,$4,$5,$6,$6,$6)
on conflict (project_id) do update set
	owner_id = excluded.owner_id,
	gateway_session_id = excluded.gateway_session_id,
	agent_chat_session_id = excluded.agent_chat_session_id,
	workspace_path = excluded.workspace_path,
	updated_at = excluded.updated_at,
	last_message_at = excluded.last_message_at
returning project_id, owner_id, gateway_session_id, agent_chat_session_id, workspace_path, created_at, updated_at, last_message_at`
	return r.scanProjectChatSession(pool.QueryRow(ctx, query, input.ProjectID, input.OwnerID, input.GatewaySessionID, input.AgentChatSessionID, input.WorkspacePath, input.Now))
}

func (r *projectRepo) ListProjectChatMessages(ctx context.Context, ownerID, projectID, cursor string, limit int) ([]*models.ProjectChatMessage, *string, error) {
	pool, err := r.ensurePool(ctx)
	if err != nil {
		return nil, nil, err
	}
	if err = r.ensureSchema(ctx); err != nil {
		return nil, nil, err
	}
	if limit <= 0 {
		limit = 200
	}
	query := `select id, project_id, owner_id, role, content, created_at
from project_chat_messages
where project_id = $1 and owner_id = $2
order by
	created_at asc,
	case role
		when 'user' then 1
		when 'assistant' then 2
		else 3
	end asc,
	id asc
limit $3`
	rows, err := pool.Query(ctx, query, projectID, ownerID, limit)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	messages := make([]*models.ProjectChatMessage, 0)
	for rows.Next() {
		message, scanErr := r.scanProjectChatMessage(rows)
		if scanErr != nil {
			return nil, nil, scanErr
		}
		messages = append(messages, message)
	}
	if err = rows.Err(); err != nil {
		return nil, nil, err
	}
	_ = cursor
	return messages, nil, nil
}

func (r *projectRepo) UpdateProjectChatMessageContent(ctx context.Context, ownerID, projectID, messageID, content string) error {
	pool, err := r.ensurePool(ctx)
	if err != nil {
		return err
	}
	if err = r.ensureSchema(ctx); err != nil {
		return err
	}
	result, err := pool.Exec(ctx, `update project_chat_messages set content = $4 where id = $1 and project_id = $2 and owner_id = $3`, messageID, projectID, ownerID, content)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return autherr.ErrProjectNotFound
	}
	return nil
}

func (r *projectRepo) CreateProjectChatMessage(ctx context.Context, input *models.CreateProjectChatMessageInput) (*models.ProjectChatMessage, error) {
	pool, err := r.ensurePool(ctx)
	if err != nil {
		return nil, err
	}
	if err = r.ensureSchema(ctx); err != nil {
		return nil, err
	}
	// pgx QueryRow cannot safely consume multi-statements here, so use a transaction.
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	message, err := r.scanProjectChatMessage(tx.QueryRow(ctx, `insert into project_chat_messages (id, project_id, owner_id, role, content, created_at)
values ($1,$2,$3,$4,$5,$6)
returning id, project_id, owner_id, role, content, created_at`, input.ID, input.ProjectID, input.OwnerID, input.Role, input.Content, input.Now))
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `update project_chat_sessions set updated_at = $3, last_message_at = $3 where project_id = $1 and owner_id = $2`, input.ProjectID, input.OwnerID, input.Now); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return message, nil
}

func (r *projectRepo) ensurePool(ctx context.Context) (*pgxpool.Pool, error) {
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

func (r *projectRepo) ensureSchema(ctx context.Context) error {
	r.schemaOnce.Do(func() {
		pool, err := r.ensurePool(ctx)
		if err != nil {
			r.schemaErr = err
			return
		}
		statements := []string{
			`create table if not exists projects (
				id text primary key,
				owner_id text not null references users(id),
				name text not null,
				template text not null,
				status text not null,
				thumbnail_url text not null default '',
				metadata jsonb not null default '{}'::jsonb,
				last_opened_at timestamptz,
				created_at timestamptz not null,
				updated_at timestamptz not null,
				deleted_at timestamptz
			)`,
			`create index if not exists idx_projects_owner_deleted_updated on projects (owner_id, deleted_at, updated_at desc)`,
			`create index if not exists idx_projects_owner_deleted_last_opened on projects (owner_id, deleted_at, last_opened_at desc)`,
			`create index if not exists idx_projects_owner_deleted_status on projects (owner_id, deleted_at, status)`,
			`create index if not exists idx_projects_owner_name_search on projects (owner_id, lower(name)) where deleted_at is null`,
			`create table if not exists project_chat_sessions (
				project_id text primary key references projects(id),
				owner_id text not null references users(id),
				gateway_session_id text not null,
				agent_chat_session_id text not null,
				workspace_path text not null,
				created_at timestamptz not null,
				updated_at timestamptz not null,
				last_message_at timestamptz not null
			)`,
			`create index if not exists idx_project_chat_sessions_owner_updated on project_chat_sessions (owner_id, updated_at desc)`,
			`create table if not exists project_chat_messages (
				id text primary key,
				project_id text not null references projects(id),
				owner_id text not null references users(id),
				role text not null,
				content text not null,
				created_at timestamptz not null
			)`,
			`create index if not exists idx_project_chat_messages_project_created on project_chat_messages (project_id, created_at asc)`,
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

type projectScanner interface {
	Scan(dest ...any) error
}

func (r *projectRepo) scanProject(scanner projectScanner) (*models.Project, error) {
	var project models.Project
	var metadataBytes []byte
	var lastOpenedAt *time.Time
	var deletedAt *time.Time
	if err := scanner.Scan(
		&project.ID,
		&project.OwnerID,
		&project.Name,
		&project.Template,
		&project.Status,
		&project.ThumbnailURL,
		&metadataBytes,
		&lastOpenedAt,
		&project.CreatedAt,
		&project.UpdatedAt,
		&deletedAt,
	); err != nil {
		return nil, err
	}
	project.LastOpenedAt = lastOpenedAt
	project.DeletedAt = deletedAt
	if len(metadataBytes) > 0 {
		if err := json.Unmarshal(metadataBytes, &project.Metadata); err != nil {
			return nil, err
		}
	}
	return &project, nil
}

func mustMarshalProjectMetadata(metadata models.ProjectMetadata) []byte {
	payload, err := json.Marshal(metadata)
	if err != nil {
		return []byte("{}")
	}
	return payload
}

type projectChatSessionScanner interface {
	Scan(dest ...any) error
}

func (r *projectRepo) scanProjectChatSession(scanner projectChatSessionScanner) (*models.ProjectChatSession, error) {
	var session models.ProjectChatSession
	if err := scanner.Scan(&session.ProjectID, &session.OwnerID, &session.GatewaySessionID, &session.AgentChatSessionID, &session.WorkspacePath, &session.CreatedAt, &session.UpdatedAt, &session.LastMessageAt); err != nil {
		return nil, err
	}
	return &session, nil
}

type projectChatMessageScanner interface {
	Scan(dest ...any) error
}

func (r *projectRepo) scanProjectChatMessage(scanner projectChatMessageScanner) (*models.ProjectChatMessage, error) {
	var message models.ProjectChatMessage
	if err := scanner.Scan(&message.ID, &message.ProjectID, &message.OwnerID, &message.Role, &message.Content, &message.CreatedAt); err != nil {
		return nil, err
	}
	return &message, nil
}
