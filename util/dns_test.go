package util

import (
	"net"
	"testing"

	"github.com/miekg/dns"
)

func TestIsEmpty(t *testing.T) {

	response := &dns.Msg{
		MsgHdr: dns.MsgHdr{
			Authoritative:     false,
			AuthenticatedData: false,
			CheckingDisabled:  false,
			RecursionDesired:  false,
			Opcode:            dns.OpcodeQuery,
		},
	}

	if !IsEmptyResponse(response) {
		t.Errorf("Expected empty response right away")
	}

	// add empty answer set
	response.Answer = make([]dns.RR, 100)
	if !IsEmptyResponse(response) {
		t.Errorf("Expected empty response even when response has answer size")
	}

	// add an empty A record
	response.Answer[0] = &dns.A{
		Hdr: dns.RR_Header{Name: "test.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 0},
	}
	if !IsEmptyResponse(response) {
		t.Errorf("Expected empty response even with empty A record")
	}

	// add an empty AAAA record
	response.Answer[1] = &dns.AAAA{
		Hdr: dns.RR_Header{Name: "test.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 0},
	}
	if !IsEmptyResponse(response) {
		t.Errorf("Expected empty response even with empty AAAA record")
	}

	// should still be empty even with made NS records
	response.Ns = make([]dns.RR, 100)
	if !IsEmptyResponse(response) {
		t.Errorf("Expected empty response even with empty Ns records")
	}

	// should still be empty even with a full Ns record (because answer has non-full records in it)
	response.Ns[0] = &dns.SOA{
		Hdr:     dns.RR_Header{Name: "test.", Rrtype: dns.TypeSOA, Class: dns.ClassINET, Ttl: 0},
		Ns:      "ns.soa.com",
		Mbox:    "ns.soa.com",
		Serial:  123394,
		Refresh: 10000,
		Retry:   1000,
		Expire:  456,
		Minttl:  300,
	}
	if !IsEmptyResponse(response) {
		t.Errorf("Expected empty response even with Ns record")
	}

	// add a nil A record
	response.Answer[2] = &dns.A{
		Hdr: dns.RR_Header{Name: "test.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 0},
		A:   nil,
	}
	if !IsEmptyResponse(response) {
		t.Errorf("Expected empty response even with nil A record")
	}

	// add something that will work
	response.Answer[3] = &dns.A{
		Hdr: dns.RR_Header{Name: "test.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 0},
		A:   net.ParseIP("127.0.0.1"),
	}
	if IsEmptyResponse(response) {
		t.Errorf("Did not expect empty response after adding A record")
	}
}

func TestGetRecordValue(t *testing.T) {
	// create static header
	hdr := dns.RR_Header{Name: "test.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 0}

	// A record
	if "127.0.0.1" != GetRecordValue(&dns.A{Hdr: hdr, A: net.ParseIP("127.0.0.1")}) {
		t.Errorf("Could not parse expectd 127.0.0.1 from A record")
	}
	if "10.0.0.1" != GetRecordValue(dns.A{Hdr: hdr, A: net.ParseIP("10.0.0.1")}) {
		t.Errorf("Could not parse expectd 127.0.0.1 from A record")
	}

	// AAAA record
	if "::1" != GetRecordValue(&dns.AAAA{Hdr: hdr, AAAA: net.ParseIP("::1")}) {
		t.Errorf("Could not parse expectd ::1 from AAAA record")
	}

	// PTR Record
	if "google.com" != GetRecordValue(&dns.PTR{Hdr: hdr, Ptr: "google.com"}) {
		t.Errorf("Could not parse expectd 'google.com' from PTR record")
	}

	// TXT Record
	if "h hello" != GetRecordValue(&dns.TXT{Hdr: hdr, Txt: []string{"h", "hello"}}) {
		t.Errorf("Could not parse expected TXT record")
	}
}

func TestGetAnswerValues(t *testing.T) {

	response := &dns.Msg{
		MsgHdr: dns.MsgHdr{
			Authoritative:     false,
			AuthenticatedData: false,
			CheckingDisabled:  false,
			RecursionDesired:  false,
			Opcode:            dns.OpcodeQuery,
		},
	}
	response.Answer = make([]dns.RR, 3)

	// add a
	response.Answer[0] = &dns.A{
		Hdr: dns.RR_Header{Name: "test.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 0},
		A:   net.ParseIP("127.0.0.1"),
	}
	// add aaaa
	response.Answer[1] = &dns.AAAA{
		Hdr:  dns.RR_Header{Name: "test.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 0},
		AAAA: net.ParseIP("::1"),
	}

	// add ptr
	response.Answer[2] = &dns.PTR{
		Hdr: dns.RR_Header{Name: "test.", Rrtype: dns.TypePTR, Class: dns.ClassINET, Ttl: 0},
		Ptr: "google.com",
	}

	// get values
	values := GetAnswerValues(response)

	if len(values) < len(response.Answer) {
		t.Errorf("Could not get all values for response, expected %d but got %d", len(response.Answer), len(values))
	}
}
