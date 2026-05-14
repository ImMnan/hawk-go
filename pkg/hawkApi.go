package pkg

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	stdsync "sync"
	"time"

	"k8s.io/client-go/kubernetes"
)

type gitLastCommitResponse struct {
	CommitId string `json:"commit_id"`
}

const (
	commitWaitPollInterval   = 60 * time.Second
	commitWaitDefaultTimeout = 30 * time.Minute
)

func resolveAPIServerEndpoint(syncCfg SyncConfig) string {
	serviceName := strings.TrimSpace(syncCfg.APIServers.Connection.SvcName)
	port := syncCfg.APIServers.Connection.Port

	if serviceName != "" {
		if strings.Contains(serviceName, ":") {
			return serviceName
		}
		if port <= 0 {
			slog.Warn("apiServer.connection.port missing/invalid, defaulting to 80", "serviceName", serviceName)
			port = 80
		}
		return fmt.Sprintf("%s:%d", serviceName, port)

	}
	slog.Warn("using static API endpoint fallback", "endpoint", "hawk.k8s.net:80")
	return "hawk.k8s.net:80"
}

func getLastCommitId(sourceName, apiServerEndpoint string) (string, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest("GET", fmt.Sprintf("http://%s/api/document/getlastcommitid/%v", apiServerEndpoint, sourceName), nil)
	if err != nil {
		return "", fmt.Errorf("build get last commit request: %w", err)
	}
	slog.Debug("requesting last commit id", "source", sourceName, "endpoint", apiServerEndpoint)

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request get last commit id source=%s endpoint=%s: %w", sourceName, apiServerEndpoint, err)
	}
	defer resp.Body.Close()

	bodyText, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read get last commit response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("get last commit id failed status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(bodyText)))
	}

	slog.Debug("received last commit response", "source", sourceName, "status", resp.StatusCode, "body", strings.TrimSpace(string(bodyText)))
	var gitResponse gitLastCommitResponse
	if err := json.Unmarshal(bodyText, &gitResponse); err != nil {
		return "", fmt.Errorf("parse get last commit response: %w", err)
	}

	commitId := strings.TrimSpace(gitResponse.CommitId)
	if commitId == "" {
		return "", fmt.Errorf("get last commit id returned empty commit id for source=%s", sourceName)
	}

	slog.Info("last commit id resolved", "source", sourceName, "commitId", commitId)
	return commitId, nil
}

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
				slog.Debug("waiting for commit update", "source", meta.SourceName, "job", meta.JobName, "targetCommit", meta.TargetCommit)
				if err != nil {
					lastPollErr = err
				} else if strings.TrimSpace(currentCommit) == strings.TrimSpace(meta.TargetCommit) {
					slog.Info("commit update observed", "source", meta.SourceName, "job", meta.JobName, "commit", meta.TargetCommit)
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
