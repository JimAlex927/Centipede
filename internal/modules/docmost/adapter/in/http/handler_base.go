package http

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"centipede/internal/modules/docmost/adapter/out/postgres"
	"centipede/internal/modules/docmost/domain"

	"github.com/gin-gonic/gin"
)

type baseRequest struct {
	PageID        string          `json:"pageId"`
	SpaceID       string          `json:"spaceId"`
	ParentPageID  string          `json:"parentPageId"`
	Name          string          `json:"name"`
	Description   *string         `json:"description"`
	Icon          *string         `json:"icon"`
	Template      string          `json:"template"`
	Cursor        string          `json:"cursor"`
	Limit         int             `json:"limit"`
	PropertyID    string          `json:"propertyId"`
	RowID         string          `json:"rowId"`
	ViewID        string          `json:"viewId"`
	Type          string          `json:"type"`
	TypeOptions   json.RawMessage `json:"typeOptions"`
	Position      string          `json:"position"`
	Cells         json.RawMessage `json:"cells"`
	RowIDs        []string        `json:"rowIds"`
	Config        json.RawMessage `json:"config"`
	RequestID     string          `json:"requestId"`
	AfterRowID    string          `json:"afterRowId"`
	Filter        json.RawMessage `json:"filter"`
	Sorts         json.RawMessage `json:"sorts"`
	AllowMultiple *bool           `json:"allowMultiple"`
	PageIDs       []string        `json:"pageIds"`
}

func (handler *Handler) bases(c *gin.Context) {
	if !handler.requireFeature(c, "bases") {
		return
	}
	var request baseRequest
	if !decodeOptional(c, &request) {
		return
	}
	current := currentPrincipal(c)
	if request.SpaceID == "" || !handler.requireSpaceRole(c, request.SpaceID, "reader") {
		return
	}
	result, err := handler.repository.Bases(c.Request.Context(), current.Workspace.ID, request.SpaceID, request.Cursor, request.Limit)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to load bases")
		return
	}
	writeData(c, http.StatusOK, result)
}

func (handler *Handler) expandBasePages(c *gin.Context) {
	if !handler.requireFeature(c, "bases") {
		return
	}
	var request baseRequest
	if !decode(c, &request) {
		return
	}
	if len(request.PageIDs) > 100 {
		writeError(c, http.StatusBadRequest, "At most 100 pages can be expanded at once")
		return
	}
	current := currentPrincipal(c)
	items := make([]gin.H, 0, len(request.PageIDs))
	seen := map[string]struct{}{}
	for _, pageID := range request.PageIDs {
		if pageID == "" {
			continue
		}
		if _, exists := seen[pageID]; exists {
			continue
		}
		seen[pageID] = struct{}{}
		page, err := handler.repository.PageByID(c.Request.Context(), pageID, pageID, current.Workspace.ID, false)
		if err != nil {
			continue
		}
		access, err := handler.repository.PageAccess(c.Request.Context(), page.ID, current.Workspace.ID, current.User.ID)
		if err != nil || (access.HasRestriction && !access.CanAccess) {
			continue
		}
		items = append(items, gin.H{
			"id": page.ID, "slugId": page.SlugID, "title": page.Title, "icon": page.Icon,
			"spaceId": page.SpaceID, "space": page.Space,
		})
	}
	writeData(c, http.StatusOK, gin.H{"items": items})
}

func (handler *Handler) baseInfo(c *gin.Context) {
	if !handler.requireFeature(c, "bases") {
		return
	}
	var request baseRequest
	if !decode(c, &request) || request.PageID == "" {
		return
	}
	base, ok := handler.loadBase(c, request.PageID, "reader")
	if !ok {
		return
	}
	writeData(c, http.StatusOK, baseWithPermissions(base, true))
}

