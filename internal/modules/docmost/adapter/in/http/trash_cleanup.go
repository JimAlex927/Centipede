package http

import (
	"context"
	"time"
)

const trashCleanupInterval = 24 * time.Hour

// startTrashCleanup replaces the upstream daily trash-cleanup queue. The
// worker is deliberately best-effort: a later tick can retry a page if a
// transient database or storage error occurs.
func (handler *Handler) startTrashCleanup() {
	go func() {
		ticker := time.NewTicker(trashCleanupInterval)
		defer ticker.Stop()
		for range ticker.C {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			handler.cleanupExpiredTrash(ctx)
			cancel()
		}
	}()
}

func (handler *Handler) cleanupExpiredTrash(ctx context.Context) {
	if handler.repository == nil {
		return
	}
	for {
		pageIDs, err := handler.repository.ExpiredTrashPageIDs(ctx, 100)
		if err != nil {
			return
		}
		if len(pageIDs) == 0 {
			return
		}
		progress := false
		for _, pageID := range pageIDs {
			workspaceID, err := handler.repository.PageWorkspaceID(ctx, pageID)
			if err != nil {
				continue
			}
			paths, err := handler.repository.PageTreeAttachmentPaths(ctx, pageID, workspaceID)
			if err != nil {
				continue
			}
			if handler.storage != nil {
				storageFailed := false
				for _, path := range paths {
					if err := handler.storage.Delete(ctx, path); err != nil {
						storageFailed = true
					}
				}
				if storageFailed {
					continue
				}
			}
			if err := handler.repository.DeletePage(ctx, pageID, workspaceID, "", true); err != nil {
				continue
			}
			progress = true
		}
		if !progress {
			return
		}
	}
}
