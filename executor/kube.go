package executor

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/microyahoo/storage-bot/security"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
)

type KubeExecutor struct {
	restConfig *rest.Config
	clientset  *kubernetes.Clientset
	namespace  string
	toolboxPod string
}

type KubeExecutorOptions struct {
	KubeconfigPath        string
	Namespace             string
	ToolboxPodHint        string
	ServerOverride        string
	InsecureSkipTLSVerify bool
}

func NewKubeExecutor(kubeconfigPath, namespace, toolboxPodHint string) (*KubeExecutor, error) {
	return NewKubeExecutorWithOptions(KubeExecutorOptions{
		KubeconfigPath: kubeconfigPath,
		Namespace:      namespace,
		ToolboxPodHint: toolboxPodHint,
	})
}

func NewKubeExecutorWithOptions(opts KubeExecutorOptions) (*KubeExecutor, error) {
	restConfig, err := clientcmd.BuildConfigFromFlags("", opts.KubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("build kubeconfig: %w", err)
	}

	if opts.ServerOverride != "" {
		restConfig.Host = opts.ServerOverride
	}
	if opts.InsecureSkipTLSVerify {
		restConfig.TLSClientConfig.Insecure = true
		restConfig.TLSClientConfig.CAData = nil
		restConfig.TLSClientConfig.CAFile = ""
	}

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("create k8s client: %w", err)
	}

	return &KubeExecutor{
		restConfig: restConfig,
		clientset:  clientset,
		namespace:  opts.Namespace,
		toolboxPod: opts.ToolboxPodHint,
	}, nil
}

func (k *KubeExecutor) findToolboxPod(ctx context.Context) (string, error) {
	if k.toolboxPod != "" {
		pod, err := k.clientset.CoreV1().Pods(k.namespace).Get(ctx, k.toolboxPod, metav1.GetOptions{})
		if err == nil && pod.Status.Phase == corev1.PodRunning {
			return k.toolboxPod, nil
		}
	}

	pods, err := k.clientset.CoreV1().Pods(k.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app=smd",
	})
	if err != nil {
		return "", fmt.Errorf("list smd pods: %w", err)
	}

	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodRunning {
			return pod.Name, nil
		}
	}

	pods, err = k.clientset.CoreV1().Pods(k.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app=rook-ceph-operator",
	})
	if err != nil {
		return "", fmt.Errorf("list operator pods: %w", err)
	}
	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodRunning {
			return pod.Name, nil
		}
	}

	return "", fmt.Errorf("no running toolbox or operator pod found in namespace %s", k.namespace)
}

// RunShellScript runs an arbitrary shell script inside the toolbox pod.
// stderr is merged into stdout (2>&1) to avoid SPDY stream deadlock where
// the executor waits for both streams to close independently.
func (k *KubeExecutor) RunShellScript(ctx context.Context, script string) (string, error) {
	podName, err := k.findToolboxPod(ctx)
	if err != nil {
		return "", err
	}
	slog.Info("run shell script", "pod", podName, "script", script)

	// Wrap in a subshell that merges stderr into stdout. This ensures only one
	// output stream is open, preventing SPDY from hanging after the process exits.
	merged := "{ " + script + "; } 2>&1"
	req := k.clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(k.namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Command: []string{"bash", "-c", merged},
			Stdout:  true,
			Stderr:  false,
		}, scheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(k.restConfig, "POST", req.URL())
	if err != nil {
		return "", fmt.Errorf("create executor: %w", err)
	}

	var buf bytes.Buffer
	if err := exec.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: &buf}); err != nil {
		return "", fmt.Errorf("exec failed: %w\noutput: %s", err, buf.String())
	}
	return buf.String(), nil
}

func (k *KubeExecutor) RunCephCommand(ctx context.Context, args ...string) (string, error) {
	if err := security.ValidateCephCommand(args); err != nil {
		return "", err
	}

	podName, err := k.findToolboxPod(ctx)
	if err != nil {
		return "", err
	}

	cmd := append([]string{"ceph"}, args...)
	return k.execInPod(ctx, podName, cmd)
}

func (k *KubeExecutor) CephHealth(ctx context.Context) (string, error) {
	commands := [][]string{
		{"status"},
		{"health", "detail"},
		{"df"},
		{"osd", "tree"},
	}

	var results []string
	for _, args := range commands {
		output, err := k.RunCephCommand(ctx, args...)
		if err != nil {
			results = append(results, fmt.Sprintf("=== ceph %s ===\nERROR: %v", strings.Join(args, " "), err))
		} else {
			results = append(results, fmt.Sprintf("=== ceph %s ===\n%s", strings.Join(args, " "), output))
		}
	}

	return strings.Join(results, "\n\n"), nil
}

// DiscoverNodes returns the internal IP and name of all Ready nodes in the cluster.
func (k *KubeExecutor) DiscoverNodes(ctx context.Context) ([]NodeInfo, error) {
	nodes, err := k.clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}

	var result []NodeInfo
	for _, node := range nodes.Items {
		if !isNodeReady(&node) {
			continue
		}
		ip := ""
		for _, addr := range node.Status.Addresses {
			if addr.Type == corev1.NodeInternalIP {
				ip = addr.Address
				break
			}
		}
		if ip != "" {
			result = append(result, NodeInfo{Name: node.Name, InternalIP: ip})
		}
	}
	return result, nil
}

type NodeInfo struct {
	Name       string
	InternalIP string
}

func isNodeReady(node *corev1.Node) bool {
	for _, cond := range node.Status.Conditions {
		if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func (k *KubeExecutor) execInPod(ctx context.Context, podName string, command []string) (string, error) {
	req := k.clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(k.namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Command: command,
			Stdout:  true,
			Stderr:  true,
		}, scheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(k.restConfig, "POST", req.URL())
	if err != nil {
		return "", fmt.Errorf("create executor: %w", err)
	}

	var stdout, stderr bytes.Buffer
	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if err != nil {
		if stderr.Len() > 0 {
			return "", fmt.Errorf("exec failed: %w, stderr: %s", err, stderr.String())
		}
		return "", fmt.Errorf("exec failed: %w", err)
	}

	output := stdout.String()
	if stderr.Len() > 0 {
		output += "\n[stderr]: " + stderr.String()
	}
	return output, nil
}
