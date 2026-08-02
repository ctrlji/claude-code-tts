package reader

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// writeFile creates a file and every directory above it.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// relPaths pulls the relative paths out of a listing, for easy comparison.
func relPaths(listing *DocListing) []string {
	out := make([]string, 0, len(listing.Files))
	for _, f := range listing.Files {
		out = append(out, f.Rel)
	}
	return out
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func TestListProjectDocsPlainDirectory(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "README.md"), "# The project\n\nHello.\n")
	writeFile(t, filepath.Join(root, "notes.txt"), "plain text\n")
	writeFile(t, filepath.Join(root, "docs", "design.md"), "# Design\n")
	writeFile(t, filepath.Join(root, "docs", "deep", "adr.markdown"), "# ADR 1\n")
	writeFile(t, filepath.Join(root, "main.go"), "package main\n")
	writeFile(t, filepath.Join(root, "node_modules", "dep", "README.md"), "# Dependency\n")
	writeFile(t, filepath.Join(root, ".hidden", "secret.md"), "# Hidden\n")

	listing, err := ListProjectDocs(root)
	if err != nil {
		t.Fatalf("ListProjectDocs: %v", err)
	}
	if listing.Git {
		t.Errorf("Git = true for a directory that is not a repository")
	}
	got := relPaths(listing)

	for _, want := range []string{"README.md", "notes.txt", "docs/design.md", "docs/deep/adr.markdown"} {
		if !contains(got, want) {
			t.Errorf("listing is missing %s; got %v", want, got)
		}
	}
	for _, unwanted := range []string{"main.go", "node_modules/dep/README.md", ".hidden/secret.md"} {
		if contains(got, unwanted) {
			t.Errorf("listing should not contain %s; got %v", unwanted, got)
		}
	}
}

func TestListProjectDocsOrderAndTitles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "zebra.md"), "no heading here\n")
	writeFile(t, filepath.Join(root, "Alpha.md"), "# Alpha title\n")
	writeFile(t, filepath.Join(root, "docs", "b.md"), "# B\n")

	listing, err := ListProjectDocs(root)
	if err != nil {
		t.Fatalf("ListProjectDocs: %v", err)
	}
	got := relPaths(listing)
	want := []string{"Alpha.md", "zebra.md", "docs/b.md"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order: got %v, want %v", got, want)
		}
	}

	byName := map[string]DocFile{}
	for _, f := range listing.Files {
		byName[f.Name] = f
	}
	if byName["Alpha.md"].Title != "Alpha title" {
		t.Errorf("Alpha.md title = %q, want %q", byName["Alpha.md"].Title, "Alpha title")
	}
	if byName["zebra.md"].Title != "" {
		t.Errorf("zebra.md has no heading, so its title should be empty; got %q", byName["zebra.md"].Title)
	}
	if byName["b.md"].Dir != "docs" {
		t.Errorf("b.md Dir = %q, want %q", byName["b.md"].Dir, "docs")
	}
	if byName["Alpha.md"].Dir != "" {
		t.Errorf("a root file should have an empty Dir; got %q", byName["Alpha.md"].Dir)
	}
}

func TestListProjectDocsHonoursGitignore(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".gitignore"), "ignored/\nsecret.md\n")
	writeFile(t, filepath.Join(root, "README.md"), "# Kept\n")
	writeFile(t, filepath.Join(root, "secret.md"), "# Ignored by name\n")
	writeFile(t, filepath.Join(root, "ignored", "inside.md"), "# Ignored by directory\n")
	writeFile(t, filepath.Join(root, "build", "generated.md"), "# Untracked but not ignored\n")

	for _, args := range [][]string{{"init"}, {"add", "README.md"}} {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v failed in this environment: %v (%s)", args, err, out)
		}
	}

	listing, err := ListProjectDocs(root)
	if err != nil {
		t.Fatalf("ListProjectDocs: %v", err)
	}
	if !listing.Git {
		t.Fatalf("Git = false inside a repository")
	}
	got := relPaths(listing)
	if !contains(got, "README.md") {
		t.Errorf("tracked file missing; got %v", got)
	}
	// Untracked but not ignored: a note you just wrote should be listed.
	if !contains(got, "build/generated.md") {
		t.Errorf("untracked-but-not-ignored file missing; got %v", got)
	}
	for _, unwanted := range []string{"secret.md", "ignored/inside.md"} {
		if contains(got, unwanted) {
			t.Errorf("gitignored %s should not be listed; got %v", unwanted, got)
		}
	}
}

func TestListProjectDocsRejectsFiles(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "README.md")
	writeFile(t, file, "# Hi\n")
	if _, err := ListProjectDocs(file); err == nil {
		t.Fatal("ListProjectDocs accepted a file path; want an error")
	}
	if _, err := ListProjectDocs(filepath.Join(root, "nope")); err == nil {
		t.Fatal("ListProjectDocs accepted a missing directory; want an error")
	}
}

func TestFirstHeadingSkipsCodeFences(t *testing.T) {
	root := t.TempDir()
	fenced := filepath.Join(root, "fenced.md")
	writeFile(t, fenced, "```sh\n# not a title\n```\n\n# The real title\n")
	if got := firstHeading(fenced); got != "The real title" {
		t.Errorf("firstHeading = %q, want %q", got, "The real title")
	}

	none := filepath.Join(root, "none.md")
	writeFile(t, none, "just prose\n")
	if got := firstHeading(none); got != "" {
		t.Errorf("firstHeading on a file with no heading = %q, want empty", got)
	}
	if got := DocumentTitle(none); got != "none.md" {
		t.Errorf("DocumentTitle should fall back to the file name; got %q", got)
	}
}
