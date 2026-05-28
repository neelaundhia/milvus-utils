package k8s

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sirupsen/logrus"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	helmaction "helm.sh/helm/v3/pkg/action"
	helmcli "helm.sh/helm/v3/pkg/cli"
)

// Well-known GVRs for custom resources used by the restore flow.
var (
	MilvusGVR = schema.GroupVersionResource{
		Group:    "milvus.io",
		Version:  "v1beta1",
		Resource: "milvuses",
	}
	FluxKustomizationGVR = schema.GroupVersionResource{
		Group:    "kustomize.toolkit.fluxcd.io",
		Version:  "v1",
		Resource: "kustomizations",
	}
	KedaScaledObjectGVR = schema.GroupVersionResource{
		Group:    "keda.sh",
		Version:  "v1alpha1",
		Resource: "scaledobjects",
	}
)

// Client wraps the Kubernetes typed and dynamic clients with the operations
// needed for snapshot restore.
type Client struct {
	typed   kubernetes.Interface
	dynamic dynamic.Interface
	log     *logrus.Logger
}

// NewClient creates a K8s client. It tries in-cluster config first,
// then falls back to the default kubeconfig (~/.kube/config or KUBECONFIG env).
func NewClient(log *logrus.Logger) (*Client, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		log.Debug("in-cluster config not available, falling back to kubeconfig")
		kubeconfig := os.Getenv("KUBECONFIG")
		if kubeconfig == "" {
			home, _ := os.UserHomeDir()
			kubeconfig = filepath.Join(home, ".kube", "config")
		}
		cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, fmt.Errorf("loading kubeconfig from %s: %w", kubeconfig, err)
		}
	}
	typed, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("creating typed client: %w", err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("creating dynamic client: %w", err)
	}
	return &Client{typed: typed, dynamic: dyn, log: log}, nil
}

// NewClientFromConfig creates a K8s client from an explicit rest.Config.
// Useful for testing or out-of-cluster usage.
func NewClientFromConfig(cfg *rest.Config, log *logrus.Logger) (*Client, error) {
	typed, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("creating typed client: %w", err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("creating dynamic client: %w", err)
	}
	return &Client{typed: typed, dynamic: dyn, log: log}, nil
}

// SuspendFlux patches the Flux Kustomization to set spec.suspend = true.
func (c *Client) SuspendFlux(ctx context.Context, name, namespace string) error {
	c.log.WithFields(logrus.Fields{"name": name, "namespace": namespace}).Debug("[DESTRUCTIVE] patching flux kustomization spec.suspend=true")
	patch := []byte(`{"spec":{"suspend":true}}`)
	_, err := c.dynamic.Resource(FluxKustomizationGVR).Namespace(namespace).Patch(
		ctx, name, types.MergePatchType, patch, metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("suspending flux kustomization %s/%s: %w", namespace, name, err)
	}
	c.log.WithFields(logrus.Fields{"name": name, "namespace": namespace}).Info("flux kustomization suspended")
	return nil
}

// ResumeFlux patches the Flux Kustomization to set spec.suspend = false.
func (c *Client) ResumeFlux(ctx context.Context, name, namespace string) error {
	patch := []byte(`{"spec":{"suspend":false}}`)
	_, err := c.dynamic.Resource(FluxKustomizationGVR).Namespace(namespace).Patch(
		ctx, name, types.MergePatchType, patch, metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("resuming flux kustomization %s/%s: %w", namespace, name, err)
	}
	c.log.WithFields(logrus.Fields{"name": name, "namespace": namespace}).Info("flux kustomization resumed")
	return nil
}

// GetMilvusCR reads the live Milvus CR as an unstructured object.
func (c *Client) GetMilvusCR(ctx context.Context, name, namespace string) (*unstructured.Unstructured, error) {
	obj, err := c.dynamic.Resource(MilvusGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting milvus CR %s/%s: %w", namespace, name, err)
	}
	return obj, nil
}

