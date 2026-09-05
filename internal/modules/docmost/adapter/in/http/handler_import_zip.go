package http

import (
	"archive/zip"
	"bytes"
	"context"
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
	"regexp"
	"sort"
	"strings"
	"time"

	"centipede/internal/modules/docmost/adapter/out/postgres"

	"github.com/gin-gonic/gin"
)

type zipImportEntry struct {
	Path string
	File *zip.File
}

type zipImportPageMetadata struct {
	PageID     string  `json:"pageId"`
	SlugID     string  `json:"slugId"`
	Icon       *string `json:"icon"`
	Position   string  `json:"position"`
	ParentPath *string `json:"parentPath"`
}

type zipImportMetadata struct {
	Source  string                           `json:"source"`
	Version string                           `json:"version"`
	Pages   map[string]zipImportPageMetadata `json:"pages"`
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
	if !isSupportedZipImportSource(source) {
		writeError(c, http.StatusBadRequest, "Invalid ZIP import source")
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
	// ZIP imports can contain hundreds of pages and attachments.  The Node
	// implementation puts this work on BullMQ and returns the processing task
	// immediately; keep the same contract so the browser can poll file-tasks
	// without holding the upload request open.
	go handler.runZipImport(data, task.ID, spaceID, source, current)
	writeData(c, http.StatusOK, task)
}

const zipImportTimeout = 30 * time.Minute

func (handler *Handler) runZipImport(data []byte, taskID, spaceID, source string, current principal) {
	ctx, cancel := context.WithTimeout(context.Background(), zipImportTimeout)
	defer cancel()

	status := "success"
	errorMessage := ""
	defer func() {
		if recovered := recover(); recovered != nil {
			status = "failed"
			errorMessage = "ZIP import failed unexpectedly"
		}
		_ = handler.repository.UpdateFileTaskStatus(context.Background(), taskID, current.Workspace.ID, status, errorMessage)
	}()

	if err := handler.processGenericZip(ctx, data, spaceID, source, current); err != nil {
		status = "failed"
		errorMessage = err.Error()
	}
}

func isSupportedZipImportSource(source string) bool {
	return source == "generic" || source == "notion" || source == "confluence"
}

func (handler *Handler) processGenericZip(ctx context.Context, data []byte, spaceID, source string, current principal) error {
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
		if isSupportedZipDocumentExtension(extension) {
			entries = append(entries, zipImportEntry{Path: name, File: file})
			addZipParentDirectories(directories, pathpkg.Dir(name))
			continue
		}
		if file.UncompressedSize64 <= maxImportedAttachmentSize {
			assets[name] = file
		}
	}
	if len(entries) == 0 {
		return errors.New("ZIP contains no supported document pages")
	}
	metadata, err := readZipImportMetadata(assets["docmost-metadata.json"])
	if err != nil {
		return err
	}
	delete(assets, "docmost-metadata.json")
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
		leftPartial := source == "notion" && notionPartialIDSuffix.MatchString(pathpkg.Base(directoriesList[left]))
		rightPartial := source == "notion" && notionPartialIDSuffix.MatchString(pathpkg.Base(directoriesList[right]))
		if leftPartial != rightPartial {
			return leftPartial
		}
		return zipPathDepth(directoriesList[left]) < zipPathDepth(directoriesList[right]) || (zipPathDepth(directoriesList[left]) == zipPathDepth(directoriesList[right]) && directoriesList[left] < directoriesList[right])
	})
	mergedEntryDirectories := make(map[string]string)
	if source == "notion" {
		beforeMerge := make([]string, len(entries))
		for index := range entries {
			beforeMerge[index] = entries[index].Path
		}
		mergeNotionFolderPages(&entries, directoriesList)
		for index, entry := range entries {
			if beforeMerge[index] != entry.Path {
				directory := strings.TrimSuffix(entry.Path, ".md")
				if strings.HasSuffix(entry.Path, ".html") {
					directory = strings.TrimSuffix(entry.Path, ".html")
				}
				mergedEntryDirectories[entry.Path] = directory
			}
		}
	}
	pageByPath := make(map[string]string)
	for _, directory := range directoriesList {
		if isSingleZipRootDirectory(directory, directoriesList, entries) {
			continue
		}
		if _, merged := mergedEntryDirectories[directory+".md"]; merged {
			continue
		}
		if _, merged := mergedEntryDirectories[directory+".html"]; merged {
			continue
		}
		title := zipImportTitle(pathpkg.Base(directory), source)
		pageMetadata := zipImportPageMetadataForPath(metadata, directory+".md")
		page, createErr := handler.repository.CreatePage(ctx, current.Workspace.ID, current.User.ID, postgres.PageInput{
			Title: &title, Icon: pageMetadata.Icon, Position: optionalString(pageMetadata.Position),
			SpaceID: &spaceID, ParentPageID: optionalString(pageByPath[pathpkg.Dir(directory)]), Content: []byte(`{"type":"doc","content":[]}`),
		})
		if createErr != nil {
			return createErr
		}
		pageByPath[directory] = page.ID
	}

	space, spaceErr := handler.repository.SpaceByID(ctx, spaceID, current.Workspace.ID, current.User.ID)
	if spaceErr != nil {
		return spaceErr
	}
	pageByEntryPath := make(map[string]string, len(entries))
	pageByPathToID := make(map[string]string, len(entries)+len(pageByPath))
	for directory, pageID := range pageByPath {
		pageByPathToID[directory] = pageID
	}
	for _, entry := range entries {
		pageMetadata := zipImportPageMetadataForPath(metadata, entry.Path)
		logicalDirectory := pathpkg.Dir(entry.Path)
		if mergedDirectory, ok := mergedEntryDirectories[entry.Path]; ok {
			logicalDirectory = pathpkg.Dir(mergedDirectory)
		}
		title := zipImportTitle(strings.TrimSuffix(pathpkg.Base(entry.Path), filepath.Ext(entry.Path)), source)
		page, createErr := handler.repository.CreatePage(ctx, current.Workspace.ID, current.User.ID, postgres.PageInput{
			Title: &title, Icon: pageMetadata.Icon, Position: optionalString(pageMetadata.Position),
			SpaceID: &spaceID, ParentPageID: optionalString(pageByPath[logicalDirectory]), Content: []byte(`{"type":"doc","content":[]}`),
		})
		if createErr != nil {
			return createErr
		}
		pageByEntryPath[entry.Path] = page.ID
		pageByPathToID[entry.Path] = page.ID
		if mergedDirectory, ok := mergedEntryDirectories[entry.Path]; ok {
			pageByPathToID[mergedDirectory] = page.ID
		}
	}

	for _, entry := range entries {
		reader, openErr := entry.File.Open()
		if openErr != nil {
			return openErr
		}
		nodes, parseErr := parseImportedDocumentWithOCR(reader, strings.ToLower(filepath.Ext(entry.Path)), handler.pdfOCR)
		reader.Close()
		if parseErr != nil {
			return parseErr
		}
		rewriteZipInternalLinks(&nodes, entry.Path, space.Slug, pageByPathToID)
		pageID := pageByEntryPath[entry.Path]
		if pageID == "" {
			return errors.New("imported page was not allocated")
		}
		if err := handler.importZipAttachments(ctx, pageID, spaceID, entry.Path, source, nodes, assets, current); err != nil {
			return err
		}
		updatedContent, marshalErr := json.Marshal(importNode{Type: "doc", Content: nodes})
		if marshalErr != nil {
			return marshalErr
		}
		if _, updateErr := handler.repository.UpdatePage(ctx, pageByEntryPath[entry.Path], current.Workspace.ID, current.User.ID, postgres.PageInput{Content: updatedContent}); updateErr != nil {
			return updateErr
		}
	}
	return nil
}

