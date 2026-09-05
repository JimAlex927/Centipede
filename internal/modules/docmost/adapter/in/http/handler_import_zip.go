package http

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strings"

	"centipede/internal/modules/docmost/adapter/out/postgres"

	"github.com/gin-gonic/gin"
)

type zipImportEntry struct {
	Path string
	File *zip.File
}

const maxImportedAttachmentSize = 30 * 1024 * 1024

func (handler *Handler) importZip(c *gin.Context) {
	current := currentPrincipal(c)
	spaceID := strings.TrimSpace(c.PostForm("spaceId"))
	if spaceID == "" {
		writeError(c, http.StatusBadRequest, "spaceId is required")
		return
	}
	if !handler.requireSpaceRole(c, spaceID, "writer") {
		return
	}
	source := strings.ToLower(strings.TrimSpace(c.PostForm("source")))
	if source != "generic" {
		writeError(c, http.StatusNotImplemented, "Only generic ZIP import is implemented in Go yet")
		return
	}
	file, err := c.FormFile("file")
	if err != nil || file == nil {
		writeError(c, http.StatusBadRequest, "Failed to upload file")
		return
	}
	if strings.ToLower(filepath.Ext(file.Filename)) != ".zip" {
		writeError(c, http.StatusBadRequest, "Invalid import file extension.")
		return
	}
	maxSize := handler.maxUpload
	if maxSize <= 0 {
		maxSize = 100 * 1024 * 1024
	}
	if file.Size > maxSize {
		writeError(c, http.StatusBadRequest, "File too large")
		return
	}
	opened, err := file.Open()
	if err != nil {
		writeError(c, http.StatusBadRequest, "Failed to read uploaded file")
		return
	}
	defer opened.Close()
	data, err := io.ReadAll(io.LimitReader(opened, maxSize+1))
	if err != nil || int64(len(data)) > maxSize {
		writeError(c, http.StatusBadRequest, "Failed to read import file")
		return
	}
	taskID, err := importTaskUUID()
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to create import task")
		return
	}
	task, err := handler.repository.CreateFileTask(c.Request.Context(), postgres.FileTaskInput{
		ID: taskID, Type: "import", Source: source, Status: "processing", FileName: filepath.Base(file.Filename),
		FilePath: "", FileSize: int64(len(data)), FileExt: "zip", CreatorID: current.User.ID,
		SpaceID: spaceID, WorkspaceID: current.Workspace.ID,
	})
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to create import task")
		return
	}
	if err := handler.processGenericZip(c, data, spaceID, current); err != nil {
		message := err.Error()
		_ = handler.repository.UpdateFileTaskStatus(c.Request.Context(), task.ID, current.Workspace.ID, "failed", message)
		task.ErrorMessage = &message
		status := "failed"
		task.Status = &status
		writeData(c, http.StatusOK, task)
		return
	}
	_ = handler.repository.UpdateFileTaskStatus(c.Request.Context(), task.ID, current.Workspace.ID, "success", "")
	status := "success"
	task.Status = &status
	writeData(c, http.StatusOK, task)
}

func (handler *Handler) processGenericZip(c *gin.Context, data []byte, spaceID string, current principal) error {
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return errors.New("invalid ZIP archive")
	}
	entries := make([]zipImportEntry, 0)
	assets := make(map[string]*zip.File)
	directories := map[string]bool{}
	for _, file := range archive.File {
		name := strings.Trim(strings.ReplaceAll(file.Name, "\\", "/"), "/")
		if name == "" || strings.HasPrefix(name, "../") || strings.Contains(name, "/../") || pathpkg.IsAbs(name) {
			continue
		}
		if file.FileInfo().IsDir() || strings.HasSuffix(file.Name, "/") {
			addZipParentDirectories(directories, name)
			continue
		}
		extension := strings.ToLower(filepath.Ext(name))
		if extension == ".md" || extension == ".html" {
			entries = append(entries, zipImportEntry{Path: name, File: file})
			addZipParentDirectories(directories, pathpkg.Dir(name))
			continue
		}
		if file.UncompressedSize64 <= maxImportedAttachmentSize {
			assets[name] = file
		}
	}
	if len(entries) == 0 {
		return errors.New("ZIP contains no Markdown or HTML pages")
	}
	sort.Slice(entries, func(left, right int) bool {
		return zipPathDepth(entries[left].Path) < zipPathDepth(entries[right].Path) || (zipPathDepth(entries[left].Path) == zipPathDepth(entries[right].Path) && entries[left].Path < entries[right].Path)
	})
	directoriesList := make([]string, 0, len(directories))
	for directory := range directories {
		if directory != "." && directory != "" {
			directoriesList = append(directoriesList, directory)
		}
	}
	sort.Slice(directoriesList, func(left, right int) bool {
		return zipPathDepth(directoriesList[left]) < zipPathDepth(directoriesList[right]) || (zipPathDepth(directoriesList[left]) == zipPathDepth(directoriesList[right]) && directoriesList[left] < directoriesList[right])
	})
	pageByPath := make(map[string]string)
	for _, directory := range directoriesList {
		title := pathpkg.Base(directory)
		page, createErr := handler.repository.CreatePage(c.Request.Context(), current.Workspace.ID, current.User.ID, postgres.PageInput{Title: &title, SpaceID: &spaceID, ParentPageID: optionalString(pageByPath[pathpkg.Dir(directory)]), Content: []byte(`{"type":"doc","content":[]}`)})
		if createErr != nil {
			return createErr
		}
		pageByPath[directory] = page.ID
	}
	for _, entry := range entries {
		reader, openErr := entry.File.Open()
		if openErr != nil {
			return openErr
		}
		nodes, parseErr := parseImportedDocument(reader, strings.ToLower(filepath.Ext(entry.Path)))
		reader.Close()
		if parseErr != nil {
			return parseErr
		}
		title, nodes := extractImportedTitle(nodes, strings.TrimSuffix(pathpkg.Base(entry.Path), filepath.Ext(entry.Path)))
		content, marshalErr := json.Marshal(importNode{Type: "doc", Content: nodes})
		if marshalErr != nil {
			return marshalErr
		}
		page, createErr := handler.repository.CreatePage(c.Request.Context(), current.Workspace.ID, current.User.ID, postgres.PageInput{Title: &title, SpaceID: &spaceID, ParentPageID: optionalString(pageByPath[pathpkg.Dir(entry.Path)]), Content: content})
		if createErr != nil {
			return createErr
		}
		if err := handler.importZipAttachments(c, page.ID, page.SpaceID, entry.Path, nodes, assets, current); err != nil {
			return err
		}
		updatedContent, marshalErr := json.Marshal(importNode{Type: "doc", Content: nodes})
		if marshalErr != nil {
			return marshalErr
		}
		if _, updateErr := handler.repository.UpdatePage(c.Request.Context(), page.ID, current.Workspace.ID, current.User.ID, postgres.PageInput{Content: updatedContent}); updateErr != nil {
			return updateErr
		}
	}
	return nil
}

