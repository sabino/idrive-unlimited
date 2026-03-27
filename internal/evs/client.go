package evs

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
	"idrive-unlimited/internal/session"
)

const (
	timeFormat          = "2006/01/02 15:04:05"
	chunkEnableFileSize = int64(1024 * 1024)
	dropzoneChunkSize   = int64(2000000)
	browseCacheTTL      = 5 * time.Minute
)

var (
	ErrNotFound     = errors.New("idrive: not found")
	ErrUnauthorized = errors.New("idrive: unauthorized")
	ErrReadOnly     = errors.New("idrive: read-only path")
	ErrIsDirectory  = errors.New("idrive: is a directory")
)

type Client struct {
	serverHost string
	baseURL    string
	jar        http.CookieJar
	httpClient *http.Client

	mu          sync.RWMutex
	devices     map[string]DeviceMeta
	browseCache map[string]browseCacheEntry
	browseGroup singleflight.Group
}

type browseCacheEntry struct {
	nodes     []Node
	expiresAt time.Time
}

type DeviceMeta struct {
	Type              string `json:"Type"`
	Identifier        string `json:"Identifier"`
	ReferencingFolder string `json:"ReferencingFolder"`
	Name              string `json:"Name"`
	ADID              string `json:"adid"`
	BackupTime        string `json:"BackupTime"`
}

type browseResponse struct {
	Message  string                `json:"message"`
	Desc     string                `json:"desc"`
	Path     string                `json:"path"`
	Contents []browseContent       `json:"contents"`
	Devices  map[string]DeviceMeta `json:"devices"`
}

type browseContent struct {
	IsDir    bool   `json:"is_dir"`
	Name     string `json:"name"`
	Size     string `json:"size"`
	Version  string `json:"ver"`
	LMD      string `json:"lmd"`
	SaveTime string `json:"save_time"`
}

type apiReply struct {
	Message string `json:"message"`
	Desc    string `json:"desc"`
}

type uploadSessionReply struct {
	Message         string `json:"message"`
	Desc            string `json:"desc"`
	UploadSessionID string `json:"upload_session_id"`
}

type Node struct {
	Path     string
	Name     string
	IsDir    bool
	Size     int64
	ModTime  time.Time
	Version  string
	Device   *DeviceMeta
	RawEntry browseContent
}

func NewClient(serverHost string, jar http.CookieJar) (*Client, error) {
	if serverHost == "" {
		return nil, fmt.Errorf("server host is required")
	}
	if jar == nil {
		var err error
		jar, err = cookiejar.New(nil)
		if err != nil {
			return nil, err
		}
	}
	return &Client{
		serverHost: serverHost,
		baseURL:    "https://" + serverHost,
		jar:        jar,
		httpClient: &http.Client{
			Jar:     jar,
			Timeout: 2 * time.Minute,
		},
		devices:     make(map[string]DeviceMeta),
		browseCache: make(map[string]browseCacheEntry),
	}, nil
}

func NewClientFromSession(data *session.Data) (*Client, error) {
	if data == nil {
		return nil, fmt.Errorf("session data is nil")
	}
	jar, err := data.NewCookieJar()
	if err != nil {
		return nil, err
	}
	return NewClient(data.ServerHost, jar)
}

func (c *Client) SessionData() *session.Data {
	data, err := session.FromCookieJar(c.serverHost, c.jar)
	if err != nil {
		return &session.Data{ServerHost: c.serverHost}
	}
	return data
}

func (c *Client) ServerHost() string {
	return c.serverHost
}

func (c *Client) Validate(ctx context.Context) error {
	_, err := c.Browse(ctx, "/")
	return err
}

