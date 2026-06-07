package worker

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// DeploymentHealth holds the result of a deployment health check.
type DeploymentHealth struct {
	Ready     bool   `json:"ready"`
	PodsReady int    `json:"pods_ready"`
	PodsTotal int    `json:"pods_total"`
	ServiceUp bool   `json:"service_up"`
	Duration  string `json:"duration"`
	Details   string `json:"details,omitempty"`
}

// NewHealthCheckStage creates a HealthCheckFn that verifies deployment readiness
// using the Kubernetes Go client. Uses the default kubeconfig (Minikube context).
// timeout is the maximum time to wait for all pods to become ready (default 120s).
func NewHealthCheckStage(logger zerolog.Logger, timeout time.Duration) HealthCheckFn {
	if timeout <= 0 {
		timeout = 120 * time.Second
	}

	return func(ctx context.Context, namespace string) *DeploymentHealth {
		start := time.Now()
		logger.Info().Str("namespace", namespace).Msg("checking deployment health via Go client")

		health := &DeploymentHealth{}

		loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
		configOverrides := &clientcmd.ConfigOverrides{}
		kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)

		cfg, err := kubeConfig.ClientConfig()
		if err != nil {
			health.Details = fmt.Sprintf("load kubeconfig (is Minikube running?): %v", err)
			health.Duration = time.Since(start).String()
			logger.Error().Err(err).Msg("health check: kubeconfig load failed")
			return health
		}

		clientset, err := kubernetes.NewForConfig(cfg)
		if err != nil {
			health.Details = fmt.Sprintf("create clientset: %v", err)
			health.Duration = time.Since(start).String()
			logger.Error().Err(err).Msg("health check: clientset creation failed")
			return health
		}

		podsReady, podsTotal, err := waitForPodsReady(ctx, clientset, logger, namespace, timeout)
		health.PodsReady = podsReady
		health.PodsTotal = podsTotal

		if err != nil {
			health.Details = fmt.Sprintf("pod readiness check: %v", err)
			health.Duration = time.Since(start).String()
			logger.Error().Err(err).Str("namespace", namespace).Msg("health check failed")
			return health
		}

		serviceUp, err := checkServiceEndpoint(clientset, namespace)
		health.ServiceUp = serviceUp
		if err != nil {
			health.Details = fmt.Sprintf("service endpoint check: %v", err)
		}

		health.Ready = podsReady == podsTotal && serviceUp
		health.Duration = time.Since(start).String()

		if health.Ready {
			logger.Info().Str("namespace", namespace).Dur("duration", time.Since(start)).Msg("deployment is healthy")
		} else {
			logger.Warn().Str("namespace", namespace).Dur("duration", time.Since(start)).Msg("deployment health check failed")
		}

		return health
	}
}

// waitForPodsReady polls the K8s API via client-go until all pods are Ready or timeout expires.
func waitForPodsReady(ctx context.Context, clientset kubernetes.Interface, logger zerolog.Logger, namespace string, timeout time.Duration) (ready, total int, err error) {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return 0, 0, ctx.Err()
		case <-ticker.C:
			pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
				FieldSelector: "status.phase!=Succeeded",
			})
			if err != nil {
				logger.Warn().Err(err).Str("namespace", namespace).Msg("list pods failed, retrying")
				continue
			}

			total = len(pods.Items)
			ready = 0
			for _, pod := range pods.Items {
				if pod.Status.Phase == corev1.PodRunning {
					for _, cond := range pod.Status.Conditions {
						if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
							ready++
							break
						}
					}
				}
			}

			if ready == total && total > 0 {
				return ready, total, nil
			}
		}
	}

	return ready, total, fmt.Errorf("timeout after %s: %d/%d pods ready", timeout, ready, total)
}

// checkServiceEndpoint verifies that at least one service in the namespace has ready endpoints.
func checkServiceEndpoint(clientset kubernetes.Interface, namespace string) (bool, error) {
	svcs, err := clientset.CoreV1().Services(namespace).List(context.Background(), metav1.ListOptions{Limit: 1})
	if err != nil {
		return false, fmt.Errorf("list services: %w", err)
	}
	if len(svcs.Items) == 0 {
		return false, fmt.Errorf("no services found in namespace %s", namespace)
	}

	svc := svcs.Items[0]
	eps, err := clientset.CoreV1().Endpoints(namespace).Get(context.Background(), svc.Name, metav1.GetOptions{})
	if err != nil {
		return false, fmt.Errorf("get endpoints for %s: %w", svc.Name, err)
	}

	for _, subset := range eps.Subsets {
		if len(subset.Addresses) > 0 {
			return true, nil
		}
	}

	return false, fmt.Errorf("service %s has no ready endpoints", svc.Name)
}