// PatchMilvusCR applies a JSON merge patch to the Milvus CR.
func (c *Client) PatchMilvusCR(ctx context.Context, name, namespace string, patch []byte) error {
	_, err := c.dynamic.Resource(MilvusGVR).Namespace(namespace).Patch(
		ctx, name, types.MergePatchType, patch, metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("patching milvus CR %s/%s: %w", namespace, name, err)
	}
	c.log.WithFields(logrus.Fields{"name": name, "namespace": namespace}).Info("milvus CR patched")
	return nil
}

// DeleteHPAs deletes all HPAs in the given namespace.
func (c *Client) DeleteHPAs(ctx context.Context, namespace string) error {
	hpas, err := c.typed.AutoscalingV2().HorizontalPodAutoscalers(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing HPAs in %s: %w", namespace, err)
	}
	c.log.WithFields(logrus.Fields{"namespace": namespace, "count": len(hpas.Items)}).Debug("[DESTRUCTIVE] deleting HPAs")
	for i := range hpas.Items {
		name := hpas.Items[i].Name
		if err := c.typed.AutoscalingV2().HorizontalPodAutoscalers(namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
			return fmt.Errorf("deleting HPA %s/%s: %w", namespace, name, err)
		}
		c.log.WithFields(logrus.Fields{"name": name, "namespace": namespace}).Info("HPA deleted")
	}
	return nil
}

// DeleteScaledObjects deletes all KEDA ScaledObjects in the given namespace.
func (c *Client) DeleteScaledObjects(ctx context.Context, namespace string) error {
	objs, err := c.dynamic.Resource(KedaScaledObjectGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing KEDA ScaledObjects in %s: %w", namespace, err)
	}
	c.log.WithFields(logrus.Fields{"namespace": namespace, "count": len(objs.Items)}).Debug("[DESTRUCTIVE] deleting KEDA ScaledObjects")
	for i := range objs.Items {
		name := objs.Items[i].GetName()
		if err := c.dynamic.Resource(KedaScaledObjectGVR).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
			return fmt.Errorf("deleting ScaledObject %s/%s: %w", namespace, name, err)
		}
		c.log.WithFields(logrus.Fields{"name": name, "namespace": namespace}).Info("KEDA ScaledObject deleted")
	}
	return nil
}

// DeleteMilvusCR deletes the Milvus CR using foreground propagation so the
// operator cascade-deletes all Milvus deployments and services before returning.
// Etcd is retained because the CR has deletionPolicy: Retain.
func (c *Client) DeleteMilvusCR(ctx context.Context, name, namespace string) error {
	c.log.WithFields(logrus.Fields{
		"name":      name,
		"namespace": namespace,
	}).Debug("[DESTRUCTIVE] deleting milvus CR")
	propagation := metav1.DeletePropagationForeground
	err := c.dynamic.Resource(MilvusGVR).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{
		PropagationPolicy: &propagation,
	})
	if err != nil {
		return fmt.Errorf("deleting milvus CR %s/%s: %w", namespace, name, err)
	}
	c.log.WithFields(logrus.Fields{"name": name, "namespace": namespace}).Info("milvus CR deleted")
	return nil
}

// CreateMilvusCR creates a Milvus CR from the given unstructured object.
func (c *Client) CreateMilvusCR(ctx context.Context, namespace string, obj *unstructured.Unstructured) error {
	c.log.WithFields(logrus.Fields{
		"name":      obj.GetName(),
		"namespace": namespace,
	}).Debug("creating milvus CR")
	_, err := c.dynamic.Resource(MilvusGVR).Namespace(namespace).Create(ctx, obj, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("creating milvus CR %s/%s: %w", namespace, obj.GetName(), err)
	}
	c.log.WithFields(logrus.Fields{"name": obj.GetName(), "namespace": namespace}).Info("milvus CR created")
	return nil
}

