package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/morgangallant/boilerplate/internal/pb"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
	"tailscale.com/net/interfaces"
)

const rpcPort = "11105"

type tailscalePeer struct {
	Host string `json:"HostName"`
	Addr net.IP `json:"TailAddr"`
	OS   string `json:"os"`
}

func me() (tailscalePeer, error) {
	addr, _, err := interfaces.Tailscale()
	if err != nil {
		return tailscalePeer{}, errors.Wrap(err, "failed to get machines tailscale ip")
	}
	hn, err := os.Hostname()
	if err != nil {
		return tailscalePeer{}, errors.Wrap(err, "failed to get machine hostname")
	}
	return tailscalePeer{Host: hn, Addr: addr, OS: runtime.GOOS}, nil
}

func fetchTailscalePeers(filter func(tailscalePeer) bool) ([]tailscalePeer, error) {
	// If we aren't in production, then we should just default to no peers.
	if runtime.GOOS != "linux" {
		return []tailscalePeer{}, nil
	}
	out, err := exec.Command("tailscale", "status", "--json").Output()
	if err != nil {
		return nil, errors.Wrap(err, "failed to exec tailscale status")
	}
	var buf struct {
		Peers map[string]tailscalePeer `json:"Peer"`
	}
	if err := json.Unmarshal(out, &buf); err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal tailscale status")
	}
	ret := []tailscalePeer{}
	for _, peer := range buf.Peers {
		if filter(peer) {
			ret = append(ret, peer)
		}
	}
	return ret, nil
}

type peer struct {
	tailscalePeer
	conn      *grpc.ClientConn
	failCount uint
}

func (p *peer) String() string {
	return fmt.Sprintf("%s (%s)", p.tailscalePeer.Host, p.tailscalePeer.Addr)
}

func (p *peer) connect() (*grpc.ClientConn, error) {
	if p.conn != nil {
		return p.conn, nil
	}
	addr := net.JoinHostPort(p.tailscalePeer.Addr.String(), rpcPort)
	conn, err := grpc.Dial(addr, grpc.WithInsecure())
	if err != nil {
		return nil, errors.Wrapf(err, "failed to dial peer %s", p)
	}
	p.conn = conn
	return p.conn, nil
}

func (p *peer) rpc(rf func(*grpc.ClientConn) (interface{}, error)) (interface{}, error) {
	conn, err := p.connect()
	if err != nil {
		return nil, err
	}
	return rf(conn)
}

type state int

const (
	follower state = iota
	candidate
	leader
)

type cluster struct {
	me     string
	peers  map[string]*peer
	logger Logger

	// Protects everything underneath.
	mu     sync.RWMutex
	term   int64
	voted  string
	leader string
	state  state
	reset  time.Time
}

func (c *cluster) isLeader() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.state == leader && c.leader == c.me
}

func (c *cluster) runElectionTimer() {
	timeout := time.Duration(150+rand.Intn(150)) * time.Millisecond // Can eventually tune.
	c.mu.RLock()
	saved := c.term
	c.mu.RUnlock()
	tkr := time.NewTicker(10 * time.Millisecond)
	defer tkr.Stop()
	for range tkr.C {
		c.mu.RLock()
		// We're leader, or someone beat us to it.
		if c.state == leader || saved != c.term {
			c.mu.RUnlock()
			return
		}
		reset := c.reset
		c.mu.RUnlock()
		if elapsed := time.Since(reset); elapsed >= timeout {
			c.startElection() // Start a revolution!!!
			return
		}
	}
}

func (c *cluster) forEachPeer(pf func(*peer), skipme bool) {
	for h, p := range c.peers {
		if skipme && h == c.me {
			continue
		}
		go pf(p)
	}
}

func (c *cluster) shouldWinByDefault() bool {
	if len(c.peers) > 1 {
		return false
	}
	_, ok := c.peers[c.me]
	return ok
}

func (c *cluster) logSuccessfulRequest(p *peer) {
	if _, ok := c.peers[p.tailscalePeer.Addr.String()]; !ok {
		return
	}
	p.failCount = 0
}

func (c *cluster) logFailedRequest(p *peer) {
	id := p.tailscalePeer.Addr.String()
	if _, ok := c.peers[id]; !ok {
		return
	}
	p.failCount++
	if p.failCount > 4 {
		delete(c.peers, id)
	}
}

func (c *cluster) startElection() {
	c.mu.Lock()
	c.reset = time.Now()
	c.voted = c.me // Vote for ourselves ofc.
	c.term++
	c.state = candidate
	saved := c.term
	c.mu.Unlock()
	if c.shouldWinByDefault() {
		c.startLeader()
		return
	}
	// Do the vote cluster-wide.
	var votes int32 = 1
	c.forEachPeer(func(p *peer) {
		d, err := p.rpc(func(conn *grpc.ClientConn) (interface{}, error) {
			client := pb.NewClusterClient(conn)
			return client.RequestVote(context.Background(), &pb.VoteRequest{
				Term:      saved,
				Candidate: c.me,
			})
		})
		if err != nil {
			c.logFailedRequest(p)
			return
		}
		c.logSuccessfulRequest(p)
		r := d.(*pb.VoteResponse)
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.state != candidate {
			return
		}
		if r.Term > saved {
			c.becomeFollower(r.Term)
		} else if r.Term == saved && r.Granted {
			v := int(atomic.AddInt32(&votes, 1))
			if v*2 > len(c.peers) {
				c.startLeader()
			}
		}
	}, /*skipme:*/ true)
	go c.runElectionTimer()
}

