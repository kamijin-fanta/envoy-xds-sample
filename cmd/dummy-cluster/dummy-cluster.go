package main

import (
	"flag"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

func main() {
	var listen string
	var interval time.Duration
	var maxServers int
	flag.StringVar(&listen, "listen", ":5000", "http listen address")
	flag.DurationVar(&interval, "interval", 1*time.Second, "frequency of add servers")
	flag.IntVar(&maxServers, "servers", 10, "maximum number of servers")
	flag.Parse()

	dummy := NewDummyServer(interval, maxServers)
	lis, err := net.Listen("tcp", listen)
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
}

func NewDummyServer(interval time.Duration, maxServers int) *DummyCluster {
	d := &DummyCluster{
		children: []net.Listener{},
	}

	go func() {
		tick := time.NewTicker(interval)
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
						failPattern := rand.Intn(50)
						switch failPattern {
						case 0:
							// wait for 500~1000ms
							time.Sleep(time.Duration(rand.Intn(500)+500) * time.Millisecond)
						case 1:
							writer.WriteHeader(500)
							writer.Write([]byte("internal server error"))
							return
						case 2:
							writer.WriteHeader(500)
							writer.Write([]byte("internal server error"))
							return
						}
						writer.Write([]byte(lis.Addr().String()))
					}))
				}()

				var remove string
				if len(d.children) > maxServers {
					latest := d.children[0]
					remove = latest.Addr().String()
					err := latest.Close()
					if err != nil {
						panic(err)
					}
					d.children = d.children[1:]
				}
				d.mux.Unlock()

				fmt.Printf("add: %s    remove: %s\n", lis.Addr().String(), remove)
			}
		}
	}()

	return d
}

func (d *DummyCluster) GetServers(writer http.ResponseWriter, req *http.Request) {
	addrs := make([]string, 0, 10)
	d.mux.Lock()
	defer d.mux.Unlock()
	for _, lis := range d.children {
		addrs = append(addrs, lis.Addr().String())
	}

	writer.Write([]byte(strings.Join(addrs, "\n")))
}
