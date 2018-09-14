/**
 *  @file
 *  @copyright defined in aergo/LICENSE.txt
 */

package blockchain

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"

	sha256 "github.com/minio/sha256-simd"

	"github.com/aergoio/aergo-lib/db"
	"github.com/aergoio/aergo/contract"
	"github.com/aergoio/aergo/internal/enc"
	"github.com/aergoio/aergo/message"
	"github.com/aergoio/aergo/state"
	"github.com/aergoio/aergo/types"
	"github.com/libp2p/go-libp2p-peer"
)

func (cs *ChainService) getBestBlockNo() types.BlockNo {
	return cs.cdb.getBestBlockNo()
}
func (cs *ChainService) GetBestBlock() (*types.Block, error) {
	return cs.getBestBlock()
}
func (cs *ChainService) getBestBlock() (*types.Block, error) {
	blockNo := cs.cdb.getBestBlockNo()
	//logger.Debug().Uint64("blockno", blockNo).Msg("get best block")

	return cs.cdb.getBlockByNo(blockNo)
}

func (cs *ChainService) getBlockByNo(blockNo types.BlockNo) (*types.Block, error) {
	return cs.cdb.getBlockByNo(blockNo)
}

func (cs *ChainService) getBlock(blockHash []byte) (*types.Block, error) {
	return cs.cdb.getBlock(blockHash)
}

func (cs *ChainService) getHashByNo(blockNo types.BlockNo) ([]byte, error) {
	return cs.cdb.getHashByNo(blockNo)
}

func (cs *ChainService) getTx(txHash []byte) (*types.Tx, *types.TxIdx, error) {

	tx, txidx, err := cs.cdb.getTx(txHash)
	if err != nil {
		return nil, nil, err
	}
	block, err := cs.cdb.getBlock(txidx.BlockHash)
	blockInMainChain, err := cs.cdb.getBlockByNo(block.Header.BlockNo)
	if !bytes.Equal(block.BlockHash(), blockInMainChain.BlockHash()) {
		return tx, nil, errors.New("tx is not in the main chain")
	}
	return tx, txidx, err
}

func (cs *ChainService) addBlock(nblock *types.Block, usedBstate *types.BlockState, peerID peer.ID) error {
	logger.Debug().Str("hash", nblock.ID()).Msg("add block")

	var bestBlock *types.Block
	var err error
	var isMainChain bool

	if bestBlock, err = cs.getBestBlock(); err != nil {
		return err
	}

	// Check consensus header validity
	if err := cs.IsBlockValid(nblock, bestBlock); err != nil {
		return err
	}

	// handle orphan
	if cs.isOrphan(nblock) {
		if usedBstate != nil {
			return fmt.Errorf("block received from BP can not be orphan")
		}
		err := cs.handleOrphan(nblock, peerID)
		return err
	}

	if isMainChain, err = cs.cdb.isMainChain(nblock); err != nil {
		return err
	}

	var dbtx *db.Transaction

	defer func() {
		if dbtx != nil {
			(*dbtx).Discard()
		}
	}()

	// connect orphans
	tblock := nblock
	processedTxn := 0
	var lastBlock *types.Block

	for tblock != nil {
		blockNo := tblock.GetHeader().GetBlockNo()
		dbtx := cs.cdb.store.NewTx(true)

		isBPMade := (usedBstate != nil)
		if isBPMade == false {
			if err = cs.verifier.VerifyBlock(tblock); err != nil {
				return err
			}
		}

		if isMainChain {
			if err = cs.executeBlock(usedBstate, tblock); err != nil {
				return err
			}
			processedTxn = len(tblock.GetBody().GetTxs())
		}

		if err = cs.cdb.addBlock(&dbtx, tblock, isMainChain, true); err != nil {
			return err
		}
		//FIXME: 에러가 발생한 경우 sdb도 rollback 되어야 한다.
		dbtx.Commit()

		if isMainChain {
			cs.RequestTo(message.MemPoolSvc, &message.MemPoolDel{
				// FIXME: remove legacy
				BlockNo: blockNo,
				Txs:     tblock.GetBody().GetTxs(),
			})

			//SyncWithConsensus :
			// 	After executing MemPoolDel in the chain service, MemPoolGet must be executed on the consensus.
			// 	To do this, cdb.setLatest() must be executed after MemPoolDel.
			//	In this case, messages of mempool is synchronized in actor message queue.
			cs.cdb.setLatest(blockNo)

			// XXX Something similar should be also done during
			// reorganization.
			cs.StatusUpdate(nblock)
			cs.notifyBlock(tblock)
		}

		logger.Info().Bool("isMainChain", isMainChain).
			Int("processed_txn", processedTxn).
			Uint64("latest", cs.cdb.latest).
			Uint64("blockNo", blockNo).
			Str("hash", tblock.ID()).
			Str("prev_hash", enc.ToString(tblock.GetHeader().GetPrevBlockHash())).
			Msg("block added")
		//return cs.mpool.Removes(tblock.GetBody().GetTxs()...)

		lastBlock = tblock

		if tblock, err = cs.connectOrphan(tblock); err != nil {
			return err
		}

		usedBstate = nil
	}

	/* reorganize
	   if new bestblock then process Txs
	   add block
	   if new bestblock then update context
	   connect next orphan
	*/
	if cs.needReorg(lastBlock) {
		err := cs.reorg(lastBlock)
		if err != nil {
			panic(err)
		}
	}

	return nil
}

