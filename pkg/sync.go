package pkg

import (
	"fmt"
	"strings"
	stdsync "sync"
	"time"

	"github.com/robfig/cron/v3"
	"k8s.io/client-go/kubernetes"
)

type Source interface {
	Validate() error
	Fetch() (SourceResult, error)
}

type SourceResult struct {
	Name             string
	Type             string
	SharedVolumeName string
	SharedVolumePath string
	GitDiff          *gitDiffResult
	ConfluenceDiff   *confluenceDiffResult
	NoOp             bool
	Err              error
}

func newSource(cfg sourceConfig, syncCfg SyncConfig) (Source, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.Type)) {
	case "git":
		if cfg.Git == nil {
			return nil, fmt.Errorf("missing git config")
		}
		return newGitSource(*cfg.Git, cfg.Name, cfg.SharedVolume.Path, resolveAPIServerEndpoint(syncCfg)), nil
	case "confluence":
		if cfg.Confluence == nil {
			return nil, fmt.Errorf("missing confluence config")
		}
		return newConfluenceSource(*cfg.Confluence), nil
	default:
		return nil, fmt.Errorf("unsupported source type: %s", cfg.Type)
	}
}

func loadLatestConfig(base Config) (Config, error) {
	confList, err := init_hawk()
	if err != nil {
		return Config{}, err
	}

	baseID := strings.TrimSpace(base.ID)
	if baseID != "" {
		for _, cfg := range confList {
			if strings.TrimSpace(cfg.ID) == baseID {
				return cfg, nil
			}
		}
	}

	baseName := strings.TrimSpace(base.Name)
	for _, cfg := range confList {
		if strings.EqualFold(strings.TrimSpace(cfg.Name), baseName) {
			return cfg, nil
		}
	}

	if baseID != "" {
		return Config{}, fmt.Errorf("config not found in latest configlist (id=%s name=%s)", baseID, baseName)
	}

	return Config{}, fmt.Errorf("config not found in latest configlist (name=%s)", baseName)
}

func performSyncCycle(c Config, clientSet *kubernetes.Clientset) error {
	latestCfg, err := loadLatestConfig(c)
	if err != nil {
		return fmt.Errorf("failed to refresh configlist for %s: %w", c.Name, err)
	}

	c = latestCfg
	syncCfg := c.Sync

	if !syncCfg.Enabled {
		fmt.Printf("sync disabled for %s in latest config, stopping worker\n", c.Name)
		return fmt.Errorf("sync disabled in config")
	}

	templateJob, err := loadJobTemplate(syncCfg.JobTemplate)
	if err != nil {
		return fmt.Errorf("failed to load job template: %w", err)
	}

	switch syncCfg.Mode {
	case "local-agent":
		fmt.Printf("syncing %s using local agent\n", c.Name)

		results := make(chan SourceResult, len(syncCfg.Sources))
		var wg stdsync.WaitGroup

		for _, source := range syncCfg.Sources {
			src := source
			wg.Add(1)
			go func() {
				defer wg.Done()
				fmt.Printf("processing source %s (%s)\n", src.Name, src.Type)

				handler, err := newSource(src, syncCfg)
				if err != nil {
					results <- SourceResult{
						Name: src.Name,
						Type: src.Type,
						Err:  fmt.Errorf("source init failed: %w", err),
					}
					return
				}

				if err := handler.Validate(); err != nil {
					results <- SourceResult{
						Name: src.Name,
						Type: src.Type,
						Err:  fmt.Errorf("source config invalid: %w", err),
					}
					return
				}

				result, err := handler.Fetch()
				result.SharedVolumeName = src.SharedVolume.Name
				result.SharedVolumePath = src.SharedVolume.Path
				if err != nil {
					result.Err = fmt.Errorf("source fetch failed: %w", err)
					if result.Name == "" {
						result.Name = src.Name
					}
					if result.Type == "" {
						result.Type = src.Type
					}
					results <- result
					return
				}

				results <- result
			}()
		}

		wg.Wait()
		close(results)

		var firstErr error
		launches := make([]jobLaunchMetadata, 0, len(syncCfg.Sources))
		for result := range results {
			if result.Err != nil {
				fmt.Printf("source %s (%s) failed: %v\n", result.Name, result.Type, result.Err)
				if firstErr == nil {
					firstErr = fmt.Errorf("source %s (%s): %w", result.Name, result.Type, result.Err)
				}
				continue
			}

			if result.NoOp {
				fmt.Printf("source %s (%s) produced no work, skipping kubernetes job creation\n", result.Name, result.Type)
				continue
			}

			launchMeta, err := createKubernetesJob(result, syncCfg, templateJob, clientSet)
			if err != nil {
				fmt.Printf("failed to create kubernetes job for source %s (%s): %v\n", result.Name, result.Type, err)
				if firstErr == nil {
					firstErr = fmt.Errorf("source %s (%s): %w", result.Name, result.Type, err)
				}
				continue
			}

			launches = append(launches, launchMeta)
			fmt.Printf("launch metadata source=%s job=%s targetCommit=%s\n", launchMeta.SourceName, launchMeta.JobName, launchMeta.TargetCommit)
		}

		if len(launches) > 0 {
			if err := waitForCommitUpdates(launches, resolveAPIServerEndpoint(syncCfg), clientSet); err != nil {
				return err
			}
		}

		if firstErr != nil {
			return firstErr
		}
	default:
		return fmt.Errorf("unsupported sync mode: %s", syncCfg.Mode)
	}

	return nil
}

