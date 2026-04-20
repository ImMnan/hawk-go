package pkg

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDebugGitGetLatestCommitFromConfig(t *testing.T) {
	cfgPath := filepath.Clean("../configlist.yaml")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("failed to read %s: %v", cfgPath, err)
	}

	var confList ConfList
	if err := yaml.Unmarshal(data, &confList); err != nil {
		t.Fatalf("failed to parse config list: %v", err)
	}

	gitCfg, found := firstGitConfig(confList)
	if !found {
		t.Fatalf("no git source found in config")
	}

	payload, err := gitGetLatestCommit(gitCfg)
	if err != nil {
		t.Fatalf("gitGetLatestCommit failed: %v", err)
	}

	var snapshot gitCommitSnapshot
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		t.Fatalf("failed to parse snapshot json: %v", err)
	}

	if snapshot.CommitSHA == "" {
		t.Fatalf("snapshot commitSha is empty")
	}

	t.Logf("repo=%s branch=%s commit=%s files=%d", snapshot.RepoURL, snapshot.Branch, snapshot.CommitSHA, len(snapshot.Files))
}

func firstGitConfig(confList ConfList) (GitCfg, bool) {
	for _, c := range confList {
		for _, src := range c.Sync.Sources {
			if src.Git != nil {
				return *src.Git, true
			}
		}
	}

	return GitCfg{}, false
}