func isSupportedZipDocumentExtension(extension string) bool {
	switch strings.ToLower(extension) {
	case ".md", ".html", ".docx", ".pdf", ".csv":
		return true
	default:
		return false
	}
}

func readZipImportMetadata(file *zip.File) (*zipImportMetadata, error) {
	if file == nil {
		return nil, nil
	}
	reader, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	var metadata zipImportMetadata
	if err := json.NewDecoder(io.LimitReader(reader, 2*1024*1024)).Decode(&metadata); err != nil {
		return nil, errors.New("invalid docmost import metadata")
	}
	if metadata.Source != "docmost" || len(metadata.Pages) == 0 {
		return nil, errors.New("unsupported docmost import metadata")
	}
	return &metadata, nil
}

func zipImportPageMetadataForPath(metadata *zipImportMetadata, path string) zipImportPageMetadata {
	if metadata == nil {
		return zipImportPageMetadata{}
	}
	if value, ok := metadata.Pages[zipEncodedPath(path)]; ok {
		return value
	}
	if value, ok := metadata.Pages[path]; ok {
		return value
	}
	return zipImportPageMetadata{}
}

func zipEncodedPath(value string) string {
	parts := strings.Split(strings.Trim(value, "/"), "/")
	for index := range parts {
		parts[index] = url.PathEscape(parts[index])
	}
	return strings.Join(parts, "/")
}

