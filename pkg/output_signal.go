package pkg

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	stdsync "sync"
	"time"
)

const (
	doneWaitPollInterval   = 1 * time.Second
	doneWaitDefaultTimeout = 30 * time.Minute
)

func buildOutputFilePath(result SourceResult) string {
	resolvedSharedPath := strings.TrimSpace(os.ExpandEnv(result.SharedVolumePath))
	resolvedSourceName := strings.TrimSpace(result.Name)
	commitID := ""
	if result.GitDiff != nil {
		commitID = strings.TrimSpace(result.GitDiff.TargetCommit)
	}

	resolvedCommitID := strings.TrimSpace(commitID)
	if resolvedSharedPath == "" || resolvedSourceName == "" || resolvedCommitID == "" {
		return ""
	}

	return filepath.Join(resolvedSharedPath, resolvedSourceName, resolvedCommitID, "output.json")
}

func waitForOutputFiles(launches []jobLaunchMetadata) error {
	timeout := doneWaitDefaultTimeout
	errCh := make(chan error, len(launches))
	var wg stdsync.WaitGroup

	for _, launch := range launches {
		meta := launch
		wg.Add(1)
		go func() {
			defer wg.Done()

			outputPath := strings.TrimSpace(meta.OutputFilePath)
			if outputPath == "" {
				errCh <- fmt.Errorf("missing output file path for source %s", meta.SourceName)
				return
			}

			deadline := time.Now().Add(timeout)
			var lastReadErr error
			for {
				if _, err := os.Stat(outputPath); err == nil {
					if _, err := readOutputPayload(outputPath); err != nil {
						lastReadErr = err
					} else {
						fmt.Printf("output file ready for source=%s job=%s path=%s\n", meta.SourceName, meta.JobName, outputPath)
						return
					}
				}

				if time.Now().After(deadline) {
					if lastReadErr != nil {
						errCh <- fmt.Errorf("timeout waiting for readable output file for source=%s job=%s path=%s: %w", meta.SourceName, meta.JobName, outputPath, lastReadErr)
						return
					}
					errCh <- fmt.Errorf("timeout waiting for output file for source=%s job=%s path=%s", meta.SourceName, meta.JobName, outputPath)
					return
				}

				time.Sleep(doneWaitPollInterval)
			}
		}()
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

func postOutputFiles(launches []jobLaunchMetadata, apiServerEndpoint string) error {
	failures := make([]string, 0)

	for _, launch := range launches {
		payload, err := readOutputPayload(launch.OutputFilePath)
		if err != nil {
			failures = append(failures, fmt.Sprintf("source=%s job=%s read error: %v", launch.SourceName, launch.JobName, err))
			continue
		}

		if err := postResultToHawkAPI(payload, apiServerEndpoint); err != nil {
			failures = append(failures, fmt.Sprintf("source=%s job=%s post error: %v", launch.SourceName, launch.JobName, err))
			continue
		}

		fmt.Printf("output file posted for source=%s job=%s path=%s\n", launch.SourceName, launch.JobName, launch.OutputFilePath)
	}

	if len(failures) > 0 {
		return fmt.Errorf("failed to post %d/%d output payloads: %s", len(failures), len(launches), strings.Join(failures, "; "))
	}

	return nil
}

func readOutputPayload(outputPath string) ([]byte, error) {
	raw, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, fmt.Errorf("read output file %s: %w", outputPath, err)
	}

	return raw, nil
}
