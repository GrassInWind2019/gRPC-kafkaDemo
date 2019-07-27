package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"runtime"
	_ "runtime/debug"
	"strconv"
	"syscall"
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
var mainStopCh chan struct{}

type hsServerMonitor struct {
	hsServers     []*helloServiceServer
	sigCh         chan os.Signal
	stopFaultCh   chan struct{}
	stopRecoverCh chan struct{}
	doneFaultCh   chan struct{}
	doneRecoverCh chan struct{}
}

type helloServiceServer struct {
	info    *serviceDiscovery.ServiceInfo
	gServer *grpc.Server
	ch      chan struct{}
}

func (s *helloServiceServer) SayHello(ctx context.Context, in *hpb.HelloRequest) (*hpb.HelloResponse, error) {
	fmt.Printf("client %s called SayHello from Server: %s:%d\n", in.Name, s.info.Addr, s.info.Port)
	return &hpb.HelloResponse{Message: "Hello! " + in.Name + "!" + fmt.Sprintf("This is Server: %s:%d", s.info.Addr, s.info.Port), Result: in.Num1 + in.Num2}, nil
}

func startHelloServiceServer(hsServer *helloServiceServer) {
	//notify helloServiceServerMonitor to do recovery
	defer func() { hsServer.ch <- struct{}{} }()

	lis, err := net.Listen("tcp", fmt.Sprintf("%s:%d", hsServer.info.Addr, hsServer.info.Port))
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

func (hssMonitor *hsServerMonitor) faultSimulator() {
	t := time.NewTicker(15 * time.Second)
	for {
		select {
		case <-t.C:
			fmt.Println("time out! Stop servers!")
			for _, s := range hssMonitor.hsServers {
				s.gServer.GracefulStop()
				fmt.Printf("server %s:%d graceful stopped!\n", s.info.Addr, s.info.Port)
			}
		case <-hssMonitor.stopFaultCh:
			fmt.Println("faultSimulator stopped!")
			hssMonitor.doneFaultCh <- struct{}{}
			return
		}
	}
}

func newHelloServiceServer(hsPort int) *helloServiceServer {
	s := grpc.NewServer()
	info := &serviceDiscovery.ServiceInfo{
		Addr:           ip,
		Port:           hsPort,
		ServiceName:    "HelloService",
		UpdateInterval: 5 * time.Second,
		CheckInterval:  20}
	ch := make(chan struct{}, 1)
	hsServer := &helloServiceServer{
		info:    info,
		gServer: s,
		ch:      ch}
	return hsServer
}

func (hssMonitor *hsServerMonitor) startNewServer(hsPort int) {
	hsServer := newHelloServiceServer(hsPort)
	hssMonitor.hsServers = append(hssMonitor.hsServers, hsServer)
	go startHelloServiceServer(hsServer)
}

func (hssMonitor *hsServerMonitor) helloServiceServerMonitor() {
	serverNum := 0
	for _, hsPort := range port {
		serverNum++
		hssMonitor.startNewServer(hsPort)
	}
	for {
		for i := 0; i < serverNum; i++ {
			select {
			//hello service server fault happened, start new server as recovery
			case <-hssMonitor.hsServers[i].ch:
				//delete the stopped grpc server
				fmt.Printf("server %s:%d deleted from slice!\n", hssMonitor.hsServers[i].info.Addr, hssMonitor.hsServers[i].info.Port)
				if i != len(hssMonitor.hsServers)-1 {
					hssMonitor.hsServers = append(hssMonitor.hsServers[:i], hssMonitor.hsServers[i+1:]...)
				} else {
					hssMonitor.hsServers = hssMonitor.hsServers[:i]
				}
				port[i] += 5
				hssMonitor.startNewServer(port[i])
			case <-hssMonitor.stopRecoverCh:
				fmt.Println("helloServiceServerMonitor stopped!")
				hssMonitor.doneRecoverCh <- struct{}{}
				return
			default:
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (hssMonitor *hsServerMonitor) signalHandler() {
	signal.Notify(hssMonitor.sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGSEGV, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGABRT)
	sig := <-hssMonitor.sigCh
	fmt.Println("Receive signal: ", sig)
	printStack()
	//notify clientMonitor to deregister services
	serviceDiscovery.SignalNotify()
	//stop faultSimulator goroutine
	hssMonitor.stopFaultCh <- struct{}{}
	//stop helloServiceServerMonitor goroutine
	hssMonitor.stopRecoverCh <- struct{}{}

	//wait clientMonitor finish work
	serviceDiscovery.SignalDone()
	//wait faultSimulator finish
	<-hssMonitor.doneFaultCh
	//wait helloServiceServerMonitor finish
	<-hssMonitor.doneRecoverCh
	fmt.Println("signalHandler done! Exit!")
	mainStopCh <- struct{}{}
	s, _ := strconv.Atoi(fmt.Sprintf("%d", sig))
	os.Exit(s)
}

func printStack() {
	fmt.Println("Print stack!")
	//debug.PrintStack()
	buf := make([]byte, 1<<14)
	runtime.Stack(buf, true)
	fmt.Printf("\n%s\n", buf)
}

func main() {
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
	hssMonitor.sigCh = make(chan os.Signal, 1)
	hssMonitor.stopFaultCh = make(chan struct{}, 1)
	hssMonitor.stopRecoverCh = make(chan struct{}, 1)
	hssMonitor.doneFaultCh = make(chan struct{}, 1)
	hssMonitor.doneRecoverCh = make(chan struct{}, 1)
	go hssMonitor.faultSimulator()
	go hssMonitor.helloServiceServerMonitor()
	go hssMonitor.signalHandler()

	mainStopCh = make(chan struct{}, 1)
	<-mainStopCh
	fmt.Println("main terminated!")
	/*for {
	}*/
}
