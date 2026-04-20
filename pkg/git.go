package pkg

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/storage/memory"
)

// this is component to proocess and handle git operations.
// get/read the reposiroty, branch, paths and credentials from the config and perform git operations accordingly.
// Compare the latest with the previous commit, last commit that was sent in the func.

type gitSource struct {
	cfg        GitCfg
	sourceName string
}

type gitFileSnapshot struct {
	Path    string `json:"path"`
	BlobSHA string `json:"blobSha"`
	Mode    string `json:"mode"`
}

type gitCommitSnapshot struct {
	RepoURL    string            `json:"repoUrl"`
	Branch     string            `json:"branch"`
	CommitSHA  string            `json:"commitSha"`
	CommitTime string            `json:"commitTime"`
	Files      []gitFileSnapshot `json:"files"`
}

type gitSecretCredentials struct {
	Username string
	Token    string
	Password string
}

type gitDiffResult struct {
	BaseCommit       string   `json:"baseCommit"`
	TargetCommit     string   `json:"targetCommit"`
	ChangedFiles     []string `json:"changedFiles"`
	DeletedFiles     []string `json:"deletedFiles"`
	ExportedFiles    []string `json:"exportedFiles,omitempty"`
	ExportedOldFiles []string `json:"exportedOldFiles,omitempty"`
	ExportedNewFiles []string `json:"exportedNewFiles,omitempty"`
}

func newGitSource(cfg GitCfg, sourceName string) Source {
	return gitSource{cfg: cfg, sourceName: sourceName}
}

func (g gitSource) Validate() error {
	if strings.TrimSpace(g.cfg.URL) == "" {
		return fmt.Errorf("git.url is required")
	}

	if strings.TrimSpace(g.cfg.Branch) == "" {
		return fmt.Errorf("git.branch is required")
	}

	return nil
}

func (g gitSource) Fetch(sharedVolumePath string) error {
	_, err := gitSync(g.cfg, g.sourceName, sharedVolumePath)
	return err
}

func gitSync(source GitCfg, sourceName string, sharedVolumePath string) ([]byte, error) {

	// use mongo libraries to get the last commit id for the name of the source, which is also source.Name
	// for now, let;s hard code the last commit id:
	lastCommitSHA := "87fd6b8d0f56e5f5bad2c887585e9288f2adae93"

	latestCommitData, err := gitGetLatestCommit(source)
	if err != nil {
		return nil, fmt.Errorf("failed to get latest commit: %w", err)
	}

	lastCommitData, err := gitGetLastCommit(source, lastCommitSHA)
	if err != nil {
		if gitDebugEnabled() {
			fmt.Printf("[git-debug] unable to load last commit %s; using empty baseline: %v\n", lastCommitSHA, err)
		}

		latestSnapshot, decodeErr := decodeGitCommitSnapshot(latestCommitData)
		if decodeErr != nil {
			return nil, fmt.Errorf("failed to decode latest snapshot while building baseline: %w", decodeErr)
		}

		emptyBaseline := gitCommitSnapshot{
			RepoURL:    latestSnapshot.RepoURL,
			Branch:     latestSnapshot.Branch,
			CommitSHA:  "",
			CommitTime: "",
			Files:      []gitFileSnapshot{},
		}

		lastCommitData, err = json.Marshal(emptyBaseline)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal empty baseline snapshot: %w", err)
		}
	}

	diffData, err := gitDiff(lastCommitData, latestCommitData)
	if err != nil {
		return nil, fmt.Errorf("failed to diff commits: %w", err)
	}

	var diffResult gitDiffResult
	if err := json.Unmarshal(diffData, &diffResult); err != nil {
		return nil, fmt.Errorf("failed to decode diff result: %w", err)
	}

	if len(diffResult.ChangedFiles) == 0 {
		if gitDebugEnabled() {
			fmt.Printf("[git-debug] no changed files detected for source=%s\n", sourceName)
		}
		return diffData, nil
	}

	latestSnapshot, err := decodeGitCommitSnapshot(latestCommitData)
	if err != nil {
		return nil, fmt.Errorf("failed to decode latest snapshot: %w", err)
	}

	lastSnapshot, err := decodeGitCommitSnapshot(lastCommitData)
	if err != nil {
		return nil, fmt.Errorf("failed to decode last snapshot: %w", err)
	}

	exportedOldFiles, exportedNewFiles, err := writeChangedFilesFromCommits(
		source,
		sharedVolumePath,
		lastSnapshot.CommitSHA,
		latestSnapshot.CommitSHA,
		diffResult.ChangedFiles,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to export changed files: %w", err)
	}

	diffResult.ExportedOldFiles = exportedOldFiles
	diffResult.ExportedNewFiles = exportedNewFiles
	diffResult.ExportedFiles = append(append([]string{}, exportedOldFiles...), exportedNewFiles...)
	sort.Strings(diffResult.ExportedFiles)
	finalDiffData, err := json.Marshal(diffResult)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal final diff result: %w", err)
	}

	if gitDebugEnabled() {
		fmt.Printf("[git-debug] exported changed files source=%s oldCount=%d newCount=%d\n", sourceName, len(exportedOldFiles), len(exportedNewFiles))
	}

	return finalDiffData, nil

}