func (handler *Handler) createBase(c *gin.Context) {
	if !handler.requireFeature(c, "bases") {
		return
	}
	var request baseRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	spaceID := request.SpaceID
	if request.ParentPageID != "" {
		parent, err := handler.repository.PageByID(c.Request.Context(), request.ParentPageID, request.ParentPageID, current.Workspace.ID, false)
		if err != nil {
			handler.writeRepositoryError(c, err, "Parent page not found")
			return
		}
		spaceID = parent.SpaceID
	}
	if spaceID == "" || !handler.requireSpaceRole(c, spaceID, "writer") {
		return
	}
	name := strings.TrimSpace(request.Name)
	if name == "" {
		name = "Untitled"
	}
	page, err := handler.repository.CreatePage(c.Request.Context(), current.Workspace.ID, current.User.ID, postgres.PageInput{Title: &name, SpaceID: &spaceID})
	if err != nil {
		writeError(c, http.StatusBadRequest, "Failed to create base")
		return
	}
	if err = handler.repository.SetPageAsBase(c.Request.Context(), page.ID, current.Workspace.ID, true); err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to initialize base")
		return
	}
	if err = handler.repository.EnsureBaseDefaults(c.Request.Context(), page.ID, current.Workspace.ID, current.User.ID, request.Template); err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to initialize base schema")
		return
	}
	base, err := handler.repository.BaseByPage(c.Request.Context(), page.ID, current.Workspace.ID)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to load created base")
		return
	}
	writeData(c, http.StatusOK, baseWithPermissions(base, true))
}

func (handler *Handler) convertPageToBase(c *gin.Context) {
	if !handler.requireFeature(c, "bases") {
		return
	}
	var request baseRequest
	if !decode(c, &request) || request.PageID == "" {
		return
	}
	current := currentPrincipal(c)
	page, ok := handler.loadBasePage(c, request.PageID, "convert")
	if !ok {
		return
	}
	if err := handler.repository.SetPageAsBase(c.Request.Context(), page.ID, current.Workspace.ID, true); err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to convert page to base")
		return
	}
	if err := handler.repository.EnsureBaseDefaults(c.Request.Context(), page.ID, current.Workspace.ID, current.User.ID, request.Template); err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to initialize base schema")
		return
	}
	base, err := handler.repository.BaseByPage(c.Request.Context(), page.ID, current.Workspace.ID)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to load base")
		return
	}
	writeData(c, http.StatusOK, baseWithPermissions(base, true))
}

func (handler *Handler) updateBase(c *gin.Context) {
	if !handler.requireFeature(c, "bases") {
		return
	}
	var request baseRequest
	if !decode(c, &request) || request.PageID == "" {
		return
	}
	page, ok := handler.loadBasePage(c, request.PageID, "writer")
	if !ok {
		return
	}
	updated, err := handler.repository.UpdatePage(c.Request.Context(), page.ID, currentPrincipal(c).Workspace.ID, currentPrincipal(c).User.ID, postgres.PageInput{Title: optionalText(request.Name), Icon: request.Icon})
	if err != nil {
		handler.writeRepositoryError(c, err, "Base not found")
		return
	}
	base, err := handler.repository.BaseByPage(c.Request.Context(), updated.ID, currentPrincipal(c).Workspace.ID)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to load base")
		return
	}
	writeData(c, http.StatusOK, baseWithPermissions(base, true))
}

func (handler *Handler) deleteBase(c *gin.Context) {
	if !handler.requireFeature(c, "bases") {
		return
	}
	var request baseRequest
	if !decode(c, &request) || request.PageID == "" {
		return
	}
	page, ok := handler.loadBasePage(c, request.PageID, "writer")
	if !ok {
		return
	}
	if err := handler.repository.DeletePage(c.Request.Context(), page.ID, currentPrincipal(c).Workspace.ID, currentPrincipal(c).User.ID, false); err != nil {
		handler.writeRepositoryError(c, err, "Base not found")
		return
	}
	writeData(c, http.StatusOK, nil)
}

func (handler *Handler) basePropertiesCreate(c *gin.Context) {
	if !handler.requireFeature(c, "bases") {
		return
	}
	var request baseRequest
	if !decode(c, &request) || request.PageID == "" || strings.TrimSpace(request.Name) == "" || strings.TrimSpace(request.Type) == "" {
		return
	}
	if _, ok := handler.loadBasePage(c, request.PageID, "writer"); !ok {
		return
	}
	current := currentPrincipal(c)
	property, err := handler.repository.CreateBaseProperty(c.Request.Context(), postgres.BaseProperty{PageID: request.PageID, Name: strings.TrimSpace(request.Name), Type: request.Type, TypeOptions: request.TypeOptions, WorkspaceID: current.Workspace.ID})
	if err != nil {
		writeError(c, http.StatusBadRequest, "Failed to create property")
		return
	}
	writeData(c, http.StatusOK, property)
}

