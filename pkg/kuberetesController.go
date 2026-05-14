package pkg

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/yaml"
)

func kubernetesInit() (*kubernetes.Clientset, error) {
	// initialize kubernetes client and return the clientset
	// Use in-cluster service account credentials mounted into the pod.
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("load in-cluster kubernetes config: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("create kubernetes clientset: %w", err)
	}

	return clientset, nil

}

func kubernetesController(syncCfg SyncConfig) (chan SourceResult, error) {
	/*
		Initialize Kubernetes client and start the job controller goroutine
		- Read the sync.template file and store it in memory for later use.
		- Process each source result and create appropriate kubernetes jobs
	*/
	templateJob, err := loadJobTemplate(syncCfg.JobTemplate)
	if err != nil {
		return nil, err
	}

	resultQueue := make(chan SourceResult, 16)
	go kubernetesControllerListener(resultQueue, syncCfg, templateJob)
	return resultQueue, nil
}

func kubernetesControllerListener(resultQueue <-chan SourceResult, syncCfg SyncConfig, templateJob *batchv1.Job) {
	/*
	   - Main function that handles kubernetes resources and workloads.
	   - Processes each source result and creates kubernetes jobs
	*/
	clientSet, err := kubernetesInit()
	if err != nil {
		fmt.Printf("failed to initialize kubernetes client: %v\n", err)
		return
	}

	for result := range resultQueue {
		if _, err := createKubernetesJob(result, syncCfg, templateJob, clientSet); err != nil {
			fmt.Printf("failed to create kubernetes job for source %s: %v\n", result.Name, err)
		}
	}
}

func loadJobTemplate(templatePath string) (*batchv1.Job, error) {
	path := strings.TrimSpace(templatePath)
	if path == "" {
		return nil, fmt.Errorf("sync.job-template is required")
	}

	templateBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read job template %s: %w", path, err)
	}

	var job batchv1.Job
	if err := yaml.Unmarshal(templateBytes, &job); err != nil {
		return nil, fmt.Errorf("parse job template %s: %w", path, err)
	}

	if len(job.Spec.Template.Spec.Containers) == 0 {
		return nil, fmt.Errorf("job template %s must include at least one container", path)
	}

	return &job, nil
}

// sourceResultPayload is a JSON-serializable view of SourceResult used as the SOURCE_RESULT env variable.
type sourceResultPayload struct {
	Name             string                `json:"name"`
	Type             string                `json:"type"`
	TargetCommit     string                `json:"targetCommit,omitempty"`
	SharedVolumeName string                `json:"sharedVolumeName"`
	SharedVolumePath string                `json:"sharedVolumePath"`
	GitDiff          *gitDiffResult        `json:"gitDiff,omitempty"`
	ConfluenceDiff   *confluenceDiffResult `json:"confluenceDiff,omitempty"`
}

type jobLaunchMetadata struct {
	SourceName   string
	JobName      string
	Namespace    string
	TargetCommit string
}

