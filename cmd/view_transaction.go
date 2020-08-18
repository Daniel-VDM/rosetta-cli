package cmd

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/coinbase/rosetta-cli/pkg/utils"

	"github.com/coinbase/rosetta-sdk-go/fetcher"
	"github.com/coinbase/rosetta-sdk-go/parser"
	"github.com/coinbase/rosetta-sdk-go/types"
	"github.com/spf13/cobra"
)

var (
	viewTransactionCmd = &cobra.Command{
		Use:   "view:transaction",
		Short: "View a transaction. Args: <block_identifier> <transaction_identifier>",
		Long: `Used to test the /block/transaction endpoint.
`,
		Run:  runViewTransactionCmd,
		Args: cobra.ExactArgs(2),
	}
)

func runViewTransactionCmd(cmd *cobra.Command, args []string) {
	ctx := context.Background()
	index, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		log.Fatal(fmt.Errorf("%w: unable to parse index %s", err, args[0]))
	}
	txHash := args[1]

	// Create a new fetcher
	newFetcher := fetcher.New(
		Config.OnlineURL,
		fetcher.WithRetryElapsedTime(time.Duration(Config.RetryElapsedTime)*time.Second),
		fetcher.WithTimeout(time.Duration(Config.HTTPTimeout)*time.Second),
	)

	// Initialize the fetcher's asserter
	//
	// Behind the scenes this makes a call to get the
	// network status and uses the response to inform
	// the asserter what are valid responses.
	_, _, err = newFetcher.InitializeAsserter(ctx)
	if err != nil {
		log.Fatal(err)
	}

	_, err = utils.CheckNetworkSupported(ctx, Config.Network, newFetcher)
	if err != nil {
		log.Fatalf("%s: unable to confirm network is supported", err.Error())
	}

	// Fetch the specified block with retries (automatically
	// asserted for correctness)
	//
	// On another note, notice that fetcher.BlockRetry
	// automatically fetches all transactions that are
	// returned in BlockResponse.OtherTransactions. If you use
	// the client directly, you will need to implement a mechanism
	// to fully populate the block by fetching all these
	// transactions.
	block, err := newFetcher.BlockRetry(
		ctx,
		Config.Network,
		&types.PartialBlockIdentifier{
			Index: &index,
		},
	)
	if err != nil {
		log.Fatal(fmt.Errorf("%w: unable to fetch block", err))
	}

	txs, err := newFetcher.UnsafeTransactions(
		ctx,
		Config.Network,
		block.BlockIdentifier,
		[]*types.TransactionIdentifier{
			{
				Hash: txHash,
			},
		},
	)

	if len(txs) != 1 {
		log.Fatal("Block contains 0 or more than 2 transactions with given identifier")
	}

	log.Printf(types.PrettyPrintStruct(parser.GroupOperations(txs[0])))
}
