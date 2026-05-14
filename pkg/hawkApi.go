package pkg

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type gitLastCommitResponse struct {
	CommitId string `json:"commit_id"`
}

func resolveAPIServerEndpoint(syncCfg SyncConfig) string {
	serviceName := strings.TrimSpace(syncCfg.APIServers.Connection.SvcName)
	port := syncCfg.APIServers.Connection.Port

	if serviceName != "" {
		if strings.Contains(serviceName, ":") {
			return serviceName
		}
		if port <= 0 {
			fmt.Printf("[WARNING] apiServer.connection.port missing/invalid for serviceName=%s, defaulting to 80\n", serviceName)
			port = 80
		}
		return fmt.Sprintf("%s:%d", serviceName, port)

	}
	fmt.Println("[WARNING] Using static endpoint: hawk.k8s.net:80 (see hawk-go/pkg/hawkApi.go)")
	return "hawk.k8s.net:80"
}

func getLastCommitId(sourceName, apiServerEndpoint string) (string, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest("GET", fmt.Sprintf("http://%s/api/document/getlastcommitid/%v", apiServerEndpoint, sourceName), nil)
	if err != nil {
		return "", fmt.Errorf("build get last commit request: %w", err)
	}
	fmt.Printf("hitting API endpoint: %v\n", req)

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

	fmt.Printf("%s\n", bodyText)
	var gitResponse gitLastCommitResponse
	if err := json.Unmarshal(bodyText, &gitResponse); err != nil {
		return "", fmt.Errorf("parse get last commit response: %w", err)
	}

	commitId := strings.TrimSpace(gitResponse.CommitId)
	if commitId == "" {
		return "", fmt.Errorf("get last commit id returned empty commit id for source=%s", sourceName)
	}

	fmt.Printf("Last commit id for %v is %v\n", sourceName, commitId)
	return commitId, nil
}
