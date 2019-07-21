package serviceDiscovery

import (
	"fmt"

	consulapi "github.com/hashicorp/consul/api"
	"google.golang.org/grpc/resolver"
)

const (
	consulScheme = "consul"
)

type consulResolverBuilder struct {
	address     string
	client      *consulapi.Client
	serviceName string
}

func (crb *consulResolverBuilder) Build(target resolver.Target, cc resolver.ClientConn, opts resolver.BuildOption) (resolver.Resolver, error) {
	consulServiceName := crb.serviceName
	addrs, err := crb.resolveServiceFromConsul()
	if err != nil {
		return &consulResolver{}, nil
	}
	r := &consulResolver{
		target: target,
		cc:     cc,
		addrsStore: map[string][]resolver.Address{
			consulServiceName: addrs,
		},
	}
	r.start()
	return r, nil
}
func (*consulResolverBuilder) Scheme() string { return consulScheme }

func (crb *consulResolverBuilder) resolveServiceFromConsul() ([]resolver.Address, error) {
	serviceEntries, _, err := crb.client.Health().Service(crb.serviceName, "", true, &consulapi.QueryOptions{})
	if err != nil {
		fmt.Println("call consul Health API failed, ", err)
		return nil, err
	}

	addrs := make([]resolver.Address, 0)
	for _, serviceEntry := range serviceEntries {
		address := resolver.Address{Addr: fmt.Sprintf("%s:%d", serviceEntry.Service.Address, serviceEntry.Service.Port)}
		addrs = append(addrs, address)
	}
	return addrs, nil
}

type consulResolver struct {
	target     resolver.Target
	cc         resolver.ClientConn
	addrsStore map[string][]resolver.Address
}

func (r *consulResolver) start() {
	addrs := r.addrsStore[r.target.Endpoint]
	r.cc.UpdateState(resolver.State{Addresses: addrs})
}
func (*consulResolver) ResolveNow(o resolver.ResolveNowOption) {}
func (*consulResolver) Close()                                 {}

func ConsulResolverInit(address string, serviceName string) error {
	config := consulapi.DefaultConfig()
	config.Address = address
	client, err := consulapi.NewClient(config)
	if err != nil {
		fmt.Println("new consul client failed: ", err.Error())
		return err
	}
	resolver.Register(&consulResolverBuilder{address: address, client: client, serviceName: serviceName})

	return nil
}
