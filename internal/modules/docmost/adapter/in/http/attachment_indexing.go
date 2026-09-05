package http

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"centipede/internal/modules/docmost/adapter/out/postgres"
	"centipede/internal/modules/docmost/domain"
)

const maxAttachmentIndexBytes int64 = 30 * 1024 * 1024

// scheduleAttachmentIndex mirrors the upstream attachment queue at the
// service boundary. The queue record is persisted before the worker starts,
// so a process restart cannot lose an indexing job. Enqueuing remains
// asynchronous and never delays an upload response.
func (handler *Handler) scheduleAttachmentIndex(attachment domain.Attachment) {
	if handler.storage == nil || attachment.Type == nil || *attachment.Type != "file" || attachment.SpaceID == nil || !isIndexableAttachmentExtension(attachment.FileExt) {
		return
	}
	go func() {
		if !handler.workspaceHasFeature(context.Background(), attachment.WorkspaceID, "attachment:indexing") {
			return
		}
		taskID, err := importTaskUUID()
		if err != nil {
			return
		}
		task, err := handler.repository.CreateFileTask(context.Background(), postgres.FileTaskInput{
			ID: taskID, Type: "attachment-index", Source: attachment.ID,
			FileName: attachment.FileName, FilePath: attachment.FilePath, FileSize: attachment.FileSize,
			FileExt: attachment.FileExt, CreatorID: attachment.CreatorID, SpaceID: *attachment.SpaceID,
			WorkspaceID: attachment.WorkspaceID, Status: "processing",
		})
		if err != nil {
			return
		}
		handler.runAttachmentIndex(task)
	}()
}

// ResumePendingAttachmentIndexes is called during handler initialization and
// picks up jobs that were persisted before an earlier process stopped.
func (handler *Handler) ResumePendingAttachmentIndexes(ctx context.Context) {
	if handler.storage == nil || handler.repository == nil {
		return
	}
	tasks, err := handler.repository.ProcessingAttachmentIndexTasks(ctx, 100)
	if err != nil {
		return
	}
	for _, task := range tasks {
		go handler.runAttachmentIndex(task)
	}
}

func (handler *Handler) runAttachmentIndex(task domain.FileTask) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	attachmentID := ""
	if task.Source != nil {
		attachmentID = strings.TrimSpace(*task.Source)
	}
	attachment, err := handler.repository.AttachmentByID(ctx, attachmentID, task.WorkspaceID)
	if err != nil {
		_ = handler.repository.UpdateFileTaskStatus(context.Background(), task.ID, task.WorkspaceID, "failed", "Attachment is no longer available")
		return
	}
	file, err := handler.storage.Open(ctx, attachment.FilePath)
	if err != nil {
		_ = handler.repository.UpdateFileTaskStatus(context.Background(), task.ID, task.WorkspaceID, "failed", "Attachment file is no longer available")
		return
	}
	textContent, extractErr := extractAttachmentText(file, attachment.FileExt)
	_ = file.Close()
	if extractErr != nil {
		_ = handler.repository.UpdateFileTaskStatus(context.Background(), task.ID, task.WorkspaceID, "failed", extractErr.Error())
		return
	}
	if err := handler.repository.UpdateAttachmentTextContent(ctx, attachment.ID, attachment.WorkspaceID, textContent); err != nil {
		_ = handler.repository.UpdateFileTaskStatus(context.Background(), task.ID, task.WorkspaceID, "failed", "Failed to save attachment index")
		return
	}
	_ = handler.repository.UpdateFileTaskStatus(context.Background(), task.ID, task.WorkspaceID, "success", "")
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

func isIndexableAttachmentExtension(extension string) bool {
	extension = strings.ToLower(strings.TrimSpace(extension))
	return extension == ".pdf" || extension == ".docx" || isTextAttachmentExtension(extension)
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
