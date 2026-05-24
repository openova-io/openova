// SetDNSRecords / GetDNSRecords — implements registrar.DNSRegistrar
// for Namecheap using the namecheap.domains.dns.setHosts +
// namecheap.domains.dns.getHosts XML API.
//
// Wire shape per https://www.namecheap.com/support/api/methods/domains-dns/set-hosts/
//
//   POST  ?ApiUser=<u>&ApiKey=<k>&UserName=<u>&ClientIp=<ip>
//        &Command=namecheap.domains.dns.setHosts
//        &SLD=<example>
//        &TLD=<com>
//        &HostName1=@        &RecordType1=A      &Address1=1.2.3.4   &TTL1=3600
//        &HostName2=www      &RecordType2=CNAME  &Address2=example.com.
//        &HostName3=@        &RecordType3=MX     &Address3=mail.example.com   &MXPref3=10
//        &HostName4=_acme-challenge   &RecordType4=TXT   &Address4=abc123
//
// Namecheap REQUIRES the domain to be using Namecheap's NameServers
// (the default "PrivateEmail" / "BasicDNS" setting) for setHosts to
// take effect. If the customer's SetNameservers list points elsewhere,
// setHosts returns an error from the Namecheap side.

package namecheap

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/openova-io/openova/core/pool-domain-manager/internal/registrar"
)

// SetDNSRecords implements registrar.DNSRegistrar.
func (a *Adapter) SetDNSRecords(ctx context.Context, token, domain string, records []registrar.DNSRecord) error {
	c, err := parseToken(token)
	if err != nil {
		return err
	}
	sld, tld, err := splitDomain(domain)
	if err != nil {
		return err
	}

	params := url.Values{}
	params.Set("SLD", sld)
	params.Set("TLD", tld)

	for i, rec := range records {
		t := strings.ToUpper(strings.TrimSpace(rec.Type))
		val := strings.TrimSpace(rec.Value)
		if val == "" {
			return fmt.Errorf("namecheap: record value is required (idx=%d type=%s)", i, t)
		}
		host := strings.TrimSpace(rec.Subhost)
		if host == "" {
			host = "@"
		}
		// 1-indexed per Namecheap API.
		n := strconv.Itoa(i + 1)
		params.Set("HostName"+n, host)
		params.Set("RecordType"+n, t)
		params.Set("Address"+n, val)
		if rec.TTL > 0 {
			params.Set("TTL"+n, strconv.Itoa(rec.TTL))
		}
		if t == "MX" {
			params.Set("MXPref"+n, strconv.Itoa(rec.Priority))
			params.Set("EmailType", "MX")
		}
	}

	body, err := a.do(ctx, c, "namecheap.domains.dns.setHosts", params)
	if err != nil {
		return err
	}
	var env errResponse
	if err := xml.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("namecheap: parse setHosts: %w", err)
	}
	return classifyEnvelope(env)
}

// GetDNSRecords reads via namecheap.domains.dns.getHosts.
func (a *Adapter) GetDNSRecords(ctx context.Context, token, domain string) ([]registrar.DNSRecord, error) {
	c, err := parseToken(token)
	if err != nil {
		return nil, err
	}
	sld, tld, err := splitDomain(domain)
	if err != nil {
		return nil, err
	}
	params := url.Values{}
	params.Set("SLD", sld)
	params.Set("TLD", tld)
	body, err := a.do(ctx, c, "namecheap.domains.dns.getHosts", params)
	if err != nil {
		return nil, err
	}

	// getHosts response shape:
	//   <ApiResponse Status="OK">
	//     <CommandResponse>
	//       <DomainDNSGetHostsResult ...>
	//         <host Name="@" Type="A"  Address="1.2.3.4" TTL="3600" MXPref="10"/>
	//         <host Name="www" Type="CNAME" Address="example.com." TTL="600"/>
	//         ...
	//       </DomainDNSGetHostsResult>
	//     </CommandResponse>
	//   </ApiResponse>
	var raw struct {
		XMLName xml.Name `xml:"ApiResponse"`
		Status  string   `xml:"Status,attr"`
		Errors  struct {
			Error []struct {
				Number string `xml:"Number,attr"`
				Value  string `xml:",chardata"`
			} `xml:"Error"`
		} `xml:"Errors"`
		CommandResponse struct {
			Result struct {
				Hosts []struct {
					Name    string `xml:"Name,attr"`
					Type    string `xml:"Type,attr"`
					Address string `xml:"Address,attr"`
					TTL     string `xml:"TTL,attr"`
					MXPref  string `xml:"MXPref,attr"`
				} `xml:"host"`
			} `xml:"DomainDNSGetHostsResult"`
		} `xml:"CommandResponse"`
	}
	if err := xml.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("namecheap: parse getHosts: %w", err)
	}
	if strings.ToUpper(raw.Status) != "OK" {
		// Build an errResponse inline so we can re-use classifyEnvelope.
		var env errResponse
		env.Status = raw.Status
		for _, e := range raw.Errors.Error {
			env.Errors.Error = append(env.Errors.Error, struct {
				Number string `xml:"Number,attr"`
				Value  string `xml:",chardata"`
			}{Number: e.Number, Value: e.Value})
		}
		if e := classifyEnvelope(env); e != nil {
			return nil, e
		}
	}

	out := make([]registrar.DNSRecord, 0, len(raw.CommandResponse.Result.Hosts))
	for _, h := range raw.CommandResponse.Result.Hosts {
		sub := h.Name
		if sub == "@" {
			sub = ""
		}
		ttl, _ := strconv.Atoi(h.TTL)
		prio, _ := strconv.Atoi(h.MXPref)
		out = append(out, registrar.DNSRecord{
			Subhost:  sub,
			Type:     strings.ToUpper(h.Type),
			Value:    h.Address,
			TTL:      ttl,
			Priority: prio,
		})
	}
	return out, nil
}

// Compile-time interface check.
var _ registrar.DNSRegistrar = (*Adapter)(nil)
