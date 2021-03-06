/**
 *  @file
 *  @copyright defined in aergo/LICENSE.txt
 */

package rpc

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"reflect"
	"time"

	"github.com/aergoio/aergo-actor/actor"

	"github.com/aergoio/aergo-lib/log"
	"github.com/aergoio/aergo/message"
	"github.com/aergoio/aergo/p2p"
	"github.com/aergoio/aergo/pkg/component"
	"github.com/aergoio/aergo/types"

	"github.com/golang/protobuf/ptypes/timestamp"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	logger = log.NewLogger("rpc")
)

// AergoRPCService implements GRPC server which is defined in rpc.proto
type AergoRPCService struct {
	hub         *component.ComponentHub
	actorHelper p2p.ActorService
	msgHelper   message.Helper
}

// FIXME remove redundant constants
const halfMinute = time.Second * 30
const defaultActorTimeout = time.Second * 3

var _ types.AergoRPCServiceServer = (*AergoRPCService)(nil)

// Blockchain handle rpc request blockchain. It has no additional input parameter
func (rpc *AergoRPCService) Blockchain(ctx context.Context, in *types.Empty) (*types.BlockchainStatus, error) {
	//last, _ := rpc.ChainService.GetBestBlock()
	result, err := rpc.hub.RequestFuture(message.ChainSvc, &message.GetBestBlock{}, defaultActorTimeout,
		"rpc.(*AergoRPCService).Blockchain").Result()
	if err != nil {
		return nil, err
	}
	rsp, ok := result.(message.GetBestBlockRsp)
	if !ok {
		return nil, status.Errorf(codes.Internal, "internal type error")
	}
	if rsp.Err != nil {
		return nil, rsp.Err
	}
	last := rsp.Block
	return &types.BlockchainStatus{
		BestBlockHash: last.BlockHash(),
		BestHeight:    last.GetHeader().GetBlockNo(),
	}, nil
}

// ListBlockHeaders handle rpc request listblocks
func (rpc *AergoRPCService) ListBlockHeaders(ctx context.Context, in *types.ListParams) (*types.BlockHeaderList, error) {
	var maxFetchSize uint32
	// TODO refactor with almost same code is in p2pcmdblock.go
	if in.Size > uint32(1000) {
		maxFetchSize = uint32(1000)
	} else {
		maxFetchSize = in.Size
	}
	idx := uint32(0)
	hashes := make([][]byte, 0, maxFetchSize)
	headers := make([]*types.Block, 0, maxFetchSize)
	if len(in.Hash) > 0 {
		hash := in.Hash
		for idx < maxFetchSize {
			foundBlock, ok := extractBlockFromFuture(rpc.hub.RequestFuture(message.ChainSvc,
				&message.GetBlock{BlockHash: hash}, defaultActorTimeout, "rpc.(*AergoRPCService).ListBlockHeaders#1"))
			if !ok || nil == foundBlock {
				break
			}
			hashes = append(hashes, foundBlock.Hash)
			foundBlock.Body = nil
			headers = append(headers, foundBlock)
			idx++
			hash = foundBlock.Header.PrevBlockHash
			if len(hash) == 0 {
				break
			}
		}
	} else {
		end := types.BlockNo(0)
		if types.BlockNo(in.Height) >= types.BlockNo(maxFetchSize) {
			end = types.BlockNo(in.Height) - types.BlockNo(maxFetchSize-1)
		}
		for i := types.BlockNo(in.Height); i >= end; i-- {
			foundBlock, ok := extractBlockFromFuture(rpc.hub.RequestFuture(message.ChainSvc,
				&message.GetBlockByNo{BlockNo: i}, defaultActorTimeout, "rpc.(*AergoRPCService).ListBlockHeaders#2"))
			if !ok || nil == foundBlock {
				break
			}
			hashes = append(hashes, foundBlock.Hash)
			foundBlock.Body = nil
			headers = append(headers, foundBlock)
			idx++
		}
	}

	return &types.BlockHeaderList{Blocks: headers}, nil

}