func createKubernetesJob(result SourceResult, syncCfg SyncConfig, templateJob *batchv1.Job, clientSet *kubernetes.Clientset) (jobLaunchMetadata, error) {
	meta := jobLaunchMetadata{
		SourceName: strings.TrimSpace(result.Name),
	}
	if result.GitDiff != nil {
		meta.TargetCommit = strings.TrimSpace(result.GitDiff.TargetCommit)
	}

	if result.Err != nil {
		fmt.Printf("source %s (%s) had fetch error, skipping job creation: %v\n", result.Name, result.Type, result.Err)
		return meta, nil
	}

	namespace := os.Getenv("WORKING_NAMESPACE")
	if namespace == "" {
		namespace = "default"
	}

	image := strings.TrimSpace(syncCfg.Image)
	if image == "" {
		return meta, fmt.Errorf("sync.image is required for source %s", result.Name)
	}

	mountPath := result.SharedVolumePath
	if mountPath == "" {
		return meta, fmt.Errorf("shared volume path is required for source %s to create kubernetes job", result.Name)
	}

	volumeName := strings.TrimSpace(result.SharedVolumeName)
	if volumeName == "" {
		return meta, fmt.Errorf("shared volume name is required for source %s to create kubernetes job", result.Name)
	}

	baseJobName := strings.ToLower(strings.TrimSpace(result.Name))
	if baseJobName == "" {
		return meta, fmt.Errorf("source name is required to create kubernetes job")
	}
	baseJobName = strings.ReplaceAll(baseJobName, "_", "-")
	baseJobName = strings.ReplaceAll(baseJobName, " ", "-")
	if len(baseJobName) > 50 {
		baseJobName = baseJobName[:50]
	}
	jobName := fmt.Sprintf("%s-%d", baseJobName, time.Now().Unix())
	meta.JobName = jobName
	meta.Namespace = namespace

	job := templateJob.DeepCopy()
	job.Namespace = namespace
	job.Name = jobName
	job.ResourceVersion = ""
	job.UID = ""
	job.CreationTimestamp = metav1.Time{}

	ctx := context.Background()
	sharedClaimName := resolveSharedClaimName(job.Spec.Template.Spec.Volumes, volumeName)
	if sharedClaimName == "" {
		return meta, fmt.Errorf("job template must include at least one pvc-backed volume for source %s", result.Name)
	}

	if err := assertVolumePathMapped(job.Spec.Template.Spec.Containers[0].VolumeMounts, volumeName, mountPath); err != nil {
		return meta, err
	}

	job.Spec.Template.Spec.Volumes = upsertSharedVolumeClaim(job.Spec.Template.Spec.Volumes, volumeName, sharedClaimName)

	payload := sourceResultPayload{
		Name:             result.Name,
		Type:             result.Type,
		TargetCommit:     meta.TargetCommit,
		SharedVolumeName: result.SharedVolumeName,
		SharedVolumePath: result.SharedVolumePath,
		GitDiff:          result.GitDiff,
		ConfluenceDiff:   result.ConfluenceDiff,
	}
	resultJSON, err := json.Marshal(payload)
	if err != nil {
		return meta, fmt.Errorf("marshal source result for %s: %w", result.Name, err)
	}

	container := &job.Spec.Template.Spec.Containers[0]
	container.Image = image
	container.Name = baseJobName + "-ctr"
	if len(container.Name) > 63 {
		container.Name = container.Name[:63]
	}

	container.Env = upsertEnv(container.Env, corev1.EnvVar{
		Name:  "SOURCE_RESULT",
		Value: string(resultJSON),
	})
	container.Env = upsertEnv(container.Env, corev1.EnvVar{
		Name:  "SOURCE_SHARED_VOLUME_PATH",
		Value: result.SharedVolumePath,
	})
	container.Env = upsertEnv(container.Env, corev1.EnvVar{
		Name:  "SOURCE_SHARED_VOLUME_NAME",
		Value: result.SharedVolumeName,
	})
	container.Env = upsertEnv(container.Env, corev1.EnvVar{
		Name:  "SOURCE_TARGET_COMMIT",
		Value: meta.TargetCommit,
	})

	if mountPath != "" {
		container.VolumeMounts = upsertSharedVolumeMount(container.VolumeMounts, volumeName, mountPath)
	}

	if job.Spec.BackoffLimit == nil {
		backoff := int32(10)
		job.Spec.BackoffLimit = &backoff
	}

	if job.Spec.Template.Spec.RestartPolicy == "" {
		job.Spec.Template.Spec.RestartPolicy = corev1.RestartPolicyOnFailure
	}

	created, err := clientSet.BatchV1().Jobs(namespace).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return meta, fmt.Errorf("create kubernetes job %s: %w", jobName, err)
		}

		if delErr := clientSet.BatchV1().Jobs(namespace).Delete(ctx, jobName, metav1.DeleteOptions{}); delErr != nil && !apierrors.IsNotFound(delErr) {
			return meta, fmt.Errorf("delete existing kubernetes job %s: %w", jobName, delErr)
		}

		created, err = clientSet.BatchV1().Jobs(namespace).Create(ctx, job, metav1.CreateOptions{})
		if err != nil {
			return meta, fmt.Errorf("recreate kubernetes job %s: %w", jobName, err)
		}
	}

	fmt.Printf("kubernetes job created: %s/%s\n", namespace, created.Name)
	meta.JobName = created.Name
	go watchJobCompletion(clientSet, namespace, created.Name)
	return meta, nil
}

