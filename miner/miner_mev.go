package miner

import (
	"context"
	"fmt"
	"github.com/ava-labs/coreth/core/types"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/params"
)

type BuilderConfig struct {
	Address common.Address `json:"address"`
	URL     string         `json:"url"`
}

type MevConfig struct {
	Enabled             bool            `json:"enabled"`             // Whether to enable Mev or not
	Builders            []BuilderConfig `json:"builders"`            // The list of builders
	ValidatorCommission uint64          `json:"validatorCommission"` // 100 means the validator claims 1% from block reward
	ValidatorWallet     common.Address  `json:"validatorWallet"`     // The wallet of the validator that gets the rewards
}

// MevRunning return true if mev is running.
func (miner *Miner) MevRunning() bool {
	return miner.bidSimulator.receivingBid()
}

func (miner *Miner) SendBid(ctx context.Context, bidArgs *types.BidArgs) (common.Hash, error) {
	builder, err := bidArgs.EcrecoverSender()
	if err != nil {
		return common.Hash{}, types.NewInvalidBidError(fmt.Sprintf("invalid signature:%v", err))
	}

	if !miner.bidSimulator.ExistBuilder(builder) {
		return common.Hash{}, types.NewInvalidBidError("builder is not registered")
	}

	err = miner.bidSimulator.CheckPending(bidArgs.RawBid.BlockNumber, builder, bidArgs.RawBid.Hash())
	if err != nil {
		return common.Hash{}, err
	}

	signer := types.MakeSigner(miner.worker.chainConfig, big.NewInt(int64(bidArgs.RawBid.BlockNumber)), uint64(time.Now().Unix()))
	bid, err := bidArgs.ToBid(builder, signer)
	if err != nil {
		return common.Hash{}, types.NewInvalidBidError(fmt.Sprintf("fail to convert bidArgs to bid, %v", err))
	}

	err = miner.bidSimulator.sendBid(ctx, bid)

	if err != nil {
		return common.Hash{}, err
	}

	return bid.Hash(), nil
}

func (miner *Miner) BestPackedBlockReward(parentHash common.Hash) *big.Int {
	bidRuntime := miner.bidSimulator.GetBestBid(parentHash)
	if bidRuntime == nil {
		return big.NewInt(0)
	}

	return bidRuntime.packedBlockReward
}

func (miner *Miner) MevParams() *types.MevParams {
	return &types.MevParams{
		ValidatorCommission: miner.worker.config.Mev.ValidatorCommission,
		ValidatorWallet:     miner.worker.config.Mev.ValidatorWallet,
		GasPrice:            miner.worker.eth.TxPool().GasTip(),
		Version:             params.Version,
	}
}
