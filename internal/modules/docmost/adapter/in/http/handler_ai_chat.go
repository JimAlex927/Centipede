package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"path/filepath"
	"strings"

	"centipede/internal/modules/docmost/adapter/out/postgres"
	"centipede/internal/modules/docmost/application"

	"github.com/gin-gonic/gin"
)

const aiFeature = "ai"

type aiChatRequest struct {
	ChatID           string   `json:"chatId"`
	Content          string   `json:"content"`
	Title            string   `json:"title"`
	Query            string   `json:"query"`
	Cursor           string   `json:"cursor"`
	Limit            int      `json:"limit"`
	MentionedPageIDs []string `json:"mentionedPageIds"`
	ContextPageID    string   `json:"contextPageId"`
	AttachmentIDs    []string `json:"attachmentIds"`
}

func (handler *Handler) requireAI(c *gin.Context) bool {
	return handler.requireFeature(c, aiFeature)
}

func (handler *Handler) createAIChat(c *gin.Context) {
	if !handler.requireAI(c) {
		return
	}
	current := currentPrincipal(c)
	chat, err := handler.repository.CreateAIChat(c.Request.Context(), current.Workspace.ID, current.User.ID)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to create AI chat")
		return
	}
	writeData(c, http.StatusOK, chat)
}

func (handler *Handler) aiChats(c *gin.Context) {
	if !handler.requireAI(c) {
		return
	}
	var request aiChatRequest
	if !decodeOptional(c, &request) {
		return
	}
	current := currentPrincipal(c)
	result, err := handler.repository.AIChats(c.Request.Context(), current.Workspace.ID, current.User.ID, request.Cursor, request.Limit)
	if errors.Is(err, postgres.ErrInvalidInput) {
		writeError(c, http.StatusBadRequest, "Invalid pagination cursor")
		return
	}
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to load AI chats")
		return
	}
	writeData(c, http.StatusOK, result)
}

func (handler *Handler) aiChatInfo(c *gin.Context) {
	if !handler.requireAI(c) {
		return
	}
	var request aiChatRequest
	if !decode(c, &request) || strings.TrimSpace(request.ChatID) == "" {
		return
	}
	current := currentPrincipal(c)
	chat, err := handler.repository.AIChatByID(c.Request.Context(), request.ChatID, current.Workspace.ID, current.User.ID)
	if errors.Is(err, postgres.ErrNotFound) {
		writeError(c, http.StatusNotFound, "AI chat not found")
		return
	}
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to load AI chat")
		return
	}
	messages, err := handler.repository.AIChatMessages(c.Request.Context(), chat.ID, current.Workspace.ID)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to load AI chat messages")
		return
	}
	writeData(c, http.StatusOK, gin.H{"chat": chat, "messages": messages})
}

func (handler *Handler) updateAIChat(c *gin.Context) {
	if !handler.requireAI(c) {
		return
	}
	var request aiChatRequest
	if !decode(c, &request) || strings.TrimSpace(request.ChatID) == "" {
		return
	}
	if len([]rune(request.Title)) > 160 {
		writeError(c, http.StatusBadRequest, "Chat title is too long")
		return
	}
	current := currentPrincipal(c)
	chat, err := handler.repository.UpdateAIChat(c.Request.Context(), request.ChatID, current.Workspace.ID, current.User.ID, request.Title)
	if errors.Is(err, postgres.ErrNotFound) {
		writeError(c, http.StatusNotFound, "AI chat not found")
		return
	}
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to update AI chat")
		return
	}
	writeData(c, http.StatusOK, chat)
}

func (handler *Handler) deleteAIChat(c *gin.Context) {
	if !handler.requireAI(c) {
		return
	}
	var request aiChatRequest
	if !decode(c, &request) || strings.TrimSpace(request.ChatID) == "" {
		return
	}
	current := currentPrincipal(c)
	attachments, attachmentErr := handler.repository.AIChatAttachments(c.Request.Context(), request.ChatID, current.Workspace.ID, current.User.ID)
	if attachmentErr != nil && !errors.Is(attachmentErr, postgres.ErrNotFound) {
		writeError(c, http.StatusInternalServerError, "Failed to load chat attachments")
		return
	}
	if err := handler.repository.DeleteAIChat(c.Request.Context(), request.ChatID, current.Workspace.ID, current.User.ID); err != nil {
		if errors.Is(err, postgres.ErrNotFound) {
			writeError(c, http.StatusNotFound, "AI chat not found")
		} else {
			writeError(c, http.StatusInternalServerError, "Failed to delete AI chat")
		}
		return
	}
	for _, attachment := range attachments {
		_ = handler.repository.DeleteAttachment(c.Request.Context(), attachment.ID, current.Workspace.ID)
		if handler.storage != nil {
			_ = handler.storage.Delete(c.Request.Context(), attachment.FilePath)
		}
	}
	writeData(c, http.StatusOK, nil)
}