func rewriteZipInternalLinks(nodes *[]importNode, currentPath, spaceSlug string, pageByPath map[string]string) {
	if nodes == nil {
		return
	}
	var walk func([]importNode)
	walk = func(items []importNode) {
		for index := range items {
			node := &items[index]
			for markIndex := range node.Marks {
				mark := &node.Marks[markIndex]
				if mark.Type != "link" || mark.Attrs == nil {
					continue
				}
				href, ok := mark.Attrs["href"].(string)
				if !ok {
					continue
				}
				if target, fragment, ok := zipInternalLinkTarget(currentPath, href, pageByPath); ok {
					mark.Attrs["href"] = "/s/" + url.PathEscape(spaceSlug) + "/p/" + url.PathEscape(target) + fragment
					mark.Attrs["internal"] = true
				}
			}
			walk(node.Content)
		}
	}
	walk(*nodes)
}

func zipInternalLinkTarget(currentPath, href string, pageByPath map[string]string) (string, string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(href))
	if err != nil || parsed.IsAbs() || parsed.Path == "" || strings.HasPrefix(parsed.Path, "/") {
		return "", "", false
	}
	targetPath := strings.TrimPrefix(pathpkg.Clean(pathpkg.Join(pathpkg.Dir(currentPath), parsed.Path)), "./")
	candidates := []string{targetPath}
	if filepath.Ext(targetPath) == "" {
		candidates = append(candidates, targetPath+".md", targetPath+".html")
	}
	for _, candidate := range candidates {
		if pageID, ok := pageByPath[candidate]; ok {
			fragment := ""
			if parsed.Fragment != "" {
				fragment = "#" + parsed.Fragment
			}
			return pageID, fragment, true
		}
	}
	return "", "", false
}

