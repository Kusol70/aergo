/**
 *  @file
 *  @copyright defined in aergo/LICENSE.txt
 */

package types

import (
	"bytes"
	"encoding/binary"
	"io"
	"reflect"

	sha256 "github.com/minio/sha256-simd"

	"github.com/aergoio/aergo/internal/enc"
	crypto "github.com/libp2p/go-libp2p-crypto"
	peer "github.com/libp2p/go-libp2p-peer"
)

const (
	// DefaultMaxBlockSize is the maximum block size (currently 1MiB)
	DefaultMaxBlockSize = 1 << 20

	lastFieldOfBH = "Sign"
)

var lastIndexOfBH int

func init() {
	lastIndexOfBH = getLastIndexOfBH()
}

func getLastIndexOfBH() (lastIndex int) {
	v := reflect.ValueOf(BlockHeader{})

	nField := v.NumField()
	var i int
	for i = 0; i < nField; i++ {
		name := v.Type().Field(i).Name
		if name == lastFieldOfBH {
			lastIndex = i
			break
		}
	}

	return i
}

// Genesis represents genesis block
type Genesis struct {
	//Header    *BlockHeader      `json:"header"`
	Balance   map[string]*State `json:"alloc"`
	Timestamp int64             `json:"timestamp,omitempty"`
	// TODO: bp Peer info
	Block *Block `json:"-"`
}

// BlockNo is the height of a block, which starts from 0 (genesis block).
type BlockNo = uint64

// BlockNoToBytes represents to serialize block no to bytes
func BlockNoToBytes(bn BlockNo) []byte {
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, bn)
	return buf
}

// BlockNoFromBytes represents to deserialize bytes to block no
func BlockNoFromBytes(raw []byte) BlockNo {
	buf := binary.LittleEndian.Uint64(raw)
	return BlockNo(buf)
}

// NewBlock represents to create a block to store transactions.
func NewBlock(prevBlock *Block, txs []*Tx, ts int64) *Block {
	var prevBlockHash []byte
	var blockNo BlockNo

	if prevBlock != nil {
		prevBlockHash = prevBlock.Hash
		blockNo = prevBlock.Header.BlockNo + 1
	}

	body := BlockBody{
		Txs: txs,
	}
	header := BlockHeader{
		PrevBlockHash: prevBlockHash,
		BlockNo:       blockNo,
		Timestamp:     ts,
		//BlockRootHash: nil,
		//StateRootHash: nil,
	}
	block := Block{
		Header: &header,
		Body:   &body,
	}

	block.Header.TxsRootHash = CalculateTxsRootHash(body.Txs)

	return &block
}

// calculateBlockHash computes sha256 hash of block header.
func (block *Block) calculateBlockHash() []byte {
	digest := sha256.New()
	serializeBH(digest, block.Header)

	return digest.Sum(nil)
}

func serializeStruct(w io.Writer, s interface{}, stopIndex int) error {
	v := reflect.Indirect(reflect.ValueOf(s))

	var i int
	for i = 0; i <= stopIndex; i++ {
		if err := binary.Write(w, binary.LittleEndian, v.Field(i).Interface()); err != nil {
			return err
		}
	}

	return nil
}

func serializeBH(w io.Writer, bh *BlockHeader) error {
	return serializeStruct(w, bh, lastIndexOfBH)
}

func serializeBhForDigest(w io.Writer, bh *BlockHeader) error {
	return serializeStruct(w, bh, lastIndexOfBH-1)
}

func writeBlockHeaderOld(w io.Writer, bh *BlockHeader) error {
	for _, f := range []interface{}{
		bh.PrevBlockHash,
		bh.BlockNo,
		bh.Timestamp,
		bh.TxsRootHash,
		bh.Confirms,
		bh.PubKey,
		bh.Sign,
	} {
		if err := binary.Write(w, binary.LittleEndian, f); err != nil {
			return err
		}
	}

	return nil
}

// BlockHash returns block hash. It returns a calculated value if the hash is nil.
func (block *Block) BlockHash() []byte {
	hash := block.GetHash()
	if len(hash) == 0 {
		block.Hash = block.calculateBlockHash()
	}

	return block.GetHash()
}

// BlockID converts block hash ([]byte) to BlockID.
func (block *Block) BlockID() BlockID {
	return ToBlockID(block.BlockHash())
}

// PrevBlockID converts parent block hash ([]byte) to BlockID.
func (block *Block) PrevBlockID() BlockID {
	return ToBlockID(block.GetHeader().GetPrevBlockHash())
}

// Confirms returns block.Header.Confirms which indicates how many block is confirmed
// by block.
func (block *Block) Confirms() BlockNo {
	return block.GetHeader().GetConfirms()
}

