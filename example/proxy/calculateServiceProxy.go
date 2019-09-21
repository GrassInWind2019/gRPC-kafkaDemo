package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync/atomic"
	"time"

	_ "github.com/GrassInWind2019/gRPC-kafkaDemo/consumer"
	cpb "github.com/GrassInWind2019/gRPC-kafkaDemo/example/CalculateService_proto"
	"github.com/GrassInWind2019/gRPC-kafkaDemo/producer"
	"github.com/GrassInWind2019/gRPC-kafkaDemo/serviceDiscovery"
	"github.com/GrassInWind2019/gRPC-kafkaDemo/uniqueID"
	"github.com/fatih/color"
	"github.com/go-redis/redis"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

var (
	tokenProducerId       int32 = 0
	errMissingMetadata          = status.Errorf(codes.InvalidArgument, "missing metadata")
	errReqExceedLimitRate       = status.Errorf(codes.Unavailable, "service unavailabe")
)

type CalculateServiceServer struct {
	info       *serviceDiscovery.ServiceInfo
	gServer    *grpc.Server
	ch         chan struct{}
	limitCh    chan struct{}
	ksProducer *producer.KafkaSyncProducer
	rdb        *redis.Client
	Ctx        context.Context
	Cancel     context.CancelFunc
}

func (s *CalculateServiceServer) Calculate(ctx context.Context, in *cpb.CalculateRequest) (*cpb.CalculateResponse, error) {
	fmt.Printf("client %s called Calculate from Server: %s:%d\n", in.Name, s.info.Addr, s.info.Port)
	reqId, err := uniqueID.GetUniqueID(s.rdb, "req")
	if err != nil {
		fmt.Println("Calculate: Get unique ID failed: ", err.Error())
		return &cpb.CalculateResponse{Message: "Hello! " + in.Name + "!" + fmt.Sprintf("This is Server: %s:%d", s.info.Addr, s.info.Port), SuccessFlag: false}, nil
	}
	request := fmt.Sprintf("%d%s%d", in.Num1, in.Method, in.Num2)
	buf := fmt.Sprintf("%d:%s", reqId, request)
	//Try to get result from redis first
	val, err := s.rdb.Get(request).Result()
	if err == nil {
		//parse result and return to client
		result, err := strconv.ParseFloat(val, 32)
		if err == nil {
			color.Set(color.FgBlue, color.Bold)
			fmt.Printf("Get result from redis: %f, reqId %d, request: %s\n", result, reqId, buf)
			color.Unset()
			return &cpb.CalculateResponse{Message: "Hello! " + in.Name + "!" + fmt.Sprintf("This is Server: %s:%d", s.info.Addr, s.info.Port), SuccessFlag: true, Result: result}, nil
		} else {
			color.Set(color.FgRed, color.Bold)
			fmt.Printf("Get result from redis: Process failed for reqId %d, request: %s\n", reqId, buf)
			color.Unset()
			return &cpb.CalculateResponse{Message: "Hello! " + in.Name + "!" + fmt.Sprintf("This is Server: %s:%d", s.info.Addr, s.info.Port), SuccessFlag: false}, nil
		}
	}
	pubsub := s.rdb.Subscribe(fmt.Sprintf("%d", reqId))
	// Wait for confirmation that subscription is created before publishing anything.
	_, err = pubsub.Receive()
	if err != nil {
		panic(err)
	}
	defer pubsub.Close()
	defer pubsub.Unsubscribe(fmt.Sprintf("%d", reqId))
	//Go channel which receives messages
	//When pubsub is closed channel is closed too
	pubsubCh := pubsub.Channel()
	//Redis didn't have the result, produce request message to kafka
	if err := s.ksProducer.ProduceKafkaMessage(topics, fmt.Sprintf("%d", reqId), buf); err != nil {
		color.Set(color.FgRed, color.Bold)
		fmt.Printf("Calculate: produce request failed: %s\n", buf)
		color.Unset()
	}
	t := time.NewTimer(3 * time.Second)
	for {
		select {
		case <-t.C:
			color.Set(color.FgRed, color.Bold)
			fmt.Printf("time out! ReqId %d, request: %s\n", reqId, buf)
			color.Unset()
			return &cpb.CalculateResponse{Message: "Hello! " + in.Name + "!" + fmt.Sprintf("This is Server: %s:%d", s.info.Addr, s.info.Port), SuccessFlag: false}, errors.New("time out!")
		case msg := <-pubsubCh:
			result, err := strconv.ParseFloat(msg.Payload, 32)
			if err != nil {
				color.Set(color.FgRed, color.Bold)
				fmt.Printf("Process failed for reqId %d, request: %s\n", reqId, buf)
				color.Unset()
				s.rdb.Set(request, "Failure", 60*time.Second).Err()
				return &cpb.CalculateResponse{Message: "Hello! " + in.Name + "!" + fmt.Sprintf("This is Server: %s:%d", s.info.Addr, s.info.Port), SuccessFlag: false}, nil
			}
			color.Set(color.FgGreen, color.Bold)
			fmt.Printf("Get result: %f, reqId %d, request: %s\n", result, reqId, buf)
			color.Unset()
			val := fmt.Sprintf("%f", result)
			//if err = s.rdb.Do("set", request, val, "EX", "10").Err(); err != nil {
			if err = s.rdb.Set(request, val, 120*time.Second).Err(); err != nil {
				color.Set(color.FgYellow, color.Bold)
				fmt.Printf("Set %s=%f to redis failed: %s\n", request, result, err.Error())
				color.Unset()
			}
			color.Set(color.FgGreen, color.Bold)
			fmt.Printf("Set %s=%f to redis succcess\n", request, result)
			color.Unset()
			return &cpb.CalculateResponse{Message: "Hello! " + in.Name + "!" + fmt.Sprintf("This is Server: %s:%d", s.info.Addr, s.info.Port), SuccessFlag: true, Result: result}, nil

			/*default:
			val, err := s.rdb.Get(fmt.Sprintf("%d", reqId)).Result()
			if err == nil {
				result, err := strconv.ParseFloat(val, 32)
				if err != nil {
					color.Set(color.FgRed, color.Bold)
					fmt.Printf("Process failed for reqId %d, request: %s\n", reqId, buf)
					color.Unset()
					s.rdb.Set(request, "Failure", 60*time.Second).Err()
					return &cpb.CalculateResponse{Message: "Hello! " + in.Name + "!" + fmt.Sprintf("This is Server: %s:%d", s.info.Addr, s.info.Port), SuccessFlag: false}, nil
				}
				color.Set(color.FgGreen, color.Bold)
				fmt.Printf("Get result: %f, reqId %d, request: %s\n", result, reqId, buf)
				color.Unset()
				val := fmt.Sprintf("%f", result)
				//if err = s.rdb.Do("set", request, val, "EX", "10").Err(); err != nil {
				if err = s.rdb.Set(request, val, 120*time.Second).Err(); err != nil {
					color.Set(color.FgYellow, color.Bold)
					fmt.Printf("Set %s=%f to redis failed: %s\n", request, result, err.Error())
					color.Unset()
				}
				color.Set(color.FgGreen, color.Bold)
				fmt.Printf("Set %s=%f to redis succcess\n", request, result)
				color.Unset()
				return &cpb.CalculateResponse{Message: "Hello! " + in.Name + "!" + fmt.Sprintf("This is Server: %s:%d", s.info.Addr, s.info.Port), SuccessFlag: true, Result: result}, nil
			}*/
		}
	}

	return &cpb.CalculateResponse{Message: "Hello! " + in.Name + "!" + fmt.Sprintf("This is Server: %s:%d", s.info.Addr, s.info.Port), SuccessFlag: true}, nil
}