func extractBlockFromFuture(future *actor.Future) (*types.Block, bool) {
	rawResponse, err := future.Result()
	if err != nil {
		return nil, false
	}
	var blockRsp *message.GetBlockRsp
	switch v := rawResponse.(type) {
	case message.GetBlockRsp:
		blockRsp = &v
	case message.GetBestBlockRsp:
		blockRsp = (*message.GetBlockRsp)(&v)
	case message.GetBlockByNoRsp:
		blockRsp = (*message.GetBlockRsp)(&v)
	default:
		return nil, false
	}
	return extractBlock(blockRsp)
}

func extractBlock(from *message.GetBlockRsp) (*types.Block, bool) {
	if nil != from.Err {
		return nil, false
	}
	return from.Block, true

}

// GetBlock handle rpc request getblock
func (rpc *AergoRPCService) GetBlock(ctx context.Context, in *types.SingleBytes) (*types.Block, error) {
	var result interface{}
	var err error
	if cap(in.Value) == 0 {
		return nil, status.Errorf(codes.InvalidArgument, "recevice no bytes")
	}
	if len(in.Value) < 32 {
		number := uint64(binary.LittleEndian.Uint64(in.Value))
		result, err = rpc.hub.RequestFuture(message.ChainSvc, &message.GetBlockByNo{BlockNo: number},
			defaultActorTimeout, "rpc.(*AergoRPCService).GetBlock#1").Result()
	} else {
		result, err = rpc.hub.RequestFuture(message.ChainSvc, &message.GetBlock{BlockHash: in.Value},
			defaultActorTimeout, "rpc.(*AergoRPCService).GetBlock#2").Result()
	}
	if err != nil {
		return nil, err
	}
	found, err := rpc.msgHelper.ExtractBlockFromResponse(result)
	if err != nil {
		return nil, status.Errorf(codes.Internal, err.Error())
	}
	if found == nil {
		return nil, status.Errorf(codes.NotFound, "not found")
	}
	return found, nil
}

// GetTX handle rpc request gettx
func (rpc *AergoRPCService) GetTX(ctx context.Context, in *types.SingleBytes) (*types.Tx, error) {
	result, err := rpc.actorHelper.CallRequest(message.MemPoolSvc,
		&message.MemPoolExist{Hash: in.Value})
	if err != nil {
		return nil, err
	}
	tx, err := rpc.msgHelper.ExtractTxFromResponse(result)
	if err != nil {
		return nil, err
	}
	if tx != nil {
		return tx, nil
	}
	// TODO try find tx in blockchain, but chainservice doesn't have method yet.

	return nil, status.Errorf(codes.NotFound, "not found")
}

// GetBlockTX handle rpc request gettx
func (rpc *AergoRPCService) GetBlockTX(ctx context.Context, in *types.SingleBytes) (*types.TxInBlock, error) {
	result, err := rpc.hub.RequestFuture(message.ChainSvc,
		&message.GetTx{TxHash: in.Value}, defaultActorTimeout, "rpc.(*AergoRPCService).GetBlockTX").Result()
	if err != nil {
		return nil, err
	}
	rsp, ok := result.(message.GetTxRsp)
	if !ok {
		return nil, status.Errorf(codes.Internal, "internal type (%v) error", reflect.TypeOf(result))
	}
	return &types.TxInBlock{Tx: rsp.Tx, TxIdx: rsp.TxIds}, rsp.Err
}

var emptyBytes = make([]byte, 0)

