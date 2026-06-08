package gateway

import (
	"context"
	"fmt"
	"time"

	"github.com/goodrain/rainbond-plugin-template/pkg/model"
	"github.com/sirupsen/logrus"
)

type HTTPLoggerSyncer struct {
	Client       RouteClient
	MappingStore RouteMappingStore
	Config       HTTPLoggerConfig
	MappingOnly  bool
	Logger       *logrus.Logger
}

func (s HTTPLoggerSyncer) SyncHTTPLogger(ctx context.Context, namespace, appID string) error {
	return s.SyncHTTPLoggerForApp(ctx, namespace, appID, appID)
}

func (s HTTPLoggerSyncer) SyncHTTPLoggerForApp(ctx context.Context, namespace, matchAppID, mappingAppID string) error {
	return s.SyncHTTPLoggerForAppRoutes(ctx, namespace, matchAppID, mappingAppID, nil)
}

func (s HTTPLoggerSyncer) SyncHTTPLoggerForAppRoutes(ctx context.Context, namespace, matchAppID, mappingAppID string, serviceAliases []string) error {
	return s.SyncHTTPLoggerForAppRoutesWithMetadata(ctx, namespace, matchAppID, mappingAppID, serviceAliases, model.RouteMappingMetadata{})
}

func (s HTTPLoggerSyncer) SyncHTTPLoggerForAppRoutesWithMetadata(ctx context.Context, namespace, matchAppID, mappingAppID string, serviceAliases []string, metadata model.RouteMappingMetadata) error {
	if namespace == "" {
		return fmt.Errorf("namespace is required")
	}
	job := HTTPLoggerAttachJob{
		Client:         s.Client,
		MappingStore:   s.MappingStore,
		Namespaces:     []string{namespace},
		AppID:          matchAppID,
		MappingAppID:   mappingAppID,
		Metadata:       metadata,
		ServiceAliases: serviceAliases,
		Config:         s.Config,
		MappingOnly:    s.MappingOnly,
		Interval:       time.Minute,
		Logger:         s.Logger,
	}
	return job.RunOnce(ctx)
}
