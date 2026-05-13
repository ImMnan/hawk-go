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

// This list of functions is responsible for maintaining the kubernetes objects and workloads required by the hawk project.

/*

- this should start/initialize a container (Specified) as a job
- every object should have an individual job
- this should mount the volume as (configured) in the sources.sharedVolume.path
- The volume for these mounts should be persisted and shared with the hawk app.
- The job should be scheduled to run at the same time as handler.Fetch() is run.
- So the kubernetes controller can trigger job + pod creation concurrently,
- By the time git.go processes the data and saves it to the shared volume, the job should be up and running, and the container should be able to read the data from the shared volume and process it.
- Start the child container with specific env variables, so it knows certain things.
-
*/

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
	templateJob, err := loadJobTemplate(syncCfg.Template)
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
		return nil, fmt.Errorf("sync.template is required")
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
	SharedVolumePath string                `json:"sharedVolumePath"`
	GitDiff          *gitDiffResult        `json:"gitDiff,omitempty"`
	ConfluenceDiff   *confluenceDiffResult `json:"confluenceDiff,omitempty"`
}

type jobLaunchMetadata struct {
	SourceName     string
	JobName        string
	OutputFilePath string
}

func createKubernetesJob(result SourceResult, syncCfg SyncConfig, templateJob *batchv1.Job, clientSet *kubernetes.Clientset) (jobLaunchMetadata, error) {
	meta := jobLaunchMetadata{
		SourceName:     strings.TrimSpace(result.Name),
		OutputFilePath: buildOutputFilePath(result),
	}
	if strings.TrimSpace(meta.OutputFilePath) == "" {
		return meta, fmt.Errorf("missing output file path for source %s (requires source name, shared volume path, and git target commit)", result.Name)
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

	// Build the SOURCE_RESULT env variable payload.
	payload := sourceResultPayload{
		Name:             result.Name,
		Type:             result.Type,
		SharedVolumePath: result.SharedVolumePath,
		GitDiff:          result.GitDiff,
		ConfluenceDiff:   result.ConfluenceDiff,
	}
	resultJSON, err := json.Marshal(payload)
	if err != nil {
		return meta, fmt.Errorf("marshal source result for %s: %w", result.Name, err)
	}

	mountPath := result.SharedVolumePath
	if mountPath == "" {
		mountPath = "/data"
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

	job := templateJob.DeepCopy()
	job.Namespace = namespace
	job.Name = jobName
	job.ResourceVersion = ""
	job.UID = ""
	job.CreationTimestamp = metav1.Time{}

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

	if mountPath != "" {
		container.VolumeMounts = upsertSharedVolumeMount(container.VolumeMounts, mountPath)
	}

	if job.Spec.BackoffLimit == nil {
		backoff := int32(10)
		job.Spec.BackoffLimit = &backoff
	}

	if job.Spec.Template.Spec.RestartPolicy == "" {
		job.Spec.Template.Spec.RestartPolicy = corev1.RestartPolicyOnFailure
	}

	ctx := context.Background()
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

func upsertEnv(envs []corev1.EnvVar, env corev1.EnvVar) []corev1.EnvVar {
	for i := range envs {
		if envs[i].Name == env.Name {
			envs[i].Value = env.Value
			return envs
		}
	}
	return append(envs, env)
}

func upsertSharedVolumeMount(mounts []corev1.VolumeMount, mountPath string) []corev1.VolumeMount {
	sharedVolumeName := os.Getenv("SHARED_VOLUME_NAME")
	if sharedVolumeName == "" {
		sharedVolumeName = "hawk-shared-volume"
	}
	for i := range mounts {
		if mounts[i].Name == sharedVolumeName {
			mounts[i].MountPath = mountPath
			return mounts
		}
	}
	return append(mounts, corev1.VolumeMount{
		Name:      sharedVolumeName,
		MountPath: mountPath,
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
