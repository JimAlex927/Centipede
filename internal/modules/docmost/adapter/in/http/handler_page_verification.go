package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"centipede/internal/modules/docmost/adapter/out/postgres"
	"centipede/internal/modules/docmost/domain"
	"github.com/gin-gonic/gin"
)

const (
	verificationFeature = "page:verification"
	maxPageVerifiers    = 10
)

type pageVerificationRequest struct {
	PageID         string   `json:"pageId"`
	Type           string   `json:"type"`
	Mode           string   `json:"mode"`
	PeriodAmount   *int     `json:"periodAmount"`
	PeriodUnit     string   `json:"periodUnit"`
	FixedExpiresAt string   `json:"fixedExpiresAt"`
	VerifierIDs    []string `json:"verifierIds"`
	Comment        string   `json:"comment"`
	SpaceIDs       []string `json:"spaceIds"`
	VerifierID     string   `json:"verifierId"`
	Cursor         string   `json:"cursor"`
	BeforeCursor   string   `json:"beforeCursor"`
	Limit          int      `json:"limit"`
	Query          string   `json:"query"`
}

func (handler *Handler) registerPageVerificationRoutes(router gin.IRouter) {
	router.POST("/pages/verification-info", handler.pageVerificationInfo)
	router.POST("/pages/create-verification", handler.createPageVerification)
	router.POST("/pages/update-verification", handler.updatePageVerification)
	router.POST("/pages/delete-verification", handler.deletePageVerification)
	router.POST("/pages/verify", handler.verifyPage)
	router.POST("/pages/submit-for-approval", handler.submitPageForApproval)
	router.POST("/pages/reject-approval", handler.rejectPageApproval)
	router.POST("/pages/mark-obsolete", handler.markPageObsolete)
	router.POST("/pages/verifications", handler.pageVerificationList)
}

func (handler *Handler) pageVerificationList(c *gin.Context) {
	var request pageVerificationRequest
	if !decodeOptional(c, &request) || !handler.requireFeature(c, verificationFeature) {
		return
	}
	current := currentPrincipal(c)
	items, err := handler.repository.PageVerificationList(c.Request.Context(), postgres.PageVerificationListInput{
		WorkspaceID: current.Workspace.ID,
		ViewerID:    current.User.ID,
		ViewerAdmin: isAdmin(current.User),
		SpaceIDs:    request.SpaceIDs,
		VerifierID:  request.VerifierID,
		Type:        request.Type,
		Cursor:      request.Cursor,
		Limit:       request.Limit,
		Query:       strings.TrimSpace(request.Query),
	})
	if err != nil {
		handler.writeRepositoryError(c, err, "Failed to load page verifications")
		return
	}
	writeData(c, http.StatusOK, items)
}

func (handler *Handler) pageVerificationInfo(c *gin.Context) {
	var request pageVerificationRequest
	if !decode(c, &request) || !handler.requireFeature(c, verificationFeature) {
		return
	}
	page, current, ok := handler.pageForPermission(c, request.PageID)
	if !ok || !handler.requirePageAccess(c, page, false) {
		return
	}
	info, err := handler.repository.PageVerificationInfo(c.Request.Context(), page.ID, current.Workspace.ID)
	if err != nil {
		handler.writeRepositoryError(c, err, "Failed to load page verification")
		return
	}
	permissions, err := handler.pageVerificationPermissions(c, page, info)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to verify page permissions")
		return
	}
	infoJSON, err := json.Marshal(info)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to encode page verification")
		return
	}
	infoMap := gin.H{}
	if err := json.Unmarshal(infoJSON, &infoMap); err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to encode page verification")
		return
	}
	infoMap["permissions"] = permissions
	writeData(c, http.StatusOK, infoMap)
}

