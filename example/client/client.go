package main

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"runtime"
	"time"

	cpb "github.com/GrassInWind2019/gRPC-kafkaDemo/example/CalculateService_proto"
	"github.com/GrassInWind2019/gRPC-kafkaDemo/serviceDiscovery"
	"github.com/fatih/color"
	"google.golang.org/grpc"
	"google.golang.org/grpc/balancer/roundrobin"
)

const (
	consulScheme = "consul"
	defaultName  = "GrassInWind2019"
	timeout      = 60 * time.Second
)

func main() {
	//get CPU numbers
	maxProcs := runtime.NumCPU()
	//set maxProcs goroutines can run concurrently
	runtime.GOMAXPROCS(maxProcs)
	err := serviceDiscovery.ConsulResolverInit("localhost:8500", "CalculateService")
	if err != nil {
		fmt.Println("ConsulResolverInit failed: ", err.Error())
		return
	}

	//Connect to the server
	conn, err := grpc.Dial(fmt.Sprintf("%s:///CalculateService", consulScheme), grpc.WithInsecure(), grpc.WithBalancerName(roundrobin.Name))
	if err != nil {
		fmt.Println("dial to CalculateService failed: %v", err)
		return
	}
	defer conn.Close()

	client := cpb.NewCalculateServiceClient(conn)

	//name will be used as request to server
	name := defaultName
	if len(os.Args) > 1 {
		name = os.Args[1]
	}

	for {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		rand.Seed(time.Now().UnixNano())
		methods := []byte("+-*/")
		method := string(methods[rand.Intn(len(methods))])
		num1 := rand.Intn(20)
		num2 := rand.Intn(20)

		if rand.Intn(100) < 20 {
			//simulate invalid request
			invalidMethods := []string{"&&&", "--", "++", "**", "//", "^^^"}
			method = invalidMethods[rand.Intn(len(invalidMethods))]
			color.Set(color.FgRed, color.Bold)
			fmt.Printf("Simulate invalid request: %d %s %d\n", num1, method, num2)
			color.Unset()
		}

		result, err := client.Calculate(ctx, &cpb.CalculateRequest{Name: name, Method: method, Num1: int32(num1), Num2: int32(num2)})
		if err != nil {
			color.Set(color.FgRed, color.Bold)
			fmt.Println("client call Calculate failed: %v", err)
			color.Unset()
		} else {
			if result.SuccessFlag {
				color.Set(color.FgGreen, color.Bold)
				fmt.Printf("client get message: %s, %s, result: %f\n", result.Message, fmt.Sprintf("Process %d %s %d success", num1, method, num2), result.Result)
				color.Unset()
			} else {
				color.Set(color.FgYellow, color.Bold)
				fmt.Printf("client get message: %s, %s\n", result.Message, fmt.Sprintf("Process %d %s %d failed", num1, method, num2))
				color.Unset()
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
}