func (handler *Handler) basePropertiesUpdate(c *gin.Context) {
	if !handler.requireFeature(c, "bases") {
		return
	}
	var request baseRequest
	if !decode(c, &request) || request.PageID == "" || request.PropertyID == "" {
		return
	}
	if _, ok := handler.loadBasePage(c, request.PageID, "writer"); !ok {
		return
	}
	current := currentPrincipal(c)
	property, err := handler.repository.UpdateBaseProperty(c.Request.Context(), request.PageID, request.PropertyID, current.Workspace.ID, optionalText(request.Name), optionalText(request.Type), request.TypeOptions)
	if err != nil {
		handler.writeRepositoryError(c, err, "Property not found")
		return
	}
	writeData(c, http.StatusOK, gin.H{"property": property, "jobId": nil})
}

func (handler *Handler) basePropertiesDelete(c *gin.Context) {
	if !handler.requireFeature(c, "bases") {
		return
	}
	var request baseRequest
	if !decode(c, &request) || request.PageID == "" || request.PropertyID == "" {
		return
	}
	if _, ok := handler.loadBasePage(c, request.PageID, "writer"); !ok {
		return
	}
	if err := handler.repository.DeleteBaseProperty(c.Request.Context(), request.PageID, request.PropertyID, currentPrincipal(c).Workspace.ID); err != nil {
		handler.writeRepositoryError(c, err, "Property not found")
		return
	}
	writeData(c, http.StatusOK, nil)
}

func (handler *Handler) basePropertiesReorder(c *gin.Context) {
	if !handler.requireFeature(c, "bases") {
		return
	}
	var request baseRequest
	if !decode(c, &request) || request.PageID == "" || request.PropertyID == "" || request.Position == "" {
		return
	}
	if _, ok := handler.loadBasePage(c, request.PageID, "writer"); !ok {
		return
	}
	if err := handler.repository.ReorderBaseProperty(c.Request.Context(), request.PageID, request.PropertyID, currentPrincipal(c).Workspace.ID, request.Position); err != nil {
		handler.writeRepositoryError(c, err, "Property not found")
		return
	}
	writeData(c, http.StatusOK, nil)
}

func (handler *Handler) baseRows(c *gin.Context) {
	if !handler.requireFeature(c, "bases") {
		return
	}
	var request baseRequest
	if !decodeOptional(c, &request) || request.PageID == "" {
		return
	}
	if _, ok := handler.loadBasePage(c, request.PageID, "reader"); !ok {
		return
	}
	var result domain.Pagination[postgres.BaseRow]
	var err error
	if len(request.Filter) > 0 || len(request.Sorts) > 0 {
		result, err = handler.repository.BaseRowsWithOptions(c.Request.Context(), request.PageID, currentPrincipal(c).Workspace.ID, request.Cursor, request.Limit, request.Filter, request.Sorts)
	} else {
		result, err = handler.repository.BaseRows(c.Request.Context(), request.PageID, currentPrincipal(c).Workspace.ID, request.Cursor, request.Limit)
	}
	if err != nil {
		if errors.Is(err, postgres.ErrInvalidInput) {
			writeError(c, http.StatusBadRequest, "Invalid base view filter or sort")
			return
		}
		writeError(c, http.StatusInternalServerError, "Failed to load rows")
		return
	}
	references, err := handler.repository.BaseRowReferences(c.Request.Context(), currentPrincipal(c).Workspace.ID, currentPrincipal(c).User.ID, result.Items)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to load row references")
		return
	}
	writeData(c, http.StatusOK, gin.H{
		"items":      result.Items,
		"meta":       result.Meta,
		"references": references,
	})
}

func (handler *Handler) baseRowInfo(c *gin.Context) {
	if !handler.requireFeature(c, "bases") {
		return
	}
	var request baseRequest
	if !decode(c, &request) || request.PageID == "" || request.RowID == "" {
		return
	}
	if _, ok := handler.loadBasePage(c, request.PageID, "reader"); !ok {
		return
	}
	row, err := handler.repository.BaseRowByID(c.Request.Context(), request.PageID, request.RowID, currentPrincipal(c).Workspace.ID)
	if err != nil {
		handler.writeRepositoryError(c, err, "Row not found")
		return
	}
	writeData(c, http.StatusOK, row)
}

