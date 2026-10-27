package idrivewebdav

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"idrive-unlimited/internal/evs"

	netwebdav "golang.org/x/net/webdav"
)

type FileSystem struct {
	client *evs.Client
	root   string
	cache  *fileCache
}

func New(client *evs.Client, root string) *FileSystem {
	return &FileSystem{
		client: client,
		root:   CleanPath(root),
		cache:  newFileCache(client.CacheIdentity() + "|" + CleanPath(root)),
	}
}

func CleanPath(name string) string {
	return evs.CleanPath(name)
}

func (f *FileSystem) Mkdir(ctx context.Context, name string, _ os.FileMode) error {
	return mapError("mkdir", name, f.client.Mkdir(ctx, f.remotePath(ctx, name)))
}

func (f *FileSystem) OpenFile(ctx context.Context, name string, flag int, _ os.FileMode) (netwebdav.File, error) {
	remote := f.remotePath(ctx, name)
	writable := flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_TRUNC|os.O_APPEND) != 0

	if writable {
		file, err := newWriteFile(ctx, f.client, remote, flag)
		if err != nil {
			return nil, mapError("open", name, err)
		}
		file.cache = f.cache
		return file, nil
	}

	node, err := f.client.Stat(ctx, remote)
	if err != nil {
		return nil, mapError("open", name, err)
	}
	info := f.info(node, name)
	if node.IsDir {
		children, err := f.client.Browse(ctx, remote)
		if err != nil {
			return nil, mapError("open", name, err)
		}
		for i := range children {
			children[i].Version += "|" + f.client.Revision()
		}
		return newDirFile(info, children, func(node evs.Node) string {
			return f.exposedPath(ctx, node.Path)
		}), nil
	}
	file, err := newReadFile(f.client, f.cache, remote, node, info)
	if err != nil {
		return nil, mapError("open", name, err)
	}
	file.ctx = ctx
	return file, nil
}

func (f *FileSystem) RemoveAll(ctx context.Context, name string) error {
	if state := stateFrom(ctx); state != nil && state.moveDestination == CleanPath(name) {
		// x/net/webdav calls this before Rename for Overwrite:T. Preserve the
		// destination here; MoveReplace owns its verified replacement instead.
		return nil
	}
	if err := f.cache.invalidateAll(); err != nil {
		return err
	}
	return mapError("remove", name, f.client.Delete(ctx, f.remotePath(ctx, name)))
}

func (f *FileSystem) Rename(ctx context.Context, oldName, newName string) error {
	if err := f.cache.invalidateAll(); err != nil {
		return err
	}
	if f.root == "/" {
		if aliases, ok := f.rootAliases(ctx); ok && isProtectedRootRenamePath(aliases, oldName, newName) {
			return &os.PathError{Op: "rename", Path: oldName, Err: fs.ErrPermission}
		}
	}
	if state := stateFrom(ctx); state != nil && state.moveDestination == CleanPath(newName) {
		return mapError("rename", oldName, f.client.MoveReplace(ctx, f.remotePath(ctx, oldName), f.remotePath(ctx, newName)))
	}
	return mapError("rename", oldName, f.client.Rename(ctx, f.remotePath(ctx, oldName), f.remotePath(ctx, newName)))
}

func (f *FileSystem) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	node, err := f.client.Stat(ctx, f.remotePath(ctx, name))
	if err != nil {
		return nil, mapError("stat", name, err)
	}
	return f.info(node, name), nil
}

func (f *FileSystem) remotePath(ctx context.Context, name string) string {
	clean := CleanPath(name)
	if f.root == "/" {
		return f.translateRootDisplayToRaw(ctx, clean)
	}
	if clean == "/" {
		return f.root
	}
	return evs.CleanPath(path.Join(f.root, strings.TrimPrefix(clean, "/")))
}

