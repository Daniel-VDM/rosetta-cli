// Copyright 2020 Coinbase, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package storage

import (
	"context"
	"fmt"

	"github.com/coinbase/rosetta-sdk-go/types"
)

var _ BlockWorker = (*BroadcastStorage)(nil)

const (
	transactionBroadcastNamespace = "transaction-broadcast"
)

func getBroadcastKey(transactionIdentifier *types.TransactionIdentifier) []byte {
	return []byte(
		fmt.Sprintf("%s/%s", transactionBroadcastNamespace, transactionIdentifier.Hash),
	)
}

// BroadcastStorage implements storage methods for managing
// transaction broadcast.
type BroadcastStorage struct {
	db      Database
	helper  BroadcastStorageHelper
	handler BroadcastStorageHandler

	confirmationDepth int64
	staleDepth        int64
}

// BroadcastStorageHelper is used by BroadcastStorage to submit transactions
// and find said transaction in blocks on-chain.
type BroadcastStorageHelper interface {
	// CurrentBlockIdentifier is called before transaction broadcast and is used
	// to determine if a transaction broadcast is stale.
	CurrentBlockIdentifier(context.Context) (*types.BlockIdentifier, error) // used to determine if should rebroadcast

	// FindTransaction looks for the provided TransactionIdentifier in processed
	// blocks and returns the depth since the most recent sighting.
	FindTransaction(context.Context, *types.TransactionIdentifier) (*types.BlockIdentifier, int64, error) // used to confirm

	// BroadcastTransaction broadcasts a transaction to a Rosetta implementation
	// and returns the *types.TransactionIdentifier returned by the implementation.
	BroadcastTransaction(context.Context, string) (*types.TransactionIdentifier, error) // handle initial broadcast + confirm matches provided + rebroadcast if stale
}

// BroadcastStorageHandler is invoked when a transaction is confirmed on-chain
// or when a transaction is considered stale.
type BroadcastStorageHandler interface {
	// TransactionConfirmed is called when a transaction is observed on-chain for the
	// last time at a block height < current block height - confirmationDepth.
	TransactionConfirmed(context.Context, *types.BlockIdentifier, *types.Transaction, []*types.Operation) error // can use locked account again + confirm matches intent + update logger

	// TransactionStale is called when a transaction has not yet been
	// seen on-chain and is considered stale. This occurs when
	// current block height - last broadcast > staleDepth.
	TransactionStale(context.Context, *types.TransactionIdentifier) error // log in counter (rebroadcast should occur here)
}

// broadcast is persisted to the db to track transaction broadcast.
type broadcast struct {
	Identifier    *types.TransactionIdentifier `json:"identifier"`
	Sender        string                       `json:"sender"`
	Intent        []*types.Operation           `json:"intent"`
	Payload       string                       `json:"payload"`
	LastBroadcast *types.BlockIdentifier       `json:"broadcast_at"`
}

// NewBroadcastStorage returns a new BroadcastStorage.
func NewBroadcastStorage(
	db Database,
	confirmationDepth int64,
	staleDepth int64,
) *BroadcastStorage {
	return &BroadcastStorage{
		db:                db,
		confirmationDepth: confirmationDepth,
		staleDepth:        staleDepth,
	}
}

// Initialize adds a BroadcastStorageHelper and BroadcastStorageHandler to BroadcastStorage.
// This must be called prior to syncing!
func (b *BroadcastStorage) Initialize(helper BroadcastStorageHelper, handler BroadcastStorageHandler) {
	b.helper = helper
	b.handler = handler
}

// AddingBlock is called by BlockStorage when adding a block.
func (b *BroadcastStorage) AddingBlock(
	ctx context.Context,
	block *types.Block,
	transaction DatabaseTransaction,
) (CommitWorker, error) {
	// TODO: call handler -> transactionRebroadcast should not block processing (could be in CommitWorker)
	// TODO: on each added block commit worker, attempt to broadcast all txs with no last identifier
	return nil, nil
}

