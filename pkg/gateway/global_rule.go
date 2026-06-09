package gateway

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

const (
	DefaultHTTPLoggerGlobalRuleName  = "rainbond-gateway-monitoring-http-logger"
	HTTPLoggerGlobalRuleManagedLabel = "network-monitor.rainbond.io/global-rule"
	httpLoggerManagedBy              = "rainbond-gateway-monitoring"
)

var apisixGlobalRuleGVR = schema.GroupVersionResource{
	Group:    "apisix.apache.org",
	Version:  "v2",
	Resource: "apisixglobalrules",
}

type GlobalRuleClient interface {
	UpsertHTTPLoggerGlobalRule(ctx context.Context, namespace, name string, cfg HTTPLoggerConfig) error
	DeleteManagedHTTPLoggerGlobalRules(ctx context.Context, namespaces []string, name string) error
	DeleteManagedHTTPLoggerGlobalRulesExcept(ctx context.Context, namespaces []string, name, keepNamespace string) error
}

type DynamicGlobalRuleClient struct {
	client dynamic.Interface
}

func NewDynamicGlobalRuleClient(client dynamic.Interface) *DynamicGlobalRuleClient {
	return &DynamicGlobalRuleClient{client: client}
}

func BuildHTTPLoggerGlobalRule(namespace, name string, cfg HTTPLoggerConfig) *unstructured.Unstructured {
	if name == "" {
		name = DefaultHTTPLoggerGlobalRuleName
	}
	rule := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "apisix.apache.org/v2",
		"kind":       "ApisixGlobalRule",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": namespace,
			"labels": map[string]interface{}{
				"app.kubernetes.io/managed-by":   httpLoggerManagedBy,
				HTTPLoggerGlobalRuleManagedLabel: "true",
			},
		},
		"spec": map[string]interface{}{
			"plugins": []interface{}{cfg.plugin()},
		},
	}}
	return rule
}

func (c *DynamicGlobalRuleClient) UpsertHTTPLoggerGlobalRule(ctx context.Context, namespace, name string, cfg HTTPLoggerConfig) error {
	if c == nil || c.client == nil {
		return fmt.Errorf("global rule client is required")
	}
	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		return fmt.Errorf("global rule namespace is required")
	}
	if name == "" {
		name = DefaultHTTPLoggerGlobalRuleName
	}
	desired := BuildHTTPLoggerGlobalRule(namespace, name, cfg)
	resource := c.client.Resource(apisixGlobalRuleGVR).Namespace(namespace)
	existing, err := resource.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = resource.Create(ctx, desired, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	desired.SetResourceVersion(existing.GetResourceVersion())
	_, err = resource.Update(ctx, desired, metav1.UpdateOptions{})
	return err
}

func (c *DynamicGlobalRuleClient) DeleteManagedHTTPLoggerGlobalRules(ctx context.Context, namespaces []string, name string) error {
	return c.deleteManagedHTTPLoggerGlobalRules(ctx, namespaces, name, "")
}

func (c *DynamicGlobalRuleClient) DeleteManagedHTTPLoggerGlobalRulesExcept(ctx context.Context, namespaces []string, name, keepNamespace string) error {
	return c.deleteManagedHTTPLoggerGlobalRules(ctx, namespaces, name, strings.TrimSpace(keepNamespace))
}

