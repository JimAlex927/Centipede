package http

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
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
	"strconv"
	"strings"
	"time"

	"centipede/internal/modules/docmost/adapter/out/postgres"

	"github.com/gin-gonic/gin"
	htmlnode "golang.org/x/net/html"
)

type zipImportEntry struct {
	Path       string
	File       *zip.File
	ParentPath string
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
	if source == "confluence" && !handler.requireFeature(c, "import:confluence") {
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
	fileName := safeExportFileName(filepath.Base(file.Filename))
	filePath := pathpkg.Join("import", current.Workspace.ID, taskID, fileName)
	if handler.storage == nil {
		writeError(c, http.StatusInternalServerError, "Import storage is not configured")
		return
	}
	if _, err := handler.storage.Save(c.Request.Context(), filePath, bytes.NewReader(data), maxSize); err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to store import file")
		return
	}
	task, err := handler.repository.CreateFileTask(c.Request.Context(), postgres.FileTaskInput{
		ID: taskID, Type: "import", Source: source, Status: "processing", FileName: filepath.Base(file.Filename),
		FilePath: filePath, FileSize: int64(len(data)), FileExt: "zip", CreatorID: current.User.ID,
		SpaceID: spaceID, WorkspaceID: current.Workspace.ID,
	})
	if err != nil {
		_ = handler.storage.Delete(c.Request.Context(), filePath)
		writeError(c, http.StatusInternalServerError, "Failed to create import task")
		return
	}
	// ZIP imports can contain hundreds of pages and attachments.  The Node
	// implementation puts this work on BullMQ and returns the processing task
	// immediately; keep the same contract so the browser can poll file-tasks
	// without holding the upload request open.
	go handler.runZipImport(data, task.ID, filePath, spaceID, source, current)
	writeData(c, http.StatusOK, task)
}

const zipImportTimeout = 30 * time.Minute

func (handler *Handler) runZipImport(data []byte, taskID, filePath, spaceID, source string, current principal) {
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
		if status == "success" && handler.storage != nil && filePath != "" {
			_ = handler.storage.Delete(context.Background(), filePath)
		}
	}()

	if err := handler.processGenericZip(ctx, data, spaceID, source, current); err != nil {
		status = "failed"
		errorMessage = err.Error()
	}
}