func gitGetLatestCommit(source GitCfg) ([]byte, error) {
	if gitDebugEnabled() {
		fmt.Printf("[git-debug] fetching latest commit repo=%s branch=%s\n", source.URL, source.Branch)
	}

	auth, err := resolveGitAuth(source)
	if err != nil {
		return nil, err
	}

	repo, err := git.Clone(memory.NewStorage(), memfs.New(), &git.CloneOptions{
		URL:           source.URL,
		ReferenceName: plumbing.NewBranchReferenceName(source.Branch),
		SingleBranch:  true,
		Depth:         1,
		Auth:          auth,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to clone repo %s branch %s: %w", source.URL, source.Branch, err)
	}

	if gitDebugEnabled() {
		fmt.Printf("[git-debug] clone succeeded repo=%s branch=%s\n", source.URL, source.Branch)
	}

	ref, err := repo.Reference(plumbing.NewBranchReferenceName(source.Branch), true)
	if err != nil {
		ref, err = repo.Head()
		if err != nil {
			return nil, fmt.Errorf("failed to resolve head for repo %s: %w", source.URL, err)
		}
	}

	commit, err := repo.CommitObject(ref.Hash())
	if err != nil {
		return nil, fmt.Errorf("failed to read commit object %s: %w", ref.Hash(), err)
	}

	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to read tree for commit %s: %w", commit.Hash, err)
	}

	files := make([]gitFileSnapshot, 0)
	err = tree.Files().ForEach(func(f *object.File) error {
		if !shouldIncludePath(f.Name, source.Dirs, source.IgnoreDirs) {
			return nil
		}

		files = append(files, gitFileSnapshot{
			Path:    normalizeGitPath(f.Name),
			BlobSHA: f.Hash.String(),
			Mode:    f.Mode.String(),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to scan files for commit %s: %w", commit.Hash, err)
	}

	if gitDebugEnabled() {
		fmt.Printf("[git-debug] commit=%s filteredFiles=%d\n", commit.Hash.String(), len(files))
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].Path < files[j].Path
	})

	snapshot := gitCommitSnapshot{
		RepoURL:    source.URL,
		Branch:     source.Branch,
		CommitSHA:  commit.Hash.String(),
		CommitTime: commit.Committer.When.UTC().Format(time.RFC3339),
		Files:      files,
	}

	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal commit snapshot: %w", err)
	}

	return encoded, nil
}

func gitGetLastCommit(source GitCfg, lastCommitId string) ([]byte, error) {
	lastCommitID := strings.TrimSpace(lastCommitId)
	if lastCommitID == "" {
		return nil, fmt.Errorf("last commit id is required")
	}

	if gitDebugEnabled() {
		fmt.Printf("[git-debug] fetching last commit repo=%s commit=%s\n", source.URL, lastCommitID)
	}

	auth, err := resolveGitAuth(source)
	if err != nil {
		return nil, err
	}

	repo, err := git.Clone(memory.NewStorage(), memfs.New(), &git.CloneOptions{
		URL:           source.URL,
		ReferenceName: plumbing.NewBranchReferenceName(source.Branch),
		SingleBranch:  true,
		Auth:          auth,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to clone repo %s for commit %s: %w", source.URL, lastCommitID, err)
	}

	hash := plumbing.NewHash(lastCommitID)
	if hash.IsZero() {
		return nil, fmt.Errorf("invalid last commit id: %s", lastCommitID)
	}

	commit, err := repo.CommitObject(hash)
	if err != nil {
		return nil, fmt.Errorf("failed to read commit object %s: %w", lastCommitID, err)
	}

	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to read tree for commit %s: %w", commit.Hash, err)
	}

	files := make([]gitFileSnapshot, 0)
	err = tree.Files().ForEach(func(f *object.File) error {
		if !shouldIncludePath(f.Name, source.Dirs, source.IgnoreDirs) {
			return nil
		}

		files = append(files, gitFileSnapshot{
			Path:    normalizeGitPath(f.Name),
			BlobSHA: f.Hash.String(),
			Mode:    f.Mode.String(),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to scan files for commit %s: %w", commit.Hash, err)
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].Path < files[j].Path
	})

	snapshot := gitCommitSnapshot{
		RepoURL:    source.URL,
		Branch:     source.Branch,
		CommitSHA:  commit.Hash.String(),
		CommitTime: commit.Committer.When.UTC().Format(time.RFC3339),
		Files:      files,
	}

	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal commit snapshot: %w", err)
	}

	return encoded, nil

}

