package model

// Commit is one commit of a code repo's default branch, as the commit store keeps it.
type Commit struct {
	Sha       string  `json:"sha"`
	Title     string  `json:"title"`
	Author    *string `json:"author"`
	At        *string `json:"at"`
	Additions *int    `json:"additions"`
	Deletions *int    `json:"deletions"`
}

// CommitInfo is what a host says about one commit when its files are read.
type CommitInfo struct {
	Title  string  `json:"title"`
	Author *string `json:"author"`
	At     *string `json:"at"`
}

// HostFile is a file of a commit as a host gives it, before finishFiles trims the diffs.
type HostFile struct {
	Path      string  `json:"path"`
	OldPath   *string `json:"oldPath"`
	Status    string  `json:"status"` // added | deleted | renamed | modified
	Additions *int    `json:"additions,omitempty"`
	Deletions *int    `json:"deletions,omitempty"`
	Patch     *string `json:"patch"`
	TooLarge  bool    `json:"tooLarge,omitempty"`
}

// CommitPage is one page of the default branch's history, newest first. Next is the cursor of the next page ("" at the end).
type CommitPage struct {
	Items []Commit
	Next  string
}

// CIRun is one CI run (a GitHub Actions run, a GitLab pipeline), normalized the same way for both.
type CIRun struct {
	ID         string  `json:"id"`
	URL        string  `json:"url"`
	Name       string  `json:"name"`
	Path       string  `json:"path"`
	Event      string  `json:"event"`
	Actor      string  `json:"actor"`
	Branch     *string `json:"branch"`
	Sha        string  `json:"sha"`
	Status     string  `json:"status"`
	Conclusion *string `json:"conclusion"`
	At         string  `json:"at"`
	Category   string  `json:"category"`
}

// WorkflowChange is a commit that changed the CI definition.
type WorkflowChange struct {
	Sha    string  `json:"sha"`
	Title  string  `json:"title"`
	Author *string `json:"author"`
	At     *string `json:"at"`
	URL    *string `json:"url"`
}
