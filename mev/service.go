package mev

import (
	"context"

	"github.com/ava-labs/libevm/common"
	"github.com/mev-zone/coreth-validator/core/types"
)

type API struct {
	b Backend
}

func NewAPI(b Backend) *API {
	return &API{b}
}

func (m *API) SendBid(ctx context.Context, args types.BidArgs) (common.Hash, error) {
	return m.b.SendBid(ctx, &args)
}