func gitDiff(lastCommitData []byte, latestCommitData []byte) ([]byte, error) {
	lastSnapshot, err := decodeGitCommitSnapshot(lastCommitData)
	if err != nil {
		return nil, fmt.Errorf("failed to decode last commit snapshot: %w", err)
	}

	latestSnapshot, err := decodeGitCommitSnapshot(latestCommitData)
	if err != nil {
		return nil, fmt.Errorf("failed to decode latest commit snapshot: %w", err)
	}

	lastByPath := make(map[string]string, len(lastSnapshot.Files))
	for _, f := range lastSnapshot.Files {
		lastByPath[f.Path] = f.BlobSHA
	}

	latestByPath := make(map[string]string, len(latestSnapshot.Files))
	changed := make([]string, 0)
	for _, f := range latestSnapshot.Files {
		latestByPath[f.Path] = f.BlobSHA
		if oldSHA, ok := lastByPath[f.Path]; !ok || oldSHA != f.BlobSHA {
			changed = append(changed, f.Path)
		}
	}

	deleted := make([]string, 0)
	for oldPath := range lastByPath {
		if _, ok := latestByPath[oldPath]; !ok {
			deleted = append(deleted, oldPath)
		}
	}

	sort.Strings(changed)
	sort.Strings(deleted)

	result := gitDiffResult{
		BaseCommit:   lastSnapshot.CommitSHA,
		TargetCommit: latestSnapshot.CommitSHA,
		ChangedFiles: changed,
		DeletedFiles: deleted,
	}

	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal diff result: %w", err)
	}

	return encoded, nil
}

func shouldIncludePath(filePath string, dirs []string, ignoreDirs []string) bool {
	normalizedFile := normalizeGitPath(filePath)
	if normalizedFile == "" {
		return false
	}

	if len(dirs) > 0 && !pathMatchesAny(normalizedFile, dirs) {
		return false
	}

	if len(ignoreDirs) > 0 && pathMatchesAny(normalizedFile, ignoreDirs) {
		return false
	}

	return true
}

func pathMatchesAny(filePath string, dirs []string) bool {
	for _, dir := range dirs {
		normalizedDir := normalizeGitPath(dir)
		if normalizedDir == "" {
			continue
		}

		if filePath == normalizedDir || strings.HasPrefix(filePath, normalizedDir+"/") {
			return true
		}
	}

	return false
}

func normalizeGitPath(p string) string {
	normalized := strings.ReplaceAll(strings.TrimSpace(p), "\\", "/")
	if normalized == "" {
		return ""
	}

	normalized = strings.TrimPrefix(normalized, "./")
	normalized = strings.TrimPrefix(normalized, "/")
	normalized = path.Clean(normalized)
	if normalized == "." {
		return ""
	}

	return normalized
}

func resolveGitAuth(source GitCfg) (transport.AuthMethod, error) {
	credentialType := strings.ToLower(strings.TrimSpace(source.Credentials.Type))
	if credentialType == "" {
		return nil, nil
	}

	if credentialType != "secret" {
		return nil, fmt.Errorf("unsupported git credential type: %s", source.Credentials.Type)
	}

	secretName := strings.TrimSpace(source.Credentials.Name)
	if secretName == "" {
		return nil, fmt.Errorf("git.credentials.name is required when credentials.type is secret")
	}

	secretValues := loadGitSecretCredentials(source.Credentials)
	if gitDebugEnabled() {
		basePath := resolveSecretBasePath(source.Credentials)
		fmt.Printf("[git-debug] secret lookup type=%s name=%s path=%s usernamePresent=%t tokenPresent=%t passwordPresent=%t\n",
			source.Credentials.Type,
			secretName,
			basePath,
			strings.TrimSpace(secretValues.Username) != "",
			strings.TrimSpace(secretValues.Token) != "",
			strings.TrimSpace(secretValues.Password) != "",
		)
	}

	password := firstNonEmpty(secretValues.Token, secretValues.Password)
	if password == "" {
		return nil, fmt.Errorf("secret %s is missing token/password for https auth", secretName)
	}

	username := firstNonEmpty(secretValues.Username, "x-access-token")
	return &githttp.BasicAuth{Username: username, Password: password}, nil
}

