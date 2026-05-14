package pkg

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
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

	templatePVC, err := loadPVCTemplate(syncCfg.PVCTemplate)
	if err != nil {
		return nil, err
	}

	resultQueue := make(chan SourceResult, 16)
	go kubernetesControllerListener(resultQueue, syncCfg, templateJob, templatePVC)
	return resultQueue, nil
}

func kubernetesControllerListener(resultQueue <-chan SourceResult, syncCfg SyncConfig, templateJob *batchv1.Job, templatePVC *corev1.PersistentVolumeClaim) {
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
		if _, err := createKubernetesJob(result, syncCfg, templateJob, templatePVC, clientSet); err != nil {
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

func loadPVCTemplate(templatePath string) (*corev1.PersistentVolumeClaim, error) {
	path := strings.TrimSpace(templatePath)
	if path == "" {
		return nil, fmt.Errorf("sync.pvc-template is required")
	}

	templateBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read pvc template %s: %w", path, err)
	}

	var pvc corev1.PersistentVolumeClaim
	if err := yaml.Unmarshal(templateBytes, &pvc); err != nil {
		return nil, fmt.Errorf("parse pvc template %s: %w", path, err)
	}

	if pvc.Spec.StorageClassName == nil || strings.TrimSpace(*pvc.Spec.StorageClassName) == "" {
		return nil, fmt.Errorf("pvc template %s must include spec.storageClassName", path)
	}

	if len(pvc.Spec.AccessModes) == 0 {
		return nil, fmt.Errorf("pvc template %s must include at least one access mode", path)
	}

	if pvc.Spec.Resources.Requests.Storage().IsZero() {
		return nil, fmt.Errorf("pvc template %s must include storage request", path)
	}

	return &pvc, nil
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
	PVCName      string
	TargetCommit string
}

func createKubernetesJob(result SourceResult, syncCfg SyncConfig, templateJob *batchv1.Job, templatePVC *corev1.PersistentVolumeClaim, clientSet *kubernetes.Clientset) (jobLaunchMetadata, error) {
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
	jobPVCName := jobName
	if len(jobPVCName) > 59 {
		jobPVCName = jobPVCName[:59]
	}
	jobPVCName += "-pvc"
	meta.PVCName = jobPVCName

	job := templateJob.DeepCopy()
	job.Namespace = namespace
	job.Name = jobName
	job.ResourceVersion = ""
	job.UID = ""
	job.CreationTimestamp = metav1.Time{}

	ctx := context.Background()
	sourceBinding, err := resolveRuntimeSourceVolumeBinding(ctx, clientSet, namespace, volumeName)
	if err != nil {
		return meta, err
	}
	if err := ensureJobPVCFromTemplate(ctx, clientSet, namespace, templatePVC, jobPVCName, jobName, result.Name); err != nil {
		return meta, err
	}

	if err := copySourceDataToJobPVC(ctx, clientSet, namespace, sourceBinding, jobPVCName, mountPath, jobName, result.Name); err != nil {
		return meta, err
	}

	job.Spec.Template.Spec.Volumes = upsertSharedVolumeClaim(job.Spec.Template.Spec.Volumes, volumeName, jobPVCName)

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
	pvcName := strings.TrimSpace(meta.PVCName)

	deletePolicy := metav1.DeletePropagationForeground
	if jobName != "" {
		if err := clientSet.BatchV1().Jobs(namespace).Delete(ctx, jobName, metav1.DeleteOptions{PropagationPolicy: &deletePolicy}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete kubernetes job %s/%s: %w", namespace, jobName, err)
		}
	}

	if pvcName != "" {
		if err := clientSet.CoreV1().PersistentVolumeClaims(namespace).Delete(ctx, pvcName, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete job pvc %s/%s: %w", namespace, pvcName, err)
		}
	}

	fmt.Printf("cleaned up kubernetes resources namespace=%s job=%s pvc=%s\n", namespace, jobName, pvcName)
	return nil
}

type sourceVolumeBinding struct {
	ClaimName string
	MountPath string
	NodeName  string
}

func resolveRuntimeSourceVolumeBinding(ctx context.Context, clientSet *kubernetes.Clientset, namespace, volumeName string) (sourceVolumeBinding, error) {
	podName := strings.TrimSpace(os.Getenv("HOSTNAME"))
	if podName == "" {
		return sourceVolumeBinding{}, fmt.Errorf("unable to resolve current pod name from HOSTNAME")
	}

	pod, err := clientSet.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return sourceVolumeBinding{}, fmt.Errorf("get current pod %s/%s: %w", namespace, podName, err)
	}

	claimName := ""
	for _, volume := range pod.Spec.Volumes {
		if volume.Name != volumeName || volume.PersistentVolumeClaim == nil {
			continue
		}
		claimName = strings.TrimSpace(volume.PersistentVolumeClaim.ClaimName)
		break
	}
	if claimName == "" {
		return sourceVolumeBinding{}, fmt.Errorf("current pod %s/%s does not include pvc-backed volume %q", namespace, podName, volumeName)
	}

	mountPath := ""
	for _, container := range pod.Spec.Containers {
		for _, vm := range container.VolumeMounts {
			if vm.Name != volumeName {
				continue
			}
			mountPath = strings.TrimSpace(vm.MountPath)
			break
		}
		if mountPath != "" {
			break
		}
	}
	if mountPath == "" {
		return sourceVolumeBinding{}, fmt.Errorf("current pod %s/%s does not mount volume %q", namespace, podName, volumeName)
	}

	return sourceVolumeBinding{
		ClaimName: claimName,
		MountPath: mountPath,
		NodeName:  strings.TrimSpace(pod.Spec.NodeName),
	}, nil
}

