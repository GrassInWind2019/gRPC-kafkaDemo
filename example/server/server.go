package main

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/GrassInWind2019/gRPCwithConsul/example/proto"
	"github.com/GrassInWind2019/gRPCwithConsul/serviceDiscovery"
	"google.golang.org/grpc"
)

const (
	ip         = "127.0.0.1"
	port       = 10000
	consulPort = 8500
)

type server struct{}

func (s *server) SayHello(ctx context.Context, in *proto.HelloRequest) (*proto.HelloResponse, error) {
	fmt.Println("client called SayHello!")
	return &proto.HelloResponse{Result: "Hello! " + in.Name + "!"}, nil
}

func main() {
	lis, err := net.ListenTCP("tcp", &net.TCPAddr{net.ParseIP(ip), port, ""})
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
	proto.RegisterHelloServiceServer(s, &server{})
	if err := s.Serve(lis); err != nil {
		fmt.Println("serve failed: ", err.Error())
		return
	}
}
