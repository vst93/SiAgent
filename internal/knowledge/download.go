package knowledge

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
)

// Download opens the currently published original file for a document. Only
// active, non-deleted versions are downloadable.
func (c *Core) Download(ctx context.Context, id string) (*os.File, Document, error) {
	document, err := c.Document(ctx, id)
	if err != nil {
		return nil, Document{}, err
	}
	if document.Deleted || document.ActiveVersion < 1 {
		return nil, Document{}, missing()
	}
	file, err := os.Open(filepath.Join(c.root, id, strconv.Itoa(document.ActiveVersion), "original"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, Document{}, missing()
		}
		return nil, Document{}, err
	}
	return file, document, nil
}