func (handler *Handler) baseRowsCreate(c *gin.Context) {
	if !handler.requireFeature(c, "bases") {
		return
	}
	var request baseRequest
	if !decode(c, &request) || request.PageID == "" {
		return
	}
	if _, ok := handler.loadBasePage(c, request.PageID, "writer"); !ok {
		return
	}
	current := currentPrincipal(c)
	row, err := handler.repository.CreateBaseRow(c.Request.Context(), request.PageID, current.Workspace.ID, current.User.ID, request.Position, request.Cells)
	if err != nil {
		writeError(c, http.StatusBadRequest, "Failed to create row")
		return
	}
	writeData(c, http.StatusOK, row)
}

func (handler *Handler) baseRowsUpdate(c *gin.Context) {
	if !handler.requireFeature(c, "bases") {
		return
	}
	var request baseRequest
	if !decode(c, &request) || request.PageID == "" || request.RowID == "" {
		return
	}
	if _, ok := handler.loadBasePage(c, request.PageID, "writer"); !ok {
		return
	}
	row, err := handler.repository.UpdateBaseRow(c.Request.Context(), request.PageID, request.RowID, currentPrincipal(c).Workspace.ID, currentPrincipal(c).User.ID, request.Cells, optionalText(request.Position))
	if err != nil {
		handler.writeRepositoryError(c, err, "Row not found")
		return
	}
	writeData(c, http.StatusOK, row)
}

func (handler *Handler) baseRowsDelete(c *gin.Context) {
	if !handler.requireFeature(c, "bases") {
		return
	}
	var request baseRequest
	if !decode(c, &request) || request.PageID == "" || request.RowID == "" {
		return
	}
	if _, ok := handler.loadBasePage(c, request.PageID, "writer"); !ok {
		return
	}
	if err := handler.repository.DeleteBaseRows(c.Request.Context(), request.PageID, currentPrincipal(c).Workspace.ID, []string{request.RowID}); err != nil {
		handler.writeRepositoryError(c, err, "Row not found")
		return
	}
	writeData(c, http.StatusOK, nil)
}

func (handler *Handler) baseRowsDeleteMany(c *gin.Context) {
	if !handler.requireFeature(c, "bases") {
		return
	}
	var request baseRequest
	if !decode(c, &request) || request.PageID == "" || len(request.RowIDs) == 0 {
		return
	}
	if _, ok := handler.loadBasePage(c, request.PageID, "writer"); !ok {
		return
	}
	if err := handler.repository.DeleteBaseRows(c.Request.Context(), request.PageID, currentPrincipal(c).Workspace.ID, request.RowIDs); err != nil {
		handler.writeRepositoryError(c, err, "Rows not found")
		return
	}
	writeData(c, http.StatusOK, nil)
}

func (handler *Handler) baseRowsReorder(c *gin.Context) {
	if !handler.requireFeature(c, "bases") {
		return
	}
	var request baseRequest
	if !decode(c, &request) || request.PageID == "" || request.RowID == "" || request.Position == "" {
		return
	}
	if _, ok := handler.loadBasePage(c, request.PageID, "writer"); !ok {
		return
	}
	if err := handler.repository.ReorderBaseRow(c.Request.Context(), request.PageID, request.RowID, currentPrincipal(c).Workspace.ID, request.Position); err != nil {
		handler.writeRepositoryError(c, err, "Row not found")
		return
	}
	writeData(c, http.StatusOK, nil)
}

func (handler *Handler) baseViews(c *gin.Context) {
	if !handler.requireFeature(c, "bases") {
		return
	}
	var request baseRequest
	if !decode(c, &request) || request.PageID == "" {
		return
	}
	if _, ok := handler.loadBasePage(c, request.PageID, "reader"); !ok {
		return
	}
	views, err := handler.repository.BaseViews(c.Request.Context(), request.PageID, currentPrincipal(c).Workspace.ID)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to load views")
		return
	}
	writeData(c, http.StatusOK, views)
}

func (handler *Handler) baseViewsCreate(c *gin.Context) {
	if !handler.requireFeature(c, "bases") {
		return
	}
	var request baseRequest
	if !decode(c, &request) || request.PageID == "" || strings.TrimSpace(request.Name) == "" {
		return
	}
	if _, ok := handler.loadBasePage(c, request.PageID, "writer"); !ok {
		return
	}
	current := currentPrincipal(c)
	view, err := handler.repository.CreateBaseView(c.Request.Context(), request.PageID, current.Workspace.ID, current.User.ID, strings.TrimSpace(request.Name), request.Type, request.Position, request.Config)
	if err != nil {
		writeError(c, http.StatusBadRequest, "Failed to create view")
		return
	}
	writeData(c, http.StatusOK, view)
}

