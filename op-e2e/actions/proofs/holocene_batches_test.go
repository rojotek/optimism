package proofs

import (
	"fmt"
	"testing"

	actionsHelpers "github.com/ethereum-optimism/optimism/op-e2e/actions/helpers"
	"github.com/ethereum-optimism/optimism/op-e2e/actions/proofs/helpers"
	"github.com/ethereum-optimism/optimism/op-program/client/claim"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
)

func Test_ProgramAction_HoloceneBatches(gt *testing.T) {
	type testCase struct {
		name        string
		blocks      []uint // blocks is an ordered list of blocks (by number) to add to a single channel.
		isSpanBatch bool
		holoceneExpectations
	}

	// Depending on the blocks list,  we expect a different
	// progression of the safe head under Holocene
	// derivation rules, compared with pre Holocene.
	testCases := []testCase{
		// Standard channel composition
		{
			name: "case-0", blocks: []uint{1, 2, 3},
			holoceneExpectations: holoceneExpectations{
				safeHeadPreHolocene: 3,
				safeHeadHolocene:    3,
			},
		},

		// Non-standard channel composition
		{
			name: "case-2a", blocks: []uint{1, 3, 2},
			holoceneExpectations: holoceneExpectations{
				safeHeadPreHolocene: 3, // batches are buffered, so the block ordering does not matter
				safeHeadHolocene:    1, // batch for block 3 is considered invalid because it is from the future. This batch + remaining channel is dropped.
			},
		},
		{
			name: "case-2b", blocks: []uint{2, 1, 3},
			holoceneExpectations: holoceneExpectations{
				safeHeadPreHolocene: 3, // batches are buffered, so the block ordering does not matter
				safeHeadHolocene:    0, // batch for block 2 is considered invalid because it is from the future. This batch + remaining channel is dropped.
			},
		},

		{
			name: "case-2c", blocks: []uint{1, 1, 2, 3},
			holoceneExpectations: holoceneExpectations{
				safeHeadPreHolocene: 3, // duplicate batches are silently dropped, so this reduceds to case-0
				safeHeadHolocene:    3, // duplicate batches are silently dropped
			},
		},
		{
			name: "case-2d", blocks: []uint{2, 2, 1, 3},
			holoceneExpectations: holoceneExpectations{
				safeHeadPreHolocene: 3, // duplicate batches are silently dropped, so this reduces to case-2b
				safeHeadHolocene:    0, // duplicate batches are silently dropped, so this reduces to case-2b
			},
		},
	}

	runHoloceneDerivationTest := func(gt *testing.T, testCfg *helpers.TestCfg[testCase]) {
		t := actionsHelpers.NewDefaultTesting(gt)
		env := helpers.NewL2FaultProofEnv(t, testCfg, helpers.NewTestParams(), helpers.NewBatcherCfg())

		includeBatchTx := func() {
			// Include the last transaction submitted by the batcher.
			env.Miner.ActL1StartBlock(12)(t)
			env.Miner.ActL1IncludeTxByHash(env.Batcher.LastSubmitted.Hash())(t)
			env.Miner.ActL1EndBlock(t)
		}

		max := func(input []uint) uint {
			max := uint(0)
			for _, val := range input {
				if val > max {
					max = val
				}
			}
			return max
		}

		targetHeadNumber := max(testCfg.Custom.blocks)
		for env.Engine.L2Chain().CurrentBlock().Number.Uint64() < uint64(targetHeadNumber) {
			env.Sequencer.ActL2StartBlock(t)
			// Send an L2 tx
			env.Alice.L2.ActResetTxOpts(t)
			env.Alice.L2.ActSetTxToAddr(&env.Dp.Addresses.Bob)
			env.Alice.L2.ActMakeTx(t)
			env.Engine.ActL2IncludeTx(env.Alice.Address())(t)
			env.Sequencer.ActL2EndBlock(t)
		}

		// Buffer the blocks in the batcher.
		env.Batcher.ActCreateChannel(t, testCfg.Custom.isSpanBatch)
		for _, blockNum := range testCfg.Custom.blocks {
			env.Batcher.ActAddBlockByNumber(t, int64(blockNum), actionsHelpers.BlockLogger(t))
		}
		env.Batcher.ActL2ChannelClose(t)
		frame := env.Batcher.ReadNextOutputFrame(t)
		require.NotEmpty(t, frame)
		env.Batcher.ActL2BatchSubmitRaw(t, frame)
		includeBatchTx()

		// Instruct the sequencer to derive the L2 chain from the data on L1 that the batcher just posted.
		env.Sequencer.ActL1HeadSignal(t)
		env.Sequencer.ActL2PipelineFull(t)

		l2SafeHead := env.Sequencer.L2Safe()
		testCfg.Custom.RequireExpectedProgress(t, l2SafeHead, testCfg.Hardfork.Precedence < helpers.Holocene.Precedence, env.Engine)

		t.Log("Safe head progressed as expected", "l2SafeHeadNumber", l2SafeHead.Number)

		env.RunFaultProofProgram(t, l2SafeHead.Number, testCfg.CheckResult, testCfg.InputParams...)
	}

	matrix := helpers.NewMatrix[testCase]()
	defer matrix.Run(gt)

	for _, ordering := range testCases {
		matrix.AddTestCase(
			fmt.Sprintf("HonestClaim-%s", ordering.name),
			ordering,
			helpers.NewForkMatrix(helpers.Granite, helpers.LatestFork),
			runHoloceneDerivationTest,
			helpers.ExpectNoError(),
		)
		matrix.AddTestCase(
			fmt.Sprintf("JunkClaim-%s", ordering.name),
			ordering,
			helpers.NewForkMatrix(helpers.Granite, helpers.LatestFork),
			runHoloceneDerivationTest,
			helpers.ExpectError(claim.ErrClaimNotValid),
			helpers.WithL2Claim(common.HexToHash("0xdeadbeef")),
		)
	}
}