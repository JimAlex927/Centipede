package http

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"strings"
	"unicode"

	"centipede/internal/modules/docmost/adapter/out/postgres"
	"centipede/internal/modules/docmost/application"
	"centipede/internal/modules/docmost/domain"

	"github.com/gin-gonic/gin"
)

const maxIconBytes int64 = 5 * 1024 * 1024

func (handler *Handler) uploadFile(c *gin.Context) {
	if handler.storage == nil {
		writeError(c, http.StatusServiceUnavailable, "File storage is unavailable")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, handler.maxUpload+1024*1024)
	header, err := c.FormFile("file")
	if err != nil {
		writeError(c, http.StatusBadRequest, "Failed to upload file")
		return
	}
	pageID := c.PostForm("pageId")
	current := currentPrincipal(c)
	pageValue, err := handler.repository.PageByID(c.Request.Context(), pageID, "", current.Workspace.ID, false)
	if err != nil {
		handler.writeRepositoryError(c, err, "Page not found")
		return
	}
	if !handler.requireSpaceRole(c, pageValue.SpaceID, "writer") {
		return
	}
	file, err := header.Open()
	if err != nil {
		writeError(c, http.StatusBadRequest, "Failed to open uploaded file")
		return
	}
	defer file.Close()
	fileName := safeFileName(header.Filename)
	extension := strings.ToLower(filepath.Ext(fileName))
	if extension == "" || len(extension) > 16 {
		extension = ".bin"
		fileName += extension
	}
	mimeType, err := sniffMime(file)
	if err != nil {
		writeError(c, http.StatusBadRequest, "Failed to inspect uploaded file")
		return
	}

	attachmentID := strings.TrimSpace(c.PostForm("attachmentId"))
	var existing *domain.Attachment
	if attachmentID != "" {
		value, lookupErr := handler.repository.AttachmentByID(c.Request.Context(), attachmentID, current.Workspace.ID)
		if lookupErr != nil || value.PageID == nil || *value.PageID != pageValue.ID || !strings.EqualFold(value.FileExt, extension) {
			writeError(c, http.StatusBadRequest, "File attachment does not match")
			return
		}
		existing = &value
		fileName = value.FileName
	} else {
		attachmentID, err = randomUUID()
		if err != nil {
			writeError(c, http.StatusInternalServerError, "Failed to allocate attachment")
			return
		}
	}
	// Keep the storage layout compatible with Docmost Node. Existing records
	// keep using their persisted filePath; new uploads must be readable by both
	// the Go service and a temporarily retained Node service.
	relativePath := path.Join(current.Workspace.ID, "files", attachmentID, fileName)
	if existing != nil {
		relativePath = existing.FilePath
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		writeError(c, http.StatusBadRequest, "Failed to read uploaded file")
		return
	}
	size, err := handler.storage.Save(c.Request.Context(), relativePath, file, handler.maxUpload)
	if err != nil {
		if errors.Is(err, application.ErrUploadTooLarge) {
			writeError(c, http.StatusRequestEntityTooLarge, "File exceeds the configured upload limit")
		} else {
			writeError(c, http.StatusInternalServerError, "Failed to store uploaded file")
		}
		return
	}
	var attachment domain.Attachment
	if existing != nil {
		attachment, err = handler.repository.UpdateAttachmentSize(c.Request.Context(), attachmentID, pageValue.ID, current.Workspace.ID, extension, size)
	} else {
		attachment, err = handler.repository.CreateAttachment(c.Request.Context(), postgres.AttachmentInput{
			ID: attachmentID, FileName: fileName, FilePath: relativePath, FileSize: size,
			FileExt: extension, MimeType: mimeType, Type: "file", CreatorID: current.User.ID,
			WorkspaceID: current.Workspace.ID, PageID: &pageValue.ID, SpaceID: &pageValue.SpaceID,
		})
	}
	if err != nil {
		if existing == nil {
			_ = handler.storage.Delete(c.Request.Context(), relativePath)
		}
		writeError(c, http.StatusInternalServerError, "Failed to save attachment metadata")
		return
	}
	attachment.URL = handler.fileURL(c, attachment)
	// The upstream upload endpoint returns the attachment itself, rather than
	// the API envelope used by most JSON endpoints. The editor upload helpers
	// intentionally consume attachment.id/fileName/fileSize directly.
	c.JSON(http.StatusOK, attachment)
}

