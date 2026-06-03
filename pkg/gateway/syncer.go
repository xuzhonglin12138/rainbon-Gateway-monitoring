package gateway

import (
	"context"
	"fmt"
	"time"

	"github.com/sirupsen/logrus"
)

type HTTPLoggerSyncer struct {
	Client       RouteClient
	MappingStore RouteMappingStore
	Config       HTTPLoggerConfig
	Logger       *logrus.Logger
}

func (s HTTPLoggerSyncer) SyncHTTPLogger(ctx context.Context, namespace, appID string) error {
	return s.SyncHTTPLoggerForApp(ctx, namespace, appID, appID)
}

func (s HTTPLoggerSyncer) SyncHTTPLoggerForApp(ctx context.Context, namespace, matchAppID, mappingAppID string) error {
	if namespace == "" {
		return fmt.Errorf("namespace is required")
	}
	job := HTTPLoggerAttachJob{
		Client:       s.Client,
		MappingStore: s.MappingStore,
		Namespaces:   []string{namespace},
		AppID:        matchAppID,
		MappingAppID: mappingAppID,
		Config:       s.Config,
		Interval:     time.Minute,
		Logger:       s.Logger,
	}
	return job.RunOnce(ctx)
}