func (handler *Handler) searchAIChats(c *gin.Context) {
	if !handler.requireAI(c) {
		return
	}
	var request aiChatRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	items, err := handler.repository.SearchAIChats(c.Request.Context(), current.Workspace.ID, current.User.ID, request.Query)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to search AI chats")
		return
	}
	writeData(c, http.StatusOK, items)
}

func (handler *Handler) uploadAIChatFile(c *gin.Context) {
	if !handler.requireAI(c) {
		return
	}
	if handler.storage == nil {
		writeError(c, http.StatusServiceUnavailable, "File storage is unavailable")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, handler.maxUpload+1024*1024)
	header, err := c.FormFile("file")
	if err != nil {
		writeError(c, http.StatusBadRequest, "Failed to upload chat file")
		return
	}
	current := currentPrincipal(c)
	chatID := strings.TrimSpace(c.PostForm("chatId"))
	var chatPtr *string
	if chatID != "" {
		if _, err = handler.repository.AIChatByID(c.Request.Context(), chatID, current.Workspace.ID, current.User.ID); err != nil {
			writeError(c, http.StatusNotFound, "AI chat not found")
			return
		}
		chatPtr = &chatID
	}
	file, err := header.Open()
	if err != nil {
		writeError(c, http.StatusBadRequest, "Failed to open chat file")
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
		writeError(c, http.StatusBadRequest, "Failed to inspect chat file")
		return
	}
	attachmentID, err := randomUUID()
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to allocate chat attachment")
		return
	}
	relativePath := path.Join("chat", current.Workspace.ID, attachmentID, fileName)
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		writeError(c, http.StatusBadRequest, "Failed to read chat file")
		return
	}
	size, err := handler.storage.Save(c.Request.Context(), relativePath, file, handler.maxUpload)
	if err != nil {
		if errors.Is(err, application.ErrUploadTooLarge) {
			writeError(c, http.StatusRequestEntityTooLarge, "File exceeds the configured upload limit")
		} else {
			writeError(c, http.StatusInternalServerError, "Failed to store chat file")
		}
		return
	}
	attachment, err := handler.repository.CreateAIChatAttachment(c.Request.Context(), postgres.AttachmentInput{
		ID: attachmentID, FileName: fileName, FilePath: relativePath, FileSize: size,
		FileExt: extension, MimeType: mimeType, CreatorID: current.User.ID, WorkspaceID: current.Workspace.ID,
	}, chatPtr)
	if err != nil {
		_ = handler.storage.Delete(c.Request.Context(), relativePath)
		writeError(c, http.StatusInternalServerError, "Failed to save chat attachment metadata")
		return
	}
	mimeValue := ""
	if attachment.MimeType != nil {
		mimeValue = *attachment.MimeType
	}
	writeData(c, http.StatusOK, gin.H{
		"id": attachment.ID, "fileName": attachment.FileName, "fileExt": attachment.FileExt,
		"fileSize": attachment.FileSize, "mimeType": mimeValue,
	})
}

