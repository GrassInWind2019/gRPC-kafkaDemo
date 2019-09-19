package main

import (
	"context"
	"fmt"
	_ "net/http"
	"os"
	"os/signal"
	"runtime"
	_ "runtime/debug"
	"runtime/pprof"
	_ "strconv"
	"sync"
	"syscall"
	"time"

	"github.com/GrassInWind2019/gRPC-kafkaDemo/serviceDiscovery"
	"github.com/go-redis/redis"
)

const (
	ip         = "localhost"
	consulPort = 8500
)

//if put it in const, will meet compile error:  const initializer []int literal is not a constant
var (
	brokers    = ""
	topics     = ""
	port       = []int{10000, 10001}
	mainStopCh chan struct{}
	rdbOpt     = redis.Options{
		Addr:         ":6379",
		DialTimeout:  10 * time.Second,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		PoolSize:     10,
		PoolTimeout:  30 * time.Second,
		Password:     "123",
	}
)

type serverMonitor struct {
	servers []*CalculateServiceServer
	ctx     context.Context
	wg      *sync.WaitGroup
}

func (monitor *serverMonitor) faultSimulator() {
	monitor.wg.Add(1)
	defer monitor.wg.Done()
	t := time.NewTicker(20 * time.Second)
	for {
		select {
		case <-t.C:
			fmt.Println("time out! Stop servers!")
			for _, s := range monitor.servers {
				s.gServer.GracefulStop()
				fmt.Printf("server %s:%d graceful stopped!\n", s.info.Addr, s.info.Port)
			}
		case <-monitor.ctx.Done():
			fmt.Println("faultSimulator stopped!")
			return
		}
	}
}

func (monitor *serverMonitor) startNewServer(port int) {
	server := NewCalculateServiceServer(port)
	monitor.servers = append(monitor.servers, server)
	go StartCalculateServiceServer(server)
}

func (monitor *serverMonitor) CalculateServiceServerMonitor() {
	monitor.wg.Add(1)
	defer monitor.wg.Done()
	serverNum := 0
	for _, p := range port {
		serverNum++
		monitor.startNewServer(p)
	}
	for {
		for i := 0; i < serverNum; i++ {
			select {
			//hello service server fault happened, start new server as recovery
			case <-monitor.servers[i].ch:
				//simulate service address change with only port change
				port[i] += 5
				monitor.servers[i].info.Port = port[i]
				//need new a gRPC server after stopping old one, otherwise will meet gRPC error:
				//Server.RegisterService after Server.Serve for "CalculateService_proto.CalculateService"
				s := NewGRPCServer()
				monitor.servers[i].gServer = s
				ctx, cancel := context.WithCancel(context.Background())
				monitor.servers[i].Ctx = ctx
				monitor.servers[i].Cancel = cancel
				fmt.Printf("server %s:%d start as recovery!\n", monitor.servers[i].info.Addr, monitor.servers[i].info.Port)
				//start new server
				go StartCalculateServiceServer(monitor.servers[i])
			case <-monitor.ctx.Done():
				fmt.Println("CalculateServiceServerMonitor stopped!")
				return
			default:
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (monitor *serverMonitor) signalHandler(sig os.Signal) {
	fmt.Println("Receive signal: ", sig)
	//printStack()
	//notify clientMonitor to deregister services
	serviceDiscovery.SignalNotify()

	//wait clientMonitor finish work
	serviceDiscovery.SignalDone()
	for _, s := range monitor.servers {
		s.gServer.GracefulStop()
		fmt.Printf("server %s:%d graceful stopped!\n", s.info.Addr, s.info.Port)
	}
	time.Sleep(1 * time.Second)
	fmt.Println("signalHandler done! Exit!")
}

//print all goroutine stack
func printStack() {
	fmt.Println("Print stack!")
	//debug.PrintStack()
	buf := make([]byte, 1<<14)
	runtime.Stack(buf, true)
	fmt.Printf("\n%s\n", buf)
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintf(os.Stderr, "Usage: %s <broker> <topic>\n",
			os.Args[0])
		os.Exit(1)
	}
	brokers = os.Args[1]
	topics = os.Args[2]
	//get CPU numbers
	maxProcs := runtime.NumCPU()
	//set maxProcs goroutines can run concurrently
	runtime.GOMAXPROCS(maxProcs)
	cpuf, err := os.OpenFile("cpu.prof", os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		panic(err)
	}
	pprof.StartCPUProfile(cpuf)
	heapf, err := os.OpenFile("heap.prof", os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		panic(err)
	}

	err = serviceDiscovery.CreateConsulRegisterClient(fmt.Sprintf("%s:%d", ip, consulPort))
	if err != nil {
		fmt.Println(err.Error())
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	wg := &sync.WaitGroup{}
	monitor := new(serverMonitor)
	monitor.ctx = ctx
	monitor.wg = wg
	monitor.servers = make([]*CalculateServiceServer, 0)
	go monitor.faultSimulator()
	go monitor.CalculateServiceServerMonitor()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGSEGV, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGABRT)
	select {
	case sig := <-sigCh:
		cancel()
		monitor.signalHandler(sig)
	}

	//go http.ListenAndServe("localhost:9001", nil)
	//Wait all goroutines done
	wg.Wait()
	pprof.StopCPUProfile()
	cpuf.Close()
	pprof.WriteHeapProfile(heapf)
	heapf.Close()
	time.Sleep(1 * time.Second)
	fmt.Println("main terminated!")
	os.Exit(1)
}
