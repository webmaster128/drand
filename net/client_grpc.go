package net

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/drand/drand/protobuf/drand"
	"github.com/nikkolasg/slog"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

var _ Client = (*grpcClient)(nil)

//var defaultJSONMarshaller = &runtime.JSONBuiltin{}
var defaultJSONMarshaller = &HexJSON{}

// grpcClient implements both InternalClient and ExternalClient functionalities
// using gRPC as its underlying mechanism
type grpcClient struct {
	sync.Mutex
	conns    map[string]*grpc.ClientConn
	opts     []grpc.DialOption
	timeout  time.Duration
	manager  *CertManager
	failFast grpc.CallOption
}

var defaultTimeout = 1 * time.Minute

// NewGrpcClient returns an implementation of an InternalClient  and
// ExternalClient using gRPC connections
func NewGrpcClient(opts ...grpc.DialOption) Client {
	return &grpcClient{
		opts:    opts,
		conns:   make(map[string]*grpc.ClientConn),
		timeout: defaultTimeout,
	}
}

// NewGrpcClientFromCertManager returns a Client using gRPC with the given trust
// store of certificates.
func NewGrpcClientFromCertManager(c *CertManager, opts ...grpc.DialOption) Client {
	client := NewGrpcClient(opts...).(*grpcClient)
	client.manager = c
	return client
}

// NewGrpcClientWithTimeout returns a Client using gRPC using fixed timeout for
// method calls.
func NewGrpcClientWithTimeout(timeout time.Duration, opts ...grpc.DialOption) Client {
	c := NewGrpcClient(opts...).(*grpcClient)
	c.timeout = timeout
	return c
}

func getTimeoutContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	clientDeadline := time.Now().Add(timeout)
	return context.WithDeadline(ctx, clientDeadline)
}

func (g *grpcClient) SetTimeout(p time.Duration) {
	g.Lock()
	defer g.Unlock()
	g.timeout = p
}

func (g *grpcClient) PublicRand(p Peer, in *drand.PublicRandRequest) (*drand.PublicRandResponse, error) {
	var resp *drand.PublicRandResponse
	c, err := g.conn(p)
	if err != nil {
		return nil, err
	}
	client := drand.NewPublicClient(c)
	ctx, _ := getTimeoutContext(context.Background(), g.timeout)
	resp, err = client.PublicRand(ctx, in)
	return resp, err
}

func (g *grpcClient) PublicRandStream(ctx context.Context, p Peer, in *drand.PublicRandRequest, opts ...CallOption) (chan *drand.PublicRandResponse, error) {
	var outCh = make(chan *drand.PublicRandResponse, 10)
	c, err := g.conn(p)
	if err != nil {
		return nil, err
	}
	client := drand.NewPublicClient(c)
	ctx, _ = getTimeoutContext(ctx, g.timeout)
	stream, err := client.PublicRandStream(ctx, in)
	if err != nil {
		return nil, err
	}
	go func() {
		for {
			resp, err := stream.Recv()
			if err == io.EOF {
				close(outCh)
				return
			}
			if err != nil {
				// XXX should probably do stg different here but since we are
				// continuously stream, if stream stops, it means stg went
				// wrong; it should never EOF
				close(outCh)
				return
			}
			select {
			case outCh <- resp:
			case <-ctx.Done():
				close(outCh)
				return
			}
		}
	}()
	return outCh, nil
}

func (g *grpcClient) PrivateRand(p Peer, in *drand.PrivateRandRequest) (*drand.PrivateRandResponse, error) {
	var resp *drand.PrivateRandResponse
	c, err := g.conn(p)
	if err != nil {
		return nil, err
	}
	client := drand.NewPublicClient(c)
	ctx, _ := getTimeoutContext(context.Background(), g.timeout)
	resp, err = client.PrivateRand(ctx, in)
	return resp, err
}

func (g *grpcClient) Group(p Peer, in *drand.GroupRequest) (*drand.GroupResponse, error) {
	var resp *drand.GroupResponse
	c, err := g.conn(p)
	if err != nil {
		return nil, err
	}
	client := drand.NewPublicClient(c)
	ctx, _ := getTimeoutContext(context.Background(), g.timeout)
	resp, err = client.Group(ctx, in)
	return resp, err
}
func (g *grpcClient) DistKey(p Peer, in *drand.DistKeyRequest) (*drand.DistKeyResponse, error) {
	var resp *drand.DistKeyResponse
	c, err := g.conn(p)
	if err != nil {
		return nil, err
	}
	client := drand.NewPublicClient(c)
	resp, err = client.DistKey(context.Background(), in)
	return resp, err
}

func (g *grpcClient) Setup(p Peer, in *drand.SetupPacket, opts ...CallOption) (*drand.Empty, error) {
	var resp *drand.Empty
	c, err := g.conn(p)
	if err != nil {
		return nil, err
	}
	client := drand.NewProtocolClient(c)
	// give more time for DKG we are not in a hurry
	ctx, _ := getTimeoutContext(context.Background(), g.timeout*time.Duration(2))
	resp, err = client.Setup(ctx, in, opts...)
	return resp, err
}

