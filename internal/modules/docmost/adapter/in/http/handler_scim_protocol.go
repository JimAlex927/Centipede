package http

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"centipede/internal/modules/docmost/adapter/out/postgres"
	"centipede/internal/modules/docmost/domain"

	"github.com/gin-gonic/gin"
)

const (
	scimCoreSchema    = "urn:ietf:params:scim:schemas:core:2.0"
	scimUserSchema    = scimCoreSchema + ":User"
	scimGroupSchema   = scimCoreSchema + ":Group"
	scimPatchSchema   = "urn:ietf:params:scim:api:messages:2.0:PatchOp"
	scimProtocolUser  = "User"
	scimProtocolGroup = "Group"
)

type scimUserPayload struct {
	Schemas     []string `json:"schemas"`
	ExternalID  *string  `json:"externalId"`
	UserName    string   `json:"userName"`
	DisplayName *string  `json:"displayName"`
	Name        scimName `json:"name"`
	Active      *bool    `json:"active"`
}

type scimName struct {
	Formatted  *string `json:"formatted"`
	GivenName  *string `json:"givenName"`
	FamilyName *string `json:"familyName"`
}

type scimGroupPayload struct {
	Schemas     []string         `json:"schemas"`
	ExternalID  *string          `json:"externalId"`
	DisplayName string           `json:"displayName"`
	Members     []scimMemberBody `json:"members"`
}

type scimMemberBody struct {
	Value   string `json:"value"`
	Display string `json:"display"`
}

type scimPatchRequest struct {
	Schemas    []string             `json:"schemas"`
	Operations []scimPatchOperation `json:"Operations"`
}

type scimPatchOperation struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value"`
}

type scimListResponse struct {
	Schemas      []string `json:"schemas"`
	TotalResults int      `json:"totalResults"`
	StartIndex   int      `json:"startIndex"`
	ItemsPerPage int      `json:"itemsPerPage"`
	Resources    any      `json:"Resources"`
}

func (handler *Handler) registerSCIMProtocolRoutes(router gin.IRouter) {
	router.GET("/scim/v2/ServiceProviderConfig", handler.scimServiceProviderConfig)
	router.GET("/scim/v2/ResourceTypes", handler.scimResourceTypes)
	router.GET("/scim/v2/ResourceTypes/:id", handler.scimResourceType)
	router.GET("/scim/v2/Schemas", handler.scimSchemas)
	router.GET("/scim/v2/Users", handler.scimUsers)
	router.POST("/scim/v2/Users", handler.scimUserCreate)
	router.GET("/scim/v2/Users/:id", handler.scimUserInfo)
	router.PUT("/scim/v2/Users/:id", handler.scimUserUpdate)
	router.PATCH("/scim/v2/Users/:id", handler.scimUserPatch)
	router.DELETE("/scim/v2/Users/:id", handler.scimUserDelete)
	router.GET("/scim/v2/Groups", handler.scimGroups)
	router.POST("/scim/v2/Groups", handler.scimGroupCreate)
	router.GET("/scim/v2/Groups/:id", handler.scimGroupInfo)
	router.PUT("/scim/v2/Groups/:id", handler.scimGroupUpdate)
	router.PATCH("/scim/v2/Groups/:id", handler.scimGroupPatch)
	router.DELETE("/scim/v2/Groups/:id", handler.scimGroupDelete)
}

func (handler *Handler) scimServiceProviderConfig(c *gin.Context) {
	if _, _, ok := handler.scimContext(c); !ok {
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"schemas":        []string{"urn:ietf:params:scim:schemas:core:2.0:ServiceProviderConfig"},
		"patch":          gin.H{"supported": true},
		"bulk":           gin.H{"supported": false, "maxOperations": 0, "maxPayloadSize": 0},
		"filter":         gin.H{"supported": true, "maxResults": 1000},
		"changePassword": gin.H{"supported": false},
		"sort":           gin.H{"supported": false},
		"etag":           gin.H{"supported": false},
		"authenticationSchemes": []map[string]any{{
			"name": "OAuth Bearer Token", "description": "SCIM bearer token", "specUri": "urn:ietf:params:scim:api:messages:2.0", "type": "oauthbearertoken", "primary": true,
		}},
	})
}

