package engine

import (
	"bytes"
	"database/sql"
	"fmt"
	"net"
	"path"
	"strings"

	"github.com/miekg/dns"
	log "github.com/sirupsen/logrus"

	"github.com/chrisruffalo/gudgeon/config"
	"github.com/chrisruffalo/gudgeon/resolver"
	"github.com/chrisruffalo/gudgeon/rule"
	"github.com/chrisruffalo/gudgeon/util"
)

// incomplete list of not-implemented queries
var notImplemented = map[uint16]bool{
	dns.TypeNone: true,
	dns.TypeNULL: true,
	dns.TypeIXFR: true,
	dns.TypeAXFR: true,
}

// an active group is a group within the engine
// that has been processed and is being used to
// select rules. this will be used with the
// rule processing to create rules and will
// be used by the consumer to talk to the store
type group struct {
	engine *engine

	configGroup *config.GudgeonGroup

	lists []*config.GudgeonList
}

// represents a parsed "consumer" type that
// links it to active parsed groups
type consumer struct {
	// engine pointer so we can use the engine from the active consumer
	engine *engine

	// configuration that this consumer was parsed from
	configConsumer *config.GudgeonConsumer

	// list of parsed groups that belong to this consumer
	groupNames []string

	// list of parsed resolvers that belong to this consumer
	resolverNames []string

	// applicable lists
	lists []*config.GudgeonList
}

// stores the internals of the engine abstraction
type engine struct {
	// the session (which will represent the on-disk location inside of the gudgeon folder)
	// that is being used as backing storage and state behind the engine
	session string

	// database for long term data storage
	db *sql.DB

	// metrics instance for engine
	metrics Metrics

	// qlog instance for engine
	qlog QueryLog

	// recorder - combined query data recorder
	recorder *recorder

	// maintain config pointer
	config *config.GudgeonConfig

	// consumers that have been parsed
	consumers []*consumer

	// default consumer
	defaultConsumer *consumer

	// the default group (used to ensure we have one)
	defaultGroup *group

	// the backing store for block/allow rules
	store rule.RuleStore

	// the resolution structure
	resolvers resolver.ResolverMap
}

func (engine *engine) Root() string {
	return path.Join(engine.config.SessionRoot(), engine.session)
}

func (engine *engine) ListPath(listType string) string {
	return path.Join(engine.Root(), listType+".list")
}

type Engine interface {
	IsDomainRuleMatched(consumer *net.IP, domain string) (rule.Match, *config.GudgeonList, string)
	Resolve(domainName string) (string, error)
	Reverse(address string) string
	Handle(address *net.IP, protocol string, request *dns.Msg) (*dns.Msg, *resolver.RequestContext, *resolver.ResolutionResult)

	// stats
	CacheSize() int64

	// inner providers
	QueryLog() QueryLog
	Metrics() Metrics

	// shutdown
	Shutdown()
}

func (engine *engine) getConsumerForIp(consumerIp *net.IP) *consumer {
	var foundConsumer *consumer

	for _, activeConsumer := range engine.consumers {
		for _, match := range activeConsumer.configConsumer.Matches {
			// test ip match
			if "" != match.IP {
				matchIp := net.ParseIP(match.IP)
				if matchIp != nil && bytes.Compare(matchIp.To16(), consumerIp.To16()) == 0 {
					foundConsumer = activeConsumer
					break
				}
			}
			// test range match
			if foundConsumer == nil && match.Range != nil && "" != match.Range.Start && "" != match.Range.End {
				startIp := net.ParseIP(match.Range.Start)
				endIp := net.ParseIP(match.Range.End)
				if startIp != nil && endIp != nil && bytes.Compare(consumerIp.To16(), startIp.To16()) >= 0 && bytes.Compare(consumerIp.To16(), endIp.To16()) <= 0 {
					foundConsumer = activeConsumer
					break
				}
			}
			// test net (subnet) match
			if foundConsumer == nil && "" != match.Net {
				_, parsedNet, err := net.ParseCIDR(match.Net)
				if err == nil && parsedNet != nil && parsedNet.Contains(*consumerIp) {
					foundConsumer = activeConsumer
					break
				}
			}
			if foundConsumer != nil {
				break
			}
		}
		if foundConsumer != nil {
			break
		}
	}

	// return default consumer
	if foundConsumer == nil {
		foundConsumer = engine.defaultConsumer
	}

	return foundConsumer
}

func (engine *engine) getConsumerGroups(consumerIp *net.IP) []string {
	consumer := engine.getConsumerForIp(consumerIp)
	return engine.getGroups(consumer)
}