func (c *cluster) sendLeaderHeartbeats() {
	c.mu.RLock()
	saved := c.term
	c.mu.RUnlock()
	c.forEachPeer(func(p *peer) {
		d, err := p.rpc(func(conn *grpc.ClientConn) (interface{}, error) {
			client := pb.NewClusterClient(conn)
			return client.LeaderHeartbeat(context.Background(), &pb.HeartbeatRequest{
				Term:   saved,
				Leader: c.me,
			})
		})
		if err != nil {
			c.logFailedRequest(p)
			return
		}
		c.logSuccessfulRequest(p)
		r := d.(*pb.HeartbeatResponse)
		c.mu.Lock()
		defer c.mu.Unlock()
		if r.Term > c.term {
			c.becomeFollower(r.Term)
		}
	}, /* skipme: */ true)
}

// Requires mu held.
func (c *cluster) startLeader() {
	c.leader = c.me
	c.state = leader
	c.logger.Logf("%s became leader in term %d.", c.peers[c.me], c.term)

	// Start leader duties.
	go func() {
		tkr := time.NewTicker(50 * time.Millisecond)
		defer tkr.Stop()
		for range tkr.C {
			c.sendLeaderHeartbeats()
			c.mu.RLock()
			if c.state != leader {
				c.mu.RUnlock()
				return
			}
			c.mu.RUnlock()
		}
	}()
}

// Requires mutex held.
func (c *cluster) becomeFollower(term int64) {
	c.term = term
	c.voted = ""
	c.reset = time.Now()
	c.state = follower
	go c.runElectionTimer()
}

func (c *cluster) announceToPeers() {
	var wg sync.WaitGroup
	c.forEachPeer(func(p *peer) {
		defer wg.Done()
		_, err := p.rpc(func(conn *grpc.ClientConn) (interface{}, error) {
			client := pb.NewClusterClient(conn)
			return client.Hello(context.Background(), &pb.HelloRequest{
				TsHost:   c.me,
				Hostname: c.peers[c.me].tailscalePeer.Host,
				Os:       c.peers[c.me].tailscalePeer.OS,
			})
		})
		if err != nil {
			c.logFailedRequest(p)
			return
		}
		c.logSuccessfulRequest(p)
	}, /* skipme: */ true)
	wg.Add(len(c.peers) - 1)
	wg.Wait()
}

type clusterService struct {
	pb.UnimplementedClusterServer
	c *cluster
}

func (s *clusterService) register(server *grpc.Server) {
	pb.RegisterClusterServer(server, s)
}

func (s *clusterService) Hello(ctx context.Context, r *pb.HelloRequest) (*pb.Empty, error) {
	s.c.peers[r.TsHost] = &peer{
		tailscalePeer: tailscalePeer{
			Host: r.Hostname,
			Addr: net.ParseIP(r.TsHost),
			OS:   r.Os,
		},
	}
	return &pb.Empty{}, nil
}

func (s *clusterService) RequestVote(ctx context.Context, r *pb.VoteRequest) (*pb.VoteResponse, error) {
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	if r.Term > s.c.term {
		s.c.becomeFollower(r.Term)
	}
	resp := &pb.VoteResponse{Term: s.c.term}
	if s.c.term == r.Term && (s.c.voted == "" || s.c.voted == r.Candidate) {
		resp.Granted = true
		s.c.voted = r.Candidate
		s.c.reset = time.Now()
	}
	return resp, nil
}

func (s *clusterService) LeaderHeartbeat(ctx context.Context, r *pb.HeartbeatRequest) (*pb.HeartbeatResponse, error) {
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	if r.Term > s.c.term {
		s.c.becomeFollower(r.Term)
	}
	if r.Term == s.c.term {
		if s.c.state != follower {
			s.c.becomeFollower(r.Term)
		}
		s.c.reset = time.Now() // Super important.
	}
	s.c.leader = r.Leader
	return &pb.HeartbeatResponse{Term: s.c.term}, nil
}

// Singleton cluster object.
var inst *cluster

func (s *clusterService) initc(logger Logger) error {
	if inst != nil {
		return errors.New("cluster already exists")
	}
	m, err := me()
	if err != nil {
		return errors.Wrap(err, "failed to get myself")
	}
	ps, err := fetchTailscalePeers(func(tp tailscalePeer) bool {
		return tp.OS == "linux" && strings.HasPrefix(tp.Host, "le-")
	})
	if err != nil {
		return errors.Wrap(err, "failed to fetch tailscale peers")
	}

	s.c = &cluster{
		me: m.Addr.String(),
		peers: map[string]*peer{
			m.Addr.String(): {tailscalePeer: m},
		},
		logger: logger,
	}
	for _, p := range ps {
		s.c.peers[p.Addr.String()] = &peer{tailscalePeer: p}
	}
	inst = s.c
	return nil
}

func startCluster(logger Logger) error {
	s := &clusterService{}
	if err := s.initc(logger); err != nil {
		return errors.Wrap(err, "failed to initialize cluster")
	}
	server := grpc.NewServer()
	s.register(server)
	// Explicitly listen on the Tailscale address.
	lis, err := net.Listen("tcp", net.JoinHostPort(s.c.me, rpcPort))
	if err != nil {
		return errors.Wrap(err, "failed to start listener")
	}
	go func() {
		// Need to wait until caller has initialized gRPC server.
		time.Sleep(10 * time.Millisecond)
		s.c.announceToPeers()
		s.c.becomeFollower(0)
	}()
	return server.Serve(lis)
}

func isLeader() bool {
	if inst == nil {
		panic("cluster instance is nil")
	}
	return inst.isLeader()
}