func (handler *Handler) baseViewsUpdate(c *gin.Context) {
	if !handler.requireFeature(c, "bases") {
		return
	}
	var request baseRequest
	if !decode(c, &request) || request.PageID == "" || request.ViewID == "" {
		return
	}
	if _, ok := handler.loadBasePage(c, request.PageID, "writer"); !ok {
		return
	}
	view, err := handler.repository.UpdateBaseView(c.Request.Context(), request.PageID, request.ViewID, currentPrincipal(c).Workspace.ID, optionalText(request.Name), optionalText(request.Type), request.Config, optionalText(request.Position))
	if err != nil {
		handler.writeRepositoryError(c, err, "View not found")
		return
	}
	writeData(c, http.StatusOK, view)
}

func (handler *Handler) baseViewsDelete(c *gin.Context) {
	if !handler.requireFeature(c, "bases") {
		return
	}
	var request baseRequest
	if !decode(c, &request) || request.PageID == "" || request.ViewID == "" {
		return
	}
	if _, ok := handler.loadBasePage(c, request.PageID, "writer"); !ok {
		return
	}
	if err := handler.repository.DeleteBaseView(c.Request.Context(), request.PageID, request.ViewID, currentPrincipal(c).Workspace.ID); err != nil {
		handler.writeRepositoryError(c, err, "View not found")
		return
	}
	writeData(c, http.StatusOK, nil)
}

func (handler *Handler) exportBaseCSV(c *gin.Context) {
	if !handler.requireFeature(c, "bases") {
		return
	}
	var request baseRequest
	if !decode(c, &request) || request.PageID == "" {
		return
	}
	base, ok := handler.loadBase(c, request.PageID, "reader")
	if !ok {
		return
	}
	rows, err := handler.repository.BaseRows(c.Request.Context(), base.PageID, currentPrincipal(c).Workspace.ID, "", 100)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to export base")
		return
	}
	var output bytes.Buffer
	writer := csv.NewWriter(&output)
	header := make([]string, 0, len(base.Properties))
	for _, property := range base.Properties {
		header = append(header, property.Name)
	}
	_ = writer.Write(header)
	for _, row := range rows.Items {
		var cells map[string]any
		_ = json.Unmarshal(row.Cells, &cells)
		values := make([]string, 0, len(base.Properties))
		for _, property := range base.Properties {
			values = append(values, fmt.Sprint(cells[property.ID]))
		}
		_ = writer.Write(values)
	}
	writer.Flush()
	c.Header("Content-Disposition", `attachment; filename*=UTF-8''base.csv`)
	c.Data(http.StatusOK, "text/csv; charset=utf-8", output.Bytes())
}

func (handler *Handler) loadBase(c *gin.Context, pageID, minimumRole string) (postgres.Base, bool) {
	page, ok := handler.loadBasePage(c, pageID, minimumRole)
	if !ok {
		return postgres.Base{}, false
	}
	base, err := handler.repository.BaseByPage(c.Request.Context(), page.ID, currentPrincipal(c).Workspace.ID)
	if err != nil {
		handler.writeRepositoryError(c, err, "Base not found")
		return postgres.Base{}, false
	}
	return base, true
}

func (handler *Handler) loadBasePage(c *gin.Context, pageID, minimumRole string) (domain.Page, bool) {
	current := currentPrincipal(c)
	page, err := handler.repository.PageByID(c.Request.Context(), pageID, pageID, current.Workspace.ID, false)
	if err != nil {
		handler.writeRepositoryError(c, err, "Base not found")
		return domain.Page{}, false
	}
	if !page.IsBase && minimumRole != "convert" {
		handler.writeRepositoryError(c, postgres.ErrNotFound, "Base not found")
		return domain.Page{}, false
	}
	role := minimumRole
	if role == "convert" {
		role = "writer"
	}
	if !handler.requireSpaceRole(c, page.SpaceID, role) {
		return domain.Page{}, false
	}
	return page, true
}

func baseWithPermissions(base postgres.Base, canEdit bool) gin.H {
	contents, _ := json.Marshal(base)
	var result gin.H
	_ = json.Unmarshal(contents, &result)
	result["permissions"] = gin.H{"canEdit": canEdit, "hasRestriction": false}
	return result
}

func optionalText(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	trimmed := strings.TrimSpace(value)
	return &trimmed
}
