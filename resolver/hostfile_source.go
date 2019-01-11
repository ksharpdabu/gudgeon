package resolver

import (
	"net"
	"strings"

	"github.com/miekg/dns"

	"github.com/chrisruffalo/gudgeon/util"
)

const (
	ttl        = 0 // default to never cache since this is basically a free action
	wildcard   = "*"
	comment    = "#"
	altComment = "//"
)

type hostFileSource struct {
	filePath      string
	hostEntries   map[string][]*net.IP
	cnameEntries  map[string]string
	reverseLookup map[string][]string
	dnsWildcards  map[string][]*net.IP
}

func newHostFileSource(sourceFile string) Source {
	source := new(hostFileSource)
	source.filePath = sourceFile

	// make new map
	source.hostEntries = make(map[string][]*net.IP)
	source.cnameEntries = make(map[string]string)
	source.reverseLookup = make(map[string][]string)
	source.dnsWildcards = make(map[string][]*net.IP)

	// open file and parse each line
	data, err := util.GetFileAsArray(sourceFile)

	// on error return empty source
	if err != nil {
		// todo: logging
		return source
	}

	// parse each line
	for _, d := range data {
		// trim whitespace
		d = strings.TrimSpace(d)

		// skip empty strings or strings that start with a comment
		if "" == d || strings.HasPrefix(d, wildcard) || strings.HasPrefix(d, comment) || strings.HasPrefix(d, altComment) {
			continue
		}

		// condition string, all whitespace replaced with actual literal " "
		d = strings.Replace(d, "\t", " ", -1)

		// commas too
		d = strings.Replace(d, ",", " ", -1)

		// remove multiple adjacent spaces
		newstring := ""
		for newstring != d {
			newstring = d
			d = strings.Replace(d, "  ", " ", -1)
		}

		// split after first space
		values := strings.SplitN(d, " ", 2)

		// need at least two values to continue
		if len(values) < 2 {
			continue
		}

		// get domain
		address := values[0]
		address = strings.TrimSpace(address)
		parsedAddress := net.ParseIP(address)

		// parse out list of domains
		domains := strings.Split(values[1], " ")

		if parsedAddress != nil {
			// add to reverse lookup
			ptr := util.ReverseLookupDomain(&parsedAddress)
			source.reverseLookup[ptr] = domains

			// add to map
			for _, domain := range domains {
				if !strings.HasSuffix(domain, ".") {
					domain = domain + "."
				}

				// append value to list
				source.hostEntries[domain] = append(source.hostEntries[domain], &parsedAddress)
			}
		} else {
			// treat address as cname entry
			// target alias alias alias alias
			target := address
			if !strings.HasSuffix(target, ".") {
				target = target + "."
			}

			// add target to alias cname lookup
			for _, alias := range domains {
				if !strings.HasSuffix(alias, ".") {
					alias = alias + "."
				}
				// only one alias per taget
				if "" == source.cnameEntries[alias] {
					source.cnameEntries[alias] = target
				}
			}
		}

	}

	return source
}

func (hostFileSource *hostFileSource) respondToA(name string, response *dns.Msg) {
	// if the domain is available from the host file, go through it
	if val, ok := hostFileSource.hostEntries[name]; ok {
		offset := len(response.Answer)
		if offset > 0 {
			response.Answer = append(response.Answer, make([]dns.RR, len(val))...)
		} else {
			response.Answer = make([]dns.RR, len(val))
		}

		// entries were found so we need to loop through them
		for idx, address := range val {
			// skip nil addresses
			if address == nil {
				continue
			}

			// create response based on parsed address type (ipv6 or not)
			ipV4 := address.To4()
			ipV6 := address.To16()

			if ipV4 != nil {
				rr := &dns.A{
					Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: ttl},
					A:   ipV4,
				}
				response.Answer[offset+idx] = rr
			} else if ipV6 != nil {
				rr := &dns.AAAA{
					Hdr:  dns.RR_Header{Name: name, Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: ttl},
					AAAA: ipV6,
				}
				response.Answer[offset+idx] = rr
			}
		}
	}
}

func (hostFileSource *hostFileSource) respondToPTR(name string, response *dns.Msg) {
	// if the (reverse lookup) domain is available from the host file, go through it
	if val, ok := hostFileSource.reverseLookup[name]; ok {
		offset := len(response.Answer)
		if offset > 0 {
			response.Answer = append(response.Answer, make([]dns.RR, len(val))...)
		} else {
			response.Answer = make([]dns.RR, len(val))
		}

		// entries were found so we need to loop through them
		for idx, ptr := range val {
			// skip empty ptr
			if "" == ptr {
				continue
			}

			if !strings.HasSuffix(ptr, ".") {
				ptr = ptr + "."
			}

			rr := &dns.PTR{
				Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypePTR, Class: dns.ClassINET, Ttl: ttl},
				Ptr: ptr,
			}
			response.Answer[offset+idx] = rr
		}
	}
}

func (hostFileSource *hostFileSource) respondToCNAME(name string, response *dns.Msg) {
	// if the domain is available from the host file, go through it
	if cname, ok := hostFileSource.cnameEntries[name]; ok {
		response.Answer = make([]dns.RR, 1)

		// skip empty ptr
		if "" == cname {
			return
		}

		if !strings.HasSuffix(cname, ".") {
			cname = cname + "."
		}

		rr := &dns.CNAME{
			Hdr:    dns.RR_Header{Name: name, Rrtype: dns.TypeCNAME, Class: dns.ClassINET, Ttl: ttl},
			Target: cname,
		}
		response.Answer[0] = rr
	}
}

func (hostFileSource *hostFileSource) Name() string {
	return "hostfile:" + hostFileSource.filePath
}

func (hostFileSource *hostFileSource) Answer(context *ResolutionContext, request *dns.Msg) (*dns.Msg, error) {
	// return nil response if no question was formed
	if len(request.Question) < 1 {
		return nil, nil
	}

	// get details from question
	question := request.Question[0]
	name := question.Name
	qType := question.Qtype

	// can only respond to A, AAAA, PTR, and CNAME questions
	if qType != dns.TypeANY && qType != dns.TypeA && qType != dns.TypeAAAA && qType != dns.TypePTR && qType != dns.TypeCNAME {
		return nil, nil
	}

	// create new response message
	response := &dns.Msg{
		MsgHdr: dns.MsgHdr{
			Authoritative:     request.MsgHdr.Authoritative,
			AuthenticatedData: request.MsgHdr.AuthenticatedData,
			CheckingDisabled:  request.MsgHdr.CheckingDisabled,
			RecursionDesired:  request.MsgHdr.RecursionDesired,
			Opcode:            dns.OpcodeQuery,
		},
	}
	response.SetReply(request)

	// handle appropriate question type
	if qType == dns.TypeANY || qType == dns.TypeCNAME {
		hostFileSource.respondToCNAME(name, response)
	}

	if qType == dns.TypeANY || qType == dns.TypeA || qType == dns.TypeAAAA {
		// look for cnames before looking for other names
		if qType != dns.TypeANY {
			hostFileSource.respondToCNAME(name, response)
		}
		// if no cnames are we can look for A/AAAA responses
		if qType == dns.TypeANY || len(response.Answer) < 1 {
			hostFileSource.respondToA(name, response)
		}
	}

	if qType == dns.TypeANY || qType == dns.TypePTR {
		hostFileSource.respondToPTR(name, response)
	}

	// set source as answering source
	if context != nil {
		context.SourceUsed = hostFileSource.Name()
	}

	return response, nil
}