func StartCalculateServiceServer(server *CalculateServiceServer) {
	//notify CalculateServiceServerMonitor to do recovery
	defer func() {
		//stop tokenProducer
		server.Cancel()
		server.ch <- struct{}{}
	}()

	lis, err := net.Listen("tcp", fmt.Sprintf("%s:%d", server.info.Addr, server.info.Port))
	if err != nil {
		fmt.Println("listen failed: ", err.Error())
		return
	}

	err = serviceDiscovery.RegisterServiceToConsul(*server.info)
	if err != nil {
		fmt.Println(err.Error())
		return
	}
	defer serviceDiscovery.DeregisterServiceFromConsul(*server.info)
	cpb.RegisterCalculateServiceServer(server.gServer, server)
	go server.tokenProducer()
	if err := server.gServer.Serve(lis); err != nil {
		fmt.Println("serve failed: ", err.Error())
		return
	}
}

func NewGRPCServer() *grpc.Server {
	return grpc.NewServer(grpc.UnaryInterceptor(unaryInterceptor))
}

func NewCalculateServiceServer(port int) *CalculateServiceServer {
	s := grpc.NewServer(grpc.UnaryInterceptor(unaryInterceptor))
	info := &serviceDiscovery.ServiceInfo{
		Addr:           ip,
		Port:           port,
		ServiceName:    "CalculateService",
		UpdateInterval: 5 * time.Second,
		CheckInterval:  20}
	ch := make(chan struct{}, 1)
	limitCh := make(chan struct{}, 10)
	server := &CalculateServiceServer{
		info:    info,
		gServer: s,
		ch:      ch,
		limitCh: limitCh,
	}
	//TODO: support multi brokers
	ctx, cancel := context.WithCancel(context.Background())
	server.Ctx = ctx
	server.Cancel = cancel
	var err error
	server.ksProducer, err = producer.NewKafkaSyncProducer(ctx, brokers)
	if err != nil {
		panic(err)
	}
	server.rdb = redis.NewClient(&rdbOpt)
	return server
}

func unaryInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	// authentication (token verification)
	_, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, errMissingMetadata
	}
	if throttleHandler(info) == false {
		color.Set(color.FgHiRed, color.Bold)
		fmt.Printf("Request rate exceed server limts, reject!\n")
		color.Unset()
		return nil, errReqExceedLimitRate
	}
	m, err := handler(ctx, req)
	if err != nil {
		fmt.Printf("RPC failed with error %v\n", err)
	}
	return m, err
}

func throttleHandler(info *grpc.UnaryServerInfo) bool {
	s := info.Server.(*CalculateServiceServer)
	pass := false
	select {
	case <-s.limitCh:
		pass = true
	default:
	}
	return pass
}

func (s *CalculateServiceServer) tokenProducer() {
	id := atomic.AddInt32(&tokenProducerId, 1)
	fmt.Println("tokern producer start! ", id)
	t := time.NewTicker(100 * time.Millisecond)
	for {
		select {
		case <-t.C:
			//If bucket is full, token will been dropped
			select {
			case s.limitCh <- struct{}{}:
			default:
			}

		case <-s.Ctx.Done():
			fmt.Println("tokenProducer stopped! ", id)
			return
		}
	}
}
