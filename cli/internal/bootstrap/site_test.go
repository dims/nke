// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	pb "github.com/NVIDIA/nke/agent/pkg/agent/proto"
	"github.com/NVIDIA/nke/cli/internal/inventory"
)

const testJoinCommand = "kubeadm join 10.0.0.1:6443 --token abcdef.0123456789abcdef " +
	"--discovery-token-ca-cert-hash sha256:deadbeef --control-plane --certificate-key cafebabe"

// nodeState is what an agent reports from GetKubeadmStatus.
type nodeState struct {
	status  string
	phase   string
	cni     string
	message string
}

// fakeAgent stands in for nke-agent. It records the calls it received, in
// order, so a test can assert what the CLI did and in which sequence.
//
// The inventory gives every node the same agent port, so one fake serves every
// node in a test cluster. The call log is what tells the nodes apart.
type fakeAgent struct {
	pb.UnimplementedNodeAgentServer

	mu    sync.Mutex
	calls []string

	initReqs []*pb.KubeadmInitRequest
	joinReqs []*pb.KubeadmJoinRequest

	// state is what GetKubeadmStatus reports. onInit and onJoin replace it when
	// the matching RPC arrives, which is how the agent behaves: it sets the
	// phase before it returns, then works in the background.
	state  nodeState
	onInit nodeState
	onJoin nodeState

	// rejectInit and rejectJoin make the agent refuse the request in the
	// response body, the way it refuses a node that is already initializing.
	rejectInit string
	rejectJoin string

	joinString string
}

func newFake() *fakeAgent {
	return &fakeAgent{
		// A node that has never run kubeadm reports empty strings.
		state:      nodeState{},
		onInit:     nodeState{status: statusReady, phase: phaseInit, cni: cniReadyState},
		onJoin:     nodeState{status: statusReady, phase: phaseJoin},
		joinString: testJoinCommand,
	}
}

func (f *fakeAgent) ExecuteKubeadmInit(_ context.Context, req *pb.KubeadmInitRequest) (*pb.KubeadmInitStatusResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "init")
	f.initReqs = append(f.initReqs, req)
	if f.rejectInit != "" {
		return &pb.KubeadmInitStatusResponse{Status: statusError, ErrorMessage: f.rejectInit}, nil
	}
	f.state = f.onInit
	return &pb.KubeadmInitStatusResponse{Status: "pending", Phase: phaseInit}, nil
}

func (f *fakeAgent) ExecuteKubeadmJoin(_ context.Context, req *pb.KubeadmJoinRequest) (*pb.KubeadmJoinStatusResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "join")
	f.joinReqs = append(f.joinReqs, req)
	if f.rejectJoin != "" {
		return &pb.KubeadmJoinStatusResponse{Status: statusError, ErrorMessage: f.rejectJoin}, nil
	}
	f.state = f.onJoin
	return &pb.KubeadmJoinStatusResponse{Status: "pending", Phase: phaseJoin}, nil
}

func (f *fakeAgent) GetKubeadmStatus(context.Context, *pb.KubeadmStatusRequest) (*pb.KubeadmStatusResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &pb.KubeadmStatusResponse{
		KubeadmStatus:         f.state.status,
		KubeadmPhase:          f.state.phase,
		CniInstallStatus:      f.state.cni,
		KubeadmCurrentMessage: f.state.message,
	}, nil
}

func (f *fakeAgent) GetKubeadmJoinString(context.Context, *pb.KubeadmJoinStringRequest) (*pb.KubeadmJoinStringResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "joinString")
	return &pb.KubeadmJoinStringResponse{ControlplaneJoinString: f.joinString}, nil
}

