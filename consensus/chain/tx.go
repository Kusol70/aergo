/**
 *  @file
 *  @copyright defined in aergo/LICENSE.txt
 */

package chain

import (
	"time"

	"github.com/aergoio/aergo-lib/log"
	"github.com/aergoio/aergo/message"
	"github.com/aergoio/aergo/pkg/component"
	"github.com/aergoio/aergo/types"
	"github.com/golang/protobuf/proto"
)

var logger = log.NewLogger("consensus")

// FetchTXs requests to mempool and returns types.Tx array.
func FetchTXs(hs component.ICompSyncRequester) []*types.Tx {
	//bf.RequestFuture(message.MemPoolSvc, &message.MemPoolGenerateSampleTxs{MaxCount: 3}, time.Second)
	result, err := hs.RequestFuture(message.MemPoolSvc, &message.MemPoolGet{}, time.Second,
		"consensus/util/info.FetchTXs").Result()
	if err != nil {
		logger.Info().Err(err).Msg("can't fetch transactions from mempool")
		return make([]*types.Tx, 0)
	}

	return result.(*message.MemPoolGetRsp).Txs
}

// TxOp is an interface used by GatherTXs for apply some transaction related operation.
type TxOp interface {
	Apply(tx *types.Tx) (*types.BlockState, error)
}

// TxOpFn is the type of arguments for CompositeTxDo.
type TxOpFn func(tx *types.Tx) (*types.BlockState, error)

// Apply applies f to tx.
func (f TxOpFn) Apply(tx *types.Tx) (*types.BlockState, error) {
	return f(tx)
}

// NewCompTxOp returns a function which applies each function in fn.
func NewCompTxOp(fn ...TxOp) TxOp {
	return TxOpFn(func(tx *types.Tx) (*types.BlockState, error) {
		var blockState *types.BlockState
		for _, f := range fn {
			var curState *types.BlockState
			var err error
			if curState, err = f.Apply(tx); err != nil {
				return blockState, err
			}
			// Maintain the BlockState resulting from each tx operation.
			if curState != nil {
				blockState = curState
			}
		}

		// If TxOp executes tx, it has a resulting BlockState. The final
		// BlockState must be sent to the chain service receiver.
		return blockState, nil
	})
}

func newBlockLimitOp(maxBlockBodySize uint32) TxOpFn {
	// Caution: the closure below captures the local variable 'size.' Generate
	// it whenever needed. Don't reuse it!
	size := 0
	return TxOpFn(func(tx *types.Tx) (*types.BlockState, error) {
		if size += proto.Size(tx); uint32(size) > maxBlockBodySize {
			return nil, errBlockSizeLimit
		}
		return nil, nil
	})
}

// GatherTXs returns transactions from txIn. The selection is done by applying
// txDo.
func GatherTXs(hs component.ICompSyncRequester, txOp TxOp, maxBlockBodySize uint32) ([]*types.Tx, *types.BlockState, error) {
	var (
		nCollected int
		last       int
		nCand      int
	)

	txIn := FetchTXs(hs)
	nCand = len(txIn)
	if nCand == 0 {
		return txIn, nil, nil
	}

	defer func() {
		logger.Debug().Int("candidates", nCand).Int("collected", nCollected).Msg("transactions collected")
	}()

	op := NewCompTxOp(newBlockLimitOp(maxBlockBodySize), txOp)
	var blockState *types.BlockState
	for i, tx := range txIn {
		last = i

		curState, err := op.Apply(tx)
		if curState != nil {
			blockState = curState
		}

		if e, ok := err.(ErrTimeout); ok {
			err = e
			break
		} else if err == errBlockSizeLimit {
			break
		} else if err != nil {
			return nil, nil, err
		}
	}

	nCollected = last + 1

	return txIn[0 : last+1], blockState, nil
}