func (handler *Handler) importZipAttachments(ctx context.Context, pageID, spaceID, pagePath, source string, nodes []importNode, assets map[string]*zip.File, current principal) error {
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
				resourcePath := zipResourcePathForSource(pagePath, resource, source)
				if file, ok := assets[resourcePath]; ok {
					attachmentID, importedOK := imported[resourcePath]
					if !importedOK {
						var err error
						attachmentID, err = handler.createImportedAttachment(ctx, pageID, spaceID, resourcePath, file, current)
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

func (handler *Handler) createImportedAttachment(ctx context.Context, pageID, spaceID, resourcePath string, file *zip.File, current principal) (string, error) {
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
	if _, err = handler.storage.Save(ctx, relativePath, bytes.NewReader(data), maxImportedAttachmentSize); err != nil {
		return "", err
	}
	pageValue := pageID
	spaceValue := spaceID
	if _, err = handler.repository.CreateAttachment(ctx, postgres.AttachmentInput{
		ID: attachmentID, FileName: fileName, FilePath: relativePath, FileSize: int64(len(data)), FileExt: extension,
		MimeType: mimeType, Type: "file", CreatorID: current.User.ID, WorkspaceID: current.Workspace.ID,
		PageID: &pageValue, SpaceID: &spaceValue,
	}); err != nil {
		_ = handler.storage.Delete(ctx, relativePath)
		return "", err
	}
	return attachmentID, nil
}

func zipResourcePath(pagePath, resource string) string {
	return zipResourcePathForSource(pagePath, resource, "generic")
}

func zipResourcePathForSource(pagePath, resource, source string) string {
	parsed, err := url.Parse(strings.TrimSpace(resource))
	if err != nil || parsed.IsAbs() || parsed.Path == "" {
		return ""
	}
	resourcePath := strings.ReplaceAll(parsed.Path, "\\", "/")
	if unescaped, unescapeErr := url.PathUnescape(resourcePath); unescapeErr == nil {
		resourcePath = unescaped
	}
	if source == "confluence" {
		resourcePath = strings.TrimPrefix(resourcePath, "/")
		resourcePath = strings.TrimPrefix(resourcePath, "download/")
		if strings.HasPrefix(resourcePath, "attachments/") {
			return pathpkg.Clean(resourcePath)
		}
	}
	return strings.TrimPrefix(pathpkg.Clean(pathpkg.Join(pathpkg.Dir(pagePath), resourcePath)), "./")
}

var (
	notionPageIDSuffix    = regexp.MustCompile(`[ -]?[a-z0-9]{32}$`)
	notionPartialIDSuffix = regexp.MustCompile(` ([a-f0-9]{4})-([a-f0-9]{4})$`)
	notionFullIDSuffix    = regexp.MustCompile(`[a-z0-9]{32}$`)
)

func zipImportTitle(value, source string) string {
	if source != "notion" {
		return value
	}
	value = notionPageIDSuffix.ReplaceAllString(value, "")
	return strings.TrimSpace(notionPartialIDSuffix.ReplaceAllString(value, ""))
}

func isSingleZipRootDirectory(directory string, directories []string, entries []zipImportEntry) bool {
	if zipPathDepth(directory) != 0 || len(directories) == 0 {
		return false
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Path, directory+"/") {
			return false
		}
	}
	return len(entries) > 0
}

func mergeNotionFolderPages(entries *[]zipImportEntry, directories []string) {
	if entries == nil || len(*entries) == 0 {
		return
	}
	used := make(map[int]bool)
	for _, directory := range directories {
		folderName := pathpkg.Base(directory)
		folderTitle := zipImportTitle(folderName, "notion")
		parentDirectory := pathpkg.Dir(directory)
		partialID, partialSuffix := "", ""
		if match := notionPartialIDSuffix.FindStringSubmatch(folderName); len(match) == 3 {
			partialID, partialSuffix = strings.ToLower(match[1]), strings.ToLower(match[2])
		}
		for index := range *entries {
			entry := &(*entries)[index]
			if used[index] || pathpkg.Dir(entry.Path) != parentDirectory {
				continue
			}
			entryBase := strings.TrimSuffix(pathpkg.Base(entry.Path), filepath.Ext(entry.Path))
			if zipImportTitle(entryBase, "notion") != folderTitle {
				continue
			}
			if partialID != "" {
				fullID := notionFullIDSuffix.FindString(entryBase)
				if fullID == "" || !strings.HasPrefix(strings.ToLower(fullID), partialID) || !strings.HasSuffix(strings.ToLower(fullID), partialSuffix) {
					continue
				}
			}
			entry.Path = directory + ".md"
			used[index] = true
			break
		}
	}
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