// RemovingBlock is called by BlockStorage when removing a block.
// TODO: error if transaction removed after confirmed (means confirmation depth not deep enough)
func (b *BroadcastStorage) RemovingBlock(
	ctx context.Context,
	block *types.Block,
	transaction DatabaseTransaction,
) (CommitWorker, error) {
	return nil, nil
}

// Broadcast is called when a caller wants a transaction to be broadcast and tracked.
// The caller SHOULD NOT broadcast the transaction before calling this function.
func (b *BroadcastStorage) Broadcast(
	ctx context.Context,
	sender string,
	intent []*types.Operation,
	transactionIdentifier *types.TransactionIdentifier,
	payload string,
) error {
	txn := b.db.NewDatabaseTransaction(ctx, true)
	defer txn.Discard(ctx)

	broadcastKey := getBroadcastKey(transactionIdentifier)

	exists, _, err := txn.Get(ctx, broadcastKey)
	if err != nil {
		return fmt.Errorf("%w: unable to determine if already broadcasting transaction", err)
	}

	if exists {
		return fmt.Errorf("already broadcasting transaction %s", transactionIdentifier.Hash)
	}

	bytes, err := encode(&broadcast{
		Identifier: transactionIdentifier,
		Sender:     sender,
		Intent:     intent,
		Payload:    payload,
	})
	if err != nil {
		return fmt.Errorf("%w: unable to encode broadcast", err)
	}

	if err := txn.Set(ctx, broadcastKey, bytes); err != nil {
		return fmt.Errorf("%w: unable to set broadcast", err)
	}

	if err := txn.Commit(ctx); err != nil {
		return fmt.Errorf("%w: unable to commit broadcast", err)
	}

	return nil
}

func (b *BroadcastStorage) getAllBroadcasts(ctx context.Context) ([]*broadcast, error) {
	rawBroadcasts, err := b.db.Scan(ctx, []byte(transactionBroadcastNamespace))
	if err != nil {
		return nil, fmt.Errorf("%w: unable to scan for all broadcasts", err)
	}

	broadcasts := make([]*broadcast, len(rawBroadcasts))
	for i, rawBroadcast := range rawBroadcasts {
		var b broadcast
		if err := decode(rawBroadcast, &b); err != nil {
			return nil, fmt.Errorf("%w: unable to decode broadcast", err)
		}

		broadcasts[i] = &b
	}

	return broadcasts, nil
}

func (b *BroadcastStorage) broadcastPending(ctx context.Context) error {
	broadcasts, err := b.getAllBroadcasts(ctx)
	if err != nil {
		return fmt.Errorf("%w: unable to get all broadcasts", err)
	}

	for _, broadcast := range broadcasts {
		if broadcast.LastBroadcast != nil { // when a transaction should be broadcast, its last broadcast field should be set to nil
			continue
		}

		broadcastIdentifier, err := b.helper.BroadcastTransaction(ctx, broadcast.Payload)
		if err != nil {
			return fmt.Errorf("%w: unable to broadcast transaction %s", err, broadcast.Identifier.Hash)
		}

		if types.Hash(broadcastIdentifier) != types.Hash(broadcast.Identifier) {
			return fmt.Errorf("transaction hash returned by broadcast %s does not match expected %s", broadcastIdentifier.Hash, broadcast.Identifier.Hash)
		}
	}

	return nil
}

// LockedAddresses returns all addresses currently broadcasting a transaction.
// The caller SHOULD NOT broadcast a transaction from an account if it is
// considered locked!
func (b *BroadcastStorage) LockedAddresses(ctx context.Context) ([]string, error) {
	broadcasts, err := b.getAllBroadcasts(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: unable to get all broadcasts", err)
	}

	addresses := make([]string, len(broadcasts))
	for i, broadcast := range broadcasts {
		addresses[i] = broadcast.Sender
	}

	return addresses, nil
}
