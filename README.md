# HAWK

HAWK is a scheduled content sync service. Its current implementation focuses on Git-based sources, detecting file changes between two commits, and exporting the changed file pairs for downstream processing by another application.

At a high level, HAWK does this:

1. Reads a YAML configuration file.
2. Starts one worker per top-level config entry.
3. Schedules each worker with cron.
4. For each sync run, loads source handlers through an interface.
5. For Git sources, fetches:
  1. the latest commit on a configured branch,
  2. a previous commit, currently hardcoded,
  3. the file-level diff between them.
6. Exports only changed files to a shared volume in two trees:
  1. `Old`
  2. `New`

The exported content is intended to be consumed by another program, such as `/app`, for AI-based analysis or document comparison.

## Current Status

The Git pipeline is the most complete part of the project.

Implemented today:

1. YAML config loading.
2. Cron-triggered sync workers.
3. Source abstraction with a `Source` interface.
4. Git authentication via mounted secret files.
5. Latest commit snapshot generation.
6. Previous commit snapshot generation.
7. In-memory diff between commit snapshots.
8. Export of changed file pairs into the shared volume.

Partially implemented or scaffolded:

1. Confluence source type is only stubbed.
2. Database integration is modeled in config structs but not yet used in code.
3. Previous commit lookup is hardcoded for now instead of being loaded from state storage.

## Entry Point

The program entry point is in `main.go`.

```go
func main() {
	fmt.Println("Initializing the hawk...")
	pkg.Run()
}
```

`main()` does only one thing: hand control to `pkg.Run()`.

## Package Overview

### `main.go`

Responsibilities:

1. Start the program.
2. Delegate execution to the `pkg` package.

### `pkg/run.go`

Responsibilities:

1. Define the main configuration structs.
2. Read and unmarshal the YAML config file.
3. Start one goroutine per top-level config item.

### `pkg/sync.go`

Responsibilities:

1. Define the `Source` interface.
2. Construct the correct source handler from source config.
3. Drive scheduled sync execution.
4. Pass the shared volume path into source handlers.

### `pkg/git.go`

Responsibilities:

1. Implement the Git source.
2. Authenticate with private Git repos.
3. Build commit snapshots.
4. Compare snapshots.
5. Export changed files into the shared volume.

### `pkg/confluence.go`

Responsibilities:

1. Provide a skeleton implementation of a non-Git source.
2. Conform to the same `Source` interface used by Git.

This file exists mainly to show how additional source types can be added under the same abstraction.

## Configuration Model

The YAML config is mapped to structs in `pkg/run.go`.

### Top-Level Types

#### `ConfList`

```go
type ConfList []Config
```

This is the full YAML document. Each list item describes one independent sync job.

#### `Config`

```go
type Config struct {
	Name        string
	Type        string
	ID          string
	Description string
	Sync        SyncConfig
}
```

This is one logical sync workload.

Important fields:

1. `Name`: human-readable identifier.
2. `Type`: workload type, such as `doc`.
3. `ID`: external or internal job ID.
4. `Description`: free-form metadata.
5. `Sync`: scheduling and source details.

#### `SyncConfig`

```go
type SyncConfig struct {
	Enabled      bool
	Mode         string
	AgentType    string             `yaml:"agentType"`
	SharedVolume sharedVolumeConfig `yaml:"sharedVolume"`
	Schedule     string
	Sources      []sourceConfig
	Database     databaseConfig
}
```

This holds runtime behavior.

Important fields:

1. `Enabled`: turns the sync on or off.
2. `Mode`: currently `local-agent` is the implemented path.
3. `AgentType`: metadata for downstream processing.
4. `SharedVolume.Path`: root export location for changed files.
5. `Schedule`: cron schedule.
6. `Sources`: one or more input systems.
7. `Database`: future persistence target.

#### `sourceConfig`

```go
type sourceConfig struct {
	Type       string         `yaml:"type"`
	Name       string         `yaml:"name"`
	Git        *GitCfg        `yaml:"git,omitempty"`
	Confluence *ConfluenceCfg `yaml:"confluence,omitempty"`
}
```

This is a polymorphic source wrapper.

Only one concrete nested config should be set, based on `Type`.

Examples:

1. `type: git` with `git:` populated.
2. `type: confluence` with `confluence:` populated.

#### `GitCfg`

```go
type GitCfg struct {
	URL         string            `yaml:"url"`
	Branch      string            `yaml:"branch"`
	Dirs        []string          `yaml:"dirList"`
	IgnoreDirs  []string          `yaml:"ignoreDirList"`
	Credentials credentialsConfig `yaml:"credentials"`
}
```

This struct controls Git sync behavior.

Important fields:

