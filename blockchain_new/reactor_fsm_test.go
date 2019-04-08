package blockchain_new

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	cmn "github.com/tendermint/tendermint/libs/common"
	"github.com/tendermint/tendermint/libs/log"
	"github.com/tendermint/tendermint/p2p"
	"github.com/tendermint/tendermint/types"
)

var (
	failSendStatusRequest bool
	failSendBlockRequest  bool
	numStatusRequests     int
	numBlockRequests      int32
)

type lastBlockRequestT struct {
	peerID p2p.ID
	height int64
}

var lastBlockRequest lastBlockRequestT

type lastPeerErrorT struct {
	peerID p2p.ID
	err    error
}

var lastPeerError lastPeerErrorT

var stateTimerStarts map[string]int

func resetTestValues() {
	stateTimerStarts = make(map[string]int)
	failSendBlockRequest = false
	failSendStatusRequest = false
	numStatusRequests = 0
	numBlockRequests = 0
	lastBlockRequest.peerID = ""
	lastBlockRequest.height = 0
	lastPeerError.peerID = ""
	lastPeerError.err = nil
}

type addedBlock struct {
	peerId p2p.ID
	height int64
}

type fsmStepTestValues struct {
	startingHeight int64
	currentState   string
	event          bReactorEvent
	data           bReactorEventData
	errWanted      error
	expectedState  string

	failStatusReq       bool
	shouldSendStatusReq bool

	failBlockReq      bool
	blockReqIncreased bool
	blocksAdded       []addedBlock

	expectedLastBlockReq *lastBlockRequestT
}

func newTestReactor(height int64) *testReactor {
	testBcR := &testReactor{logger: log.TestingLogger()}
	testBcR.fsm = NewFSM(height, testBcR)
	testBcR.fsm.setLogger(testBcR.logger)
	return testBcR
}

