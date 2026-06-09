package main

import (
	"context"
	"embed"
	"flag"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	promclient "github.com/goodrain/rainbond-plugin-template/pkg/clients/prometheus"
	"github.com/goodrain/rainbond-plugin-template/pkg/gateway"
	"github.com/goodrain/rainbond-plugin-template/pkg/jobs"
	"github.com/goodrain/rainbond-plugin-template/pkg/license"
	"github.com/goodrain/rainbond-plugin-template/pkg/repository"
	"github.com/goodrain/rainbond-plugin-template/pkg/server"
	"github.com/goodrain/rainbond-plugin-template/pkg/service"
	"github.com/sirupsen/logrus"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

//go:embed static/*
var staticFiles embed.FS

var (
	// DefaultPublicKeyPEM can be embedded at build time via ldflags:
	//   go build -ldflags "-X main.DefaultPublicKeyPEM=$(cat public.pem)"
	DefaultPublicKeyPEM = ""
)

func main() {
	var (
		addr         = flag.String("addr", DefaultAddr, "HTTP listen address")
		kubeconfig   = flag.String("kubeconfig", "", "Path to kubeconfig (uses in-cluster config if empty)")
		publicKeyPEM = flag.String("public-key", "", "Path to RSA public key PEM file")
		logLevel     = flag.String("log-level", "info", "Log level (debug, info, warn, error)")
	)
	flag.Parse()

	// Logger
	logger := logrus.New()
	level, err := logrus.ParseLevel(*logLevel)
	if err != nil {
		logger.WithError(err).Warn("Invalid log level, using info")
		level = logrus.InfoLevel
	}
	logger.SetLevel(level)
	logger.SetFormatter(&logrus.TextFormatter{FullTimestamp: true})

	// Load RSA public key
	var publicKeyData []byte
	if *publicKeyPEM != "" {
		publicKeyData, err = os.ReadFile(*publicKeyPEM)
		if err != nil {
			logger.WithError(err).Fatal("Failed to read public key file")
		}
	} else if DefaultPublicKeyPEM != "" {
		publicKeyData = []byte(DefaultPublicKeyPEM)
	} else {
		logger.Fatal("Public key is required. Use -public-key flag or embed at build time with -ldflags")
	}

	rsaPublicKey, err := license.DecodePublicKeyFromPEM(publicKeyData)
	if err != nil {
		logger.WithError(err).Fatal("Failed to decode public key")
	}

	// Kubernetes client
	var config *rest.Config
	if *kubeconfig != "" {
		config, err = clientcmd.BuildConfigFromFlags("", *kubeconfig)
	} else {
		config, err = rest.InClusterConfig()
	}
	if err != nil {
		logger.WithError(err).Fatal("Failed to create Kubernetes config")
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		logger.WithError(err).Fatal("Failed to create Kubernetes client")
	}
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		logger.WithError(err).Fatal("Failed to create Kubernetes dynamic client")
	}

	// License checker
	checker := license.NewChecker(license.CheckerConfig{
		Clientset: clientset,
		PublicKey: rsaPublicKey,
		PluginID:  PluginID,
		Namespace: LicenseNamespace,
		ConfigMap: LicenseConfigMap,
		DataKey:   LicenseDataKey,
		Logger:    logger,
	})

	// Initial license check
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	result := checker.Check(ctx)
	if !result.Valid {
		logger.WithField("reason", result.Message).Warn("License check failed at startup, plugin will serve 403 until licensed")
	} else {
		logger.WithFields(logrus.Fields{
			"company": result.Token.Company,
			"tier":    result.Token.Tier,
			"expire":  time.Unix(result.Token.ExpireAt, 0).Format(time.RFC3339),
		}).Info("License verified")
	}

	// Start periodic re-check
	checker.StartPeriodicCheck(ctx, time.Duration(RecheckInterval)*time.Minute)

	redisClient := repository.NewRedisClient(repository.RedisClientConfig{
		Addr:     envOrDefault("NM_REDIS_ADDR", "127.0.0.1:6379"),
		Password: os.Getenv("NM_REDIS_PASSWORD"),
		DB:       envInt("NM_REDIS_DB", 0),
		Timeout:  time.Duration(envInt("NM_REDIS_TIMEOUT_SECONDS", 3)) * time.Second,
		TLS:      envBool("NM_REDIS_TLS", false),
	})
	redisStore := repository.NewRedisStore(redisClient)
	prometheusClient := promclient.NewClient(promclient.Config{
		BaseURL: envOrDefault("NM_PROMETHEUS_URL", "http://127.0.0.1:9999"),
		Timeout: time.Duration(envInt("NM_PROMETHEUS_TIMEOUT_SECONDS", 3)) * time.Second,
	})
	slaService := service.NewSLAService(service.SLAConfig{
		Prometheus: prometheusClient,
		Store:      redisStore,
		Target:     envFloat("NM_DEFAULT_SLA_TARGET", 0.999),
		Logger:     logger,
	})
	overviewService := service.NewOverviewService(service.OverviewConfig{
		Prometheus:   prometheusClient,
		Store:        redisStore,
		NodeProvider: service.NewKubernetesNodeProvider(clientset),
		Logger:       logger,
	})
	collector := service.NewInternalRouteCollector(service.CollectorConfig{
		Store:           redisStore,
		Mapper:          redisStore,
		RouteGroupRules: redisStore,
		Logger:          logger,
		RouteGroups: service.NewRouteGroupResolver(service.RouteGroupConfig{
			MaxGroupsPerScope: envInt("NM_ROUTE_GROUP_LIMIT", 100),
			TemplateRules: []service.RouteGroupRule{
				{Prefix: "/api/user/setting/", Group: "/api/user/setting/*"},
				{Prefix: "/api/order/detail/", Group: "/api/order/detail/*"},
			},
		}),
	})
	snapshotJob := jobs.SnapshotJob{
		Store:    redisStore,
		Interval: time.Duration(envInt("NM_SNAPSHOT_REFRESH_SECONDS", 5)) * time.Second,
		Logger:   logger,
	}
	snapshotJob.Start(ctx)

	routeClient := gateway.NewDynamicRouteClient(dynamicClient)
	collectorURI := collectorURIFromEnv()
	logger.WithField("collector_uri", collectorURI).Info("resolved apisix http-logger collector uri")
	httpLoggerConfig := gateway.HTTPLoggerConfig{
		URI:             collectorURI,
		Timeout:         envInt("NM_HTTP_LOGGER_TIMEOUT_SECONDS", DefaultHTTPLoggerTimeout),
		SSLVerify:       envBool("NM_HTTP_LOGGER_SSL_VERIFY", false),
		BatchMaxSize:    envInt("NM_HTTP_LOGGER_BATCH_MAX_SIZE", DefaultHTTPLoggerBatchMaxSize),
		InactiveTimeout: envInt("NM_HTTP_LOGGER_INACTIVE_TIMEOUT_SECONDS", DefaultHTTPLoggerInactiveTimeout),
		BufferDuration:  envInt("NM_HTTP_LOGGER_BUFFER_DURATION_SECONDS", DefaultHTTPLoggerBufferDuration),
		LogFormat:       gateway.DefaultHTTPLoggerLogFormat(),
	}
	httpLoggerMode := httpLoggerModeFromEnv(logger)
	licenseBypass := envBool("NM_SKIP_LICENSE_CHECK", false)
	httpLoggerSyncer := gateway.HTTPLoggerSyncer{
		Client:       routeClient,
		MappingStore: redisStore,
		Config:       httpLoggerConfig,
		MappingOnly:  httpLoggerMode == "global",
		Logger:       logger,
	}

	namespaces := splitCSV(os.Getenv("NM_APISIX_NAMESPACES"))
	var globalHTTPLoggerJob *gateway.GlobalHTTPLoggerJob
	switch httpLoggerMode {
	case "route":
		if len(namespaces) > 0 {
			logger.WithField("namespaces", namespaces).Info("Starting APISIX route-level http-logger attach job")
			job := &gateway.HTTPLoggerAttachJob{
				Client:       routeClient,
				MappingStore: redisStore,
				Namespaces:   namespaces,
				Config:       httpLoggerConfig,
				Interval:     time.Duration(envInt("NM_HTTP_LOGGER_SYNC_INTERVAL_SECONDS", 60)) * time.Second,
				Logger:       logger,
			}
			job.Start(ctx)
		} else {
			logger.Info("route-level http-logger mode selected but NM_APISIX_NAMESPACES is empty; background attach job is disabled")
		}
	case "global":
		globalHTTPLoggerJob = &gateway.GlobalHTTPLoggerJob{
			RouteClient:         routeClient,
			GlobalRules:         gateway.NewDynamicGlobalRuleClient(dynamicClient),
			MappingStore:        redisStore,
			Namespaces:          namespaces,
			GlobalRuleName:      envOrDefault("NM_HTTP_LOGGER_GLOBAL_RULE_NAME", gateway.DefaultHTTPLoggerGlobalRuleName),
			GlobalRuleNamespace: strings.TrimSpace(os.Getenv("NM_APISIX_GLOBAL_RULE_NAMESPACE")),
			IngressClassName:    strings.TrimSpace(os.Getenv("NM_APISIX_INGRESS_CLASS")),
			Config:              httpLoggerConfig,
			Interval:            time.Duration(envInt("NM_HTTP_LOGGER_SYNC_INTERVAL_SECONDS", 60)) * time.Second,
			Ready: func() bool {
				return collector != nil && (licenseBypass || checker.IsValid())
			},
			Logger: logger,
		}
		globalHTTPLoggerJob.Start(ctx)
	case "off":
		cleanupJob := &gateway.GlobalHTTPLoggerJob{
			GlobalRules:         gateway.NewDynamicGlobalRuleClient(dynamicClient),
			Namespaces:          namespaces,
			GlobalRuleName:      envOrDefault("NM_HTTP_LOGGER_GLOBAL_RULE_NAME", gateway.DefaultHTTPLoggerGlobalRuleName),
			GlobalRuleNamespace: strings.TrimSpace(os.Getenv("NM_APISIX_GLOBAL_RULE_NAMESPACE")),
			IngressClassName:    strings.TrimSpace(os.Getenv("NM_APISIX_INGRESS_CLASS")),
			Config:              httpLoggerConfig,
			Ready:               func() bool { return false },
			Logger:              logger,
		}
		if err := cleanupJob.Cleanup(ctx); err != nil {
			logger.WithError(err).Warn("cleanup global http-logger failed")
		}
	}

	// HTTP server
	srv := server.New(server.Config{
		Addr:             *addr,
		Checker:          checker,
		StaticFS:         staticFiles,
		Logger:           logger,
		Collector:        collector,
		QueryStore:       redisStore,
		SLAService:       slaService,
		OverviewService:  overviewService,
		ConfigStore:      redisStore,
		DefaultSLATarget: envFloat("NM_DEFAULT_SLA_TARGET", 0.999),
		HTTPLoggerSyncer: httpLoggerSyncer,
		HTTPLoggerMode:   httpLoggerMode,
	})

	go func() {
		if err := srv.Start(); err != nil {
			logger.WithError(err).Fatal("Server failed")
		}
	}()

	logger.WithFields(logrus.Fields{
		"addr":      *addr,
		"plugin_id": PluginID,
	}).Info("Plugin started")

	// Graceful shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	logger.Info("Shutting down...")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()

	if globalHTTPLoggerJob != nil {
		if err := globalHTTPLoggerJob.Cleanup(shutdownCtx); err != nil {
			logger.WithError(err).Warn("cleanup global http-logger during shutdown failed")
		}
	}
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.WithError(err).Error("Shutdown error")
	}
	cancel()
	logger.Info("Plugin stopped")
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func collectorURIFromEnv() string {
	configured := strings.TrimSpace(os.Getenv("NM_COLLECTOR_URI"))
	if configured != "" && configured != DefaultCollectorURI && !isKubernetesServiceURI(configured) {
		return configured
	}
	if uri := rainbondNodePortCollectorURI(); uri != "" {
		return uri
	}
	if configured != "" {
		return configured
	}
	return DefaultCollectorURI
}

