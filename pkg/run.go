package pkg

import (
	"fmt"
	"os"
	"strings"
	stdsync "sync"

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
	Enabled    bool
	Mode       string
	AgentType  string `yaml:"agentType"`
	Image      string `yaml:"image"`
	Template   string `yaml:"template"`
	Schedule   string
	Sources    []sourceConfig
	APIServers apiServerCfg `yaml:"apiServer"`
}

type credentialsConfig struct {
	Type string
	Name string
	Path string `yaml:"path"`
}

type sharedVolumeConfig struct {
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

	fmt.Println("Running the hawk...")
	// This is supposed to run 2 functions, one for the init and other for the main loop.

	fmt.Println("Initializing hawk configuraions...")

	confList, err := init_hawk()
	if err != nil {
		fmt.Printf("error encountered: %v\n", err)
		panic(err)
	}

	// For loop that is going to run the main loop for config list.
	// Iterate over conflist struct and proceed in the for loop.
	var workers stdsync.WaitGroup
	for _, c := range confList {
		fmt.Printf("[DEBUG] Processing config: %s\n", c.Name)
		workers.Add(1)
		go func(cfg Config) {
			defer workers.Done()
			if err := sync(cfg); err != nil {
				fmt.Printf("sync worker for %s stopped: %v\n", cfg.Name, err)
			}
		}(c)
	}

	workers.Wait()
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

	fmt.Println("Config loaded, returning success")

	return confList, nil

}
