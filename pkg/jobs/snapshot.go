package jobs

import (
	"context"
	"time"

	"github.com/sirupsen/logrus"
)

type RouteGroupSnapshotStore interface {
	RefreshRouteGroupSnapshots(ctx context.Context) error
}

type SnapshotJob struct {
	Store    RouteGroupSnapshotStore
	Interval time.Duration
	Logger   *logrus.Logger
}

func (j SnapshotJob) RunOnce(ctx context.Context) error {
	return j.Store.RefreshRouteGroupSnapshots(ctx)
}

func (j SnapshotJob) Start(ctx context.Context) {
	interval := j.Interval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			if err := j.RunOnce(ctx); err != nil && j.Logger != nil {
				j.Logger.WithError(err).Warn("route group snapshot job failed")
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
