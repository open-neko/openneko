package webfront

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// MoveLibraryDocuments moves the Library documents that still sit in the org
// workspace into each owner's folder in openneko serve. Thread attachments
// keep their thread copy. It stops at the first serve error, so a later run
// takes the rest.
func (a *API) MoveLibraryDocuments(ctx context.Context) (moved, missing int, err error) {
	rows, err := a.DB.Query(ctx, `select org_id, id::text, coalesce(user_id, ''), filename, relative_path
		from library_document where relative_path not like 'files/%' and status = 'ready'`)
	if err != nil {
		return 0, 0, err
	}
	type document struct{ orgID, id, userID, filename, path string }
	var documents []document
	for rows.Next() {
		var d document
		if err := rows.Scan(&d.orgID, &d.id, &d.userID, &d.filename, &d.path); err != nil {
			rows.Close()
			return 0, 0, err
		}
		documents = append(documents, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	for _, d := range documents {
		if d.userID == "" {
			missing++
			if err := a.markLibraryDocumentFailed(ctx, d.orgID, d.id, "This document has no owner. Remove it and upload it again."); err != nil {
				return moved, missing, err
			}
			continue
		}
		original, err := libraryUploadPath(d.orgID, d.path)
		var data []byte
		if err == nil {
			data, err = os.ReadFile(original)
		}
		if err != nil {
			missing++
			if err := a.markLibraryDocumentFailed(ctx, d.orgID, d.id, "The original file is missing. Remove this document and upload it again."); err != nil {
				return moved, missing, err
			}
			continue
		}
		_, path := libraryPath(d.filename, data)
		if err := a.putServeFile(ctx, actor{OrgID: d.orgID, UserID: d.userID}, path, data, ""); err != nil {
			return moved, missing, err
		}
		if _, err := a.DB.Exec(ctx, `update library_document set relative_path = $3, status = 'ready', error = null, updated_at = now()
			where org_id = $1 and id = $2::uuid`, d.orgID, d.id, "files/"+path); err != nil {
			return moved, missing, err
		}
		if strings.HasPrefix(d.path, "library/uploads/") {
			_ = os.Remove(original)
		}
		moved++
	}
	return moved, missing, nil
}

func (a *API) markLibraryDocumentFailed(ctx context.Context, orgID, id, message string) error {
	_, err := a.DB.Exec(ctx, `update library_document set status = 'failed', error = $3, updated_at = now()
		where org_id = $1 and id = $2::uuid`, orgID, id, message)
	return err
}

func libraryUploadPath(orgID, relativePath string) (string, error) {
	root := filepath.Clean(orgAgentRoot(orgID))
	clean := strings.TrimLeft(strings.ReplaceAll(relativePath, `\`, "/"), "/")
	path := filepath.Join(root, filepath.FromSlash(clean))
	if !strings.HasPrefix(path, root+string(filepath.Separator)) {
		return "", errors.New("library upload path leaves the org workspace")
	}
	return path, nil
}