func (handler *Handler) createPageVerification(c *gin.Context) {
	var request pageVerificationRequest
	if !decode(c, &request) || !handler.requireFeature(c, verificationFeature) {
		return
	}
	page, current, ok := handler.pageForPermission(c, request.PageID)
	if !ok || !handler.requirePageAccess(c, page, true) {
		return
	}
	typ := request.Type
	if typ == "" {
		typ = "expiring"
	}
	mode, amount, unit, expiresAt, valid := verificationConfig(request, typ)
	if !valid || len(request.VerifierIDs) == 0 || len(request.VerifierIDs) > maxPageVerifiers {
		writeError(c, http.StatusBadRequest, "Invalid page verification configuration")
		return
	}
	if err := handler.validateVerifierIDs(c, current.Workspace.ID, request.VerifierIDs); err != nil {
		writeError(c, http.StatusBadRequest, "Invalid verifier")
		return
	}
	err := handler.repository.CreatePageVerification(c.Request.Context(), postgres.PageVerificationCreateInput{
		PageID: page.ID, WorkspaceID: current.Workspace.ID, SpaceID: page.SpaceID,
		Type: typ, Mode: mode, PeriodAmount: amount, PeriodUnit: unit,
		ExpiresAt: expiresAt, CreatorID: current.User.ID, VerifierIDs: request.VerifierIDs,
	})
	if err != nil {
		handler.writeRepositoryError(c, err, "Failed to create page verification")
		return
	}
	handler.recordPageVerificationAudit(c, current, page, "page.verification_created")
	handler.publishPageVerificationEvent(current.Workspace.ID, page)
	writeData(c, http.StatusOK, nil)
}

func (handler *Handler) updatePageVerification(c *gin.Context) {
	var request pageVerificationRequest
	if !decode(c, &request) || !handler.requireFeature(c, verificationFeature) {
		return
	}
	page, current, ok := handler.pageForPermission(c, request.PageID)
	if !ok || !handler.requirePageAccess(c, page, true) {
		return
	}
	info, err := handler.repository.PageVerificationInfo(c.Request.Context(), page.ID, current.Workspace.ID)
	if err != nil || info.Status == "none" {
		writeError(c, http.StatusNotFound, "Page verification not found")
		return
	}
	if len(request.VerifierIDs) == 0 || len(request.VerifierIDs) > maxPageVerifiers {
		writeError(c, http.StatusBadRequest, "Between 1 and 10 verifiers are required")
		return
	}
	if err := handler.validateVerifierIDs(c, current.Workspace.ID, request.VerifierIDs); err != nil {
		writeError(c, http.StatusBadRequest, "Invalid verifier")
		return
	}
	mode := request.Mode
	if mode == "" && info.Mode != nil {
		mode = *info.Mode
	}
	amount := request.PeriodAmount
	if amount == nil {
		amount = info.PeriodAmount
	}
	unit := request.PeriodUnit
	if unit == "" && info.PeriodUnit != nil {
		unit = *info.PeriodUnit
	}
	configRequest := request
	configRequest.Type = info.Type
	configRequest.Mode = mode
	configRequest.PeriodAmount = amount
	configRequest.PeriodUnit = unit
	if request.FixedExpiresAt == "" && info.ExpiresAt != nil && mode == "fixed" {
		configRequest.FixedExpiresAt = info.ExpiresAt.Format(time.RFC3339)
	}
	configMode, amount, configUnit, expiresAt, valid := verificationConfig(configRequest, info.Type)
	if !valid {
		writeError(c, http.StatusBadRequest, "Invalid page verification configuration")
		return
	}
	if err := handler.repository.UpdatePageVerification(c.Request.Context(), postgres.PageVerificationUpdateInput{
		PageID: page.ID, WorkspaceID: current.Workspace.ID, CreatorID: current.User.ID,
		Mode: configMode, PeriodAmount: amount, PeriodUnit: configUnit,
		ExpiresAt: expiresAt, VerifierIDs: request.VerifierIDs,
	}); err != nil {
		handler.writeRepositoryError(c, err, "Failed to update page verification")
		return
	}
	handler.recordPageVerificationAudit(c, current, page, "page.verification_updated")
	handler.publishPageVerificationEvent(current.Workspace.ID, page)
	writeData(c, http.StatusOK, nil)
}

func (handler *Handler) deletePageVerification(c *gin.Context) {
	var request pageVerificationRequest
	if !decode(c, &request) || !handler.requireFeature(c, verificationFeature) {
		return
	}
	page, current, ok := handler.pageForPermission(c, request.PageID)
	if !ok || !handler.requirePageAccess(c, page, true) {
		return
	}
	if err := handler.repository.DeletePageVerification(c.Request.Context(), page.ID, current.Workspace.ID); err != nil {
		handler.writeRepositoryError(c, err, "Page verification not found")
		return
	}
	handler.recordPageVerificationAudit(c, current, page, "page.verification_removed")
	handler.publishPageVerificationEvent(current.Workspace.ID, page)
	writeData(c, http.StatusOK, nil)
}