1. `URL`: repository URL.
2. `Branch`: branch to inspect.
3. `Dirs`: the only directory prefixes included in snapshots.
4. `IgnoreDirs`: excluded prefixes, even if they sit under `Dirs`.
5. `Credentials`: location of secret-backed Git auth data.

#### `credentialsConfig`

```go
type credentialsConfig struct {
	Type string
	Name string
	Path string `yaml:"path"`
}
```

This defines how credentials are resolved.

Current implementation expects:

1. `Type = secret`
2. `Name = mounted secret folder or file name`
3. `Path = base directory where the secret is mounted`

### Example Config

```yaml
- name: perfecto-doc
  type: doc
  id: perfecto-doc-12451
  description: "this is a sample doc sync"
  sync:
   enabled: true
   mode: local-agent
   agentType: py-quant-ai
   sharedVolume:
    path: $HOME/Desktop/
   schedule: "* * * * *"
   sources:
    - type: git
      name: basic-demo
      git:
       url: "https://github.com/ImMnan/doc-test.git"
       branch: main
       dirList: ["content/Perfecto"]
       ignoreDirList: ["content/Perfecto/rest-api"]
       credentials:
        type: secret
        name: git-secret-token
        path: $HOME
```

## Program Flow

### Startup Flow

The startup sequence is:

1. `main()` calls `pkg.Run()`.
2. `Run()` calls `init_hawk()`.
3. `init_hawk()` reads the YAML config file.
4. `Run()` starts one goroutine per `Config` item.
5. Each goroutine calls `sync(cfg)`.

Simplified view:

```text
main()
  -> pkg.Run()
     -> init_hawk()
     -> for each config
        -> go sync(config)
```

### Scheduling Flow

Each worker sets up a cron trigger.

Flow:

1. `sync(c)` verifies that sync is enabled.
2. `syncTrigger(c)` creates a cron scheduler.
3. The cron job sends timestamps into a channel.
4. `sync(c)` loops over the trigger channel.
5. On each tick, all configured sources are processed.

This design decouples scheduling from source logic.

## Why the `Source` Interface Exists

The `Source` interface is defined in `pkg/sync.go`:

```go
type Source interface {
	Validate() error
	Fetch(sharedVolumePath string) error
}
```

This is the contract every source type must satisfy.

### Why use an interface here?

Without an interface, `sync()` would need type-specific branching everywhere. That becomes messy as more sources are added.

With the interface:

1. `sync()` stays generic.
2. Source-specific logic lives in the source implementation.
3. Adding a new source only requires:
  1. a new config type,
  2. a new handler,
  3. a branch in `newSource()`.

### When the interface is used

The interface is used in this sequence:

1. `sync()` iterates `syncCfg.Sources`.
2. `newSource(sourceConfig)` inspects `source.Type`.
3. It returns a `Source` implementation.
4. `sync()` calls:
  1. `handler.Validate()`
  2. `handler.Fetch(syncCfg.SharedVolume.Path)`

That means `sync()` does not need to know whether a handler is Git, Confluence, or another future source.

## Source Construction

`newSource()` is the source factory.

Current behavior:

1. `type: git` returns a `gitSource`.
2. `type: confluence` returns a `confluenceSource`.
3. Any other type returns an error.

This pattern centralizes source creation and keeps the rest of the runtime code simpler.

## Git Source Design

The Git implementation lives in `pkg/git.go`.

### `gitSource`

```go
type gitSource struct {
	cfg        GitCfg
	sourceName string
}
```

This is the concrete implementation of the `Source` interface for Git.

Why store both fields?

1. `cfg` contains Git connection and filtering behavior.
2. `sourceName` retains the source identifier from YAML.

Even though the current export path no longer uses `sourceName`, it is still part of the source model and debug flow.

### `gitSource.Validate()`

This checks only the minimum required config:

1. `git.url` must be present.
2. `git.branch` must be present.

The validation is intentionally light right now.

### `gitSource.Fetch(sharedVolumePath)`

This is the main entry point for Git work.

It delegates to `gitSync()`.

## Git Sync Flow

`gitSync()` is the coordinator for a Git source run.

Current sequence:

1. Use a hardcoded previous commit SHA.
2. Build a snapshot for the latest branch commit via `gitGetLatestCommit()`.
3. Build a snapshot for the previous commit via `gitGetLastCommit()`.
4. Diff both snapshots via `gitDiff()`.
5. Export changed files via `writeChangedFilesFromCommits()`.
6. Return a JSON-encoded diff result.

### About the hardcoded last commit

At the moment, previous-commit lookup is not integrated with a database or state store.

So this line is intentionally temporary:

```go
lastCommitSHA := "87fd6b8d0f56e5f5bad2c887585e9288f2adae93"
```

Later, that value can be replaced by a call to another function without rewriting the rest of the sync pipeline.