func sync(c Config) error {
	if !c.Sync.Enabled {
		fmt.Printf("sync disabled for %s\n", c.Name)
		return nil
	}

	clientSet, err := kubernetesInit()
	if err != nil {
		return fmt.Errorf("failed to initialize kubernetes client: %w", err)
	}

	trigger, stop, err := syncTrigger(c)
	if err != nil {
		return err
	}
	defer stop()

	fmt.Printf("syncing %s of type %s as per cron %v\n", c.Name, c.Type, c.Sync.Schedule)

	// Execute the first sync cycle immediately on startup
	fmt.Printf("executing initial sync cycle for %s\n", c.Name)
	if err := performSyncCycle(c, clientSet); err != nil {
		fmt.Printf("initial sync cycle for %s failed: %v\n", c.Name, err)
		// Continue to next trigger instead of returning
	}

	// Enter the loop for subsequent sync cycles triggered by cron
	for triggeredAt := range trigger {
		fmt.Printf("sync trigger fired for %s at %s\n", c.Name, triggeredAt.Format(time.RFC3339))

		if err := performSyncCycle(c, clientSet); err != nil {
			// Check if sync was disabled in the config
			if strings.Contains(err.Error(), "sync disabled") {
				return nil
			}
			fmt.Printf("sync cycle for %s failed: %v\n", c.Name, err)
			// Continue to next trigger instead of returning
		}
	}

	return fmt.Errorf("sync trigger stopped for %s", c.Name)

}

func syncTrigger(c Config) (<-chan time.Time, func(), error) {
	if c.Sync.Schedule == "" {
		return nil, nil, fmt.Errorf("sync schedule is required for %s", c.Name)
	}

	cronSched := cron.New()
	trigger := make(chan time.Time)

	_, err := cronSched.AddFunc(c.Sync.Schedule, func() {
		select {
		case trigger <- time.Now():
		default:
			fmt.Printf("warning: sync trigger blocked for %s\n", c.Name)
		}
	})
	if err != nil {
		return nil, nil, fmt.Errorf("invalid cron schedule for %s: %w", c.Name, err)
	}

	cronSched.Start()
	fmt.Printf("cron scheduler started for %s with schedule: %s\n", c.Name, c.Sync.Schedule)

	stop := func() {
		<-cronSched.Stop().Done()
		close(trigger)
	}

	return trigger, stop, nil
}