// sendAIChatMessage persists the user turn and exposes the SSE boundary used
// by the enterprise client. When no provider is configured the endpoint fails
// explicitly rather than fabricating an assistant answer.
func (handler *Handler) sendAIChatMessage(c *gin.Context) {
	if !handler.requireAI(c) {
		return
	}
	if handler.aiProvider == nil || !handler.aiProvider.Configured() {
		writeError(c, http.StatusServiceUnavailable, "AI provider is not configured")
		return
	}
	var request aiChatRequest
	if !decode(c, &request) {
		return
	}
	if strings.TrimSpace(request.Content) == "" && len(request.AttachmentIDs) == 0 {
		writeError(c, http.StatusBadRequest, "Chat content or an attachment is required")
		return
	}
	current := currentPrincipal(c)
	chatID := strings.TrimSpace(request.ChatID)
	created := false
	var chat postgres.AIChat
	var err error
	if chatID == "" {
		chat, err = handler.repository.CreateAIChat(c.Request.Context(), current.Workspace.ID, current.User.ID)
		created = true
	} else {
		chat, err = handler.repository.AIChatByID(c.Request.Context(), chatID, current.Workspace.ID, current.User.ID)
	}
	if err != nil {
		if errors.Is(err, postgres.ErrNotFound) {
			writeError(c, http.StatusNotFound, "AI chat not found")
		} else {
			writeError(c, http.StatusInternalServerError, "Failed to create AI chat")
		}
		return
	}
	previousMessages, err := handler.repository.AIChatMessages(c.Request.Context(), chat.ID, current.Workspace.ID)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to load AI chat messages")
		return
	}
	metadata := map[string]any{}
	if len(request.MentionedPageIDs) > 0 {
		metadata["mentionedPageIds"] = request.MentionedPageIDs
	}
	if request.ContextPageID != "" {
		metadata["contextPageId"] = request.ContextPageID
	}
	if len(request.AttachmentIDs) > 0 {
		metadata["attachmentIds"] = request.AttachmentIDs
		if err := handler.repository.ClaimAIChatAttachments(c.Request.Context(), request.AttachmentIDs, chat.ID, current.User.ID, current.Workspace.ID); err != nil {
			writeError(c, http.StatusInternalServerError, "Failed to attach chat files")
			return
		}
	}
	metadataJSON, _ := json.Marshal(metadata)
	if _, err := handler.repository.CreateAIChatMessage(c.Request.Context(), chat.ID, current.Workspace.ID, current.User.ID, "user", request.Content, nil, metadataJSON); err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to save chat message")
		return
	}
	if chat.Title == nil && strings.TrimSpace(request.Content) != "" {
		_, _ = handler.repository.UpdateAIChat(c.Request.Context(), chat.ID, current.Workspace.ID, current.User.ID, truncateAIText(request.Content, 80))
	}

	providerMessages := []application.AIMessage{{Role: "system", Content: "You are a helpful assistant for a Docmost workspace. Answer clearly and concisely. Do not claim to have accessed information that is not included in the conversation."}}
	for _, message := range previousMessages {
		if message.Content == nil || (message.Role != "user" && message.Role != "assistant") {
			continue
		}
		providerMessages = append(providerMessages, application.AIMessage{Role: message.Role, Content: *message.Content})
	}
	content := strings.TrimSpace(request.Content)
	if content == "" {
		content = "Please process the attached file(s)."
	}
	providerMessages = append(providerMessages, application.AIMessage{Role: "user", Content: content})

	if created {
		writeAIStreamEvent(c, gin.H{"type": "chat_created", "chatId": chat.ID})
	}
	completion, err := handler.aiProvider.Complete(c.Request.Context(), providerMessages)
	if err != nil {
		writeAIStreamError(c, err.Error(), "provider_error", true)
		return
	}
	usage := gin.H{"promptTokens": completion.PromptTokens, "completionTokens": completion.CompletionTokens, "totalTokens": completion.TotalTokens}
	assistantMetadata, _ := json.Marshal(map[string]any{"tokenUsage": usage})
	assistant, err := handler.repository.CreateAIChatMessage(c.Request.Context(), chat.ID, current.Workspace.ID, current.User.ID, "assistant", completion.Content, nil, assistantMetadata)
	if err != nil {
		writeAIStreamError(c, "Failed to save assistant message", "persistence_error", true)
		return
	}
	writeAIStreamEvent(c, gin.H{"type": "content", "text": completion.Content})
	writeAIStreamEvent(c, gin.H{"type": "done", "messageId": assistant.ID, "usage": usage})
	writeAIStreamDone(c)
}

type aiGenerateRequest struct {
	Action  string `json:"action"`
	Content string `json:"content"`
	Prompt  string `json:"prompt"`
}

func (handler *Handler) aiGenerate(c *gin.Context) {
	if !handler.requireAI(c) {
		return
	}
	var request aiGenerateRequest
	if !decode(c, &request) {
		return
	}
	if strings.TrimSpace(request.Content) == "" {
		writeError(c, http.StatusBadRequest, "Content is required")
		return
	}
	completion, err := handler.completeGeneratedContent(c, request)
	if err != nil {
		handler.writeAIProviderError(c, err)
		return
	}
	writeData(c, http.StatusOK, completion)
}

func (handler *Handler) aiGenerateStream(c *gin.Context) {
	if !handler.requireAI(c) {
		return
	}
	var request aiGenerateRequest
	if !decode(c, &request) {
		return
	}
	if strings.TrimSpace(request.Content) == "" {
		writeError(c, http.StatusBadRequest, "Content is required")
		return
	}
	if handler.aiProvider == nil || !handler.aiProvider.Configured() {
		writeError(c, http.StatusServiceUnavailable, "AI provider is not configured")
		return
	}
	completion, err := handler.completeGeneratedContent(c, request)
	if err != nil {
		handler.writeAIStreamProviderError(c, err)
		return
	}
	writeAIStreamEvent(c, gin.H{"content": completion.Content})
	writeAIStreamDone(c)
}

