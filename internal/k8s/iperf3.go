package k8s

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// Defaults for the iperf3 fixture. Pod and service share a name so the
// teardown logic can find them with a fixed label selector.
const (
	Iperf3Namespace    = "bnkctl-test"
	Iperf3PodName      = "bnkctl-iperf3"
	Iperf3SvcName      = "bnkctl-iperf3"
	Iperf3Port         = 5201
	Iperf3DefaultImage = "networkstatic/iperf3:latest"

	defaultReadyTimeout = 3 * time.Minute
	defaultLBTimeout    = 5 * time.Minute
	pollInterval        = 2 * time.Second
)

// Iperf3Options configures the in-cluster fixture.
type Iperf3Options struct {
	Namespace   string             // default: "bnkctl-test"
	Image       string             // default: networkstatic/iperf3:latest
	ServiceType corev1.ServiceType // ClusterIP for east-west, LoadBalancer for north-south
}

// DeployIperf3 ensures the test namespace exists, then creates the
// iperf3 server Pod + Service. Idempotent: existing fixtures are
// re-used (returns success). Caller follows up with WaitIperf3Ready
// and (for LoadBalancer) WaitLoadBalancerEndpoint.
func (c *Client) DeployIperf3(ctx context.Context, opts Iperf3Options) error {
	if opts.Namespace == "" {
		opts.Namespace = Iperf3Namespace
	}
	if opts.Image == "" {
		opts.Image = Iperf3DefaultImage
	}
	if opts.ServiceType == "" {
		opts.ServiceType = corev1.ServiceTypeClusterIP
	}

	if err := c.ensureNamespace(ctx, opts.Namespace); err != nil {
		return err
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      Iperf3PodName,
			Namespace: opts.Namespace,
			Labels:    map[string]string{"app": Iperf3PodName, "bnkctl.io/test": "iperf3"},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:  "iperf3",
				Image: opts.Image,
				Args:  []string{"-s"},
				Ports: []corev1.ContainerPort{{ContainerPort: Iperf3Port, Protocol: corev1.ProtocolTCP}},
			}},
			RestartPolicy: corev1.RestartPolicyAlways,
		},
	}

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      Iperf3SvcName,
			Namespace: opts.Namespace,
			Labels:    map[string]string{"app": Iperf3PodName, "bnkctl.io/test": "iperf3"},
		},
		Spec: corev1.ServiceSpec{
			Type:     opts.ServiceType,
			Selector: map[string]string{"app": Iperf3PodName},
			Ports: []corev1.ServicePort{{
				Port:       Iperf3Port,
				TargetPort: intstr.FromInt(Iperf3Port),
				Protocol:   corev1.ProtocolTCP,
			}},
		},
	}

	if _, err := c.clientset.CoreV1().Pods(opts.Namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating iperf3 pod: %w", err)
		}
	}
	if _, err := c.clientset.CoreV1().Services(opts.Namespace).Create(ctx, svc, metav1.CreateOptions{}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating iperf3 service: %w", err)
		}
	}
	return nil
}

// ensureNamespace creates the namespace if it doesn't already exist.
func (c *Client) ensureNamespace(ctx context.Context, name string) error {
	_, err := c.clientset.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("checking namespace %s: %w", name, err)
	}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"bnkctl.io/test": "true"}}}
	if _, err := c.clientset.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating namespace %s: %w", name, err)
		}
	}
	return nil
}

// WaitIperf3Ready polls until the pod is Running with all containers
// reporting Ready. Default timeout 3 minutes — generous because a
// cold image pull on slow nodes can take a while.
func (c *Client) WaitIperf3Ready(ctx context.Context, namespace string, timeout time.Duration) error {
	if namespace == "" {
		namespace = Iperf3Namespace
	}
	if timeout == 0 {
		timeout = defaultReadyTimeout
	}
	deadline := time.Now().Add(timeout)
	for {
		pod, err := c.clientset.CoreV1().Pods(namespace).Get(ctx, Iperf3PodName, metav1.GetOptions{})
		if err == nil && pod.Status.Phase == corev1.PodRunning && allContainersReady(pod) {
			return nil
		}
		if time.Now().After(deadline) {
			phase := "unknown"
			if pod != nil {
				phase = string(pod.Status.Phase)
			}
			return fmt.Errorf("timeout waiting for iperf3 pod ready (phase=%s)", phase)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

func allContainersReady(pod *corev1.Pod) bool {
	if len(pod.Status.ContainerStatuses) == 0 {
		return false
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if !cs.Ready {
			return false
		}
	}
	return true
}

// WaitLoadBalancerEndpoint polls until the iperf3 Service has an
// external IP or hostname assigned. Returns the address. Default
// timeout 5 minutes — IBM Cloud LB provisioning is typically 30–90s
// but spike loads can stretch.
func (c *Client) WaitLoadBalancerEndpoint(ctx context.Context, namespace string, timeout time.Duration) (string, error) {
	if namespace == "" {
		namespace = Iperf3Namespace
	}
	if timeout == 0 {
		timeout = defaultLBTimeout
	}
	deadline := time.Now().Add(timeout)
	for {
		svc, err := c.clientset.CoreV1().Services(namespace).Get(ctx, Iperf3SvcName, metav1.GetOptions{})
		if err == nil {
			for _, ing := range svc.Status.LoadBalancer.Ingress {
				if ing.IP != "" {
					return ing.IP, nil
				}
				if ing.Hostname != "" {
					return ing.Hostname, nil
				}
			}
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("timeout waiting for LoadBalancer endpoint on %s/%s", namespace, Iperf3SvcName)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// ClusterIPEndpoint returns the iperf3 Service's ClusterIP. For
// east-west tests where the client is also a Pod, this is the address
// to point iperf3 -c at.
func (c *Client) ClusterIPEndpoint(ctx context.Context, namespace string) (string, error) {
	if namespace == "" {
		namespace = Iperf3Namespace
	}
	svc, err := c.clientset.CoreV1().Services(namespace).Get(ctx, Iperf3SvcName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("reading service %s/%s: %w", namespace, Iperf3SvcName, err)
	}
	if svc.Spec.ClusterIP == "" {
		return "", fmt.Errorf("service %s/%s has no ClusterIP", namespace, Iperf3SvcName)
	}
	return svc.Spec.ClusterIP, nil
}

// TeardownIperf3 deletes the pod + service. Best-effort: errors on
// individual deletes are returned but each step still attempts.
func (c *Client) TeardownIperf3(ctx context.Context, namespace string) error {
	if namespace == "" {
		namespace = Iperf3Namespace
	}
	var firstErr error
	if err := c.clientset.CoreV1().Services(namespace).Delete(ctx, Iperf3SvcName, metav1.DeleteOptions{}); err != nil {
		if !apierrors.IsNotFound(err) {
			firstErr = fmt.Errorf("deleting service: %w", err)
		}
	}
	if err := c.clientset.CoreV1().Pods(namespace).Delete(ctx, Iperf3PodName, metav1.DeleteOptions{}); err != nil {
		if !apierrors.IsNotFound(err) && firstErr == nil {
			firstErr = fmt.Errorf("deleting pod: %w", err)
		}
	}
	return firstErr
}
