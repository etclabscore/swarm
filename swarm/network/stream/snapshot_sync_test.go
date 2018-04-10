// Copyright 2018 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.
package stream

import (
	"context"
	crand "crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"math/rand"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/discover"
	"github.com/ethereum/go-ethereum/p2p/simulations"
	"github.com/ethereum/go-ethereum/p2p/simulations/adapters"
	"github.com/ethereum/go-ethereum/pot"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/ethereum/go-ethereum/swarm/network"
	streamTesting "github.com/ethereum/go-ethereum/swarm/network/stream/testing"
	"github.com/ethereum/go-ethereum/swarm/storage"
)

const testMinProxBinSize = 2
const MAX_TIMEOUT = 600

var (
	pof = pot.DefaultPof(256)

	conf      *synctestConfig
	startTime time.Time
	ids       []discover.NodeID
	datadirs  map[discover.NodeID]string
	ppmap     map[string]*network.PeerPot

	globalWg sync.WaitGroup

	live    bool
	history bool

	longrunning = flag.Bool("longrunning", false, "do run long-running tests")
)

type synctestConfig struct {
	addrs            [][]byte
	chunks           []storage.Key
	idToChunksMap    map[discover.NodeID][]int
	chunksToNodesMap map[string][]int
	idToAddrMap      map[discover.NodeID][]byte
	addrToIdMap      map[string]discover.NodeID
}

func init() {
	rand.Seed(time.Now().Unix())
}

//common_test needs to initialize the test in a init() func
//in order for adapters to register the NewStreamerService;
//this service is dependent on some global variables
//we thus need to initialize first as init() as well.
func initSyncTest() {
	//assign the toAddr func so NewStreamerService can build the addr
	toAddr = func(id discover.NodeID) *network.BzzAddr {
		addr := network.NewAddrFromNodeID(id)
		return addr
	}

	createStoreFunc = createTestLocalStorageForId
	//local stores
	stores = make(map[discover.NodeID]storage.ChunkStore)
	//data directories for each node and store
	datadirs = make(map[discover.NodeID]string)
	//deliveries for each node
	deliveries = make(map[discover.NodeID]*Delivery)
	//registries, map of discover.NodeID to its streamer
	registries = make(map[discover.NodeID]*TestRegistry)
	//channel to wait for peers connected
	//not needed for this test but required from common_test for NewStreamService
	waitPeerErrC = make(chan error)
	//also not needed for this test but required for NewStreamService
	peerCount = func(id discover.NodeID) int {
		if ids[0] == id || ids[len(ids)-1] == id {
			return 1
		}
		return 2
	}
}

//This file executes a number of tests with the syntax
//TestSyncing_x_y
//x is the number of chunks which will be uploaded
//y is the number of nodes for the test
func TestSyncing_4_32(t *testing.T)  { testSyncing(t, 4, 32) }
func TestSyncing_32_16(t *testing.T) { testSyncing(t, 32, 16) }

func TestLongRunningSyncing(t *testing.T) {
	if *longrunning {
		chnkCnt := []int{1, 8, 32, 256, 1024}
		nCnt := []int{16, 32, 64, 128, 256}

		for _, chnk := range chnkCnt {
			for _, n := range nCnt {
				log.Info(fmt.Sprintf("Long running test with %d chunks and %d nodes...", chnk, n))
				testSyncing(t, chnk, n)
			}
		}
	}
}

//do run the tests
func testSyncing(t *testing.T, chunkCount int, nodeCount int) {
	initSyncTest()
	ids = make([]discover.NodeID, nodeCount)

	//test live and NO history
	log.Info("Testing live and no history")
	live = true
	history = false
	err := runSyncTest(chunkCount, nodeCount, live, history)
	if err != nil {
		t.Fatal(err)
	}
	//test history only
	log.Info("Testing history only")
	live = false
	history = true
	err = runSyncTest(chunkCount, nodeCount, live, history)
	if err != nil {
		t.Fatal(err)
	}
	//finally test live and history
	log.Info("Testing live and history")
	live = true
	err = runSyncTest(chunkCount, nodeCount, live, history)
	if err != nil {
		t.Fatal(err)
	}
}

