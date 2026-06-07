package worker

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/rs/zerolog"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	yamldecoder "k8s.io/apimachinery/pkg/runtime/serializer/yaml"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
)

// NewApplyStage creates an ApplyFn that applies K8s manifests using the Go client
// with server-side apply. Uses the default kubeconfig (Minikube context).
func NewApplyStage(logger zerolog.Logger, manifestBase string) (ApplyFn, error) {
	if manifestBase == "" {
		manifestBase = "/tmp/deploy-api/manifests"
	}

	return func(ctx context.Context, namespace, manifestDir string) error {
		loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
		configOverrides := &clientcmd.ConfigOverrides{}
		kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)

		cfg, err := kubeConfig.ClientConfig()
		if err != nil {
			return fmt.Errorf("load kubeconfig (is Minikube running?): %w", err)
		}

		dynClient, err := dynamic.NewForConfig(cfg)
		if err != nil {
			return fmt.Errorf("create dynamic client: %w", err)
		}

		dc, err := discovery.NewDiscoveryClientForConfig(cfg)
		if err != nil {
			return fmt.Errorf("create discovery client: %w", err)
		}
		mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(dc))

		decoder := yamldecoder.NewDecodingSerializer(unstructured.UnstructuredJSONScheme)

		// Ensure the target namespace exists
		clientset, err := kubernetes.NewForConfig(cfg)
		if err == nil {
			_, err := clientset.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
			if err != nil {
				ns := &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{Name: namespace},
				}
				if _, err := clientset.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{}); err != nil {
					logger.Warn().Err(err).Str("namespace", namespace).Msg("failed to create namespace (may already exist)")
				} else {
					logger.Info().Str("namespace", namespace).Msg("created namespace")
				}
			}
		}

		entries, err := os.ReadDir(manifestDir)
		if err != nil {
			return fmt.Errorf("read manifest dir %s: %w", manifestDir, err)
		}

		start := time.Now()
		logger.Info().Str("namespace", namespace).Str("dir", manifestDir).Msg("applying kubernetes manifests via Go client")

		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			if ext := filepath.Ext(entry.Name()); ext != ".yaml" && ext != ".yml" {
				continue
			}

			data, err := os.ReadFile(filepath.Join(manifestDir, entry.Name()))
			if err != nil {
				logger.Warn().Err(err).Str("file", entry.Name()).Msg("skipping unreadable manifest")
				continue
			}

			docs := splitYAML(data)
			for _, doc := range docs {
				obj := &unstructured.Unstructured{}
				_, gvk, err := decoder.Decode(doc, nil, obj)
				if err != nil {
					logger.Warn().Err(err).Str("file", entry.Name()).Msg("skipping unparseable YAML doc")
					continue
				}
				if gvk == nil || obj.GetName() == "" {
					continue
				}

				mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
				if err != nil {
					logger.Warn().Err(err).Str("kind", gvk.Kind).Str("file", entry.Name()).Msg("skipping unknown resource type")
					continue
				}

				objBytes, err := obj.MarshalJSON()
				if err != nil {
					return fmt.Errorf("marshal %s/%s: %w", gvk.Kind, obj.GetName(), err)
				}

				ns := obj.GetNamespace()
				if ns == "" {
					ns = namespace
				}

				var res dynamic.ResourceInterface
				if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
					res = dynClient.Resource(mapping.Resource).Namespace(ns)
				} else {
					res = dynClient.Resource(mapping.Resource)
				}

				_, err = res.Patch(ctx, obj.GetName(), types.ApplyPatchType, objBytes, metav1.PatchOptions{
					FieldManager: "deploy-api",
				})
				if err != nil {
					return fmt.Errorf("apply %s/%s: %w", gvk.Kind, obj.GetName(), err)
				}

				logger.Debug().Str("kind", gvk.Kind).Str("name", obj.GetName()).Str("namespace", ns).Msg("applied resource")
			}
		}

		logger.Info().Str("namespace", namespace).Dur("duration", time.Since(start)).Msg("manifests applied")
		return nil
	}, nil
}

func splitYAML(data []byte) [][]byte {
	var docs [][]byte
	for _, doc := range bytes.Split(data, []byte("\n---\n")) {
		doc = bytes.TrimSpace(doc)
		if len(doc) == 0 {
			continue
		}
		docs = append(docs, doc)
	}
	if len(docs) == 0 {
		docs = append(docs, bytes.TrimSpace(data))
	}
	return docs
}