func (c *Client) Browse(ctx context.Context, remotePath string) ([]Node, error) {
	clean := CleanPath(remotePath)
	if nodes, ok := c.cachedBrowse(clean); ok {
		return nodes, nil
	}

	result, err, _ := c.browseGroup.Do(clean, func() (any, error) {
		if nodes, ok := c.cachedBrowse(clean); ok {
			return nodes, nil
		}

		values := url.Values{}
		values.Set("p", clean)
		values.Set("json", "yes")
		values.Set("device_id", "")
		if clean == "/" {
			values.Set("devices", "yes")
		}

		var resp browseResponse
		if err := c.postForm(ctx, "/evs/browseFolder", values, &resp); err != nil {
			return nil, err
		}
		if !isSuccess(resp.Message) {
			return nil, apiError(http.StatusOK, resp.Message, resp.Desc)
		}

		nodes := make([]Node, 0, len(resp.Contents))
		for _, entry := range resp.Contents {
			node := Node{
				Path:     joinClean(resp.Path, entry.Name),
				Name:     entry.Name,
				IsDir:    entry.IsDir,
				Size:     parseSize(entry.Size),
				ModTime:  parseModTime(entry.LMD, entry.SaveTime),
				Version:  entry.Version,
				RawEntry: entry,
			}
			if clean == "/" {
				if device, ok := resp.Devices[entry.Name]; ok {
					deviceCopy := device
					node.Device = &deviceCopy
				}
			}
			nodes = append(nodes, node)
		}

		c.storeBrowse(clean, nodes, resp.Devices)
		return cloneNodes(nodes), nil
	})
	if err != nil {
		return nil, err
	}
	return result.([]Node), nil
}

func (c *Client) Stat(ctx context.Context, remotePath string) (Node, error) {
	clean := CleanPath(remotePath)
	if clean == "/" {
		return Node{
			Path:    "/",
			Name:    "/",
			IsDir:   true,
			ModTime: time.Time{},
		}, nil
	}

	parent := ParentDir(clean)
	if node, ok := c.lookupCachedNode(parent, path.Base(clean)); ok {
		return node, nil
	}
	entries, err := c.Browse(ctx, parent)
	if err != nil {
		return Node{}, err
	}
	base := path.Base(clean)
	for _, entry := range entries {
		if entry.Name == base {
			return entry, nil
		}
	}
	return Node{}, ErrNotFound
}

func (c *Client) Mkdir(ctx context.Context, remotePath string) error {
	clean := CleanPath(remotePath)
	if clean == "/" {
		return nil
	}
	if IsReadOnlyPath(clean) {
		return ErrReadOnly
	}

	values := url.Values{}
	values.Set("foldername", path.Base(clean))
	values.Set("p", ParentDir(clean))
	values.Set("json", "yes")

	var resp apiReply
	if err := c.postForm(ctx, "/evs/createFolder", values, &resp); err != nil {
		return err
	}
	if !isSuccess(resp.Message) {
		return apiError(http.StatusOK, resp.Message, resp.Desc)
	}
	c.invalidatePaths(ParentDir(clean), clean)
	return nil
}

func (c *Client) Rename(ctx context.Context, oldPath, newPath string) error {
	oldClean := CleanPath(oldPath)
	newClean := CleanPath(newPath)
	if oldClean == newClean {
		return nil
	}
	if oldClean == "/" || newClean == "/" {
		return fmt.Errorf("cannot rename the root directory")
	}
	if IsReadOnlyPath(oldClean) || IsReadOnlyPath(newClean) {
		return ErrReadOnly
	}

	oldParent := ParentDir(oldClean)
	newParent := ParentDir(newClean)
	if oldParent == newParent {
		values := url.Values{}
		values.Set("oldpath", oldClean)
		values.Set("newpath", newClean)
		values.Set("json", "yes")

		var resp apiReply
		if err := c.postForm(ctx, "/evs/renameFileFolder", values, &resp); err != nil {
			return err
		}
		if !isSuccess(resp.Message) {
			return apiError(http.StatusOK, resp.Message, resp.Desc)
		}
		c.invalidatePaths(oldParent, newParent, oldClean, newClean)
		return nil
	}

	if err := c.Move(ctx, oldClean, newParent); err != nil {
		return err
	}
	movedPath := joinClean(newParent, path.Base(oldClean))
	if movedPath == newClean {
		return nil
	}
	return c.Rename(ctx, movedPath, newClean)
}

func (c *Client) Copy(ctx context.Context, sourcePath, destinationDir string) error {
	sourceClean := CleanPath(sourcePath)
	destClean := CleanPath(destinationDir)
	if IsReadOnlyPath(sourceClean) || IsReadOnlyPath(destClean) {
		return ErrReadOnly
	}

	values := url.Values{}
	values.Add("fileFolderPaths", sourceClean)
	values.Set("p", destClean)
	values.Set("sync_acc", "no")
	values.Set("json", "yes")

	if _, err := c.postFormRaw(ctx, "/evs/copyPasteFileFolder", values); err != nil {
		return err
	}
	targetPath := joinClean(destClean, path.Base(sourceClean))
	if err := c.waitForPathState(ctx, targetPath, true, 45*time.Second); err != nil {
		return err
	}
	c.invalidatePaths(destClean, targetPath)
	return nil
}

