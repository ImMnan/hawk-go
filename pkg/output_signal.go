package pkg

import (
	"context"
	"fmt"
	"strings"
	stdsync "sync"
	"time"

	"k8s.io/client-go/kubernetes"
)

const (
	commitWaitPollInterval   = 2 * time.Second
	commitWaitDefaultTimeout = 30 * time.Minute
)

func waitForCommitUpdates(launches []jobLaunchMetadata, apiServerEndpoint string, clientSet *kubernetes.Clientset) error {
	timeout := commitWaitDefaultTimeout
	errCh := make(chan error, len(launches))
	var wg stdsync.WaitGroup
	waitCount := 0

	for _, launch := range launches {
		meta := launch
		if strings.TrimSpace(meta.TargetCommit) == "" {
			continue
		}
		waitCount++
		wg.Add(1)
		go func() {
			defer wg.Done()

			deadline := time.Now().Add(timeout)
			var lastPollErr error
			for {
				currentCommit, err := getLastCommitId(meta.SourceName, apiServerEndpoint)
				if err != nil {
					lastPollErr = err
				} else if strings.TrimSpace(currentCommit) == strings.TrimSpace(meta.TargetCommit) {
					fmt.Printf("commit update observed for source=%s job=%s commit=%s\n", meta.SourceName, meta.JobName, meta.TargetCommit)
					if err := cleanupJobResources(context.Background(), clientSet, meta); err != nil {
						errCh <- err
					}
					return
				}

				if time.Now().After(deadline) {
					if lastPollErr != nil {
						errCh <- fmt.Errorf("timeout waiting for commit update for source=%s job=%s targetCommit=%s: %w", meta.SourceName, meta.JobName, meta.TargetCommit, lastPollErr)
						return
					}
					errCh <- fmt.Errorf("timeout waiting for commit update for source=%s job=%s targetCommit=%s", meta.SourceName, meta.JobName, meta.TargetCommit)
					return
				}

				time.Sleep(commitWaitPollInterval)
			}
		}()
	}

	if waitCount == 0 {
		return nil
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			return err
		}
	}

	return nil
}