func (handler *Handler) completeGeneratedContent(c *gin.Context, request aiGenerateRequest) (application.AICompletion, error) {
	instruction := aiActionInstruction(request.Action)
	if strings.TrimSpace(request.Prompt) != "" {
		instruction += "\nAdditional instruction: " + strings.TrimSpace(request.Prompt)
	}
	return handler.aiProvider.Complete(c.Request.Context(), []application.AIMessage{
		{Role: "system", Content: instruction},
		{Role: "user", Content: request.Content},
	})
}

func aiActionInstruction(action string) string {
	base := "You are a writing assistant. Return only the requested result, without explaining the instruction."
	if action == "" {
		return base
	}
	return base + " Requested action: " + strings.ReplaceAll(action, "_", " ") + "."
}

func (handler *Handler) writeAIProviderError(c *gin.Context, err error) {
	if errors.Is(err, application.ErrAIProviderNotConfigured) {
		writeError(c, http.StatusServiceUnavailable, "AI provider is not configured")
		return
	}
	writeError(c, http.StatusBadGateway, "AI provider request failed")
}

func (handler *Handler) writeAIStreamProviderError(c *gin.Context, err error) {
	if errors.Is(err, application.ErrAIProviderNotConfigured) {
		writeError(c, http.StatusServiceUnavailable, "AI provider is not configured")
		return
	}
	writeAIStreamError(c, "AI provider request failed", "provider_error", true)
}

type aiAnswerRequest struct {
	Query   string  `json:"query"`
	SpaceID *string `json:"spaceId"`
	Limit   int     `json:"limit"`
}

func (handler *Handler) aiAnswers(c *gin.Context) {
	if !handler.requireAI(c) {
		return
	}
	var request aiAnswerRequest
	if !decode(c, &request) {
		return
	}
	if strings.TrimSpace(request.Query) == "" {
		writeError(c, http.StatusBadRequest, "Query is required")
		return
	}
	if handler.aiProvider == nil || !handler.aiProvider.Configured() {
		writeError(c, http.StatusServiceUnavailable, "AI provider is not configured")
		return
	}
	current := currentPrincipal(c)
	pages, err := handler.repository.SearchPages(c.Request.Context(), current.Workspace.ID, request.Query, request.SpaceID, request.Limit, current.User.ID, isAdmin(current.User))
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to search workspace knowledge")
		return
	}
	contextText := strings.Builder{}
	sources := make([]gin.H, 0, len(pages))
	for _, page := range pages {
		title := ""
		if page.Title != nil {
			title = *page.Title
		}
		excerpt := page.Highlight
		contextText.WriteString("Title: " + title + "\nExcerpt: " + excerpt + "\n\n")
		similarity := float64(page.Rank)
		sources = append(sources, gin.H{"pageId": page.ID, "title": title, "slugId": page.SlugID, "spaceSlug": page.Space.Slug, "similarity": similarity, "distance": 1 - similarity, "chunkIndex": 0, "excerpt": excerpt})
	}
	completion, err := handler.aiProvider.Complete(c.Request.Context(), []application.AIMessage{
		{Role: "system", Content: "Answer the user's question using only the supplied Docmost workspace excerpts. If the excerpts do not contain the answer, say so."},
		{Role: "user", Content: fmt.Sprintf("Question: %s\n\nWorkspace excerpts:\n%s", request.Query, contextText.String())},
	})
	if err != nil {
		handler.writeAIStreamProviderError(c, err)
		return
	}
	writeAIStreamEvent(c, gin.H{"content": completion.Content})
	writeAIStreamEvent(c, gin.H{"sources": sources})
	writeAIStreamDone(c)
}

func (handler *Handler) vectorCacheHint(c *gin.Context) {
	if !handler.requireAI(c) {
		return
	}
	// The Go service currently uses the database search index directly. Keep
	// the endpoint idempotent so the frontend can issue its best-effort hint.
	writeData(c, http.StatusOK, nil)
}

func writeAIStreamEvent(c *gin.Context, event any) {
	data, err := json.Marshal(event)
	if err != nil {
		return
	}
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", data)
	if flusher, ok := c.Writer.(http.Flusher); ok {
		flusher.Flush()
	}
}

func writeAIStreamError(c *gin.Context, message, code string, retryable bool) {
	writeAIStreamEvent(c, gin.H{"type": "error", "message": message, "code": code, "retryable": retryable})
	writeAIStreamDone(c)
}

func writeAIStreamDone(c *gin.Context) {
	c.Header("Content-Type", "text/event-stream")
	_, _ = fmt.Fprint(c.Writer, "data: [DONE]\n\n")
	if flusher, ok := c.Writer.(http.Flusher); ok {
		flusher.Flush()
	}
}

func truncateAIText(value string, limit int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
