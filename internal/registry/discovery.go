package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

const (
	DiscoveryTypeRepository = "repository"
	DiscoveryTypeWorktree   = "worktree"
	DiscoveryTypeBare       = "bare"
	DiscoveryTypeBroken     = "broken"
	DiscoveryStateUnknown   = "unknown"
)

// DiscoveryOptions are hard scan bounds. Zero values are replaced by the
// conservative defaults returned by DefaultDiscoveryOptions.
type DiscoveryOptions struct {
	MaxDepth       int  `json:"maxDepth"`
	MaxDirectories int  `json:"maxDirectories"`
	MaxResults     int  `json:"maxResults"`
	TimeoutSeconds int  `json:"timeoutSeconds"`
	FindNested     bool `json:"findNested"`
}

func DefaultDiscoveryOptions() DiscoveryOptions {
	return DiscoveryOptions{MaxDepth: 10, MaxDirectories: 25000, MaxResults: 2000, TimeoutSeconds: 300}
}

func (o DiscoveryOptions) Validate() error {
	if o.MaxDepth < 1 || o.MaxDepth > 64 {
		return fmt.Errorf("maximum depth must be between 1 and 64")
	}
	if o.MaxDirectories < 1 || o.MaxDirectories > 1_000_000 {
		return fmt.Errorf("maximum directories must be between 1 and 1000000")
	}
	if o.MaxResults < 1 || o.MaxResults > 100_000 {
		return fmt.Errorf("maximum results must be between 1 and 100000")
	}
	if o.TimeoutSeconds < 1 || o.TimeoutSeconds > 86400 {
		return fmt.Errorf("scan timeout must be between 1 and 86400 seconds")
	}
	return nil
}

func normalizeDiscoveryOptions(o DiscoveryOptions) (DiscoveryOptions, error) {
	defaults := DefaultDiscoveryOptions()
	if o.MaxDepth == 0 {
		o.MaxDepth = defaults.MaxDepth
	}
	if o.MaxDirectories == 0 {
		o.MaxDirectories = defaults.MaxDirectories
	}
	if o.MaxResults == 0 {
		o.MaxResults = defaults.MaxResults
	}
	if o.TimeoutSeconds == 0 {
		o.TimeoutSeconds = defaults.TimeoutSeconds
	}
	return o, o.Validate()
}

type DiscoveryResult struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	Path              string   `json:"path"`
	Type              string   `json:"type"`
	Branch            string   `json:"branch,omitempty"`
	Head              string   `json:"head,omitempty"`
	State             string   `json:"state"`
	WorktreeOf        string   `json:"worktreeOf,omitempty"`
	AlreadyRegistered bool     `json:"alreadyRegistered"`
	LastModified      string   `json:"lastModified,omitempty"`
	Accessible        bool     `json:"accessible"`
	Warnings          []string `json:"warnings"`
	DiscoveryRoot     string   `json:"discoveryRoot"`
	Hidden            bool     `json:"hidden"`
}

type DiscoveryProgress struct {
	Status              string `json:"status"`
	CurrentRoot         string `json:"currentRoot,omitempty"`
	DirectoriesExamined int    `json:"directoriesExamined"`
	RepositoriesFound   int    `json:"repositoriesFound"`
	RepositoriesSkipped int    `json:"repositoriesSkipped"`
	PermissionErrors    int    `json:"permissionErrors"`
	ElapsedMillis       int64  `json:"elapsedMillis"`
}

type DiscoverySummary struct {
	DiscoveryProgress
	StartedAt   string `json:"startedAt,omitempty"`
	FinishedAt  string `json:"finishedAt,omitempty"`
	LimitReason string `json:"limitReason,omitempty"`
}

type DiscoveryOutcome struct {
	Results      []DiscoveryResult `json:"results"`
	Summary      DiscoverySummary  `json:"summary"`
	ScannedRoots []string          `json:"-"`
}

type DiscoveryScanRequest struct {
	Roots            []string
	CustomExclusions []string
	ProtectedPaths   []string
	Options          DiscoveryOptions
	Registered       []Repo
	HiddenTokens     []string
	OnProgress       func(DiscoveryProgress)
}

