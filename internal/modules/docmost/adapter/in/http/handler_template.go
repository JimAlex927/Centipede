package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"centipede/internal/modules/docmost/adapter/out/postgres"
	"centipede/internal/modules/docmost/domain"

	"github.com/gin-gonic/gin"
)

func (handler *Handler) registerTemplateRoutes(router gin.IRouter) {
	router.POST("/templates", handler.templates)
	router.POST("/templates/info", handler.templateInfo)
	router.POST("/templates/create", handler.createTemplate)
	router.POST("/templates/update", handler.updateTemplate)
	router.POST("/templates/delete", handler.deleteTemplate)
	router.POST("/templates/use", handler.useTemplate)
}

type templateRequest struct {
	TemplateID   string          `json:"templateId"`
	Title        string          `json:"title"`
	Description  *string         `json:"description"`
	Icon         *string         `json:"icon"`
	SpaceID      *string         `json:"spaceId"`
	Content      json.RawMessage `json:"content"`
	Cursor       string          `json:"cursor"`
	Limit        int             `json:"limit"`
	ParentPageID *string         `json:"parentPageId"`
}

// templateUpdateRequest retains whether nullable fields were supplied. This
// lets the editor clear an icon or move a template back to workspace scope,
// while still allowing partial updates from API clients.
type templateUpdateRequest struct {
	TemplateID  string          `json:"templateId"`
	Title       *string         `json:"title"`
	Description *string         `json:"description"`
	Icon        *string         `json:"icon"`
	SpaceID     *string         `json:"spaceId"`
	Content     json.RawMessage `json:"content"`
	setTitle    bool
	setDesc     bool
	setIcon     bool
	setSpace    bool
	setContent  bool
}

func (request *templateUpdateRequest) UnmarshalJSON(data []byte) error {
	type plain templateUpdateRequest
	var value plain
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*request = templateUpdateRequest(value)
	_, request.setTitle = fields["title"]
	_, request.setDesc = fields["description"]
	_, request.setIcon = fields["icon"]
	_, request.setSpace = fields["spaceId"]
	_, request.setContent = fields["content"]
	return nil
}

func (handler *Handler) templates(c *gin.Context) {
	if !handler.requireFeature(c, "templates") {
		return
	}
	var request templateRequest
	if !decodeOptional(c, &request) {
		return
	}
	current := currentPrincipal(c)
	spaceID := ""
	if request.SpaceID != nil {
		spaceID = strings.TrimSpace(*request.SpaceID)
		if spaceID != "" && !handler.requireSpaceRole(c, spaceID, "reader") {
			return
		}
	}
	result, err := handler.repository.Templates(c.Request.Context(), current.Workspace.ID, current.User.ID, isAdmin(current.User), spaceID, request.Cursor, request.Limit)
	if err != nil {
		if errors.Is(err, postgres.ErrInvalidInput) {
			writeError(c, http.StatusBadRequest, "Invalid pagination cursor")
			return
		}
		writeError(c, http.StatusInternalServerError, "Failed to load templates")
		return
	}
	writeData(c, http.StatusOK, result)
}

func (handler *Handler) templateInfo(c *gin.Context) {
	if !handler.requireFeature(c, "templates") {
		return
	}
	var request templateRequest
	if !decode(c, &request) || request.TemplateID == "" {
		return
	}
	current := currentPrincipal(c)
	template, err := handler.repository.TemplateByID(c.Request.Context(), request.TemplateID, current.Workspace.ID, current.User.ID, isAdmin(current.User), true)
	if err != nil {
		handler.writeRepositoryError(c, err, "Template not found")
		return
	}
	writeData(c, http.StatusOK, template)
}

func (handler *Handler) createTemplate(c *gin.Context) {
	if !handler.requireFeature(c, "templates") {
		return
	}
	var request templateRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	if request.SpaceID == nil || strings.TrimSpace(*request.SpaceID) == "" {
		if !isAdmin(current.User) {
			writeError(c, http.StatusForbidden, "Only workspace administrators can create global templates")
			return
		}
	} else if !handler.requireSpaceRole(c, strings.TrimSpace(*request.SpaceID), "writer") {
		return
	}
	template, err := handler.repository.CreateTemplate(c.Request.Context(), current.Workspace.ID, current.User.ID, postgres.TemplateCreateInput{
		Title: strings.TrimSpace(request.Title), Description: request.Description, Icon: request.Icon,
		SpaceID: normalizedStringPointer(request.SpaceID), Content: request.Content,
	})
	if err != nil {
		if errors.Is(err, postgres.ErrInvalidInput) {
			writeError(c, http.StatusBadRequest, "Template title is required")
			return
		}
		writeError(c, http.StatusInternalServerError, "Failed to create template")
		return
	}
	writeData(c, http.StatusOK, template)
}