func (c *Client) Move(ctx context.Context, sourcePath, destinationDir string) error {
	sourceClean := CleanPath(sourcePath)
	destClean := CleanPath(destinationDir)
	if IsReadOnlyPath(sourceClean) || IsReadOnlyPath(destClean) {
		return ErrReadOnly
	}

	values := url.Values{}
	values.Add("fileFolderPaths", sourceClean)
	values.Set("p", destClean)
	values.Set("sync_acc", "no")
	values.Set("json", "yes")

	if _, err := c.postFormRaw(ctx, "/evs/move", values); err != nil {
		return err
	}
	targetPath := joinClean(destClean, path.Base(sourceClean))
	if err := c.waitForPathState(ctx, targetPath, true, 45*time.Second); err != nil {
		return err
	}
	if err := c.waitForPathState(ctx, sourceClean, false, 45*time.Second); err != nil {
		return err
	}
	c.invalidatePaths(ParentDir(sourceClean), destClean, sourceClean, targetPath)
	return nil
}

func (c *Client) Delete(ctx context.Context, remotePath string) error {
	clean := CleanPath(remotePath)
	if clean == "/" {
		return fmt.Errorf("cannot delete the root directory")
	}
	if IsReadOnlyPath(clean) {
		return ErrReadOnly
	}

	values := url.Values{}
	values.Add("p", clean)
	values.Set("trash", "no")
	values.Set("json", "yes")

	_, err := c.postFormRaw(ctx, "/evs/v1/deleteFile", values)
	if err != nil {
		return err
	}
	c.invalidatePaths(ParentDir(clean), clean)
	return nil
}

func (c *Client) Download(ctx context.Context, remotePath string) (io.ReadCloser, error) {
	clean := CleanPath(remotePath)
	endpoint := c.baseURL + "/evs/v1/downloadFile?version=0&p=" + url.QueryEscape(clean)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, ErrNotFound
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		resp.Body.Close()
		return nil, ErrUnauthorized
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
		resp.Body.Close()
		return nil, fmt.Errorf("download %s: unexpected status %s: %s", clean, resp.Status, strings.TrimSpace(string(body)))
	}
	return resp.Body, nil
}

func (c *Client) DownloadToFile(ctx context.Context, remotePath, localPath string) error {
	rc, err := c.Download(ctx, remotePath)
	if err != nil {
		return err
	}
	defer rc.Close()

	file, err := os.Create(localPath)
	if err != nil {
		return err
	}
	defer file.Close()

	if _, err := io.Copy(file, rc); err != nil {
		return err
	}
	return nil
}

func (c *Client) UploadFile(ctx context.Context, localPath, remotePath string, modTime time.Time) error {
	clean := CleanPath(remotePath)
	if IsReadOnlyPath(clean) {
		return ErrReadOnly
	}

	info, err := os.Stat(localPath)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return ErrIsDirectory
	}

	if existing, err := c.Stat(ctx, clean); err == nil {
		if existing.IsDir {
			return ErrIsDirectory
		}
		if err := c.Delete(ctx, clean); err != nil {
			return err
		}
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}

	if info.Size() < chunkEnableFileSize {
		if err := c.uploadSmall(ctx, localPath, clean, modTime); err != nil {
			return err
		}
		c.invalidatePaths(ParentDir(clean), clean)
		return nil
	}
	if err := c.uploadChunked(ctx, localPath, clean, modTime); err != nil {
		return err
	}
	c.invalidatePaths(ParentDir(clean), clean)
	return nil
}

