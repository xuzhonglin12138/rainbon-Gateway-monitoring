package service

import (
	"context"

	"github.com/goodrain/rainbond-plugin-template/pkg/model"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type KubernetesNodeProvider struct {
	client kubernetes.Interface
}

func NewKubernetesNodeProvider(client kubernetes.Interface) *KubernetesNodeProvider {
	return &KubernetesNodeProvider{client: client}
}

func (p *KubernetesNodeProvider) ListPlatformNodes(ctx context.Context) ([]model.PlatformNode, error) {
	if p == nil || p.client == nil {
		return nil, nil
	}
	list, err := p.client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	nodes := make([]model.PlatformNode, 0, len(list.Items))
	for _, item := range list.Items {
		nodes = append(nodes, model.PlatformNode{
			Name:    item.Name,
			Cluster: nodeClusterLabel(item.Labels),
		})
	}
	return nodes, nil
}

func nodeClusterLabel(labels map[string]string) string {
	if labels == nil {
		return ""
	}
	for _, key := range []string{
		"topology.kubernetes.io/region",
		"failure-domain.beta.kubernetes.io/region",
		"kubernetes.io/cluster",
	} {
		if value := labels[key]; value != "" {
			return value
		}
	}
	return ""
}