func (f *FileSystem) exposedPath(ctx context.Context, remotePath string) string {
	clean := CleanPath(remotePath)
	if f.root != "/" {
		if clean == f.root {
			return "/"
		}
		prefix := strings.TrimSuffix(f.root, "/") + "/"
		if strings.HasPrefix(clean, prefix) {
			return CleanPath("/" + strings.TrimPrefix(clean, prefix))
		}
		return clean
	}
	return f.translateRootRawToDisplay(ctx, clean)
}

func (f *FileSystem) translateRootDisplayToRaw(ctx context.Context, exposedPath string) string {
	clean := CleanPath(exposedPath)
	if clean == "/" {
		return clean
	}
	head, tail := splitFirstComponent(clean)
	if head == "" {
		return clean
	}
	if aliases, ok := f.rootAliases(ctx); ok {
		if raw, exists := aliases.displayToRaw[head]; exists {
			return CleanPath("/" + raw + tail)
		}
	}
	return clean
}

func (f *FileSystem) translateRootRawToDisplay(ctx context.Context, remotePath string) string {
	clean := CleanPath(remotePath)
	if clean == "/" {
		return clean
	}
	head, tail := splitFirstComponent(clean)
	if head == "" {
		return clean
	}
	if aliases, ok := f.rootAliases(ctx); ok {
		if display, exists := aliases.rawToDisplay[head]; exists {
			return CleanPath("/" + display + tail)
		}
	}
	return clean
}

type fileInfo struct {
	name    string
	size    int64
	mode    fs.FileMode
	modTime time.Time
	isDir   bool
	version string
}

type rootAliasSet struct {
	rawToDisplay map[string]string
	displayToRaw map[string]string
	protectedRaw map[string]struct{}
}

func (f *FileSystem) info(node evs.Node, name string) fileInfo {
	node.Version += "|" + f.client.Revision()
	return infoFromNode(node, name)
}

func infoFromNode(node evs.Node, exposedPath string) fileInfo {
	name := path.Base(CleanPath(exposedPath))
	if CleanPath(exposedPath) == "/" {
		name = "/"
	}
	mode := fs.FileMode(0o644)
	if node.IsDir {
		mode = fs.ModeDir | 0o755
		if node.ReadOnly {
			mode = fs.ModeDir | 0o555
		}
	} else if node.ReadOnly {
		mode = 0o444
	}
	return fileInfo{
		name:    name,
		size:    node.Size,
		mode:    mode,
		modTime: node.ModTime,
		isDir:   node.IsDir,
		version: node.Version,
	}
}

func (fi fileInfo) Name() string       { return fi.name }
func (fi fileInfo) Size() int64        { return fi.size }
func (fi fileInfo) Mode() fs.FileMode  { return fi.mode }
func (fi fileInfo) ModTime() time.Time { return fi.modTime }
func (fi fileInfo) IsDir() bool        { return fi.isDir }
func (fi fileInfo) Sys() any           { return nil }

// Metadata cannot establish byte identity across external IDrive writers.
func (fi fileInfo) ETag(context.Context) (string, error) {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%s|%s", fi.name, fi.size, fi.modTime.UTC().Format(time.RFC3339Nano), fi.version)))
	return fmt.Sprintf("W/\"%x\"", sum[:]), nil
}

type dirFile struct {
	info    os.FileInfo
	entries []os.FileInfo
	pos     int
}

func newDirFile(info os.FileInfo, nodes []evs.Node, expose func(evs.Node) string) *dirFile {
	entries := make([]os.FileInfo, 0, len(nodes))
	for _, node := range nodes {
		exposedPath := node.Path
		if expose != nil {
			exposedPath = expose(node)
		}
		entries = append(entries, infoFromNode(node, exposedPath))
	}
	return &dirFile{
		info:    info,
		entries: entries,
	}
}

