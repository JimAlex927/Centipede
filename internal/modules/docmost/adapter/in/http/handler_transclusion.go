package http

import (
	"encoding/json"
	"net/http"
	"strings"

	"centipede/internal/modules/docmost/adapter/out/postgres"
	"centipede/internal/modules/docmost/domain"

	"github.com/gin-gonic/gin"
)

type transclusionReference struct {
	SourcePageID   string `json:"sourcePageId"`
	TransclusionID string `json:"transclusionId"`
}

type transclusionLookupRequest struct {
	ShareID    string                  `json:"shareId"`
	References []transclusionReference `json:"references"`
}

func (handler *Handler) transclusionLookup(c *gin.Context) {
	var request transclusionLookupRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	items := make([]any, len(request.References))
	for index, reference := range request.References {
		items[index] = handler.lookupTransclusionForUser(c, current, reference)
	}
	writeData(c, http.StatusOK, gin.H{"items": items})
}

func (handler *Handler) transclusionReferences(c *gin.Context) {
	var request transclusionReference
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	response := gin.H{"source": nil, "references": []any{}}
	source, err := handler.repository.PageByID(c.Request.Context(), request.SourcePageID, request.SourcePageID, current.Workspace.ID, false)
	if err != nil {
		writeData(c, http.StatusOK, response)
		return
	}
	if handler.canViewPage(c, current, source) {
		response["source"] = transclusionPageInfo(source)
	}
	ids, err := handler.repository.PageTransclusionReferenceIDs(c.Request.Context(), request.SourcePageID, request.TransclusionID, current.Workspace.ID)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to load transclusion references")
		return
	}
	references := make([]any, 0, len(ids))
	for _, id := range ids {
		page, pageErr := handler.repository.PageByID(c.Request.Context(), id, id, current.Workspace.ID, false)
		if pageErr == nil && handler.canViewPage(c, current, page) {
			references = append(references, transclusionPageInfo(page))
		}
	}
	response["references"] = references
	writeData(c, http.StatusOK, response)
}

func (handler *Handler) unsyncTransclusionReference(c *gin.Context) {
	var request struct {
		ReferencePageID string `json:"referencePageId"`
		SourcePageID    string `json:"sourcePageId"`
		TransclusionID  string `json:"transclusionId"`
	}
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	referencePage, err := handler.repository.PageByID(c.Request.Context(), request.ReferencePageID, request.ReferencePageID, current.Workspace.ID, false)
	if err != nil {
		handler.writeRepositoryError(c, err, "Reference page not found")
		return
	}
	if !handler.requirePageAccess(c, referencePage, true) {
		return
	}
	sourcePage, err := handler.repository.PageByID(c.Request.Context(), request.SourcePageID, request.SourcePageID, current.Workspace.ID, false)
	if err != nil {
		handler.writeRepositoryError(c, err, "Source page not found")
		return
	}
	if !handler.canViewPage(c, current, sourcePage) {
		writeError(c, http.StatusForbidden, "Forbidden")
		return
	}
	content, found := transclusionContent(sourcePage.Content, request.TransclusionID)
	if !found {
		writeError(c, http.StatusNotFound, "Transclusion not found")
		return
	}
	rewrittenContent, copies, rewriteErr := rewriteTransclusionAttachments(content)
	if rewriteErr != nil {
		writeError(c, http.StatusInternalServerError, "Failed to prepare transclusion attachments")
		return
	}
	if len(copies) > 0 && handler.storage != nil {
		oldIDs := make([]string, 0, len(copies))
		for _, copy := range copies {
			oldIDs = append(oldIDs, copy.OldID)
		}
		oldAttachments, attachmentErr := handler.repository.AttachmentsByIDs(c.Request.Context(), oldIDs, current.Workspace.ID)
		if attachmentErr != nil {
			writeError(c, http.StatusInternalServerError, "Failed to load transclusion attachments")
			return
		}
		byID := make(map[string]domain.Attachment, len(oldAttachments))
		for _, attachment := range oldAttachments {
			if attachment.PageID != nil && *attachment.PageID == sourcePage.ID {
				byID[attachment.ID] = attachment
			}
		}
		for _, copy := range copies {
			old, ok := byID[copy.OldID]
			if !ok {
				rewrittenContent = replaceTransclusionAttachmentID(rewrittenContent, copy.NewID, copy.OldID)
				continue
			}
			file, openErr := handler.storage.Open(c.Request.Context(), old.FilePath)
			if openErr != nil {
				rewrittenContent = replaceTransclusionAttachmentID(rewrittenContent, copy.NewID, copy.OldID)
				continue
			}
			newPath := strings.ReplaceAll(old.FilePath, copy.OldID, copy.NewID)
			limit := handler.maxUpload
			if limit < old.FileSize {
				limit = old.FileSize
			}
			if limit <= 0 {
				limit = maxImportedAttachmentSize
			}
			_, saveErr := handler.storage.Save(c.Request.Context(), newPath, file, limit)
			_ = file.Close()
			if saveErr != nil {
				rewrittenContent = replaceTransclusionAttachmentID(rewrittenContent, copy.NewID, copy.OldID)
				continue
			}
			pageID := referencePage.ID
			spaceID := referencePage.SpaceID
			if _, createErr := handler.repository.CreateAttachment(c.Request.Context(), postgres.AttachmentInput{
				ID: copy.NewID, FileName: old.FileName, FilePath: newPath, FileSize: old.FileSize, FileExt: old.FileExt,
				MimeType: valueOrEmpty(old.MimeType), Type: valueOrEmpty(old.Type), CreatorID: current.User.ID,
				WorkspaceID: current.Workspace.ID, PageID: &pageID, SpaceID: &spaceID,
			}); createErr != nil {
				_ = handler.storage.Delete(c.Request.Context(), newPath)
				rewrittenContent = replaceTransclusionAttachmentID(rewrittenContent, copy.NewID, copy.OldID)
			}
		}
	}
	if err := handler.repository.DeletePageTransclusionReference(c.Request.Context(), referencePage.ID, sourcePage.ID, request.TransclusionID, current.Workspace.ID); err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to unsync transclusion")
		return
	}
	writeData(c, http.StatusOK, gin.H{"content": rewrittenContent})
}