func loadGitSecretCredentials(cfg credentialsConfig) gitSecretCredentials {
	return gitSecretCredentials{
		Username: readSecretField(cfg, "username", "USERNAME"),
		Token:    readSecretField(cfg, "token", "TOKEN"),
		Password: readSecretField(cfg, "password", "PASSWORD"),
	}
}

func readSecretField(cfg credentialsConfig, fileName string, bundleKey string) string {
	basePath := resolveSecretBasePath(cfg)
	secretName := strings.TrimSpace(cfg.Name)
	if secretName == "" {
		return ""
	}

	filePath := path.Join(basePath, secretName, fileName)
	content, err := os.ReadFile(filePath)
	if err == nil {
		return strings.TrimSpace(string(content))
	}

	bundleFilePath := path.Join(basePath, secretName)
	bundleContent, bundleErr := os.ReadFile(bundleFilePath)
	if bundleErr != nil {
		return ""
	}

	return parseSecretBundleValue(string(bundleContent), fileName, bundleKey)
}

func resolveSecretBasePath(cfg credentialsConfig) string {
	basePath := strings.TrimSpace(os.ExpandEnv(cfg.Path))
	if basePath == "" {
		return "/etc/hawk/secrets"
	}

	return basePath
}

func parseSecretBundleValue(bundle string, fileKey string, envStyleKey string) string {
	normalizedTargets := map[string]struct{}{
		normalizeSecretBundleKey(fileKey):     {},
		normalizeSecretBundleKey(envStyleKey): {},
	}

	for _, rawLine := range strings.Split(bundle, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		sep := "="
		if strings.Contains(line, ":") {
			sep = ":"
		}

		parts := strings.SplitN(line, sep, 2)
		if len(parts) != 2 {
			continue
		}

		key := normalizeSecretBundleKey(parts[0])
		if _, ok := normalizedTargets[key]; !ok {
			continue
		}

		value := strings.TrimSpace(parts[1])
		value = strings.Trim(value, `"'`)
		return value
	}

	return ""
}

func normalizeSecretBundleKey(v string) string {
	key := strings.ToLower(strings.TrimSpace(v))
	key = strings.ReplaceAll(key, "-", "")
	key = strings.ReplaceAll(key, "_", "")
	key = strings.ReplaceAll(key, ".", "")
	return key
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}

	return ""
}

func decodeGitCommitSnapshot(data []byte) (gitCommitSnapshot, error) {
	var snapshot gitCommitSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return gitCommitSnapshot{}, err
	}

	return snapshot, nil
}