func (d *dirFile) Close() error                      { return nil }
func (d *dirFile) Read([]byte) (int, error)          { return 0, io.EOF }
func (d *dirFile) ReadAt([]byte, int64) (int, error) { return 0, fs.ErrInvalid }
func (d *dirFile) Seek(int64, int) (int64, error)    { return 0, fs.ErrInvalid }
func (d *dirFile) Write([]byte) (int, error)         { return 0, fs.ErrInvalid }
func (d *dirFile) Stat() (os.FileInfo, error)        { return d.info, nil }
func (d *dirFile) Readdir(count int) ([]os.FileInfo, error) {
	if d.pos >= len(d.entries) {
		// Readdir with count <= 0 returns all remaining entries and no EOF,
		// including an empty directory. WebDAV PROPFIND relies on this.
		if count <= 0 {
			return []os.FileInfo{}, nil
		}
		return nil, io.EOF
	}
	if count <= 0 || d.pos+count > len(d.entries) {
		count = len(d.entries) - d.pos
	}
	out := d.entries[d.pos : d.pos+count]
	d.pos += count
	return out, nil
}

type readFile struct {
	client     downloader
	cache      *fileCache
	remotePath string
	node       evs.Node
	info       os.FileInfo

	mu          sync.Mutex
	file        *os.File
	pendingSeek int64
	ctx         context.Context
	closed      bool
}

func newReadFile(client downloader, cache *fileCache, remotePath string, node evs.Node, info os.FileInfo) (*readFile, error) {
	return &readFile{
		client:     client,
		cache:      cache,
		remotePath: remotePath,
		node:       node,
		info:       info,
	}, nil
}

func (f *readFile) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	if f.file == nil {
		return nil
	}
	err := f.file.Close()
	f.file = nil
	return err
}

func (f *readFile) Read(p []byte) (int, error) {
	file, err := f.ensureLocal(f.requestContext())
	if err != nil {
		stateFrom(f.requestContext()).fail(err)
		return 0, err
	}
	n, err := file.Read(p)
	stateFrom(f.requestContext()).fail(err)
	return n, err
}

func (f *readFile) ReadAt(p []byte, off int64) (int, error) {
	file, err := f.ensureLocal(f.requestContext())
	if err != nil {
		stateFrom(f.requestContext()).fail(err)
		return 0, err
	}
	n, err := file.ReadAt(p, off)
	stateFrom(f.requestContext()).fail(err)
	return n, err
}

func (f *readFile) Seek(off int64, whence int) (int64, error) {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return 0, fs.ErrClosed
	}
	if f.file != nil {
		file := f.file
		f.mu.Unlock()
		return file.Seek(off, whence)
	}

	base := f.pendingSeek
	switch whence {
	case io.SeekStart:
		base = 0
	case io.SeekCurrent:
	case io.SeekEnd:
		base = f.info.Size()
	default:
		f.mu.Unlock()
		return 0, fs.ErrInvalid
	}

	next := base + off
	if next < 0 {
		f.mu.Unlock()
		return 0, fs.ErrInvalid
	}
	f.pendingSeek = next
	f.mu.Unlock()
	return next, nil
}
func (f *readFile) Write([]byte) (int, error)          { return 0, fs.ErrPermission }
func (f *readFile) Readdir(int) ([]os.FileInfo, error) { return nil, fs.ErrInvalid }
func (f *readFile) Stat() (os.FileInfo, error)         { return f.info, nil }

func (f *readFile) requestContext() context.Context {
	if f.ctx != nil {
		return f.ctx
	}
	return context.Background()
}
func (f *readFile) ensureLocal(ctx context.Context) (*os.File, error) {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return nil, fs.ErrClosed
	}
	if f.file != nil {
		file := f.file
		f.mu.Unlock()
		return file, nil
	}
	f.mu.Unlock()

	file, err := f.cache.Open(ctx, f.client, f.remotePath, f.node)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		file.Close()
		return nil, fs.ErrClosed
	}
	if f.file != nil {
		_ = file.Close()
		return f.file, nil
	}
	if _, err := file.Seek(f.pendingSeek, io.SeekStart); err != nil {
		file.Close()
		return nil, err
	}
	f.file = file
	return f.file, nil
}

type writeFile struct {
	cache         *fileCache
	client        *evs.Client
	remotePath    string
	file          *os.File
	tempPath      string
	commitOnClose bool
	dirty         bool
	ctx           context.Context
}

