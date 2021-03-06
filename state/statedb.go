/**
 *  @file
 *  @copyright defined in aergo/LICENSE.txt
 */

package state

import (
	"bytes"
	"fmt"
	"os"
	"path"
	"sort"
	"sync"

	"github.com/aergoio/aergo-lib/db"
	"github.com/aergoio/aergo-lib/log"
	"github.com/aergoio/aergo/pkg/trie"
	"github.com/aergoio/aergo/types"
	"github.com/golang/protobuf/proto"
)

const (
	stateName     = "state"
	stateAccounts = stateName + ".accounts"
	stateLatest   = stateName + ".latest"
)

var (
	logger = log.NewLogger("state")
)

var (
	emptyHashID    = types.HashID{}
	emptyBlockID   = types.BlockID{}
	emptyAccountID = types.AccountID{}
)

type ChainStateDB struct {
	sync.RWMutex
	accounts map[types.AccountID]*types.State
	trie     *trie.Trie
	latest   *types.BlockInfo
	statedb  *db.DB
}

func NewStateDB() *ChainStateDB {
	return &ChainStateDB{
		accounts: make(map[types.AccountID]*types.State),
	}
}

func InitDB(basePath, dbName string) *db.DB {
	dbPath := path.Join(basePath, dbName)
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		_ = os.MkdirAll(dbPath, 0711)
	}
	dbInst := db.NewDB(db.BadgerImpl, dbPath)
	return &dbInst
}

func (sdb *ChainStateDB) Init(dataDir string) error {
	sdb.Lock()
	defer sdb.Unlock()

	// init db
	if sdb.statedb == nil {
		sdb.statedb = InitDB(dataDir, stateName)
	}

	// init trie
	sdb.trie = trie.NewTrie(32, types.TrieHasher, *sdb.statedb)

	// load data from db
	err := sdb.loadStateDB()
	return err
}

func (sdb *ChainStateDB) Close() error {
	sdb.Lock()
	defer sdb.Unlock()

	// save data to db
	err := sdb.saveStateDB()
	if err != nil {
		return err
	}

	// close db
	if sdb.statedb != nil {
		(*sdb.statedb).Close()
	}
	return nil
}

func (sdb *ChainStateDB) SetGenesis(genesisBlock *types.Genesis) error {
	block := genesisBlock.Block
	gbInfo := &types.BlockInfo{
		BlockNo:   0,
		BlockHash: types.ToBlockID(block.Hash),
	}
	sdb.latest = gbInfo

	// create state of genesis block
	gbState := types.NewBlockState(gbInfo)
	for address, balance := range genesisBlock.Balance {
		bytes := types.ToAddress(address)
		id := types.ToAccountID(bytes)
		gbState.PutAccount(id, nil, balance)
	}
	// save state of genesis block
	err := sdb.apply(gbState)
	return err
}

func (sdb *ChainStateDB) getAccountState(aid types.AccountID) (*types.State, error) {
	if aid == emptyAccountID {
		return nil, fmt.Errorf("Failed to get block account: invalid account id")
	}
	if state, ok := sdb.accounts[aid]; ok {
		return state, nil
	}
	state := types.NewState()
	sdb.accounts[aid] = state
	return state, nil
}
func (sdb *ChainStateDB) GetAccountStateClone(aid types.AccountID) (*types.State, error) {
	state, err := sdb.getAccountState(aid)
	if err != nil {
		return nil, err
	}
	res := types.Clone(*state).(types.State)
	return &res, nil
}
func (sdb *ChainStateDB) getBlockAccount(bs *types.BlockState, aid types.AccountID) (*types.State, error) {
	if aid == emptyAccountID {
		return nil, fmt.Errorf("Failed to get block account: invalid account id")
	}

	if prev, ok := bs.GetAccount(aid); ok {
		return prev, nil
	}
	return sdb.getAccountState(aid)
}
func (sdb *ChainStateDB) GetBlockAccountClone(bs *types.BlockState, aid types.AccountID) (*types.State, error) {
	state, err := sdb.getBlockAccount(bs, aid)
	if err != nil {
		return nil, err
	}
	res := types.Clone(*state).(types.State)
	return &res, nil
}