// WIP
func TestFSMBasicTransitionSequences(t *testing.T) {
	txs := []types.Tx{types.Tx("foo"), types.Tx("bar")}

	tests := []struct {
		name               string
		startingHeight     int64
		maxRequestsPerPeer int32
		steps              []fsmStepTestValues
	}{
		{
			name:               "one block fast sync",
			startingHeight:     1,
			maxRequestsPerPeer: 2,
			steps: []fsmStepTestValues{
				{
					currentState: "unknown", event: startFSMEv,
					expectedState:       "waitForPeer",
					shouldSendStatusReq: true},
				{
					currentState: "waitForPeer", event: statusResponseEv,
					data:          bReactorEventData{peerId: "P1", height: 2},
					expectedState: "waitForBlock"},
				{
					currentState: "waitForBlock", event: makeRequestsEv,
					data:              bReactorEventData{maxNumRequests: maxNumPendingRequests},
					expectedState:     "waitForBlock",
					blockReqIncreased: true,
				},
				{
					currentState: "waitForBlock", event: blockResponseEv,
					data: bReactorEventData{
						peerId: "P1",
						block:  types.MakeBlock(int64(1), txs, nil, nil)},
					expectedState: "waitForBlock",
					blocksAdded:   []addedBlock{{"P1", 1}},
				},
				{
					currentState: "waitForBlock", event: blockResponseEv,
					data: bReactorEventData{
						peerId: "P1",
						block:  types.MakeBlock(int64(2), txs, nil, nil)},
					expectedState: "waitForBlock",
					blocksAdded:   []addedBlock{{"P1", 1}, {"P1", 2}},
				},
				{
					currentState: "waitForBlock", event: processedBlockEv,
					data:          bReactorEventData{err: nil},
					expectedState: "finished",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test reactor
			testBcR := newTestReactor(tt.startingHeight)
			resetTestValues()

			if tt.maxRequestsPerPeer != 0 {
				maxRequestsPerPeer = tt.maxRequestsPerPeer
			}

			for _, step := range tt.steps {
				assert.Equal(t, step.currentState, testBcR.fsm.state.name)
				failSendStatusRequest = step.failStatusReq
				failSendBlockRequest = step.failBlockReq

				oldNumStatusRequests := numStatusRequests
				oldNumBlockRequests := numBlockRequests

				fsmErr := sendEventToFSM(testBcR.fsm, step.event, step.data)
				assert.Equal(t, step.errWanted, fsmErr)

				if step.shouldSendStatusReq {
					assert.Equal(t, oldNumStatusRequests+1, numStatusRequests)
				} else {
					assert.Equal(t, oldNumStatusRequests, numStatusRequests)
				}

				if step.blockReqIncreased {
					assert.True(t, oldNumBlockRequests < numBlockRequests)
				} else {
					assert.Equal(t, oldNumBlockRequests, numBlockRequests)
				}

				for _, block := range step.blocksAdded {
					_, err := testBcR.fsm.pool.getBlockAndPeerAtHeight(block.height)
					assert.Nil(t, err)
				}
				assert.Equal(t, step.expectedState, testBcR.fsm.state.name)
			}

		})
	}
}

func TestReactorFSMBasic(t *testing.T) {
	maxRequestsPerPeer = 2

	// Create test reactor
	testBcR := newTestReactor(1)
	resetTestValues()
	fsm := testBcR.fsm

	if err := fsm.handle(&bReactorMessageData{event: startFSMEv}); err != nil {
	}

	// Check that FSM sends a status request message
	assert.Equal(t, 1, numStatusRequests)
	assert.Equal(t, waitForPeer.name, fsm.state.name)

	// Send a status response message to FSM
	peerID := p2p.ID(cmn.RandStr(12))
	sendStatusResponse2(fsm, peerID, 10)

	if err := fsm.handle(&bReactorMessageData{
		event: makeRequestsEv,
		data:  bReactorEventData{maxNumRequests: maxNumPendingRequests}}); err != nil {
	}
	// Check that FSM sends a block request message and...
	assert.Equal(t, maxRequestsPerPeer, numBlockRequests)
	// ... the block request has the expected height
	assert.Equal(t, maxRequestsPerPeer, int32(len(fsm.pool.peers[peerID].blocks)))
	assert.Equal(t, waitForBlock.name, fsm.state.name)
}

func TestReactorFSMPeerTimeout(t *testing.T) {
	maxRequestsPerPeer = 2
	resetTestValues()
	peerTimeout = 20 * time.Millisecond
	// Create and start the FSM
	testBcR := newTestReactor(1)
	fsm := testBcR.fsm
	fsm.start()

	// Check that FSM sends a status request message
	time.Sleep(time.Millisecond)
	assert.Equal(t, 1, numStatusRequests)

	// Send a status response message to FSM
	peerID := p2p.ID(cmn.RandStr(12))
	sendStatusResponse(fsm, peerID, 10)
	time.Sleep(5 * time.Millisecond)

	if err := fsm.handle(&bReactorMessageData{
		event: makeRequestsEv,
		data:  bReactorEventData{maxNumRequests: maxNumPendingRequests}}); err != nil {
	}
	// Check that FSM sends a block request message and...
	assert.Equal(t, maxRequestsPerPeer, numBlockRequests)
	// ... the block request has the expected height and peer
	assert.Equal(t, maxRequestsPerPeer, int32(len(fsm.pool.peers[peerID].blocks)))
	assert.Equal(t, peerID, lastBlockRequest.peerID)

	// let FSM timeout on the block response message
	time.Sleep(100 * time.Millisecond)

}

// reactor for FSM testing
type testReactor struct {
	logger log.Logger
	fsm    *bReactorFSM
	testCh chan bReactorMessageData
}

func sendEventToFSM(fsm *bReactorFSM, ev bReactorEvent, data bReactorEventData) error {
	return fsm.handle(&bReactorMessageData{event: ev, data: data})
}

// ----------------------------------------
// implementation for the test reactor APIs

func (testR *testReactor) sendPeerError(err error, peerID p2p.ID) {
	testR.logger.Info("Reactor received sendPeerError call from FSM", "peer", peerID, "err", err)
	lastPeerError.peerID = peerID
	lastPeerError.err = err
}

func (testR *testReactor) sendStatusRequest() {
	testR.logger.Info("Reactor received sendStatusRequest call from FSM")
	numStatusRequests++
}

func (testR *testReactor) sendBlockRequest(peerID p2p.ID, height int64) error {
	testR.logger.Info("Reactor received sendBlockRequest call from FSM", "peer", peerID, "height", height)
	numBlockRequests++
	lastBlockRequest.peerID = peerID
	lastBlockRequest.height = height
	return nil
}

func (testR *testReactor) resetStateTimer(name string, timer *time.Timer, timeout time.Duration, f func()) {
	testR.logger.Info("Reactor received resetStateTimer call from FSM", "state", name, "timeout", timeout)
	if _, ok := stateTimerStarts[name]; !ok {
		stateTimerStarts[name] = 1
	} else {
		stateTimerStarts[name]++
	}
}

func (testR *testReactor) switchToConsensus() {
	testR.logger.Info("Reactor received switchToConsensus call from FSM")

}

// ----------------------------------------

// -------------------------------------------------------
// helper functions for tests to simulate different events
func sendStatusResponse(fsm *bReactorFSM, peerID p2p.ID, height int64) {
	msgBytes := makeStatusResponseMessage(height)
	msgData := bReactorMessageData{
		event: statusResponseEv,
		data: bReactorEventData{
			peerId: peerID,
			height: height,
			length: len(msgBytes),
		},
	}

	_ = sendMessageToFSMSync(fsm, msgData)
}

func sendStatusResponse2(fsm *bReactorFSM, peerID p2p.ID, height int64) {
	msgBytes := makeStatusResponseMessage(height)
	msgData := &bReactorMessageData{
		event: statusResponseEv,
		data: bReactorEventData{
			peerId: peerID,
			height: height,
			length: len(msgBytes),
		},
	}
	_ = fsm.handle(msgData)
}

func sendStateTimeout(fsm *bReactorFSM, name string) {
	msgData := &bReactorMessageData{
		event: stateTimeoutEv,
		data: bReactorEventData{
			stateName: name,
		},
	}
	_ = fsm.handle(msgData)
}

// -------------------------------------------------------

// ----------------------------------------------------
// helper functions to make blockchain reactor messages
func makeStatusResponseMessage(height int64) []byte {
	msgBytes := cdc.MustMarshalBinaryBare(&bcStatusResponseMessage{height})
	return msgBytes
}

// ----------------------------------------------------
