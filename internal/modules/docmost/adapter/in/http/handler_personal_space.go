package http

import (
	"encoding/json"
	"errors"
	"net/http"

	"centipede/internal/modules/docmost/adapter/out/postgres"

	"github.com/gin-gonic/gin"
)

func (handler *Handler) personalSpaceInfo(c *gin.Context) {
	if !handler.requireFeature(c, "spaces:personal") {
		return
	}
	current := currentPrincipal(c)
	space, err := handler.repository.PersonalSpaceByUser(c.Request.Context(), current.Workspace.ID, current.User.ID)
	if errors.Is(err, postgres.ErrNotFound) {
		writeData(c, http.StatusOK, nil)
		return
	}
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to load personal space")
		return
	}
	space.Membership.Permissions = spacePermissions(space.Membership.Role)
	writeData(c, http.StatusOK, space)
}

func (handler *Handler) createPersonalSpace(c *gin.Context) {
	if !handler.requireFeature(c, "spaces:personal") {
		return
	}
	current := currentPrincipal(c)
	if !personalSpacesAllowed(current.Workspace.Settings) {
		writeError(c, http.StatusForbidden, "Personal spaces are disabled for this workspace")
		return
	}
	var request struct {
		Name string `json:"name"`
	}
	if !decodeOptional(c, &request) {
		return
	}
	if existing, err := handler.repository.PersonalSpaceByUser(c.Request.Context(), current.Workspace.ID, current.User.ID); err == nil {
		writeData(c, http.StatusOK, existing)
		return
	} else if !errors.Is(err, postgres.ErrNotFound) {
		writeError(c, http.StatusInternalServerError, "Failed to inspect personal space")
		return
	}
	space, err := handler.repository.CreatePersonalSpace(c.Request.Context(), current.Workspace.ID, current.User.ID, request.Name)
	if err != nil {
		writeError(c, http.StatusBadRequest, "Failed to create personal space")
		return
	}
	space.Membership.Permissions = spacePermissions(space.Membership.Role)
	writeData(c, http.StatusOK, space)
}

func personalSpacesAllowed(settings json.RawMessage) bool {
	var values map[string]any
	if len(settings) == 0 || json.Unmarshal(settings, &values) != nil {
		return false
	}
	spaces, ok := values["spaces"].(map[string]any)
	if !ok {
		return false
	}
	allowed, _ := spaces["allowPersonal"].(bool)
	return allowed
}

func setPersonalSpacesSetting(settings json.RawMessage, allowed bool) json.RawMessage {
	values := map[string]any{}
	if len(settings) > 0 {
		_ = json.Unmarshal(settings, &values)
	}
	spaces, _ := values["spaces"].(map[string]any)
	if spaces == nil {
		spaces = map[string]any{}
	}
	spaces["allowPersonal"] = allowed
	values["spaces"] = spaces
	encoded, err := json.Marshal(values)
	if err != nil {
		return settings
	}
	return encoded
}
