package reader

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Discovery of the readable text files inside a project directory. The
// navigator uses this to offer a project's own documentation — its README,
// its notes, its design documents — without the reader having to be told a
// path. Reading a file that is found this way goes through the same /api/load
// path as `tts-ctl read notes.md`, so nothing about reading changes here.

// docExtensions are the file types the read-along view can render. Markdown
// first, because that is what these listings are for; plain text is included
// because the reader handles it just as well and a NOTES.txt is worth seeing.
var docExtensions = map[string]bool{
	".md":       true,
	".markdown": true,
	".mdx":      true,
	".txt":      true,
}

// docDenyDirs are directory names never worth walking into. They hold
// dependencies and build output, whose Markdown files (a dependency's README,
// for instance) are noise in a listing of *your* project's documents. This
// list only matters for projects that are not git repositories; in a git
// repository .gitignore already covers these and much more.
var docDenyDirs = map[string]bool{
	".git": true, ".hg": true, ".svn": true,
	"node_modules": true, "vendor": true, "bower_components": true,
	"dist": true, "build": true, "out": true, "target": true,
	".next": true, ".nuxt": true, ".venv": true, "venv": true,
	"__pycache__": true, ".tox": true, ".cache": true, ".idea": true,
}

// maxDocFiles bounds a listing. A repository with more readable text files
// than this is one where the filter box matters more than completeness, and
// an unbounded list would make the page slow to build for no benefit.
const maxDocFiles = 2000

// maxDocWalkDepth bounds how deep the non-git walk goes below the project
// root. Documentation lives near the top; a very deep tree is usually
// generated output that the deny-list did not happen to name.
const maxDocWalkDepth = 12

// DocFile is one readable text file inside a project.
type DocFile struct {
	Path     string `json:"path"`     // absolute path, which is what /api/load takes
	Rel      string `json:"rel"`      // path relative to the project directory
	Name     string `json:"name"`     // base name
	Dir      string `json:"dir"`      // relative directory holding it; "" at the root
	Title    string `json:"title"`    // first Markdown heading, when the file has one
	Size     int64  `json:"size"`     // bytes
	Modified int64  `json:"modified"` // last write time, in Unix milliseconds
}

// DocListing is every readable text file in one project directory, together
// with whether the listing was cut short by maxDocFiles.
type DocListing struct {
	Dir       string    `json:"dir"`
	Files     []DocFile `json:"files"`
	Truncated bool      `json:"truncated"`
	Git       bool      `json:"git"` // true when .gitignore was honoured
}

// ListProjectDocs finds the readable text files inside a project directory.
//
// In a git repository the file list comes from git itself, which honours
// .gitignore for free: without that, a listing is dominated by the Markdown
// files inside node_modules and vendor directories. Outside a repository it
// falls back to walking the tree with the deny-list above.
//
// Files are returned sorted by directory and then by name, with the project
// root's own files first, so the caller can build a tree by scanning once.
func ListProjectDocs(dir string) (*DocListing, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("not a directory: %s", abs)
	}

	rels, git := gitDocFiles(abs)
	if !git {
		rels = walkDocFiles(abs)
	}

	listing := &DocListing{Dir: abs, Git: git}
	if len(rels) > maxDocFiles {
		rels = rels[:maxDocFiles]
		listing.Truncated = true
	}
	for _, rel := range rels {
		full := filepath.Join(abs, rel)
		info, err := os.Stat(full)
		if err != nil || info.IsDir() {
			continue // raced with a delete, or a symlink to a directory
		}
		relDir := filepath.Dir(rel)
		if relDir == "." {
			relDir = ""
		}
		listing.Files = append(listing.Files, DocFile{
			Path:     full,
			Rel:      filepath.ToSlash(rel),
			Name:     filepath.Base(rel),
			Dir:      filepath.ToSlash(relDir),
			Title:    firstHeading(full),
			Size:     info.Size(),
			Modified: info.ModTime().UnixMilli(),
		})
	}
	sortDocFiles(listing.Files)
	return listing, nil
}

// sortDocFiles orders a listing the way the tree displays it: the project
// root's own files first, then each subdirectory in path order, and files
// within a directory by name, case-insensitively.
func sortDocFiles(files []DocFile) {
	sort.SliceStable(files, func(i, j int) bool {
		a, b := files[i], files[j]
		if a.Dir != b.Dir {
			if a.Dir == "" || b.Dir == "" {
				return a.Dir == ""
			}
			return a.Dir < b.Dir
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
}

// gitDocFiles lists a repository's readable text files through git, which
// applies .gitignore for us. It returns tracked files plus untracked files
// that are not ignored, so a brand-new note shows up before it is committed.
// The second return value reports whether git could answer at all.
func gitDocFiles(dir string) ([]string, bool) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, false
	}
	// --others adds untracked files and --exclude-standard drops the ignored
	// ones; together with --cached this is "every file git would show you".
	cmd := exec.Command("git", "-C", dir, "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	out, err := cmd.Output()
	if err != nil {
		return nil, false // not a repository, or git refused
	}
	seen := make(map[string]bool)
	var rels []string
	for _, rel := range strings.Split(string(out), "\x00") {
		if rel == "" || !isDocFile(rel) || seen[rel] {
			continue
		}
		// A submodule or a nested repository can report paths git-side that
		// climb out of the directory; refuse those outright.
		if strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
			continue
		}
		seen[rel] = true
		rels = append(rels, filepath.FromSlash(rel))
	}
	return rels, true
}

// walkDocFiles is the fallback for a project directory that is not a git
// repository: a bounded walk that skips the noisy directories by name.
func walkDocFiles(root string) []string {
	var rels []string
	var walk func(dir, rel string, depth int)
	walk = func(dir, rel string, depth int) {
		if depth > maxDocWalkDepth || len(rels) >= maxDocFiles {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			name := e.Name()
			child := filepath.Join(rel, name)
			if e.IsDir() {
				// Symlinked directories are skipped: following them can loop,
				// and can leave the project entirely.
				if docDenyDirs[name] || strings.HasPrefix(name, ".") || e.Type()&os.ModeSymlink != 0 {
					continue
				}
				walk(filepath.Join(dir, name), child, depth+1)
				continue
			}
			if isDocFile(name) {
				rels = append(rels, child)
				if len(rels) >= maxDocFiles {
					return
				}
			}
		}
	}
	walk(root, "", 0)
	return rels
}

func isDocFile(name string) bool {
	return docExtensions[strings.ToLower(filepath.Ext(name))]
}