func (handler *Handler) scimResourceTypes(c *gin.Context) {
	if _, _, ok := handler.scimContext(c); !ok {
		return
	}
	resources := []map[string]any{
		{"schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:ResourceType"}, "id": "User", "name": "User", "endpoint": "/Users", "schema": scimUserSchema, "meta": gin.H{"resourceType": "ResourceType"}},
		{"schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:ResourceType"}, "id": "Group", "name": "Group", "endpoint": "/Groups", "schema": scimGroupSchema, "meta": gin.H{"resourceType": "ResourceType"}},
	}
	c.JSON(http.StatusOK, scimListResponse{Schemas: []string{scimCoreSchema + ":ListResponse"}, TotalResults: len(resources), StartIndex: 1, ItemsPerPage: len(resources), Resources: resources})
}

func (handler *Handler) scimResourceType(c *gin.Context) {
	if _, _, ok := handler.scimContext(c); !ok {
		return
	}
	resourceType := strings.ToLower(strings.TrimSpace(c.Param("id")))
	if resourceType != "user" && resourceType != "group" {
		scimNotFound(c, "ResourceType")
		return
	}
	schema := scimUserSchema
	if resourceType == "group" {
		schema = scimGroupSchema
	}
	name := strings.ToUpper(resourceType[:1]) + resourceType[1:]
	c.JSON(http.StatusOK, gin.H{"schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:ResourceType"}, "id": name, "name": name, "endpoint": "/" + name + "s", "schema": schema, "meta": gin.H{"resourceType": "ResourceType"}})
}

func (handler *Handler) scimSchemas(c *gin.Context) {
	if _, _, ok := handler.scimContext(c); !ok {
		return
	}
	resources := []map[string]any{
		{"id": scimUserSchema, "name": "User", "description": "SCIM User", "attributes": []any{}},
		{"id": scimGroupSchema, "name": "Group", "description": "SCIM Group", "attributes": []any{}},
	}
	c.JSON(http.StatusOK, scimListResponse{Schemas: []string{scimCoreSchema + ":ListResponse"}, TotalResults: len(resources), StartIndex: 1, ItemsPerPage: len(resources), Resources: resources})
}

func (handler *Handler) scimContext(c *gin.Context) (postgres.SCIMToken, domain.Workspace, bool) {
	authorization := strings.TrimSpace(c.GetHeader("Authorization"))
	if !strings.HasPrefix(strings.ToLower(authorization), "bearer ") {
		scimError(c, http.StatusUnauthorized, "A SCIM bearer token is required")
		return postgres.SCIMToken{}, domain.Workspace{}, false
	}
	tokenValue := strings.TrimSpace(authorization[len("Bearer "):])
	token, err := handler.repository.SCIMTokenByHash(c.Request.Context(), tokenValue)
	if err != nil {
		scimError(c, http.StatusUnauthorized, "Invalid or revoked SCIM token")
		return postgres.SCIMToken{}, domain.Workspace{}, false
	}
	workspace, err := handler.repository.WorkspaceByID(c.Request.Context(), token.WorkspaceID)
	if err != nil || !workspace.IsSCIMEnabled || !handler.workspaceHasFeature(c.Request.Context(), workspace.ID, "scim") {
		scimError(c, http.StatusForbidden, "SCIM provisioning is disabled")
		return postgres.SCIMToken{}, domain.Workspace{}, false
	}
	_ = handler.repository.TouchSCIMToken(c.Request.Context(), token.ID, token.WorkspaceID)
	c.Header("Content-Type", "application/scim+json")
	return token, workspace, true
}