/*
The test generates the given number of chunks,
then uploads these to a random node.
Afterwards for every chunk generated, the nearest node addresses
are identified, syncing is started, and finally we verify
that the nodes closer to the chunk addresses actually do have
the chunks in their local stores.

The test loads a snapshot file to construct the swarm network,
assuming that the snapshot file identifies a healthy
kademlia network. The snapshot should have 'streamer' in its service list.

For every test run, a series of three tests will be executed:
- a LIVE test first, where first subscriptions are established,
  then a file (random chunks) is uploaded
- a HISTORY test, where the file is uploaded first, and then
  the subscriptions are established
- a crude LIVE AND HISTORY test last, where (different) chunks
  are uploaded twice, once before and once after subscriptions
*/
func runSyncTest(chunkCount int, nodeCount int, live bool, history bool) error {
	//initialize the test struct
	conf = &synctestConfig{}
	//map of discover ID to indexes of chunks expected at that ID
	conf.idToChunksMap = make(map[discover.NodeID][]int)
	//map of discover ID to kademlia overlay address
	conf.idToAddrMap = make(map[discover.NodeID][]byte)
	//map of overlay address to discover ID
	conf.addrToIdMap = make(map[string]discover.NodeID)
	conf.chunks = make([]storage.Key, 0)
	//First load the snapshot from the file
	trigger := make(chan discover.NodeID)
	// channel to signal simulation initialisation with action call complete
	// or node disconnections
	disconnectC := make(chan error)
	quitC := make(chan struct{})

	//load nodes from the snapshot file
	net, err := initNetWithSnapshot(nodeCount)
	if err != nil {
		return err
	}
	//do cleanup after test is terminated
	defer func() {
		//shutdown the snapshot network
		net.Shutdown()
		//after the test, clean up local stores initialized with createLocalStoreForId
		localStoreCleanup()
		//finally clear all data directories
		datadirsCleanup()
		close(quitC)
	}()
	//get the nodes of the network
	nodes := net.GetNodes()
	//select one index at random...
	idx := rand.Intn(len(nodes))
	//...and get the the node at that index
	//this is the node selected for upload
	node := nodes[idx]

	log.Info("Initializing test config")
	//iterate over all nodes...
	for c := 0; c < len(nodes); c++ {
		//create an array of discovery node IDs
		ids[c] = nodes[c].ID()
		//get the kademlia overlay address from this ID
		a := network.ToOverlayAddr(ids[c].Bytes())
		//append it to the array of all overlay addresses
		conf.addrs = append(conf.addrs, a)
		//the proximity calculation is on overlay addr,
		//the p2p/simulations check func triggers on discover.NodeID,
		//so we need to know which overlay addr maps to which nodeID
		conf.idToAddrMap[ids[c]] = a
		conf.addrToIdMap[string(a)] = ids[c]
	}
	log.Info("Test config successfully initialized")

	//only needed for healthy call when debugging
	ppmap = network.NewPeerPotMap(testMinProxBinSize, conf.addrs)

	//define the action to be performed before the test checks: start syncing
	action := func(ctx context.Context) error {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for range ticker.C {
			healthy := true
			for _, id := range ids {
				r := registries[id]
				//PeerPot for this node
				addr := common.Bytes2Hex(network.ToOverlayAddr(id.Bytes()))
				pp := ppmap[addr]
				//call Healthy RPC
				h := r.delivery.overlay.Healthy(pp)
				//print info
				log.Debug(r.delivery.overlay.String())
				log.Debug(fmt.Sprintf("IS HEALTHY: %t", h.GotNN && h.KnowNN && h.Full))
				if !h.GotNN || !h.Full {
					healthy = false
					break
				}
			}
			if healthy {
				break
			}
		}

		if history {
			log.Info("Uploading for history")
			//If testing only history, we upload the chunk(s) first
			chunks, err := uploadFileToSingleNodeStore(node.ID(), chunkCount)
			if err != nil {
				return err
			}
			conf.chunks = append(conf.chunks, chunks...)
			//finally map chunks to the closest addresses
			mapKeysToNodes(conf)
		}

		//variables needed to wait for all subscriptions established before uploading
		errc := make(chan error)

		//now setup and start event watching in order to know when we can upload
		ctx, watchCancel := context.WithTimeout(context.Background(), MAX_TIMEOUT*time.Second)
		defer watchCancel()

		log.Info("Setting up stream subscription")
		// each node Subscribes to each other's swarmChunkServerStreamName
		for j, id := range ids {
			log.Trace(fmt.Sprintf("subscribe: %d", j))
			client, err := net.GetNode(id).Client()
			if err != nil {
				return err
			}

			watchSubscriptionEvents(ctx, id, client, errc)

			if log.Lvl(*loglevel) >= log.LvlTrace {
				//this will print the kademlia tables of all nodes
				//to only print the kademlia of the pivot node,
				//use: if j == idx {}
				var kt string
				err = client.CallContext(ctx, &kt, "stream_getKad")
				if err != nil {
					return err
				}

				log.Debug("kad table " + node.ID().String())
				log.Debug(kt)
			}
			//watch for peers disconnecting
			err = streamTesting.WatchDisconnections(id, client, disconnectC, quitC)
			if err != nil {
				return err
			}
			//start syncing!
			err = client.CallContext(ctx, nil, "stream_startSyncing")
			if err != nil {
				return err
			}
		}

		//now wait until the number of expected subscriptions has been finished
		go func() {
			globalWg.Wait()
			errc <- nil
		}()

		err := <-errc
		if err != nil {
			return err
		}
		log.Info("Stream subscriptions successfully requested")
		if live {
			//now upload the chunks to the selected random single node
			chunks, err := uploadFileToSingleNodeStore(node.ID(), chunkCount)
			if err != nil {
				return err
			}
			conf.chunks = append(conf.chunks, chunks...)
			//finally map chunks to the closest addresses
			log.Debug(fmt.Sprintf("Uploaded chunks for live syncing: %v", conf.chunks))
			mapKeysToNodes(conf)
			log.Info(fmt.Sprintf("Uploaded %d chunks to random single node", chunkCount))
		}

		log.Info("Action terminated")

		return nil
	}

	//check defines what will be checked during the test
	check := func(ctx context.Context, id discover.NodeID) (bool, error) {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case e := <-disconnectC:
			log.Error(e.Error())
			return false, fmt.Errorf("Disconnect event detected, network unhealthy")
		default:
		}
		log.Trace(fmt.Sprintf("Checking node: %s", id))
		//select the local store for the given node
		lstore := stores[id]
		//if there are more than one chunk, test only succeeds if all expected chunks are found
		allSuccess := true

		//all the chunk indexes which are supposed to be found for this node
		localChunks := conf.idToChunksMap[id]
		//for each expected chunk, check if it is in the local store
		for _, ch := range localChunks {
			//get the real chunk by the index in the index array
			chunk := conf.chunks[ch]
			log.Trace(fmt.Sprintf("node has chunk: %s:", chunk))
			//check if the expected chunk is indeed in the localstore
			if _, err := lstore.Get(chunk); err != nil {
				log.Warn(fmt.Sprintf("Chunk %s NOT found for id %s", chunk, id))
				allSuccess = false
			} else {
				log.Debug(fmt.Sprintf("Chunk %s IS FOUND for id %s", chunk, id))
			}
		}

		return allSuccess, nil
	}

	//for each tick, run the checks on all nodes
	timingTicker := time.NewTicker(time.Second * 1)
	defer timingTicker.Stop()
	go func() {
		for range timingTicker.C {
			for i := 0; i < len(ids); i++ {
				log.Trace(fmt.Sprintf("triggering step %d, id %s", i, ids[i]))
				trigger <- ids[i]
			}
		}
	}()

	log.Info("Starting simulation run...")

	timeout := MAX_TIMEOUT * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	//run the simulation
	result := simulations.NewSimulation(net).Run(ctx, &simulations.Step{
		Action:  action,
		Trigger: trigger,
		Expect: &simulations.Expectation{
			Nodes: ids,
			Check: check,
		},
	})

	if result.Error != nil {
		return result.Error
	}
	log.Info("Simulation terminated")
	return nil
}