// SendTX try to fill the nonce, sign, hash in the transaction automatically and commit it
func (rpc *AergoRPCService) SendTX(ctx context.Context, tx *types.Tx) (*types.CommitResult, error) {
	getStateResult, err := rpc.hub.RequestFuture(message.ChainSvc,
		&message.GetState{Account: tx.Body.Account}, defaultActorTimeout, "rpc.(*AergoRPCService).SendTx").Result()
	if err != nil {
		return nil, err
	}
	getStateRsp, ok := getStateResult.(message.GetStateRsp)
	if !ok {
		return nil, status.Errorf(codes.Internal, "internal type (%v) error", reflect.TypeOf(getStateResult))
	}
	if getStateRsp.Err != nil {
		return nil, status.Errorf(codes.Internal, "internal error : %s", err.Error())
	}
	tx.Body.Nonce = getStateRsp.State.GetNonce() + 1

	signTxResult, err := rpc.hub.RequestFuture(message.AccountsSvc,
		&message.SignTx{Tx: tx}, defaultActorTimeout, "rpc.(*AergoRPCService).SendTX").Result()
	if err != nil {
		return nil, err
	}
	signTxRsp, ok := signTxResult.(*message.SignTxRsp)
	if !ok {
		return nil, status.Errorf(codes.Internal, "internal type (%v) error", reflect.TypeOf(signTxResult))
	}
	if signTxRsp.Err != nil {
		return nil, signTxRsp.Err
	}
	tx = signTxRsp.Tx
	memPoolPutResult, err := rpc.hub.RequestFuture(message.MemPoolSvc,
		&message.MemPoolPut{Txs: []*types.Tx{tx}},
		defaultActorTimeout, "rpc.(*AergoRPCService).SendTX").Result()
	memPoolPutRsp, ok := memPoolPutResult.(*message.MemPoolPutRsp)
	if !ok {
		return nil, status.Errorf(codes.Internal, "internal type (%v) error", reflect.TypeOf(memPoolPutResult))
	}
	return &types.CommitResult{Hash: tx.Hash, Error: convertError(memPoolPutRsp.Err[0])}, err
}

// CommitTX handle rpc request commit
func (rpc *AergoRPCService) CommitTX(ctx context.Context, in *types.TxList) (*types.CommitResultList, error) {
	// TODO: check validity
	//if bytes.Equal(emptyBytes, in.Hash) {
	//	return nil, status.Errorf(codes.InvalidArgument, "invalid hash")
	//}
	if in.Txs == nil {
		return nil, status.Errorf(codes.InvalidArgument, "input tx is empty")
	}
	rs := make([]*types.CommitResult, len(in.Txs))
	results := &types.CommitResultList{Results: rs}
	//results := &types.CommitResultList{}
	start := 0
	cnt := 0
	chunk := 100

	for i, tx := range in.Txs {
		hash := tx.Hash
		var r types.CommitResult
		r.Hash = hash

		calculated := tx.CalculateTxHash()

		if !bytes.Equal(hash, calculated) {
			r.Error = types.CommitStatus_TX_INVALID_HASH
		}
		results.Results[i] = &r
		cnt++

		if (i > 0 && i%chunk == 0) || i == len(in.Txs)-1 {
			//send tx message to mempool
			result, err := rpc.hub.RequestFuture(message.MemPoolSvc,
				&message.MemPoolPut{Txs: in.Txs[start : start+cnt]},
				defaultActorTimeout, "rpc.(*AergoRPCService).CommitTX").Result()
			if err != nil {
				return nil, err
			}
			rsp, ok := result.(*message.MemPoolPutRsp)
			if !ok {
				return nil, status.Errorf(codes.Internal, "internal type (%v) error", reflect.TypeOf(result))
			}

			for j, err := range rsp.Err {
				results.Results[start+j].Error = convertError(err)
			}
			start += cnt
			cnt = 0
		}

	}

	return results, nil
}

// GetState handle rpc request getstate
func (rpc *AergoRPCService) GetState(ctx context.Context, in *types.SingleBytes) (*types.State, error) {
	result, err := rpc.hub.RequestFuture(message.ChainSvc,
		&message.GetState{Account: in.Value}, defaultActorTimeout, "rpc.(*AergoRPCService).GetState").Result()
	if err != nil {
		return nil, err
	}
	rsp, ok := result.(message.GetStateRsp)
	if !ok {
		return nil, status.Errorf(codes.Internal, "internal type (%v) error", reflect.TypeOf(result))
	}
	return rsp.State, rsp.Err
}

// CreateAccount handle rpc request newaccount
func (rpc *AergoRPCService) CreateAccount(ctx context.Context, in *types.Personal) (*types.Account, error) {
	result, err := rpc.hub.RequestFuture(message.AccountsSvc,
		&message.CreateAccount{Passphrase: in.Passphrase}, defaultActorTimeout, "rpc.(*AergoRPCService).CreateAccount").Result()
	if err != nil {
		return nil, err
	}
	rsp, ok := result.(*message.CreateAccountRsp)
	if !ok {
		return nil, status.Errorf(codes.Internal, "internal type (%v) error", reflect.TypeOf(result))
	}
	return rsp.Account, nil
}

