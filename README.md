# 目录
- [gRPC/consul/kafka简介](#gRPC/consul/kafka简介)
- [gRPC+kafka的Demo](#gRPC+kafka的Demo)
  * [gRPC+kafka整体示意图](#gRPC+kafka整体示意图)
  * [限流器](#限流器)
  * [基于redis计数器生成唯一ID](#基于redis计数器生成唯一ID)
  * [kafka生产消费](#kafka生产消费)
  * * [kafka生产消费示意图](#kafka生产消费示意图)
  * * [本文kafka生产消费过程](#本文kafka生产消费过程)
  * [基于pprof的性能分析Demo](#基于pprof的性能分析Demo)
  * * [使用pprof统计CPU/HEAP数据的code的example](#使用pprof统计CPU/HEAP数据的code的example)
  * * [基于redis的Set/Get方式的pprof的CPU火焰图](#基于redis的Set/Get方式的pprof的CPU火焰图)
  * * [基于redis的PUB/SUB方式的pprof的CPU火焰图](#基于redis的PUB/SUB方式的pprof的CPU火焰图)
  * [RPC接口](#RPC接口)
  * [client](#client)
  * [proxy](#proxy)
  * [server](#server)
- [本文github链接](#本文github链接)
- [运行Demo简介](#运行Demo简介)
  * [需要下载安装的如下](#需要下载安装的如下)
  * [运行步骤](#运行步骤)
  * [修改RPC接口](#修改RPC接口)
  * [运行结果](#运行结果)
  * * [client](#client)
  * * [proxy](#proxy)
  * * [server](#server)
# gRPC/consul/kafka简介  
consul是一个分布式的基于数据中心的服务发现框架，常用的其他服务发现框架有zookeeper, etcd, eureka。  
gRPC是google开源的一个高性能、通用的RPC框架，基于http2、protobuf设计开发，是一个跨编程语言的RPC框架，跨编程语言让client和server可以采用不同的编程语言开发。 
kafka是一个分布式的流处理平台。它可以用于两大类别的应用：  
1.构造实时流数据管道，它可以在系统或应用之间可靠地获取数据。（相当于message queue）  
2.构建实时流式应用程序，对这些流数据进行转换或者影响。（就是流处理，通过kafka stream topic和topic之间内部进行变化）  
kafka中文文档官网：http://kafka.apachecn.org/  
常用的其他消息队列框架有：ActiveMQ,RabbitMQ,RocketMQ.  

本文使用consul做服务发现，gRPC来处理RPC(Remote Procedure Call)即远程过程调用，消息队列框架采用了kafka，数据存储采用了redis。  

# gRPC+kafka Demo  
本文使用的注记说明：  
funcA()-->funcB()-->funcC()  
&emsp;&emsp;&emsp;-->funcD()-->funcE()  
funcC()-->funcF()  
&emsp;&emsp;&emsp;-->funcG()  
上面的函数调用关系为：funcA按序调用了funcB和funcD，funcB调用了funcC,funcD直接调用了funcE, funcC调用了funcF和funcG。 

## gRPC+kafka整体示意图
![gRPC-kafkaDemo.png](https://github.com/GrassInWind2019/gRPC-kafkaDemo/blob/master/image/gRPC-kafkaDemo.png)

## 限流器  
基于gRPC拦截器和令牌桶算法实现了一个简易的限流器。相关code如下：  
color.Set()是一个设置屏幕输出颜色的函数，由github.com/fatih/color提供。  
```
//gRPC拦截器
func unaryInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	//调用限流器handler
	if throttleHandler(info) == false {
		color.Set(color.FgHiRed, color.Bold)  //设置打印颜色
		fmt.Printf("Request rate exceed server limts, reject!\n")
		color.Unset()
		return nil, errReqExceedLimitRate
	}
	//调用RPC handler
	m, err := handler(ctx, req)
	...
}
//限流器
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
//令牌产生器
//simulate produce one token per 0.1 second
func (s *CalculateServiceServer) tokenProducer() {
	t := time.NewTicker(100 * time.Millisecond)
	for {
		select {
		case <-t.C:
			//If bucket is full, token will been dropped
			select {
			case s.limitCh <- struct{}{}:
			default:
			}
		...
		}
	}
}
```

## 基于redis计数器生成唯一ID  
相关code如下:  
```  
func GetUniqueID(rdb *redis.Client, key string) (int64, error) {
	//get a unique number for file name prefix
	val, err := rdb.Incr(key).Result()
	if err != nil {
		return -1, err
	}
	return val, nil
}
```  
## kafka生产消费  
### kafka生产消费示意图  
![kafka生产消费示意图.png](https://github.com/GrassInWind2019/gRPC-kafkaDemo/blob/master/image/kafka生产消费示意图.png)
### 本文kafka生产消费过程  
proxy将client请求按[topic type][retry count][reqID][req]编码为一条消息发送到kafka，server从kafka获取client请求然后处理，若处理失败发送到对应的Retry topic，如果Retry处理后依旧失败，则发送到对应的Dead Letter topic。  
本文通过将kafka consumer消费的offset保存到redis中，并在consumer下次重启后从redis中恢复offset来继续消费，从而避免重复消费。（sarama实现的kafka consumer是有一个专门的goroutine定时flush commmited offset,发生重启后sarama从zookeeper获取offset可能有较大偏差。）  
proxy在将请求发送到kafka之前，会在redis订阅以请求ID为名的channel,server对于处理完的请求，将相应的处理结果publish到redis对应的channel。  
相关code如下:  
```
//proxy
//producer
func (s *CalculateServiceServer) Calculate(ctx context.Context, in *cpb.CalculateRequest) (*cpb.CalculateResponse, error) {
	...
	//在redis中订阅以reqId为名的channel
	pubsub := s.rdb.Subscribe(fmt.Sprintf("%d", reqId))
	// 等待redis订阅成功
	_, err = pubsub.Receive()
	if err != nil {
	...
	}
	defer pubsub.Close()
	defer pubsub.Unsubscribe(fmt.Sprintf("%d", reqId))
	//Go channel which receives messages
	//When pubsub is closed channel is closed too
	pubsubCh := pubsub.Channel()
	//将client请求编码为消息后发送到kafka
	if err := s.ksProducer.ProduceKafkaMessage(topics, fmt.Sprintf("%d", reqId), buf); err != nil {
	...
	}
	t := time.NewTimer(3 * time.Second)
	for {
		select {
		case <-t.C:
			...
		case msg := <-pubsubCh:
			result, err := strconv.ParseFloat(msg.Payload, 32)
			if err != nil {
			...
			}
			val := fmt.Sprintf("%f", result)
			//将server处理结果缓存到redis中
			if err = s.rdb.Set(request, val, 120*time.Second).Err(); err != nil {
			...
			}
			return &cpb.CalculateResponse{Message: "Hello! " + in.Name + "!" + fmt.Sprintf("This is Server: %s:%d", s.info.Addr, s.info.Port), SuccessFlag: true, Result: result}, nil
		}
	}
}
//server
//consumer
//在sarama的每次session建立后，在consume执行前，根据redis记录的offset接着消费
func (consumer *Consumer) Setup(sess sarama.ConsumerGroupSession) error {
	//获取session消费的topic有哪些分区
	m_partitions := sess.Claims()
	...
	for _, topic := range consumer.topics {
		for _, partNo := range partitions {
			offset, hwOffset, err := consumer.getOffset(fmt.Sprintf("%s-%d", topic, partNo))
			//No record value in redis for the first time run
			if err != nil {
			...
			}
			//if the offset record in redis smaller than the record in zookeeper, then need use ResetOffset
			//if the offset record in redis bigger than the record in zookeeper, then need use MarkOffset
			sess.ResetOffset(topic, partNo, offset, "")
			sess.MarkOffset(topic, partNo, offset, "")
		}
	}
	return nil
}
//message protocol: [topic type][retry count][reqID][req]
func (consumer *Consumer) ProcessMessage(topic, msg string) {
	message := strings.Split(msg, ":")
	reqId, err := strconv.ParseInt(message[2], 10, 64)
	if err != nil {
	...
	}
	switch message[0] {
	case "Normal":
		consumer.processNormalTopic(topic, message[3], reqId)
	case "Retry":
		retryCnt, err := strconv.Atoi(message[1])
		if err != nil {
		...
		}
		topic := strings.Split(topic, "Retry-")
		consumer.processRetryTopic(topic[1], message[3], retryCnt, reqId)
	case "DeadLetter":
		consumer.processDeadLetterTopic(topic, message[3], reqId)
	default:
	}
}
func (s *server) ProcessTopic(topic, msg string, reqId int64) (int, error) {
	switch topic {
	case "GrassInWind2019":
		res, err := s.processReq(msg)
		if err != nil {
			return 2, err
		}
		//Publish result to redis
		err = rdb.Publish(fmt.Sprintf("%d", reqId), fmt.Sprintf("%f", res)).Err()
		if err != nil {
		...
		}
		return 0, nil
	default:
	}
	return 0, nil
}
```  
## 基于pprof的性能分析Demo  
在proxy和server之间对于处理结果的获取一开始是采用redis的Set/Get来实现的，在proxy的Calculate方法中循环不断调用Get来查询结果直到超时。  
在多次测试中发现，采用Set/Get的方式，proxy的经常会超时拿不到处理结果。  
通过pprof性能分析工具发现，proxy的CPU消耗集中在Calculate的redis.cmdable.Get()调用中（消耗CPU比例达到了91.86%）。  
然后采用redis的PUB/SUB对此作了优化，proxy的Calculate的CPU消耗降低到了42.86%。proxy不会出现超时拿不到结果了。(需要在测试前创建好topic,否则刚开始运行还是会出现超时，一段时间后正常。)  
pprof分析CPU数据的命令如下  
pprof -http=:8080 cpu_pub-sub.prof  
### 使用pprof统计CPU/HEAP数据的code example  
```
import "runtime/pprof"

cpuf, err := os.OpenFile("cpu.prof", os.O_RDWR|os.O_CREATE, 0644)
if err != nil {
	panic(err)
}
pprof.StartCPUProfile(cpuf)
heapf, err := os.OpenFile("heap.prof", os.O_RDWR|os.O_CREATE, 0644)
if err != nil {
	panic(err)
}
pprof.StopCPUProfile()
cpuf.Close()
pprof.WriteHeapProfile(heapf)
heapf.Close()
```
### 基于redis的Set/Get方式的pprof的CPU火焰图  
![proxy-cpu_set-get.png](https://github.com/GrassInWind2019/gRPC-kafkaDemo/blob/master/image/proxy-cpu_set-get.png)
### 基于redis的PUB/SUB方式的pprof的CPU火焰图  
![proxy-cpu_pub-sub.png](https://github.com/GrassInWind2019/gRPC-kafkaDemo/blob/master/image/proxy-cpu_pub-sub.png)

## RPC接口  
RPC接口通过protobuf定义，使用的是proto3版本。  
```
//The request message containing the user's name
message CalculateRequest {
    string name = 1;
    string method = 2;
    int32  num1 = 3;
    int32  num2 = 4;
}
//The response message
message CalculateResponse {
    string message = 1;
    bool    successFlag = 2;
    double  result = 3;
}
//service definition
service CalculateService {
    rpc Calculate(CalculateRequest) returns(CalculateResponse);
}
```

## client
1. client通过调用ConsulResolverInit注册了一个consul client,同时向gRPC注册了自定义的resolver即consulResolverBuilder。  
2. client调用Dial与HelloService server建立连接。  
Dial连接server大致实现过程如下：  
  Dial()-->DialContext()-->newCCResolverWrapper()  
  newCCResolverWrapper()-->Build()  
  Build()-->resolveServiceFromConsul() 通过consul获取service的地址信息  
  &emsp;&emsp;&emsp;&emsp;-->start()-->UpdateState()-->updateResolverState() 这个方法里会将consul获取的service地址信息存储到gRPC中  
  &emsp;&emsp;&emsp;&emsp;-->csMonitor()创建一个goroutine每隔500ms向consul获取service信息，然后调用UpdateState更新地址  
  updateResolverState()-->newCCBalancerWrapper()-->watcher()这个watcher goroutine专门监控service的地址变化并更新连接  
 &emsp;&emsp;&emsp;&emsp;&emsp;&emsp;&emsp;&emsp;&emsp;&emsp;-->updateClientConnState()这个方法是将通过consul获取的地址信息中去除GRPCLB地址，然后通过通道发送给watcher去处理  
watcher()-->UpdateClientConnState()这个方法会根据consul获取的新地址创建SubConn并调用Connect()去连接server，然后会检查当前的地址集合将失效的地址删除  
3. client调用Calculate方法  
实际上调用的是通过proto自动生成的代码中的helloServiceClient的Calculate方法。  
Calculate()-->Invoke()  
Invoke()-->newClientStream()-->newAttemptLocked()-->getTransport()-->Pick()该方法轮询可用连接来使用即roundrobin负载均衡    
&emsp;&emsp;&emsp;-->SendMsg()该方法将请求发送给对应的server  
&emsp;&emsp;&emsp;-->RecvMsg()该方法接收server回应的respoonse  
 
 4. 小结
 client需要实现gRPC resolver相关接口，以使得gRPC能够获取service的地址。gRPC的resolver example: https://github.com/grpc/grpc-go/blob/master/examples/features/name_resolving/client/main.go  
 本文的resolver example: https://github.com/GrassInWind2019/gRPC-kafkaDemo/blob/master/serviceDiscovery/consulResolver.go  
client首先通过调用ConsulResolverInit向gRPC注册实现的resolver，然后调用Dial与server建立连接，然后再调用NewCalculateClient创建一个通过protoc自动生成的CalculateService的client，最后就可以调用这个client的Calculate方法来实现RPC。  
本文的client example: https://github.com/GrassInWind2019/gRPC-kafkaDemo/blob/master/example/client/client.go
  
## proxy  
1. 调用NewCalculateServiceServer来创建一个gRPC server及CalculateService。 
2. server通过调用Listen来侦听指定的地址和端口。 
2. 调用CreateConsulRegisterClient创建一个consul client， 
3. 调用RegisterServiceToConsul向consul server注册一个service。  
RegisterServiceToConsul()-->registerServiceToConsul()  
registerServiceToConsul()-->ServiceRegister()通过调用consul client的ServiceRegister方法向consul server注册  
 &emsp;&emsp;&emsp;&emsp;&emsp;&emsp;&emsp;&emsp;&emsp;&emsp;&emsp;-->AgentServiceCheck()向consul server注册service的health check  
 &emsp;&emsp;&emsp;&emsp;&emsp;&emsp;&emsp;&emsp;&emsp;&emsp;&emsp;-->创建了一个goroutine并定期调用UpdateTTL向consul server表明service还是OK的。  
4. 调用RegisterCalculateServiceServer向gRPC注册一个service及它提供的方法。  
RegisterHelloServiceServer()-->RegisterService()-->register()  
register将service提供的方法根据名称保存到了一个map中。  
```
func (s *Server) register(sd *ServiceDesc, ss interface{}) {
    ...
	srv := &service{
		server: ss,
		md:     make(map[string]*MethodDesc),
        ...
	}
	//将待注册的service的MethodDesc对象保存到新创建的service的md中
	for i := range sd.Methods {
		d := &sd.Methods[i]
		srv.md[d.MethodName] = d
	}
    ...
	//将新创建的service对象保存到server的m中
	s.m[sd.ServiceName] = srv
}
```
6. 调用Serve来为client提供服务。  
Serve()-->Accept()接受client连接  
&emsp;&emsp;&emsp;-->新创建一个goroutine来处理建立的连接-->handleRawConn()  
handleRawConn()-->newHTTP2Transport()-->NewServerTransport()-->newHTTP2Server()这个方法会与client完成http2握手，然后创建一个goroutine专门用于发送数据。  
&emsp;&emsp;&emsp;&emsp;&emsp;&emsp;&emsp;-->serveStreams()-->HandleStreams()-->operateHeaders()-->handleStream()会从接收到的stream中取出service和method名称，然后从server结构对应的map表中找出method handler。  
```
func (s *Server) handleStream(t transport.ServerTransport, stream *transport.Stream, trInfo *traceInfo) {
	//获取stream的Method名称
	sm := stream.Method()
	if sm != "" && sm[0] == '/' {
		sm = sm[1:]
	}
	pos := strings.LastIndex(sm, "/")
	...
	//获取service名称
	service := sm[:pos]
	//获取method名称
	method := sm[pos+1:]
	//从gRPC server的m map中根据service名称找到对应的service对象
	srv, knownService := s.m[service]
	if knownService {
		//再从service的md map中找到对应的MethodDesc对象，其中包含method handler
		if md, ok := srv.md[method]; ok {
			s.processUnaryRPC(t, stream, srv, md, trInfo)
			return
		}
		...
	}
	...
	if err := t.WriteStatus(stream, status.New(codes.Unimplemented, errDesc)); err != nil {
	...
	}
	...
}
```
processUnaryRPC()-->NewContextWithServerTransportStream()创建一个context  
&emsp;&emsp;&emsp;&emsp;&emsp;&emsp;&emsp;&emsp;-->Handler()实际就是调用SayHello  
&emsp;&emsp;&emsp;&emsp;&emsp;&emsp;&emsp;&emsp;-->sendResponse()将执行结果发送给client  
sendResponse()-->Write()-->put()-->executeAndPut()将数据存入controlBuffer的list中，然后通知consumer即newHTTP2Server创建的那个goroutine调用get来取数据并发送出去。  
  
7. Calculate服务  
Calculate首先根据请求内容去redis中查询是否有对应的记录，若没有则先获取一个唯一id作为请求id，然后向redis订阅以这个id为名称的channel。订阅成功后，新产生一条消息，发送到kafka。然后设置3s超时，在订阅的通道等待server的处理结果。  

8.模拟service故障及恢复  
通过faultSimulator每隔20s调用GracefulStop来停止正在运行的server来模拟service发生故障。  
```
func (monitor *serverMonitor) faultSimulator() {
	t := time.NewTicker(20 * time.Second)
	for {
		select {
		case <-t.C:
			fmt.Println("time out! Stop servers!")
			for _, s := range monitor.servers {
				s.gServer.GracefulStop()
				fmt.Printf("server %s:%d graceful stopped!\n", s.info.Addr, s.info.Port)
			}
		...
		}
	}
}
```
通过serverMonitor来监控server状态，若发生失败退出，则重新启动一个新的server。通过改变port，来模拟service地址变化，client调用RPC接口依旧能正常工作。  

## server  
1. server启动后创建一个producer和一个consumer group。consumer group的go client在sarama中已经提供了example,可参考https://github.com/Shopify/sarama/tree/master/examples/consumergroup  
2. consumer启动后在Setup()中先获取session的group及partition信息，然后从redis中获取对应的offset信息，再通过ResetOffset/MarkOffset将offset重置到上一次消费的offset。 
3. 然后consumer就进入消费消息的循环。  
4. 在循环中，每获取一条消息就调用ProcessMessage()，对于处理失败的，在原有topic名称的基础上加上"Retry-"，然后将消息发送到一个通道，server启动的producer会从这个通道取消息发送到kafka。对于Retry类型的topic处理依旧失败的，则将其加入Dead Letter类型的topic，发送到kafka。每条消息消费后，都会将offset记录到redis中。  
5. 对于处理成功的消息，server会将处理结果publish到以请求id为名称的channel.  

# 本文github链接  
https://github.com/GrassInWind2019/gRPC-kafkaDemo   
<iframe src="https://ghbtns.com/github-btn.html?user=GrassInWind2019&repo=gRPC-kafkaDemo&type=watch&count=true&size=large" allowtransparency="true" frameborder="0" scrolling="0" width="156px" height="30px"></iframe>  
<iframe src="https://ghbtns.com/github-btn.html?user=GrassInWind2019&repo=gRPC-kafkaDemo&type=fork&count=true&size=large" allowtransparency="true" frameborder="0" scrolling="0" width="156px" height="30px"></iframe>  
  
# 运行Demo简介    
## 需要下载安装的如下  
1. Go语言  
	下载link: https://golang.google.cn/dl/  
2. consul  
	 consul github link: https://github.com/hashicorp/consul  
    mkdir -p $GOPATH/src/github.com/hashicorp/  
    cd $GOPATH/src/github.com/hashicorp/  
    git clone git@github.com:hashicorp/consul.git  
    下载速度只有几kB/s，需要几个小时才能下完  
3. protobuf  
    从https://github.com/protocolbuffers/protobuf/releases下载protobuf和protoc的包
4. gRPC  
	mkdir -p $GOPATH/src/github.com/grpc/  
	cd $GOPATH/src/github.com/grpc/  
	git clone git@github.com:grpc/grpc-go.git  
	下载速度只有几kB/s，需要几个小时才能下完  
5. zookeeper  
   kafka需要zookeeper来提交偏移量及配置管理等。  
   zookeeper下载链接：http://zookeeper.apache.org/releases.html  
6. kafka  
   kafka下载链接：http://kafka.apache.org/downloads  
7. redis  
   windows 32位 redis下载链接：https://github.com/microsoftarchive/redis/releases/tag/win-3.2.100  
   这个版本在windows下运行不稳定...，但是redis的PUB/SUB功能需要大于2.6的版本。
   微软官方只支持64位redis，下载链接：https://github.com/microsoftarchive/redis/releases  
8. 下载kafka go client sarama  
   go get github.com/Shopify/sarama  
   
具体的安装步骤请自行搜索教程。  
9. 下载本文code  
   mkdir -p $GOPATH/src/github.com/GrassInWind2019/  
   cd $GOPATH/src/github.com/GrassInWind2019/  
   git clone git@github.com:GrassInWind2019/gRPC-kafkaDemo.git  
## 运行步骤  
1. consul.exe agent -dev   
   我是在windows环境下运行的，首先启动consul server，这是最简单的方式。 
2. 运行zookeeper  
   zkServer.cmd，windows在cmd直接运行这个，需要在zkServer.cmd所在路径打开cmd。  
3. 启动kafka  
   .\bin\windows\kafka-server-start.bat .\config\server1.properties
   .\bin\windows\kafka-server-start.bat .\config\server2.properties
   .\bin\windows\kafka-server-start.bat .\config\server3.properties
   windows在cmd运行。  
   kafka的server的配置可参考http://kafka.apachecn.org/quickstart.html  
4. 启动redis server  
   redis-server.exe redis.windows.conf
   在windows的cmd下运行，需要在redis-server.exe所在路径打开cmd。  
5. 编译 gRPC-kafkaDemo/example/proxy/proxy.go  
使用liteIDE打开proxy.go，点击build按钮即可完成编译。 
liteIDE下载link：https://sourceforge.net/projects/liteide/   
6. 运行proxy  
    windows 下可以用git bash运行，在$GOPATH/src/github.com/GrassInWind2019/gRPC-kafkaDemo/example/proxy/目录下打开git bash,  ./proxy.exe localhost:9092 GrassInWind2019即可。localhost:9092是kafka broker地址。  
7. 编译 gRPC-kafkaDemo/example/server/server.go  
使用liteIDE打开server.go，点击build按钮即可完成编译。
8. 运行server  
windows下在目录下打开git bash，运行./server.exe -brokers localhost:9092 -topics="GrassInWind2019" -group="example"即可。其中locahost:9092是kafka broker地址，topic名称要和proxy保持一致。
9. 编译gRPC-kafkaDemo/example/client/client.go  
使用liteIDE打开client.go，点击build按钮即可完成编译。 
10. 运行client   
	在$GOPATH/src/github.com/GrassInWind2019/gRPC-kafkaDemo/example/client/目录下打开git bash， ./client.exe   [可选参数name]。也可以直接在liteIDE点击运行按钮。  
##  修改RPC接口  
如果想修改RPC接口也就是修改CalculateService.proto文件  
修改完后需要利用protoc工具重新生成CalculateService.pb.go  
可使用如下命令  
protoc.exe --plugin=protoc-gen-go=$GOPATH/bin/protoc-gen-go.exe --go_out=plugins=grpc:. --proto_path .  CalculateService.proto  
一定要带上--go_out=plugins=grpc，否则生成的go文件会缺少gRPC相关的code比如编译会报错，找不到CalculateService.RegisterHelloServiceServer。  
## 运行结果  
### client  
![client.png](https://github.com/GrassInWind2019/gRPC-kafkaDemo/blob/master/image/client_pub-sub.png)
### proxy  
![proxy_pub-sub.png](https://github.com/GrassInWind2019/gRPC-kafkaDemo/blob/master/image/proxy_pub-sub.png)
### server  
![server.png](https://github.com/GrassInWind2019/gRPC-kafkaDemo/blob/master/image/server_pub-sub.png)
