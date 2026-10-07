package syncer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/sbot5/lms-mcp/internal/store"
)

// syncEdCourse collects a complete course before publishing any managed file.
// Its database checkpoint and events advance together; failed publication or
// a rejected transaction restores the prior managed file generation.
func syncEdCourse(ctx context.Context, p *Providers, db *store.DB, course store.Course, full bool, holder string) (store.SnapshotResult, error) {
	discussion, err := collectEdDiscussions(ctx, p, db, course, full)
	if err != nil {
		return store.SnapshotResult{}, err
	}
	materials, batch, err := collectEdMaterials(ctx, p, course, full)
	if err != nil {
		return store.SnapshotResult{}, err
	}
	snapshot := materials
	snapshot.Items = append(snapshot.Items, discussion.Items...)
	snapshot.Kinds = append(snapshot.Kinds, discussion.Kinds...)
	snapshot.Warnings = append(snapshot.Warnings, discussion.Warnings...)
	snapshot.LeaseHolder = holder
	if err := ctx.Err(); err != nil {
		return store.SnapshotResult{}, errors.Join(err, batch.Rollback())
	}
	if err := batch.Commit(); err != nil {
		return store.SnapshotResult{}, errors.Join(err, batch.Rollback())
	}
	result, err := db.ApplyEdSnapshot(ctx, course.ID, snapshot, time.Now().Unix())
	if err != nil {
		return store.SnapshotResult{}, errors.Join(err, batch.Rollback())
	}
	if err := batch.Finish(); err != nil {
		return result, fmt.Errorf("Ed content saved, but staging cleanup failed: %w", err)
	}
	return result, nil
}