type DiscoveryScanner struct{}

type discoveryWalk struct {
	ctx         context.Context
	req         DiscoveryScanRequest
	options     DiscoveryOptions
	started     time.Time
	progress    DiscoveryProgress
	results     []DiscoveryResult
	seenDirs    map[string]bool
	seenRepos   map[string]bool
	registered  map[string]bool
	hidden      map[string]bool
	exclusions  []DiscoveryExclusion
	roots       []string
	stop        bool
	limitReason string
}

func (DiscoveryScanner) Scan(parent context.Context, req DiscoveryScanRequest) (DiscoveryOutcome, error) {
	options, err := normalizeDiscoveryOptions(req.Options)
	if err != nil {
		return DiscoveryOutcome{}, err
	}
	if len(req.Roots) == 0 {
		return DiscoveryOutcome{}, fmt.Errorf("at least one approved scan root is required")
	}
	canonicalRoots := make([]string, 0, len(req.Roots))
	seenRoots := map[string]bool{}
	for _, rawRoot := range req.Roots {
		root, rootErr := CanonicalScanRoot(rawRoot, req.ProtectedPaths...)
		if rootErr != nil {
			return DiscoveryOutcome{}, rootErr
		}
		if !seenRoots[root] {
			seenRoots[root] = true
			canonicalRoots = append(canonicalRoots, root)
		}
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(options.TimeoutSeconds)*time.Second)
	defer cancel()
	now := time.Now().UTC()
	w := &discoveryWalk{ctx: ctx, req: req, options: options, started: now,
		seenDirs: map[string]bool{}, seenRepos: map[string]bool{}, registered: map[string]bool{}, hidden: map[string]bool{}, roots: canonicalRoots}
	for _, repo := range req.Registered {
		w.registered[repo.Path] = true
	}
	for _, id := range req.HiddenTokens {
		w.hidden[id] = true
	}
	home, _ := os.UserHomeDir()
	w.exclusions = DefaultDiscoveryExclusions(home, req.ProtectedPaths...)
	for _, custom := range req.CustomExclusions {
		custom = strings.TrimSpace(custom)
		if custom == "" {
			continue
		}
		if filepath.IsAbs(custom) {
			w.exclusions = append(w.exclusions, DiscoveryExclusion{ID: exclusionID("custom-path:" + filepath.Clean(custom)), Label: custom, Path: filepath.Clean(custom)})
		} else if filepath.Base(custom) == custom && custom != "." && custom != ".." {
			w.exclusions = append(w.exclusions, DiscoveryExclusion{ID: exclusionID("custom-name:" + custom), Label: custom, Basename: custom})
		} else {
			return DiscoveryOutcome{}, fmt.Errorf("invalid custom exclusion %q", custom)
		}
	}
	w.progress.Status = "running"
	w.emit()
	scannedRoots := make([]string, 0, len(canonicalRoots))
	rootFailures := 0
	for _, root := range canonicalRoots {
		w.progress.CurrentRoot = root
		w.emit()
		rootFS, openErr := openDiscoveryRoot(root)
		if openErr != nil {
			rootFailures++
			w.noteAccessError(openErr)
			w.emit()
			continue
		}
		info, statErr := rootFS.Stat(".")
		if statErr != nil {
			rootFS.Close()
			rootFailures++
			w.noteAccessError(statErr)
			w.emit()
			continue
		}
		scannedRoots = append(scannedRoots, root)
		rootMountID := uint64(0)
		if rootFile, fileErr := rootFS.Open("."); fileErr == nil {
			rootMountID = mountIDOfFile(rootFile)
			_ = rootFile.Close()
		}
		w.walkDir(rootFS, root, root, 0, deviceOf(info), rootMountID)
		_ = rootFS.Close()
		if w.stop || w.ctx.Err() != nil {
			break
		}
	}
	for i := range w.results {
		matches := 0
		for _, root := range canonicalRoots {
			if pathWithin(root, w.results[i].Path) {
				matches++
			}
		}
		if matches > 1 {
			w.results[i].Warnings = append(w.results[i].Warnings, "duplicate canonical repository matched overlapping scan roots; one result retained")
			w.progress.RepositoriesSkipped += matches - 1
		}
	}
	sort.Slice(w.results, func(i, j int) bool { return w.results[i].Path < w.results[j].Path })
	status := "completed"
	var scanErr error
	if err := w.ctx.Err(); err != nil {
		scanErr = err
		if errors.Is(err, context.DeadlineExceeded) {
			status = "timed_out"
		} else {
			status = "cancelled"
		}
	} else if w.limitReason != "" {
		status = "bounded"
	} else if rootFailures > 0 && len(scannedRoots) == 0 {
		status = "failed"
		scanErr = fmt.Errorf("all approved scan roots became inaccessible")
	}
	w.progress.Status = status
	w.progress.CurrentRoot = ""
	w.emit()
	finished := time.Now().UTC()
	return DiscoveryOutcome{Results: w.results, ScannedRoots: scannedRoots, Summary: DiscoverySummary{
		DiscoveryProgress: w.progress,
		StartedAt:         now.Format(time.RFC3339), FinishedAt: finished.Format(time.RFC3339), LimitReason: w.limitReason,
	}}, scanErr
}