// SetConfirms sets block.Header.Confirms to confirms.
func (block *Block) SetConfirms(confirms BlockNo) {
	block.Header.Confirms = confirms
}

// BlockNo returns the block number of block.
func (block *Block) BlockNo() BlockNo {
	return block.GetHeader().GetBlockNo()
}

// Sign adds a pubkey and a block signature to block.
func (block *Block) Sign(privKey crypto.PrivKey) error {
	var err error

	if err = block.setPubKey(privKey.GetPublic()); err != nil {
		return err
	}

	var msg []byte
	if msg, err = block.Header.bytesForDigest(); err != nil {
		return err
	}

	var sig []byte
	if sig, err = privKey.Sign(msg); err != nil {
		return err
	}
	block.Header.Sign = sig

	return nil
}

func (bh *BlockHeader) bytesForDigest() ([]byte, error) {
	var buf bytes.Buffer

	if err := serializeBhForDigest(&buf, bh); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// VerifySign verifies the signature of block.
func (block *Block) VerifySign() (valid bool, err error) {
	var pubKey crypto.PubKey
	if pubKey, err = crypto.UnmarshalPublicKey(block.Header.PubKey); err != nil {
		return false, err
	}

	var msg []byte
	if msg, err = block.Header.bytesForDigest(); err != nil {
		return false, err
	}

	if valid, err = pubKey.Verify(msg, block.Header.Sign); err != nil {
		return
	}

	return valid, nil
}

// BPID returns its Block Producer's ID from block.
func (block *Block) BPID() (id peer.ID, err error) {
	var pubKey crypto.PubKey
	if pubKey, err = crypto.UnmarshalPublicKey(block.Header.PubKey); err != nil {
		return peer.ID(""), err
	}

	if id, err = peer.IDFromPublicKey(pubKey); err != nil {
		return peer.ID(""), err
	}

	return
}

// BpID2Str returns its Block Producer's ID in base64 format.
func (block *Block) BPID2Str() string {
	id, err := block.BPID()
	if err != nil {
		return ""
	}

	return enc.ToString([]byte(id))
}

// ID returns the base64 encoded formated ID (hash) of block.
func (block *Block) ID() string {
	hash := block.BlockHash()
	if hash != nil {
		return enc.ToString(hash)
	}

	return ""

}

// PrevID returns the base64 encoded formated ID (hash) of the parent block.
func (block *Block) PrevID() string {
	hash := block.GetHeader().GetPrevBlockHash()
	if hash != nil {
		return enc.ToString(hash)
	}

	return ""

}

// SetPubKey sets block.Header.PubKey to pubkey.
func (block *Block) setPubKey(pubKey crypto.PubKey) error {
	var pk []byte
	var err error
	if pk, err = pubKey.Bytes(); err != nil {
		return err
	}
	block.Header.PubKey = pk

	return nil
}

// CalculateBlocksRootHash generates merkle tree of block headers and returns root hash.
func CalculateBlocksRootHash(blocks []*Block) []byte {
	return nil
}

// CalculateTxsRootHash generates merkle tree of transactions and returns root hash.
func CalculateTxsRootHash(txs []*Tx) []byte {
	return nil
}

func NewTx() *Tx {
	tx := &Tx{
		Body: &TxBody{
			Nonce: uint64(1),
		},
	}
	return tx
}

func (tx *Tx) CalculateTxHash() []byte {
	txBody := tx.Body
	digest := sha256.New()
	binary.Write(digest, binary.LittleEndian, txBody.Nonce)
	digest.Write(txBody.Account)
	digest.Write(txBody.Recipient)
	binary.Write(digest, binary.LittleEndian, txBody.Amount)
	digest.Write(txBody.Payload)
	binary.Write(digest, binary.LittleEndian, txBody.Limit)
	binary.Write(digest, binary.LittleEndian, txBody.Price)
	binary.Write(digest, binary.LittleEndian, txBody.Type)
	digest.Write(txBody.Sign)
	return digest.Sum(nil)
}

func (tx *Tx) Clone() *Tx {
	if tx == nil {
		return nil
	}
	if tx.Body == nil {
		return &Tx{}
	}
	body := &TxBody{
		Nonce:     tx.Body.Nonce,
		Account:   Clone(tx.Body.Account).([]byte),
		Recipient: Clone(tx.Body.Recipient).([]byte),
		Amount:    tx.Body.Amount,
		Payload:   Clone(tx.Body.Payload).([]byte),
		Limit:     tx.Body.Limit,
		Price:     tx.Body.Price,
		Sign:      Clone(tx.Body.Sign).([]byte),
		Type:      tx.Body.Type,
	}
	res := &Tx{
		Body: body,
	}
	res.Hash = tx.CalculateTxHash()
	return res
}
