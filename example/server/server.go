package main

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	hpb "github.com/GrassInWind2019/gRPCwithConsul/example/HelloService_proto"
	"github.com/GrassInWind2019/gRPCwithConsul/serviceDiscovery"
	"google.golang.org/grpc"
)

const (
	ip         = "localhost"
	consulPort = 8500
)

//if put it in const, will meet compile error:  const initializer []int literal is not a constant
var port = []int{10000, 10001}

type server struct {
	ip   string
	port int
}

func (s *server) SayHello(ctx context.Context, in *hpb.HelloRequest) (*hpb.HelloResponse, error) {
	fmt.Printf("client %s called SayHello from Server: %s:%d\n", in.Name, s.ip, s.port)
	return &hpb.HelloResponse{Message: "Hello! " + in.Name + "!" + fmt.Sprintf("This is Server: %s:%d", s.ip, s.port), Result: in.Num1 + in.Num2}, nil
}

func startHelloServiceServer(ip string, hsPort, consulPort int, wg *sync.WaitGroup) {
	defer wg.Done()

	lis, err := net.Listen("tcp", fmt.Sprintf("%s:%d", ip, hsPort))
	if err != nil {
		fmt.Println("listen failed: ", err.Error())
		return
	}

	s := grpc.NewServer()

	err = serviceDiscovery.RegisterServiceToConsul(serviceDiscovery.ServiceInfo{
		Addr:           ip,
		Port:           hsPort,
		ServiceName:    "HelloService",
		UpdateInterval: 10 * time.Second,
		CheckInterval:  15})
	if err != nil {
		fmt.Println(err.Error())
		return
	}
	hpb.RegisterHelloServiceServer(s, &server{ip: ip, port: hsPort})
	if err := s.Serve(lis); err != nil {
		fmt.Println("serve failed: ", err.Error())
		return
	}
}

func main() {
	var wg sync.WaitGroup
	err := serviceDiscovery.CreateConsulRegisterClient(fmt.Sprintf("%s:%d", ip, consulPort))
	if err != nil {
		fmt.Println(err.Error())
		return
	}
	for _, hsPort := range port {
		wg.Add(1)
		go startHelloServiceServer(ip, hsPort, consulPort, &wg)
	}
	//wait all goroutines done
	wg.Wait()
}
