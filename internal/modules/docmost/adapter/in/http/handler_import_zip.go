package http

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
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
		if extension != ".md" && extension != ".html" {
			continue
		}
		entries = append(entries, zipImportEntry{Path: name, File: file})
		addZipParentDirectories(directories, pathpkg.Dir(name))
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
		if _, createErr := handler.repository.CreatePage(c.Request.Context(), current.Workspace.ID, current.User.ID, postgres.PageInput{Title: &title, SpaceID: &spaceID, ParentPageID: optionalString(pageByPath[pathpkg.Dir(entry.Path)]), Content: content}); createErr != nil {
			return createErr
		}
	}
	return nil
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