func cleanupJobResources(ctx context.Context, clientSet *kubernetes.Clientset, meta jobLaunchMetadata) error {
	namespace := strings.TrimSpace(meta.Namespace)
	jobName := strings.TrimSpace(meta.JobName)

	deletePolicy := metav1.DeletePropagationForeground
	if jobName != "" {
		if err := clientSet.BatchV1().Jobs(namespace).Delete(ctx, jobName, metav1.DeleteOptions{PropagationPolicy: &deletePolicy}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete kubernetes job %s/%s: %w", namespace, jobName, err)
		}
	}

	fmt.Printf("cleaned up kubernetes resources namespace=%s job=%s\n", namespace, jobName)
	return nil
}

func resolveSharedClaimName(volumes []corev1.Volume, volumeName string) string {
	preferred := strings.TrimSpace(volumeName)
	for _, volume := range volumes {
		if volume.Name != preferred || volume.PersistentVolumeClaim == nil {
			continue
		}
		if claim := strings.TrimSpace(volume.PersistentVolumeClaim.ClaimName); claim != "" {
			return claim
		}
	}

	for _, volume := range volumes {
		if volume.PersistentVolumeClaim == nil {
			continue
		}
		if claim := strings.TrimSpace(volume.PersistentVolumeClaim.ClaimName); claim != "" {
			return claim
		}
	}

	return ""
}

func assertVolumePathMapped(mounts []corev1.VolumeMount, volumeName, mountPath string) error {
	resolvedVolume := strings.TrimSpace(volumeName)
	resolvedMount := strings.TrimSpace(mountPath)
	for _, mount := range mounts {
		if mount.Name == resolvedVolume && strings.TrimSpace(mount.MountPath) == resolvedMount {
			return nil
		}
	}
	for _, mount := range mounts {
		if strings.TrimSpace(mount.MountPath) == resolvedMount {
			return nil
		}
	}
	return fmt.Errorf("job template must include mountPath %q for source shared volume", resolvedMount)
}

func upsertEnv(envs []corev1.EnvVar, env corev1.EnvVar) []corev1.EnvVar {
	for i := range envs {
		if envs[i].Name == env.Name {
			envs[i].Value = env.Value
			return envs
		}
	}
	return append(envs, env)
}

func upsertSharedVolumeMount(mounts []corev1.VolumeMount, volumeName, mountPath string) []corev1.VolumeMount {
	resolvedVolumeName := strings.TrimSpace(volumeName)
	resolvedMountPath := strings.TrimSpace(mountPath)

	for i := range mounts {
		if mounts[i].Name == resolvedVolumeName {
			mounts[i].MountPath = resolvedMountPath
			return mounts
		}
	}

	for i := range mounts {
		if mounts[i].MountPath == resolvedMountPath {
			mounts[i].Name = resolvedVolumeName
			return mounts
		}
	}

	return append(mounts, corev1.VolumeMount{
		Name:      resolvedVolumeName,
		MountPath: resolvedMountPath,
	})
}

func upsertSharedVolumeClaim(volumes []corev1.Volume, volumeName, claimName string) []corev1.Volume {
	for i := range volumes {
		if volumes[i].Name != volumeName {
			continue
		}
		volumes[i].PersistentVolumeClaim = &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claimName}
		return volumes
	}

	return append(volumes, corev1.Volume{
		Name: volumeName,
		VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claimName},
		},
	})
}

// watchJobCompletion watches the job until it succeeds or fails, logging the outcome.
func watchJobCompletion(clientSet *kubernetes.Clientset, namespace, jobName string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	watcher, err := clientSet.BatchV1().Jobs(namespace).Watch(ctx, metav1.ListOptions{
		FieldSelector: "metadata.name=" + jobName,
	})
	if err != nil {
		fmt.Printf("failed to watch job %s/%s: %v\n", namespace, jobName, err)
		return
	}
	defer watcher.Stop()

	for event := range watcher.ResultChan() {
		job, ok := event.Object.(*batchv1.Job)
		if !ok {
			continue
		}
		for _, cond := range job.Status.Conditions {
			if cond.Type == batchv1.JobComplete && cond.Status == corev1.ConditionTrue {
				fmt.Printf("job %s/%s completed successfully\n", namespace, jobName)
				return
			}
			if cond.Type == batchv1.JobFailed && cond.Status == corev1.ConditionTrue {
				fmt.Printf("job %s/%s failed: %s\n", namespace, jobName, cond.Message)
				return
			}
		}
	}
}
