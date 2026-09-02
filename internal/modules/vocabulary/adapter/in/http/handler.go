package http

import (
	"errors"
	"net/http"
	"strconv"

	"centipede/internal/modules/vocabulary/application"
	"centipede/internal/modules/vocabulary/domain"

	"github.com/gin-gonic/gin"
)

type Handler struct{ service *application.Service }

func NewHandler(service *application.Service) *Handler { return &Handler{service: service} }

type createRequest struct {
	LanguageTag  string           `json:"language"`
	OriginalText string           `json:"text"`
	Lemma        string           `json:"lemma"`
	Definition   string           `json:"definition"`
	Notes        string           `json:"notes"`
	Status       string           `json:"status"`
	Source       domain.Source    `json:"source"`
	Contexts     []contextRequest `json:"contexts"`
}

type contextRequest struct {
	Text     string `json:"text"`
	Location string `json:"location"`
}

type statusRequest struct {
	Status string `json:"status"`
}

func (handler *Handler) Register(router gin.IRouter, authMiddleware gin.HandlerFunc) {
	group := router.Group("/api/v1/vocabulary", authMiddleware)
	group.POST("/entries", handler.create)
	group.GET("/entries", handler.list)
	group.GET("/entries/:id", handler.find)
	group.PATCH("/entries/:id/status", handler.updateStatus)
}

func (handler *Handler) create(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		return
	}
	var request createRequest
	if !decodeJSON(c, &request) {
		return
	}
	entry := domain.Entry{UserID: userID, LanguageTag: request.LanguageTag, OriginalText: request.OriginalText, Lemma: request.Lemma, Definition: request.Definition, Notes: request.Notes, Status: request.Status, Source: request.Source}
	entry.Contexts = make([]domain.Context, 0, len(request.Contexts))
	for _, item := range request.Contexts {
		entry.Contexts = append(entry.Contexts, domain.Context{Text: item.Text, Location: item.Location})
	}
	created, err := handler.service.Create(c.Request.Context(), entry)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, toResponse(created))
}

func (handler *Handler) list(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	entries, err := handler.service.List(c.Request.Context(), application.Filter{UserID: userID, Language: c.Query("language"), Status: c.Query("status"), Query: c.Query("q"), Limit: limit, Offset: offset})
	if err != nil {
		writeServiceError(c, err)
		return
	}
	items := make([]entryResponse, 0, len(entries))
	for _, entry := range entries {
		items = append(items, toResponse(entry))
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "limit": limit, "offset": offset})
}

func (handler *Handler) find(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		return
	}
	entryID, ok := parseID(c.Param("id"))
	if !ok {
		writeError(c, http.StatusBadRequest, "invalid_id", "entry id is invalid")
		return
	}
	entry, err := handler.service.FindByID(c.Request.Context(), userID, entryID)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, toResponse(entry))
}

func (handler *Handler) updateStatus(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		return
	}
	entryID, ok := parseID(c.Param("id"))
	if !ok {
		writeError(c, http.StatusBadRequest, "invalid_id", "entry id is invalid")
		return
	}
	var request statusRequest
	if !decodeJSON(c, &request) {
		return
	}
	entry, err := handler.service.UpdateStatus(c.Request.Context(), userID, entryID, request.Status)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, toResponse(entry))
}

type entryResponse struct {
	ID             int64            `json:"id"`
	Language       string           `json:"language"`
	Text           string           `json:"text"`
	NormalizedText string           `json:"normalized_text"`
	Lemma          string           `json:"lemma,omitempty"`
	Definition     string           `json:"definition"`
	Notes          string           `json:"notes"`
	Status         string           `json:"status"`
	Source         domain.Source    `json:"source"`
	Contexts       []domain.Context `json:"contexts"`
	CreatedAt      string           `json:"created_at"`
	UpdatedAt      string           `json:"updated_at"`
}

func toResponse(entry domain.Entry) entryResponse {
	return entryResponse{ID: entry.ID, Language: entry.LanguageTag, Text: entry.OriginalText, NormalizedText: entry.NormalizedText, Lemma: entry.Lemma, Definition: entry.Definition, Notes: entry.Notes, Status: entry.Status, Source: entry.Source, Contexts: entry.Contexts, CreatedAt: entry.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"), UpdatedAt: entry.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z")}
}

func currentUserID(c *gin.Context) (int64, bool) {
	value, exists := c.Get("user_id")
	id, ok := value.(int64)
	if !exists || !ok || id <= 0 {
		writeError(c, http.StatusUnauthorized, "unauthorized", "authentication is required")
		return 0, false
	}
	return id, true
}

func parseID(raw string) (int64, bool) {
	id, err := strconv.ParseInt(raw, 10, 64)
	return id, err == nil && id > 0
}

func decodeJSON(c *gin.Context, destination any) bool {
	if err := c.ShouldBindJSON(destination); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_json", "request body is invalid")
		return false
	}
	return true
}

func writeServiceError(c *gin.Context, err error) {
	if errors.Is(err, domain.ErrInvalidEntry) {
		writeError(c, http.StatusBadRequest, "invalid_entry", "vocabulary entry is invalid")
		return
	}
	if errors.Is(err, domain.ErrNotFound) {
		writeError(c, http.StatusNotFound, "not_found", "vocabulary entry was not found")
		return
	}
	writeError(c, http.StatusInternalServerError, "vocabulary_failed", "vocabulary request failed")
}

func writeError(c *gin.Context, status int, code, message string) {
	c.JSON(status, gin.H{"error": gin.H{"code": code, "message": message}})
}