//Show kademlia of uploading node for debugging
func (r *TestRegistry) GetKad(ctx context.Context) string {
	return r.delivery.overlay.String()
}

//the server func to start syncing
func (r *TestRegistry) StartSyncing(ctx context.Context) error {
	var err error

	if log.Lvl(*loglevel) == log.LvlDebug {
		//PeerPot for this node
		addr := common.Bytes2Hex(r.addr.OAddr)
		pp := ppmap[addr]
		//call Healthy RPC
		h := r.delivery.overlay.Healthy(pp)
		//print info
		log.Debug(r.delivery.overlay.String())
		log.Debug(fmt.Sprintf("IS HEALTHY: %t", h.GotNN && h.KnowNN && h.Full))
	}

	kad, ok := r.delivery.overlay.(*network.Kademlia)
	if !ok {
		return fmt.Errorf("Not a Kademlia!")
	}

	//iterate over each bin and solicit needed subscription to bins
	kad.EachBin(r.addr.Over(), pof, 0, func(conn network.OverlayConn, po int) bool {
		//identify begin and start index of the bin(s) we want to subscribe to
		log.Debug(fmt.Sprintf("Requesting subscription by: registry %s from peer %s for bin: %d", r.addr.ID(), conf.addrToIdMap[string(conn.Address())], po))
		var histRange *Range
		if history {
			histRange = &Range{}
		}

		globalWg.Add(1)
		err = r.RequestSubscription(conf.addrToIdMap[string(conn.Address())], NewStream("SYNC", FormatSyncBinKey(uint8(po)), live), histRange, Top)
		if err != nil {
			log.Error(fmt.Sprintf("Error in RequestSubsciption! %v", err))
			return false
		}
		return true

	})
	return nil
}