func newWriteFile(ctx context.Context, client *evs.Client, remotePath string, flag int) (*writeFile, error) {
	temp, err := os.CreateTemp("", "idrive-gateway-write-*")
	if err != nil {
		return nil, err
	}

	wf := &writeFile{
		ctx:        ctx,
		client:     client,
		remotePath: remotePath,
		file:       temp,
		tempPath:   temp.Name(),
	}

	if node, err := client.Stat(ctx, remotePath); err == nil {
		if flag&os.O_CREATE != 0 && flag&os.O_EXCL != 0 {
			temp.Close()
			os.Remove(temp.Name())
			return nil, fs.ErrExist
		}
		if node.IsDir {
			temp.Close()
			os.Remove(temp.Name())
			return nil, evs.ErrIsDirectory
		}
		if flag&os.O_TRUNC == 0 {
			if err := client.DownloadToFile(ctx, remotePath, temp.Name()); err != nil {
				temp.Close()
				os.Remove(temp.Name())
				return nil, err
			}
			if err := temp.Close(); err != nil {
				os.Remove(temp.Name())
				return nil, err
			}
			temp, err = os.OpenFile(temp.Name(), os.O_RDWR, 0o600)
			if err != nil {
				os.Remove(wf.tempPath)
				return nil, err
			}
			wf.file = temp
		} else {
			wf.commitOnClose = true
		}
	} else if errors.Is(err, evs.ErrNotFound) {
		if flag&os.O_CREATE == 0 {
			temp.Close()
			os.Remove(temp.Name())
			return nil, err
		}
		wf.commitOnClose = true
	} else {
		temp.Close()
		os.Remove(temp.Name())
		return nil, err
	}

	if flag&os.O_TRUNC != 0 {
		if err := temp.Truncate(0); err != nil {
			temp.Close()
			os.Remove(temp.Name())
			return nil, err
		}
		if _, err := temp.Seek(0, io.SeekStart); err != nil {
			temp.Close()
			os.Remove(temp.Name())
			return nil, err
		}
	}

	if flag&os.O_APPEND != 0 {
		if _, err := temp.Seek(0, io.SeekEnd); err != nil {
			temp.Close()
			os.Remove(temp.Name())
			return nil, err
		}
	} else {
		if _, err := temp.Seek(0, io.SeekStart); err != nil {
			temp.Close()
			os.Remove(temp.Name())
			return nil, err
		}
	}

	return wf, nil
}

func (f *writeFile) Close() error {
	if err := f.file.Close(); err != nil {
		os.Remove(f.tempPath)
		return err
	}
	if !f.dirty && !f.commitOnClose {
		os.Remove(f.tempPath)
		return nil
	}
	if err := errors.Join(f.ctx.Err(), stateFrom(f.ctx).complete()); err != nil {
		os.Remove(f.tempPath)
		return err
	}
	var err error
	if f.cache != nil {
		err = f.cache.invalidate(f.remotePath)
	}
	if err == nil {
		err = f.client.UploadFile(f.ctx, f.tempPath, f.remotePath, time.Now())
	}
	if err != nil {
		saved, saveErr := retainUpload(f.tempPath, f.remotePath, err)
		if saveErr == nil {
			os.Remove(f.tempPath)
		}
		return fmt.Errorf("%w; local recovery copy: %s (retention error: %v)", err, saved, saveErr)
	}
	return os.Remove(f.tempPath)
}

func (f *writeFile) Read(p []byte) (int, error)              { return f.file.Read(p) }
func (f *writeFile) ReadAt(p []byte, off int64) (int, error) { return f.file.ReadAt(p, off) }
func (f *writeFile) Seek(off int64, whence int) (int64, error) {
	return f.file.Seek(off, whence)
}

func (f *writeFile) Write(p []byte) (int, error) {
	if offset, err := f.file.Seek(0, io.SeekCurrent); err != nil {
		return 0, err
	} else if offset > evs.MaxUploadSize || int64(len(p)) > evs.MaxUploadSize-offset {
		stateFrom(f.ctx).fail(evs.ErrFileTooLarge)
		return 0, evs.ErrFileTooLarge
	}
	n, err := f.file.Write(p)
	stateFrom(f.ctx).fail(err)
	if n > 0 {
		f.dirty = true
	}
	return n, err
}