func (g *grpcClient) Reshare(p Peer, in *drand.ResharePacket, opts ...CallOption) (*drand.Empty, error) {
	var resp *drand.Empty
	c, err := g.conn(p)
	if err != nil {
		return nil, err
	}
	client := drand.NewProtocolClient(c)
	// give more time for DKG we are not in a hurry
	ctx, _ := getTimeoutContext(context.Background(), g.timeout*time.Duration(2))
	resp, err = client.Reshare(ctx, in, opts...)
	return resp, err
}

func (g *grpcClient) NewBeacon(p Peer, in *drand.BeaconRequest, opts ...CallOption) (*drand.BeaconResponse, error) {
	var resp *drand.BeaconResponse
	c, err := g.conn(p)
	if err != nil {
		return nil, err
	}
	client := drand.NewProtocolClient(c)
	ctx, _ := getTimeoutContext(context.Background(), g.timeout)
	resp, err = client.NewBeacon(ctx, in, opts...)
	return resp, err
}

const SyncBlockKey = "sync"

func (g *grpcClient) SyncChain(ctx context.Context, p Peer, in *drand.SyncRequest, opts ...CallOption) (chan *drand.SyncResponse, error) {
	resp := make(chan *drand.SyncResponse)
	c, err := g.conn(p)
	if err != nil {
		return nil, err
	}
	client := drand.NewProtocolClient(c)
	ctx, _ = getTimeoutContext(ctx, g.timeout)
	stream, err := client.SyncChain(ctx, in)
	if err != nil {
		return nil, err
	}
	/*// read how many first*/
	//md, err := stream.Header()
	//if err != nil {
	//return nil, err
	//}
	//vals := md.Get(SyncBlockKey)
	//if len(vals) != 1 {
	//return nil, fmt.Errorf("invalid header received: %v", vals)
	//}
	//toReceive, err := strconv.Atoi(vals[0])
	//if err != nil {
	//return nil, fmt.Errorf("invalid number of blocks to sync received: %v", vals[0])
	/*}*/
	go func() {
		for {
			reply, err := stream.Recv()
			if err == io.EOF {
				break
			}
			if err != nil {
				break
			}
			select {
			case <-ctx.Done():
				break
			default:
				resp <- reply
			}
		}
	}()
	return resp, nil
}

func (g *grpcClient) Home(p Peer, in *drand.HomeRequest) (*drand.HomeResponse, error) {
	var resp *drand.HomeResponse
	c, err := g.conn(p)
	if err != nil {
		return nil, err
	}
	client := drand.NewPublicClient(c)
	ctx, _ := getTimeoutContext(context.Background(), g.timeout)
	resp, err = client.Home(ctx, in)
	return resp, err
}

func (g *grpcClient) deleteConn(p Peer) {
	g.Lock()
	defer g.Unlock()
	delete(g.conns, p.Address())
}

// conn retrieve an already existing conn to the given peer or create a new one
func (g *grpcClient) conn(p Peer) (*grpc.ClientConn, error) {
	g.Lock()
	defer g.Unlock()
	var err error
	c, ok := g.conns[p.Address()]
	if !ok {
		slog.Debugf("grpc-client: attempting connection to %s (TLS %v)", p.Address(), p.IsTLS())
		if !p.IsTLS() {
			fmt.Println(" || USING NO TLS !")
			c, err = grpc.Dial(p.Address(), append(g.opts, grpc.WithInsecure())...)
		} else {
			opts := g.opts
			if g.manager != nil {
				fmt.Println(" || USING POOL !")
				pool := g.manager.Pool()
				creds := credentials.NewClientTLSFromCert(pool, "")
				opts = append(g.opts, grpc.WithTransportCredentials(creds))
			} else {
				fmt.Println(" || NOT USING POOL!", g.manager)
			}
			c, err = grpc.Dial(p.Address(), opts...)
		}
		g.conns[p.Address()] = c
	}
	return c, err
}

// proxyClient is used by the gRPC json gateway to dispatch calls to the
// underlying gRPC server. It needs only to implement the public facing API
type proxyClient struct {
	s Service
}

func newProxyClient(s Service) *proxyClient {
	return &proxyClient{s}
}

func (p *proxyClient) Public(c context.Context, in *drand.PublicRandRequest, opts ...grpc.CallOption) (*drand.PublicRandResponse, error) {
	return p.s.PublicRand(c, in)
}
func (p *proxyClient) Private(c context.Context, in *drand.PrivateRandRequest, opts ...grpc.CallOption) (*drand.PrivateRandResponse, error) {
	return p.s.PrivateRand(c, in)
}
func (p *proxyClient) DistKey(c context.Context, in *drand.DistKeyRequest, opts ...grpc.CallOption) (*drand.DistKeyResponse, error) {
	return p.s.DistKey(c, in)
}

func (p *proxyClient) Home(c context.Context, in *drand.HomeRequest, opts ...grpc.CallOption) (*drand.HomeResponse, error) {
	return p.s.Home(c, in)
}
