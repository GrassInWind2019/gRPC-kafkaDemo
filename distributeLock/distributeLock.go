package distributeLock

import (
	"log"
	"time"

	"github.com/go-redis/redis"
)

type DistributeLock struct {
	rdb *redis.Client
	key string
}

//TODO: 设置短过期时间，创建一个专用goroutine定期刷新过期时间

func NewDistributeLock(rdbOpt *redis.Options, lockName string) (lock *DistributeLock) {
	lock = new(DistributeLock)
	lock.rdb = redis.NewClient(rdbOpt)
	lock.key = lockName

	return lock
}

func (lock *DistributeLock) Lock() (bool, error) {
	val, err := lock.rdb.SetNX(lock.key, " ", 3*time.Second).Result()
	if err != nil {
		log.Printf("Lock: set lock %s failed", lock.key)
		return false, err
	}

	return val, nil
}

func (lock *DistributeLock) UnLock() (bool, error) {
	_, err := lock.rdb.Del(lock.key).Result()
	if err != nil {
		log.Printf("UnLock: del lock %s failed\n", lock.key)
		return false, err
	}
	return true, nil
}
