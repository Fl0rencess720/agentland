package data

import (
	"context"
	"fmt"
)

// Bootstrap creates application tables in foreign-key order and verifies Redis
// before the HTTP server or Run worker starts accepting work.
func Bootstrap(ctx context.Context) error {
	auth := sharedStore()
	if err := auth.ensureSchema(ctx); err != nil {
		return fmt.Errorf("initialize auth schema: %w", err)
	}
	projects := NewProjectRepo().(*projectRepo)
	if _, err := projects.ready(ctx); err != nil {
		return fmt.Errorf("initialize project schema: %w", err)
	}
	runs := NewRunRepo().(*runRepo)
	if _, err := runs.ready(ctx); err != nil {
		return fmt.Errorf("initialize run schema: %w", err)
	}
	redisClient, err := auth.ensureRedis()
	if err != nil {
		return fmt.Errorf("initialize redis: %w", err)
	}
	if err = redisClient.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("connect redis: %w", err)
	}
	return nil
}