func (handler *Handler) scimUsers(c *gin.Context) {
	_, workspace, ok := handler.scimContext(c)
	if !ok {
		return
	}
	filter, err := parseSCIMFilter(c.Query("filter"), "userName", "externalId")
	if err != nil {
		scimError(c, http.StatusBadRequest, err.Error())
		return
	}
	startIndex, count := scimPageQuery(c)
	items, total, err := handler.repository.SCIMUsers(c.Request.Context(), workspace.ID, filter, startIndex, count)
	if err != nil {
		scimError(c, http.StatusInternalServerError, "Failed to load SCIM users")
		return
	}
	resources := make([]map[string]any, 0, len(items))
	for _, item := range items {
		resources = append(resources, scimUserResource(c, item))
	}
	c.JSON(http.StatusOK, scimListResponse{Schemas: []string{scimCoreSchema + ":ListResponse"}, TotalResults: total, StartIndex: startIndex, ItemsPerPage: len(resources), Resources: resources})
}

func (handler *Handler) scimUserInfo(c *gin.Context) {
	_, workspace, ok := handler.scimContext(c)
	if !ok {
		return
	}
	item, err := handler.repository.SCIMUserByIDOrExternalID(c.Request.Context(), c.Param("id"), workspace.ID)
	if err != nil {
		scimNotFound(c, scimProtocolUser)
		return
	}
	c.JSON(http.StatusOK, scimUserResource(c, item))
}

func (handler *Handler) scimUserCreate(c *gin.Context) {
	_, workspace, ok := handler.scimContext(c)
	if !ok {
		return
	}
	var payload scimUserPayload
	if err := c.ShouldBindJSON(&payload); err != nil || strings.TrimSpace(payload.UserName) == "" {
		scimError(c, http.StatusBadRequest, "userName is required")
		return
	}
	item, err := handler.repository.CreateSCIMUser(c.Request.Context(), workspace.ID, postgres.SCIMUserInput{
		ExternalID: payload.ExternalID, UserName: payload.UserName, DisplayName: scimDisplayName(payload),
		GivenName: payload.Name.GivenName, FamilyName: payload.Name.FamilyName, Active: payload.Active,
	})
	if err != nil {
		scimError(c, http.StatusConflict, "A SCIM user with this userName or externalId already exists")
		return
	}
	c.Header("Location", scimResourceLocation(c, scimProtocolUser, item.ID))
	c.JSON(http.StatusCreated, scimUserResource(c, item))
}

func (handler *Handler) scimUserUpdate(c *gin.Context) {
	_, workspace, ok := handler.scimContext(c)
	if !ok {
		return
	}
	var payload scimUserPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		scimError(c, http.StatusBadRequest, "Invalid SCIM user resource")
		return
	}
	existing, err := handler.repository.SCIMUserByIDOrExternalID(c.Request.Context(), c.Param("id"), workspace.ID)
	if err != nil {
		scimNotFound(c, scimProtocolUser)
		return
	}
	userName := strings.TrimSpace(payload.UserName)
	if userName == "" {
		userName = existing.UserName
	}
	displayName := payload.DisplayName
	if displayName == nil {
		displayName = &existing.DisplayName
	}
	active := payload.Active
	if active == nil {
		active = &existing.Active
	}
	item, err := handler.repository.UpdateSCIMUser(c.Request.Context(), c.Param("id"), workspace.ID, postgres.SCIMUserInput{
		ExternalID: payload.ExternalID, UserName: userName, DisplayName: displayName, GivenName: payload.Name.GivenName,
		FamilyName: payload.Name.FamilyName, Active: active,
	})
	if err != nil {
		scimError(c, http.StatusConflict, "Failed to update SCIM user")
		return
	}
	c.JSON(http.StatusOK, scimUserResource(c, item))
}

func (handler *Handler) scimUserPatch(c *gin.Context) {
	_, workspace, ok := handler.scimContext(c)
	if !ok {
		return
	}
	existing, err := handler.repository.SCIMUserByIDOrExternalID(c.Request.Context(), c.Param("id"), workspace.ID)
	if err != nil {
		scimNotFound(c, scimProtocolUser)
		return
	}
	var patch scimPatchRequest
	if err = c.ShouldBindJSON(&patch); err != nil {
		scimError(c, http.StatusBadRequest, "Invalid SCIM patch request")
		return
	}
	input := postgres.SCIMUserInput{UserName: existing.UserName, DisplayName: &existing.DisplayName, Active: &existing.Active, ExternalID: existing.ExternalID}
	for _, operation := range patch.Operations {
		if err = applySCIMUserPatch(&input, operation); err != nil {
			scimError(c, http.StatusBadRequest, err.Error())
			return
		}
	}
	item, err := handler.repository.UpdateSCIMUser(c.Request.Context(), c.Param("id"), workspace.ID, input)
	if err != nil {
		scimError(c, http.StatusConflict, "Failed to patch SCIM user")
		return
	}
	c.JSON(http.StatusOK, scimUserResource(c, item))
}