func (w *discoveryWalk) emit() {
	w.progress.ElapsedMillis = time.Since(w.started).Milliseconds()
	if w.req.OnProgress != nil {
		w.req.OnProgress(w.progress)
	}
}

func (w *discoveryWalk) walkDir(rootFS *os.Root, root, path string, depth int, rootDevice, rootMountID uint64) {
	if w.stop || w.ctx.Err() != nil {
		return
	}
	if depth > w.options.MaxDepth {
		return
	}
	canonical := filepath.Clean(path)
	if !pathWithin(root, canonical) {
		return
	}
	if w.seenDirs[canonical] {
		return
	}
	if w.progress.DirectoriesExamined >= w.options.MaxDirectories {
		w.stop, w.limitReason = true, "maximum directories reached"
		return
	}
	if w.excluded(canonical) {
		return
	}
	rel, err := filepath.Rel(root, canonical)
	if err != nil {
		return
	}
	f, info, err := openDiscoveryPath(rootFS, rel, true)
	if err != nil {
		w.noteAccessError(err)
		return
	}
	defer f.Close()
	if rootDevice != 0 && deviceOf(info) != 0 && deviceOf(info) != rootDevice {
		return
	}
	w.seenDirs[canonical] = true
	w.progress.DirectoriesExamined++
	if w.progress.DirectoriesExamined == 1 || w.progress.DirectoriesExamined%64 == 0 {
		// Cooperatively yield between bounded metadata batches so a scan cannot
		// monopolize the local server or disk scheduler on a low-resource
		// machine. This avoids mutating process-wide OS priority.
		runtime.Gosched()
		time.Sleep(time.Millisecond)
		w.emit()
	}

	if mountID := mountIDOfFile(f); rootMountID != 0 && mountID != 0 && mountID != rootMountID {
		return
	}
	markers := map[string]os.DirEntry{}
	children := make([]string, 0)
	childLimit := false
	remaining := w.options.MaxDirectories - w.progress.DirectoriesExamined
	for {
		batch, readErr := f.ReadDir(128)
		for _, entry := range batch {
			switch entry.Name() {
			case ".git", "HEAD", "objects", "refs", "packed-refs":
				markers[entry.Name()] = entry
			}
			if entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			if entry.Name() == ".git" || !entry.IsDir() {
				continue
			}
			if len(children) < remaining {
				children = append(children, entry.Name())
			} else {
				childLimit = true
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			w.noteAccessError(readErr)
			return
		}
		if w.ctx.Err() != nil {
			return
		}
	}
	found, result := w.detect(root, canonical, info, markers)
	if found {
		if !w.seenRepos[result.Path] {
			w.seenRepos[result.Path] = true
			result.AlreadyRegistered = w.registered[result.Path]
			result.Hidden = w.hidden[result.ID]
			w.results = append(w.results, result)
			w.progress.RepositoriesFound++
			w.emit()
			if len(w.results) >= w.options.MaxResults {
				w.stop, w.limitReason = true, "maximum results reached"
				return
			}
		} else {
			w.progress.RepositoriesSkipped++
		}
		// A bare repository contains Git's object database directly. Descending
		// into it cannot find source-tree nested repositories and can explode the
		// scan across object fanout directories, so it always remains a leaf.
		if !w.options.FindNested || result.Type == DiscoveryTypeBare {
			return
		}
	}
	sort.Strings(children)
	for _, name := range children {
		if w.stop || w.ctx.Err() != nil {
			return
		}
		w.walkDir(rootFS, root, filepath.Join(canonical, name), depth+1, rootDevice, rootMountID)
	}
	if childLimit && !w.stop {
		w.stop, w.limitReason = true, "maximum directories reached"
	}
}

func (w *discoveryWalk) excluded(path string) bool {
	for _, exclusion := range w.exclusions {
		if exclusion.Basename != "" && filepath.Base(path) == exclusion.Basename {
			return true
		}
		if exclusion.Path != "" {
			candidate := filepath.Clean(exclusion.Path)
			if pathWithin(candidate, path) {
				return true
			}
		}
	}
	return false
}

func (w *discoveryWalk) noteAccessError(err error) {
	if errors.Is(err, os.ErrPermission) {
		w.progress.PermissionErrors++
	}
}

func (w *discoveryWalk) detect(scanRoot, path string, info os.FileInfo, byName map[string]os.DirEntry) (bool, DiscoveryResult) {
	typeName := ""
	warnings := []string{}
	worktreeOutside := false
	metadataDir := ""
	if gitEntry, ok := byName[".git"]; ok {
		switch {
		case gitEntry.Type()&os.ModeSymlink != 0:
			typeName = DiscoveryTypeBroken
			warnings = append(warnings, ".git symlink is not followed")
		case gitEntry.IsDir():
			typeName = DiscoveryTypeRepository
			metadataDir = filepath.Join(path, ".git")
		default:
			target, outside, warning := validGitFile(filepath.Join(path, ".git"), w.roots)
			if target != "" {
				typeName = DiscoveryTypeWorktree
				metadataDir = target
				worktreeOutside = outside
				if warning != "" {
					warnings = append(warnings, warning)
				}
			} else {
				typeName = DiscoveryTypeBroken
				warnings = append(warnings, warning)
			}
		}
	} else if looksBare(byName) {
		typeName = DiscoveryTypeBare
		metadataDir = path
	}
	if typeName == "" {
		return false, DiscoveryResult{}
	}
	result := DiscoveryResult{ID: discoveryID(path), Name: filepath.Base(path), Path: path, Type: typeName,
		State: DiscoveryStateUnknown, LastModified: info.ModTime().UTC().Format(time.RFC3339), DiscoveryRoot: scanRoot, Warnings: warnings}
	if typeName == DiscoveryTypeBroken {
		return true, result
	}
	if worktreeOutside {
		// Registering this worktree would make later Git observation follow
		// metadata beyond the explicitly approved boundary. Keep it visible for
		// owner review, but fail closed for discovery-based registration.
		result.Accessible = false
		return true, result
	}
	branch, head, commonDir, metadataWarnings, err := readDiscoveryGitMetadata(w.roots, metadataDir)
	result.Warnings = append(result.Warnings, metadataWarnings...)
	if err != nil {
		result.Type = DiscoveryTypeBroken
		result.Warnings = append(result.Warnings, "Git metadata is missing, unsafe, or unreadable")
		return true, result
	}
	result.Accessible = true
	result.Branch = branch
	result.Head = head
	if typeName == DiscoveryTypeWorktree {
		result.WorktreeOf = commonDir
	}
	return true, result
}

func validGitFile(path string, approvedRoots []string) (string, bool, string) {
	b, err := readBoundedDiscoveryMetadata(approvedRoots, path, 4096)
	if err != nil {
		return "", false, ".git metadata file is unreadable or unsafe"
	}
	line := strings.TrimSpace(string(b))
	if !strings.HasPrefix(line, "gitdir: ") {
		return "", false, ".git metadata file has an invalid format"
	}
	target := strings.TrimSpace(strings.TrimPrefix(line, "gitdir: "))
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(path), target)
	}
	target = filepath.Clean(target)
	if discoveryRootFor(approvedRoots, target) == "" {
		// Do not stat or resolve the untrusted external target. The owner can
		// see that this is a worktree, but discovery cannot inspect or add it.
		return target, true, ".git metadata target is outside approved scan roots; Git metadata was not opened"
	}
	if err := validateDiscoveryMetadataDir(approvedRoots, target); err != nil {
		return "", false, ".git metadata target is missing or unsafe"
	}
	return target, false, ""
}

