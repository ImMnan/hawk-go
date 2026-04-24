package pkg

import (
	"fmt"
	"strings"
	stdsync "sync"
	"time"

	"github.com/robfig/cron/v3"
)

type Source interface {
	Validate() error
	Fetch() (SourceResult, error)
}

type SourceResult struct {
	Name           string
	Type           string
	GitDiff        *gitDiffResult
	ConfluenceDiff *confluenceDiffResult
	Err            error
}

func newSource(cfg sourceConfig) (Source, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.Type)) {
	case "git":
		if cfg.Git == nil {
			return nil, fmt.Errorf("missing git config")
		}
		return newGitSource(*cfg.Git, cfg.Name, cfg.SharedVolume.Path), nil
	case "confluence":
		if cfg.Confluence == nil {
			return nil, fmt.Errorf("missing confluence config")
		}
		return newConfluenceSource(*cfg.Confluence), nil
	default:
		return nil, fmt.Errorf("unsupported source type: %s", cfg.Type)
	}
}

func sync(c Config) error {
	syncCfg := c.Sync
	if !syncCfg.Enabled {
		fmt.Printf("sync disabled for %s\n", c.Name)
		return nil
	}

	resultQueue, err := kubernetesController()
	if err != nil {
		return fmt.Errorf("failed to initialize kubernetes controller: %w", err)
	}
	defer close(resultQueue)

	trigger, stop, err := syncTrigger(c)
	if err != nil {
		return err
	}
	defer stop()

	fmt.Printf("syncing %s\n of type %s as per cron %v", c.Name, c.Type, c.Sync.Schedule)
	for triggeredAt := range trigger {
		fmt.Printf("sync trigger fired for %s at %s\n", c.Name, triggeredAt.Format(time.RFC3339))

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

					handler, err := newSource(src)
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
			for result := range results {
				if result.Err != nil {
					fmt.Printf("source %s (%s) failed: %v\n", result.Name, result.Type, result.Err)
					if firstErr == nil {
						firstErr = fmt.Errorf("source %s (%s): %w", result.Name, result.Type, result.Err)
					}
					continue
				}

				resultQueue <- result
			}

			if firstErr != nil {
				return firstErr
			}
		default:
			return fmt.Errorf("unsupported sync mode: %s", syncCfg.Mode)
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