## Commit Snapshot Model

The snapshot structs are central to how diffing works.

### `gitFileSnapshot`

```go
type gitFileSnapshot struct {
	Path    string `json:"path"`
	BlobSHA string `json:"blobSha"`
	Mode    string `json:"mode"`
}
```

This represents one file inside one commit tree.

Why these fields matter:

1. `Path`: the logical file identity.
2. `BlobSHA`: the file content identity.
3. `Mode`: file mode metadata.

### `gitCommitSnapshot`

```go
type gitCommitSnapshot struct {
	RepoURL    string            `json:"repoUrl"`
	Branch     string            `json:"branch"`
	CommitSHA  string            `json:"commitSha"`
	CommitTime string            `json:"commitTime"`
	Files      []gitFileSnapshot `json:"files"`
}
```

This is the serialized representation of a commit used by `gitDiff()`.

Why encode snapshots as JSON bytes?

1. It keeps the function boundaries simple.
2. It makes the output portable.
3. It allows snapshots to stay in memory for now.
4. It can later be persisted or transmitted without redesigning the model.

## Latest Commit Retrieval

`gitGetLatestCommit(source GitCfg) ([]byte, error)` does this:

1. Resolve Git auth.
2. Clone the repository into in-memory storage.
3. Resolve the configured branch head.
4. Read the commit tree.
5. Filter files according to `dirList` and `ignoreDirList`.
6. Build a `gitCommitSnapshot`.
7. Marshal it to JSON bytes.

Notable implementation detail:

1. It uses `memory.NewStorage()` and `memfs.New()`, so no working tree is written to disk during clone.

## Previous Commit Retrieval

`gitGetLastCommit(source GitCfg, lastCommitId string) ([]byte, error)` follows the same structure as latest-commit retrieval, but it loads a specific commit SHA instead of branch head.

This means both latest and previous commit data share the same snapshot format. That is why `gitDiff()` can compare them cleanly.

## Path Filtering

The Git implementation applies directory filters before files enter the snapshot.

Functions involved:

1. `shouldIncludePath()`
2. `pathMatchesAny()`
3. `normalizeGitPath()`

### How filtering works

For a file to be included:

1. It must match at least one `dirList` prefix if `dirList` is not empty.
2. It must not match any `ignoreDirList` prefix.

### Why normalize paths?

`normalizeGitPath()` makes prefix matching deterministic by:

1. converting backslashes to forward slashes,
2. removing leading `./`,
3. removing leading `/`,
4. applying `path.Clean()`.

This prevents mismatches caused by path formatting differences.

## Diff Logic

`gitDiff(lastCommitData, latestCommitData)` compares two JSON snapshots in memory.

It works by:

1. decoding both snapshots,
2. building a path-to-blob map for the old snapshot,
3. building a path-to-blob map for the new snapshot,
4. identifying changed files,
5. identifying deleted files,
6. returning a `gitDiffResult` as JSON.

### `gitDiffResult`

```go
type gitDiffResult struct {
	BaseCommit       string   `json:"baseCommit"`
	TargetCommit     string   `json:"targetCommit"`
	ChangedFiles     []string `json:"changedFiles"`
	DeletedFiles     []string `json:"deletedFiles"`
	ExportedFiles    []string `json:"exportedFiles,omitempty"`
	ExportedOldFiles []string `json:"exportedOldFiles,omitempty"`
	ExportedNewFiles []string `json:"exportedNewFiles,omitempty"`
}
```

Interpretation:

1. `ChangedFiles`: files added or modified in the target commit.
2. `DeletedFiles`: files present in the base commit but missing in target.
3. `ExportedOldFiles`: actual old-version files written to disk.
4. `ExportedNewFiles`: actual new-version files written to disk.
5. `ExportedFiles`: combined list of both old and new exported paths.

## Git Authentication

Git auth is resolved by `resolveGitAuth()`.

Current supported auth model:

1. HTTPS
2. Secret-backed credentials
3. Username + token or username + password

### Secret resolution

Credential lookup uses:

1. `credentials.type`
2. `credentials.name`
3. `credentials.path`

The code expects either:

1. mounted files:
  1. `<path>/<name>/username`
  2. `<path>/<name>/token`
  3. `<path>/<name>/password`
2. or a bundled secret file at `<path>/<name>` with key-value pairs.

Example mounted layout:

```text
$HOME/git-secret-token/username
$HOME/git-secret-token/token
```

## Shared Volume Export Layout

This is the most important output for the downstream application.

Current layout:

```text
<sharedVolume.path>/<dirList>/<latestCommitSHA>/Old/<relative-path-within-dirList>
<sharedVolume.path>/<dirList>/<latestCommitSHA>/New/<relative-path-within-dirList>
```