func (engine *engine) getGroups(consumer *consumer) []string {
	// return found consumer data if something was found
	if consumer != nil && len(consumer.groupNames) > 0 {
		return consumer.groupNames
	}

	// return the default group in the event nothing else is available
	return []string{"default"}
}

func (engine *engine) getConsumerResolvers(consumerIp *net.IP) []string {
	consumer := engine.getConsumerForIp(consumerIp)
	return engine.getResolvers(consumer)
}

func (engine *engine) getResolvers(consumer *consumer) []string {
	// return found consumer data if something was found
	if consumer != nil && len(consumer.resolverNames) > 0 {
		return consumer.resolverNames
	}

	// return the default resolver in the event nothing else is available
	return []string{"default"}
}

// return if the domain matches any rule
func (engine *engine) IsDomainRuleMatched(consumerIp *net.IP, domain string) (rule.Match, *config.GudgeonList, string) {
	// get consumer
	consumer := engine.getConsumerForIp(consumerIp)
	return engine.domainRuleMatchedForConsumer(consumer, domain)
}

func (engine *engine) domainRuleMatchedForConsumer(consumer *consumer, domain string) (rule.Match, *config.GudgeonList, string) {
	// drop ending . if present from domain
	if strings.HasSuffix(domain, ".") {
		domain = domain[:len(domain)-1]
	}

	// sometimes (in testing, downloading) the store mechanism is nil/unloaded
	if engine.store == nil {
		return rule.MatchNone, nil, ""
	}

	// return match values
	return engine.store.FindMatch(consumer.lists, domain)
}

// handles recursive resolution of cnames
func (engine *engine) handleCnameResolution(address *net.IP, protocol string, originalRequest *dns.Msg, originalResponse *dns.Msg) *dns.Msg {
	// scope provided finding response
	var response *dns.Msg

	// guard
	if originalResponse == nil || len(originalResponse.Answer) < 1 || originalRequest == nil || len(originalRequest.Question) < 1 {
		return nil
	}

	// if the (first) response is a CNAME then repeat the question but with the cname instead
	if originalResponse.Answer[0] != nil && originalResponse.Answer[0].Header() != nil && originalResponse.Answer[0].Header().Rrtype == dns.TypeCNAME && originalRequest.Question[0].Qtype != dns.TypeCNAME {
		cnameRequest := originalRequest.Copy()
		answer := originalResponse.Answer[0]
		newName := answer.(*dns.CNAME).Target
		cnameRequest.Question[0].Name = newName
		cnameResponse, _, _ := engine.performRequest(address, protocol, cnameRequest)
		if cnameResponse != nil && !util.IsEmptyResponse(cnameResponse) {
			// use response
			response = cnameResponse
			// update answer name
			for _, answer := range response.Answer {
				answer.Header().Name = originalRequest.Question[0].Name
			}
			// but set reply as original request
			response.SetReply(originalRequest)
		}
	}

	return response
}

func (engine *engine) performRequest(address *net.IP, protocol string, request *dns.Msg) (*dns.Msg, *resolver.RequestContext, *resolver.ResolutionResult) {
	// scope provided finding response
	var (
		response *dns.Msg
		err      error
	)

	// create context
	rCon := resolver.DefaultRequestContext()
	rCon.Protocol = protocol

	// get consumer and use it to set initial/noreply result
	consumer := engine.getConsumerForIp(address)
	result := &resolver.ResolutionResult{
		Consumer: consumer.configConsumer.Name,
	}

	// drop questions that don't meet minimum requirements
	if request == nil || len(request.Question) < 1 {
		response = new(dns.Msg)
		response.SetReply(request)
		response.Rcode = dns.RcodeRefused
		return response, rCon, result
	}

	// drop questions for domain names that could be malicious
	if len(request.Question[0].Name) < 1 || len(request.Question[0].Name) > 255 {
		response = new(dns.Msg)
		response.SetReply(request)
		response.Rcode = dns.RcodeBadName
		return response, rCon, result
	}

	// drop questions that aren't implemented
	qType := request.Question[0].Qtype
	if _, found := notImplemented[qType]; found {
		response = new(dns.Msg)
		response.SetReply(request)
		response.Rcode = dns.RcodeNotImplemented
		return response, rCon, result
	}

	// get domain name
	domain := request.Question[0].Name

	// get block status and refuse the request
	if consumer.configConsumer.Block {
		result.Blocked = true
		response = new(dns.Msg)
		response.Rcode = dns.RcodeRefused
		response.SetReply(request)
	} else {
		match, list, ruleText := engine.domainRuleMatchedForConsumer(consumer, domain)
		if match != rule.MatchNone {
			result.Match = match
			result.MatchList = list
			result.MatchRule = ruleText
		}
		if match != rule.MatchBlock {
			// if not blocked then actually try resolution, by grabbing the resolver names
			resolverNames := engine.getResolvers(consumer)
			response, result, err = engine.resolvers.AnswerMultiResolvers(rCon, resolverNames, request)
			if err != nil {
				log.Errorf("Could not resolve <%s> for consumer '%s': %s", domain, consumer.configConsumer.Name, err)
			} else {
				cnameResponse := engine.handleCnameResolution(address, protocol, request, response)
				if !util.IsEmptyResponse(cnameResponse) {
					response = cnameResponse
				}
			}
		}
	}

	// if no response is found at this point ensure it is created
	if response == nil {
		response = new(dns.Msg)
	}

	// set codes for response/reply
	if util.IsEmptyResponse(response) {
		response.SetReply(request)
		response.Rcode = dns.RcodeNameError
	}

	// recover during response... this isn't the best golang paradigm but if we don't
	// do this then dns just stops and the entire executable crashes and we stop getting
	// resolution. if you're eating your own dogfood on this one then you lose DNS until
	// you can find and fix the bug which is not ideal.
	if recovery := recover(); recovery != nil {
		response = new(dns.Msg)
		response.SetReply(request)
		response.Rcode = dns.RcodeServerFailure

		// add panic reason to result
		result.Message = fmt.Sprintf("%v", recovery)
	}

	// new result
	if result == nil {
		result = &resolver.ResolutionResult{}
	}

	// update/set
	if consumer != nil && consumer.configConsumer != nil {
		result.Consumer = consumer.configConsumer.Name
	}

	// return result
	return response, rCon, result
}