const maxDiscoveryGitMetadataBytes = 1 << 20

var errUnsafeDiscoveryMetadata = errors.New("unsafe discovery metadata")

func readDiscoveryGitMetadata(approvedRoots []string, metadataDir string) (branch, head, commonDir string, warnings []string, err error) {
	if err := validateDiscoveryMetadataDir(approvedRoots, metadataDir); err != nil {
		return "", "", "", nil, err
	}
	commonDir = metadataDir
	commondirPath := filepath.Join(metadataDir, "commondir")
	if b, readErr := readBoundedDiscoveryMetadata(approvedRoots, commondirPath, 4096); readErr == nil {
		candidate := strings.TrimSpace(string(b))
		if candidate == "" {
			return "", "", "", nil, fmt.Errorf("empty commondir metadata")
		}
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(metadataDir, filepath.FromSlash(candidate))
		}
		candidate = filepath.Clean(candidate)
		if discoveryRootFor(approvedRoots, candidate) == "" {
			return "", "", "", nil, fmt.Errorf("commondir metadata escapes approved roots")
		}
		if err := validateDiscoveryMetadataDir(approvedRoots, candidate); err != nil {
			return "", "", "", nil, err
		}
		commonDir = candidate
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return "", "", "", nil, readErr
	}
	headBytes, err := readBoundedDiscoveryMetadata(approvedRoots, filepath.Join(metadataDir, "HEAD"), 4096)
	if err != nil {
		return "", "", "", nil, err
	}
	headLine := strings.TrimSpace(string(headBytes))
	if strings.HasPrefix(headLine, "ref: ") {
		ref := strings.TrimSpace(strings.TrimPrefix(headLine, "ref: "))
		if !validDiscoveryRef(ref) {
			return "", "", "", nil, fmt.Errorf("invalid HEAD ref")
		}
		branch = strings.TrimPrefix(ref, "refs/heads/")
		if branch == ref {
			branch = ""
		}
		refBytes, readErr := readBoundedDiscoveryMetadata(approvedRoots, filepath.Join(commonDir, filepath.FromSlash(ref)), 4096)
		if readErr == nil {
			head = shortDiscoveryOID(strings.TrimSpace(string(refBytes)))
		} else if !errors.Is(readErr, os.ErrNotExist) {
			return "", "", "", nil, readErr
		} else if packed, packedErr := readBoundedDiscoveryMetadata(approvedRoots, filepath.Join(commonDir, "packed-refs"), maxDiscoveryGitMetadataBytes); packedErr == nil {
			head = packedDiscoveryOID(string(packed), ref)
		} else if !errors.Is(packedErr, os.ErrNotExist) {
			return "", "", "", nil, packedErr
		}
		return branch, head, commonDir, warnings, nil
	}
	head = shortDiscoveryOID(headLine)
	if head == "" {
		return "", "", "", nil, fmt.Errorf("invalid detached HEAD")
	}
	return "", head, commonDir, warnings, nil
}