type transclusionAttachmentCopy struct {
	OldID string
	NewID string
}

func rewriteTransclusionAttachments(raw json.RawMessage) (json.RawMessage, []transclusionAttachmentCopy, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, nil, err
	}
	copies := make([]transclusionAttachmentCopy, 0)
	idMap := make(map[string]string)
	var walk func(any) error
	walk = func(current any) error {
		switch node := current.(type) {
		case []any:
			for _, child := range node {
				if err := walk(child); err != nil {
					return err
				}
			}
		case map[string]any:
			if attrs, ok := node["attrs"].(map[string]any); ok {
				if oldID, ok := attrs["attachmentId"].(string); ok && oldID != "" {
					newID, found := idMap[oldID]
					if !found {
						var err error
						newID, err = randomUUID()
						if err != nil {
							return err
						}
						idMap[oldID] = newID
						copies = append(copies, transclusionAttachmentCopy{OldID: oldID, NewID: newID})
					}
					attrs["attachmentId"] = newID
					for _, key := range []string{"src", "url"} {
						if value, ok := attrs[key].(string); ok {
							attrs[key] = strings.ReplaceAll(value, oldID, newID)
						}
					}
				}
			}
			for _, child := range node {
				if err := walk(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(value); err != nil {
		return nil, nil, err
	}
	encoded, err := json.Marshal(value)
	return encoded, copies, err
}

func replaceTransclusionAttachmentID(raw json.RawMessage, from, to string) json.RawMessage {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return raw
	}
	var walk func(any)
	walk = func(current any) {
		switch node := current.(type) {
		case []any:
			for _, child := range node {
				walk(child)
			}
		case map[string]any:
			if attrs, ok := node["attrs"].(map[string]any); ok {
				if attachmentID, ok := attrs["attachmentId"].(string); ok && attachmentID == from {
					attrs["attachmentId"] = to
				}
				for _, key := range []string{"src", "url"} {
					if value, ok := attrs[key].(string); ok {
						attrs[key] = strings.ReplaceAll(value, from, to)
					}
				}
			}
			for _, child := range node {
				walk(child)
			}
		}
	}
	walk(value)
	encoded, err := json.Marshal(value)
	if err != nil {
		return raw
	}
	return encoded
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func transclusionPageInfo(page domain.Page) gin.H {
	var spaceSlug *string
	if page.Space != nil {
		spaceSlug = &page.Space.Slug
	}
	return gin.H{"id": page.ID, "slugId": page.SlugID, "title": page.Title, "icon": page.Icon, "spaceId": page.SpaceID, "spaceSlug": spaceSlug}
}

func (handler *Handler) lookupTransclusionForUser(c *gin.Context, current principal, reference transclusionReference) map[string]any {
	base := map[string]any{"sourcePageId": reference.SourcePageID, "transclusionId": reference.TransclusionID}
	if reference.SourcePageID == "" || reference.TransclusionID == "" {
		base["status"] = "not_found"
		return base
	}
	page, err := handler.repository.PageByID(c.Request.Context(), reference.SourcePageID, reference.SourcePageID, current.Workspace.ID, false)
	if err != nil {
		base["status"] = "not_found"
		return base
	}
	if !handler.canViewPage(c, current, page) {
		base["status"] = "no_access"
		return base
	}
	content, found := transclusionContent(page.Content, reference.TransclusionID)
	if !found {
		base["status"] = "not_found"
		return base
	}
	base["content"] = content
	base["sourceUpdatedAt"] = page.UpdatedAt
	return base
}

func (handler *Handler) shareTransclusionLookup(c *gin.Context) {
	var request transclusionLookupRequest
	if !decode(c, &request) {
		return
	}
	if request.ShareID == "" {
		writeError(c, http.StatusBadRequest, "shareId is required")
		return
	}
	share, err := handler.publicShare(c, request.ShareID)
	if err != nil {
		writeError(c, http.StatusNotFound, "Share not found")
		return
	}
	items := make([]any, len(request.References))
	for index, reference := range request.References {
		items[index] = handler.lookupTransclusionForShare(c, share, reference)
	}
	writeData(c, http.StatusOK, gin.H{"items": items})
}

func (handler *Handler) lookupTransclusionForShare(c *gin.Context, share domain.Share, reference transclusionReference) map[string]any {
	base := map[string]any{"sourcePageId": reference.SourcePageID, "transclusionId": reference.TransclusionID}
	if reference.SourcePageID == "" || reference.TransclusionID == "" {
		base["status"] = "no_access"
		return base
	}
	contains, err := handler.repository.ShareContainsPage(c.Request.Context(), share, reference.SourcePageID)
	if err != nil || !contains {
		base["status"] = "no_access"
		return base
	}
	page, err := handler.repository.PageByID(c.Request.Context(), reference.SourcePageID, reference.SourcePageID, share.WorkspaceID, false)
	if err != nil {
		base["status"] = "no_access"
		return base
	}
	restricted, err := handler.repository.PageHasRestrictedAncestor(c.Request.Context(), page.ID, share.WorkspaceID)
	if err != nil || restricted {
		base["status"] = "no_access"
		return base
	}
	content, found := transclusionContent(page.Content, reference.TransclusionID)
	if !found {
		base["status"] = "not_found"
		return base
	}
	content, err = handler.preparePublicPageContent(content, page.ID, share.WorkspaceID)
	if err != nil {
		base["status"] = "no_access"
		return base
	}
	base["content"] = content
	base["sourceUpdatedAt"] = page.UpdatedAt
	return base
}

func (handler *Handler) canViewPage(c *gin.Context, current principal, page domain.Page) bool {
	if isAdmin(current.User) {
		return true
	}
	role, err := handler.repository.SpaceRole(c.Request.Context(), page.SpaceID, current.Workspace.ID, current.User.ID)
	if err != nil {
		return false
	}
	weight := map[string]int{"reader": 1, "writer": 2, "admin": 3}
	if weight[role] < 1 {
		return false
	}
	access, err := handler.repository.PageAccess(c.Request.Context(), page.ID, current.Workspace.ID, current.User.ID)
	return err == nil && access.CanAccess
}

func transclusionContent(raw json.RawMessage, transclusionID string) (json.RawMessage, bool) {
	var root any
	if len(raw) == 0 || json.Unmarshal(raw, &root) != nil {
		return nil, false
	}
	var walk func(any) (json.RawMessage, bool)
	walk = func(value any) (json.RawMessage, bool) {
		if children, ok := value.([]any); ok {
			for _, child := range children {
				if found, ok := walk(child); ok {
					return found, true
				}
			}
			return nil, false
		}
		node, ok := value.(map[string]any)
		if !ok {
			return nil, false
		}
		if nodeType, _ := node["type"].(string); nodeType == "transclusionSource" {
			attrs, _ := node["attrs"].(map[string]any)
			if id, _ := attrs["id"].(string); id == transclusionID {
				content, ok := node["content"]
				if !ok {
					return json.RawMessage(`{"type":"doc","content":[]}`), true
				}
				encoded, err := json.Marshal(content)
				return encoded, err == nil
			}
			return nil, false
		}
		for _, child := range node {
			if found, ok := walk(child); ok {
				return found, true
			}
		}
		return nil, false
	}
	return walk(root)
}