func (engine *engine) Resolve(domainName string) (string, error) {
	m := &dns.Msg{
		MsgHdr: dns.MsgHdr{
			Authoritative:     true,
			AuthenticatedData: true,
			RecursionDesired:  true,
			Opcode:            dns.OpcodeQuery,
		},
	}

	if domainName == "" {
		return domainName, fmt.Errorf("cannot resolve an empty domain name")
	}

	// ensure the domain name is fully qualified
	domainName = dns.Fqdn(domainName)

	// make question parts
	m.Question = make([]dns.Question, 1)
	m.Question[0] = dns.Question{Name: domainName, Qtype: dns.TypeA, Qclass: dns.ClassINET}

	// get just response
	address := net.ParseIP("127.0.0.1")
	response, _, _ := engine.performRequest(&address, "udp", m)

	// return answer
	return util.GetFirstIPResponse(response), nil
}

// return the reverse lookup details for an address and return the result of the (first) ptr record
func (engine *engine) Reverse(address string) string {
	// cannot do reverse lookup
	if address == "" || net.ParseIP(address) == nil {
		return ""
	}

	m := &dns.Msg{
		MsgHdr: dns.MsgHdr{
			Authoritative:     true,
			AuthenticatedData: true,
			RecursionDesired:  true,
			Opcode:            dns.OpcodeQuery,
		},
	}

	// we already checked and know this won't be nil
	ip := net.ParseIP(address)

	// make question parts
	m.Question = make([]dns.Question, 1)
	m.Question[0] = dns.Question{Name: util.ReverseLookupDomain(&ip), Qtype: dns.TypePTR, Qclass: dns.ClassINET}

	// get just response
	client := net.ParseIP("127.0.0.1")
	response, _, _ := engine.performRequest(&client, "udp", m)

	// look for first pointer
	for _, answer := range response.Answer {
		if aRecord, ok := answer.(*dns.PTR); ok {
			if aRecord != nil && aRecord.Ptr != "" {
				return strings.TrimSpace(aRecord.Ptr)
			}
		}
	}

	// return answer
	return ""
}

// entry point for external handler
func (engine *engine) Handle(address *net.IP, protocol string, request *dns.Msg) (*dns.Msg, *resolver.RequestContext, *resolver.ResolutionResult) {
	// get results
	response, rCon, result := engine.performRequest(address, protocol, request)

	// log them if recorder is active
	if engine.recorder != nil {
		engine.recorder.queue(address, request, response, rCon, result)
	}

	// return only the result
	return response, rCon, result
}

func (engine *engine) CacheSize() int64 {
	if engine.resolvers != nil && engine.resolvers.Cache() != nil {
		return int64(engine.resolvers.Cache().Size())
	}
	return 0
}

func (engine *engine) Metrics() Metrics {
	return engine.metrics
}

func (engine *engine) QueryLog() QueryLog {
	return engine.qlog
}

func (engine *engine) Shutdown() {
	// shutting down the recorder shuts down
	// other elements in turn
	if nil != engine.recorder {
		engine.recorder.shutdown()
	}

	// close db
	if nil != engine.db {
		engine.db.Close()
	}
}