func (c *DynamicGlobalRuleClient) deleteManagedHTTPLoggerGlobalRules(ctx context.Context, namespaces []string, name, keepNamespace string) error {
	if c == nil || c.client == nil {
		return fmt.Errorf("global rule client is required")
	}
	if name == "" {
		name = DefaultHTTPLoggerGlobalRuleName
	}
	namespaces = normalizeGlobalRuleNamespaces(namespaces)
	for _, namespace := range namespaces {
		list, err := c.client.Resource(apisixGlobalRuleGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return err
		}
		for i := range list.Items {
			item := &list.Items[i]
			if item.GetName() != name {
				continue
			}
			labels := item.GetLabels()
			if labels[HTTPLoggerGlobalRuleManagedLabel] != "true" || labels["app.kubernetes.io/managed-by"] != httpLoggerManagedBy {
				continue
			}
			itemNamespace := item.GetNamespace()
			if itemNamespace == "" {
				itemNamespace = namespace
			}
			if keepNamespace != "" && itemNamespace == keepNamespace {
				continue
			}
			if err := c.client.Resource(apisixGlobalRuleGVR).Namespace(itemNamespace).Delete(ctx, item.GetName(), metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
	}
	return nil
}

type GlobalHTTPLoggerJob struct {
	RouteClient         RouteClient
	GlobalRules         GlobalRuleClient
	MappingStore        RouteMappingStore
	Namespaces          []string
	GlobalRuleName      string
	GlobalRuleNamespace string
	IngressClassName    string
	Config              HTTPLoggerConfig
	Interval            time.Duration
	Ready               func() bool
	Logger              *logrus.Logger
}

func (j *GlobalHTTPLoggerJob) RunOnce(ctx context.Context) error {
	if j.GlobalRules == nil {
		return fmt.Errorf("global rule client is required")
	}
	if !j.ready() {
		return j.Cleanup(ctx)
	}
	if j.RouteClient == nil {
		return fmt.Errorf("route client is required")
	}

	namespaces := normalizeGlobalRuleNamespaces(j.Namespaces)
	discoveredRuleNamespaces := make(map[string]struct{})
	for _, namespace := range namespaces {
		routes, err := j.RouteClient.List(ctx, namespace)
		if err != nil {
			return fmt.Errorf("list apisix routes in %s: %w", namespace, err)
		}
		for _, route := range routes {
			if !IsRainbondManagedRoute(route) {
				continue
			}
			if !j.matchesIngressClass(route) {
				continue
			}
			ruleNamespace := strings.TrimSpace(j.GlobalRuleNamespace)
			if ruleNamespace == "" {
				ruleNamespace = route.GetNamespace()
			}
			if ruleNamespace == "" {
				ruleNamespace = namespace
			}
			if ruleNamespace == metav1.NamespaceAll {
				continue
			}
			discoveredRuleNamespaces[ruleNamespace] = struct{}{}
		}
		if err := j.scanMappings(ctx, namespace); err != nil {
			return err
		}
	}

	ruleNamespace := j.resolveGlobalRuleNamespace(discoveredRuleNamespaces)
	if ruleNamespace == "" {
		if j.Logger != nil {
			j.Logger.Info("no rainbond apisix routes discovered for global http-logger")
		}
		return nil
	}

	if err := j.GlobalRules.DeleteManagedHTTPLoggerGlobalRulesExcept(ctx, []string{metav1.NamespaceAll}, j.globalRuleName(), ruleNamespace); err != nil {
		return fmt.Errorf("cleanup duplicate apisix global rules except %s/%s: %w", ruleNamespace, j.globalRuleName(), err)
	}
	if j.Logger != nil {
		j.Logger.WithFields(logrus.Fields{
			"namespace":     ruleNamespace,
			"global_rule":   j.globalRuleName(),
			"collector_uri": j.Config.URI,
		}).Info("ensuring apisix global http-logger")
	}
	if err := j.GlobalRules.UpsertHTTPLoggerGlobalRule(ctx, ruleNamespace, j.globalRuleName(), j.Config); err != nil {
		return fmt.Errorf("upsert apisix global rule %s/%s: %w", ruleNamespace, j.globalRuleName(), err)
	}
	return nil
}

func (j *GlobalHTTPLoggerJob) matchesIngressClass(route *unstructured.Unstructured) bool {
	expected := strings.TrimSpace(j.IngressClassName)
	if expected == "" {
		return true
	}
	actual, _, _ := unstructured.NestedString(route.Object, "spec", "ingressClassName")
	actual = strings.TrimSpace(actual)
	return actual == "" || actual == expected
}

func (j *GlobalHTTPLoggerJob) Cleanup(ctx context.Context) error {
	if j.GlobalRules == nil {
		return nil
	}
	namespaces := normalizeGlobalRuleNamespaces(j.Namespaces)
	if j.GlobalRuleNamespace != "" {
		namespaces = []string{j.GlobalRuleNamespace}
	}
	if j.Logger != nil {
		j.Logger.WithFields(logrus.Fields{
			"namespaces":    strings.Join(namespaces, ","),
			"global_rule":   j.globalRuleName(),
			"collector_uri": j.Config.URI,
		}).Info("cleaning apisix global http-logger")
	}
	return j.GlobalRules.DeleteManagedHTTPLoggerGlobalRules(ctx, namespaces, j.globalRuleName())
}

func (j *GlobalHTTPLoggerJob) Start(ctx context.Context) {
	interval := j.Interval
	if interval <= 0 {
		interval = time.Minute
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			if err := j.RunOnce(ctx); err != nil && j.Logger != nil {
				j.Logger.WithError(err).Warn("global http-logger job failed")
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (j *GlobalHTTPLoggerJob) scanMappings(ctx context.Context, namespace string) error {
	job := HTTPLoggerAttachJob{
		Client:       j.RouteClient,
		MappingStore: j.MappingStore,
		Namespaces:   []string{namespace},
		MappingOnly:  true,
		Config:       j.Config,
		Logger:       j.Logger,
	}
	return job.RunOnce(ctx)
}

func (j *GlobalHTTPLoggerJob) ready() bool {
	if j.Ready == nil {
		return true
	}
	return j.Ready()
}

func (j *GlobalHTTPLoggerJob) globalRuleName() string {
	if j.GlobalRuleName != "" {
		return j.GlobalRuleName
	}
	return DefaultHTTPLoggerGlobalRuleName
}

func (j *GlobalHTTPLoggerJob) resolveGlobalRuleNamespace(discovered map[string]struct{}) string {
	configured := strings.TrimSpace(j.GlobalRuleNamespace)
	if configured != "" && configured != metav1.NamespaceAll {
		return configured
	}
	if len(discovered) == 0 {
		return ""
	}
	namespaces := make([]string, 0, len(discovered))
	for namespace := range discovered {
		if namespace != "" && namespace != metav1.NamespaceAll {
			namespaces = append(namespaces, namespace)
		}
	}
	sort.Strings(namespaces)
	if len(namespaces) == 0 {
		return ""
	}
	return namespaces[0]
}

func normalizeGlobalRuleNamespaces(namespaces []string) []string {
	result := normalizeServiceAliases(namespaces)
	if len(result) == 0 {
		return []string{metav1.NamespaceAll}
	}
	return result
}