### Example

With this config:

1. `sharedVolume.path = $HOME/Desktop`
2. `dirList = content/Perfecto`
3. `latestCommitSHA = abc123`

The exported directories become:

```text
$HOME/Desktop/content/Perfecto/abc123/Old
$HOME/Desktop/content/Perfecto/abc123/New
```

If the changed file is:

```text
content/Perfecto/guides/setup.md
```

Then HAWK writes:

```text
$HOME/Desktop/content/Perfecto/abc123/Old/guides/setup.md
$HOME/Desktop/content/Perfecto/abc123/New/guides/setup.md
```

### Why this layout exists

It satisfies two downstream needs:

1. preserve the logical content grouping from `dirList`,
2. preserve a stable pairing between old and new file versions.

The external application can walk the `Old` and `New` trees under a single commit folder and process file pairs by relative path.

## How the Export Function Works

`writeChangedFilesFromCommits()` does this:

1. Expand the configured shared volume path.
2. Clone the repo in memory.
3. Load the base commit tree.
4. Load the target commit tree.
5. For each changed file:
  1. find the best matching `dirList` prefix,
  2. compute the path relative to that dirList,
  3. write old file content into `Old/` if present,
  4. write new file content into `New/` if present.

Deleted files only appear under `Old/`.
Newly added files only appear under `New/`.

## How `splitByMatchedDir()` Is Used

This helper exists because the export layout is based on `dirList`.

It returns:

1. the matched `dirList` prefix,
2. the file path relative to that prefix.

Example:

1. file path: `content/Perfecto/guides/setup.md`
2. matched dir: `content/Perfecto`
3. relative path: `guides/setup.md`

That is how HAWK avoids writing duplicate path segments like:

```text
$HOME/Desktop/content/Perfecto/abc123/New/content/Perfecto/guides/setup.md
```

and instead writes:

```text
$HOME/Desktop/content/Perfecto/abc123/New/guides/setup.md
```

## Confluence Source

The Confluence handler is currently a scaffold.

What it currently does:

1. validates required config,
2. implements the `Source` interface,
3. returns `nil` from `confluenceSync()`.

Why it matters now:

1. It proves the interface design supports multiple source types.
2. It shows where additional source-specific logic will be added later.

## Database Structs

Database structs already exist in `pkg/run.go`:

1. `databaseConfig`
2. `databaseConnectionConfig`

These are not yet used by runtime code.

Their likely future role is:

1. persist the last processed commit,
2. store sync metadata,
3. store downstream processing results.

## Current Limitations

These are important to understand when extending the project.

1. The config file path in `init_hawk()` is currently hardcoded to a local development path, not `/etc/hawk/configlist.yaml`.
2. Previous commit SHA is hardcoded in `gitSync()`.
3. Git export reclones the repository for export instead of reusing an existing in-memory repo handle.
4. Confluence support is not implemented.
5. Database integration is not implemented.

## Recommended Next Steps

The natural next improvements are:

1. Replace hardcoded previous commit lookup with a stored state provider.
2. Persist run metadata and last processed commit to the database.
3. Reuse repo state between snapshot generation and export to avoid repeated clone cost.
4. Add a manifest file in each commit export folder describing old/new file pairs.
5. Implement Confluence source fetching using the same `Source` abstraction.

## Summary of Important Functions

### `Run()`

Starts the full runtime.

### `init_hawk()`

Loads and parses the YAML config.

### `sync()`

Runs the scheduled processing loop for one `Config` item.

### `syncTrigger()`

Creates the cron-backed trigger channel.

### `newSource()`

Constructs a concrete source handler from config.

### `gitSource.Validate()`

Checks Git config validity.

### `gitSource.Fetch()`

Starts Git sync for one source.

### `gitSync()`

Coordinates latest snapshot, previous snapshot, diff, and export.

### `gitGetLatestCommit()`

Builds the filtered snapshot for the latest branch head.

### `gitGetLastCommit()`

Builds the filtered snapshot for a specific older commit.

### `gitDiff()`

Computes changed and deleted files between snapshots.

### `writeChangedFilesFromCommits()`

Writes old/new changed file content into the shared volume.

### `resolveGitAuth()`

Builds HTTP auth from mounted secret data.

### `normalizeGitPath()`

Canonicalizes paths for consistent matching and export logic.

### `splitByMatchedDir()`

Maps a changed file back to its matched `dirList` base and relative path.

## Mental Model

The simplest mental model for HAWK today is:

1. config defines what to watch,
2. scheduler decides when to watch it,
3. source handlers know how to read a source,
4. Git handler turns commits into snapshots,
5. snapshots are diffed in memory,
6. only changed file pairs are exported for downstream AI processing.

That is the current architecture the rest of the project is building on.