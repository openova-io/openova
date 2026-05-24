// SetDNSRecords / GetDNSRecords — implements registrar.DNSRegistrar
// for Dynadot using the set_dns2 + domain_info commands.
//
// Dynadot's set_dns2 command separates record types by scope:
//   - main_record_typeN / main_record_valueN / main_record_ttlN
//     → records on the root of the zone (@)
//   - sub_record_typeN / sub_recordN / sub_record_valueN / sub_record_ttlN
//     → records on a subdomain (the sub_recordN field carries the
//       leftmost label(s))
//   - mx_priorityN → MX priority parallel to the *_record arrays
//
// The implementation packs the caller's []DNSRecord into these
// parallel arrays. SetDNSRecords is a full REPLACE — any record
// not in the caller's list is removed from Dynadot's zone, per the
// SetDNSRecords contract documented in registrar.go.

package dynadot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/openova-io/openova/core/pool-domain-manager/internal/registrar"
)

// SetDNSRecords implements registrar.DNSRegistrar.
//
// Wire shape (subset, ordered by Dynadot api3.json):
//
//	command            = "set_dns2"
//	domain             = <fqdn>
//	main_record_typeN  = A | AAAA | CNAME | MX | TXT   (root records)
//	main_record_valueN = <data>                         (per type)
//	main_record_ttlN   = <seconds>                      (0 → default)
//	mx_priority0       = <int>                          (only when type=MX)
//	sub_recordN        = <leftmost-label>               (subdomain records)
//	sub_record_typeN   = A | AAAA | CNAME | MX | TXT
//	sub_record_valueN  = <data>
//	sub_record_ttlN    = <seconds>
//	sub_mx_priorityN   = <int>                          (only when type=MX)
func (a *Adapter) SetDNSRecords(ctx context.Context, token, domain string, records []registrar.DNSRecord) error {
	key, secret, err := parseToken(token)
	if err != nil {
		return err
	}

	params := url.Values{}
	params.Set("key", key)
	params.Set("secret", secret)
	params.Set("command", "set_dns2")
	params.Set("domain", domain)

	var mainIdx, subIdx int
	for _, rec := range records {
		t := strings.ToUpper(strings.TrimSpace(rec.Type))
		sub := strings.ToLower(strings.TrimSpace(rec.Subhost))
		val := strings.TrimSpace(rec.Value)
		if val == "" {
			return fmt.Errorf("dynadot: record value is required (type=%s subhost=%q)", t, sub)
		}
		if sub == "" || sub == "@" {
			// Root record — main_record_*
			params.Set(fmt.Sprintf("main_record_type%d", mainIdx), t)
			params.Set(fmt.Sprintf("main_record%d", mainIdx), val)
			if rec.TTL > 0 {
				params.Set(fmt.Sprintf("main_record_ttl%d", mainIdx), itoa(rec.TTL))
			}
			if t == "MX" {
				params.Set(fmt.Sprintf("main_mx_priority%d", mainIdx), itoa(rec.Priority))
			}
			mainIdx++
			continue
		}
		// Subdomain record — sub_record_*
		params.Set(fmt.Sprintf("sub_record%d", subIdx), sub)
		params.Set(fmt.Sprintf("sub_record_type%d", subIdx), t)
		params.Set(fmt.Sprintf("sub_record_value%d", subIdx), val)
		if rec.TTL > 0 {
			params.Set(fmt.Sprintf("sub_record_ttl%d", subIdx), itoa(rec.TTL))
		}
		if t == "MX" {
			params.Set(fmt.Sprintf("sub_mx_priority%d", subIdx), itoa(rec.Priority))
		}
		subIdx++
	}

	body, err := a.do(ctx, params)
	if err != nil {
		return err
	}
	// set_dns2 live response shape per Dynadot api3.json docs:
	//   {"SetDns2Response":{"ResponseCode":0,"Status":"success"}}
	var raw struct {
		SetDns2Response struct {
			respHeader
		} `json:"SetDns2Response"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return fmt.Errorf("dynadot: parse set_dns2: %w", err)
	}
	return classifyDynadotError(raw.SetDns2Response.respHeader)
}

// GetDNSRecords reads back the current record set via Dynadot's
// `domain_info` command. The response includes a NameServerSettings
// block + a DnsType / Records block. We translate the per-record
// Dynadot shape into the provider-agnostic registrar.DNSRecord.
//
// Returns an empty slice (not nil, not error) when the zone has no
// records — that's a valid state for a fresh domain.
func (a *Adapter) GetDNSRecords(ctx context.Context, token, domain string) ([]registrar.DNSRecord, error) {
	key, secret, err := parseToken(token)
	if err != nil {
		return nil, err
	}

	params := url.Values{}
	params.Set("key", key)
	params.Set("secret", secret)
	params.Set("command", "domain_info")
	params.Set("domain", domain)

	body, err := a.do(ctx, params)
	if err != nil {
		return nil, err
	}

	// domain_info response shape (subset):
	//   {"DomainInfoResponse": {
	//      "ResponseCode": 0,
	//      "DomainInfoContent": {
	//        "NameServerSettings": {
	//          "Type": "Dynadot DNS",
	//          "MainDomains": [{ "RecordType": "A", "Value": "1.2.3.4", "Ttl": 3600 }, ...],
	//          "Subdomains":  [{ "Subhost": "www", "RecordType": "CNAME", "Value": "example.com", "Ttl": 3600 }, ...]
	//        }
	//      }
	//   }}
	var raw struct {
		DomainInfoResponse struct {
			respHeader
			DomainInfoContent struct {
				NameServerSettings struct {
					MainDomains []struct {
						RecordType string      `json:"RecordType"`
						Value      string      `json:"Value"`
						Ttl        json.Number `json:"Ttl"`
						MxPriority json.Number `json:"MxPriority"`
					} `json:"MainDomains"`
					Subdomains []struct {
						Subhost    string      `json:"Subhost"`
						RecordType string      `json:"RecordType"`
						Value      string      `json:"Value"`
						Ttl        json.Number `json:"Ttl"`
						MxPriority json.Number `json:"MxPriority"`
					} `json:"Subdomains"`
				} `json:"NameServerSettings"`
			} `json:"DomainInfoContent"`
		} `json:"DomainInfoResponse"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("dynadot: parse domain_info: %w", err)
	}
	if e := classifyDynadotError(raw.DomainInfoResponse.respHeader); e != nil {
		return nil, e
	}

	settings := raw.DomainInfoResponse.DomainInfoContent.NameServerSettings
	out := make([]registrar.DNSRecord, 0, len(settings.MainDomains)+len(settings.Subdomains))
	for _, m := range settings.MainDomains {
		ttl, _ := m.Ttl.Int64()
		prio, _ := m.MxPriority.Int64()
		out = append(out, registrar.DNSRecord{
			Subhost:  "",
			Type:     strings.ToUpper(m.RecordType),
			Value:    m.Value,
			TTL:      int(ttl),
			Priority: int(prio),
		})
	}
	for _, s := range settings.Subdomains {
		ttl, _ := s.Ttl.Int64()
		prio, _ := s.MxPriority.Int64()
		out = append(out, registrar.DNSRecord{
			Subhost:  s.Subhost,
			Type:     strings.ToUpper(s.RecordType),
			Value:    s.Value,
			TTL:      int(ttl),
			Priority: int(prio),
		})
	}
	return out, nil
}

// itoa avoids importing strconv just for the URL-param encoding —
// the encoder runs once per record so the trade-off is minimal.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// Compile-time check that *Adapter implements DNSRegistrar.
var _ registrar.DNSRegistrar = (*Adapter)(nil)