func validateDiscoveryMetadataDir(approvedRoots []string, path string) error {
	path = filepath.Clean(path)
	rootPath := discoveryRootFor(approvedRoots, path)
	if rootPath == "" {
		return fmt.Errorf("metadata directory escapes approved roots")
	}
	root, err := openDiscoveryRoot(rootPath)
	if err != nil {
		return err
	}
	defer root.Close()
	rel, err := filepath.Rel(rootPath, path)
	if err != nil {
		return err
	}
	dirFile, info, err := openDiscoveryPath(root, rel, true)
	if err != nil {
		return fmt.Errorf("metadata directory is missing or unsafe")
	}
	defer dirFile.Close()
	rootInfo, rootErr := root.Stat(".")
	if rootErr != nil || !sameDiscoveryDevice(rootInfo, info) {
		return fmt.Errorf("metadata directory crosses a filesystem boundary")
	}
	rootFile, rootFileErr := root.Open(".")
	if rootFileErr != nil {
		if rootFile != nil {
			_ = rootFile.Close()
		}
		return fmt.Errorf("metadata directory boundary is unavailable")
	}
	rootMountID, dirMountID := mountIDOfFile(rootFile), mountIDOfFile(dirFile)
	_ = rootFile.Close()
	_ = dirFile.Close()
	if rootMountID != 0 && dirMountID != 0 && rootMountID != dirMountID {
		return fmt.Errorf("metadata directory crosses a mount boundary")
	}
	return nil
}