func (handler *Handler) scimUserDelete(c *gin.Context) {
	_, workspace, ok := handler.scimContext(c)
	if !ok {
		return
	}
	if err := handler.repository.DeleteSCIMUser(c.Request.Context(), c.Param("id"), workspace.ID); err != nil {
		scimNotFound(c, scimProtocolUser)
		return
	}
	c.Status(http.StatusNoContent)
}

func (handler *Handler) scimGroups(c *gin.Context) {
	_, workspace, ok := handler.scimContext(c)
	if !ok {
		return
	}
	filter, err := parseSCIMFilter(c.Query("filter"), "displayName", "externalId")
	if err != nil {
		scimError(c, http.StatusBadRequest, err.Error())
		return
	}
	startIndex, count := scimPageQuery(c)
	items, total, err := handler.repository.SCIMGroups(c.Request.Context(), workspace.ID, filter, startIndex, count)
	if err != nil {
		scimError(c, http.StatusInternalServerError, "Failed to load SCIM groups")
		return
	}
	resources := make([]map[string]any, 0, len(items))
	for _, item := range items {
		resources = append(resources, scimGroupResource(c, item))
	}
	c.JSON(http.StatusOK, scimListResponse{Schemas: []string{scimCoreSchema + ":ListResponse"}, TotalResults: total, StartIndex: startIndex, ItemsPerPage: len(resources), Resources: resources})
}

func (handler *Handler) scimGroupInfo(c *gin.Context) {
	_, workspace, ok := handler.scimContext(c)
	if !ok {
		return
	}
	item, err := handler.repository.SCIMGroupByIDOrExternalID(c.Request.Context(), c.Param("id"), workspace.ID)
	if err != nil {
		scimNotFound(c, scimProtocolGroup)
		return
	}
	c.JSON(http.StatusOK, scimGroupResource(c, item))
}

func (handler *Handler) scimGroupCreate(c *gin.Context) {
	token, workspace, ok := handler.scimContext(c)
	if !ok {
		return
	}
	var payload scimGroupPayload
	if err := c.ShouldBindJSON(&payload); err != nil || strings.TrimSpace(payload.DisplayName) == "" {
		scimError(c, http.StatusBadRequest, "displayName is required")
		return
	}
	members := scimMemberValues(payload.Members)
	item, err := handler.repository.CreateSCIMGroup(c.Request.Context(), workspace.ID, token.CreatorID, postgres.SCIMGroupInput{ExternalID: payload.ExternalID, DisplayName: payload.DisplayName, Members: members})
	if err != nil {
		scimError(c, http.StatusConflict, "A SCIM group with this externalId already exists")
		return
	}
	c.Header("Location", scimResourceLocation(c, scimProtocolGroup, item.ID))
	c.JSON(http.StatusCreated, scimGroupResource(c, item))
}

func (handler *Handler) scimGroupUpdate(c *gin.Context) {
	_, workspace, ok := handler.scimContext(c)
	if !ok {
		return
	}
	var payload scimGroupPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		scimError(c, http.StatusBadRequest, "Invalid SCIM group resource")
		return
	}
	item, err := handler.repository.UpdateSCIMGroup(c.Request.Context(), c.Param("id"), workspace.ID, postgres.SCIMGroupInput{ExternalID: payload.ExternalID, DisplayName: payload.DisplayName, Members: scimMemberValues(payload.Members)}, true)
	if err != nil {
		scimNotFound(c, scimProtocolGroup)
		return
	}
	c.JSON(http.StatusOK, scimGroupResource(c, item))
}