func (f *writeFile) Readdir(int) ([]os.FileInfo, error) {
	return nil, fs.ErrInvalid
}

func (f *writeFile) Stat() (os.FileInfo, error) {
	info, err := os.Stat(f.tempPath)
	if err != nil {
		return nil, err
	}
	return fileInfo{
		name:    path.Base(f.remotePath),
		size:    info.Size(),
		mode:    0o644,
		modTime: info.ModTime(),
		isDir:   false,
	}, nil
}

func mapError(op, name string, err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, evs.ErrNotFound):
		return &os.PathError{Op: op, Path: name, Err: fs.ErrNotExist}
	case errors.Is(err, evs.ErrReadOnly):
		return &os.PathError{Op: op, Path: name, Err: fs.ErrPermission}
	case errors.Is(err, evs.ErrUnauthorized):
		return &os.PathError{Op: op, Path: name, Err: fs.ErrPermission}
	case errors.Is(err, evs.ErrIsDirectory):
		return &os.PathError{Op: op, Path: name, Err: err}
	default:
		return &os.PathError{Op: op, Path: name, Err: err}
	}
}

func (f *FileSystem) rootAliases(ctx context.Context) (rootAliasSet, bool) {
	nodes, err := f.client.Browse(ctx, "/")
	if err != nil {
		return rootAliasSet{}, false
	}
	return buildRootAliases(nodes), true
}

func buildRootAliases(nodes []evs.Node) rootAliasSet {
	aliases := rootAliasSet{
		rawToDisplay: map[string]string{},
		displayToRaw: map[string]string{},
		protectedRaw: map[string]struct{}{},
	}
	candidateCounts := map[string]int{}
	reserved := map[string]struct{}{}

	type candidate struct {
		raw     string
		display string
	}
	deviceCandidates := make([]candidate, 0)

	for _, node := range nodes {
		reserved[node.Name] = struct{}{}
		if node.Device == nil {
			reserved[node.Name] = struct{}{}
			continue
		}
		display := strings.TrimSpace(node.Device.Name)
		if display == "" {
			display = node.Name
		}
		candidateCounts[display]++
		deviceCandidates = append(deviceCandidates, candidate{
			raw:     node.Name,
			display: display,
		})
	}

	for _, item := range deviceCandidates {
		display := item.raw
		if item.display != item.raw && validAlias(item.display) && candidateCounts[item.display] == 1 {
			if _, conflict := reserved[item.display]; !conflict {
				display = item.display
			}
		}
		aliases.rawToDisplay[item.raw] = display
		aliases.displayToRaw[display] = item.raw
		aliases.protectedRaw[item.raw] = struct{}{}
	}
	return aliases
}

func validAlias(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\\x00")
}

func splitFirstComponent(cleanPath string) (head, tail string) {
	trimmed := strings.TrimPrefix(CleanPath(cleanPath), "/")
	if trimmed == "" {
		return "", ""
	}
	if idx := strings.IndexByte(trimmed, '/'); idx >= 0 {
		return trimmed[:idx], trimmed[idx:]
	}
	return trimmed, ""
}

func isProtectedRootRenamePath(aliases rootAliasSet, oldName, newName string) bool {
	oldHead, oldTail := splitFirstComponent(oldName)
	newHead, newTail := splitFirstComponent(newName)
	if oldHead == "" || newHead == "" {
		return false
	}
	if oldTail != "" {
		return false
	}

	oldRaw := oldHead
	if raw, exists := aliases.displayToRaw[oldHead]; exists {
		oldRaw = raw
	}
	if _, exists := aliases.protectedRaw[oldRaw]; !exists {
		return false
	}

	newRaw := newHead
	if raw, exists := aliases.displayToRaw[newHead]; exists {
		newRaw = raw
	}
	return oldRaw != newRaw || newTail != ""
}
