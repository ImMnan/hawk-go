package pkg

// this is component to proocess and handle git operations.
// get/read the reposiroty, branch, paths and credentials from the config and perform git operations accordingly.
// Compare the latest with the previous commit, last commit that was sent in the func.

type gitSource struct {
	cfg sourceConfig
}

func newGitSource(cfg sourceConfig) Source {
	return gitSource{cfg: cfg}
}

func (g gitSource) Fetch() error {
	_, err := gitSync(g.cfg)
	return err
}

func gitSync(source sourceConfig) ([]byte, error) {

	// use mongo libraries to get the last commit id for the name of the source, which is also source.Name
	// Use that last commit to start the gitGetlastCommit

	// Also initialise gitGetLatestCommit
	// Both the values of these 2 functions will be diff using gitDiff.

	// Once gitDiff returns the list of files that have changed,
	// retun these files - both lastcommit based files as well as newCommit based files.

	return nil, nil

}

func gitDiff([]byte, []byte) error {
	// This will do the diff between files, and return the files that have changed.

	return nil
}

func gitGetLatestCommit(source sourceConfig) ([]byte, error) {
	// get the lastest coomit from git and retun it
	// whats the best output/format to return the commit data?, we return the data in that format
	return nil, nil
}

func gitGetLastCommit(source sourceConfig, lastCommitId string) ([]byte, error) {

	// get the last coomit from git and retun it
	// whats the best output/format to return the commit data?, we return the data in that format

	return nil, nil

}