func readBoundedDiscoveryMetadata(approvedRoots []string, path string, limit int64) ([]byte, error) {
	path = filepath.Clean(path)
	rootPath := discoveryRootFor(approvedRoots, path)
	if rootPath == "" {
		return nil, fmt.Errorf("metadata path is outside approved roots")
	}
	root, err := openDiscoveryRoot(rootPath)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	rel, err := filepath.Rel(rootPath, path)
	if err != nil {
		return nil, err
	}
	f, info, err := openDiscoveryPath(root, rel, false)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if info.Size() > limit {
		return nil, fmt.Errorf("%w: file exceeds limit", errUnsafeDiscoveryMetadata)
	}
	rootInfo, err := root.Stat(".")
	if err != nil || !sameDiscoveryDevice(rootInfo, info) {
		return nil, fmt.Errorf("%w: file crosses a filesystem boundary", errUnsafeDiscoveryMetadata)
	}
	rootFile, rootFileErr := root.Open(".")
	if rootFileErr != nil {
		return nil, rootFileErr
	}
	rootMountID, fileMountID := mountIDOfFile(rootFile), mountIDOfFile(f)
	_ = rootFile.Close()
	if rootMountID != 0 && fileMountID != 0 && rootMountID != fileMountID {
		return nil, fmt.Errorf("%w: file crosses a mount boundary", errUnsafeDiscoveryMetadata)
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, fmt.Errorf("%w: file exceeds limit", errUnsafeDiscoveryMetadata)
	}
	return b, nil
}

// openDiscoveryRoot resolves an absolute root one component at a time from a
// trusted filesystem/volume root. Every next component is opened relative to
// the retained parent descriptor and identity-checked before traversal
// continues, so an ancestor rename/symlink race cannot redirect the scan.
func openDiscoveryRoot(path string) (*os.Root, error) {
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("%w: discovery root is not absolute", errUnsafeDiscoveryMetadata)
	}
	volume := filepath.VolumeName(path)
	basePath := string(filepath.Separator)
	if volume != "" {
		basePath = volume + string(filepath.Separator)
	}
	base, err := os.OpenRoot(basePath)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(basePath, path)
	if err != nil {
		_ = base.Close()
		return nil, err
	}
	if rel == "." {
		return base, nil
	}
	nested, err := openDiscoveryRelativeRoot(base, rel)
	_ = base.Close()
	return nested, err
}

