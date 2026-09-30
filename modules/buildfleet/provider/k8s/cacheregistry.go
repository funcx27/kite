package k8s

import (
	"context"
	"fmt"
	"log/slog"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	bfresource "github.com/funcx27/kite/modules/buildfleet/resource"
)

const (
	cacheRegistryName = "buildfleet-cache-registry"
	cacheRepoPath     = "buildfleet-cache"
)

// CacheBaseRef is the cache base (registry host + repo prefix); the
// per-project path is appended by the fleet layer. Empty when no cache
// registry is configured.
func (p *Provider) CacheBaseRef() string {
	if p.cfg.CacheRegistry == nil {
		return ""
	}
	return fmt.Sprintf("%s.%s.svc:5000/%s", cacheRegistryName, p.cfg.Namespace, cacheRepoPath)
}

// CacheHost is the registry host to declare as plain http in
// buildkitd.toml (buildkitd reaches it over cluster DNS).
func (p *Provider) CacheHost() string {
	return CacheRegistryHost(p.cfg.Namespace)
}

// CacheRegistryHost derives the cache registry host for a namespace
// without a provider instance (config wiring renders it into the shared
// buildkitd.toml before providers are constructed).
func CacheRegistryHost(namespace string) string {
	return fmt.Sprintf("%s.%s.svc:5000", cacheRegistryName, namespace)
}

// EnsureCacheRegistry deploys the layer-cache registry (PVC + Deployment
// + Service) idempotently: existing objects are adopted as-is, so a
// daemon restart reuses the accumulated cache. Call once at startup.
func (p *Provider) EnsureCacheRegistry(ctx context.Context) error {
	if p.cfg.CacheRegistry == nil {
		return nil
	}
	c := p.cfg.CacheRegistry
	// All fields are required: defaults live in the embedded config
	// (providerDefaults.k8s.cache); an empty one here is a wiring bug
	// and must fail loudly rather than fall back silently.
	if c.Image == "" || c.Size == "" || c.GCSchedule == "" {
		return fmt.Errorf("k8s cache registry: image/size/gcSchedule all required (configure providerDefaults.k8s.cache)")
	}
	image := c.Image
	size := c.Size
	qty, err := bfresource.ParseQuantity(size)
	if err != nil {
		return fmt.Errorf("k8s cache registry: size %q: %w", size, err)
	}
	cs := p.cfg.Clientset.CoreV1()

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: cacheRegistryName},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: *resource.NewQuantity(int64(qty), resource.BinarySI)},
			},
		},
	}
	if c.StorageClass != "" {
		pvc.Spec.StorageClassName = &c.StorageClass
	}
	if _, err := cs.PersistentVolumeClaims(p.cfg.Namespace).
		Create(ctx, pvc, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("k8s cache registry: create pvc: %w", err)
	}

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: cacheRegistryName, Labels: map[string]string{"buildfleet": "cache-registry"}},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": cacheRegistryName}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": cacheRegistryName, "buildfleet": "cache-registry"}},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "registry",
						Image: image,
						Env: []corev1.EnvVar{
							{Name: "REGISTRY_LOG_LEVEL", Value: "warn"},
							{Name: "REGISTRY_STORAGE_DELETE_ENABLED", Value: "true"},
						},
						Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 5000}},
						// Registry is a lightweight sidecar; bound it so it
						// never competes with buildkitd instances.
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    *resource.NewMilliQuantity(100, resource.DecimalSI),
								corev1.ResourceMemory: *resource.NewQuantity(128*1024*1024, resource.BinarySI),
							},
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:    *resource.NewMilliQuantity(1000, resource.DecimalSI),
								corev1.ResourceMemory: *resource.NewQuantity(512*1024*1024, resource.BinarySI),
							},
						},
						VolumeMounts: []corev1.VolumeMount{{
							Name: "data", MountPath: "/var/lib/registry",
						}},
					}},
					Volumes: []corev1.Volume{{
						Name: "data",
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: cacheRegistryName},
						},
					}},
				},
			},
		},
	}
	if _, err := p.cfg.Clientset.AppsV1().Deployments(p.cfg.Namespace).
		Create(ctx, dep, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("k8s cache registry: create deployment: %w", err)
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: cacheRegistryName},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": cacheRegistryName},
			Ports:    []corev1.ServicePort{{Name: "http", Port: 5000, TargetPort: intstr.FromString("http")}},
		},
	}
	if _, err := cs.Services(p.cfg.Namespace).Create(ctx, svc, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("k8s cache registry: create service: %w", err)
	}
	if err := p.ensureCacheGC(ctx, c.GCSchedule); err != nil {
		return err
	}
	slog.Info("k8s cache registry ensured", "name", cacheRegistryName, "namespace", p.cfg.Namespace, "cacheBase", p.CacheBaseRef())
	return nil
}

// ensureCacheGC deploys the garbage-collect CronJob. Retention: the
// latest cache chain per project; everything unreferenced by a tagged
// manifest is reclaimed. "off" skips deployment.
func (p *Provider) ensureCacheGC(ctx context.Context, schedule string) error {
	if schedule == "off" {
		return nil
	}
	if schedule == "" {
		return fmt.Errorf("k8s cache gc: schedule required (configure providerDefaults.k8s.cache.gcSchedule)")
	}
	gcConfig := `version: 0.1
storage:
  filesystem:
    rootdirectory: /var/lib/registry
  delete:
    enabled: true
`
	cmName := cacheRegistryName + "-gc-config"
	if _, err := p.cfg.Clientset.CoreV1().ConfigMaps(p.cfg.Namespace).
		Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: cmName},
			Data:       map[string]string{"config.yml": gcConfig},
		}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("k8s cache gc: create config: %w", err)
	}

	cj := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: cacheRegistryName + "-gc"},
		Spec: batchv1.CronJobSpec{
			Schedule:          schedule,
			ConcurrencyPolicy: batchv1.ForbidConcurrent,
			JobTemplate: batchv1.JobTemplateSpec{
				Spec: batchv1.JobSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							RestartPolicy: corev1.RestartPolicyOnFailure,
							Containers: []corev1.Container{{
								Name:    "gc",
								Image:   p.cfg.CacheRegistry.Image,
								Command: []string{"registry", "garbage-collect", "--delete-untagged", "/gc/config.yml"},
								VolumeMounts: []corev1.VolumeMount{
									{Name: "data", MountPath: "/var/lib/registry"},
									{Name: "config", MountPath: "/gc", ReadOnly: true},
								},
							}},
							Volumes: []corev1.Volume{
								{
									Name: "data",
									VolumeSource: corev1.VolumeSource{
										PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: cacheRegistryName},
									},
								},
								{
									Name: "config",
									VolumeSource: corev1.VolumeSource{
										ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: cmName}},
									},
								},
							},
						},
					},
				},
			},
		},
	}
	if _, err := p.cfg.Clientset.BatchV1().CronJobs(p.cfg.Namespace).
		Create(ctx, cj, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("k8s cache gc: create cronjob: %w", err)
	}
	slog.Info("k8s cache gc scheduled", "schedule", schedule)
	return nil
}