func writeChangedFilesFromCommits(source GitCfg, sharedVolumePath string, baseCommitSHA string, targetCommitSHA string, changedFiles []string) ([]string, []string, error) {
	resolvedSharedPath := strings.TrimSpace(os.ExpandEnv(sharedVolumePath))
	if resolvedSharedPath == "" {
		return nil, nil, fmt.Errorf("shared volume path is required")
	}

	auth, err := resolveGitAuth(source)
	if err != nil {
		return nil, nil, err
	}

	repo, err := git.Clone(memory.NewStorage(), memfs.New(), &git.CloneOptions{
		URL:           source.URL,
		ReferenceName: plumbing.NewBranchReferenceName(source.Branch),
		SingleBranch:  true,
		Auth:          auth,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to clone repo %s for export: %w", source.URL, err)
	}

	targetHash := plumbing.NewHash(strings.TrimSpace(targetCommitSHA))
	if targetHash.IsZero() {
		return nil, nil, fmt.Errorf("invalid latest commit hash")
	}

	targetCommit, err := repo.CommitObject(targetHash)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load commit %s for export: %w", targetCommitSHA, err)
	}

	targetTree, err := targetCommit.Tree()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load tree for export commit %s: %w", targetCommitSHA, err)
	}

	var baseTree *object.Tree
	baseHash := plumbing.NewHash(strings.TrimSpace(baseCommitSHA))
	if !baseHash.IsZero() {
		baseCommit, baseErr := repo.CommitObject(baseHash)
		if baseErr != nil {
			return nil, nil, fmt.Errorf("failed to load base commit %s for export: %w", baseCommitSHA, baseErr)
		}

		baseTree, baseErr = baseCommit.Tree()
		if baseErr != nil {
			return nil, nil, fmt.Errorf("failed to load tree for base commit %s: %w", baseCommitSHA, baseErr)
		}
	}

	exportedOldFiles := make([]string, 0, len(changedFiles))
	exportedNewFiles := make([]string, 0, len(changedFiles))
	for _, changedPath := range changedFiles {
		normalizedChangedPath := normalizeGitPath(changedPath)
		if normalizedChangedPath == "" {
			continue
		}

		matchedDir, relativePath := splitByMatchedDir(normalizedChangedPath, source.Dirs)
		if matchedDir == "" {
			continue
		}

		rootPath := path.Join(resolvedSharedPath, matchedDir, targetCommitSHA)

		if baseTree != nil {
			baseContent, foundInBase, readErr := readFileFromTree(baseTree, normalizedChangedPath)
			if readErr != nil {
				return nil, nil, fmt.Errorf("failed to read old file %s: %w", normalizedChangedPath, readErr)
			}

			if foundInBase {
				oldTargetPath := path.Join(rootPath, "Old", relativePath)
				if err := os.MkdirAll(path.Dir(oldTargetPath), 0o755); err != nil {
					return nil, nil, fmt.Errorf("failed to create export directory for %s: %w", oldTargetPath, err)
				}

				if err := os.WriteFile(oldTargetPath, baseContent, 0o644); err != nil {
					return nil, nil, fmt.Errorf("failed to write old file %s: %w", oldTargetPath, err)
				}

				exportedOldFiles = append(exportedOldFiles, oldTargetPath)
			}
		}

		targetContent, foundInTarget, readErr := readFileFromTree(targetTree, normalizedChangedPath)
		if readErr != nil {
			return nil, nil, fmt.Errorf("failed to read new file %s: %w", normalizedChangedPath, readErr)
		}

		if foundInTarget {
			newTargetPath := path.Join(rootPath, "New", relativePath)
			if err := os.MkdirAll(path.Dir(newTargetPath), 0o755); err != nil {
				return nil, nil, fmt.Errorf("failed to create export directory for %s: %w", newTargetPath, err)
			}

			if err := os.WriteFile(newTargetPath, targetContent, 0o644); err != nil {
				return nil, nil, fmt.Errorf("failed to write new file %s: %w", newTargetPath, err)
			}

			exportedNewFiles = append(exportedNewFiles, newTargetPath)
		}
	}

	sort.Strings(exportedOldFiles)
	sort.Strings(exportedNewFiles)
	return exportedOldFiles, exportedNewFiles, nil
}

func readFileFromTree(tree *object.Tree, filePath string) ([]byte, bool, error) {
	f, err := tree.File(filePath)
	if err != nil {
		return nil, false, nil
	}

	reader, err := f.Reader()
	if err != nil {
		return nil, false, err
	}
	defer reader.Close()

	content, err := io.ReadAll(reader)
	if err != nil {
		return nil, false, err
	}

	return content, true, nil
}

func splitByMatchedDir(filePath string, dirs []string) (string, string) {
	normalizedFile := normalizeGitPath(filePath)
	if normalizedFile == "" {
		return "", ""
	}

	bestMatch := ""
	for _, dir := range dirs {
		normalizedDir := normalizeGitPath(dir)
		if normalizedDir == "" {
			continue
		}

		if normalizedFile == normalizedDir || strings.HasPrefix(normalizedFile, normalizedDir+"/") {
			if len(normalizedDir) > len(bestMatch) {
				bestMatch = normalizedDir
			}
		}
	}

	if bestMatch == "" {
		return "", ""
	}

	relativePath := strings.TrimPrefix(normalizedFile, bestMatch)
	relativePath = strings.TrimPrefix(relativePath, "/")
	if relativePath == "" {
		relativePath = path.Base(normalizedFile)
	}

	return bestMatch, relativePath
}

func gitDebugEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("HAWK_DEBUG_GIT")))
	return v == "1" || v == "true" || v == "yes"
}
