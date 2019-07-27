package serviceDiscovery

import (
	"errors"
	"fmt"
	"sync"
	"time"

	consulapi "github.com/hashicorp/consul/api"
)

var ccMonitor *consulClientMonitor

type consulClientMonitor struct {
	conulServerAddr string
	client          *consulapi.Client
	//record service which registered to consul server
	csr         consulServiceRegister
	mutex       sync.Mutex
	csAddrExist bool
	//sigCh use to notify clientMonitor to start work
	sigCh chan struct{}
	//doneCh use to notify clientMonitor had finished work
	doneCh chan struct{}
}

type consulServiceRegister struct {
	sm  map[string]*ServiceInfo
	chm map[string]chan struct{}
}

type ServiceInfo struct {
	Addr           string
	Port           int
	ServiceName    string
	UpdateInterval time.Duration
	CheckInterval  int
}

func getServiceId(name, addr string, port int) string {
	return fmt.Sprintf("%s:%d:%s", addr, port, name)
}

//create a consul client for register and unregister service to consul
//csAddr is the address of consul server
//this function should only call once
func CreateConsulRegisterClient(csAddr string) error {
	ccMonitor.mutex.Lock()
	defer ccMonitor.mutex.Unlock()
	if ccMonitor.csAddrExist == false {
		ccMonitor.conulServerAddr = csAddr
		fmt.Println("consul server address set to ", ccMonitor.conulServerAddr)
		//create a consul client
		config := consulapi.DefaultConfig()
		config.Address = ccMonitor.conulServerAddr
		client, err := consulapi.NewClient(config)
		if err != nil {
			fmt.Println("Create consul client failed: ", err.Error())
			return err
		}
		ccMonitor.client = client
		ccMonitor.csAddrExist = true
		return nil
	} else {
		fmt.Println("consul server addr already exist! cannot set again")
		return errors.New("consul server addr already exist! cannot set again")
	}
}

//API for user to register a service to consul server
func RegisterServiceToConsul(info ServiceInfo) error {
	return ccMonitor.csr.registerServiceToConsul(info)
}

//API for user to deregister a service to consul server
func DeregisterServiceFromConsul(info ServiceInfo) error {
	return ccMonitor.csr.deregisterServiceFromConsul(info)
}

//User need call SignalNotify when received signal to let clientMonitor goroutine deregister services
func SignalNotify() {
	ccMonitor.sigCh <- struct{}{}
}

//User need call SignalDone() wait clientMonitor finish work
func SignalDone() {
	<-ccMonitor.doneCh
}

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
	if err != nil {
		fmt.Printf("register service %s to consul failed\n", info.ServiceName)
		return err
	}
	fmt.Printf("Service %s:%s:%d registered to consul server %s\n", info.ServiceName, info.Addr, info.Port, ccMonitor.conulServerAddr)
	//need protect store service info for concurrent calls
	ccMonitor.mutex.Lock()
	ccMonitor.csr.sm[serviceId] = &info
	ch := make(chan struct{}, 1)
	ccMonitor.csr.chm[serviceId] = ch
	ccMonitor.mutex.Unlock()

	//register service health check
	asCheck := consulapi.AgentServiceCheck{TTL: fmt.Sprintf("%ds", info.CheckInterval), Status: consulapi.HealthPassing}
	err = ccMonitor.client.Agent().CheckRegister(
		&consulapi.AgentCheckRegistration{
			ID:                serviceId,
			Name:              info.ServiceName,
			ServiceID:         serviceId,
			AgentServiceCheck: asCheck})
	if err != nil {
		fmt.Println("register service check to consul failed: ", info.ServiceName)
		return err
	}

	//start a goroutine to update health status to consul server
	go func(<-chan struct{}) {
		t := time.NewTicker(info.UpdateInterval)
		for {
			//check service deregistered or not before update service health
			select {
			case <-t.C:
			case <-ch:
				fmt.Printf("Service %s had been deregistered, health update stopped!\n", serviceId)
				return
			}
			err = ccMonitor.client.Agent().UpdateTTL(serviceId, "", asCheck.Status)
			if err != nil {
				fmt.Printf("Update TTL for service %s failed: %s\n", info.ServiceName, err.Error())
			}
		}
	}(ch)

	return nil
}

func (csr *consulServiceRegister) deregisterServiceFromConsul(info ServiceInfo) error {
	serviceId := getServiceId(info.ServiceName, info.Addr, info.Port)

	err := ccMonitor.client.Agent().ServiceDeregister(serviceId)
	if err != nil {
		fmt.Printf("deregister for service %s failed: %s\n", info.ServiceName, err.Error())
		return err
	} else {
		fmt.Printf("deregister service %s:%s:%d from consul server\n", info.ServiceName, info.Addr, info.Port)
	}

	err = ccMonitor.client.Agent().CheckDeregister(serviceId)
	if err != nil {
		fmt.Printf("deregister service %s from consul check failed\n", info.ServiceName)
		return err
	}

	ccMonitor.mutex.Lock()
	delete(ccMonitor.csr.sm, serviceId)
	ch := ccMonitor.csr.chm[serviceId]
	ch <- struct{}{}
	delete(ccMonitor.csr.chm, serviceId)
	fmt.Printf("service %s delete from ccMonitor\n", serviceId)
	ccMonitor.mutex.Unlock()

	return nil
}

//clientMonitor is used to monitor signals and dereigster services before exit
func clientMonitor() {
	//wait signal notify
	<-ccMonitor.sigCh
	fmt.Println("consul client service monitor start!")
	//do service unregister
	for _, info := range ccMonitor.csr.sm {
		ccMonitor.csr.deregisterServiceFromConsul(*info)
	}
	ccMonitor.csr.sm = nil
	ccMonitor.doneCh <- struct{}{}
	fmt.Println("consul client service monitor stopped!")
}

//ensure only execute once
func init() {
	ccMonitor = new(consulClientMonitor)
	ccMonitor.csAddrExist = false
	ccMonitor.sigCh = make(chan struct{}, 1)
	ccMonitor.doneCh = make(chan struct{}, 1)
	ccMonitor.csr.sm = make(map[string]*ServiceInfo)
	ccMonitor.csr.chm = make(map[string]chan struct{})
	//start a goroutine to monitor consul client and do services unregister before exit
	go clientMonitor()
}
