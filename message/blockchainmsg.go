/**
 *  @file
 *  @copyright defined in aergo/LICENSE.txt
 */

package message

import (
	"github.com/aergoio/aergo/types"
	"github.com/libp2p/go-libp2p-peer"
)

const ChainSvc = "ChainSvc"

type GetBestBlockNo struct{}
type GetBestBlockNoRsp struct {
	BlockNo types.BlockNo
}

type GetBestBlock struct{}
type GetBestBlockRsp GetBlockRsp

type GetBlock struct {
	BlockHash []byte
}
type GetBlockRsp struct {
	Block *types.Block
	Err   error
}
type GetMissing struct {
	Hashes   [][]byte
	StopHash []byte
}
type GetMissingRsp struct {
	Hashes   []BlockHash
	Blocknos []types.BlockNo
}

type GetBlockByNo struct {
	BlockNo types.BlockNo
}
type GetBlockByNoRsp GetBlockRsp

type AddBlock struct {
	PeerID peer.ID
	Block  *types.Block
	Bstate *types.BlockState
}
type AddBlockRsp struct {
	BlockNo   types.BlockNo
	BlockHash []byte
	Err       error
}
type GetState struct {
	Account []byte
}
type GetStateRsp struct {
	State *types.State
	Err   error
}
type GetTx struct {
	TxHash []byte
}
type GetTxRsp struct {
	Tx    *types.Tx
	TxIds *types.TxIdx
	Err   error
}

type GetReceipt struct {
	TxHash []byte
}
type GetReceiptRsp struct {
	Receipt *types.Receipt
	Err     error
}

type GetABI struct {
	Contract []byte
}
type GetABIRsp struct {
	ABI *types.ABI
	Err error
}

type GetQuery struct {
	Contract  []byte
	Queryinfo []byte
}
type GetQueryRsp struct {
	Result []byte
	Err    error
}

// SyncBlockState is request to sync from remote peer. It returns sync result.
type SyncBlockState struct {
	PeerID    peer.ID
	BlockNo   types.BlockNo
	BlockHash []byte
}

// GetElected is request to get voting result about top N elect
type GetElected struct {
	N int
}

// GetElectedRsp is return to get voting result
type GetElectedRsp struct {
	Top *types.VoteList
	Err error
}
