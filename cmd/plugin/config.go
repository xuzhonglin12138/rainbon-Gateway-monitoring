package main

// Plugin configuration. Modify these values for your plugin.
const (
	// PluginID is the unique identifier for this plugin.
	// Must match the plugin_id in plugin_manifest.
	PluginID = "rainbond-plugin-template"

	// DefaultAddr is the default HTTP listen address.
	DefaultAddr = ":8080"

	// LicenseNamespace is the namespace where the license ConfigMap is stored.
	LicenseNamespace = "rbd-system"

	// LicenseConfigMap is the ConfigMap name for license data.
	LicenseConfigMap = "rbd-license-info"

	// LicenseDataKey is the key in ConfigMap that stores the license JSON.
	LicenseDataKey = "license"

	// RecheckInterval is how often to re-verify the license (in minutes).
	RecheckInterval = 60

	// CollectorPath is the APISIX http-logger target path exposed by this plugin.
	CollectorPath = "/api/v1/collector/apisix/logs"

	// DefaultHTTPLoggerTimeout is the APISIX http-logger request timeout in seconds.
	DefaultHTTPLoggerTimeout = 3
)
