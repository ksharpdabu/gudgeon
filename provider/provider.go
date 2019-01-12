package provider

import (
	"fmt"
	"strconv"

	"github.com/miekg/dns"

	"github.com/chrisruffalo/gudgeon/config"
	"github.com/chrisruffalo/gudgeon/engine"
)

// incomplete list of not-implemented queries
var notImplemented = map[uint16]bool{
	dns.TypeNone: true,
	dns.TypeIXFR: true,
	dns.TypeAXFR: true,
}

type provider struct {
	engine engine.Engine
}

type Provider interface {
	Host(config *config.GudgeonConfig, engine engine.Engine) error
	//UpdateConfig(config *GudgeonConfig) error
	//UpdateEngine(engine *engine.Engine) error
	//Shutdown()
}

func NewProvider() Provider {
	provider := new(provider)
	return provider
}

func serve(netType string, host string, port int) {
	addr := host + ":" + strconv.Itoa(port)
	fmt.Printf("%s server on address: %s ...\n", netType, addr)
	server := &dns.Server{Addr: addr, Net: netType, TsigSecret: nil}
	if err := server.ListenAndServe(); err != nil {
		fmt.Printf("Failed starting %s server: %s\n", netType, err.Error())
		return
	}
}

func (provider *provider) handle(writer dns.ResponseWriter, request *dns.Msg) {
	// drop questions that don't meet minimum requirements
	if request == nil || len(request.Question) < 1 {
		response := new(dns.Msg)
		response.SetReply(request)
		response.Rcode = dns.RcodeRefused
		writer.WriteMsg(response)
		return
	}

	// drop questions that aren't implemented
	qType := request.Question[0].Qtype
	if _, found := notImplemented[qType]; found {
		response := new(dns.Msg)
		response.SetReply(request)
		response.Rcode = dns.RcodeNotImplemented
		writer.WriteMsg(response)
		return
	}

	// actually provide some resolution
	if provider.engine != nil {
		provider.engine.Handle(writer, request)
	} else {
		// when no engine defined return that there was a server failure
		response := new(dns.Msg)
		response.SetReply(request)
		response.Rcode = dns.RcodeServerFailure
		writer.WriteMsg(response)
	}
}

func (provider *provider) Host(config *config.GudgeonConfig, engine engine.Engine) error {
	// get network config
	netConf := config.Network
	if netConf == nil {
		// todo: log no network structure
		return nil
	}

	// interfaces
	interfaces := netConf.Interfaces
	if interfaces == nil || len(interfaces) < 1 {
		// todo: log no interfaces
		return nil
	}

	// if no engine provided return nil
	if engine != nil {
		provider.engine = engine
	}

	// global dns handle function
	dns.HandleFunc(".", provider.handle)

	defaultTcp := true
	defaultUdp := true
	for _, iface := range interfaces {
		if defaultTcp {
			go serve("tcp", iface.IP, iface.Port)
		}
		if defaultUdp {
			go serve("udp", iface.IP, iface.Port)
		}
	}

	return nil
}
