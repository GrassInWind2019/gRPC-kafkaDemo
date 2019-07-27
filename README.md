# gRPCwithConsul
Use gRPC + Consul to do service discovery and RPC.  

# 服务发现过程  
本文使用的注记说明：  
funcA()-->funcB()-->funcC()  
&emsp;&emsp;&emsp;-->funcD()-->funcE()  
funcC()-->funcF()  
&emsp;&emsp;&emsp;-->funcG()  
上面的函数调用关系为：funcA按序调用了funcB和funcD，funcB调用了funcC,funcD直接调用了funcE, funcC调用了funcF和funcG。  

##RPC接口
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
 &emsp;&emsp;&emsp;&emsp;-->SendMsg()该方法将请求发送给对应的server
 &emsp;&emsp;&emsp;&emsp;-->RecvMsg()该方法接收server回应的respoonse
 
 4. 小结
 client需要实现gRPC resolver相关接口，以使得gRPC能够获取service的地址。gRPC的resolver example: https://github.com/grpc/grpc-go/blob/master/examples/features/name_resolving/client/main.go
 本文的resolver example: https://github.com/GrassInWind2019/gRPCwithConsul/blob/master/serviceDiscovery/consulResolver.go
client首先通过调用ConsulResolverInit向gRPC注册实现的resolver，然后调用Dial与server建立连接，然后再调用NewHelloServiceClient创建一个通过protoc自动生成的HelloService的client，最后就可以调用这个client的SayHello方法来实现RPC。
本文的client example: https://github.com/GrassInWind2019/gRPCwithConsul/blob/master/example/client/client.go  
##server

 ## 相关函数原型
  ```
  func (r *consulResolver) start()
  //gRPCwithConsul/example/HelloService_proto/HelloService.pb.go
  func (c *helloServiceClient) SayHello(ctx context.Context, in *HelloRequest, opts ...grpc.CallOption) (*HelloResponse, error)

  func newCCResolverWrapper(cc *ClientConn) (*ccResolverWrapper, error)
  func (crb *consulResolverBuilder) Build(target resolver.Target, cc resolver.ClientConn, opts resolver.BuildOption) (resolver.Resolver, error)
  //google.golang.org/grpc/resolver_conn_wrapper.go
  func (ccr *ccResolverWrapper) UpdateState(s resolver.State)     
  //google.golang.org/grpc/clientconn.go
  func (cc *ClientConn) updateResolverState(s resolver.State) error   
  //grpc/grpc-go/balancer/roundrobin/roundrobin.go
func (p *rrPicker) Pick(ctx context.Context, opts balancer.PickOptions) (balancer.SubConn, func(balancer.DoneInfo), error)
  ```  
  ## gRPC相关代码说明
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

##本文相关代码说明  
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
gRPCwithConsul/example/HelloService_proto/HelloService.pb.go
func (c *helloServiceClient) SayHello(ctx context.Context, in *HelloRequest, opts ...grpc.CallOption) (*HelloResponse, error) {    
	out := new(HelloResponse)
	err := c.cc.Invoke(ctx, "/HelloService_proto.HelloService/SayHello", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}
```  
