# 目录
- [gRPC + consul](#gRPC + consul)
- [服务发现及RPC过程](#服务发现及RPC过程)
  * [服务发现及RPC示意图](#服务发现及RPC示意图)
  * [RPC接口](#RPC接口)
  * [client](#client)
  * [server](#server)
  * [相关函数原型](#相关函数原型)
  * [gRPC其他相关代码说明](#gRPC其他相关代码说明)
  * [本文其他相关代码说明](#本文其他相关代码说明)
  * [github code link](#github code link)
- [使用本文code简介](#使用本文code简介)
  * [需要下载安装的如下](#需要下载安装的如下)
  * [运行步骤](#运行步骤)
  * [修改RPC接口](#修改RPC接口)
  * [运行结果](#运行结果)
  * * [server](#server)
  * * [client](#client)
# gRPC + consul
本文使用consul做服务发现，gRPC来处理RPC(Remote Procedure Call)即远程过程调用。
gRPC是google开源的一个高性能、通用的RPC框架，基于http2、protobuf设计开发，是一个跨编程语言的RPC框架，跨编程语言让client和server可以采用不同的编程语言开发。

# 服务发现及RPC过程  
本文使用的注记说明：  
funcA()-->funcB()-->funcC()  
&emsp;&emsp;&emsp;-->funcD()-->funcE()  
funcC()-->funcF()  
&emsp;&emsp;&emsp;-->funcG()  
上面的函数调用关系为：funcA按序调用了funcB和funcD，funcB调用了funcC,funcD直接调用了funcE, funcC调用了funcF和funcG。 

## 服务发现及RPC示意图
![服务发现及RPC示意图.png](https://github.com/GrassInWind2019/gRPCwithConsul/blob/master/服务发现及RPC示意图.png)

## RPC接口  
RPC接口通过protobuf定义，使用的是proto3版本。  
```
//The request message containing the user's name
message HelloRequest {
    string name = 1;
    int32  num1 = 2;
    int32  num2 = 3;
}
//The response message
message HelloResponse {
    string message = 1;
    int32  result = 2;
}
//service definition
service HelloService {
    rpc SayHello(HelloRequest) returns(HelloResponse);
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
3. client调用SayHello方法  
实际上调用的是通过proto自动生成的代码中的helloServiceClient的SayHello方法。  
SayHello()-->Invoke()  
Invoke()-->newClientStream()-->newAttemptLocked()-->getTransport()-->Pick()该方法轮询可用连接来使用即roundrobin负载均衡    
&emsp;&emsp;&emsp;-->SendMsg()该方法将请求发送给对应的server  
&emsp;&emsp;&emsp;-->RecvMsg()该方法接收server回应的respoonse  
 
 4. 小结
 client需要实现gRPC resolver相关接口，以使得gRPC能够获取service的地址。gRPC的resolver example: https://github.com/grpc/grpc-go/blob/master/examples/features/name_resolving/client/main.go  
 本文的resolver example: https://github.com/GrassInWind2019/gRPCwithConsul/blob/master/serviceDiscovery/consulResolver.go  
client首先通过调用ConsulResolverInit向gRPC注册实现的resolver，然后调用Dial与server建立连接，然后再调用NewHelloServiceClient创建一个通过protoc自动生成的HelloService的client，最后就可以调用这个client的SayHello方法来实现RPC。  
本文的client example: https://github.com/GrassInWind2019/gRPCwithConsul/blob/master/example/client/client.go
  
## server  
1. 调用newHelloServiceServer来创建一个gRPC server及helloServiceServer。 
2. server通过调用Listen来侦听指定的地址和端口。 
2. 调用CreateConsulRegisterClient创建一个consul client， 
3. 调用RegisterServiceToConsul向consul server注册一个service。  
RegisterServiceToConsul()-->registerServiceToConsul()  
registerServiceToConsul()-->ServiceRegister()通过调用consul client的ServiceRegister方法向consul server注册  
 &emsp;&emsp;&emsp;&emsp;&emsp;&emsp;&emsp;&emsp;&emsp;&emsp;&emsp;-->AgentServiceCheck()向consul server注册service的health check  
 &emsp;&emsp;&emsp;&emsp;&emsp;&emsp;&emsp;&emsp;&emsp;&emsp;&emsp;-->创建了一个goroutine并定期调用UpdateTTL向consul server表明service还是OK的。  
4. 调用RegisterHelloServiceServer向gRPC注册一个service及它提供的方法。  
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
func RegisterHelloServiceServer(s *grpc.Server, srv HelloServiceServer) {
	s.RegisterService(&_HelloService_serviceDesc, srv)
}
var _HelloService_serviceDesc = grpc.ServiceDesc{
	ServiceName: "HelloService_proto.HelloService",
	HandlerType: (*HelloServiceServer)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "SayHello",
			Handler:    _HelloService_SayHello_Handler,
		},
	},
	Streams:  []grpc.StreamDesc{},
	Metadata: "HelloService.proto",
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

```
//  google.golang.org/grpc/internal/transport/controlbuf.go
func (c *controlBuffer) executeAndPut(f func(it interface{}) bool, it interface{}) (bool, error) {
	var wakeUp bool
	c.mu.Lock()
	...
	if c.consumerWaiting {
		wakeUp = true
		c.consumerWaiting = false
	}
	//将数据加入list中
	c.list.enqueue(it)
	c.mu.Unlock()
	if wakeUp {
		select {
		//通知consumer取数据
		case c.ch <- struct{}{}:
		default:
		}
	}
	return true, nil
}
//  google.golang.org/grpc/internal/transport/controlbuf.go
func (c *controlBuffer) get(block bool) (interface{}, error) {
	for {
		...
		if !c.list.isEmpty() {
			//从list中取数据
			h := c.list.dequeue()
			c.mu.Unlock()
			return h, nil
		}
		c.consumerWaiting = true
		select {
		//consumer等待producer生产数据
		case <-c.ch:
		case <-c.done:
			c.finish()
			return nil, ErrConnClosing
		}
	}
}
```
7.模拟service故障及恢复  
通过faultSimulator每隔15s调用GracefulStop来停止正在运行的server来模拟service发生故障。  
```
func (hssMonitor *hsServerMonitor) faultSimulator() {
	t := time.NewTicker(15 * time.Second)
	for {
		select {
		case <-t.C:
			fmt.Println("time out! Stop servers!")
			for _, s := range hssMonitor.hsServers {
				s.gServer.GracefulStop()
				fmt.Printf("server %s:%d graceful stopped!\n", s.info.Addr, s.info.Port)
			}
		...
		}
	}
}
```
通过helloServiceServerMonitor来监控server状态，若发生失败退出，则重新启动一个新的server。  
```
 //   gRPCwithConsul/example/server/server.go
func (hssMonitor *hsServerMonitor) startNewServer(hsPort int) {
	hsServer := newHelloServiceServer(hsPort)
	hssMonitor.hsServers = append(hssMonitor.hsServers, hsServer)
	go startHelloServiceServer(hsServer)
}
//   gRPCwithConsul/example/server/server.go
func (hssMonitor *hsServerMonitor) helloServiceServerMonitor() {
	...
	for {
		for i := 0; i < serverNum; i++ {
			select {
			//hello service server fault happened, start new server as recovery
			case <-hssMonitor.hsServers[i].ch:
				//通过改变port，来模拟service地址变化，client调用RPC接口依旧正常工作
				port[i] += 5
				hssMonitor.hsServers[i].info.Port = port[i]
				//need new a gRPC server after stopping old one, otherwise will meet gRPC error:
				//Server.RegisterService after Server.Serve for "HelloService_proto.HelloService"
				s := grpc.NewServer()
				hssMonitor.hsServers[i].gServer = s
				//start new server
				go startHelloServiceServer(hssMonitor.hsServers[i])
			...
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
}
```

## 相关函数原型
  ```
   //  gRPCwithConsul/serviceDiscovery/consulResolver.go
  func (r *consulResolver) start()
  //  gRPCwithConsul/serviceDiscovery/consulResolver.go
  func (crb *consulResolverBuilder) Build(target resolver.Target, cc resolver.ClientConn, opts resolver.BuildOption) (resolver.Resolver, error)
  //  gRPCwithConsul/example/HelloService_proto/HelloService.pb.go
  func (c *helloServiceClient) SayHello(ctx context.Context, in *HelloRequest, opts ...grpc.CallOption) (*HelloResponse, error)
  //  gRPCwithConsul/example/HelloService_proto/HelloService.pb.go
  func RegisterHelloServiceServer(s *grpc.Server, srv HelloServiceServer)
  //  gRPCwithConsul/serviceDiscovery/consulRegister.go
  func CreateConsulRegisterClient(csAddr string) error
  //  gRPCwithConsul/serviceDiscovery/consulRegister.go
  func (csr *consulServiceRegister) registerServiceToConsul(info ServiceInfo) error
  //   gRPCwithConsul/example/server/server.go
func newHelloServiceServer(hsPort int) *helloServiceServer

 //  google.golang.org/grpc/resolver_conn_wrapper.go
func newCCResolverWrapper(cc *ClientConn) (*ccResolverWrapper, error)
//  google.golang.org/grpc/resolver_conn_wrapper.go
func (ccr *ccResolverWrapper) UpdateState(s resolver.State)     
//  google.golang.org/grpc/clientconn.go
func (cc *ClientConn) updateResolverState(s resolver.State) error   
//  grpc/grpc-go/balancer/roundrobin/roundrobin.go
func (p *rrPicker) Pick(ctx context.Context, opts balancer.PickOptions) (balancer.SubConn, func(balancer.DoneInfo), error)
//  google.golang.org/grpc/server.go
func (s *Server) Serve(lis net.Listener) error
//  google.golang.org/grpc/server.go
func (s *Server) newHTTP2Transport(c net.Conn, authInfo credentials.AuthInfo) transport.ServerTransport
//  google.golang.org/grpc/server.go
func (s *Server) serveStreams(st transport.ServerTransport)
//  google.golang.org/grpc/internal/transport/http2_server.go
func (t *http2Server) HandleStreams(handle func(*Stream), traceCtx func(context.Context, string) context.Context)
//   google.golang.org/grpc/internal/transport/http2_server.go
func (t *http2Server) Write(s *Stream, hdr []byte, data []byte, opts *Options) error
  ```  
  ## gRPC其他相关代码说明
  ```
  google.golang.org/grpc/resolver.go
  GRPCLB源码解释
  const (
	// Backend indicates the address is for a backend server.
	Backend AddressType = iota
	// GRPCLB indicates the address is for a grpclb load balancer.
	GRPCLB
)
  func (ccb *ccBalancerWrapper) updateClientConnState(ccs *balancer.ClientConnState)  google.golang.org/grpc/balancer_conn_wrappers.go
  {
    ...
    //通过通道发送给watcher goroutine去更新service地址
    ccb.ccUpdateCh <- ccs
  }
  func (ccb *ccBalancerWrapper) watcher()   google.golang.org/grpc/balancer_conn_wrappers.go
  {
    ...
    case s := <-ccb.ccUpdateCh:
      ...
      if ub, ok := ccb.balancer.(balancer.V2Balancer); ok {
				ub.UpdateClientConnState(*s)
			}
    ...
  }
    func (b *baseBalancer) UpdateClientConnState(s balancer.ClientConnState) {    google.golang.org/grpc/balancer/base/balancer.go
  ...
	for _, a := range s.ResolverState.Addresses {
		addrsSet[a] = struct{}{}
		if _, ok := b.subConns[a]; !ok {
			// a is a new address (not existing in b.subConns).
			sc, err := b.cc.NewSubConn([]resolver.Address{a}, balancer.NewSubConnOptions{HealthCheckEnabled: b.config.HealthCheck})
      ...
			b.subConns[a] = sc
			b.scStates[sc] = connectivity.Idle
			sc.Connect()
		}
	}
	//在通过consul获取service最新地址后，检查subConns中旧的地址是否失效
	for a, sc := range b.subConns {
		// a was removed by resolver.
		//如果最新地址集合中不包含地址a，则代表a已失效
		if _, ok := addrsSet[a]; !ok {
			b.cc.RemoveSubConn(sc)
			delete(b.subConns, a)
		}
	}
}
//roundrobin实现
//grpc/grpc-go/balancer/roundrobin/roundrobin.go
func (p *rrPicker) Pick(ctx context.Context, opts balancer.PickOptions) (balancer.SubConn, func(balancer.DoneInfo), error) {
	p.mu.Lock()
	sc := p.subConns[p.next]
	//轮流选择可用连接使用
	p.next = (p.next + 1) % len(p.subConns)
	p.mu.Unlock()
	return sc, nil, nil
}
```
## 本文其他相关代码说明  
```
 func (crb *consulResolverBuilder) resolveServiceFromConsul() ([]resolver.Address, error) {
  //调用consul API来获取指定service的地址信息
	serviceEntries, _, err := crb.client.Health().Service(crb.serviceName, "", true, &consulapi.QueryOptions{})
	if err != nil {
		fmt.Println("call consul Health API failed, ", err)
		return nil, err
	}
	addrs := make([]resolver.Address, 0)
	for _, serviceEntry := range serviceEntries {
    //将获取的地址信息组装成resolver.Address类型返回
		address := resolver.Address{Addr: fmt.Sprintf("%s:%d", serviceEntry.Service.Address, serviceEntry.Service.Port)}
		addrs = append(addrs, address)
	}
	return addrs, nil
}
func (crb *consulResolverBuilder) csMonitor(cr *consulResolver) {
	t := time.NewTicker(500 * time.Millisecond)
	//Get service addresses from consul every 500 Millisecond and update them to gRPC
	for {
		select {
		case <-t.C:
		//resolve now
		case <-cr.rnCh:
		}
		addrs, err := crb.resolveServiceFromConsul()
...
		cr.cc.UpdateState(resolver.State{Addresses: addrs})
	}
}
//   gRPCwithConsul/example/HelloService_proto/HelloService.pb.go
func (c *helloServiceClient) SayHello(ctx context.Context, in *HelloRequest, opts ...grpc.CallOption) (*HelloResponse, error) {    
	out := new(HelloResponse)
	err := c.cc.Invoke(ctx, "/HelloService_proto.HelloService/SayHello", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}
 //  gRPCwithConsul/serviceDiscovery/consulRegister.go
func (csr *consulServiceRegister) registerServiceToConsul(info ServiceInfo) error {
	serviceId := getServiceId(info.ServiceName, info.Addr, info.Port)
	asg := &consulapi.AgentServiceRegistration{
		ID:      serviceId,
		Name:    info.ServiceName,
		Tags:    []string{info.ServiceName},
		Port:    info.Port,
		Address: info.Addr,
	}
	//register service to consul server
	err := ccMonitor.client.Agent().ServiceRegister(asg)
   ...
	//向consul server注册 health check
	asCheck := consulapi.AgentServiceCheck{TTL: fmt.Sprintf("%ds", info.CheckInterval), Status: consulapi.HealthPassing}
	err = ccMonitor.client.Agent().CheckRegister(
		&consulapi.AgentCheckRegistration{
			ID:                serviceId,
			Name:              info.ServiceName,
			ServiceID:         serviceId,
			AgentServiceCheck: asCheck})
   ...
	//start a goroutine to update health status to consul server
	go func(<-chan struct{}) {
		t := time.NewTicker(info.UpdateInterval)
		for {
             select {
			case <-t.C:
            ...
			}
			//向consul server报告service HealthPassing
			err = ccMonitor.client.Agent().UpdateTTL(serviceId, "", asCheck.Status)
            ...
		}
	}(ch)
	return nil
}
//   gRPCwithConsul/example/server/server.go
func newHelloServiceServer(hsPort int) *helloServiceServer {
	//创建一个gRPC server
	s := grpc.NewServer()
	info := &serviceDiscovery.ServiceInfo{
		Addr:           ip,
		Port:           hsPort,
		ServiceName:    "HelloService",
		UpdateInterval: 5 * time.Second,
		CheckInterval:  20}
	ch := make(chan struct{}, 1)
	//创建一个helloServiceServer
	hsServer := &helloServiceServer{
		info:    info,
		gServer: s,
		ch:      ch}
	return hsServer
}
```  

# github code link  
https://github.com/GrassInWind2019/gRPCwithConsul  
<iframe src="https://ghbtns.com/github-btn.html?user=GrassInWind2019&repo=gRPCwithConsul&type=watch&count=true&size=large" allowtransparency="true" frameborder="0" scrolling="0" width="156px" height="30px"></iframe>  
<iframe src="https://ghbtns.com/github-btn.html?user=GrassInWind2019&repo=gRPCwithConsul&type=fork&count=true&size=large" allowtransparency="true" frameborder="0" scrolling="0" width="156px" height="30px"></iframe>  
  
# 使用本文code简介  
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
具体的安装步骤请自行搜索教程。
5. 下载本文code  
   mkdir -p $GOPATH/src/github.com/GrassInWind2019/  
   cd $GOPATH/src/github.com/GrassInWind2019/  
   git clone git@github.com:GrassInWind2019/gRPCwithConsul.git  
## 运行步骤  
1. consul.exe agent -dev   
   我是在windows环境下运行的，首先启动consul server，这是最简单的方式。 
2. 编译 gRPCwithConsul/example/server/server.go  
使用liteIDE打开server.go，点击build按钮即可完成编译。 
liteIDE下载link：https://sourceforge.net/projects/liteide/   
3. 运行server  
    windows 下可以用git bash运行，在$GOPATH/src/github.com/GrassInWind2019/gRPCwithConsul/example/server/目录下打开git bash,  ./server.exe即可。也可以直接在liteIDE点击运行按钮。  
4. 编译gRPCwithConsul/example/client/client.go  
使用liteIDE打开client.go，点击build按钮即可完成编译。 
5. 运行client   
	在$GOPATH/src/github.com/GrassInWind2019/gRPCwithConsul/example/client/目录下打开git bash， ./client.exe   [可选参数name]。也可以直接在liteIDE点击运行按钮。  
##  修改RPC接口  
如果想修改RPC接口也就是修改HelloService.proto文件  
修改完后需要利用protoc工具重新生成HelloService.pb.go  
可使用如下命令  
protoc.exe --plugin=protoc-gen-go=$GOPATH/bin/protoc-gen-go.exe --go_out=plugins=grpc:. --proto_path .  HelloService.proto  
一定要带上--go_out=plugins=grpc，否则生成的go文件会缺少gRPC相关的code比如编译会报错，找不到HelloService.RegisterHelloServiceServer。  
## 运行结果  
### server  
![server.png](https://github.com/GrassInWind2019/gRPCwithConsul/blob/master/server.png)
### client  
![client.png](https://github.com/GrassInWind2019/gRPCwithConsul/blob/master/client.png)
