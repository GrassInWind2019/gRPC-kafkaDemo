# gRPCwithConsul
Use gRPC + Consul to do service discovery and RPC.  

# 服务发现过程  
本文使用的注记说明：  
funcA()-->funcB()-->funcC()  
&emsp;&emsp;&emsp;-->funcD()-->funcE()  
funcC()-->funcF()  
&emsp;&emsp;&emsp;-->funcG()  
上面的函数调用关系为：funcA按序调用了funcB和funcD，funcB调用了funcC,funcD直接调用了funcE, funcC调用了funcF和funcG。  

## client
1. client通过调用ConsulResolverInit注册了一个consul client,同时向gRPC注册了自定义的resolver即consulResolverBuilder。  
2. client调用Dial与HelloService server建立连接。  
Dial连接server大致实现过程如下：  
  a. Dial()-->DialContext()-->newCCResolverWrapper()  
  b. newCCResolverWrapper()-->Build()  
  c. Build()-->resolveServiceFromConsul() 通过consul获取service的地址信息  
  &emsp;&emsp;&emsp;&emsp;-->start()-->UpdateState()-->updateResolverState() 这个方法里会将consul获取的service地址信息存储到gRPC中  
  &emsp;&emsp;&emsp;&emsp;-->csMonitor()创建一个goroutine每隔500ms向consul获取service信息，然后调用UpdateState进行更新  
     
updateResolverState()-->newCCBalancerWrapper()-->watcher()这个watcher goroutine专门监控service的地址变化并更新连接  
 &emsp;&emsp;&emsp;&emsp;&emsp;&emsp;&emsp;&emsp;&emsp;&emsp;-->updateClientConnState()这个方法是将通过consul获取的地址信息中去除GRPCLB地址，然后通过通道发送给watcher去处理  
watcher()-->UpdateClientConnState()这个方法会根据consul获取的新地址创建SubConn并调用Connect()去连接server，然后会检查当前的地址集合将失效的地址删除  

##server

 ## 相关函数原型
  ```
  func (r *consulResolver) start()

  func newCCResolverWrapper(cc *ClientConn) (*ccResolverWrapper, error)
  func (crb *consulResolverBuilder) Build(target resolver.Target, cc resolver.ClientConn, opts resolver.BuildOption) (resolver.Resolver, error)
  func (ccr *ccResolverWrapper) UpdateState(s resolver.State)     google.golang.org/grpc/resolver_conn_wrapper.go
  func (cc *ClientConn) updateResolverState(s resolver.State) error   google.golang.org/grpc/clientconn.go
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
func (crb *consulResolverBuilder) csMonitor(cc resolver.ClientConn) {
	t := time.NewTicker(time.Second)
	//Get service addresses from consul every second and update them to gRPC
	for {
		<-t.C
		addrs, err := crb.resolveServiceFromConsul()
		if err != nil {
			fmt.Println("resolveServiceFromConsul failed: ", err.Error())
			continue
		} else {
			//fmt.Println("resolveServiceFromConsul success: ", addrs)
		}
		cc.UpdateState(resolver.State{Addresses: addrs})
	}
}
```  
