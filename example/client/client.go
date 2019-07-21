package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/GrassInWind2019/gRPCwithConsul/example/proto"
	"github.com/GrassInWind2019/gRPCwithConsul/serviceDiscovery"
	"google.golang.org/grpc"
	"google.golang.org/grpc/balancer/roundrobin"
)

const (
	consulScheme = "consul"
	defaultName  = "GrassInWind2019"
	timeout      = 60 * time.Second
)

func main() {
	err := serviceDiscovery.ConsulResolverInit("127.0.0.1:8500", "HelloService")
	if err != nil {
		fmt.Println("ConsulResolverInit failed: ", err.Error())
		return
	}

	//Connect to the server
	conn, err := grpc.Dial(fmt.Sprintf("%s:///HelloService", consulScheme), grpc.WithInsecure(), grpc.WithBalancerName(roundrobin.Name))
	if err != nil {
		fmt.Println("dial to HelloService failed: %v", err)
		return
	}
	defer conn.Close()

	client := proto.NewHelloServiceClient(conn)

	//name will be used as request to server
	name := defaultName
	if len(os.Args) > 1 {
		name = os.Args[1]
	}

	for {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		result, err := client.SayHello(ctx, &proto.HelloRequest{Name: name})
		if err != nil {
			fmt.Println("client call SayHello failed: %v", err)
		} else {
			fmt.Println("client get: ", result.Result)
		}
		time.Sleep(3 * time.Second)
	}
}
