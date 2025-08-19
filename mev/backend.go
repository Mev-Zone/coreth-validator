package mev

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/ava-labs/avalanchego/snow"
	"github.com/ava-labs/avalanchego/utils/crypto/bls"
	"github.com/ava-labs/avalanchego/vms/platformvm/warp"
	"github.com/ava-labs/libevm/common"
	types2 "github.com/ava-labs/libevm/core/types"
	"github.com/ava-labs/libevm/log"
	"github.com/ava-labs/libevm/metrics"
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
	SetBidSimulator(client BidSimulatorClient)
	MevParams() (*types.HexParams, error)
	FetchBids(ctx context.Context, height int64) error
}

type EthereumClient interface {
	CurrentHeader() *types2.Header
}

type Builder interface {
	Bid(ctx context.Context, result *types.BidArgs, args *types.HexParams) error
}

type BidSimulatorClient interface {
	ExistBuilder(builder common.Address) bool
	CheckPending(blockNumber uint64, builder common.Address, bidHash common.Hash) error
	SendBid(ctx context.Context, bid *types.Bid) error
	Builders() map[common.Address]Builder
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

func (m *backend) FetchBids(ctx context.Context, height int64) error {
	var wg sync.WaitGroup
	var bids []types.BidArgs
	errors := make([]error, 0)

	p := &types.BidParams{
		Height: height,
	}

	msg, err := m.sign(p)
	if err != nil {
		return err
	}

	for k, builder := range m.bidSimulator.Builders() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var result types.BidArgs
			err = builder.Bid(ctx, &result, msg)
			if err != nil {
				errors = append(errors, fmt.Errorf("fail to fetch bid: %v: %w", k, err))
				metrics.GetOrRegisterCounter(fmt.Sprintf("bid/fetch/err/%v", k), nil).Inc(1)
			} else {
				bids = append(bids, result)
			}
		}()
	}

	wg.Wait()

	for _, bid := range bids {
		err = m.sendBid(ctx, bid)
		if err != nil {
			errors = append(errors, err)
		}
	}

	for _, err = range errors {
		log.Error("Error fetching bid", "error", err)
		return err
	}

	return nil
}

// SendBid receives bid from the builders.
// If mev is not running or bid is invalid, return error.
// Otherwise, creates a builder bid for the given argument, submit it to the miner.
func (m *backend) sendBid(ctx context.Context, args types.BidArgs) error {
	var (
		rawBid        = args.RawBid
		currentHeader = m.b.CurrentHeader()
	)

	if rawBid == nil {
		return types.NewInvalidBidError("rawBid should not be nil")
	}

	// only support bidding for the next block not for the future block
	if rawBid.BlockNumber != currentHeader.Number.Uint64()+1 {
		return types.NewInvalidBidError("stale block number or block in future")
	}

	if rawBid.ParentHash != currentHeader.Hash() {
		return types.NewInvalidBidError(
			fmt.Sprintf("non-aligned parent hash: %v", currentHeader.Hash()))
	}

	if rawBid.GasFee == nil || rawBid.GasFee.Cmp(common.Big0) == 0 || rawBid.GasUsed == 0 {
		return types.NewInvalidBidError("empty gasFee or empty gasUsed")
	}

	if len(args.BurnTx) == 0 {
		return types.NewInvalidPayBidTxError("burnTx are must-have")
	}

	if len(args.PayBidTx) == 0 || args.PayBidTxGasUsed == 0 {
		return types.NewInvalidPayBidTxError("payBidTx and payBidTxGasUsed are must-have")
	}

	if args.PayBidTxGasUsed > params.PayBidTxGasLimit {
		return types.NewInvalidBidError(
			fmt.Sprintf("transfer tx gas used must be no more than %v", params.PayBidTxGasLimit))
	}

	builder, err := args.EcrecoverSender()
	if err != nil {
		return types.NewInvalidBidError(fmt.Sprintf("invalid signature:%v", err))
	}

	if !m.bidSimulator.ExistBuilder(builder) {
		return types.NewInvalidBidError("builder is not registered")
	}

	err = m.bidSimulator.CheckPending(args.RawBid.BlockNumber, builder, args.RawBid.Hash())
	if err != nil {
		return err
	}

	signer := types2.MakeSigner(m.chainConfig, big.NewInt(int64(args.RawBid.BlockNumber)), uint64(time.Now().Unix()))
	bid, err := args.ToBid(builder, signer)
	if err != nil {
		return types.NewInvalidBidError(fmt.Sprintf("fail to convert bidArgs to bid, %v", err))
	}

	return m.bidSimulator.SendBid(ctx, bid)
}

func (m *backend) MevParams() (*types.HexParams, error) {
	p := &types.MevParams{
		ValidatorCommission: m.config.ValidatorCommission,
		ValidatorWallet:     m.config.ValidatorWallet,
		Version:             params.Version,
	}

	return m.sign(p)
}

func (m *backend) sign(data any) (*types.HexParams, error) {
	payload, err := json.Marshal(data)
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

	pkBytes := bls.PublicKeyToCompressedBytes(m.ctx.PublicKey)

	return &types.HexParams{
		HexMsg:    hex.EncodeToString(msg.Bytes()),
		Signature: hex.EncodeToString(sign),
		PublicKey: hex.EncodeToString(pkBytes),
		SubnetID:  m.ctx.SubnetID.String(),
	}, nil
}

func (m *backend) SetBidSimulator(client BidSimulatorClient) {
	m.bidSimulator = client
}
