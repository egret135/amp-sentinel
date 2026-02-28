package amp

// ReadOnlyPermissions returns Amp permission rules that enforce strict read-only
// access. This is the safety foundation of Amp Sentinel — code must never be
// modified during diagnosis.
func ReadOnlyPermissions() []string {
	return []string{
		// ===== Allow: read-only tools =====
		`allow Read`,
		`allow Grep`,
		`allow glob`,
		`allow finder`,
		`allow web_search`,
		`allow read_web_page`,
		`allow librarian`,
		`allow oracle`,

		// ===== Reject: all write tools =====
		`reject edit_file`,
		`reject create_file`,
		`reject undo_edit`,

		// ===== Bash: allow read-only commands =====
		`allow Bash --cmd "cat *"`,
		`allow Bash --cmd "head *"`,
		`allow Bash --cmd "tail *"`,
		`allow Bash --cmd "grep *"`,
		`allow Bash --cmd "wc *"`,
		`allow Bash --cmd "ls *"`,
		`allow Bash --cmd "tree *"`,
		`allow Bash --cmd "file *"`,
		`allow Bash --cmd "git log *"`,
		`allow Bash --cmd "git show *"`,
		`allow Bash --cmd "git diff *"`,
		`allow Bash --cmd "git blame *"`,
		`allow Bash --cmd "git branch *"`,
		`allow Bash --cmd "git tag *"`,
		`allow Bash --cmd "git status *"`,
		`allow Bash --cmd "go doc *"`,
		`allow Bash --cmd "java -version*"`,
		`allow Bash --cmd "python --version*"`,
		`allow Bash --cmd "node --version*"`,

		// ===== Bash: reject dangerous commands =====
		`reject Bash --cmd "git commit*"`,
		`reject Bash --cmd "git push*"`,
		`reject Bash --cmd "git add*"`,
		`reject Bash --cmd "git checkout*"`,
		`reject Bash --cmd "git reset*"`,
		`reject Bash --cmd "git merge*"`,
		`reject Bash --cmd "git rebase*"`,
		`reject Bash --cmd "git stash*"`,
		`reject Bash --cmd "rm *"`,
		`reject Bash --cmd "mv *"`,
		`reject Bash --cmd "cp *"`,
		`reject Bash --cmd "chmod *"`,
		`reject Bash --cmd "chown *"`,
		`reject Bash --cmd "sed *"`,
		`reject Bash --cmd "awk *"`,
		`reject Bash --cmd "dd *"`,
		`reject Bash --cmd "tee *"`,
		`reject Bash --cmd "truncate *"`,
		`reject Bash --cmd "curl -X PUT*"`,
		`reject Bash --cmd "curl -X POST*"`,
		`reject Bash --cmd "curl -X DELETE*"`,
		`reject Bash --cmd "curl -X PATCH*"`,
		`reject Bash --cmd "wget *"`,

		// ===== Reject: subagent spawning (prevent write escalation) =====
		`reject Task`,
		`reject handoff`,

		// ===== Catch-all: reject any Bash command not explicitly allowed above =====
		`reject Bash`,
	}
}
