package pkg

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	stdsync "sync"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

type ConfList []Config

type Config struct {
	Name        string
	Type        string
	ID          string
	Description string
	Sync        SyncConfig
}

type SyncConfig struct {
	Enabled     bool
	Mode        string
	AgentType   string `yaml:"agentType"`
	Image       string `yaml:"image"`
	JobTemplate string `yaml:"job-template"`
	Schedule    string
	Sources     []sourceConfig
	APIServers  apiServerCfg `yaml:"apiServer"`
}

type credentialsConfig struct {
	Type string
	Name string
	Path string `yaml:"path"`
}

type sharedVolumeConfig struct {
	Name string `yaml:"name"`
	Path string
}

type sourceConfig struct {
	Type         string             `yaml:"type"`
	Name         string             `yaml:"name"`
	SharedVolume sharedVolumeConfig `yaml:"sharedVolume"`
	Git          *GitCfg            `yaml:"git,omitempty"`
	Confluence   *ConfluenceCfg     `yaml:"confluence,omitempty"`
}

type GitCfg struct {
	URL         string            `yaml:"url"`
	Branch      string            `yaml:"branch"`
	Dirs        []string          `yaml:"dirList"`
	IgnoreDirs  []string          `yaml:"ignoreDirList"`
	Credentials credentialsConfig `yaml:"credentials"`
}

type ConfluenceCfg struct {
	URL         string            `yaml:"url"`
	Space       string            `yaml:"space"`
	Dirs        []string          `yaml:"dirs"`
	Credentials credentialsConfig `yaml:"credentials"`
}

type apiServerCfg struct {
	Type       string `yaml:"type"`
	Name       string `yaml:"name"`
	Connection struct {
		SvcName string `yaml:"serviceName"`
		Port    int    `yaml:"port"`
	} `yaml:"connection"`
	Credentials credentialsConfig `yaml:"credentials"`
}

func Run() {

	slog.Info("running hawk")

	// Initialize health check server
	if err := InitHealthCheck(); err != nil {
		slog.Error("failed to initialize health check", "error", err)
		panic(err)
	}

	// Setup graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// This is supposed to run 2 functions, one for the init and other for the main loop.

	slog.Info("initializing hawk configurations")

	confList, err := init_hawk()
	if err != nil {
		slog.Error("failed to initialize hawk", "error", err)
		panic(err)
	}

	// Mark configuration as loaded
	MarkConfigLoaded()

	// For loop that is going to run the main loop for config list.
	// Iterate over conflist struct and proceed in the for loop.
	var workers stdsync.WaitGroup
	for _, c := range confList {
		slog.Debug("processing config", "config", c.Name)
		workers.Add(1)
		go func(cfg Config) {
			defer workers.Done()
			if err := sync(cfg); err != nil {
				slog.Error("sync worker stopped", "config", cfg.Name, "error", err)
			}
		}(c)
	}

	// Mark workers as started
	MarkWorkersStarted()

	// Wait for shutdown signal or workers to complete
	shutdownDone := make(chan struct{})
	go func() {
		workers.Wait()
		close(shutdownDone)
	}()

	select {
	case sig := <-sigChan:
		slog.Info("received shutdown signal", "signal", sig)
	case <-shutdownDone:
		slog.Info("all workers completed")
	}

	// Gracefully shutdown health check server
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	if err := ShutdownHealthCheck(ctx); err != nil {
		slog.Error("error shutting down health check server", "error", err)
	}

	slog.Info("hawk shutdown complete")
}

func init_hawk() (ConfList, error) {

	// This function is main initialisation function, tieng all other sub-init functions.
	configPath := strings.TrimSpace(os.Getenv("HAWK_CONFIG_PATH"))
	if configPath == "" {
		configPath = "/etc/hawk/configlist.yaml"
	}
	if _, err := os.Stat(configPath); err != nil {
		// Local development fallback when the container path does not exist.
		configPath = "configlist.yaml"
	}
	configData, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("unable to read config file %s: %w", configPath, err)
	}

	var confList ConfList
	if err := yaml.Unmarshal(configData, &confList); err != nil {
		return nil, fmt.Errorf("unable to parse config YAML: %w", err)
	}

	slog.Info("config loaded")

	return confList, nil

}