func (c *Client) uploadSmall(ctx context.Context, localPath, remotePath string, modTime time.Time) error {
	file, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return err
	}
	payload, err := io.ReadAll(file)
	if err != nil {
		return err
	}
	checksum := md5.Sum(payload)

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	uploadID, err := randomUploadID()
	if err != nil {
		return err
	}
	if err := writer.WriteField("p", ParentDir(remotePath)); err != nil {
		return err
	}
	if err := writer.WriteField("json", "yes"); err != nil {
		return err
	}
	if err := writer.WriteField("lmd", strconv.FormatInt(modTime.UnixMilli(), 10)); err != nil {
		return err
	}
	if err := writer.WriteField("file_size", strconv.FormatInt(info.Size(), 10)); err != nil {
		return err
	}
	if err := writer.WriteField("file_name", path.Base(remotePath)); err != nil {
		return err
	}
	if err := writer.WriteField("dzuuid", uploadID); err != nil {
		return err
	}
	if err := writer.WriteField("dzchunkindex", "0"); err != nil {
		return err
	}
	if err := writer.WriteField("dztotalfilesize", strconv.FormatInt(info.Size(), 10)); err != nil {
		return err
	}
	if err := writer.WriteField("dzchunksize", strconv.FormatInt(dropzoneChunkSize, 10)); err != nil {
		return err
	}
	if err := writer.WriteField("dztotalchunkcount", "1"); err != nil {
		return err
	}
	if err := writer.WriteField("dzchunkbyteoffset", "0"); err != nil {
		return err
	}
	if err := writer.WriteField("chunk", "0"); err != nil {
		return err
	}
	if err := writer.WriteField("totalChunks", "1"); err != nil {
		return err
	}
	part, err := writer.CreateFormFile("file", path.Base(remotePath))
	if err != nil {
		return err
	}
	if _, err := part.Write(payload); err != nil {
		return err
	}
	if err := writer.WriteField("total_checksum", hex.EncodeToString(checksum[:])); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}

	raw, err := c.postMultipartRaw(ctx, "/evs/web/upload", body, writer.FormDataContentType())
	if err != nil {
		return err
	}
	return checkMaybeJSONReply(raw)
}

func (c *Client) uploadChunked(ctx context.Context, localPath, remotePath string, modTime time.Time) error {
	file, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return err
	}

	var sessionResp uploadSessionReply
	sessionBody := fmt.Sprintf(`{"file_name":%q,"file_size":%d}`, path.Base(remotePath), info.Size())
	if err := c.postRawJSONBody(ctx, "/evs/web/upload_session", strings.NewReader(sessionBody), &sessionResp); err != nil {
		return err
	}
	if sessionResp.UploadSessionID == "" {
		return fmt.Errorf("upload session did not return upload_session_id")
	}

	chunkSize := getChunkSize(info.Size())
	totalChunks := int((info.Size() + chunkSize - 1) / chunkSize)
	hasher := md5.New()
	uploadID, err := randomUploadID()
	if err != nil {
		return err
	}

	for chunkIndex := 0; chunkIndex < totalChunks; chunkIndex++ {
		chunkOffset := int64(chunkIndex) * chunkSize
		chunkLength := min64(chunkSize, info.Size()-chunkOffset)
		chunkData := make([]byte, chunkLength)
		if _, err := io.ReadFull(file, chunkData); err != nil {
			return err
		}
		if _, err := hasher.Write(chunkData); err != nil {
			return err
		}

		body := &bytes.Buffer{}
		writer := multipart.NewWriter(body)
		fields := map[string]string{
			"p":                 ParentDir(remotePath),
			"json":              "yes",
			"lmd":               strconv.FormatInt(modTime.UnixMilli(), 10),
			"file_size":         strconv.FormatInt(info.Size(), 10),
			"file_name":         path.Base(remotePath),
			"upload_session_id": sessionResp.UploadSessionID,
			"dzuuid":            uploadID,
			"dzchunkindex":      strconv.Itoa(chunkIndex),
			"dztotalfilesize":   strconv.FormatInt(info.Size(), 10),
			"dzchunksize":       strconv.FormatInt(chunkSize, 10),
			"dztotalchunkcount": strconv.Itoa(totalChunks),
			"dzchunkbyteoffset": strconv.FormatInt(chunkOffset, 10),
			"chunk":             strconv.Itoa(chunkIndex),
			"totalChunks":       strconv.Itoa(totalChunks),
		}
		for key, value := range fields {
			if err := writer.WriteField(key, value); err != nil {
				return err
			}
		}
		part, err := writer.CreateFormFile("file", path.Base(remotePath))
		if err != nil {
			return err
		}
		if _, err := part.Write(chunkData); err != nil {
			return err
		}
		if err := writer.Close(); err != nil {
			return err
		}

		raw, err := c.postMultipartRaw(ctx, "/evs/web/upload", body, writer.FormDataContentType())
		if err != nil {
			return err
		}
		if err := checkMaybeJSONReply(raw); err != nil {
			return err
		}
	}

	finalBody := &bytes.Buffer{}
	finalWriter := multipart.NewWriter(finalBody)
	finalFields := map[string]string{
		"file_name":         path.Base(remotePath),
		"upload_session_id": sessionResp.UploadSessionID,
		"close_session":     "true",
		"p":                 ParentDir(remotePath),
		"json":              "yes",
		"lmd":               strconv.FormatInt(modTime.UnixMilli(), 10),
		"file_size":         strconv.FormatInt(info.Size(), 10),
		"chunk":             strconv.Itoa(totalChunks),
		"totalChunks":       strconv.Itoa(totalChunks),
		"total_checksum":    hex.EncodeToString(hasher.Sum(nil)),
	}
	for key, value := range finalFields {
		if err := finalWriter.WriteField(key, value); err != nil {
			return err
		}
	}
	if err := finalWriter.Close(); err != nil {
		return err
	}

	raw, err := c.postMultipartRaw(ctx, "/evs/web/upload", finalBody, finalWriter.FormDataContentType())
	if err != nil {
		return err
	}
	return checkMaybeJSONReply(raw)
}