func (sdb *ChainStateDB) updateTrie(bstate *types.BlockState) error {
	accounts := bstate.GetAccountStates()
	size := len(accounts)
	if size <= 0 {
		// do nothing
		return nil
	}
	accs := make([]types.AccountID, 0, size)
	for k := range accounts {
		accs = append(accs, k)
	}
	sort.Slice(accs, func(i, j int) bool {
		return bytes.Compare(accs[i][:], accs[j][:]) == -1
	})
	keys := make(trie.DataArray, size)
	vals := make(trie.DataArray, size)
	var err error
	for i, v := range accs {
		keys[i] = accs[i][:]
		vals[i], err = proto.Marshal(accounts[v])
		if err != nil {
			return err
		}
	}
	_, err = sdb.trie.Update(keys, vals)
	if err != nil {
		return err
	}
	sdb.trie.Commit()
	return nil
}

func (sdb *ChainStateDB) revertTrie(prevBlockStateRoot types.HashID) error {
	if bytes.Equal(sdb.trie.Root, prevBlockStateRoot[:]) {
		// same root, do nothing
		return nil
	}
	err := sdb.trie.Revert(prevBlockStateRoot[:])
	if err != nil {
		// FIXME: is that enough?
		// if prevRoot is not contained in the cached tries.
		sdb.trie.Root = prevBlockStateRoot[:]
		err = sdb.trie.LoadCache(sdb.trie.Root)
		return err
	}
	return nil
}

func (sdb *ChainStateDB) Apply(bstate *types.BlockState) error {
	if sdb.latest.BlockNo+1 != bstate.BlockNo {
		return fmt.Errorf("Failed to apply: invalid block no - latest=%v, this=%v", sdb.latest.BlockNo, bstate.BlockNo)
	}
	if sdb.latest.BlockHash != bstate.PrevHash {
		return fmt.Errorf("Failed to apply: invalid previous block latest=%v, bstate=%v",
			sdb.latest.BlockHash, bstate.PrevHash)
	}
	return sdb.apply(bstate)
}

func (sdb *ChainStateDB) apply(bstate *types.BlockState) error {
	sdb.Lock()
	defer sdb.Unlock()

	// rollback and revert trie requires state root before apply
	if bstate.Undo.StateRoot == emptyHashID {
		bstate.Undo.StateRoot = types.ToHashID(sdb.trie.Root)
	}

	// save blockState
	sdb.saveBlockState(bstate)

	// apply blockState to statedb
	accounts := bstate.GetAccountStates()
	for k, v := range accounts {
		sdb.accounts[k] = v
	}
	// apply blockState to trie
	err := sdb.updateTrie(bstate)
	if err != nil {
		return err
	}
	// logger.Debugf("- trie.root: %v", base64.StdEncoding.EncodeToString(sdb.GetHash()))
	sdb.latest = &bstate.BlockInfo
	err = sdb.saveStateDB()
	return err
}

func (sdb *ChainStateDB) Rollback(blockNo types.BlockNo) error {
	if sdb.latest.BlockNo <= blockNo {
		return fmt.Errorf("Failed to rollback: invalid block no")
	}
	sdb.Lock()
	defer sdb.Unlock()

	target := sdb.latest
	for target.BlockNo >= blockNo {
		bs, err := sdb.loadBlockState(target.BlockHash)
		if err != nil {
			return err
		}
		sdb.latest = &bs.BlockInfo

		if target.BlockNo == blockNo {
			break
		}

		for k, v := range bs.Undo.Accounts {
			sdb.accounts[k] = v
		}
		err = sdb.revertTrie(bs.Undo.StateRoot)
		if err != nil {
			return err
		}
		// logger.Debugf("- trie.root: %v", base64.StdEncoding.EncodeToString(sdb.GetHash()))

		target = &types.BlockInfo{
			BlockNo:   sdb.latest.BlockNo - 1,
			BlockHash: sdb.latest.PrevHash,
		}
	}
	err := sdb.saveStateDB()
	return err
}

func (sdb *ChainStateDB) GetHash() []byte {
	return sdb.trie.Root
}
