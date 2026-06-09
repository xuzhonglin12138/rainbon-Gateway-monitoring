package service

import (
	"context"

	"github.com/goodrain/rainbond-plugin-template/pkg/model"
	corev1 "k8s.io/api/core/v1"
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
	pods, err := p.client.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	requestsByNode := nodeResourceRequests(pods.Items)
	nodes := make([]model.PlatformNode, 0, len(list.Items))
	for _, item := range list.Items {
		requested := requestsByNode[item.Name]
		cpuAllocatable := float64(item.Status.Allocatable.Cpu().MilliValue()) / 1000
		memoryAllocatable := float64(item.Status.Allocatable.Memory().Value())
		nodes = append(nodes, model.PlatformNode{
			Name:                   item.Name,
			Cluster:                nodeClusterLabel(item.Labels),
			Status:                 nodeReadyStatus(item),
			CPURequestedCores:      requested.cpuCores,
			CPUAllocatableCores:    cpuAllocatable,
			CPUAllocatedPercent:    percent(requested.cpuCores, cpuAllocatable),
			MemoryRequestedBytes:   requested.memoryBytes,
			MemoryAllocatableBytes: memoryAllocatable,
			MemoryAllocatedPercent: percent(requested.memoryBytes, memoryAllocatable),
		})
	}
	return nodes, nil
}

type nodeResourceRequest struct {
	cpuCores    float64
	memoryBytes float64
}

func nodeResourceRequests(pods []corev1.Pod) map[string]nodeResourceRequest {
	result := map[string]nodeResourceRequest{}
	for _, pod := range pods {
		if pod.Spec.NodeName == "" || pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		requested := result[pod.Spec.NodeName]
		for _, container := range pod.Spec.Containers {
			if cpu := container.Resources.Requests.Cpu(); cpu != nil {
				requested.cpuCores += float64(cpu.MilliValue()) / 1000
			}
			if memory := container.Resources.Requests.Memory(); memory != nil {
				requested.memoryBytes += float64(memory.Value())
			}
		}
		result[pod.Spec.NodeName] = requested
	}
	return result
}

func nodeReadyStatus(node corev1.Node) string {
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			if condition.Status == corev1.ConditionTrue {
				return "ready"
			}
			return "not_ready"
		}
	}
	return "unknown"
}

func percent(used, total float64) float64 {
	if total <= 0 {
		return 0
	}
	return used / total * 100
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
