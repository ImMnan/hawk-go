package pkg

import (
	"fmt"
	"log/slog"
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
		slog.Info("sync disabled in latest config, stopping worker", "config", c.Name)
		return fmt.Errorf("sync disabled in config")
	}

	templateJob, err := loadJobTemplate(syncCfg.JobTemplate)
	if err != nil {
		return fmt.Errorf("failed to load job template: %w", err)
	}

	switch syncCfg.Mode {
	case "local-agent":
		slog.Info("syncing config using local agent", "config", c.Name)

		results := make(chan SourceResult, len(syncCfg.Sources))
		var wg stdsync.WaitGroup

		for _, source := range syncCfg.Sources {
			src := source
			wg.Add(1)
			go func() {
				defer wg.Done()
				slog.Debug("processing source", "source", src.Name, "type", src.Type)

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
				slog.Error("source failed", "source", result.Name, "type", result.Type, "error", result.Err)
				if firstErr == nil {
					firstErr = fmt.Errorf("source %s (%s): %w", result.Name, result.Type, result.Err)
				}
				continue
			}

			if result.NoOp {
				slog.Info("source produced no work, skipping kubernetes job creation", "source", result.Name, "type", result.Type)
				continue
			}

			launchMeta, err := createKubernetesJob(result, syncCfg, templateJob, clientSet)
			if err != nil {
				slog.Error("failed to create kubernetes job", "source", result.Name, "type", result.Type, "error", err)
				if firstErr == nil {
					firstErr = fmt.Errorf("source %s (%s): %w", result.Name, result.Type, err)
				}
				continue
			}

			launches = append(launches, launchMeta)
			slog.Info("launch metadata", "source", launchMeta.SourceName, "job", launchMeta.JobName, "targetCommit", launchMeta.TargetCommit)
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
		slog.Info("sync disabled", "config", c.Name)
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

	slog.Info("sync scheduler started", "config", c.Name, "type", c.Type, "schedule", c.Sync.Schedule)

	// Execute the first sync cycle immediately on startup
	slog.Info("executing initial sync cycle", "config", c.Name)
	if err := performSyncCycle(c, clientSet); err != nil {
		slog.Error("initial sync cycle failed", "config", c.Name, "error", err)
		// Continue to next trigger instead of returning
	}
	logNextSyncCycle(c.Name, c.Sync.Schedule, time.Now())

	// Enter the loop for subsequent sync cycles triggered by cron
	for triggeredAt := range trigger {
		slog.Info("sync trigger fired", "config", c.Name, "time", triggeredAt.Format(time.RFC3339))

		if err := performSyncCycle(c, clientSet); err != nil {
			// Check if sync was disabled in the config
			if strings.Contains(err.Error(), "sync disabled") {
				return nil
			}
			slog.Error("sync cycle failed", "config", c.Name, "error", err)
			// Continue to next trigger instead of returning
		}

		logNextSyncCycle(c.Name, c.Sync.Schedule, time.Now())
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
			slog.Warn("sync trigger blocked", "config", c.Name)
		}
	})
	if err != nil {
		return nil, nil, fmt.Errorf("invalid cron schedule for %s: %w", c.Name, err)
	}

	cronSched.Start()
	slog.Info("cron scheduler started", "config", c.Name, "schedule", c.Sync.Schedule)

	stop := func() {
		<-cronSched.Stop().Done()
		close(trigger)
	}

	return trigger, stop, nil
}

func logNextSyncCycle(configName string, schedule string, reference time.Time) {
	parsedSchedule, err := cron.ParseStandard(schedule)
	if err != nil {
		slog.Warn("failed to compute next sync cycle", "config", configName, "schedule", schedule, "error", err)
		return
	}

	nextRun := parsedSchedule.Next(reference)
	if nextRun.IsZero() {
		slog.Warn("next sync cycle could not be determined", "config", configName, "schedule", schedule)
		return
	}

	slog.Info("next sync cycle scheduled", "config", configName, "schedule", schedule, "nextRun", nextRun.Format(time.RFC3339))
}