// openDiscoveryRelativeRoot walks only directory components. It never closes
// base; any intermediate descriptors it creates are closed before return.
func openDiscoveryRelativeRoot(base *os.Root, rel string) (*os.Root, error) {
	components, err := discoveryPathComponents(rel)
	if err != nil || len(components) == 0 {
		return nil, fmt.Errorf("%w: invalid directory path", errUnsafeDiscoveryMetadata)
	}
	current := base
	owned := false
	for _, component := range components {
		info, statErr := current.Lstat(component)
		if statErr != nil {
			if owned {
				_ = current.Close()
			}
			return nil, statErr
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			if owned {
				_ = current.Close()
			}
			return nil, fmt.Errorf("%w: directory component is a symlink or not a directory", errUnsafeDiscoveryMetadata)
		}
		next, openErr := current.OpenRoot(component)
		if openErr != nil {
			if owned {
				_ = current.Close()
			}
			return nil, openErr
		}
		openedInfo, openedErr := next.Stat(".")
		if openedErr != nil || !os.SameFile(info, openedInfo) {
			_ = next.Close()
			if owned {
				_ = current.Close()
			}
			return nil, fmt.Errorf("%w: directory component changed during open", errUnsafeDiscoveryMetadata)
		}
		if owned {
			_ = current.Close()
		}
		current = next
		owned = true
	}
	return current, nil
}

// openDiscoveryPath opens a final file/directory relative to a descriptor-
// anchored parent chain. Contents are not read until the final Lstat/Open
// identity check succeeds.
func openDiscoveryPath(root *os.Root, rel string, wantDir bool) (*os.File, os.FileInfo, error) {
	components, err := discoveryPathComponents(rel)
	if err != nil {
		return nil, nil, err
	}
	if len(components) == 0 {
		info, statErr := root.Stat(".")
		if statErr != nil {
			return nil, nil, statErr
		}
		file, openErr := root.Open(".")
		if openErr != nil {
			return nil, nil, openErr
		}
		openedInfo, openedErr := file.Stat()
		if openedErr != nil || !os.SameFile(info, openedInfo) || !openedInfo.IsDir() || !wantDir {
			_ = file.Close()
			return nil, nil, fmt.Errorf("%w: root changed or has wrong type", errUnsafeDiscoveryMetadata)
		}
		return file, openedInfo, nil
	}
	parent := root
	ownedParent := false
	if len(components) > 1 {
		parent, err = openDiscoveryRelativeRoot(root, filepath.Join(components[:len(components)-1]...))
		if err != nil {
			return nil, nil, err
		}
		ownedParent = true
	}
	if ownedParent {
		defer parent.Close()
	}
	name := components[len(components)-1]
	info, err := parent.Lstat(name)
	if err != nil {
		return nil, nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || (wantDir && !info.IsDir()) || (!wantDir && !info.Mode().IsRegular()) {
		return nil, nil, fmt.Errorf("%w: final path is a symlink or has wrong type", errUnsafeDiscoveryMetadata)
	}
	file, err := parent.Open(name)
	if err != nil {
		return nil, nil, err
	}
	openedInfo, err := file.Stat()
	if err != nil || !os.SameFile(info, openedInfo) || (wantDir && !openedInfo.IsDir()) || (!wantDir && !openedInfo.Mode().IsRegular()) {
		_ = file.Close()
		return nil, nil, fmt.Errorf("%w: final path changed during open", errUnsafeDiscoveryMetadata)
	}
	return file, openedInfo, nil
}

func discoveryPathComponents(rel string) ([]string, error) {
	rel = filepath.Clean(rel)
	if rel == "." {
		return []string{}, nil
	}
	if filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("%w: path escapes approved root", errUnsafeDiscoveryMetadata)
	}
	components := strings.Split(rel, string(filepath.Separator))
	for _, component := range components {
		if component == "" || component == "." || component == ".." {
			return nil, fmt.Errorf("%w: invalid path component", errUnsafeDiscoveryMetadata)
		}
	}
	return components, nil
}

func sameDiscoveryDevice(left, right os.FileInfo) bool {
	leftDevice, rightDevice := deviceOf(left), deviceOf(right)
	return leftDevice == 0 || rightDevice == 0 || leftDevice == rightDevice
}

