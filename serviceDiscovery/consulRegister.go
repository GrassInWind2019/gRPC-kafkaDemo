package serviceDiscovery

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	consulapi "github.com/hashicorp/consul/api"
)

var ccMonitor *consulClientMonitor

type consulClientMonitor struct {
	conulServerAddr string
	client          *consulapi.Client
	//record service which registered to consul server
	csr         consulServiceRegister
	csAddrExist bool
	//ch use to notify clientMonitor to start work
	ch               chan struct{}
	isMonitorWorking bool
}

type consulServiceRegister struct {
	sm map[string]*ServiceInfo
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
func CreateConsulRegisterClient(csAddr string) error {
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

func RegisterServiceToConsul(info ServiceInfo) error {
	return ccMonitor.csr.registerServiceToConsul(info)
}

func DeregisterServiceFromConsul(info ServiceInfo) error {
	return ccMonitor.csr.deregisterServiceFromConsul(info)
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
	fmt.Printf("Service %s registered to consul server %s\n", info.ServiceName, ccMonitor.conulServerAddr)
	ccMonitor.csr.sm[info.ServiceName] = &info

	if ccMonitor.isMonitorWorking == false {
		ccMonitor.ch <- struct{}{}
	}

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
	go func() {
		t := time.NewTicker(info.UpdateInterval)
		for {
			<-t.C
			err = ccMonitor.client.Agent().UpdateTTL(serviceId, "", asCheck.Status)
			if err != nil {
				fmt.Printf("Update TTL for service %s failed: %s\n", info.ServiceName, err.Error())
			}
		}
	}()

	return nil
}

func (csr *consulServiceRegister) deregisterServiceFromConsul(info ServiceInfo) error {
	serviceId := getServiceId(info.ServiceName, info.Addr, info.Port)

	err := ccMonitor.client.Agent().ServiceDeregister(serviceId)
	if err != nil {
		fmt.Printf("deregister for service %s failed: %s\n", info.ServiceName, err.Error())
		return err
	} else {
		fmt.Printf("deregister service %s from consul server\n", info.ServiceName)
	}

	err = ccMonitor.client.Agent().CheckDeregister(serviceId)
	if err != nil {
		fmt.Printf("deregister service %s from consul check failed\n", info.ServiceName)
	}

	delete(ccMonitor.csr.sm, info.ServiceName)

	return nil
}

func clientMonitor() {
	//wait until client had registered service to consul
	<-ccMonitor.ch
	ccMonitor.isMonitorWorking = true
	fmt.Println("consul client service monitor start!")
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGINT, syscall.SIGTERM, syscall.SIGKILL, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGABRT)
	sig := <-c
	fmt.Println("Receive signal: ", sig)
	//do service unregister
	for _, info := range ccMonitor.csr.sm {
		ccMonitor.csr.deregisterServiceFromConsul(*info)
	}
	ccMonitor.csr.sm = nil

	s, _ := strconv.Atoi(fmt.Sprintf("%d", sig))
	os.Exit(s)
}

//ensure only execute once
func init() {
	ccMonitor = new(consulClientMonitor)
	ccMonitor.csAddrExist = false
	ccMonitor.ch = make(chan struct{})
	ccMonitor.isMonitorWorking = false
	ccMonitor.csr.sm = make(map[string]*ServiceInfo)
	//start a goroutine to monitor consul client and do services unregister before exit
	go clientMonitor()
}