// GetAccounts handle rpc request getaccounts
func (rpc *AergoRPCService) GetAccounts(ctx context.Context, in *types.Empty) (*types.AccountList, error) {
	result, err := rpc.hub.RequestFuture(message.AccountsSvc,
		&message.GetAccounts{}, defaultActorTimeout, "rpc.(*AergoRPCService).GetAccounts").Result()
	if err != nil {
		return nil, err
	}
	rsp, ok := result.(*message.GetAccountsRsp)
	if !ok {
		return nil, status.Errorf(codes.Internal, "internal type (%v) error", reflect.TypeOf(result))
	}
	return rsp.Accounts, nil
}

// LockAccount handle rpc request lockaccount
func (rpc *AergoRPCService) LockAccount(ctx context.Context, in *types.Personal) (*types.Account, error) {
	result, err := rpc.hub.RequestFuture(message.AccountsSvc,
		&message.LockAccount{Account: in.Account, Passphrase: in.Passphrase},
		defaultActorTimeout, "rpc.(*AergoRPCService).LockAccount").Result()
	if err != nil {
		return nil, err
	}
	rsp, ok := result.(*message.AccountRsp)
	if !ok {
		return nil, status.Errorf(codes.Internal, "internal type (%v) error", reflect.TypeOf(result))
	}
	return rsp.Account, rsp.Err
}

// UnlockAccount handle rpc request unlockaccount
func (rpc *AergoRPCService) UnlockAccount(ctx context.Context, in *types.Personal) (*types.Account, error) {
	result, err := rpc.hub.RequestFuture(message.AccountsSvc,
		&message.UnlockAccount{Account: in.Account, Passphrase: in.Passphrase},
		defaultActorTimeout, "rpc.(*AergoRPCService).UnlockAccount").Result()
	if err != nil {
		return nil, err
	}
	rsp, ok := result.(*message.AccountRsp)
	if !ok {
		return nil, status.Errorf(codes.Internal, "internal type (%v) error", reflect.TypeOf(result))
	}
	return rsp.Account, rsp.Err
}

// SignTX handle rpc request signtx
func (rpc *AergoRPCService) SignTX(ctx context.Context, in *types.Tx) (*types.Tx, error) {
	result, err := rpc.hub.RequestFuture(message.AccountsSvc,
		&message.SignTx{Tx: in}, defaultActorTimeout, "rpc.(*AergoRPCService).SignTX").Result()
	if err != nil {
		return nil, err
	}
	rsp, ok := result.(*message.SignTxRsp)
	if !ok {
		return nil, status.Errorf(codes.Internal, "internal type (%v) error", reflect.TypeOf(result))
	}
	return rsp.Tx, rsp.Err
}

// VerifyTX handle rpc request verifytx
func (rpc *AergoRPCService) VerifyTX(ctx context.Context, in *types.Tx) (*types.VerifyResult, error) {
	result, err := rpc.hub.RequestFuture(message.AccountsSvc,
		&message.VerifyTx{Tx: in}, defaultActorTimeout, "rpc.(*AergoRPCService).VerifyTX").Result()
	if err != nil {
		return nil, err
	}
	rsp, ok := result.(*message.VerifyTxRsp)
	if !ok {
		return nil, status.Errorf(codes.Internal, "internal type (%v) error", reflect.TypeOf(result))
	}
	ret := &types.VerifyResult{Tx: rsp.Tx}
	if rsp.Err == message.ErrSignNotMatch {
		ret.Error = types.VerifyStatus_VERIFY_STATUS_SIGN_NOT_MATCH
	} else {
		ret.Error = types.VerifyStatus_VERIFY_STATUS_OK
	}
	return ret, nil
}

