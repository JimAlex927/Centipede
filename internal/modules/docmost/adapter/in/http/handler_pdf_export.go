package http

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type pdfRenderRequest struct {
	PageID string `json:"pageId"`
	Token  string `json:"token"`
}

// pdfRender returns the minimal page payload consumed by the standalone
// /pdf-render/:pageId React route. Node issued this as a short-lived
// pdf_render JWT, so keep the same claim names and signing key for a seamless
// cutover.
func (handler *Handler) pdfRender(c *gin.Context) {
	var request pdfRenderRequest
	if !decode(c, &request) || request.PageID == "" || request.Token == "" {
		return
	}
	claims, err := handler.tokens.parse(request.Token, "pdf_render")
	if err != nil || claims.PageID != request.PageID {
		writeError(c, http.StatusUnauthorized, "Expired or invalid PDF render token")
		return
	}

	page, err := handler.repository.PageByID(c.Request.Context(), request.PageID, request.PageID, claims.WorkspaceID, false)
	if err != nil {
		handler.writeRepositoryError(c, err, "Page not found")
		return
	}
	writeData(c, http.StatusOK, gin.H{
		"pageId":  page.ID,
		"title":   exportTitle(page.Title),
		"content": page.Content,
	})
}
