package resolver

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/miekg/dns"

	"github.com/chrisruffalo/gudgeon/util"
)

const (
	defaultPort    = uint(53)
	defaultTLSPort = uint(853)
	portDelimeter  = ":"
	protoDelimeter = "/"
)

// how long to wait before source is active again
var backoffInterval = 15 * time.Second
var defaultTimeout = 1 * time.Second

var validProtocols = []string{"udp", "tcp", "tcp-tls"}

type dnsSource struct {
	dnsServer     string
	port          uint
	remoteAddress string
	protocol      string

	backoffTime *time.Time
	tlsConfig   *tls.Config
}

func newDNSSource(sourceAddress string) Source {
	source := new(dnsSource)
	source.port = 0
	source.dnsServer = ""
	source.protocol = ""

	// determine first if there is an attached protocol
	if strings.Contains(sourceAddress, protoDelimeter) {
		split := strings.Split(sourceAddress, protoDelimeter)
		if len(split) > 1 && util.StringIn(strings.ToLower(split[1]), validProtocols) {
			sourceAddress = split[0]
			source.protocol = strings.ToLower(split[1])
		}
	}

	// need to determine if a port comes along with the address and parse it out once
	if strings.Contains(sourceAddress, portDelimeter) {
		split := strings.Split(sourceAddress, portDelimeter)
		if len(split) > 1 {
			source.dnsServer = split[0]
			var err error
			parsePort, err := strconv.ParseUint(split[1], 10, 32)
			// recover from error
			if err != nil {
				source.port = 0
			} else {
				source.port = uint(parsePort)
			}
		}
	} else {
		source.dnsServer = sourceAddress
	}

	// recover from parse errors or use default port in event port wasn't set
	if source.port == 0 {
		if "tcp-tls" == source.protocol {
			source.port = defaultTLSPort
		} else {
			source.port = defaultPort
		}
	}

	// set up tls config
	source.tlsConfig = &tls.Config{InsecureSkipVerify: true}

	// check final output
	if ip := net.ParseIP(source.dnsServer); ip == nil {
		return nil
	}

	// save/parse remote address once
	source.remoteAddress = fmt.Sprintf("%s%s%d", source.dnsServer, portDelimeter, source.port)

	return source
}

func (dnsSource *dnsSource) Name() string {
	return dnsSource.remoteAddress
}

func (dnsSource *dnsSource) query(coType string, request *dns.Msg, remoteAddress string) (*dns.Msg, error) {
	var err error

	// create new request context
	context, cancel := context.WithTimeout(context.Background(), 4*defaultTimeout)
	defer cancel()

	co := &dns.Conn{}
	dialer := &net.Dialer{
		Timeout: defaultTimeout,
	}
	if coType == "tcp-tls" {
		conn, err := dialer.DialContext(context, "tcp", remoteAddress)
		if err != nil {
			return nil, err
		}
		co.Conn = tls.Client(conn, dnsSource.tlsConfig)
		defer conn.Close()
	} else {
		if co.Conn, err = dialer.DialContext(context, coType, remoteAddress); err != nil {
			return nil, err
		}
	}
	defer co.Conn.Close()

	// update deadline waiting for write to succeed
	co.Conn.SetDeadline(time.Now().Add(2 * defaultTimeout))

	// write message
	if err := co.WriteMsg(request); err != nil {
		co.Close()
		return nil, err
	}

	// read response with deadline
	co.Conn.SetDeadline(time.Now().Add(2 * defaultTimeout))
	response, err := co.ReadMsg()
	if err != nil {
		co.Close()
		return nil, err
	}

	// close and return response
	co.Close()
	return response, nil
}

func (dnsSource *dnsSource) Answer(rCon *RequestContext, context *ResolutionContext, request *dns.Msg) (*dns.Msg, error) {
	now := time.Now()
	if dnsSource.backoffTime != nil && now.Before(*dnsSource.backoffTime) {
		// "asleep" during backoff interval
		return nil, nil
	}
	// the backoff time is irrelevant now
	dnsSource.backoffTime = nil

	// this is considered a recursive query so don't if recursion was not requested
	if request == nil || !request.MsgHdr.RecursionDesired {
		return nil, nil
	}

	// default
	protocol := dnsSource.protocol
	if protocol == "" && rCon != nil {
		protocol = rCon.Protocol
	} else if protocol == "" {
		protocol = "udp"
	}

	// forward message without interference
	response, err := dnsSource.query(protocol, request, dnsSource.remoteAddress)
	if err != nil {
		backoff := time.Now().Add(backoffInterval)
		dnsSource.backoffTime = &backoff
		return nil, err
	}

	// do not set reply here (doesn't seem to matter, leaving this comment so nobody decides to do it in the future without cause)
	// response.SetReply(request)

	// set source as answering source
	if context != nil && !util.IsEmptyResponse(response) && context.SourceUsed == "" {
		context.SourceUsed = dnsSource.Name() + "/" + protocol
	}

	// otherwise just return
	return response, nil
}