func (handler *Handler) scimGroupPatch(c *gin.Context) {
	_, workspace, ok := handler.scimContext(c)
	if !ok {
		return
	}
	existing, err := handler.repository.SCIMGroupByIDOrExternalID(c.Request.Context(), c.Param("id"), workspace.ID)
	if err != nil {
		scimNotFound(c, scimProtocolGroup)
		return
	}
	var patch scimPatchRequest
	if err = c.ShouldBindJSON(&patch); err != nil {
		scimError(c, http.StatusBadRequest, "Invalid SCIM patch request")
		return
	}
	input := postgres.SCIMGroupInput{ExternalID: existing.ExternalID, DisplayName: existing.DisplayName}
	replaceMembers := false
	input.Members = make([]string, 0, len(existing.Members))
	for _, member := range existing.Members {
		input.Members = append(input.Members, member.Value)
	}
	for _, operation := range patch.Operations {
		path := strings.ToLower(strings.TrimSpace(operation.Path))
		switch path {
		case "displayname":
			value, valid := operation.Value.(string)
			if !valid || strings.TrimSpace(value) == "" {
				scimError(c, http.StatusBadRequest, "displayName must be a non-empty string")
				return
			}
			input.DisplayName = value
		case "externalid":
			value, valid := operation.Value.(string)
			if !valid {
				scimError(c, http.StatusBadRequest, "externalId must be a string")
				return
			}
			input.ExternalID = &value
		case "members":
			members, valid := operation.Value.([]any)
			if !valid {
				scimError(c, http.StatusBadRequest, "members must be an array")
				return
			}
			input.Members = scimMemberValuesFromAny(members)
			replaceMembers = true
		default:
			scimError(c, http.StatusBadRequest, fmt.Sprintf("Unsupported SCIM group patch path %q", operation.Path))
			return
		}
	}
	item, err := handler.repository.UpdateSCIMGroup(c.Request.Context(), c.Param("id"), workspace.ID, input, replaceMembers)
	if err != nil {
		scimError(c, http.StatusConflict, "Failed to patch SCIM group")
		return
	}
	c.JSON(http.StatusOK, scimGroupResource(c, item))
}

func (handler *Handler) scimGroupDelete(c *gin.Context) {
	_, workspace, ok := handler.scimContext(c)
	if !ok {
		return
	}
	if err := handler.repository.DeleteSCIMGroup(c.Request.Context(), c.Param("id"), workspace.ID); err != nil {
		scimNotFound(c, scimProtocolGroup)
		return
	}
	c.Status(http.StatusNoContent)
}

func scimUserResource(c *gin.Context, item postgres.SCIMUserResource) map[string]any {
	resource := map[string]any{
		"schemas": []string{scimUserSchema}, "id": item.ID, "userName": item.UserName,
		"displayName": item.DisplayName, "active": item.Active,
		"emails": []map[string]any{{"value": item.UserName, "type": "work", "primary": true}},
		"meta":   map[string]any{"resourceType": scimProtocolUser, "created": item.CreatedAt, "lastModified": item.UpdatedAt, "location": scimResourceLocation(c, scimProtocolUser, item.ID)},
	}
	if item.ExternalID != nil {
		resource["externalId"] = *item.ExternalID
	}
	return resource
}

func scimGroupResource(c *gin.Context, item postgres.SCIMGroupResource) map[string]any {
	members := make([]map[string]any, 0, len(item.Members))
	for _, member := range item.Members {
		members = append(members, map[string]any{"value": member.Value, "display": member.Display, "type": "User"})
	}
	resource := map[string]any{
		"schemas": []string{scimGroupSchema}, "id": item.ID, "displayName": item.DisplayName, "members": members,
		"meta": map[string]any{"resourceType": scimProtocolGroup, "created": item.CreatedAt, "lastModified": item.UpdatedAt, "location": scimResourceLocation(c, scimProtocolGroup, item.ID)},
	}
	if item.ExternalID != nil {
		resource["externalId"] = *item.ExternalID
	}
	return resource
}