func ensureJobPVCFromTemplate(ctx context.Context, clientSet *kubernetes.Clientset, namespace string, templatePVC *corev1.PersistentVolumeClaim, jobPVCName, jobName, sourceName string) error {
	if templatePVC == nil {
		return fmt.Errorf("pvc template is required to create job pvc for source %s", sourceName)
	}

	jobPVC := templatePVC.DeepCopy()
	if jobPVC.Labels == nil {
		jobPVC.Labels = map[string]string{}
	}
	jobPVC.Name = jobPVCName
	jobPVC.Namespace = namespace
	//jobPVC.ResourceVersion = ""
	//jobPVC.UID = ""
	jobPVC.CreationTimestamp = metav1.Time{}
	jobPVC.Finalizers = nil
	jobPVC.Labels["app.kubernetes.io/managed-by"] = "hawk"
	jobPVC.Labels["hawk/source"] = strings.TrimSpace(sourceName)
	jobPVC.Labels["hawk/job"] = strings.TrimSpace(jobName)
	jobPVC.Spec.VolumeName = ""

	if _, err := clientSet.CoreV1().PersistentVolumeClaims(namespace).Create(ctx, jobPVC, metav1.CreateOptions{}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create job pvc %s from template: %w", jobPVCName, err)
		}
	}

	return nil
}

func copySourceDataToJobPVC(ctx context.Context, clientSet *kubernetes.Clientset, namespace string, source sourceVolumeBinding, targetPVCName, requestedPath, jobName, sourceName string) error {
	sourceClaim := strings.TrimSpace(source.ClaimName)
	if sourceClaim == "" {
		return fmt.Errorf("source pvc claim is required to stage files for source %s", sourceName)
	}

	requested := strings.TrimSpace(requestedPath)
	runtimeMount := strings.TrimSpace(source.MountPath)
	if requested == "" {
		requested = runtimeMount
	}

	relPath := "."
	if requested != runtimeMount {
		prefix := strings.TrimSuffix(runtimeMount, "/") + "/"
		if !strings.HasPrefix(requested, prefix) {
			return fmt.Errorf("shared volume path %q must be within runtime mount path %q", requested, runtimeMount)
		}
		relPath = strings.TrimPrefix(requested, prefix)
		relPath = path.Clean(relPath)
		if relPath == "." {
			relPath = "."
		}
	}

	copyJobName := jobName + "-seed"
	if len(copyJobName) > 63 {
		copyJobName = copyJobName[:63]
	}

	copyScript := fmt.Sprintf("set -eu; mkdir -p /target; SRC=/source/%s; [ -d \"$SRC\" ] || exit 0; cp -a \"$SRC\"/. /target/", relPath)

	backoff := int32(0)
	copyJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      copyJobName,
			Namespace: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "hawk",
				"hawk/source":                  strings.TrimSpace(sourceName),
				"hawk/job":                     strings.TrimSpace(jobName),
				"hawk/purpose":                 "pvc-seed",
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoff,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					NodeName:      strings.TrimSpace(source.NodeName),
					Containers: []corev1.Container{
						{
							Name:    "seed",
							Image:   "busybox:1.36",
							Command: []string{"sh", "-c", copyScript},
							VolumeMounts: []corev1.VolumeMount{
								{Name: "source", MountPath: "/source", ReadOnly: true},
								{Name: "target", MountPath: "/target"},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name:         "source",
							VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: sourceClaim, ReadOnly: true}},
						},
						{
							Name:         "target",
							VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: targetPVCName}},
						},
					},
				},
			},
		},
	}

	if _, err := clientSet.BatchV1().Jobs(namespace).Create(ctx, copyJob, metav1.CreateOptions{}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create pvc seed job %s/%s: %w", namespace, copyJobName, err)
		}
	}

	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if err := waitForJobTerminalState(waitCtx, clientSet, namespace, copyJobName); err != nil {
		return fmt.Errorf("wait for pvc seed job %s/%s: %w", namespace, copyJobName, err)
	}

	deletePolicy := metav1.DeletePropagationForeground
	if err := clientSet.BatchV1().Jobs(namespace).Delete(context.Background(), copyJobName, metav1.DeleteOptions{PropagationPolicy: &deletePolicy}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete pvc seed job %s/%s: %w", namespace, copyJobName, err)
	}

	return nil
}

func waitForJobTerminalState(ctx context.Context, clientSet *kubernetes.Clientset, namespace, jobName string) error {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		job, err := clientSet.BatchV1().Jobs(namespace).Get(ctx, jobName, metav1.GetOptions{})
		if err != nil {
			return err
		}

		for _, cond := range job.Status.Conditions {
			if cond.Type == batchv1.JobComplete && cond.Status == corev1.ConditionTrue {
				return nil
			}
			if cond.Type == batchv1.JobFailed && cond.Status == corev1.ConditionTrue {
				return fmt.Errorf("seed job failed: %s", strings.TrimSpace(cond.Message))
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
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