// GetPeers handle rpc request getpeers
func (rpc *AergoRPCService) GetPeers(ctx context.Context, in *types.Empty) (*types.PeerList, error) {
	result, err := rpc.hub.RequestFuture(message.P2PSvc,
		&message.GetPeers{}, halfMinute, "rpc.(*AergoRPCService).GetPeers").Result()
	if err != nil {
		return nil, err
	}
	rsp := result.(*message.GetPeersRsp)
	states := make([]int32, len(rsp.States))
	for i, state := range rsp.States {
		states[i] = int32(state)
	}

	return &types.PeerList{Peers: rsp.Peers, States: states}, nil
}

// NodeState handle rpc request nodestate
func (rpc *AergoRPCService) NodeState(ctx context.Context, in *types.SingleBytes) (*types.SingleBytes, error) {
	timeout := int64(binary.LittleEndian.Uint64(in.Value))
	statics := rpc.hub.Statistics(time.Duration(timeout) * time.Second)
	data, err := json.MarshalIndent(statics, "", "\t")
	if err != nil {
		return nil, err
	}
	return &types.SingleBytes{Value: data}, nil
}

//GetVotes handle rpc request getvotes
func (rpc *AergoRPCService) GetVotes(ctx context.Context, in *types.SingleBytes) (*types.VoteList, error) {
	const addresslength = 32
	var number int
	if len(in.Value) < addresslength {
		number = int(binary.LittleEndian.Uint64(in.Value))
	}
	result, err := rpc.hub.RequestFuture(message.ChainSvc,
		&message.GetElected{N: number}, defaultActorTimeout, "rpc.(*AergoRPCService).GetElected").Result()
	if err != nil {
		return nil, err
	}
	rsp, ok := result.(*message.GetElectedRsp)
	if !ok {
		return nil, status.Errorf(codes.Internal, "internal type (%v) error", reflect.TypeOf(result))
	}
	return rsp.Top, rsp.Err
}

func (rpc *AergoRPCService) GetReceipt(ctx context.Context, in *types.SingleBytes) (*types.Receipt, error) {
	result, err := rpc.hub.RequestFuture(message.ChainSvc,
		&message.GetReceipt{TxHash: in.Value}, defaultActorTimeout, "rpc.(*AergoRPCService).GetReceipt").Result()
	if err != nil {
		return nil, err
	}
	rsp, ok := result.(message.GetReceiptRsp)
	if !ok {
		return nil, status.Errorf(codes.Internal, "internal type (%v) error", reflect.TypeOf(result))
	}
	return rsp.Receipt, rsp.Err
}

func (rpc *AergoRPCService) GetABI(ctx context.Context, in *types.SingleBytes) (*types.ABI, error) {
	result, err := rpc.hub.RequestFuture(message.ChainSvc,
		&message.GetABI{Contract: in.Value}, defaultActorTimeout, "rpc.(*AergoRPCService).GetABI").Result()
	if err != nil {
		return nil, err
	}
	rsp, ok := result.(message.GetABIRsp)
	if !ok {
		return nil, status.Errorf(codes.Internal, "internal type (%v) error", reflect.TypeOf(result))
	}
	return rsp.ABI, rsp.Err
}

func (rpc *AergoRPCService) QueryContract(ctx context.Context, in *types.Query) (*types.SingleBytes, error) {
	result, err := rpc.hub.RequestFuture(message.ChainSvc,
		&message.GetQuery{Contract: in.ContractAddress, Queryinfo: in.Queryinfo}, defaultActorTimeout, "rpc.(*AergoRPCService).QueryContract").Result()
	if err != nil {
		return nil, err
	}
	rsp, ok := result.(message.GetQueryRsp)
	if !ok {
		return nil, status.Errorf(codes.Internal, "internal type (%v) error", reflect.TypeOf(result))
	}
	return &types.SingleBytes{Value: rsp.Result}, rsp.Err
}

func toTimestamp(time time.Time) *timestamp.Timestamp {
	return &timestamp.Timestamp{
		Seconds: time.Unix(),
		Nanos:   int32(time.Nanosecond())}
}

func fromTimestamp(timestamp *timestamp.Timestamp) time.Time {
	return time.Unix(timestamp.Seconds, int64(timestamp.Nanos))
}
