package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	_ "net/http"
	"os"
	"os/signal"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/GrassInWind2019/gRPC-kafkaDemo/consumer"
	"github.com/GrassInWind2019/gRPC-kafkaDemo/producer"
	"github.com/fatih/color"
	"github.com/go-redis/redis"
)

// Sarma configuration options
var (
	brokers = ""
	version = ""
	group   = ""
	topics  = ""
	oldest  = true
	verbose = false
	rdb     *redis.Client
	rdbOpt  = redis.Options{
		Addr:         ":6379",
		DialTimeout:  10 * time.Second,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		PoolSize:     10,
		PoolTimeout:  30 * time.Second,
		Password:     "123",
	}
)

func init() {
	flag.StringVar(&brokers, "brokers", "", "Kafka bootstrap brokers to connect to, as a comma separated list")
	flag.StringVar(&group, "group", "", "Kafka consumer group definition")
	flag.StringVar(&version, "version", "2.1.1", "Kafka cluster version")
	flag.StringVar(&topics, "topics", "", "Kafka topics to be consumed, as a comma seperated list")
	flag.Parse()

	if len(brokers) == 0 {
		panic("no Kafka bootstrap brokers defined, please set the -brokers flag")
	}

	if len(topics) == 0 {
		panic("no topics given to be consumed, please set the -topics flag")
	}

	if len(group) == 0 {
		panic("no Kafka consumer group defined, please set the -group flag")
	}
	rdb = redis.NewClient(&rdbOpt)
}

type server struct {
	ctx    context.Context
	cancel context.CancelFunc
}

func (s *server) processReq(req string) (float32, error) {
	cnt1 := strings.Count(req, "+")
	cnt2 := strings.Count(req, "-")
	cnt3 := strings.Count(req, "*")
	cnt4 := strings.Count(req, "/")
	total := cnt1 + cnt2 + cnt3 + cnt4
	if total != 1 {
		return 0, errors.New("request is invalid")
	}
	method := ""
	if cnt1 == 1 {
		method = "+"
	}
	if cnt2 == 1 {
		method = "-"
	}
	if cnt3 == 1 {
		method = "*"
	}
	if cnt4 == 1 {
		method = "/"
	}
	str := strings.Split(req, method)
	if len(str) != 2 {
		return 0, errors.New("request is invalid")
	}
	num1, err := strconv.Atoi(str[0])
	if err != nil {
		return 0, errors.New("Invalid request")
	}
	num2, err := strconv.Atoi(str[1])
	if err != nil {
		return 0, errors.New("Invalid request")
	}
	var result float32
	if cnt1 == 1 {
		result = float32(num1 + num2)
	}
	if cnt2 == 1 {
		result = float32(num1 - num2)
	}
	if cnt3 == 1 {
		result = float32(num1) * float32(num2)
	}
	if cnt4 == 1 {
		result = float32(num1) / float32(num2)
	}
	return result, nil
}

//msg represent the client request
//If process request success, result will set to redis with [reqId, result]
//Otherwise will return error
func (s *server) ProcessTopic(topic, msg string, reqId int64) (int, error) {
	switch topic {
	case "sarama":
		res, err := s.processReq(msg)
		if err != nil {
			return 2, err
		}
		err = rdb.Publish(fmt.Sprintf("%d", reqId), fmt.Sprintf("%f", res)).Err()
		if err != nil {
			color.Set(color.FgYellow, color.Bold)
			log.Printf("ProcessTopic: Publish to redis failed, channel %d, value %f, %s\n", reqId, res, err.Error())
			color.Unset()
			return 2, err
		}
		/*err = rdb.Set(fmt.Sprintf("%d", reqId), fmt.Sprintf("%f", res), 120*time.Second).Err()
		if err != nil {
			color.Set(color.FgYellow, color.Bold)
			log.Printf("ProcessTopic: set to redis failed, key %d, value %f, %s\n", reqId, res, err.Error())
			color.Unset()
			return 2, err
		}*/
		color.Set(color.FgGreen, color.Bold)
		log.Printf("ProcessTopic: [%s] success, reqId %d, result %f", msg, reqId, res)
		color.Unset()
		return 0, nil
	case "GrassInWind2019":
		log.Println("ProcessTopic: GrassInWind2019: ", msg)
	default:
		errStr := fmt.Sprintf("unknown topic %s !", topic)
		return 0, errors.New(errStr)
	}
	return 0, nil
}

func (s *server) ProcessRetryFailure(topic, msg string, reqId int64) error {
	color.Set(color.FgHiRed, color.Bold)
	defer color.Unset()
	//err := rdb.Set(fmt.Sprintf("%d", reqId), "Failure", 0).Err()
	err := rdb.Publish(fmt.Sprintf("%d", reqId), "Failure").Err()
	if err != nil {
		log.Printf("ProcessDeadLetterTopic: set to redis failed, key %d, value %s\n", reqId, "Failure")
		return err
	}
	return nil
}

//For request process failed after retry, result will set to redis with [reqId, "Failure"]
func (s *server) ProcessDeadLetterTopic(topic, msg string, reqId int64) error {
	log.Printf("ProcessDeadLetterTopic: topic %s, ID %d, body: %s\n", topic, reqId, msg)
	return nil
}

func (s *server) CheckDone() bool {
	// check if context was cancelled, notify the consumer to stop
	if s.ctx.Err() != nil {
		return true
	}
	return false
}

func (s *server) Context() context.Context {
	return s.ctx
}

func main() {
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

	ctx, cancel := context.WithCancel(context.Background())
	s := &server{
		ctx:    ctx,
		cancel: cancel,
	}
	kafkaProducer, err := producer.NewKafkaSyncProducer(ctx, brokers)
	if err != nil {
		panic(err)
	}
	buffer := kafkaProducer.GetBuffer()
	if buffer == nil {
		panic("Kafka buffer is nil")
	}
	wg := &sync.WaitGroup{}
	wg.Add(1)
	kafkaConsumer, client := consumer.NewConsumer(brokers, group, topics, s, wg)
	kafkaConsumer.SetBuffer(buffer)
	<-kafkaConsumer.Ready // Await till the consumer has been set up
	log.Println("Sarama consumer up and running!...")

	//go http.ListenAndServe("localhost:9000", nil)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGSEGV, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGABRT)
	select {
	case <-ctx.Done():
		log.Println("terminating: context cancelled")
	case <-sigCh:
		log.Println("terminating: via signal")
	}
	//notify consumer to stop
	s.cancel()
	//wait consumer exit
	wg.Wait()
	pprof.StopCPUProfile()
	cpuf.Close()
	pprof.WriteHeapProfile(heapf)
	heapf.Close()
	if err := client.Close(); err != nil {
		log.Panicf("Error closing client: %v", err)
	}
	os.Exit(1)
}
