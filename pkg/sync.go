package pkg

import (
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

type Source interface {
	Validate() error
	Fetch() error
}

func newSource(cfg sourceConfig) (Source, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.Type)) {
	case "git":
		if cfg.Git == nil {
			return nil, fmt.Errorf("missing git config")
		}
		return newGitSource(*cfg.Git, cfg.Name), nil
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

			for _, source := range syncCfg.Sources {
				fmt.Printf("processing source %s (%s)\n", source.Name, source.Type)

				handler, err := newSource(source)
				if err != nil {
					return fmt.Errorf("source %s (%s): %w", source.Name, source.Type, err)
				}

				if err := handler.Validate(); err != nil {
					return fmt.Errorf("source %s config invalid: %w", source.Name, err)
				}

				if err := handler.Fetch(); err != nil {
					return fmt.Errorf("source %s failed: %w", source.Name, err)
				}
			}
		default:
			return fmt.Errorf("unsupported sync mode: %s", syncCfg.Mode)
		}
	}

	return fmt.Errorf("sync trigger stopped for %s", c.Name)

	/*
		sync is supposed to run as a go routine.
		We need to make sure that the sync is triggering operations as per the schedule cron
		the the cron is triggered
		Sync is supposed to run a for loop on source:
		   get the diffed files from the gitSync
		   all files much be put to sharedVolume path,
		   along with the additional sources.dirs paths.
		   all files much be prefixed with their commit ids (something like a - or _)
		   Once all files are saved into the shared volume, we need to trigger the agent,
		   The agent will be triggered in the kubernetes
		   Agent reads and processes the data, returns the output
		   the output them must be placed into the database i.e. database.type
		this

	*/

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