func httpLoggerModeFromEnv(logger *logrus.Logger) string {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("NM_HTTP_LOGGER_MODE")))
	if mode == "" {
		mode = "global"
	}
	switch mode {
	case "global", "route", "off":
		return mode
	default:
		if logger != nil {
			logger.WithField("mode", mode).Warn("invalid NM_HTTP_LOGGER_MODE, using global")
		}
		return "global"
	}
}

func isKubernetesServiceURI(uri string) bool {
	return strings.Contains(uri, ".svc")
}

func rainbondNodePortCollectorURI() string {
	alias := strings.ToUpper(strings.TrimSpace(os.Getenv("_SERVICE_ALIAS")))
	host := strings.TrimSpace(os.Getenv("_HOST_IP"))
	if alias == "" || host == "" {
		return ""
	}

	prefix := alias + "_"
	var nodePort int
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || !strings.HasPrefix(key, prefix) || !strings.HasSuffix(key, "_SERVICE_HOST") {
			continue
		}
		candidate := strings.TrimSuffix(strings.TrimPrefix(key, prefix), "_SERVICE_HOST")
		parsed, err := strconv.Atoi(candidate)
		if err != nil || parsed < 30000 || parsed > 32767 {
			continue
		}
		if os.Getenv(prefix+candidate+"_SERVICE_PORT") != "8080" {
			continue
		}
		if nodePort == 0 || parsed < nodePort {
			nodePort = parsed
		}
	}
	if nodePort == 0 {
		return ""
	}
	return "http://" + host + ":" + strconv.Itoa(nodePort) + CollectorPath
}

func envInt(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envBool(key string, fallback bool) bool {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envFloat(key string, fallback float64) float64 {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

func splitCSV(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}