func (handler *Handler) importZipAttachments(c *gin.Context, pageID, spaceID, pagePath string, nodes []importNode, assets map[string]*zip.File, current principal) error {
	if handler.storage == nil || len(assets) == 0 {
		return nil
	}
	imported := make(map[string]string)
	var walk func([]importNode) error
	walk = func(items []importNode) error {
		for index := range items {
			node := &items[index]
			if node.Attrs != nil {
				resource := ""
				if value, ok := node.Attrs["src"].(string); ok {
					resource = value
				}
				if resource == "" {
					if value, ok := node.Attrs["url"].(string); ok {
						resource = value
					}
				}
				resourcePath := zipResourcePath(pagePath, resource)
				if file, ok := assets[resourcePath]; ok {
					attachmentID, importedOK := imported[resourcePath]
					if !importedOK {
						var err error
						attachmentID, err = handler.createImportedAttachment(c, pageID, spaceID, resourcePath, file, current)
						if err != nil {
							return err
						}
						imported[resourcePath] = attachmentID
					}
					node.Attrs["attachmentId"] = attachmentID
					fileName := safeExportFileName(pathpkg.Base(resourcePath))
					node.Attrs["src"] = "/api/files/" + url.PathEscape(attachmentID) + "/" + url.PathEscape(fileName)
					node.Attrs["url"] = node.Attrs["src"]
				}
			}
			if err := walk(node.Content); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(nodes)
}

func (handler *Handler) createImportedAttachment(c *gin.Context, pageID, spaceID, resourcePath string, file *zip.File, current principal) (string, error) {
	if file.UncompressedSize64 > maxImportedAttachmentSize {
		return "", errors.New("ZIP attachment is too large")
	}
	opened, err := file.Open()
	if err != nil {
		return "", err
	}
	data, readErr := io.ReadAll(io.LimitReader(opened, maxImportedAttachmentSize+1))
	opened.Close()
	if readErr != nil {
		return "", readErr
	}
	if len(data) > maxImportedAttachmentSize {
		return "", errors.New("ZIP attachment is too large")
	}
	attachmentID, err := randomUUID()
	if err != nil {
		return "", err
	}
	fileName := safeExportFileName(pathpkg.Base(resourcePath))
	extension := filepath.Ext(fileName)
	mimeType := mime.TypeByExtension(extension)
	if mimeType == "" {
		mimeType = http.DetectContentType(data)
	}
	relativePath := pathpkg.Join("file", current.Workspace.ID, attachmentID, fileName)
	if _, err = handler.storage.Save(c.Request.Context(), relativePath, bytes.NewReader(data), maxImportedAttachmentSize); err != nil {
		return "", err
	}
	pageValue := pageID
	spaceValue := spaceID
	if _, err = handler.repository.CreateAttachment(c.Request.Context(), postgres.AttachmentInput{
		ID: attachmentID, FileName: fileName, FilePath: relativePath, FileSize: int64(len(data)), FileExt: extension,
		MimeType: mimeType, Type: "file", CreatorID: current.User.ID, WorkspaceID: current.Workspace.ID,
		PageID: &pageValue, SpaceID: &spaceValue,
	}); err != nil {
		_ = handler.storage.Delete(c.Request.Context(), relativePath)
		return "", err
	}
	return attachmentID, nil
}

func zipResourcePath(pagePath, resource string) string {
	parsed, err := url.Parse(strings.TrimSpace(resource))
	if err != nil || parsed.IsAbs() || parsed.Path == "" {
		return ""
	}
	resourcePath := strings.ReplaceAll(parsed.Path, "\\", "/")
	return strings.TrimPrefix(pathpkg.Clean(pathpkg.Join(pathpkg.Dir(pagePath), resourcePath)), "./")
}

func addZipParentDirectories(directories map[string]bool, directory string) {
	directory = strings.Trim(directory, "/")
	for directory != "" && directory != "." {
		directories[directory] = true
		directory = pathpkg.Dir(directory)
	}
}

func zipPathDepth(value string) int { return strings.Count(strings.Trim(value, "/"), "/") }

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func importTaskUUID() (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	buffer[6] = (buffer[6] & 0x0f) | 0x40
	buffer[8] = (buffer[8] & 0x3f) | 0x80
	value := hex.EncodeToString(buffer)
	return value[:8] + "-" + value[8:12] + "-" + value[12:16] + "-" + value[16:20] + "-" + value[20:], nil
}
