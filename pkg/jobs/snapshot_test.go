package jobs

import (
	"context"
	"testing"
	"time"
)

type fakeSnapshotStore struct {
	calls int
}

func (f *fakeSnapshotStore) RefreshRouteGroupSnapshots(_ context.Context) error {
	f.calls++
	return nil
}

func TestSnapshotJobRunOnceRefreshesSnapshots(t *testing.T) {
	store := &fakeSnapshotStore{}
	job := SnapshotJob{Store: store, Interval: 5 * time.Second}

	if err := job.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() unexpected error: %v", err)
	}
	if store.calls != 1 {
		t.Fatalf("refresh calls = %d; want 1", store.calls)
	}
}