func (handler *Handler) uploadImage(c *gin.Context) {
	if handler.storage == nil {
		writeError(c, http.StatusServiceUnavailable, "File storage is unavailable")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxIconBytes+1024*1024)
	header, err := c.FormFile("image")
	if err != nil {
		writeError(c, http.StatusBadRequest, "Invalid image upload")
		return
	}
	attachmentType := c.PostForm("type")
	if attachmentType != "avatar" && attachmentType != "space-icon" && attachmentType != "workspace-icon" {
		writeError(c, http.StatusBadRequest, "Invalid image attachment type")
		return
	}
	current := currentPrincipal(c)
	spaceID := strings.TrimSpace(c.PostForm("spaceId"))
	if attachmentType == "workspace-icon" && !isAdmin(current.User) {
		writeError(c, http.StatusForbidden, "Forbidden")
		return
	}
	if attachmentType == "space-icon" {
		if !handler.requireSpaceRole(c, spaceID, "admin") {
			return
		}
	}
	file, err := header.Open()
	if err != nil {
		writeError(c, http.StatusBadRequest, "Failed to open uploaded image")
		return
	}
	defer file.Close()
	mimeType, err := sniffMime(file)
	if err != nil || (mimeType != "image/png" && mimeType != "image/jpeg" && mimeType != "image/gif" && mimeType != "image/webp") {
		writeError(c, http.StatusBadRequest, "Unsupported image type")
		return
	}
	extension := strings.ToLower(filepath.Ext(safeFileName(header.Filename)))
	if expected, _ := mime.ExtensionsByType(mimeType); extension == "" || !containsFold(expected, extension) {
		switch mimeType {
		case "image/png":
			extension = ".png"
		case "image/jpeg":
			extension = ".jpg"
		case "image/gif":
			extension = ".gif"
		case "image/webp":
			extension = ".webp"
		}
	}
	attachmentID, err := randomUUID()
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to allocate image")
		return
	}
	fileName := attachmentID + extension
	imageFolder := map[string]string{
		"avatar":         "avatars",
		"space-icon":     "space-logos",
		"workspace-icon": "workspace-logos",
	}[attachmentType]
	relativePath := path.Join(current.Workspace.ID, imageFolder, fileName)
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		writeError(c, http.StatusBadRequest, "Failed to read uploaded image")
		return
	}
	size, err := handler.storage.Save(c.Request.Context(), relativePath, file, maxIconBytes)
	if err != nil {
		writeError(c, http.StatusBadRequest, "Failed to store uploaded image")
		return
	}
	var optionalSpaceID *string
	if spaceID != "" {
		optionalSpaceID = &spaceID
	}
	attachment, err := handler.repository.CreateAttachment(c.Request.Context(), postgres.AttachmentInput{
		ID: attachmentID, FileName: fileName, FilePath: relativePath, FileSize: size,
		FileExt: extension, MimeType: mimeType, Type: attachmentType,
		CreatorID: current.User.ID, WorkspaceID: current.Workspace.ID, SpaceID: optionalSpaceID,
	})
	if err != nil {
		_ = handler.storage.Delete(c.Request.Context(), relativePath)
		writeError(c, http.StatusInternalServerError, "Failed to save image metadata")
		return
	}
	attachment.URL = handler.imageURL(c, attachment)
	switch attachmentType {
	case "avatar":
		err = handler.repository.SetUserAvatar(c.Request.Context(), current.User.ID, current.Workspace.ID, attachment.FileName)
	case "space-icon":
		err = handler.repository.SetSpaceLogo(c.Request.Context(), spaceID, current.Workspace.ID, attachment.FileName)
	case "workspace-icon":
		err = handler.repository.SetWorkspaceLogo(c.Request.Context(), current.Workspace.ID, attachment.FileName)
	}
	if err != nil {
		_ = handler.repository.DeleteAttachment(c.Request.Context(), attachment.ID, current.Workspace.ID)
		_ = handler.storage.Delete(c.Request.Context(), relativePath)
		writeError(c, http.StatusInternalServerError, "Failed to update image reference")
		return
	}
	// Match Node's upload-image response shape. In particular, do not wrap the
	// attachment in {data, success, status}; uploadIcon returns this value
	// directly to the editor/settings code.
	c.JSON(http.StatusOK, attachment)
}

func (handler *Handler) attachmentInfo(c *gin.Context) {
	var request struct {
		AttachmentID string `json:"attachmentId"`
	}
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	attachment, err := handler.repository.AttachmentByID(c.Request.Context(), request.AttachmentID, current.Workspace.ID)
	if err != nil || attachment.PageID == nil || attachment.Type == nil || *attachment.Type != "file" {
		writeError(c, http.StatusNotFound, "File not found")
		return
	}
	pageValue, err := handler.repository.PageByID(c.Request.Context(), *attachment.PageID, "", current.Workspace.ID, false)
	if err != nil {
		handler.writeRepositoryError(c, err, "Page not found")
		return
	}
	if !handler.requireSpaceRole(c, pageValue.SpaceID, "reader") {
		return
	}
	attachment.URL = handler.fileURL(c, attachment)
	writeData(c, http.StatusOK, attachment)
}

