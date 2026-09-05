package http

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"centipede/internal/modules/docmost/domain"
)

const maxAttachmentIndexBytes int64 = 30 * 1024 * 1024

// scheduleAttachmentIndex mirrors the upstream attachment queue at the
// service boundary. Indexing is best effort and never delays an upload.
func (handler *Handler) scheduleAttachmentIndex(attachment domain.Attachment) {
	if handler.storage == nil || attachment.Type == nil || *attachment.Type != "file" {
		return
	}
	go func() {
		if !handler.workspaceHasFeature(context.Background(), attachment.WorkspaceID, "attachment:indexing") {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		file, err := handler.storage.Open(ctx, attachment.FilePath)
		if err != nil {
			return
		}
		defer file.Close()
		textContent, err := extractAttachmentText(file, attachment.FileExt)
		if err != nil || strings.TrimSpace(textContent) == "" {
			return
		}
		_ = handler.repository.UpdateAttachmentTextContent(ctx, attachment.ID, attachment.WorkspaceID, textContent)
	}()
}

func extractAttachmentText(reader io.Reader, extension string) (string, error) {
	textExtension := strings.ToLower(extension)
	if textExtension == ".pdf" {
		return extractAttachmentPDFText(reader)
	}
	if textExtension == ".docx" {
		nodes, err := parseDocx(reader)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(importNodesText(nodes)), nil
	}
	if !isTextAttachmentExtension(textExtension) {
		return "", nil
	}
	data, err := io.ReadAll(io.LimitReader(reader, maxAttachmentIndexBytes+1))
	if err != nil {
		return "", err
	}
	if int64(len(data)) > maxAttachmentIndexBytes {
		return "", errors.New("attachment text is too large")
	}
	return strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(string(data), "\r\n", "\n"), "\r", "\n")), nil
}

func extractAttachmentPDFText(reader io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxAttachmentIndexBytes+1))
	if err != nil {
		return "", err
	}
	if int64(len(data)) > maxAttachmentIndexBytes {
		return "", errors.New("attachment PDF is too large")
	}
	temporary, err := os.CreateTemp("", "centipede-attachment-*.pdf")
	if err != nil {
		return "", err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if _, err = temporary.Write(data); err != nil {
		_ = temporary.Close()
		return "", err
	}
	if err = temporary.Close(); err != nil {
		return "", err
	}
	return extractPDFText(temporaryName)
}

func isTextAttachmentExtension(extension string) bool {
	switch extension {
	case ".txt", ".md", ".csv", ".html", ".htm", ".json", ".xml", ".yaml", ".yml", ".log", ".drawio":
		return true
	default:
		return false
	}
}

func importNodesText(nodes []importNode) string {
	var builder strings.Builder
	var visit func([]importNode)
	visit = func(values []importNode) {
		for _, node := range values {
			if node.Text != "" {
				builder.WriteString(node.Text)
			}
			if len(node.Content) > 0 {
				visit(node.Content)
			}
			if node.Type != "text" {
				builder.WriteByte('\n')
			}
		}
	}
	visit(nodes)
	return builder.String()
}
