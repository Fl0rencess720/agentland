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
	poolOnce    sync.Once
	pool        *pgxpool.Pool
	poolErr     error
	schemaMu    sync.Mutex
	schemaReady bool
	schemaErr   error
}

func NewProjectRepo() biz.ProjectRepo {
	sharedProjectRepoOnce.Do(func() { sharedProjectRepo = &projectRepo{} })
	return sharedProjectRepo
}

func (r *projectRepo) CreateProject(ctx context.Context, input *models.CreateProjectInput) (*models.Project, error) {
	pool, err := r.ready(ctx)
	if err != nil {
		return nil, err
	}
	metadata, _ := json.Marshal(models.ProjectMetadata{})
	return scanProject(pool.QueryRow(ctx, `insert into projects
(id,owner_id,name,template,status,thumbnail_url,metadata,last_opened_at,created_at,updated_at,deleted_at)
values ($1,$2,$3,$4,$5,'',$6,null,$7,$7,null)
returning id,owner_id,name,template,status,thumbnail_url,metadata,last_opened_at,created_at,updated_at,deleted_at`, input.ID, input.OwnerID, input.Name, input.Template, input.Status, metadata, input.Now))
}

func (r *projectRepo) ListProjects(ctx context.Context, filter *models.ProjectListFilter) ([]*models.Project, int, error) {
	pool, err := r.ready(ctx)
	if err != nil {
		return nil, 0, err
	}
	where := []string{"owner_id=$1", "deleted_at is null"}
	args := []any{filter.OwnerID}
	n := 2
	if filter.Keyword != "" {
		where = append(where, fmt.Sprintf("lower(name) like $%d", n))
		args = append(args, "%"+strings.ToLower(filter.Keyword)+"%")
		n++
	}
	if filter.Status != "" {
		where = append(where, fmt.Sprintf("status=$%d", n))
		args = append(args, filter.Status)
		n++
	}
	whereSQL := strings.Join(where, " and ")
	var total int
	if err = pool.QueryRow(ctx, "select count(*) from projects where "+whereSQL, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	order := "updated_at desc"
	if filter.View == "recent" {
		order = "coalesce(last_opened_at,updated_at) desc"
	} else {
		columns := map[string]string{"updated_at": "updated_at", "created_at": "created_at", "name": "name"}
		column := columns[filter.SortBy]
		if column == "" {
			column = "updated_at"
		}
		direction := strings.ToUpper(filter.SortOrder)
		if direction != "ASC" {
			direction = "DESC"
		}
		order = column + " " + direction
	}
	args = append(args, filter.PageSize, (filter.Page-1)*filter.PageSize)
	query := fmt.Sprintf(`select id,owner_id,name,template,status,thumbnail_url,metadata,last_opened_at,created_at,updated_at,deleted_at
from projects where %s order by %s limit $%d offset $%d`, whereSQL, order, n, n+1)
	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := make([]*models.Project, 0)
	for rows.Next() {
		item, scanErr := scanProject(rows)
		if scanErr != nil {
			return nil, 0, scanErr
		}
		items = append(items, item)
	}
	return items, total, rows.Err()
}

func (r *projectRepo) GetProjectByID(ctx context.Context, ownerID, projectID string) (*models.Project, error) {
	pool, err := r.ready(ctx)
	if err != nil {
		return nil, err
	}
	project, err := scanProject(pool.QueryRow(ctx, projectSelect+` where id=$1 and owner_id=$2 and deleted_at is null`, projectID, ownerID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, autherr.ErrProjectNotFound
	}
	return project, err
}

func (r *projectRepo) GetProjectAndTouch(ctx context.Context, ownerID, projectID string, now time.Time) (*models.Project, error) {
	pool, err := r.ready(ctx)
	if err != nil {
		return nil, err
	}
	project, err := scanProject(pool.QueryRow(ctx, `update projects set last_opened_at=$3
where id=$1 and owner_id=$2 and deleted_at is null
returning id,owner_id,name,template,status,thumbnail_url,metadata,last_opened_at,created_at,updated_at,deleted_at`, projectID, ownerID, now))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, autherr.ErrProjectNotFound
	}
	return project, err
}

func (r *projectRepo) UpdateProject(ctx context.Context, input *models.UpdateProjectInput) (*models.Project, error) {
	pool, err := r.ready(ctx)
	if err != nil {
		return nil, err
	}
	metadata, err := json.Marshal(input.Metadata)
	if err != nil {
		return nil, err
	}
	project, err := scanProject(pool.QueryRow(ctx, `update projects set name=$3,metadata=$4,updated_at=$5
where id=$1 and owner_id=$2 and deleted_at is null
returning id,owner_id,name,template,status,thumbnail_url,metadata,last_opened_at,created_at,updated_at,deleted_at`, input.ProjectID, input.OwnerID, input.Name, metadata, input.Now))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, autherr.ErrProjectNotFound
	}
	return project, err
}

func (r *projectRepo) SoftDeleteProject(ctx context.Context, ownerID, projectID string, now time.Time) error {
	pool, err := r.ready(ctx)
	if err != nil {
		return err
	}
	tag, err := pool.Exec(ctx, `update projects set deleted_at=$3,updated_at=$3 where id=$1 and owner_id=$2 and deleted_at is null`, projectID, ownerID, now)
	if err == nil && tag.RowsAffected() == 0 {
		return autherr.ErrProjectNotFound
	}
	return err
}

func (r *projectRepo) CountActiveProjectsByOwner(ctx context.Context, ownerID string) (int, error) {
	pool, err := r.ready(ctx)
	if err != nil {
		return 0, err
	}
	var count int
	err = pool.QueryRow(ctx, `select count(*) from projects where owner_id=$1 and deleted_at is null`, ownerID).Scan(&count)
	return count, err
}

func (r *projectRepo) GetUserPlan(ctx context.Context, userID string) (string, error) {
	pool, err := r.ready(ctx)
	if err != nil {
		return "", err
	}
	var plan string
	err = pool.QueryRow(ctx, `select plan from users where id=$1`, userID).Scan(&plan)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", autherr.ErrUserNotFound
	}
	return plan, err
}

func (r *projectRepo) ready(ctx context.Context) (*pgxpool.Pool, error) {
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
		_, r.schemaErr = r.pool.Exec(ctx, `create table if not exists projects (
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
		)`)
		if r.schemaErr == nil {
			_, r.schemaErr = r.pool.Exec(ctx, `create index if not exists idx_projects_owner_updated on projects(owner_id,updated_at desc) where deleted_at is null`)
		}
	}
	if r.schemaErr == nil {
		r.schemaReady = true
	}
	return r.pool, r.schemaErr
}

const projectSelect = `select id,owner_id,name,template,status,thumbnail_url,metadata,last_opened_at,created_at,updated_at,deleted_at from projects`

type rowScanner interface{ Scan(...any) error }

func scanProject(scanner rowScanner) (*models.Project, error) {
	var project models.Project
	var metadata []byte
	if err := scanner.Scan(&project.ID, &project.OwnerID, &project.Name, &project.Template, &project.Status, &project.ThumbnailURL, &metadata, &project.LastOpenedAt, &project.CreatedAt, &project.UpdatedAt, &project.DeletedAt); err != nil {
		return nil, err
	}
	if len(metadata) != 0 {
		if err := json.Unmarshal(metadata, &project.Metadata); err != nil {
			return nil, err
		}
	}
	return &project, nil
}