func (handler *Handler) verifyPage(c *gin.Context) {
	var request pageVerificationRequest
	if !decode(c, &request) || !handler.requireFeature(c, verificationFeature) {
		return
	}
	page, current, ok := handler.pageForPermission(c, request.PageID)
	if !ok || !handler.requirePageAccess(c, page, false) {
		return
	}
	canVerify, err := handler.repository.PageVerificationCanVerify(c.Request.Context(), page.ID, current.Workspace.ID, current.User.ID)
	if err != nil || (!canVerify && !isAdmin(current.User)) {
		writeError(c, http.StatusForbidden, "You are not assigned as a verifier")
		return
	}
	info, err := handler.repository.PageVerificationInfo(c.Request.Context(), page.ID, current.Workspace.ID)
	if err != nil || info.Status == "none" || (info.Type == "qms" && info.Status != "in_approval") {
		writeError(c, http.StatusBadRequest, "Page cannot be verified in its current state")
		return
	}
	if err := handler.repository.VerifyPage(c.Request.Context(), page.ID, current.Workspace.ID, current.User.ID); err != nil {
		handler.writeRepositoryError(c, err, "Failed to verify page")
		return
	}
	handler.publishPageVerificationEvent(current.Workspace.ID, page)
	writeData(c, http.StatusOK, nil)
}

func (handler *Handler) submitPageForApproval(c *gin.Context) {
	var request pageVerificationRequest
	if !decode(c, &request) || !handler.requireFeature(c, verificationFeature) {
		return
	}
	page, current, ok := handler.pageForPermission(c, request.PageID)
	if !ok || !handler.requirePageAccess(c, page, true) {
		return
	}
	if err := handler.repository.SubmitPageVerification(c.Request.Context(), page.ID, current.Workspace.ID, current.User.ID); err != nil {
		writeError(c, http.StatusBadRequest, "Page cannot be submitted for approval")
		return
	}
	handler.publishPageVerificationEvent(current.Workspace.ID, page)
	writeData(c, http.StatusOK, nil)
}

func (handler *Handler) rejectPageApproval(c *gin.Context) {
	var request pageVerificationRequest
	if !decode(c, &request) || !handler.requireFeature(c, verificationFeature) {
		return
	}
	page, current, ok := handler.pageForPermission(c, request.PageID)
	if !ok || !handler.requirePageAccess(c, page, false) {
		return
	}
	canVerify, err := handler.repository.PageVerificationCanVerify(c.Request.Context(), page.ID, current.Workspace.ID, current.User.ID)
	if err != nil || (!canVerify && !isAdmin(current.User)) {
		writeError(c, http.StatusForbidden, "You are not assigned as a verifier")
		return
	}
	if len(request.Comment) > 500 {
		writeError(c, http.StatusBadRequest, "Rejection comment is too long")
		return
	}
	if err := handler.repository.RejectPageVerification(c.Request.Context(), page.ID, current.Workspace.ID, current.User.ID, strings.TrimSpace(request.Comment)); err != nil {
		writeError(c, http.StatusBadRequest, "Page cannot be rejected")
		return
	}
	handler.publishPageVerificationEvent(current.Workspace.ID, page)
	writeData(c, http.StatusOK, nil)
}

func (handler *Handler) markPageObsolete(c *gin.Context) {
	var request pageVerificationRequest
	if !decode(c, &request) || !handler.requireFeature(c, verificationFeature) {
		return
	}
	page, current, ok := handler.pageForPermission(c, request.PageID)
	if !ok || !handler.requirePageAccess(c, page, true) {
		return
	}
	if err := handler.repository.MarkPageVerificationObsolete(c.Request.Context(), page.ID, current.Workspace.ID); err != nil {
		writeError(c, http.StatusBadRequest, "Page cannot be marked obsolete")
		return
	}
	handler.publishPageVerificationEvent(current.Workspace.ID, page)
	writeData(c, http.StatusOK, nil)
}