func (handler *Handler) pageAttachments(c *gin.Context) {
	var request struct {
		PageID string `json:"pageId"`
		Limit  int    `json:"limit"`
	}
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	pageValue, err := handler.repository.PageByID(c.Request.Context(), request.PageID, "", current.Workspace.ID, false)
	if err != nil {
		handler.writeRepositoryError(c, err, "Page not found")
		return
	}
	if !handler.requireSpaceRole(c, pageValue.SpaceID, "reader") {
		return
	}
	result, err := handler.repository.PageAttachments(c.Request.Context(), pageValue.ID, current.Workspace.ID, request.Limit)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to load attachments")
		return
	}
	for index := range result.Items {
		result.Items[index].URL = handler.fileURL(c, result.Items[index])
	}
	writeData(c, http.StatusOK, result)
}

func (handler *Handler) getFile(c *gin.Context) {
	current := currentPrincipal(c)
	attachment, err := handler.repository.AttachmentByID(c.Request.Context(), c.Param("fileId"), current.Workspace.ID)
	if err != nil || attachment.PageID == nil || attachment.Type == nil || *attachment.Type != "file" || c.Param("fileName") != attachment.FileName {
		writeError(c, http.StatusNotFound, "File not found")
		return
	}
	pageValue, err := handler.repository.PageByID(c.Request.Context(), *attachment.PageID, "", current.Workspace.ID, false)
	if err != nil {
		handler.writeRepositoryError(c, err, "Page not found")
		return
	}
	if !handler.requireSpaceRole(c, pageValue.SpaceID, "reader") {
		return
	}
	handler.serveAttachment(c, attachment, false)
}

func (handler *Handler) getPublicFile(c *gin.Context) {
	claims, err := handler.tokens.parse(c.Query("jwt"), "attachment")
	if err != nil {
		writeError(c, http.StatusBadRequest, "Expired or invalid attachment access token")
		return
	}
	fileID := c.Param("fileId")
	if fileID == "" || fileID != claims.AttachmentID {
		writeError(c, http.StatusNotFound, "File not found")
		return
	}
	attachment, err := handler.repository.AttachmentByID(c.Request.Context(), fileID, claims.WorkspaceID)
	if err != nil || attachment.PageID == nil || attachment.SpaceID == nil || attachment.Type == nil || *attachment.Type != "file" || claims.PageID != *attachment.PageID || c.Param("fileName") != attachment.FileName {
		writeError(c, http.StatusNotFound, "File not found")
		return
	}
	handler.serveAttachment(c, attachment, true)
}

func (handler *Handler) getPublicImage(c *gin.Context) {
	attachmentKey := c.Param("attachmentType")
	fileName := c.Param("fileName")
	var attachment domain.Attachment
	var err error
	if looksLikeUUID(attachmentKey) {
		// Keep serving URLs produced by older Go builds while stored avatar/logo
		// values and newly generated URLs follow the Node-compatible format.
		attachment, err = handler.repository.AttachmentByPublicImageID(c.Request.Context(), attachmentKey)
	} else {
		if !isPublicImageType(attachmentKey) {
			writeError(c, http.StatusBadRequest, "Invalid image attachment type")
			return
		}
		workspace, workspaceErr := handler.repository.OnlyWorkspace(c.Request.Context())
		if workspaceErr != nil {
			err = workspaceErr
		} else {
			attachment, err = handler.repository.AttachmentByPublicImagePath(c.Request.Context(), attachmentKey, fileName, workspace.ID)
		}
	}
	if err != nil || fileName != attachment.FileName {
		writeError(c, http.StatusNotFound, "Image not found")
		return
	}
	handler.serveAttachment(c, attachment, true)
}

func isPublicImageType(value string) bool {
	switch value {
	case "avatar", "space-icon", "workspace-icon":
		return true
	default:
		return false
	}
}