func (c *Client) postForm(ctx context.Context, endpoint string, values url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if err := checkHTTPStatus(resp.StatusCode, raw); err != nil {
		return err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode %s response: %w: %s", endpoint, err, strings.TrimSpace(string(raw)))
	}
	return nil
}

func (c *Client) postRawJSONBody(ctx context.Context, endpoint string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+endpoint, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if err := checkHTTPStatus(resp.StatusCode, raw); err != nil {
		return err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode %s response: %w: %s", endpoint, err, strings.TrimSpace(string(raw)))
	}
	reply := uploadSessionReply{}
	if err := json.Unmarshal(raw, &reply); err == nil && reply.Message != "" && !isSuccess(reply.Message) {
		return apiError(resp.StatusCode, reply.Message, reply.Desc)
	}
	return nil
}

func (c *Client) postFormRaw(ctx context.Context, endpoint string, values url.Values) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if err := checkHTTPStatus(resp.StatusCode, raw); err != nil {
		return nil, err
	}
	if err := checkMaybeJSONReply(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func (c *Client) postMultipartRaw(ctx context.Context, endpoint string, body *bytes.Buffer, contentType string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+endpoint, bytes.NewReader(body.Bytes()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if err := checkHTTPStatus(resp.StatusCode, raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func checkHTTPStatus(statusCode int, body []byte) error {
	if statusCode >= 200 && statusCode < 300 {
		return nil
	}
	message := strings.TrimSpace(string(body))
	if statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden {
		return ErrUnauthorized
	}
	if statusCode == http.StatusNotFound {
		return ErrNotFound
	}
	return fmt.Errorf("unexpected status %d: %s", statusCode, message)
}

func checkMaybeJSONReply(raw []byte) error {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || !strings.HasPrefix(trimmed, "{") {
		return nil
	}
	var reply apiReply
	if err := json.Unmarshal([]byte(trimmed), &reply); err != nil {
		return nil
	}
	if reply.Message == "" {
		return nil
	}
	if isSuccess(reply.Message) {
		return nil
	}
	return apiError(http.StatusOK, reply.Message, reply.Desc)
}

func apiError(status int, message, desc string) error {
	upperMessage := strings.ToUpper(strings.TrimSpace(message))
	upperDesc := strings.ToUpper(strings.TrimSpace(desc))

	switch {
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return ErrUnauthorized
	case upperMessage == "INVALID_SESSION":
		return ErrUnauthorized
	case strings.Contains(upperDesc, "AUTHENTICATION FAILED"):
		return ErrUnauthorized
	case strings.Contains(upperDesc, "INVALID PATH"):
		return ErrNotFound
	case strings.Contains(upperDesc, "NO SUCH FILE"), strings.Contains(upperDesc, "NOT FOUND"):
		return ErrNotFound
	}

	if desc != "" {
		return fmt.Errorf("idrive api error: %s (%s)", message, desc)
	}
	return fmt.Errorf("idrive api error: %s", message)
}

func isSuccess(message string) bool {
	return strings.EqualFold(strings.TrimSpace(message), "SUCCESS")
}

func parseSize(raw string) int64 {
	if raw == "" || raw == "-" {
		return 0
	}
	size, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0
	}
	return size
}

func parseModTime(values ...string) time.Time {
	for _, value := range values {
		if strings.TrimSpace(value) == "" || value == "-" {
			continue
		}
		if ts, err := time.ParseInLocation(timeFormat, value, time.Local); err == nil {
			return ts
		}
	}
	return time.Time{}
}

func getChunkSize(fileSize int64) int64 {
	sixMB := int64(6 * 1024 * 1024)
	oneMB := int64(1 * 1024 * 1024)
	fourMB := int64(4 * 1024 * 1024)
	sixteenMB := int64(16 * 1024 * 1024)

	if fileSize/sixMB > 16 {
		return sixteenMB
	}
	if fileSize/fourMB > 4 {
		return fourMB
	}
	return oneMB
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func cloneNodes(nodes []Node) []Node {
	cloned := make([]Node, len(nodes))
	copy(cloned, nodes)
	return cloned
}

func (c *Client) cachedBrowse(remotePath string) ([]Node, bool) {
	c.mu.RLock()
	entry, ok := c.browseCache[remotePath]
	c.mu.RUnlock()
	if !ok || time.Now().After(entry.expiresAt) {
		if ok {
			c.invalidatePaths(remotePath)
		}
		return nil, false
	}
	return cloneNodes(entry.nodes), true
}

func (c *Client) storeBrowse(remotePath string, nodes []Node, devices map[string]DeviceMeta) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if remotePath == "/" && devices != nil {
		c.devices = make(map[string]DeviceMeta, len(devices))
		for key, value := range devices {
			c.devices[key] = value
		}
	}
	c.browseCache[remotePath] = browseCacheEntry{
		nodes:     cloneNodes(nodes),
		expiresAt: time.Now().Add(browseCacheTTL),
	}
}

func (c *Client) invalidatePaths(paths ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, item := range paths {
		delete(c.browseCache, CleanPath(item))
	}
}

func (c *Client) lookupCachedNode(parentPath, baseName string) (Node, bool) {
	c.mu.RLock()
	entry, ok := c.browseCache[parentPath]
	c.mu.RUnlock()
	if !ok || time.Now().After(entry.expiresAt) {
		if ok {
			c.invalidatePaths(parentPath)
		}
		return Node{}, false
	}
	for _, node := range entry.nodes {
		if node.Name == baseName {
			return node, true
		}
	}
	return Node{}, false
}

func (c *Client) waitForPathState(ctx context.Context, remotePath string, wantExists bool, maxWait time.Duration) error {
	deadline := time.Now().Add(maxWait)
	for {
		_, err := c.Stat(ctx, remotePath)
		exists := err == nil
		if wantExists && exists {
			return nil
		}
		if !wantExists && errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		if time.Now().After(deadline) {
			if wantExists {
				return fmt.Errorf("timed out waiting for %s to appear", remotePath)
			}
			return fmt.Errorf("timed out waiting for %s to disappear", remotePath)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(1 * time.Second):
		}
	}
}

func randomUploadID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x", buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16]), nil
}

func CleanPath(remotePath string) string {
	normalized := strings.ReplaceAll(strings.TrimSpace(remotePath), "\\", "/")
	if normalized == "" {
		return "/"
	}
	if !strings.HasPrefix(normalized, "/") {
		normalized = "/" + normalized
	}
	clean := path.Clean(normalized)
	if clean == "." {
		return "/"
	}
	return clean
}

func ParentDir(remotePath string) string {
	clean := CleanPath(remotePath)
	if clean == "/" {
		return "/"
	}
	parent := path.Dir(clean)
	if parent == "." {
		return "/"
	}
	return parent
}

func joinClean(parent, name string) string {
	if CleanPath(parent) == "/" {
		return CleanPath("/" + name)
	}
	return CleanPath(path.Join(parent, name))
}

func IsReadOnlyPath(remotePath string) bool {
	clean := CleanPath(remotePath)
	if clean == "/" {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(clean, "/"), "/")
	if len(parts) < 2 {
		return false
	}
	switch parts[1] {
	case "Contacts", "Calendar", "Call Logs", "SMS":
		return true
	default:
		return false
	}
}
