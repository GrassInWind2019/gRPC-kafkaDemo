package redisQueue

import (
	"log"
	"time"

	"github.com/go-redis/redis"
)

type RetryQueue struct {
	curSize int64
	maxSize int64
	rqName  string
	rdb     *redis.Client
}

func (rq *RetryQueue) CreateQueue(rdbOpt *redis.Options, maxSize int64, queueName string) {
	rq.rdb = redis.NewClient(rdbOpt)
	rq.maxSize = maxSize
	rq.rqName = queueName
}

//not concurrent safe
func (rq *RetryQueue) EnQueue(msg string) error {
	if rq.curSize < rq.maxSize {
		if err := rq.rdb.LPush(rq.rqName, msg).Err(); err != nil {
			return err
		}
		rq.curSize++
	} else {
		size, err := rq.Size()
		if err != nil {
			return err
		}
		rq.curSize = size
		if size < rq.maxSize {
			return rq.EnQueue(msg)
		}
		log.Printf("EnQueue: queue %s is full, oldest member will be poped!\n", rq.rqName)
		if err := rq.rdb.RPopLPush(rq.rqName, msg).Err(); err != nil {
			return err
		}
		if rq.curSize > rq.maxSize {
			//pop one message in queue due to over limit
			if err := rq.rdb.RPop(rq.rqName).Err(); err == nil {
				rq.curSize--
			}
		}
	}

	log.Printf("Enqueue: queue: %s, msg: %s\n", rq.rqName, msg)

	return nil
}

//not concurrent safe
func (rq *RetryQueue) DeQueue() (string, error) {
	result, err := rq.rdb.BRPop(3*time.Second, rq.rqName).Result()
	if err != nil {
		return "", err
	}
	if rq.curSize > rq.maxSize {
		//pop one additional message in queue due to over limit
		if err := rq.rdb.RPop(rq.rqName).Err(); err == nil {
			rq.curSize--
		}
	}

	rq.curSize--
	log.Printf("Dequeue: queue: %s, value: %s\n", result[0], result[1])
	return result[1], err
}

func (rq *RetryQueue) Size() (int64, error) {
	return rq.rdb.LLen(rq.rqName).Result()
}
