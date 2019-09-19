package producer

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Shopify/sarama"
	"github.com/fatih/color"
	"github.com/go-redis/redis"
)

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

type KafkaSyncProducer struct {
	producer sarama.SyncProducer
	buffer   chan []string
}

func NewKafkaSyncProducer(ctx context.Context, brokers string) (*KafkaSyncProducer, error) {
	config := sarama.NewConfig()
	//Retry up to 5 times to produce the message
	config.Producer.Retry.Max = 5
	//Wait for all in-sync replicas to ack the message
	config.Producer.RequiredAcks = sarama.WaitForAll
	//Producer.Return.Successes must be true to be used in a SyncProducer
	config.Producer.Return.Successes = true
	producer, err := sarama.NewSyncProducer(strings.Split(brokers, ","), config)
	if err != nil {
		return nil, err
	}
	ksProducer := new(KafkaSyncProducer)
	ksProducer.producer = producer
	ksProducer.buffer = make(chan []string, 100)
	go ksProducer.Producer(ctx)

	return ksProducer, nil
}

//This buffer can be used for consumer to put retry/dead letter/result message.
func (ksProducer *KafkaSyncProducer) GetBuffer() chan []string {
	return ksProducer.buffer
}

func (ksProducer *KafkaSyncProducer) Producer(ctx context.Context) {
	log.Println("Kafka Producer start!")
	for {
		select {
		//structure of buffer member: [topic][key][message]
		//message protocol: [topic type][retry count][reqID][req/rep]
		case message := <-ksProducer.buffer:
			if err := ksProducer.produceKafkaMessage(message[0], message[1], message[2]); err != nil {
				log.Println("Produce failed: ", message, err.Error())
			}

		case <-ctx.Done():
			log.Println("Kafka Producer exit!")
			return
		}
	}
	log.Println("Kafka Producer exit!")
}

//message protocol: [topic type][retry count][reqID][req/rep]
func (ksProducer *KafkaSyncProducer) ProduceKafkaMessage(topic, key, val string) error {
	val = "Normal:0:" + val
	return ksProducer.produceKafkaMessage(topic, key, val)
}

func (ksProducer *KafkaSyncProducer) ProduceKafkaMessages(topics, keys, vals []string) (int, error) {
	for _, val := range vals {
		val += "Normal:0:" + val
	}
	return ksProducer.produceKafkaMessages(topics, keys, vals)
}

func (ksProducer *KafkaSyncProducer) produceKafkaMessage(topic, key, val string) error {
	msg := &sarama.ProducerMessage{
		Topic: topic,
		Key:   sarama.StringEncoder(key),
		Value: sarama.StringEncoder(val),
	}
	partition, offset, err := ksProducer.producer.SendMessage(msg)
	if err != nil {
		fmt.Printf("Produce msg for topic %s [%s] failed\n", msg.Topic, msg.Value)
		return err
	}
	color.Set(color.FgGreen, color.Bold)
	defer color.Unset()
	fmt.Printf("Produce msg topic %s partition %d offset %d: %s\n", msg.Topic, partition, offset, msg.Value)
	return nil
}

func (ksProducer *KafkaSyncProducer) produceKafkaMessages(topics, keys, vals []string) (int, error) {
	if len(topics) != len(keys) || len(topics) != len(vals) {
		return 0, errors.New("The length of topic/key/val isn't equal!")
	}
	for i := 0; i < len(topics); i++ {
		if err := ksProducer.ProduceKafkaMessage(topics[i], keys[i], vals[i]); err != nil {
			return i, err
		}
	}
	return len(topics), nil
}

func (ksProducer *KafkaSyncProducer) Close() error {
	return ksProducer.producer.Close()
}
