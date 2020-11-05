package main

import (
	"bytes"
	"context"
	"crypto/md5"
	"errors"
	"flag"
	"fmt"
	"github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	"github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	"github.com/envoyproxy/go-control-plane/envoy/service/endpoint/v3"
	"github.com/envoyproxy/go-control-plane/pkg/cache/types"
	"github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	"github.com/envoyproxy/go-control-plane/pkg/server/v3"
	"google.golang.org/grpc"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func main() {
	var listen string
	var serversEndpoint string
	var interval time.Duration
	flag.StringVar(&listen, "listen", "127.0.0.1:20000", "gRPC listen address")
	flag.StringVar(&serversEndpoint, "api", "http://127.0.0.1:5000/servers", "servers API endpoint")
	flag.DurationVar(&interval, "interval", 100*time.Millisecond, "frequency of servers polling")
	flag.Parse()

	ctx := context.TODO()

	watcher := ServerWatcher(100*time.Millisecond, serversEndpoint)

	lis, err := net.Listen("tcp", listen)
	if err != nil {
		panic(err)
	}
	fmt.Printf("grpc listen on %s\n", lis.Addr())
	err = RunServer(ctx, lis, watcher)
	if err != nil {
		panic(err)
	}
}

func ServerWatcher(pollInterval time.Duration, targetAddr string) chan []*InetServerAddr {
	watcher := make(chan []*InetServerAddr)
	go func() {
		ticker := time.NewTicker(pollInterval)
		var latestDigest []byte
		for {
			<-ticker.C
			func() {
				res, err := http.Get(targetAddr)
				if err != nil {
					log.Printf("error on fetch servers %v", err)
					return
				}
				defer res.Body.Close()

				byteArr, _ := ioutil.ReadAll(res.Body)
				digest := md5.Sum(byteArr)
				if bytes.Equal(latestDigest, digest[:]) {
					return
				}
				latestDigest = digest[:]

				servers := strings.Split(string(byteArr), "\n")
				addresses := make([]*InetServerAddr, 0, len(servers))
				for _, server := range servers {
					// serer: 127.0.0.1:12345
					addr, err := InetServerAddrParse(server)
					if err != nil {
						log.Printf("failed parse address %s %v", server, err)
						return
					}
					addresses = append(addresses, addr)
				}
				watcher <- addresses
			}()

		}
	}()
	return watcher
}

var _ cache.NodeHash = &StandardNodeHash{}

type StandardNodeHash struct{}

func (s *StandardNodeHash) ID(node *envoy_config_core_v3.Node) string {
	return "default"
}

type InetServerAddr struct {
	Host string
	Port uint32
}

func InetServerAddrParse(input string) (*InetServerAddr, error) {
	addrs := strings.Split(input, ":")
	if len(addrs) != 2 {
		return nil, errors.New("invalid address format")
	}
	host := addrs[0]
	port, err := strconv.Atoi(addrs[1])
	if err != nil {
		return nil, err
	}
	return &InetServerAddr{
		Host: host,
		Port: uint32(port),
	}, nil
}

func RunServer(ctx context.Context, listener net.Listener, upstreamsChan chan []*InetServerAddr) error {
	snapshotCache := cache.NewSnapshotCache(false, &StandardNodeHash{}, nil)
	srv := server.NewServer(ctx, snapshotCache, nil)

	go func() {
		for {
			upstreams := <-upstreamsChan
			err := snapshotCache.SetSnapshot("default", generateSnapshot(upstreams))
			if err != nil {
				panic(err)
			}
		}
	}()

	grpcServer := grpc.NewServer()
	envoy_service_endpoint_v3.RegisterEndpointDiscoveryServiceServer(grpcServer, srv)

	return grpcServer.Serve(listener)
}

func generateSnapshot(upstreams []*InetServerAddr) cache.Snapshot {
	var resources []types.Resource

	endpoints := make([]*envoy_config_endpoint_v3.LbEndpoint, 0, len(upstreams))
	for _, upstream := range upstreams {
		endpoint := &envoy_config_endpoint_v3.LbEndpoint{
			HostIdentifier: &envoy_config_endpoint_v3.LbEndpoint_Endpoint{
				Endpoint: &envoy_config_endpoint_v3.Endpoint{
					Address: &envoy_config_core_v3.Address{
						Address: &envoy_config_core_v3.Address_SocketAddress{
							SocketAddress: &envoy_config_core_v3.SocketAddress{
								Protocol: envoy_config_core_v3.SocketAddress_TCP,
								Address:  upstream.Host,
								PortSpecifier: &envoy_config_core_v3.SocketAddress_PortValue{
									PortValue: upstream.Port,
								},
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
			},
		},
	}
	resources = append(resources, assignment)
	return cache.NewSnapshot(fmt.Sprintf("%d", time.Now().Unix()), resources, nil, nil, nil, nil, nil)
}