// ResumePendingZipImports lets the Go process recover durable imports that
// were interrupted by a restart. The task file is kept in storage until the
// import succeeds, so this does not depend on an in-memory goroutine or a
// Node/BullMQ worker being available.
func (handler *Handler) ResumePendingZipImports(ctx context.Context) {
	if handler.storage == nil {
		return
	}
	tasks, err := handler.repository.ProcessingImportFileTasks(ctx, 100)
	if err != nil {
		return
	}
	for _, task := range tasks {
		if task.CreatorID == nil || task.SpaceID == nil || task.Source == nil || task.FilePath == "" {
			continue
		}
		reader, openErr := handler.storage.Open(ctx, task.FilePath)
		if openErr != nil {
			_ = handler.repository.UpdateFileTaskStatus(context.Background(), task.ID, task.WorkspaceID, "failed", "Import file is no longer available")
			continue
		}
		maxSize := handler.maxUpload
		if maxSize <= 0 {
			maxSize = 100 * 1024 * 1024
		}
		data, readErr := io.ReadAll(io.LimitReader(reader, maxSize+1))
		_ = reader.Close()
		if readErr != nil || int64(len(data)) > maxSize {
			_ = handler.repository.UpdateFileTaskStatus(context.Background(), task.ID, task.WorkspaceID, "failed", "Failed to read import file")
			continue
		}
		user, userErr := handler.repository.UserByID(ctx, *task.CreatorID, task.WorkspaceID)
		workspace, workspaceErr := handler.repository.WorkspaceByID(ctx, task.WorkspaceID)
		if userErr != nil || workspaceErr != nil {
			continue
		}
		current := principal{User: user, Workspace: workspace}
		go handler.runZipImport(data, task.ID, task.FilePath, *task.SpaceID, *task.Source, current)
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
			assetPath := name
			if source == "confluence" {
				// Confluence HTML exports keep attachments under
				// download/attachments/<pageID>/, while the HTML references
				// them through /download/attachments/... URLs. Store a single
				// canonical form so both Cloud and Server exports resolve.
				assetPath = normalizeConfluenceAssetPath(name)
			}
			assets[assetPath] = file
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
	if source == "confluence" {
		parents := confluencePageParents(findConfluenceIndexEntry(archive.File), entries)
		entries = filterConfluencePageEntries(entries)
		for index := range entries {
			entries[index].ParentPath = parents[entries[index].Path]
		}
	}
	if len(entries) == 0 {
		return errors.New("ZIP contains no supported document pages")
	}
	sort.Slice(entries, func(left, right int) bool {
		return zipPathDepth(entries[left].Path) < zipPathDepth(entries[right].Path) || (zipPathDepth(entries[left].Path) == zipPathDepth(entries[right].Path) && entries[left].Path < entries[right].Path)
	})
	if source == "confluence" {
		// The generic path sort above is deterministic, but it does not know
		// about the parent relationships recovered from index.html. Reorder
		// once more so every parent is inserted before its children.
		entries = orderConfluencePageEntries(entries)
	}
	directoriesList := make([]string, 0, len(directories))
	// Confluence's `pages/<numeric-id>/...` directories are export
	// implementation details, not page folders. Creating them as pages would
	// leave users with synthetic "pages" and numeric-ID pages above every real
	// document, so Confluence pages are imported as top-level pages here.
	if source != "confluence" {
		for directory := range directories {
			if directory != "." && directory != "" {
				directoriesList = append(directoriesList, directory)
			}
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
		parentPageID := pageByPath[logicalDirectory]
		if source == "confluence" && entry.ParentPath != "" {
			parentPageID = pageByEntryPath[entry.ParentPath]
		}
		title := zipImportTitle(strings.TrimSuffix(pathpkg.Base(entry.Path), filepath.Ext(entry.Path)), source)
		page, createErr := handler.repository.CreatePage(ctx, current.Workspace.ID, current.User.ID, postgres.PageInput{
			Title: &title, Icon: pageMetadata.Icon, Position: optionalString(pageMetadata.Position),
			SpaceID: &spaceID, ParentPageID: optionalString(parentPageID), Content: []byte(`{"type":"doc","content":[]}`),
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
		nodes, err = handler.importZipAttachments(ctx, pageID, spaceID, entry.Path, entry.File, source, nodes, assets, current)
		if err != nil {
			return err
		}
		fallbackTitle := zipImportTitle(strings.TrimSuffix(pathpkg.Base(entry.Path), filepath.Ext(entry.Path)), source)
		pageTitle, contentNodes := extractImportedTitle(nodes, fallbackTitle)
		updatedContent, marshalErr := json.Marshal(importNode{Type: "doc", Content: contentNodes})
		if marshalErr != nil {
			return marshalErr
		}
		if _, updateErr := handler.repository.UpdatePage(ctx, pageByEntryPath[entry.Path], current.Workspace.ID, current.User.ID, postgres.PageInput{Title: &pageTitle, Content: updatedContent}); updateErr != nil {
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

func filterConfluencePageEntries(entries []zipImportEntry) []zipImportEntry {
	if len(entries) <= 1 {
		return entries
	}
	indexPath := confluenceRootIndexPath(entries)
	filtered := make([]zipImportEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Path == indexPath {
			// The root index is the export navigation page. Importing it as a
			// document creates a useless duplicate page and its links are only
			// navigation chrome, not page content.
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func findConfluenceIndexEntry(files []*zip.File) *zip.File {
	var result *zip.File
	resultDepth := int(^uint(0) >> 1)
	for _, file := range files {
		name := normalizeZipEntryPath(file.Name)
		base := strings.ToLower(pathpkg.Base(name))
		if (base == "index.html" || base == "index.htm") && zipPathDepth(name) < resultDepth {
			result = file
			resultDepth = zipPathDepth(name)
		}
	}
	return result
}

func normalizeZipEntryPath(value string) string {
	return strings.Trim(strings.ReplaceAll(value, "\\", "/"), "/")
}

func confluenceRootIndexPath(entries []zipImportEntry) string {
	indexPath := ""
	indexDepth := int(^uint(0) >> 1)
	for _, entry := range entries {
		base := strings.ToLower(pathpkg.Base(entry.Path))
		depth := zipPathDepth(entry.Path)
		if (base == "index.html" || base == "index.htm") && depth < indexDepth {
			indexPath = entry.Path
			indexDepth = depth
		}
	}
	return indexPath
}

// confluencePageParents reads the nested page list from the HTML export's
// root index. Some Confluence versions omit this list; in that case the
// importer intentionally returns no parents and keeps the pages at root.
func confluencePageParents(indexFile *zip.File, entries []zipImportEntry) map[string]string {
	parents := make(map[string]string)
	if indexFile == nil || len(entries) == 0 {
		return parents
	}
	known := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		known[entry.Path] = struct{}{}
	}
	reader, err := indexFile.Open()
	if err != nil {
		return parents
	}
	document, err := htmlnode.Parse(reader)
	_ = reader.Close()
	if err != nil {
		return parents
	}
	walkConfluenceIndex(document, "", known, parents, normalizeZipEntryPath(indexFile.Name))
	return parents
}

func walkConfluenceIndex(node *htmlnode.Node, parent string, known map[string]struct{}, parents map[string]string, indexPath string) {
	if node == nil {
		return
	}
	if node.Type == htmlnode.ElementNode && node.Data == "li" {
		pagePath := confluenceIndexPageAnchor(node, known, indexPath)
		effectiveParent := parent
		if pagePath != "" {
			if parent != "" && parent != pagePath {
				parents[pagePath] = parent
			}
			effectiveParent = pagePath
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if child.Type == htmlnode.ElementNode && child.Data == "ul" {
				walkConfluenceIndex(child, effectiveParent, known, parents, indexPath)
			}
		}
		return
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		walkConfluenceIndex(child, parent, known, parents, indexPath)
	}
}

func confluenceIndexPageAnchor(node *htmlnode.Node, known map[string]struct{}, indexPath string) string {
	if node == nil {
		return ""
	}
	if node.Type == htmlnode.ElementNode && node.Data == "ul" {
		return ""
	}
	if node.Type == htmlnode.ElementNode && node.Data == "a" {
		for _, attribute := range node.Attr {
			if attribute.Key != "href" {
				continue
			}
			candidate := zipResourcePathForSource(indexPath, attribute.Val, "confluence")
			if _, ok := known[candidate]; ok {
				return candidate
			}
		}
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if candidate := confluenceIndexPageAnchor(child, known, indexPath); candidate != "" {
			return candidate
		}
	}
	return ""
}

func orderConfluencePageEntries(entries []zipImportEntry) []zipImportEntry {
	if len(entries) < 2 {
		return entries
	}
	byPath := make(map[string]zipImportEntry, len(entries))
	for _, entry := range entries {
		byPath[entry.Path] = entry
	}
	ordered := make([]zipImportEntry, 0, len(entries))
	visiting := make(map[string]bool, len(entries))
	visited := make(map[string]bool, len(entries))
	var visit func(string)
	visit = func(path string) {
		if visited[path] || visiting[path] {
			return
		}
		entry, ok := byPath[path]
		if !ok {
			return
		}
		visiting[path] = true
		if entry.ParentPath != "" {
			visit(entry.ParentPath)
		}
		visiting[path] = false
		visited[path] = true
		ordered = append(ordered, entry)
	}
	for _, entry := range entries {
		visit(entry.Path)
	}
	return ordered
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

func (handler *Handler) importZipAttachments(ctx context.Context, pageID, spaceID, pagePath string, pageFile *zip.File, source string, nodes []importNode, assets map[string]*zip.File, current principal) ([]importNode, error) {
	if handler.storage == nil || len(assets) == 0 {
		return nodes, nil
	}
	imported := make(map[string]string)
	drawioRendered := make(map[string]string)
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
				if source == "confluence" {
					resourcePath = resolveConfluenceAssetPath(resourcePath, assets)
				}
				if file, ok := assets[resourcePath]; ok {
					if source == "confluence" {
						drawioPath, pngPath, isDrawio := confluenceDrawioPair(resourcePath, assets)
						if isDrawio {
							attachmentID, rendered := drawioRendered[drawioPath]
							if !rendered {
								var err error
								attachmentID, err = handler.createImportedDrawioAttachment(ctx, pageID, spaceID, drawioPath, pngPath, assets, current)
								if err != nil {
									return err
								}
								drawioRendered[drawioPath] = attachmentID
								if pngPath != "" {
									drawioRendered[pngPath] = attachmentID
								}
							}
							fileName := "diagram.drawio.svg"
							node.Attrs["attachmentId"] = attachmentID
							node.Attrs["src"] = "/api/files/" + url.PathEscape(attachmentID) + "/" + url.PathEscape(fileName)
							node.Attrs["url"] = node.Attrs["src"]
							continue
						}
					}
					attachmentID, importedOK := imported[resourcePath]
					if !importedOK {
						var err error
						attachmentID, err = handler.createImportedAttachmentNamed(ctx, pageID, spaceID, resourcePath, file, current, confluenceAttachmentName(resource, resourcePath, source))
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
	if err := walk(nodes); err != nil {
		return nil, err
	}
	if source == "confluence" {
		var appendErr error
		nodes, appendErr = appendConfluenceUnreferencedAttachments(ctx, handler, pageID, spaceID, pagePath, pageFile, nodes, assets, imported, drawioRendered, current)
		if appendErr != nil {
			return nil, appendErr
		}
	}
	return nodes, nil
}

// appendConfluenceUnreferencedAttachments preserves files listed in a
// Confluence page's attachment directory even when the exported HTML does not
// contain a link to them. The Node importer adds these as attachment nodes as
// well, which is important for diagrams and files that users expect to find in
// the page's attachment panel after migration.
func appendConfluenceUnreferencedAttachments(ctx context.Context, handler *Handler, pageID, spaceID, pagePath string, pageFile *zip.File, nodes []importNode, assets map[string]*zip.File, imported, drawioRendered map[string]string, current principal) ([]importNode, error) {
	attachmentPaths := confluencePageAttachmentPathsForFile(pagePath, pageFile, assets)
	if len(attachmentPaths) == 0 {
		return nodes, nil
	}

	for _, resourcePath := range attachmentPaths {
		if _, ok := imported[resourcePath]; ok {
			continue
		}
		if _, ok := drawioRendered[resourcePath]; ok {
			continue
		}

		drawioPath, pngPath, isDrawio := confluenceDrawioPair(resourcePath, assets)
		if isDrawio {
			attachmentID := drawioRendered[drawioPath]
			if attachmentID == "" {
				var err error
				attachmentID, err = handler.createImportedDrawioAttachment(ctx, pageID, spaceID, drawioPath, pngPath, assets, current)
				if err != nil {
					return nil, err
				}
				drawioRendered[drawioPath] = attachmentID
				if pngPath != "" {
					drawioRendered[pngPath] = attachmentID
				}
			}
			fileName := "diagram.drawio.svg"
			src := "/api/files/" + url.PathEscape(attachmentID) + "/" + url.PathEscape(fileName)
			nodes = append(nodes, importNode{Type: "drawio", Attrs: map[string]any{
				"src": src, "url": src, "title": fileName, "attachmentId": attachmentID,
			}})
			continue
		}

		file := assets[resourcePath]
		if file == nil {
			continue
		}
		attachmentID, err := handler.createImportedAttachmentNamed(ctx, pageID, spaceID, resourcePath, file, current, pathpkg.Base(resourcePath))
		if err != nil {
			return nil, err
		}
		imported[resourcePath] = attachmentID
		fileName := safeExportFileName(pathpkg.Base(resourcePath))
		src := "/api/files/" + url.PathEscape(attachmentID) + "/" + url.PathEscape(fileName)
		mimeType := mime.TypeByExtension(filepath.Ext(fileName))
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
		nodes = append(nodes, importNode{Type: "attachment", Attrs: map[string]any{
			"url": src, "name": fileName, "mime": mimeType, "size": int64(file.UncompressedSize64), "attachmentId": attachmentID,
		}})
	}
	return nodes, nil
}

func confluencePageAttachmentPaths(pagePath string, assets map[string]*zip.File) []string {
	return confluencePageAttachmentPathsForIDs(confluencePageIDs(pagePath), assets)
}

func confluencePageAttachmentPathsForFile(pagePath string, pageFile *zip.File, assets map[string]*zip.File) []string {
	pageIDs := confluencePageIDs(pagePath)
	pageIDs = appendUniqueStrings(pageIDs, confluencePageIDsFromFile(pageFile)...)
	return confluencePageAttachmentPathsForIDs(pageIDs, assets)
}

func confluencePageAttachmentPathsForIDs(pageIDs []string, assets map[string]*zip.File) []string {
	if len(pageIDs) == 0 {
		return nil
	}
	allowed := make(map[string]struct{})
	for _, pageID := range pageIDs {
		prefix := "attachments/" + pageID + "/"
		for path := range assets {
			if strings.HasPrefix(path, prefix) && pathpkg.Base(path) != "" {
				allowed[path] = struct{}{}
			}
		}
	}
	paths := make([]string, 0, len(allowed))
	for path := range allowed {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

var confluenceAttachmentPageIDPattern = regexp.MustCompile(`(?i)(?:download/)?attachments/([0-9]+)/`)

func confluencePageIDsFromFile(file *zip.File) []string {
	if file == nil {
		return nil
	}
	reader, err := file.Open()
	if err != nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(reader, maxImportedAttachmentSize+1))
	_ = reader.Close()
	if err != nil || len(data) > maxImportedAttachmentSize {
		return nil
	}
	matches := confluenceAttachmentPageIDPattern.FindAllSubmatch(data, -1)
	result := make([]string, 0, len(matches))
	seen := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		pageID := string(match[1])
		if _, ok := seen[pageID]; ok {
			continue
		}
		seen[pageID] = struct{}{}
		result = append(result, pageID)
	}
	return result
}

func appendUniqueStrings(values []string, additions ...string) []string {
	seen := make(map[string]struct{}, len(values)+len(additions))
	for _, value := range values {
		seen[value] = struct{}{}
	}
	for _, value := range additions {
		if _, ok := seen[value]; ok || value == "" {
			continue
		}
		seen[value] = struct{}{}
		values = append(values, value)
	}
	return values
}

func confluencePageIDs(pagePath string) []string {
	parts := strings.Split(strings.Trim(pagePath, "/"), "/")
	seen := make(map[string]struct{})
	result := make([]string, 0, 2)
	for index, part := range parts {
		if index > 0 && (strings.EqualFold(parts[index-1], "pages") || strings.EqualFold(parts[index-1], "page")) && isDecimalString(part) {
			if _, ok := seen[part]; !ok {
				seen[part] = struct{}{}
				result = append(result, part)
			}
		}
	}
	return result
}

func isDecimalString(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func (handler *Handler) createImportedAttachment(ctx context.Context, pageID, spaceID, resourcePath string, file *zip.File, current principal) (string, error) {
	return handler.createImportedAttachmentNamed(ctx, pageID, spaceID, resourcePath, file, current, pathpkg.Base(resourcePath))
}

func (handler *Handler) createImportedAttachmentNamed(ctx context.Context, pageID, spaceID, resourcePath string, file *zip.File, current principal, displayName string) (string, error) {
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
	if strings.TrimSpace(displayName) == "" {
		displayName = pathpkg.Base(resourcePath)
	}
	return handler.createImportedAttachmentData(ctx, pageID, spaceID, displayName, data, current)
}

func (handler *Handler) createImportedAttachmentData(ctx context.Context, pageID, spaceID, fileName string, data []byte, current principal) (string, error) {
	if len(data) > maxImportedAttachmentSize {
		return "", errors.New("ZIP attachment is too large")
	}
	attachmentID, err := randomUUID()
	if err != nil {
		return "", err
	}
	fileName = safeExportFileName(fileName)
	extension := filepath.Ext(fileName)
	mimeType := mime.TypeByExtension(extension)
	if mimeType == "" {
		mimeType = http.DetectContentType(data)
	}
	// Match Docmost Node's canonical local-storage layout. Older Go imports
	// used file/<workspace>/<attachment>/<name>; Local storage keeps that path
	// readable as a compatibility fallback, but new records should use the
	// Node-compatible path.
	relativePath := pathpkg.Join(current.Workspace.ID, "files", attachmentID, fileName)
	if _, err = handler.storage.Save(ctx, relativePath, bytes.NewReader(data), maxImportedAttachmentSize); err != nil {
		return "", err
	}
	pageValue := pageID
	spaceValue := spaceID
	attachment, err := handler.repository.CreateAttachment(ctx, postgres.AttachmentInput{
		ID: attachmentID, FileName: fileName, FilePath: relativePath, FileSize: int64(len(data)), FileExt: extension,
		MimeType: mimeType, Type: "file", CreatorID: current.User.ID, WorkspaceID: current.Workspace.ID,
		PageID: &pageValue, SpaceID: &spaceValue,
	})
	if err != nil {
		_ = handler.storage.Delete(ctx, relativePath)
		return "", err
	}
	handler.scheduleAttachmentIndex(attachment)
	return attachmentID, nil
}

func (handler *Handler) createImportedDrawioAttachment(ctx context.Context, pageID, spaceID, drawioPath, pngPath string, assets map[string]*zip.File, current principal) (string, error) {
	drawioFile := assets[drawioPath]
	if drawioFile == nil {
		return "", errors.New("Draw.io source is missing")
	}
	drawioData, err := readZipAsset(drawioFile)
	if err != nil {
		return "", err
	}
	var pngData []byte
	if pngPath != "" {
		pngFile := assets[pngPath]
		if pngFile == nil {
			return "", errors.New("Draw.io preview is missing")
		}
		pngData, err = readZipAsset(pngFile)
		if err != nil {
			return "", err
		}
	}
	data := buildDrawioSVG(drawioData, pngData)
	return handler.createImportedAttachmentData(ctx, pageID, spaceID, "diagram.drawio.svg", data, current)
}

func readZipAsset(file *zip.File) ([]byte, error) {
	if file == nil || file.UncompressedSize64 > maxImportedAttachmentSize {
		return nil, errors.New("ZIP attachment is too large")
	}
	opened, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer opened.Close()
	data, err := io.ReadAll(io.LimitReader(opened, maxImportedAttachmentSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxImportedAttachmentSize {
		return nil, errors.New("ZIP attachment is too large")
	}
	return data, nil
}

func buildDrawioSVG(drawioData, pngData []byte) []byte {
	drawioEncoded := base64.StdEncoding.EncodeToString(drawioData)
	imageElement := ""
	if len(pngData) > 0 {
		imageElement = `<image href="data:image/png;base64,` + base64.StdEncoding.EncodeToString(pngData) + `" width="100%" height="100%"/>`
	}
	value := `<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" width="600" height="400" viewBox="0 0 600 400" content="` + drawioEncoded + `">` + imageElement + `</svg>`
	return []byte(value)
}

func confluenceDrawioPair(resourcePath string, assets map[string]*zip.File) (string, string, bool) {
	extension := strings.ToLower(filepath.Ext(resourcePath))
	if extension == ".drawio" {
		base := strings.TrimSuffix(resourcePath, filepath.Ext(resourcePath))
		for _, candidate := range []string{base + ".png", base + ".drawio.png"} {
			if assets[candidate] != nil {
				return resourcePath, candidate, true
			}
		}
		return resourcePath, "", true
	}
	if extension == ".png" {
		base := strings.TrimSuffix(resourcePath, filepath.Ext(resourcePath))
		if strings.HasSuffix(strings.ToLower(base), ".drawio") {
			base = base[:len(base)-len(".drawio")]
		}
		candidate := base + ".drawio"
		if assets[candidate] != nil {
			return candidate, resourcePath, true
		}
		if candidate = confluenceNumericDrawioSourceSibling(resourcePath, assets); candidate != "" {
			return candidate, resourcePath, true
		}
		return "", "", false
	}
	if confluenceDrawioSource(assets[resourcePath]) {
		return resourcePath, confluenceDrawioPreview(resourcePath, assets), true
	}
	return "", "", false
}

func confluenceDrawioSource(file *zip.File) bool {
	if file == nil || file.UncompressedSize64 == 0 || file.UncompressedSize64 > 2*1024*1024 {
		return false
	}
	opened, err := file.Open()
	if err != nil {
		return false
	}
	defer opened.Close()
	data, err := io.ReadAll(io.LimitReader(opened, 256*1024))
	if err != nil {
		return false
	}
	value := strings.ToLower(string(data))
	return strings.Contains(value, "<mxfile") || strings.Contains(value, "<mxgraphmodel")
}

func confluenceDrawioPreview(drawioPath string, assets map[string]*zip.File) string {
	for _, candidate := range []string{drawioPath + ".png", drawioPath + ".drawio.png"} {
		if assets[candidate] != nil {
			return candidate
		}
	}
	return confluenceNumericPreviewSibling(drawioPath, assets)
}

func confluenceNumericPreviewSibling(resourcePath string, assets map[string]*zip.File) string {
	directory := pathpkg.Dir(resourcePath)
	previewID, hasPreviewID := confluenceNumericFileID(pathpkg.Base(resourcePath))
	for candidate, file := range assets {
		if pathpkg.Dir(candidate) != directory || !strings.EqualFold(filepath.Ext(candidate), ".png") || file == nil {
			continue
		}
		candidateBase := pathpkg.Base(candidate)
		candidateID, hasCandidateID := confluenceNumericFileID(candidateBase)
		if hasPreviewID && hasCandidateID && absInt64(previewID-candidateID) <= 30 {
			return candidate
		}
	}
	return ""
}

func confluenceNumericDrawioSourceSibling(resourcePath string, assets map[string]*zip.File) string {
	directory := pathpkg.Dir(resourcePath)
	previewID, hasPreviewID := confluenceNumericFileID(pathpkg.Base(resourcePath))
	for candidate, file := range assets {
		if pathpkg.Dir(candidate) != directory || filepath.Ext(candidate) != "" || file == nil || !confluenceDrawioSource(file) {
			continue
		}
		drawioID, hasDrawioID := confluenceNumericFileID(pathpkg.Base(candidate))
		if hasPreviewID && hasDrawioID && absInt64(previewID-drawioID) <= 30 {
			return candidate
		}
	}
	return ""
}

func confluenceNumericFileID(name string) (int64, bool) {
	name = strings.TrimSuffix(name, filepath.Ext(name))
	if name == "" {
		return 0, false
	}
	value, err := strconv.ParseInt(name, 10, 64)
	return value, err == nil
}

func absInt64(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
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
		resourcePath = normalizeConfluenceAssetPath(resourcePath)
		if strings.HasPrefix(resourcePath, "attachments/") {
			return pathpkg.Clean(resourcePath)
		}
	}
	return strings.TrimPrefix(pathpkg.Clean(pathpkg.Join(pathpkg.Dir(pagePath), resourcePath)), "./")
}

func normalizeConfluenceAssetPath(value string) string {
	value = strings.Trim(strings.ReplaceAll(value, "\\", "/"), "/")
	value = strings.TrimPrefix(value, "download/")
	return pathpkg.Clean(value)
}

// Confluence Server exports are inconsistent about attachment names. The HTML
// may contain the original filename while the ZIP stores an attachment ID, or
// the HTML URL may include the original filename after the numeric ZIP entry:
//
//	attachments/123/45678
//	attachments/123/45678/diagram.png
//	attachments/123/45678.png
//
// Resolve those aliases before the importer decides that a resource is
// external. The returned path always points at an existing ZIP asset.
func resolveConfluenceAssetPath(resourcePath string, assets map[string]*zip.File) string {
	if resourcePath == "" {
		return ""
	}
	clean := strings.Trim(strings.ReplaceAll(resourcePath, "\\", "/"), "/")
	if _, ok := assets[clean]; ok {
		return clean
	}

	// Numeric Confluence entries can be followed by a display filename in the
	// HTML URL. Walk upward until an existing asset is found, but only accept a
	// numeric leaf so an unrelated directory cannot swallow the resource.
	for candidate := clean; candidate != "." && candidate != ""; candidate = pathpkg.Dir(candidate) {
		if _, ok := assets[candidate]; ok && isConfluenceNumericAsset(candidate) {
			return candidate
		}
	}

	// Some exports append an extension to the numeric ID in the HTML while the
	// ZIP entry has no extension.
	base := pathpkg.Base(clean)
	if extension := filepath.Ext(base); extension != "" {
		candidate := pathpkg.Join(pathpkg.Dir(clean), strings.TrimSuffix(base, extension))
		if _, ok := assets[candidate]; ok && isConfluenceNumericAsset(candidate) {
			return candidate
		}
	}

	// Last, resolve an original filename located in the same attachment
	// directory. This covers exports that keep filenames for some resources and
	// numeric IDs for others.
	directory := pathpkg.Dir(clean)
	for candidate := range assets {
		if pathpkg.Dir(candidate) == directory && strings.EqualFold(pathpkg.Base(candidate), base) {
			return candidate
		}
	}
	return clean
}

func isConfluenceNumericAsset(path string) bool {
	_, ok := confluenceNumericFileID(pathpkg.Base(path))
	return ok
}

func confluenceAttachmentName(resource, resolvedPath, source string) string {
	if source != "confluence" {
		return pathpkg.Base(resolvedPath)
	}
	parsed, err := url.Parse(strings.TrimSpace(resource))
	if err != nil || parsed.Path == "" {
		return pathpkg.Base(resolvedPath)
	}
	name := pathpkg.Base(parsed.Path)
	if name == "" || name == "." || isConfluenceNumericAsset(name) {
		return pathpkg.Base(resolvedPath)
	}
	if decoded, decodeErr := url.PathUnescape(name); decodeErr == nil && decoded != "" {
		name = decoded
	}
	return name
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