func (handler *Handler) pageVerificationPermissions(c *gin.Context, page domain.Page, info domain.PageVerificationInfo) (gin.H, error) {
	current := currentPrincipal(c)
	canManage := isAdmin(current.User)
	canAccess := isAdmin(current.User)
	if !isAdmin(current.User) {
		access, err := handler.repository.PageAccess(c.Request.Context(), page.ID, current.Workspace.ID, current.User.ID)
		if err != nil {
			return nil, err
		}
		role, err := handler.repository.SpaceRole(c.Request.Context(), page.SpaceID, current.Workspace.ID, current.User.ID)
		if err != nil {
			return nil, err
		}
		canAccess = access.CanAccess && role != ""
		canManage = canAccess && access.CanEdit && (role == "writer" || role == "admin")
	}
	canVerify := canAccess && isAdmin(current.User)
	if !canVerify && canAccess && info.Status != "none" {
		canVerify, _ = handler.repository.PageVerificationCanVerify(c.Request.Context(), page.ID, current.Workspace.ID, current.User.ID)
	}
	return gin.H{
		"canVerify":            canVerify,
		"canManage":            canManage,
		"canSubmitForApproval": canManage && info.Type == "qms" && (info.Status == "draft" || info.Status == "approved"),
		"canMarkObsolete":      canManage && info.Type == "qms" && info.Status == "approved",
	}, nil
}

func (handler *Handler) validateVerifierIDs(c *gin.Context, workspaceID string, userIDs []string) error {
	seen := make(map[string]struct{}, len(userIDs))
	for _, userID := range userIDs {
		if userID == "" {
			return errors.New("empty verifier")
		}
		if _, ok := seen[userID]; ok {
			return errors.New("duplicate verifier")
		}
		seen[userID] = struct{}{}
		if _, err := handler.repository.UserByID(c.Request.Context(), userID, workspaceID); err != nil {
			return err
		}
	}
	return nil
}

func verificationConfig(request pageVerificationRequest, typ string) (*string, *int, *string, *time.Time, bool) {
	if typ == "qms" {
		return nil, nil, nil, nil, request.Mode == ""
	}
	if typ != "expiring" {
		return nil, nil, nil, nil, false
	}
	mode := request.Mode
	if mode == "" {
		mode = "period"
	}
	if mode != "period" && mode != "fixed" && mode != "indefinite" {
		return nil, nil, nil, nil, false
	}
	modeValue := mode
	if mode == "indefinite" {
		return &modeValue, nil, nil, nil, true
	}
	if mode == "period" {
		if request.PeriodAmount == nil || *request.PeriodAmount < 1 || request.PeriodUnit == "" {
			return nil, nil, nil, nil, false
		}
		if !validPeriodUnit(request.PeriodUnit) {
			return nil, nil, nil, nil, false
		}
		return &modeValue, request.PeriodAmount, nonEmptyString(request.PeriodUnit), verificationExpiry(time.Now().UTC(), request.PeriodAmount, request.PeriodUnit), true
	}
	if request.FixedExpiresAt == "" {
		return nil, nil, nil, nil, false
	}
	expiresAt, err := time.Parse(time.RFC3339, request.FixedExpiresAt)
	if err != nil || !expiresAt.After(time.Now().UTC()) {
		return nil, nil, nil, nil, false
	}
	return &modeValue, nil, nil, &expiresAt, true
}

func validPeriodUnit(unit string) bool {
	return unit == "day" || unit == "week" || unit == "month" || unit == "year"
}

func verificationExpiry(now time.Time, amount *int, unit string) *time.Time {
	days := *amount
	switch unit {
	case "week":
		days *= 7
	case "month":
		days *= 30
	case "year":
		days *= 365
	}
	expiresAt := now.AddDate(0, 0, days)
	return &expiresAt
}

func nonEmptyString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func (handler *Handler) recordPageVerificationAudit(c *gin.Context, current principal, page domain.Page, event string) {
	actorID := current.User.ID
	resourceID := page.ID
	spaceID := page.SpaceID
	_ = handler.repository.RecordAudit(c.Request.Context(), postgres.AuditInput{
		WorkspaceID: current.Workspace.ID, ActorID: &actorID, Event: event,
		ResourceType: "page", ResourceID: &resourceID, SpaceID: &spaceID,
	})
}

func (handler *Handler) publishPageVerificationEvent(workspaceID string, page domain.Page) {
	if handler.realtime != nil {
		handler.realtime.PublishSpaceEvent(workspaceID, page.SpaceID, gin.H{
			"operation": "pageVerificationUpdated", "pageId": page.ID,
		})
	}
}
