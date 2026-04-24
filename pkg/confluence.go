package pkg

import (
	"fmt"
	"strings"
)

type confluenceSource struct {
	cfg ConfluenceCfg
}

type confluenceDiffResult struct {
	BaseFile     string   `json:"baseFile"`
	TargetFile   string   `json:"targetFile"`
	ChangedFiles []string `json:"changedFiles"`
	DeletedFiles []string `json:"deletedFiles"`
	AddedFiles   []string `json:"addedFiles"`
}

func newConfluenceSource(cfg ConfluenceCfg) Source {
	return confluenceSource{cfg: cfg}
}

func (c confluenceSource) Validate() error {
	if strings.TrimSpace(c.cfg.URL) == "" {
		return fmt.Errorf("confluence.url is required")
	}

	if strings.TrimSpace(c.cfg.Space) == "" {
		return fmt.Errorf("confluence.space is required")
	}

	hasCredType := strings.TrimSpace(c.cfg.Credentials.Type) != ""
	hasCredName := strings.TrimSpace(c.cfg.Credentials.Name) != ""
	if hasCredType != hasCredName {
		return fmt.Errorf("confluence.credentials.type and confluence.credentials.name must both be set")
	}

	return nil
}

func (c confluenceSource) Fetch() (SourceResult, error) {
	return SourceResult{
		Type: "confluence",
	}, nil
}

//
//func confluenceSync(source ConfluenceCfg) (confluenceDiffResult, error) {
//	return confluenceDiffResult{}, nil
//}
//
