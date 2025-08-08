package mev

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"time"

	"github.com/ava-labs/avalanchego/snow"
	"github.com/ava-labs/avalanchego/vms/platformvm/warp"
	"github.com/ava-labs/libevm/common"
	types2 "github.com/ava-labs/libevm/core/types"
	"github.com/mev-zone/coreth-validator/core/types"
	"github.com/mev-zone/coreth-validator/params"
)

type BuilderConfig struct {
	Address common.Address `json:"address"`
	URL     string         `json:"url"`
}

type Config struct {
	Builders            []BuilderConfig `json:"builders"`            // The list of builders
	ValidatorCommission uint64          `json:"validatorCommission"` // 100 means the validator claims 1% from block reward
	ValidatorWallet     common.Address  `json:"validatorWallet"`     // The wallet of the validator that gets the rewards
}

type Backend interface {
	// SendBid receives bid from the builders.
	SendBid(ctx context.Context, bid *types.BidArgs) (common.Hash, error)
	SetBidSimulator(client BidSimulatorClient)
	MevParams() (*types.MevParams, error)
}

type EthereumClient interface {
	CurrentHeader() *types2.Header
}

type BidSimulatorClient interface {
	ExistBuilder(builder common.Address) bool
	CheckPending(blockNumber uint64, builder common.Address, bidHash common.Hash) error
	SendBid(ctx context.Context, bid *types.Bid) error
}

type backend struct {
	ctx          *snow.Context
	config       Config
	b            EthereumClient
	bidSimulator BidSimulatorClient
	chainConfig  *params.ChainConfig
}

func NewBackend(
	ctx *snow.Context,
	config Config,
	eth EthereumClient,
	chainConfig *params.ChainConfig,
) Backend {
	return &backend{
		ctx:         ctx,
		config:      config,
		b:           eth,
		chainConfig: chainConfig,
	}
}

// SendBid receives bid from the builders.
// If mev is not running or bid is invalid, return error.
// Otherwise, creates a builder bid for the given argument, submit it to the miner.
func (m *backend) SendBid(ctx context.Context, args *types.BidArgs) (common.Hash, error) {
	var (
		rawBid        = args.RawBid
		currentHeader = m.b.CurrentHeader()
	)

	if rawBid == nil {
		return common.Hash{}, types.NewInvalidBidError("rawBid should not be nil")
	}

	// only support bidding for the next block not for the future block
	if rawBid.BlockNumber != currentHeader.Number.Uint64()+1 {
		return common.Hash{}, types.NewInvalidBidError("stale block number or block in future")
	}

	if rawBid.ParentHash != currentHeader.Hash() {
		return common.Hash{}, types.NewInvalidBidError(
			fmt.Sprintf("non-aligned parent hash: %v", currentHeader.Hash()))
	}

	if rawBid.GasFee == nil || rawBid.GasFee.Cmp(common.Big0) == 0 || rawBid.GasUsed == 0 {
		return common.Hash{}, types.NewInvalidBidError("empty gasFee or empty gasUsed")
	}

	if len(args.BurnTx) == 0 {
		return common.Hash{}, types.NewInvalidPayBidTxError("burnTx are must-have")
	}

	if len(args.PayBidTx) == 0 || args.PayBidTxGasUsed == 0 {
		return common.Hash{}, types.NewInvalidPayBidTxError("payBidTx and payBidTxGasUsed are must-have")
	}

	if args.PayBidTxGasUsed > params.PayBidTxGasLimit {
		return common.Hash{}, types.NewInvalidBidError(
			fmt.Sprintf("transfer tx gas used must be no more than %v", params.PayBidTxGasLimit))
	}

	builder, err := args.EcrecoverSender()
	if err != nil {
		return common.Hash{}, types.NewInvalidBidError(fmt.Sprintf("invalid signature:%v", err))
	}

	if !m.bidSimulator.ExistBuilder(builder) {
		return common.Hash{}, types.NewInvalidBidError("builder is not registered")
	}

	err = m.bidSimulator.CheckPending(args.RawBid.BlockNumber, builder, args.RawBid.Hash())
	if err != nil {
		return common.Hash{}, err
	}

	signer := types2.MakeSigner(m.chainConfig, big.NewInt(int64(args.RawBid.BlockNumber)), uint64(time.Now().Unix()))
	bid, err := args.ToBid(builder, signer)
	if err != nil {
		return common.Hash{}, types.NewInvalidBidError(fmt.Sprintf("fail to convert bidArgs to bid, %v", err))
	}

	err = m.bidSimulator.SendBid(ctx, bid)

	if err != nil {
		return common.Hash{}, err
	}

	return bid.Hash(), nil
}

func (m *backend) MevParams() (*types.MevParams, error) {
	p := &types.MevParams{
		ValidatorCommission: m.config.ValidatorCommission,
		ValidatorWallet:     m.config.ValidatorWallet,
		Version:             params.Version,
	}
	payload, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}

	msg, err := warp.NewUnsignedMessage(m.ctx.NetworkID, m.ctx.ChainID, payload)
	if err != nil {
		return nil, err
	}

	sign, err := m.ctx.WarpSigner.Sign(msg)
	if err != nil {
		return nil, err
	}

	p.Signature = sign
	return p, nil
}

func (m *backend) SetBidSimulator(client BidSimulatorClient) {
	m.bidSimulator = client
}
