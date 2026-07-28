// Command audit_inventory creates reproducible path-level audit manifests for the Shield fork.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const tgSpamEquivalentCommit = "808353b26aa292d2a236ba314ea748fb0965251b"
const shieldAuditBaseline = "3f6313fa3f80dc568f20652bb93a6abb7ba3a506"

func main() {
	root := strings.TrimSpace(os.Getenv("SHIELD_AUDIT_ROOT"))
	if root == "" {
		var err error
		root, err = git("rev-parse", "--show-toplevel")
		must(err)
		root = strings.TrimSpace(root)
	} else {
		var err error
		root, err = filepath.Abs(root)
		must(err)
	}

	implementationTree, err := git("write-tree")
	must(err)
	implementationTree = strings.TrimSpace(implementationTree)

	pathsText, err := git("ls-tree", "-r", "--name-only", implementationTree)
	must(err)
	paths := nonEmptyLines(pathsText)
	sort.Strings(paths)

	must(os.MkdirAll(filepath.Join(root, "audit"), 0o750))
	must(os.WriteFile(filepath.Join(root, "audit", "paths-head.txt"), []byte(strings.Join(paths, "\n")+"\n"), 0o600))

	var classification strings.Builder
	classification.WriteString("path\tcategory\taudit_method\n")
	counts := map[string]int{}
	for _, path := range paths {
		category, method := classify(path)
		counts[category]++
		fmt.Fprintf(&classification, "%s\t%s\t%s\n", path, category, method)
	}
	must(os.WriteFile(filepath.Join(root, "audit", "classification.tsv"), []byte(classification.String()), 0o600))

	writeGitOutput(root, "changed-since-tg-spam.tsv",
		"diff-tree", "-r", "--name-status", "-M", tgSpamEquivalentCommit, implementationTree, "--")
	writeGitOutput(root, "changed-since-tg-spam-numstat.tsv",
		"diff-tree", "-r", "--numstat", tgSpamEquivalentCommit, implementationTree, "--")
	writeGitOutput(root, "changed-dirstat.txt",
		"diff-tree", "-r", "--dirstat=files,0", tgSpamEquivalentCommit, implementationTree, "--")
	writeGitOutput(root, "implementation-status.txt",
		"diff-tree", "-r", "--name-status", "-M", shieldAuditBaseline, implementationTree, "--")

	tree, err := git("rev-parse", shieldAuditBaseline+"^{tree}")
	must(err)
	var summary strings.Builder
	fmt.Fprintf(&summary, "baseline_head=%s\n", shieldAuditBaseline)
	fmt.Fprintf(&summary, "baseline_tree=%s\n", strings.TrimSpace(tree))
	fmt.Fprintf(&summary, "implementation_tree=%s\n", implementationTree)
	fmt.Fprintf(&summary, "tg_spam_equivalent_commit=%s\n", tgSpamEquivalentCommit)
	fmt.Fprintf(&summary, "total_paths=%d\n", len(paths))
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Fprintf(&summary, "category_%s=%d\n", key, counts[key])
	}
	must(os.WriteFile(filepath.Join(root, "audit", "summary.txt"), []byte(summary.String()), 0o600))
}

func classify(path string) (category, method string) {
	switch {
	case strings.HasPrefix(path, "vendor/"):
		return "third_party_vendor", "module checksum and manifest review; external scanners not run locally"
	case strings.HasPrefix(path, "docs/") || strings.HasPrefix(path, "site/") ||
		strings.HasSuffix(path, ".md"):
		return "documentation", "content and unsafe-instruction review"
	case strings.HasPrefix(path, ".github/") || strings.HasPrefix(path, "updater/") ||
		strings.HasPrefix(path, "docker-compose") || path == "Dockerfile" ||
		path == "entrypoint.sh" || path == "Makefile" || path == "go.mod" || path == "go.sum":
		return "build_config_supply_chain", "manual trust-boundary review and static checks"
	case isAsset(path):
		return "asset_or_generated", "type, provenance, secret and distribution review"
	default:
		return "first_party_source", "manual risk review, tests, race detector, vet and static analysis"
	}
}

func isAsset(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".db", ".sqlite", ".png", ".jpg", ".jpeg", ".gif", ".ico", ".woff", ".woff2", ".svg":
		return true
	default:
		return false
	}
}

func writeGitOutput(root, name string, args ...string) {
	output, err := git(args...)
	must(err)
	if output != "" && !strings.HasSuffix(output, "\n") {
		output += "\n"
	}
	must(os.WriteFile(filepath.Join(root, "audit", name), []byte(output), 0o600))
}

func git(args ...string) (string, error) {
	//nolint:gosec // the executable is fixed and callers provide only repository-local git arguments.
	cmd := exec.Command("git", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func nonEmptyLines(value string) []string {
	lines := strings.Split(strings.TrimSpace(value), "\n")
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		if line = strings.TrimSpace(line); line != "" {
			result = append(result, line)
		}
	}
	return result
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
