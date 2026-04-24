package pkg

import "fmt"

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

func kubernetesController() (chan SourceResult, error) {
	/*
		Initialize Kubernetes client and start the job controller goroutine
		- Read the sync.template file and store it in memory for later use.
		- Process each source result and create appropriate kubernetes jobs
	*/

	resultQueue := make(chan SourceResult, 16)
	go kubernetesControllerListener(resultQueue)
	return resultQueue, nil
}

func kubernetesControllerListener(resultQueue <-chan SourceResult) {
	/*
	   - Main function that handles kubernetes resources and workloads.
	   - Processes each source result and creates kubernetes jobs
	*/
	for result := range resultQueue {
		if err := createKubernetesJob(result); err != nil {
			fmt.Printf("failed to create kubernetes job for source %s: %v\n", result.Name, err)
		}
	}
}

func createKubernetesJob(result SourceResult) error {

	// based on the job template, run the job with the specified container and volume mounts.
	if result.Err != nil {
		fmt.Printf("source %s (%s) had fetch error, skipping job creation: %v\n", result.Name, result.Type, result.Err)
		return nil
	}

	fmt.Printf("kubernetes job received: source=%s type=%s gitDiff=%v confluenceDiff=%v\n", result.Name, result.Type, result.GitDiff != nil, result.ConfluenceDiff != nil)
	return nil
}

func waitForJobTrigger() {

	/*
		Once the git
	*/

}