func (handler *Handler) serveAttachment(c *gin.Context, attachment domain.Attachment, public bool) {
	file, err := handler.storage.Open(c.Request.Context(), attachment.FilePath)
	if err != nil {
		writeError(c, http.StatusNotFound, "File not found")
		return
	}
	defer file.Close()
	if attachment.MimeType != nil {
		c.Header("Content-Type", *attachment.MimeType)
	}
	if public {
		c.Header("Cache-Control", "public, max-age=86400")
	} else {
		c.Header("Cache-Control", "private, max-age=86400")
	}
	// The editor renders PDFs and other inline attachments in an iframe. The
	// global security middleware sets X-Frame-Options: DENY for normal pages,
	// but that header would also block this already-authorized attachment.
	// Keep the attachment-specific CSP below and allow the browser to render
	// inline previews.
	c.Writer.Header().Del("X-Frame-Options")
	c.Header("Content-Security-Policy", "base-uri 'none'; object-src 'self'; default-src 'self';")
	if attachment.Type != nil && *attachment.Type == "file" && !inlineExtension(attachment.FileExt) {
		c.Header("Content-Disposition", fmt.Sprintf("attachment; filename*=UTF-8''%s", url.PathEscape(attachment.FileName)))
	}
	http.ServeContent(c.Writer, c.Request, attachment.FileName, attachment.UpdatedAt, file)
}

func (handler *Handler) removeIcon(c *gin.Context) {
	var request struct {
		Type    string `json:"type"`
		SpaceID string `json:"spaceId"`
	}
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	var spaceID *string
	switch request.Type {
	case "avatar":
		if err := handler.repository.SetUserAvatar(c.Request.Context(), current.User.ID, current.Workspace.ID, ""); err != nil {
			writeError(c, http.StatusInternalServerError, "Failed to remove avatar")
			return
		}
	case "space-icon":
		if !handler.requireSpaceRole(c, request.SpaceID, "admin") {
			return
		}
		spaceID = &request.SpaceID
		if err := handler.repository.SetSpaceLogo(c.Request.Context(), request.SpaceID, current.Workspace.ID, ""); err != nil {
			writeError(c, http.StatusInternalServerError, "Failed to remove space icon")
			return
		}
	case "workspace-icon":
		if !isAdmin(current.User) {
			writeError(c, http.StatusForbidden, "Forbidden")
			return
		}
		if err := handler.repository.SetWorkspaceLogo(c.Request.Context(), current.Workspace.ID, ""); err != nil {
			writeError(c, http.StatusInternalServerError, "Failed to remove workspace icon")
			return
		}
	default:
		writeError(c, http.StatusBadRequest, "Invalid image attachment type")
		return
	}
	paths, err := handler.repository.RemoveIconAttachments(c.Request.Context(), request.Type, current.Workspace.ID, current.User.ID, spaceID)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to remove image metadata")
		return
	}
	for _, filePath := range paths {
		_ = handler.storage.Delete(c.Request.Context(), filePath)
	}
	writeData(c, http.StatusOK, nil)
}

func (handler *Handler) fileURL(c *gin.Context, attachment domain.Attachment) string {
	return handler.backendURL(c) + "/api/files/" + url.PathEscape(attachment.ID) + "/" + url.PathEscape(attachment.FileName)
}

func (handler *Handler) imageURL(c *gin.Context, attachment domain.Attachment) string {
	attachmentType := "file"
	if attachment.Type != nil && *attachment.Type != "" {
		attachmentType = *attachment.Type
	}
	return handler.backendURL(c) + "/api/attachments/img/" + url.PathEscape(attachmentType) + "/" + url.PathEscape(attachment.FileName)
}

func (handler *Handler) backendURL(c *gin.Context) string {
	if handler.publicURL != "" && handler.publicURL != "CHANGE_ME" {
		return handler.publicURL
	}
	scheme := "http"
	if c.Request.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + c.Request.Host
}

func sniffMime(file interface {
	io.Reader
	io.Seeker
}) (string, error) {
	buffer := make([]byte, 512)
	read, err := file.Read(buffer)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	return http.DetectContentType(buffer[:read]), nil
}

func safeFileName(value string) string {
	value = filepath.Base(strings.ReplaceAll(value, "\\", "/"))
	value = strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return -1
		}
		return character
	}, value)
	value = strings.TrimSpace(value)
	if value == "" || value == "." {
		return "file"
	}
	if len(value) > 180 {
		extension := filepath.Ext(value)
		value = value[:160] + extension
	}
	return value
}

func randomUUID() (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	buffer[6] = (buffer[6] & 0x0f) | 0x40
	buffer[8] = (buffer[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(buffer)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32], nil
}

func looksLikeUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if character != '-' {
				return false
			}
			continue
		}
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') || (character >= 'A' && character <= 'F')) {
			return false
		}
	}
	return true
}

func containsFold(items []string, target string) bool {
	for _, item := range items {
		if strings.EqualFold(item, target) {
			return true
		}
	}
	return false
}

func inlineExtension(extension string) bool {
	switch strings.ToLower(extension) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".pdf", ".txt", ".mp3", ".mp4", ".webm", ".ogg":
		return true
	default:
		return false
	}
}