// DeleteEtcdResources uninstalls the etcd Helm release and deletes PVCs and
// their backing PVs using the provided label selector.
func (c *Client) DeleteEtcdResources(ctx context.Context, namespace, releaseName, labelSelector string) error {
	c.log.WithFields(logrus.Fields{
		"release":       releaseName,
		"namespace":     namespace,
		"labelSelector": labelSelector,
	}).Debug("[DESTRUCTIVE] uninstalling etcd helm release and deleting PVCs/PVs")

	// Uninstall the Helm release (deployed by the Milvus operator).
	if err := c.uninstallHelmRelease(namespace, releaseName); err != nil {
		return err
	}

	// Delete PVCs and their backing PVs.
	pvcs, err := c.typed.CoreV1().PersistentVolumeClaims(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return fmt.Errorf("listing etcd PVCs: %w", err)
	}
	for i := range pvcs.Items {
		pvc := &pvcs.Items[i]
		pvcName := pvc.Name

		// Delete backing PV if bound.
		if pvc.Spec.VolumeName != "" {
			if err := c.typed.CoreV1().PersistentVolumes().Delete(ctx, pvc.Spec.VolumeName, metav1.DeleteOptions{}); err != nil {
				c.log.WithError(err).WithField("pv", pvc.Spec.VolumeName).Warn("failed to delete PV (may already be gone)")
			} else {
				c.log.WithField("pv", pvc.Spec.VolumeName).Info("PV deleted")
			}
		}

		if err := c.typed.CoreV1().PersistentVolumeClaims(namespace).Delete(ctx, pvcName, metav1.DeleteOptions{}); err != nil {
			return fmt.Errorf("deleting etcd PVC %s: %w", pvcName, err)
		}
		c.log.WithFields(logrus.Fields{"name": pvcName, "namespace": namespace}).Info("etcd PVC deleted")
	}
	return nil
}

// uninstallHelmRelease uses the Helm SDK to uninstall a release by name.
func (c *Client) uninstallHelmRelease(namespace, releaseName string) error {
	settings := helmcli.New()
	settings.SetNamespace(namespace)

	actionConfig := new(helmaction.Configuration)
	if err := actionConfig.Init(settings.RESTClientGetter(), namespace, "secret", func(format string, v ...interface{}) {
		c.log.Debugf(format, v...)
	}); err != nil {
		return fmt.Errorf("initializing helm action config: %w", err)
	}

	uninstall := helmaction.NewUninstall(actionConfig)
	uninstall.Wait = true
	uninstall.Timeout = 5 * time.Minute

	_, err := uninstall.Run(releaseName)
	if err != nil {
		return fmt.Errorf("uninstalling helm release %s: %w", releaseName, err)
	}
	c.log.WithFields(logrus.Fields{"release": releaseName, "namespace": namespace}).Info("helm release uninstalled")
	return nil
}

// CreateTempPVC creates a temporary PVC for holding the etcd snapshot.
func (c *Client) CreateTempPVC(ctx context.Context, namespace, pvcName, storageClass string) error {
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName,
			Namespace: namespace,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: *parseQuantity("1Gi"),
				},
			},
		},
	}
	if storageClass != "" {
		pvc.Spec.StorageClassName = &storageClass
	}
	_, err := c.typed.CoreV1().PersistentVolumeClaims(namespace).Create(ctx, pvc, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("creating temp PVC %s: %w", pvcName, err)
	}
	c.log.WithFields(logrus.Fields{"name": pvcName, "namespace": namespace}).Info("temp PVC created")
	return nil
}

// CreateDownloadJob creates a Job that downloads the etcd snapshot from S3 to the temp PVC.
func (c *Client) CreateDownloadJob(ctx context.Context, namespace, jobName, pvcName, serviceAccount, image, s3URI, destPath string) error {
	backoffLimit := int32(3)
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: namespace,
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoffLimit,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					ServiceAccountName: serviceAccount,
					RestartPolicy:      corev1.RestartPolicyOnFailure,
					Containers: []corev1.Container{
						{
							Name:    "download",
							Image:   image,
							Command: []string{"aws", "s3", "cp", s3URI, destPath},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "snapshot",
									MountPath: "/snapshot",
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "snapshot",
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: pvcName,
								},
							},
						},
					},
				},
			},
		},
	}
	_, err := c.typed.BatchV1().Jobs(namespace).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("creating download job %s: %w", jobName, err)
	}
	c.log.WithFields(logrus.Fields{"name": jobName, "namespace": namespace}).Info("download job created")
	return nil
}

