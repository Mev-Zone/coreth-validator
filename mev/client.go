package mev

import (
	"context"
	"fmt"

	"github.com/ava-labs/libevm/common"
	"github.com/mev-zone/coreth-validator/core/types"
	"github.com/mev-zone/coreth-validator/rpc"
)

var _ Client = (*client)(nil)

type Client interface {
	SendBid(ctx context.Context, args *types.BidArgs) (common.Hash, error)
}

// client implementation for interacting with EVM [chain]
type client struct {
	client *rpc.Client
}

// NewClient returns a Client for interacting with EVM [chain]
func NewClient(uri, chain string) (Client, error) {
	innerClient, err := rpc.Dial(fmt.Sprintf("%s/ext/bc/%s/rpc", uri, chain))
	if err != nil {
		return nil, fmt.Errorf("failed to dial client. err: %w", err)
	}
	return &client{
		client: innerClient,
	}, nil
}

func (c *client) SendBid(ctx context.Context, args *types.BidArgs) (common.Hash, error) {
	var res common.Hash
	if err := c.client.CallContext(ctx, &res, "mev_sendBid", args); err != nil {
		return common.Hash{}, fmt.Errorf("call to mev_sendBid failed. err: %w", err)
	}
	return res, nil
}
