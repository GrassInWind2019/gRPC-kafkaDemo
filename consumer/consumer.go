package consumer

import (
	"context"
	"errors"
	"fmt"
	"log"
	_ "math/rand"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Shopify/sarama"
	"github.com/fatih/color"
	"github.com/go-redis/redis"
)

// Sarma configuration options
var (
	rdb    *redis.Client
	rdbOpt = redis.Options{
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
	rdb = redis.NewClient(&rdbOpt)
}

type APP interface {
	ProcessTopic(topic, msg string, reqId int64) (maxRetryCnt int, err error)
	ProcessRetryFailure(topic, msg string, reqId int64) error
	ProcessDeadLetterTopic(topic, msg string, reqId int64) error
	Context() context.Context
	//for consumer goroutine to check exit
	CheckDone() bool
}

func NewConsumer(brokers, group, topics string, app APP, wg *sync.WaitGroup) (*Consumer, sarama.ConsumerGroup) {
	log.Println("Starting a new Sarama consumer")

	verbose := false
	if verbose {
		sarama.Logger = log.New(os.Stdout, "[sarama] ", log.LstdFlags)
	}
	m_version := "2.1.1"
	version, err := sarama.ParseKafkaVersion(m_version)
	if err != nil {
		log.Panicf("Error parsing Kafka version: %v", err)
	}

	/**
	 * Construct a new Sarama configuration.
	 * The Kafka cluster version has to be defined before the consumer/producer is initialized.
	 */
	config := sarama.NewConfig()
	config.Version = version
	config.Net.ReadTimeout = 20 * time.Second
	config.Net.WriteTimeout = 20 * time.Second
	config.Consumer.MaxWaitTime = 20 * time.Second
	//config.Consumer.Offsets.Initial = sarama.OffsetOldest

	topicSlice := []string{}
	for _, topic := range strings.Split(topics, ",") {
		topicRetry := "Retry-" + topic
		topicDeadLetter := "DeadLetter-" + topic
		topicSlice = append(topicSlice, topic)
		topicSlice = append(topicSlice, topicRetry)
		topicSlice = append(topicSlice, topicDeadLetter)
	}

	/**
	 * Setup a new Sarama consumer group
	 */
	consumer := Consumer{
		Ready:  make(chan bool),
		topics: topicSlice,
		app:    app,
		o:      &sync.Once{},
	}

	client, err := sarama.NewConsumerGroup(strings.Split(brokers, ","), group, config)
	if err != nil {
		log.Panicf("Error creating consumer group client: %v", err)
	}

	go func() {
		defer wg.Done()
		for {
			if err := client.Consume(app.Context(), consumer.topics, &consumer); err != nil {
				log.Panicf("Error from consumer: %v", err)
			}
			// check whether finished work
			if app.CheckDone() {
				log.Println("Consumer exit!")
				return
			}
			consumer.Ready = make(chan bool)
		}
	}()
	return &consumer, client
}

// Consumer represents a Sarama consumer group consumer
type Consumer struct {
	Ready  chan bool
	topics []string
	app    APP
	buffer chan []string
	o      *sync.Once
}

func closeChannel(ch chan bool, o *sync.Once) {
	o.Do(func() {
		close(ch)
	})
}

// Setup is run at the beginning of a new session, before ConsumeClaim
func (consumer *Consumer) Setup(sess sarama.ConsumerGroupSession) error {
	m_partitions := sess.Claims()
	log.Println("\n\nSetup: ", m_partitions)
	log.Println()
	// Mark the consumer as ready
	defer closeChannel(consumer.Ready, consumer.o)
	for _, topic := range consumer.topics {
		partitions := m_partitions[topic]
		log.Printf("Setup: topic:%s, claim partition: ", topic)
		log.Println(partitions)
		for _, partNo := range partitions {
			offset, hwOffset, err := consumer.getOffset(fmt.Sprintf("%s-%d", topic, partNo))
			//No record value in redis for the first time run
			if err != nil {
				log.Println("Setup: ", err)
				return nil
			}
			log.Printf("Setup: topic: %s, partition: %d, offset: %d, HighWaterOffset: %d\n", topic, partNo, offset, hwOffset)
			//HighWaterOffset is next produce offset
			if offset >= hwOffset {
				log.Println("Setup: record offset is invalid!")
				return nil
			}
			//if the offset record in redis smaller than the record in zookeeper, then need use ResetOffset
			//if the offset record in redis bigger than the record in zookeeper, then need use MarkOffset
			sess.ResetOffset(topic, partNo, offset, "")
			sess.MarkOffset(topic, partNo, offset, "")
			log.Printf("\n\nSetup: reset topic %s partition %d offset to %d\n\n", topic, partNo, offset)
		}
	}

	return nil
}

// Cleanup is run at the end of a session, once all ConsumeClaim goroutines have exited
func (consumer *Consumer) Cleanup(sess sarama.ConsumerGroupSession) error {
	return nil
}

// ConsumeClaim must start a consumer loop of ConsumerGroupClaim's Messages().
func (consumer *Consumer) ConsumeClaim(session sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
	// NOTE:
	// Do not move the code below to a goroutine.
	// The `ConsumeClaim` itself is called within a goroutine, see:
	// https://github.com/Shopify/sarama/blob/master/consumer_group.go#L27-L29
	for message := range claim.Messages() {
		log.Printf("Message claimed: value = %s, timestamp = %v, topic = %s, partition=%d, offset=%d",
			string(message.Value), message.Timestamp, message.Topic, message.Partition, message.Offset)
		session.MarkMessage(message, "")
		consumer.recordOffset(message.Topic, message.Partition, message.Offset, claim.HighWaterMarkOffset())
		consumer.ProcessMessage(message.Topic, string(message.Value))
	}

	return nil
}

func (consumer *Consumer) recordOffset(topic string, partition int32, offset, hwOffset int64) {
	err := rdb.Set(fmt.Sprintf("%s-%d", topic, partition), fmt.Sprintf("%d:%d", offset, hwOffset), 0).Err()
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("recordOffset: topic: %s, partition: %d, offset: %d, HighWaterOffset: %d\n", topic, partition, offset, hwOffset)
}

func (consumer *Consumer) getOffset(key string) (offset, hwOffset int64, err error) {
	value, err := rdb.Get(key).Result()
	if err != nil {
		return 0, 0, err
	}
	valueSlice := strings.Split(value, ":")
	offset, err = strconv.ParseInt(valueSlice[0], 10, 64)
	if err != nil {
		return 0, 0, err
	}
	hwOffset, err = strconv.ParseInt(valueSlice[1], 10, 64)
	if err != nil {
		return 0, 0, err
	}
	return
}

//This method used to set a buffer for consumer to put retry/dead letter/result message.
//structure of buffer member: [topic][reqID][message]
//message protocol: [topic type][retry count][reqID][req]
func (consumer *Consumer) SetBuffer(buf chan []string) {
	consumer.buffer = buf
}

//message protocol: [topic type][retry count][reqID][req]
func (consumer *Consumer) ProcessMessage(topic, msg string) {
	message := strings.Split(msg, ":")
	reqId, err := strconv.ParseInt(message[2], 10, 64)
	if err != nil {
		log.Printf("ProcessMessage: request ID is invalid, message: %s\n", msg)
		return
	}
	switch message[0] {
	case "Normal":
		consumer.processNormalTopic(topic, message[3], reqId)
	case "Retry":
		color.Set(color.FgYellow, color.Bold)
		retryCnt, err := strconv.Atoi(message[1])
		if err != nil {
			log.Printf("ProcessMessage: retry count is invalid, message: %s\n", msg)
			return
		}
		topic := strings.Split(topic, "Retry-")
		log.Printf("ProcessMessage: Retry topic is %s, message: %s\n", topic[1], msg)
		color.Unset()
		consumer.processRetryTopic(topic[1], message[3], retryCnt, reqId)
	case "DeadLetter":
		consumer.processDeadLetterTopic(topic, message[3], reqId)
	default:
		errStr := fmt.Sprintf("unknown topic type %s !", message[0])
		log.Println(errStr)
	}
}

func (consumer *Consumer) processNormalTopic(topic, msg string, reqId int64) {
	retryCnt, err := consumer.app.ProcessTopic(topic, msg, reqId)
	if err != nil {
		color.Set(color.FgYellow, color.Bold)
		log.Printf("processNormalTopic: process [%s] failed\n", msg)
		color.Unset()
		if retryCnt > 0 {
			msg = fmt.Sprintf("%s:%d:%d:%s", "Retry", retryCnt, reqId, msg)
			key := fmt.Sprintf("%d", reqId)
			topic = "Retry-" + topic
			message := []string{topic, key, msg}
			//Put retry message to buffer, producer will put them to retry topic
			consumer.putToBuffer(message)
		}
	}
}

func (consumer *Consumer) processRetryTopic(topic, msg string, retryCnt int, reqId int64) {
	var err error
	for i := 0; i < retryCnt; i++ {
		if _, err = consumer.app.ProcessTopic(topic, msg, reqId); err == nil {
			color.Set(color.FgGreen, color.Bold)
			log.Printf("processRetryTopic: process reqId %d [%s] success\n", reqId, msg)
			color.Unset()
			return
		}
	}
	color.Set(color.FgRed, color.Bold)
	log.Printf("Process reqId %d [%s] %d times still failed: %s\n", reqId, msg, retryCnt, err.Error())
	color.Unset()
	if err = consumer.app.ProcessRetryFailure(topic, msg, reqId); err != nil {
		color.Set(color.FgRed, color.Bold)
		log.Printf("Call APP ProcessRetryFailure for reqId %d failed\n", reqId)
		color.Unset()
	}
	msg = fmt.Sprintf("%s:%d:%d:%s", "DeadLetter", retryCnt, reqId, msg)
	key := fmt.Sprintf("%d", reqId)
	topic = "DeadLetter-" + topic
	message := []string{topic, key, msg}
	//Put DeadLetter message to buffer, producer will put them to dead letter topic
	consumer.putToBuffer(message)
}

func (consumer *Consumer) processDeadLetterTopic(topic, msg string, reqId int64) {
	consumer.app.ProcessDeadLetterTopic(topic, msg, reqId)
}

func (consumer *Consumer) putToBuffer(msg []string) error {
	if consumer.buffer == nil {
		log.Println("Buffer is nil, need set it before using consumer!")
		return errors.New("Buffer is nil, need set it before using consumer!")
	}
	t := time.NewTimer(1 * time.Second)
	select {
	case consumer.buffer <- msg:
		color.Set(color.FgYellow, color.Bold)
		log.Println("putToBuffer success: ", msg)
		color.Unset()
		return nil
	case <-t.C:
		color.Set(color.FgMagenta, color.Bold)
		log.Println("putToBuffer: timeout! buffer is full!")
		color.Unset()
		return errors.New("timeout!")
	}
}