//map chunk keys to addresses which are responsible
func mapKeysToNodes(conf *synctestConfig) {
	kmap := make(map[string][]int)
	nodemap := make(map[string][]int)
	//build a pot for chunk hashes
	np := pot.NewPot(nil, 0)
	indexmap := make(map[string]int)
	for i, a := range conf.addrs {
		indexmap[string(a)] = i
		np, _, _ = pot.Add(np, a, pof)
	}
	//for each address, run EachNeighbour on the chunk hashes pot to identify closest nodes
	log.Trace(fmt.Sprintf("Generated hash chunk(s): %v", conf.chunks))
	for i := 0; i < len(conf.chunks); i++ {
		pl := 256 //highest possible proximity
		var nns []int
		np.EachNeighbour([]byte(conf.chunks[i]), pof, func(val pot.Val, po int) bool {
			a := val.([]byte)
			if pl < 256 && pl != po {
				return false
			}
			if pl == 256 || pl == po {
				log.Trace(fmt.Sprintf("appending %s", conf.addrToIdMap[string(a)]))
				nns = append(nns, indexmap[string(a)])
				nodemap[string(a)] = append(nodemap[string(a)], i)
			}
			if pl == 256 && len(nns) >= testMinProxBinSize {
				//maxProxBinSize has been reached at this po, so save it
				//we will add all other nodes at the same po
				pl = po
			}
			return true
		})
		kmap[string(conf.chunks[i])] = nns
	}
	for addr, chunks := range nodemap {
		//this selects which chunks are expected to be found with the given node
		conf.idToChunksMap[conf.addrToIdMap[addr]] = chunks
	}
	log.Debug(fmt.Sprintf("Map of expected chunks by ID: %v", conf.idToChunksMap))
	conf.chunksToNodesMap = kmap
}