func discoveryRootFor(roots []string, path string) string {
	best := ""
	for _, root := range roots {
		if pathWithin(root, path) && len(root) > len(best) {
			best = root
		}
	}
	return best
}

func validDiscoveryRef(ref string) bool {
	if !strings.HasPrefix(ref, "refs/") || strings.Contains(ref, "\\") || strings.Contains(ref, "..") || strings.ContainsAny(ref, "\x00\r\n ~^:?*[]") {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(ref)))
	return clean == ref && !strings.HasSuffix(ref, "/")
}

func shortDiscoveryOID(value string) string {
	if len(value) < 7 || len(value) > 64 {
		return ""
	}
	for _, char := range value {
		if !strings.ContainsRune("0123456789abcdefABCDEF", char) {
			return ""
		}
	}
	if len(value) > 12 {
		return value[:12]
	}
	return value
}

func packedDiscoveryOID(contents, ref string) string {
	for _, line := range strings.Split(contents, "\n") {
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "^") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) == 2 && parts[1] == ref {
			return shortDiscoveryOID(parts[0])
		}
	}
	return ""
}

// ValidateDiscoveryCandidate repeats the metadata-only safety checks at the
// final approval boundary. It prevents a repository or worktree from changing
// its Git metadata target between preview and registration.
func ValidateDiscoveryCandidate(path, expectedType string, approvedRoots []string) error {
	canonical := filepath.Clean(path)
	if !filepath.IsAbs(canonical) || discoveryRootFor(approvedRoots, canonical) == "" {
		return fmt.Errorf("repository path is no longer canonical inside an approved root")
	}
	if err := validateDiscoveryMetadataDir(approvedRoots, canonical); err != nil {
		return fmt.Errorf("repository directory is missing or unsafe")
	}
	metadataDir := ""
	switch expectedType {
	case DiscoveryTypeRepository:
		if err := validateDiscoveryMetadataDir(approvedRoots, filepath.Join(canonical, ".git")); err != nil {
			return fmt.Errorf("repository .git directory changed or is unsafe")
		}
		metadataDir = filepath.Join(canonical, ".git")
	case DiscoveryTypeWorktree:
		target, outside, _ := validGitFile(filepath.Join(canonical, ".git"), approvedRoots)
		if target == "" || outside {
			return fmt.Errorf("worktree metadata changed or escapes approved roots")
		}
		metadataDir = target
	case DiscoveryTypeBare:
		if _, headErr := readBoundedDiscoveryMetadata(approvedRoots, filepath.Join(canonical, "HEAD"), 4096); headErr != nil {
			return fmt.Errorf("bare repository metadata changed or is unsafe")
		}
		if objectsErr := validateDiscoveryMetadataDir(approvedRoots, filepath.Join(canonical, "objects")); objectsErr != nil {
			return fmt.Errorf("bare repository metadata changed or is unsafe")
		}
		refsErr := validateDiscoveryMetadataDir(approvedRoots, filepath.Join(canonical, "refs"))
		_, packedErr := readBoundedDiscoveryMetadata(approvedRoots, filepath.Join(canonical, "packed-refs"), maxDiscoveryGitMetadataBytes)
		if refsErr != nil && packedErr != nil {
			return fmt.Errorf("bare repository metadata changed or is unsafe")
		}
		metadataDir = canonical
	default:
		return fmt.Errorf("broken or unknown discovery type is not registrable")
	}
	_, _, _, _, err := readDiscoveryGitMetadata(approvedRoots, metadataDir)
	return err
}

func looksBare(entries map[string]os.DirEntry) bool {
	head, hasHead := entries["HEAD"]
	objects, hasObjects := entries["objects"]
	refs, hasRefs := entries["refs"]
	_, hasPackedRefs := entries["packed-refs"]
	return hasHead && head.Type()&os.ModeSymlink == 0 && hasObjects && objects.IsDir() && ((hasRefs && refs.IsDir()) || hasPackedRefs)
}

func discoveryID(canonicalPath string) string {
	sum := sha256.Sum256([]byte(canonicalPath))
	return "disc_" + hex.EncodeToString(sum[:16])
}
