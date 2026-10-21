package extres

import (
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// BenchmarkReserve is TestLoad's large shape (S11a §8): 50 nodes, a quarter
// advertising GPUs, and 5,000 pods, a tenth requesting one.
func BenchmarkReserve(b *testing.B) {
	var nodes []*corev1.Node
	for i := 0; i < 50; i++ {
		c, a := rl("cpu", "32", "memory", "256Gi"), rl("cpu", "31", "memory", "250Gi")
		if i%4 == 0 {
			c, a = rl("cpu", "32", "memory", "256Gi", "nvidia.com/gpu", "8"), rl("cpu", "31", "memory", "250Gi", "nvidia.com/gpu", "8")
		}
		nodes = append(nodes, node(fmt.Sprintf("node-%02d", i), c, a, nil))
	}
	var pods []*corev1.Pod
	for i := 0; i < 5000; i++ {
		req := rl("cpu", "100m", "memory", "128Mi")
		if i%10 == 0 {
			req = rl("cpu", "100m", "memory", "128Mi", "nvidia.com/gpu", "1")
		}
		pods = append(pods, pod(fmt.Sprintf("p%05d", i), fmt.Sprintf("node-%02d", i%50), ctr("main", req)))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Reserve(nodes, pods)
	}
}