type txExecFn func(tx *types.Tx, dbTx db.Transaction) error

type executor struct {
	sdb        *state.ChainStateDB
	blockState *types.BlockState
	execTx     txExecFn
	txs        []*types.Tx
}

func newExecutor(sdb *state.ChainStateDB, bState *types.BlockState, block *types.Block) *executor {
	var exec txExecFn

	// The DPoS block factory excutes transactions during block generation. In
	// such a case it send block with block state so that bState != nil. On the
	// contrary, the block propagated from the network is not half-executed.
	// Hence we need a new block state and tx executor (execTx).
	if bState == nil {
		bState = types.NewBlockState(types.NewBlockInfo(block.Header.BlockNo, block.BlockID(), block.PrevBlockID()))
		exec = func(tx *types.Tx, dbTx db.Transaction) error {
			return executeTx(sdb, bState, tx, dbTx, block.BlockNo(), block.GetHeader().GetTimestamp())
		}
	}

	txs := block.GetBody().GetTxs()

	return &executor{
		sdb:        sdb,
		blockState: bState,
		execTx:     exec,
		txs:        txs,
	}
}

func (e *executor) execute() error {
	dbTx := contract.DB.NewTx(true)
	if e.execTx != nil {
		for _, tx := range e.txs {
			if err := e.execTx(tx, dbTx); err != nil {
				dbTx.Commit()
				return err
			}
		}
	}
	dbTx.Commit()

	// TODO: sync status of bstate and cdb what to do if cdb.commit fails after
	// sdb.Apply() succeeds
	err := e.sdb.Apply(e.blockState)
	if err != nil {
		return err
	}
	return nil
}

func (cs *ChainService) executeBlock(bstate *types.BlockState, block *types.Block) error {
	ex := newExecutor(cs.sdb, bstate, block)

	if err := ex.execute(); err != nil {
		// FIXME: is that enough?
		logger.Error().Err(err).Str("hash", block.ID()).Msg("failed to execute block")

		return err
	}

	return nil
}

func executeTx(sdb *state.ChainStateDB, bs *types.BlockState, tx *types.Tx, dbTx db.Transaction, blockNo uint64, ts int64) error {
	txBody := tx.GetBody()
	senderID := types.ToAccountID(txBody.Account)
	senderState, err := sdb.GetBlockAccountClone(bs, senderID)
	if err != nil {
		return err
	}
	recipient := txBody.Recipient
	var receiverID types.AccountID
	var createContract bool
	if len(recipient) > 0 {
		receiverID = types.ToAccountID(recipient)
	} else {
		createContract = true
		// Determine new contract address
		h := sha256.New()
		h.Write(txBody.Account)
		h.Write([]byte(strconv.FormatUint(txBody.Nonce, 10)))
		recipientHash := h.Sum(nil) // byte array with length 32
		recipient = append([]byte{0x0C}, recipientHash...) // prepend 0x0C to make it same length as account addresses
		receiverID = types.ToAccountID(recipient)
	}
	receiverState, err := sdb.GetBlockAccountClone(bs, receiverID)
	if err != nil {
		return err
	}

	senderChange := types.Clone(*senderState).(types.State)
	receiverChange := types.Clone(*receiverState).(types.State)

	switch txBody.Type {
	case types.TxType_NORMAL:
		if senderID != receiverID {
			if senderChange.Balance < txBody.Amount {
				senderChange.Balance = 0 // FIXME: reject insufficient tx.
			} else {
				senderChange.Balance = senderState.Balance - txBody.Amount
			}
			receiverChange.Balance = receiverChange.Balance + txBody.Amount
		}
		if txBody.Payload != nil {
			contractState, err := sdb.OpenContractState(&receiverChange)
			if err != nil {
				return err
			}

			if createContract {
				err = contract.Create(contractState, txBody.Payload, recipient, tx.Hash, dbTx)
			} else {
				bcCtx := contract.NewContext(contractState, txBody.GetAccount(), tx.GetHash(),
					blockNo, ts, "", false, recipient, false)

				err = contract.Call(contractState, txBody.Payload, recipient, tx.Hash, bcCtx, dbTx)
				if err != nil {
					return err
				}
				err = sdb.CommitContractState(contractState)
			}
			if err != nil {
				return err
			}
		}
	case types.TxType_GOVERNANCE:
		err = executeGovernanceTx(sdb, txBody, &senderChange, &receiverChange, blockNo)
	default:
		logger.Warn().Str("tx", tx.String()).Msg("unknown type of transaction")
	}

	senderChange.Nonce = txBody.Nonce
	bs.PutAccount(senderID, senderState, &senderChange)
	if senderID != receiverID {
		bs.PutAccount(receiverID, receiverState, &receiverChange)
	}

	return err
}

