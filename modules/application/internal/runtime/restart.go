package runtime

import (
	"context"
	"errors"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/funcx27/kite/modules/application/internal/store"
	"github.com/funcx27/kite/modules/application/internal/syncops"
)

// RestartAnnotation stamps the pod template for a rolling restart, the same
// mechanism the host's deployment restart uses
// (pkg/resources/deployment_handler.go:40). syncops.Normalize curates it
// away, so a restart never shows up as drift.
const RestartAnnotation = "kite.kubernetes.io/restartedAt"

// ErrNoWorkload reports that the config has no workload document to act on.
var ErrNoWorkload = errors.New("config has no workload document")

// ErrPodNotFound reports that the pod is unknown or does not belong to the
// app's workload; both hide the pod's existence.
var ErrPodNotFound = errors.New("pod not found in this application")

// RestartWorkload rolls the app's workload (design §10 应用重启): the live
// pod template is stamped with RestartAnnotation, which triggers the
// controller's rollout exactly like the host's deployment restart. The
// workload must be deployed.
func RestartWorkload(ctx context.Context, cli client.Client, app, namespace string, manifests []unstructured.Unstructured) (*unstructured.Unstructured, error) {
	wl := WorkloadOf(app, manifests)
	if wl == nil {
		return nil, ErrNoWorkload
	}
	live := syncops.BlankDoc(wl)
	if err := cli.Get(ctx, client.ObjectKey{Namespace: namespace, Name: wl.GetName()}, live); err != nil {
		return nil, fmt.Errorf("get workload %s: %w", wl.GetName(), err)
	}
	spec, ok := store.AsMap(live.Object["spec"])
	if !ok {
		return nil, fmt.Errorf("workload %s has no spec to annotate", wl.GetName())
	}
	tpl, ok := store.AsMap(spec["template"])
	if !ok {
		return nil, fmt.Errorf("workload %s has no pod template", wl.GetName())
	}
	// Pod-template annotations live under template.metadata — writing
	// template.annotations would create an unknown field the API server
	// silently drops (the update would no-op with a success response).
	meta, ok := store.AsMap(tpl["metadata"])
	if !ok {
		meta = map[string]interface{}{}
		tpl["metadata"] = meta
	}
	annotations, ok := store.AsMap(meta["annotations"])
	if !ok {
		annotations = map[string]interface{}{}
		meta["annotations"] = annotations
	}
	annotations[RestartAnnotation] = time.Now().UTC().Format(time.RFC3339)
	if err := cli.Update(ctx, live); err != nil {
		return nil, fmt.Errorf("update workload %s: %w", wl.GetName(), err)
	}
	return live, nil
}

// appPod resolves the pod and verifies it belongs to the app's workload
// selector — the module never deletes pods outside its app.
func appPod(ctx context.Context, cli client.Client, app, namespace string, manifests []unstructured.Unstructured, podName string) (*unstructured.Unstructured, error) {
	wl := WorkloadOf(app, manifests)
	if wl == nil {
		return nil, ErrNoWorkload
	}
	live, err := fetchLiveDoc(ctx, cli, namespace, wl)
	if err != nil {
		return nil, err
	}
	selector := selectorOf(wl, live)
	if len(selector) == 0 {
		return nil, fmt.Errorf("workload %s has no matchLabels selector to identify its pods", wl.GetName())
	}
	pod := &unstructured.Unstructured{}
	pod.SetAPIVersion("v1")
	pod.SetKind("Pod")
	if err := cli.Get(ctx, client.ObjectKey{Namespace: namespace, Name: podName}, pod); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, ErrPodNotFound
		}
		return nil, fmt.Errorf("get pod %s: %w", podName, err)
	}
	labels := pod.GetLabels()
	for k, want := range selector {
		if labels[k] != want {
			return nil, ErrPodNotFound
		}
	}
	return pod, nil
}

// RestartPod restarts one pod by deleting it so its controller recreates it
// (design §10).
func RestartPod(ctx context.Context, cli client.Client, app, namespace string, manifests []unstructured.Unstructured, podName string) error {
	pod, err := appPod(ctx, cli, app, namespace, manifests, podName)
	if err != nil {
		return err
	}
	if err := cli.Delete(ctx, pod); err != nil {
		return fmt.Errorf("delete pod %s: %w", podName, err)
	}
	return nil
}

// ForceDeletePod deletes the pod with a zero grace period, for pods stuck in
// Terminating (typically an unreachable node): the API server drops the object
// immediately instead of waiting for kubelet to confirm shutdown.
func ForceDeletePod(ctx context.Context, cli client.Client, app, namespace string, manifests []unstructured.Unstructured, podName string) error {
	pod, err := appPod(ctx, cli, app, namespace, manifests, podName)
	if err != nil {
		return err
	}
	if err := cli.Delete(ctx, pod, client.GracePeriodSeconds(0)); err != nil {
		return fmt.Errorf("force delete pod %s: %w", podName, err)
	}
	return nil
}
