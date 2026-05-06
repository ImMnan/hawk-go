package pkg

import (
	"encoding/json"
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

func buildDoneFilePath(sharedVolumePath, sourceName string) string {
	resolvedSharedPath := strings.TrimSpace(os.ExpandEnv(sharedVolumePath))
	resolvedSourceName := strings.TrimSpace(sourceName)
	if resolvedSharedPath == "" || resolvedSourceName == "" {
		return ""
	}

	doneFileName := fmt.Sprintf("done-%s.json", sanitizeSourceNameForFileName(resolvedSourceName))
	return filepath.Join(resolvedSharedPath, resolvedSourceName, doneFileName)
}

func sanitizeSourceNameForFileName(sourceName string) string {
	raw := strings.TrimSpace(strings.ToLower(sourceName))
	if raw == "" {
		return "source"
	}

	replaced := strings.NewReplacer(" ", "-", "_", "-").Replace(raw)
	out := make([]rune, 0, len(replaced))
	for _, r := range replaced {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '.' {
			out = append(out, r)
			continue
		}
		out = append(out, '-')
	}

	clean := strings.Trim(strings.Join(strings.FieldsFunc(string(out), func(r rune) bool { return r == '-' }), "-"), "-")
	if clean == "" {
		return "source"
	}

	return clean
}

func deleteStaleDoneFiles(paths ...string) error {
	for _, p := range paths {
		path := strings.TrimSpace(p)
		if path == "" {
			continue
		}

		err := os.Remove(path)
		if err == nil {
			fmt.Printf("deleted stale done signal: %s\n", path)
			continue
		}
		if os.IsNotExist(err) {
			continue
		}
		return fmt.Errorf("delete stale done signal %s: %w", path, err)
	}

	return nil
}

func waitForDoneSignals(launches []jobLaunchMetadata) error {
	timeout := doneWaitDefaultTimeout
	errCh := make(chan error, len(launches))
	var wg stdsync.WaitGroup

	for _, launch := range launches {
		meta := launch
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := waitForDoneSignal(meta, timeout); err != nil {
				errCh <- err
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

func waitForDoneSignal(meta jobLaunchMetadata, timeout time.Duration) error {
	donePath := strings.TrimSpace(meta.DoneFilePath)
	if donePath == "" {
		return fmt.Errorf("missing done file path for source %s", meta.SourceName)
	}

	deadline := time.Now().Add(timeout)
	var lastReadErr error
	for {
		if fileExists(donePath) {
			payload, err := readDonePayload(donePath, meta.SourceName)
			if err != nil {
				lastReadErr = err
			} else {
				if err := postResultToHawkAPI(payload); err != nil {
					return fmt.Errorf("post done payload for source=%s job=%s: %w", meta.SourceName, meta.JobName, err)
				}

				fmt.Printf("done signal received and posted for source=%s job=%s path=%s\n", meta.SourceName, meta.JobName, donePath)
				return nil
			}
		}

		if time.Now().After(deadline) {
			if lastReadErr != nil {
				return fmt.Errorf("timeout waiting for readable done signal for source=%s job=%s path=%s: %w", meta.SourceName, meta.JobName, donePath, lastReadErr)
			}
			return fmt.Errorf("timeout waiting for done signal for source=%s job=%s path=%s", meta.SourceName, meta.JobName, donePath)
		}

		time.Sleep(doneWaitPollInterval)
	}
}

func readDonePayload(donePath, fallbackSourceName string) (doneJson, error) {
	raw, err := os.ReadFile(donePath)
	if err != nil {
		return doneJson{}, fmt.Errorf("read done file %s: %w", donePath, err)
	}

	var payload doneJson
	if err := json.Unmarshal(raw, &payload); err != nil {
		return doneJson{}, fmt.Errorf("parse done file %s: %w", donePath, err)
	}

	if strings.TrimSpace(payload.SourceName) == "" {
		payload.SourceName = strings.TrimSpace(fallbackSourceName)
	}

	return payload, nil
}

func fileExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}
