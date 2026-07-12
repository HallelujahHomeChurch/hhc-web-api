package publication

import (
	"context"
	"errors"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
)

type AssetAdapter struct{ client *assetclient.Client }

func NewAssetAdapter(client *assetclient.Client) *AssetAdapter { return &AssetAdapter{client: client} }

func (a *AssetAdapter) Get(ctx context.Context, id string) (Asset, error) {
	value, err := a.client.Get(ctx, id)
	return Asset{
		ID:               value.ID,
		OwnerService:     value.OwnerService,
		UploadStatus:     value.UploadStatus,
		ScanStatus:       value.ScanStatus,
		ProcessingStatus: value.ProcessingStatus,
	}, err
}
func (a *AssetAdapter) CreatePublicGrant(ctx context.Context, id, key string) (Grant, error) {
	value, err := a.client.CreatePublicGrant(ctx, id, key)
	return Grant{ID: value.ID}, err
}
func (a *AssetAdapter) RevokeGrant(ctx context.Context, assetID, grantID string) error {
	err := a.client.RevokeGrant(ctx, assetID, grantID)
	if errors.Is(err, assetclient.ErrNotFound) {
		return ErrGrantNotFound
	}
	return err
}
func (a *AssetAdapter) PublicURL(id string) string { return a.client.PublicURL(id) }

var _ AssetClient = (*AssetAdapter)(nil)
