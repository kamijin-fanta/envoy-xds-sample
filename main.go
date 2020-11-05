package main

import (
	"context"
	"fmt"
	"github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	"github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	"github.com/envoyproxy/go-control-plane/envoy/service/endpoint/v3"
	"github.com/envoyproxy/go-control-plane/pkg/cache/types"
	"github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	"google.golang.org/grpc"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	xds "github.com/envoyproxy/go-control-plane/pkg/server/v3"
)

func main() {
	dummy := NewDummyServer()

	go func() {
		ctx := context.TODO()
		lis, err := net.Listen("tcp", "127.0.0.1:20000")
		if err != nil {
			panic(err)
		}
		fmt.Printf("grpc listen on %s\n", lis.Addr())
		err = run(ctx, lis, dummy.ServersNotif)
		if err != nil {
			panic(err)
		}
	}()

	lis, err := net.Listen("tcp", "127.0.0.1:5000")
	if err != nil {
		panic(err)
	}
	fmt.Printf("http listen on %s\n", lis.Addr())
	err = http.Serve(lis, http.HandlerFunc(dummy.GetServers))
	if err != nil {
		panic(err)
	}
}

type DummyCluster struct {
	children []net.Listener
	mux      sync.Mutex

	ServersNotif chan []net.Listener
}

func NewDummyServer() *DummyCluster {
	d := &DummyCluster{
		children:     []net.Listener{},
		ServersNotif: make(chan []net.Listener),
	}

	go func() {
		tick := time.NewTicker(1 * time.Second)
		for {
			select {
			case <-tick.C:
				d.mux.Lock()
				lis, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					panic(err)
				}
				d.children = append(d.children, lis)

				go func() {
					http.Serve(lis, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
						writer.Write([]byte(lis.Addr().String()))
					}))
				}()

				var remove string
				if len(d.children) > 10 {
					latest := d.children[0]
					remove = latest.Addr().String()
					err := latest.Close()
					if err != nil {
						panic(err)
					}
					d.children = d.children[1:]
				}
				d.mux.Unlock()

				d.ServersNotif <- d.children

				fmt.Printf("add: %s    remove: %s\n", lis.Addr().String(), remove)
			}
		}
	}()

	return d
}

func (d *DummyCluster) GetServers(writer http.ResponseWriter, req *http.Request) {
	addrs := make([]string, 0, 10)
	for _, lis := range d.children {
		addrs = append(addrs, lis.Addr().String())
	}

	writer.Write([]byte(strings.Join(addrs, "\n")))
}

var _ cache.NodeHash = &StandardNodeHash{}

type StandardNodeHash struct{}

func (s *StandardNodeHash) ID(node *envoy_config_core_v3.Node) string {
	if node == nil {
		return "unknown"
	}
	return node.Cluster + "/" + node.Id
}

func run(ctx context.Context, listener net.Listener, serversNotif chan []net.Listener) error {
	// xDSの結果をキャッシュとして設定すると、いい感じにxDS APIとして返してくれる。
	snapshotCache := cache.NewSnapshotCache(false, &StandardNodeHash{}, nil)
	server := xds.NewServer(ctx, snapshotCache, nil)

	go func() {
		for {
			servers := <-serversNotif
			err := snapshotCache.SetSnapshot("cluster.local/node0", defaultSnapshot(servers))
			if err != nil {
				panic(err)
			}
		}
	}()
	// NodeHashで返ってくるハッシュ値とその設定のスナップショットをキャッシュとして覚える
	//err := snapshotCache.SetSnapshot("cluster.local/node0", defaultSnapshot())
	//if err != nil {
	//	return err
	//}

	// gRCPサーバーを起動してAPIを提供
	grpcServer := grpc.NewServer()
	envoy_service_endpoint_v3.RegisterEndpointDiscoveryServiceServer(grpcServer, server)

	return grpcServer.Serve(listener)
}

func defaultSnapshot(servers []net.Listener) cache.Snapshot {
	var resources []types.Resource

	endpoints := make([]*envoy_config_endpoint_v3.LbEndpoint, 0, len(servers))
	for _, server := range servers {
		adds := strings.Split(server.Addr().String(), ":")
		host := adds[0]
		port, err := strconv.Atoi(adds[1])
		if err != nil {
			panic(err)
		}
		endpoint := &envoy_config_endpoint_v3.LbEndpoint{
			HostIdentifier: &envoy_config_endpoint_v3.LbEndpoint_Endpoint{
				Endpoint: &envoy_config_endpoint_v3.Endpoint{
					Address: &envoy_config_core_v3.Address{
						Address: &envoy_config_core_v3.Address_SocketAddress{
							SocketAddress: &envoy_config_core_v3.SocketAddress{
								Protocol: envoy_config_core_v3.SocketAddress_TCP,
								Address:  host,
								PortSpecifier: &envoy_config_core_v3.SocketAddress_PortValue{
									PortValue: uint32(port),
								},
								Ipv4Compat: false,
							},
						},
					},
				},
			},
		}
		endpoints = append(endpoints, endpoint)
	}

	assignment := &envoy_config_endpoint_v3.ClusterLoadAssignment{
		ClusterName: "app_cluster",
		Endpoints: []*envoy_config_endpoint_v3.LocalityLbEndpoints{
			{
				LbEndpoints: endpoints,
				//[]*envoy_config_endpoint_v3.LbEndpoint{
				//	{
				//		HostIdentifier: &envoy_config_endpoint_v3.LbEndpoint_Endpoint{
				//			Endpoint: &envoy_config_endpoint_v3.Endpoint{
				//				Address: &envoy_config_core_v3.Address{
				//					Address: &envoy_config_core_v3.Address_SocketAddress{
				//						SocketAddress: &envoy_config_core_v3.SocketAddress{
				//							Protocol: envoy_config_core_v3.SocketAddress_TCP,
				//							Address:  "127.0.0.1",
				//							PortSpecifier: &envoy_config_core_v3.SocketAddress_PortValue{
				//								PortValue: 1234,
				//							},
				//							Ipv4Compat: false,
				//						},
				//					},
				//				},
				//			},
				//		},
				//	},
				//},
			},
		},
	}
	resources = append(resources, assignment)
	return cache.NewSnapshot(fmt.Sprintf("%d", time.Now().Unix()), resources, nil, nil, nil, nil, nil)
}
