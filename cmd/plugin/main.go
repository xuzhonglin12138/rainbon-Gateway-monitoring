package main

import (
	"context"
	"embed"
	"flag"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/goodrain/rainbond-plugin-template/pkg/license"
	"github.com/goodrain/rainbond-plugin-template/pkg/server"
	"github.com/sirupsen/logrus"
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

	// HTTP server
	srv := server.New(server.Config{
		Addr:     *addr,
		Checker:  checker,
		StaticFS: staticFiles,
		Logger:   logger,
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

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.WithError(err).Error("Shutdown error")
	}
	cancel()
	logger.Info("Plugin stopped")
}