//upload a file(chunks) to a single local node store
func uploadFileToSingleNodeStore(id discover.NodeID, chunkCount int) ([]storage.Key, error) {
	log.Debug(fmt.Sprintf("Uploading to node id: %s", id))
	lstore := stores[id]
	size := chunkSize
	dpa := storage.NewDPA(lstore, storage.NewDPAParams())
	var rootkeys []storage.Key
	for i := 0; i < chunkCount; i++ {
		rk, wait, err := dpa.Store(io.LimitReader(crand.Reader, int64(size)), int64(size), false)
		wait()
		if err != nil {
			return nil, err
		}
		rootkeys = append(rootkeys, (rk))
	}

	return rootkeys, nil
}

//initialize a network from a snapshot
func initNetWithSnapshot(nodeCount int) (*simulations.Network, error) {

	var a adapters.NodeAdapter
	//add the streamer service to the node adapter

	if *adapter == "exec" {
		dirname, err := ioutil.TempDir(".", "")
		if err != nil {
			return nil, err
		}
		a = adapters.NewExecAdapter(dirname)
	} else if *adapter == "socket" {
		a = adapters.NewSocketAdapter(services)
	} else if *adapter == "tcp" {
		a = adapters.NewTCPAdapter(services)
	} else if *adapter == "sim" {
		a = adapters.NewSimAdapter(services)
	}

	log.Info("Setting up Snapshot network")

	net := simulations.NewNetwork(a, &simulations.NetworkConfig{
		ID:             "0",
		DefaultService: "streamer",
	})

	f, err := os.Open(fmt.Sprintf("testing/snapshot_%d.json", nodeCount))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	jsonbyte, err := ioutil.ReadAll(f)
	if err != nil {
		return nil, err
	}
	var snap simulations.Snapshot
	err = json.Unmarshal(jsonbyte, &snap)
	if err != nil {
		return nil, err
	}

	//the snapshot probably has the property EnableMsgEvents not set
	//just in case, set it to true!
	//(we need this to wait for messages before uploading)
	for _, n := range snap.Nodes {
		n.Node.Config.EnableMsgEvents = true
	}

	log.Info("Waiting for p2p connections to be established...")

	//now we can load the snapshot
	err = net.Load(&snap)
	if err != nil {
		return nil, err
	}
	log.Info("Snapshot loaded")
	return net, nil
}

//we want to wait for subscriptions to be established before uploading to test
//that live syncing is working correctly
func watchSubscriptionEvents(ctx context.Context, id discover.NodeID, client *rpc.Client, errc chan error) {
	events := make(chan *p2p.PeerEvent)
	sub, err := client.Subscribe(context.Background(), "admin", events, "peerEvents")
	if err != nil {
		log.Error(err.Error())
		errc <- fmt.Errorf("error getting peer events for node %v: %s", id, err)
		return
	}
	go func() {
		defer sub.Unsubscribe()

		for {
			select {
			case <-ctx.Done():
				errc <- ctx.Err()
				return
			case e := <-events:
				//just catch SubscribeMsg
				if e.Type == p2p.PeerEventTypeMsgRecv && e.Protocol == "stream" && e.MsgCode != nil && *e.MsgCode == 4 {
					globalWg.Done()
				}
			case err := <-sub.Err():
				if err != nil {
					errc <- fmt.Errorf("error getting peer events for node %v: %v", id, err)
					return
				}
			}
		}
	}()
}

//create a local store for the given node
func createTestLocalStorageForId(id discover.NodeID, addr *network.BzzAddr) (storage.ChunkStore, error) {
	var datadir string
	var err error
	datadir, err = ioutil.TempDir("", fmt.Sprintf("syncer-test-%s", id.TerminalString()))
	if err != nil {
		return nil, err
	}
	datadirs[id] = datadir
	var store storage.ChunkStore
	store, err = storage.NewTestLocalStoreForAddr(datadir, addr.Over())
	if err != nil {
		return nil, err
	}
	return store, nil
}