// WaitForJobComplete waits until the named Job reaches a Complete condition or fails.
func (c *Client) WaitForJobComplete(ctx context.Context, namespace, jobName string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	watcher, err := c.typed.BatchV1().Jobs(namespace).Watch(ctx, metav1.ListOptions{
		FieldSelector: fmt.Sprintf("metadata.name=%s", jobName),
	})
	if err != nil {
		return fmt.Errorf("watching job %s: %w", jobName, err)
	}
	defer watcher.Stop()

	for event := range watcher.ResultChan() {
		if event.Type == watch.Error {
			return fmt.Errorf("watch error for job %s", jobName)
		}
		job, ok := event.Object.(*batchv1.Job)
		if !ok {
			continue
		}
		for _, cond := range job.Status.Conditions {
			if cond.Type == batchv1.JobComplete && cond.Status == corev1.ConditionTrue {
				c.log.WithField("job", jobName).Info("download job completed")
				return nil
			}
			if cond.Type == batchv1.JobFailed && cond.Status == corev1.ConditionTrue {
				return fmt.Errorf("job %s failed: %s", jobName, cond.Message)
			}
		}
	}
	return fmt.Errorf("timed out waiting for job %s to complete", jobName)
}

// WaitForPodsTerminated waits until no pods with the given label selector are running.
func (c *Client) WaitForPodsTerminated(ctx context.Context, namespace, labelSelector string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for pods to terminate (selector: %s)", labelSelector)
		case <-ticker.C:
			pods, err := c.typed.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
				LabelSelector: labelSelector,
			})
			if err != nil {
				return fmt.Errorf("listing pods: %w", err)
			}
			if len(pods.Items) == 0 {
				c.log.WithField("selector", labelSelector).Info("all pods terminated")
				return nil
			}
			c.log.WithFields(logrus.Fields{
				"remaining": len(pods.Items),
				"selector":  labelSelector,
			}).Debug("waiting for pods to terminate")
		}
	}
}

// WaitForStatefulSetReady waits until the named StatefulSet has all replicas ready.
func (c *Client) WaitForStatefulSetReady(ctx context.Context, namespace, name string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	watcher, err := c.typed.AppsV1().StatefulSets(namespace).Watch(ctx, metav1.ListOptions{
		FieldSelector: fmt.Sprintf("metadata.name=%s", name),
	})
	if err != nil {
		return fmt.Errorf("watching statefulset %s: %w", name, err)
	}
	defer watcher.Stop()

	for event := range watcher.ResultChan() {
		if event.Type == watch.Error {
			return fmt.Errorf("watch error for statefulset %s", name)
		}
		sts, ok := event.Object.(*appsv1.StatefulSet)
		if !ok {
			continue
		}
		desired := int32(1)
		if sts.Spec.Replicas != nil {
			desired = *sts.Spec.Replicas
		}
		if sts.Status.ReadyReplicas >= desired && desired > 0 {
			c.log.WithFields(logrus.Fields{
				"name":     name,
				"replicas": desired,
			}).Info("statefulset ready")
			return nil
		}
	}
	return fmt.Errorf("timed out waiting for statefulset %s to be ready", name)
}

// DeleteJob deletes the named Job and its pods.
func (c *Client) DeleteJob(ctx context.Context, namespace, name string) error {
	propagation := metav1.DeletePropagationBackground
	err := c.typed.BatchV1().Jobs(namespace).Delete(ctx, name, metav1.DeleteOptions{
		PropagationPolicy: &propagation,
	})
	if err != nil {
		return fmt.Errorf("deleting job %s: %w", name, err)
	}
	c.log.WithFields(logrus.Fields{"name": name, "namespace": namespace}).Info("job deleted")
	return nil
}

// DeletePVC deletes the named PVC.
func (c *Client) DeletePVC(ctx context.Context, namespace, name string) error {
	err := c.typed.CoreV1().PersistentVolumeClaims(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		return fmt.Errorf("deleting PVC %s: %w", name, err)
	}
	c.log.WithFields(logrus.Fields{"name": name, "namespace": namespace}).Info("PVC deleted")
	return nil
}

// parseQuantity is a helper to create a resource.Quantity.
func parseQuantity(s string) *resource.Quantity {
	q := resource.MustParse(s)
	return &q
}