func (f *fakeAgent) callLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// start serves the fake on a random loopback port and returns an inventory
// pointing nodeCount nodes at it. The first node is the init node.
func start(t *testing.T, f *fakeAgent, nodeCount int) *inventory.Inventory {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	srv := grpc.NewServer()
	pb.RegisterNodeAgentServer(srv, f)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	host, port, err := net.SplitHostPort(lis.Addr().String())
	require.NoError(t, err)

	inv := &inventory.Inventory{
		ClusterName: "test-site",
		Cluster: inventory.Cluster{
			InitNode:      inventory.Node{Name: "node1", IP: host},
			K8sVersion:    "1.32.0",
			CniName:       "calico",
			CniVersion:    "3.31.4",
			PodCidr:       "10.100.0.0/16",
			ServiceCidr:   "10.200.0.0/16",
			NodeAgentPort: port,
		},
	}
	for i := 2; i <= nodeCount; i++ {
		inv.Cluster.JoinNodes = append(inv.Cluster.JoinNodes,
			inventory.Node{Name: "node" + string(rune('0'+i)), IP: host})
	}
	return inv
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func run(t *testing.T, inv *inventory.Inventory) error {
	t.Helper()
	return Run(context.Background(), inv, Options{Logger: quietLogger()})
}

func TestRunSingleNode(t *testing.T) {
	f := newFake()
	require.NoError(t, run(t, start(t, f, 1)))

	require.Equal(t, []string{"init"}, f.callLog(),
		"a single-node cluster inits and stops, with no join command to read")

	// The inventory must reach the agent intact. These are the fields the agent
	// needs to build the kubeadm command line itself.
	req := f.initReqs[0]
	require.Equal(t, "10.100.0.0/16", req.GetPodNetworkCidr())
	require.Equal(t, "10.200.0.0/16", req.GetServiceCidr())
	require.Equal(t, "1.32.0", req.GetKubernetesVersion())
	require.Equal(t, "calico", req.GetCniPluginName()) //nolint:staticcheck // in-init CNI field
	require.Equal(t, "3.31.4", req.GetTigeraVersion()) //nolint:staticcheck // ditto
	require.NotEmpty(t, req.GetControlPlaneEndpoint()) // required by the agent's CLI path
	require.Empty(t, req.GetInitConfig(),
		"the agent builds the command line only when init_config is empty")
}

func TestRunJoinsEveryNodeAfterInit(t *testing.T) {
	f := newFake()
	require.NoError(t, run(t, start(t, f, 3)))

	require.Equal(t, []string{"init", "joinString", "join", "join"}, f.callLog())

	// Every node is a control-plane node in M0, so both get the control-plane
	// command the init node handed back, not the worker one.
	require.Len(t, f.joinReqs, 2)
	for _, r := range f.joinReqs {
		require.Equal(t, testJoinCommand, r.GetKubeadmJoinString())
	}
}

func TestRunStopsBeforeTouchingAnyNodeWhenOneIsUnreachable(t *testing.T) {
	f := newFake()
	inv := start(t, f, 1)
	// Nothing listens on port 1, so this node cannot answer.
	inv.Cluster.JoinNodes = []inventory.Node{{Name: "node2", IP: "127.0.0.1"}}
	inv.Cluster.NodeAgentPort = "1"

	require.Error(t, run(t, inv))
	require.Empty(t, f.callLog(), "a run that cannot reach every node must change nothing")
}

func TestRunFailsWhenAgentRejectsInit(t *testing.T) {
	f := newFake()
	f.rejectInit = "node is already initializing"

	err := run(t, start(t, f, 1))
	require.ErrorContains(t, err, "agent rejected kubeadm init")
	require.ErrorContains(t, err, "node is already initializing")
	require.ErrorContains(t, err, "node1")
}

func TestRunFailsWhenAgentRejectsJoin(t *testing.T) {
	f := newFake()
	f.rejectJoin = "node is already joined or pending join"

	err := run(t, start(t, f, 2))
	require.ErrorContains(t, err, "agent rejected kubeadm join")
	require.ErrorContains(t, err, "node2")
}

func TestRunFailsWhenKubeadmReportsError(t *testing.T) {
	f := newFake()
	f.onInit = nodeState{status: statusError, phase: phaseInit, message: "port 6443 is in use"}

	err := run(t, start(t, f, 1))
	require.ErrorContains(t, err, "kubeadm initialize failed")
	require.ErrorContains(t, err, "port 6443 is in use")
	require.ErrorContains(t, err, "node1")
}

func TestRunFailsWhenCNIInstallFails(t *testing.T) {
	f := newFake()
	// kubeadm itself succeeded, so only the CNI status shows the failure.
	f.onInit = nodeState{
		status: statusReady, phase: phaseInit,
		cni: cniErrorState, message: "failed to install CNI plugin",
	}

	err := run(t, start(t, f, 1))
	require.ErrorContains(t, err, "CNI install failed")
	require.ErrorContains(t, err, "failed to install CNI plugin")
}

func TestRunRejectsUnknownCNI(t *testing.T) {
	f := newFake()
	inv := start(t, f, 1)
	inv.Cluster.CniName = "flannel"

	require.ErrorContains(t, run(t, inv), "unsupported cniName")
	require.Empty(t, f.callLog(), "an inventory the CLI cannot honour must change nothing")
}

func TestRunFailsWhenJoinCommandIsEmpty(t *testing.T) {
	f := newFake()
	f.joinString = ""

	err := run(t, start(t, f, 2))
	require.ErrorContains(t, err, "empty control-plane join command")
	require.Equal(t, []string{"init", "joinString"}, f.callLog(),
		"no node may be joined with an empty command")
}
