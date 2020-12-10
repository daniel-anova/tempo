package azure

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	blob "github.com/Azure/azure-storage-blob-go/azblob"
	"github.com/google/uuid"
	"github.com/grafana/tempo/tempodb/backend"
	"github.com/grafana/tempo/tempodb/backend/util"
	"github.com/grafana/tempo/tempodb/encoding"
	"github.com/pkg/errors"
)

type BlobAttributes struct {
	// Size is the blob size in bytes.
	Size int64 `json:"size"`

	// LastModified is the timestamp the blob was last modified.
	LastModified time.Time `json:"last_modified"`
}

func (rw *readerWriter) MarkBlockCompacted(blockID uuid.UUID, tenantID string) error {
	if len(tenantID) == 0 {
		return backend.ErrEmptyTenantID
	}
	if blockID == uuid.Nil {
		return backend.ErrEmptyBlockID
	}

	// move meta file to a new location
	metaFilename := util.MetaFileName(blockID, tenantID)
	compactedMetaFilename := util.CompactedMetaFileName(blockID, tenantID)
	ctx := context.TODO()

	// TODO: this can be simplified
	src, err := rw.readAll(ctx, metaFilename, 0, blob.CountToEnd)
	if err != nil {
		return err
	}

	err = rw.writeAll(ctx, compactedMetaFilename, src)
	if err != nil {
		return err
	}

	// delete the old file
	return rw.delete(ctx, metaFilename)
}

func (rw *readerWriter) ClearBlock(blockID uuid.UUID, tenantID string) error {
	var warning error
	if len(tenantID) == 0 {
		return fmt.Errorf("empty tenant id")
	}

	if blockID == uuid.Nil {
		return fmt.Errorf("empty block id")
	}

	ctx := context.TODO()

	marker := blob.Marker{}

	for {
		list, err := rw.containerURL.ListBlobsHierarchySegment(ctx, marker, "", blob.ListBlobsSegmentOptions{
			Prefix:  util.RootPath(blockID, tenantID),
			Details: blob.BlobListingDetails{},
		})
		if err != nil {
			warning = err
			continue
		}
		marker = list.NextMarker

		for _, blob := range list.Segment.BlobItems {
			rw.delete(ctx, blob.Name)
		}
		// Continue iterating if we are not done.
		if !marker.NotDone() {
			break
		}

	}

	return warning
}

func (rw *readerWriter) CompactedBlockMeta(blockID uuid.UUID, tenantID string) (*encoding.CompactedBlockMeta, error) {
	if len(tenantID) == 0 {
		return nil, backend.ErrEmptyTenantID
	}
	if blockID == uuid.Nil {
		return nil, backend.ErrEmptyBlockID
	}
	name := util.CompactedMetaFileName(blockID, tenantID)

	bytes, modTime, err := rw.readAllWithModTime(context.Background(), name)
	if err != nil {
		return nil, err
	}

	out := &encoding.CompactedBlockMeta{}
	err = json.Unmarshal(bytes, out)
	if err != nil {
		return nil, err
	}
	out.CompactedTime = modTime

	return out, err
}

func (rw *readerWriter) readAllWithModTime(ctx context.Context, name string) ([]byte, time.Time, error) {
	bytes, err := rw.readAll(ctx, name, 0, blob.CountToEnd)
	if err != nil {
		return nil, time.Time{}, err
	}

	att, err := rw.getAttributes(ctx, name)
	if err != nil {
		return nil, time.Time{}, err
	}
	return bytes, att.LastModified, nil
}

// Attributes returns information about the specified blob using his name.
func (rw *readerWriter) getAttributes(ctx context.Context, name string) (BlobAttributes, error) {
	blobURL, err := getBlobURL(ctx, rw.cfg, name)
	if err != nil {
		return BlobAttributes{}, errors.Wrapf(err, "cannot get Azure blob URL, blob: %s", name)
	}

	var props *blob.BlobGetPropertiesResponse
	props, err = blobURL.GetProperties(ctx, blob.BlobAccessConditions{})
	if err != nil {
		return BlobAttributes{}, err
	}

	return BlobAttributes{
		Size:         props.ContentLength(),
		LastModified: props.LastModified(),
	}, nil
}

// Delete removes the blob with the given name.
func (rw *readerWriter) delete(ctx context.Context, name string) error {
	blobURL, err := getBlobURL(ctx, rw.cfg, name)
	if err != nil {
		return errors.Wrapf(err, "cannot get Azure blob URL, address: %s", name)
	}

	if _, err = blobURL.Delete(ctx, blob.DeleteSnapshotsOptionInclude, blob.BlobAccessConditions{}); err != nil {
		return errors.Wrapf(err, "error deleting blob, address: %s", name)
	}
	return nil
}