func scimDisplayName(payload scimUserPayload) *string {
	if payload.DisplayName != nil && strings.TrimSpace(*payload.DisplayName) != "" {
		return payload.DisplayName
	}
	if payload.Name.Formatted != nil && strings.TrimSpace(*payload.Name.Formatted) != "" {
		return payload.Name.Formatted
	}
	return nil
}

func applySCIMUserPatch(input *postgres.SCIMUserInput, operation scimPatchOperation) error {
	path := strings.ToLower(strings.TrimSpace(operation.Path))
	if path == "" {
		if values, ok := operation.Value.(map[string]any); ok {
			for key, value := range values {
				if err := applySCIMUserPatch(input, scimPatchOperation{Op: operation.Op, Path: key, Value: value}); err != nil {
					return err
				}
			}
			return nil
		}
	}
	switch path {
	case "username":
		value, ok := operation.Value.(string)
		if !ok || strings.TrimSpace(value) == "" {
			return errors.New("userName must be a non-empty string")
		}
		input.UserName = value
	case "displayname":
		value, ok := operation.Value.(string)
		if !ok {
			return errors.New("displayName must be a string")
		}
		input.DisplayName = &value
	case "externalid":
		value, ok := operation.Value.(string)
		if !ok {
			return errors.New("externalId must be a string")
		}
		input.ExternalID = &value
	case "active":
		value, ok := operation.Value.(bool)
		if !ok {
			return errors.New("active must be a boolean")
		}
		input.Active = &value
	case "name.givenname":
		value, ok := operation.Value.(string)
		if !ok {
			return errors.New("givenName must be a string")
		}
		input.GivenName = &value
	case "name.familyname":
		value, ok := operation.Value.(string)
		if !ok {
			return errors.New("familyName must be a string")
		}
		input.FamilyName = &value
	default:
		return fmt.Errorf("unsupported SCIM user patch path %q", operation.Path)
	}
	return nil
}

func parseSCIMFilter(filter string, allowed ...string) (string, error) {
	filter = strings.TrimSpace(filter)
	if filter == "" {
		return "", nil
	}
	parts := strings.Fields(filter)
	if len(parts) != 3 || !strings.EqualFold(parts[1], "eq") {
		return "", errors.New("only SCIM equality filters are supported")
	}
	validAttribute := false
	for _, candidate := range allowed {
		if strings.EqualFold(parts[0], candidate) {
			validAttribute = true
			break
		}
	}
	if !validAttribute {
		return "", fmt.Errorf("unsupported SCIM filter attribute %q", parts[0])
	}
	value := strings.Trim(parts[2], "\"")
	if value == "" {
		return "", errors.New("SCIM filter value is required")
	}
	return value, nil
}

func scimPageQuery(c *gin.Context) (int, int) {
	startIndex, _ := strconv.Atoi(c.DefaultQuery("startIndex", "1"))
	count, _ := strconv.Atoi(c.DefaultQuery("count", "100"))
	if startIndex < 1 {
		startIndex = 1
	}
	if count < 1 || count > 1000 {
		count = 100
	}
	return startIndex, count
}

func scimMemberValues(members []scimMemberBody) []string {
	values := make([]string, 0, len(members))
	for _, member := range members {
		if strings.TrimSpace(member.Value) != "" {
			values = append(values, strings.TrimSpace(member.Value))
		}
	}
	return values
}

func scimMemberValuesFromAny(values []any) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if member, ok := value.(map[string]any); ok {
			if id, ok := member["value"].(string); ok && strings.TrimSpace(id) != "" {
				result = append(result, strings.TrimSpace(id))
			}
		}
	}
	return result
}

func scimResourceLocation(c *gin.Context, resourceType, id string) string {
	scheme := "http"
	if c.Request.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + c.Request.Host + "/api/scim/v2/" + resourceType + "/" + id
}

func scimNotFound(c *gin.Context, resourceType string) {
	scimError(c, http.StatusNotFound, resourceType+" resource not found")
}

func scimError(c *gin.Context, status int, detail string) {
	c.Header("Content-Type", "application/scim+json")
	c.JSON(status, gin.H{"schemas": []string{scimCoreSchema + ":Error"}, "detail": detail, "status": strconv.Itoa(status)})
}
