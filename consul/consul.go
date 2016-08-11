package consul

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"strings"

	"github.com/mesos-utility/mesos-consul/registry"

	consulapi "github.com/hashicorp/consul/api"
	log "github.com/sirupsen/logrus"
)

type Consul struct {
	agents map[string]*consulapi.Client
	config consulConfig
}

//
func New() *Consul {
	return &Consul{
		agents: make(map[string]*consulapi.Client),
		config: config,
	}
}

// client()
//   Return a consul client at the specified address
func (c *Consul) client(address string) *consulapi.Client {
	if address == "" {
		log.Warn("No address to Consul.Agent")
		return nil
	}

	if _, ok := c.agents[address]; !ok {
		// Agent connection not saved. Connect.
		c.agents[address] = c.newAgent(address)
	}

	return c.agents[address]
}

// newAgent()
//   Connect to a new agent specified by address
//
func (c *Consul) newAgent(address string) *consulapi.Client {
	if address == "" {
		log.Warnf("No address to Consul.NewAgent")
		return nil
	}

	config := consulapi.DefaultConfig()

	config.Address = fmt.Sprintf("%s:%s", address, c.config.port)
	log.Debugf("consul address: %s", config.Address)

	if c.config.token != "" {
		log.Debugf("setting token to %s", c.config.token)
		config.Token = c.config.token
	}

	if c.config.sslEnabled {
		log.Debugf("enabling SSL")
		config.Scheme = "https"
	}

	if !c.config.sslVerify {
		log.Debugf("disabled SSL verification")
		config.HttpClient.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		}
	}

	if c.config.auth.Enabled {
		log.Debugf("setting basic auth")
		config.HttpAuth = &consulapi.HttpBasicAuth{
			Username: c.config.auth.Username,
			Password: c.config.auth.Password,
		}
	}

	client, err := consulapi.NewClient(config)
	if err != nil {
		log.Fatal("consul: ", address)
	}
	return client
}

func (c *Consul) Register(service *registry.Service) {
	if _, ok := serviceCache[service.ID]; ok {
		log.Debugf("Service found. Not registering: %s", service.ID)
		c.CacheMark(service.ID)
		return
	}

	if _, ok := c.agents[service.Agent]; !ok {
		// Agent connection not saved. Connect.
		c.agents[service.Agent] = c.newAgent(service.Agent)
	}

	log.Info("Registering ", service.ID)

	s := &consulapi.AgentServiceRegistration{
		ID:      service.ID,
		Name:    service.Name,
		Port:    service.Port,
		Address: service.Address,
		Check: &consulapi.AgentServiceCheck{
			TTL:      service.Check.TTL,
			Script:   service.Check.Script,
			HTTP:     service.Check.HTTP,
			Interval: service.Check.Interval,
		},
	}

	if len(service.Tags) > 0 {
		s.Tags = service.Tags
	}

	err := c.agents[service.Agent].Agent().ServiceRegister(s)
	if err != nil {
		log.Warnf("Unable to register %s: %s", s.ID, err.Error())
		return
	}

	if err, ret := c.registerUpstream(service); !ret {
		log.Warnf(err.Error())
		return
	}

	serviceCache[s.ID] = newCacheEntry(s, service.Agent)
	c.CacheMark(s.ID)
}

func (c *Consul) registerUpstream(service *registry.Service) (error, bool) {
	// XXX: register nginx upstream in k/v value.
	var hkey = fmt.Sprintf("upstreams/%s/%s:%d", service.Name, service.Agent, service.Port)
	value := []byte("{\"weight\":1, \"max_fails\":2, \"fail_timeout\":10}")
	p := &consulapi.KVPair{Key: hkey, Value: value}

	if work, _, e := c.agents[service.Agent].KV().CAS(p, nil); e != nil {
		err := fmt.Errorf("Unable to CAS key %s: %s", hkey, e.Error())
		return err, false
	} else if !work {
		log.Debugf("%s is already CAS", hkey)
	}

	return nil, true
}

func (c *Consul) deRegisterUpstream(service *consulapi.AgentServiceRegistration) (error, bool) {
	// XXX: deregister nginx upstream in k/v value.
	var agents = strings.Split(service.ID, ":")
	var agent = agents[1]
	if strings.Contains(agent, "-") {
		agent = strings.Split(agent, "-")[0]
	}
	var hkey = fmt.Sprintf("upstreams/%s/%s:%d", service.Name, agent, service.Port)

	if _, ok := c.agents[agent]; ok {
		if _, e := c.agents[agent].KV().Delete(hkey, nil); e != nil {
			err := fmt.Errorf("Unable to Delete key %s: %s", hkey, e.Error())
			return err, false
		}
	}
	return nil, true
}

// Deregister()
//   Deregister services that no longer exist
//
func (c *Consul) Deregister() {
	for s, b := range serviceCache {
		if c.CacheIsValid(s) {
			c.CacheProcessDeregister(s)
		} else {
			log.Infof("Deregistering %s", s)
			err := c.deregister(b.agent, b.service)
			if err != nil {
				log.Info("Deregistration error ", err)
			} else {
				if err, _ := c.deRegisterUpstream(b.service); err != nil {
					log.Warnf(err.Error())
				}
				delete(serviceCache, s)
			}

		}
	}
}

func (c *Consul) deregister(agent string, service *consulapi.AgentServiceRegistration) error {
	if _, ok := c.agents[agent]; !ok {
		// Agent connection not saved. Connect.
		c.agents[agent] = c.newAgent(agent)
	}

	return c.agents[agent].Agent().ServiceDeregister(service.ID)
}
