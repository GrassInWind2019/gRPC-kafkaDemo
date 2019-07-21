package main

import (
	"context"
	"fmt"
	"net"
	"time"

	hpb "github.com/GrassInWind2019/gRPCwithConsul/example/HelloService_proto"
	"github.com/GrassInWind2019/gRPCwithConsul/serviceDiscovery"
	"google.golang.org/grpc"
)

const (
	ip         = "localhost"
	port       = 10000
	consulPort = 8500
)

type server struct{}

func (s *server) SayHello(ctx context.Context, in *hpb.HelloRequest) (*hpb.HelloResponse, error) {
	fmt.Printf("client %s called SayHello!\n", in.Name)
	return &hpb.HelloResponse{Message: "Hello! " + in.Name + "!", Result: in.Num1 + in.Num2}, nil
}

func main() {
	lis, err := net.Listen("tcp", fmt.Sprintf("%s:%d", ip, port))
	if err != nil {
		fmt.Println("listen failed: ", err.Error())
		return
	}

	s := grpc.NewServer()
	err = serviceDiscovery.CreateConsulRegisterClient(fmt.Sprintf("%s:%d", ip, consulPort))
	if err != nil {
		fmt.Println(err.Error())
		return
	}
	err = serviceDiscovery.RegisterServiceToConsul(serviceDiscovery.ServiceInfo{
		Addr:           ip,
		Port:           port,
		ServiceName:    "HelloService",
		UpdateInterval: 10 * time.Second,
		CheckInterval:  15})
	if err != nil {
		fmt.Println(err.Error())
		return
	}
	hpb.RegisterHelloServiceServer(s, &server{})
	if err := s.Serve(lis); err != nil {
		fmt.Println("serve failed: ", err.Error())
		return
	}
}
