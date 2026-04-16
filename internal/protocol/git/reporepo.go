// Package git — bare-repo lifecycle hooks for type="git" repos (D-38).
// OnRepoCreate seeds a bare repo on disk + HEAD ref row.
// OnRepoDelete soft-moves the bare-repo dir to trash.
package git