func (handler *Handler) updateTemplate(c *gin.Context) {
	if !handler.requireFeature(c, "templates") {
		return
	}
	var request templateUpdateRequest
	if !decode(c, &request) || request.TemplateID == "" {
		return
	}
	current := currentPrincipal(c)
	existing, err := handler.repository.TemplateByID(c.Request.Context(), request.TemplateID, current.Workspace.ID, current.User.ID, isAdmin(current.User), false)
	if err != nil {
		handler.writeRepositoryError(c, err, "Template not found")
		return
	}
	if !handler.templateCanWrite(c, existing) {
		return
	}
	if request.setSpace {
		if request.SpaceID == nil || strings.TrimSpace(*request.SpaceID) == "" {
			if !isAdmin(current.User) {
				writeError(c, http.StatusForbidden, "Only workspace administrators can create global templates")
				return
			}
		} else if !handler.requireSpaceRole(c, strings.TrimSpace(*request.SpaceID), "writer") {
			return
		}
	}
	template, err := handler.repository.UpdateTemplate(c.Request.Context(), request.TemplateID, current.Workspace.ID, current.User.ID, postgres.TemplateUpdateInput{
		Title: request.Title, SetTitle: request.setTitle,
		Description: request.Description, SetDescription: request.setDesc,
		Icon: request.Icon, SetIcon: request.setIcon,
		SpaceID: normalizedStringPointer(request.SpaceID), SetSpaceID: request.setSpace,
		Content: request.Content, SetContent: request.setContent,
	})
	if err != nil {
		if errors.Is(err, postgres.ErrInvalidInput) {
			writeError(c, http.StatusBadRequest, "Invalid template update")
			return
		}
		handler.writeRepositoryError(c, err, "Template not found")
		return
	}
	writeData(c, http.StatusOK, template)
}

func (handler *Handler) deleteTemplate(c *gin.Context) {
	if !handler.requireFeature(c, "templates") {
		return
	}
	var request templateRequest
	if !decode(c, &request) || request.TemplateID == "" {
		return
	}
	current := currentPrincipal(c)
	template, err := handler.repository.TemplateByID(c.Request.Context(), request.TemplateID, current.Workspace.ID, current.User.ID, isAdmin(current.User), false)
	if err != nil {
		handler.writeRepositoryError(c, err, "Template not found")
		return
	}
	if !handler.templateCanWrite(c, template) {
		return
	}
	if err := handler.repository.DeleteTemplate(c.Request.Context(), request.TemplateID, current.Workspace.ID); err != nil {
		handler.writeRepositoryError(c, err, "Template not found")
		return
	}
	writeData(c, http.StatusOK, nil)
}

func (handler *Handler) useTemplate(c *gin.Context) {
	if !handler.requireFeature(c, "templates") {
		return
	}
	var request templateRequest
	if !decode(c, &request) || request.TemplateID == "" || request.SpaceID == nil || strings.TrimSpace(*request.SpaceID) == "" {
		writeError(c, http.StatusBadRequest, "templateId and spaceId are required")
		return
	}
	current := currentPrincipal(c)
	template, err := handler.repository.TemplateByID(c.Request.Context(), request.TemplateID, current.Workspace.ID, current.User.ID, isAdmin(current.User), true)
	if err != nil {
		handler.writeRepositoryError(c, err, "Template not found")
		return
	}
	spaceID := strings.TrimSpace(*request.SpaceID)
	if !handler.requireSpaceRole(c, spaceID, "writer") {
		return
	}
	title := template.Title
	content := template.Content
	if len(content) == 0 || string(content) == "null" {
		content = json.RawMessage(`{"type":"doc","content":[]}`)
	}
	page, err := handler.repository.CreatePage(c.Request.Context(), current.Workspace.ID, current.User.ID, postgres.PageInput{
		Title: &title, Icon: template.Icon, ParentPageID: request.ParentPageID,
		SpaceID: &spaceID, Content: content,
	})
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to create page from template")
		return
	}
	writeData(c, http.StatusOK, withPagePermissions(page))
}

func (handler *Handler) templateCanWrite(c *gin.Context, template domain.Template) bool {
	current := currentPrincipal(c)
	if template.SpaceID == nil || strings.TrimSpace(*template.SpaceID) == "" {
		if !isAdmin(current.User) {
			writeError(c, http.StatusForbidden, "Forbidden")
			return false
		}
		return true
	}
	return handler.requireSpaceRole(c, *template.SpaceID, "writer")
}

func normalizedStringPointer(value *string) *string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil
	}
	result := strings.TrimSpace(*value)
	return &result
}
