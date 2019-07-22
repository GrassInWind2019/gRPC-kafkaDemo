package main

import (
	"context"
	"fmt"
	"net"
	"runtime"
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

type hsServerMonitor struct {
	hsServers []*helloServiceServer
}

type helloServiceServer struct {
	ip      string
	port    int
	info    *serviceDiscovery.ServiceInfo
	gServer *grpc.Server
}

func (s *helloServiceServer) SayHello(ctx context.Context, in *hpb.HelloRequest) (*hpb.HelloResponse, error) {
	fmt.Printf("client %s called SayHello from Server: %s:%d\n", in.Name, s.ip, s.port)
	return &hpb.HelloResponse{Message: "Hello! " + in.Name + "!" + fmt.Sprintf("This is Server: %s:%d", s.ip, s.port), Result: in.Num1 + in.Num2}, nil
}

func startHelloServiceServer(ip string, hsPort int, wg *sync.WaitGroup, hsServer *helloServiceServer) {
	defer wg.Done()

	lis, err := net.Listen("tcp", fmt.Sprintf("%s:%d", ip, hsPort))
	if err != nil {
		fmt.Println("listen failed: ", err.Error())
		return
	}

	err = serviceDiscovery.RegisterServiceToConsul(*hsServer.info)
	if err != nil {
		fmt.Println(err.Error())
		return
	}
	defer serviceDiscovery.DeregisterServiceFromConsul(*hsServer.info)
	hpb.RegisterHelloServiceServer(hsServer.gServer, hsServer)
	if err := hsServer.gServer.Serve(lis); err != nil {
		fmt.Println("serve failed: ", err.Error())
		return
	}
}

func (hssMonitor *hsServerMonitor) helloServiceServerMonitor() {
	t := time.NewTimer(10 * time.Second)
	for {
		<-t.C
		for _, s := range hssMonitor.hsServers {
			s.gServer.GracefulStop()
			fmt.Printf("server %s:%d graceful stopped!\n", s.ip, s.port)
		}
	}
}

func main() {
	var wg sync.WaitGroup
	//get CPU numbers
	maxProcs := runtime.NumCPU()
	//set maxProcs goroutines can run concurrently
	runtime.GOMAXPROCS(maxProcs)
	err := serviceDiscovery.CreateConsulRegisterClient(fmt.Sprintf("%s:%d", ip, consulPort))
	if err != nil {
		fmt.Println(err.Error())
		return
	}
	hssMonitor := new(hsServerMonitor)
	hssMonitor.hsServers = make([]*helloServiceServer, 0)
	t := time.NewTimer(15 * time.Second)
	for {
		for i, hsPort := range port {
			wg.Add(1)
			port[i] += 5
			s := grpc.NewServer()
			info := &serviceDiscovery.ServiceInfo{
				Addr:           ip,
				Port:           hsPort,
				ServiceName:    "HelloService",
				UpdateInterval: 10 * time.Second,
				CheckInterval:  15}
			hsServer := &helloServiceServer{
				ip:      ip,
				port:    hsPort,
				info:    info,
				gServer: s}
			hssMonitor.hsServers = append(hssMonitor.hsServers, hsServer)
			go startHelloServiceServer(ip, hsPort, &wg, hsServer)
		}
		<-t.C
		fmt.Println("time out! Stop servers!")
		for _, s := range hssMonitor.hsServers {
			s.gServer.GracefulStop()
			fmt.Printf("server %s:%d graceful stopped!\n", s.ip, s.port)
		}
		//wait all goroutines done
		wg.Wait()
	}
}
