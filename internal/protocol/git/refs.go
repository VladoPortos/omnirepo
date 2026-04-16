// Package git — refs walker for the post-ReceivePack hook (D-37) and ref
// classification (P13). Opens the bare repo via go-git PlainOpen, iterates
// all refs, classifies each, then atomically replaces the git_refs rows.
package git
