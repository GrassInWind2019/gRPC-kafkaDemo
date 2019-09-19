package uniqueID

import (
	"log"

	"github.com/go-redis/redis"
)

func GetUniqueID(rdb *redis.Client, key string) (int64, error) {
	//get a unique number for file name prefix
	val, err := rdb.Incr(key).Result()
	if err != nil {
		log.Printf("getFileNamePrefix: redis increase %s failed %s", err.Error())
		return -1, err
	}
	log.Printf("GetUniqueID: key %s, ID %d\n", key, val)
	return val, nil
}