// find an orphan block which is the child of the added block
func (cs *ChainService) connectOrphan(block *types.Block) (*types.Block, error) {
	hash := block.BlockHash()

	orphanID := types.ToBlockID(hash)
	orphan, exists := cs.op.cache[orphanID]
	if !exists {
		return nil, nil
	}

	orphanBlock := orphan.block

	if (block.GetHeader().GetBlockNo() + 1) != orphanBlock.GetHeader().GetBlockNo() {
		return nil, fmt.Errorf("invalid orphan block no (p=%d, c=%d)", block.GetHeader().GetBlockNo(),
			orphanBlock.GetHeader().GetBlockNo())
	}

	logger.Debug().Str("parentHash=", block.ID()).
		Str("orphanHash=", orphanBlock.ID()).
		Msg("connect orphan")

	cs.op.removeOrphan(orphanID)

	return orphanBlock, nil
}

func (cs *ChainService) isOrphan(block *types.Block) bool {
	prevhash := block.Header.PrevBlockHash
	_, err := cs.getBlock(prevhash)

	return err != nil
}

func (cs *ChainService) handleOrphan(block *types.Block, peerID peer.ID) error {
	err := cs.addOrphan(block)
	if err != nil {
		// logging???
		logger.Debug().Str("hash", block.ID()).Msg("add Orphan Block failed")

		return err
	}
	// request missing
	anchors := cs.getAnchorsFromHash(block.Hash)
	hashes := make([]message.BlockHash, 0)
	for _, a := range anchors {
		hashes = append(hashes, message.BlockHash(a))
	}
	cs.RequestTo(message.P2PSvc, &message.GetMissingBlocks{ToWhom: peerID, Hashes: hashes})

	return nil
}

func (cs *ChainService) addOrphan(block *types.Block) error {
	return cs.op.addOrphan(block)
}

func (cs *ChainService) handleMissing(stopHash []byte, Hashes [][]byte) ([]message.BlockHash, []types.BlockNo) {
	// 1. check endpoint is on main chain (or, return nil)
	logger.Debug().Str("hash", enc.ToString(stopHash)).Int("len", len(Hashes)).Msg("handle missing")
	var stopBlock *types.Block
	var err error
	if stopHash == nil {
		stopBlock, err = cs.getBestBlock()
	} else {
		stopBlock, err = cs.cdb.getBlock(stopHash)
	}
	if err != nil {
		return nil, nil
	}

	var mainhash []byte
	var mainblock *types.Block
	// 2. get the highest block of Hashes hash on main chain
	for _, hash := range Hashes {
		// need to be short
		mainblock, err = cs.cdb.getBlock(hash)
		if err != nil {
			continue
		}
		// get main hash with same block height
		mainhash, err = cs.cdb.getHashByNo(
			types.BlockNo(mainblock.GetHeader().GetBlockNo()))
		if err != nil {
			continue
		}

		if bytes.Equal(mainhash, mainblock.Hash) {
			break
		}
		mainblock = nil
	}

	// TODO: handle the case that can't find the hash in main chain
	if mainblock == nil {
		logger.Debug().Msg("Can't search same ancestor")
		return nil, nil
	}

	// 3. collect missing parts and reply them
	mainBlockNo := mainblock.GetHeader().GetBlockNo()
	var loop = stopBlock.GetHeader().GetBlockNo() - mainBlockNo
	logger.Debug().Uint64("mainBlockNo", mainBlockNo).Str("mainHash", enc.ToString(mainhash)).
		Uint64("stopBlockNo", stopBlock.GetHeader().GetBlockNo()).Str("stopHash", enc.ToString(stopBlock.Hash)).
		Msg("Get hashes of missing part")
	rhashes := make([]message.BlockHash, 0, loop)
	rnos := make([]types.BlockNo, 0, loop)
	for i := uint64(0); i < loop; i++ {
		tBlock, _ := cs.getBlockByNo(types.BlockNo(mainBlockNo + i))
		rhashes = append(rhashes, message.BlockHash(tBlock.Hash))
		rnos = append(rnos, types.BlockNo(tBlock.GetHeader().GetBlockNo()))
		logger.Debug().Uint64("blockNo", tBlock.GetHeader().GetBlockNo()).Str("hash", enc.ToString(tBlock.Hash)).
			Msg("append block for replying missing tree")
	}

	return rhashes, rnos
}

func (cs *ChainService) checkBlockHandshake(peerID peer.ID, bestHeight uint64, bestHash []byte) {
	myBestBlock, err := cs.getBestBlock()
	if err != nil {
		logger.Error().Err(err).Msg("Failed to get best block")
		return
	}
	sameBestHash := bytes.Equal(myBestBlock.Hash, bestHash)
	if sameBestHash {
		// two node has exact best block.
		// TODO: myBestBlock.GetHeader().BlockNo == bestHeight
		logger.Debug().Str("peer", peerID.Pretty()).Msg("peer is in sync status")
	} else if !sameBestHash && myBestBlock.GetHeader().BlockNo < bestHeight {
		cs.ChainSync(peerID)
	}

	return
}
