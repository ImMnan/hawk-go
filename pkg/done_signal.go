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
	for {
		if fileExists(donePath) {
			fmt.Printf("done signal received for source=%s job=%s path=%s\n", meta.SourceName, meta.JobName, donePath)
			return nil
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for done signal for source=%s job=%s path=%s", meta.SourceName, meta.JobName, donePath)
		}

		time.Sleep(doneWaitPollInterval)
	}
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
