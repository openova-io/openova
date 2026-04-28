package handlers

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"time"
)

// whoisServers maps TLDs to their WHOIS servers.
var whoisServers = map[string]string{
	"com":   "whois.verisign-grs.com",
	"net":   "whois.verisign-grs.com",
	"org":   "whois.pir.org",
	"io":    "whois.nic.io",
	"co":    "whois.nic.co",
	"dev":   "whois.nic.google",
	"app":   "whois.nic.google",
	"me":    "whois.nic.me",
	"info":  "whois.afilias.net",
	"biz":   "whois.biz",
	"us":    "whois.nic.us",
	"uk":    "whois.nic.uk",
	"de":    "whois.denic.de",
	"fr":    "whois.nic.fr",
	"nl":    "whois.sidn.nl",
	"au":    "whois.auda.org.au",
	"ca":    "whois.cira.ca",
	"in":    "whois.registry.in",
	"xyz":   "whois.nic.xyz",
	"tech":  "whois.nic.tech",
	"store": "whois.nic.store",
	"site":  "whois.nic.site",
	"rest":  "whois.nic.rest",
}

// detectRegistrar performs a WHOIS lookup on the domain and returns the registrar name.
func detectRegistrar(domain string) (string, error) {
	// Extract the root domain (e.g., "sub.example.com" -> "example.com").
	root := rootDomain(domain)
	parts := strings.Split(root, ".")
	if len(parts) < 2 {
		return "unknown", nil
	}
	tld := parts[len(parts)-1]

	server, ok := whoisServers[tld]
	if !ok {
		server = "whois.iana.org"
	}

	conn, err := net.DialTimeout("tcp", server+":43", 5*time.Second)
	if err != nil {
		return "unknown", fmt.Errorf("whois: connect to %s: %w", server, err)
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(5 * time.Second))

	_, err = fmt.Fprintf(conn, "%s\r\n", root)
	if err != nil {
		return "unknown", fmt.Errorf("whois: write query: %w", err)
	}

	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		line := scanner.Text()
		// Look for "Registrar:" in the response.
		if idx := strings.Index(strings.ToLower(line), "registrar:"); idx != -1 {
			registrar := strings.TrimSpace(line[idx+len("registrar:"):])
			if registrar != "" {
				return registrar, nil
			}
		}
		// Some WHOIS servers use "Registrar Name:" instead.
		if idx := strings.Index(strings.ToLower(line), "registrar name:"); idx != -1 {
			registrar := strings.TrimSpace(line[idx+len("registrar name:"):])
			if registrar != "" {
				return registrar, nil
			}
		}
	}

	return "unknown", nil
}

// rootDomain extracts the registrable domain from a potentially fully-qualified hostname.
// e.g., "www.sub.example.com" -> "example.com"
func rootDomain(domain string) string {
	domain = strings.TrimSuffix(domain, ".")
	parts := strings.Split(domain, ".")
	if len(parts) <= 2 {
		return domain
	}
	return strings.Join(parts[len(parts)-2:], ".")
}

// dnsInstructions returns markdown-formatted DNS configuration instructions
// for the given registrar to set up a CNAME record pointing to the target.
func dnsInstructions(registrar, targetCNAME string) string {
	lower := strings.ToLower(registrar)

	switch {
	case strings.Contains(lower, "godaddy"):
		return fmt.Sprintf(`### GoDaddy DNS Setup

1. Log in to your [GoDaddy account](https://account.godaddy.com/)
2. Go to **My Products** > select your domain > **DNS**
3. Click **Add Record**
4. Set **Type** to **CNAME**
5. Set **Name** to **@** (or your subdomain)
6. Set **Value** to `+"`%s`"+`
7. Set **TTL** to **1 Hour**
8. Click **Save**`, targetCNAME)

	case strings.Contains(lower, "namecheap"):
		return fmt.Sprintf(`### Namecheap DNS Setup

1. Log in to your [Namecheap account](https://www.namecheap.com/myaccount/)
2. Go to **Domain List** > click **Manage** on your domain
3. Go to **Advanced DNS** tab
4. Click **Add New Record**
5. Select **CNAME Record**
6. Set **Host** to **@** (or your subdomain)
7. Set **Value** to `+"`%s`"+`
8. Set **TTL** to **Automatic**
9. Click the green checkmark to save`, targetCNAME)

	case strings.Contains(lower, "cloudflare"):
		return fmt.Sprintf(`### Cloudflare DNS Setup

1. Log in to your [Cloudflare dashboard](https://dash.cloudflare.com/)
2. Select your domain
3. Go to **DNS** > **Records**
4. Click **Add record**
5. Set **Type** to **CNAME**
6. Set **Name** to **@** (or your subdomain)
7. Set **Target** to `+"`%s`"+`
8. Set **Proxy status** to **DNS only** (grey cloud)
9. Click **Save**

> **Important**: The proxy must be set to DNS only (grey cloud) for TLS to work correctly.`, targetCNAME)

	case strings.Contains(lower, "dynadot"):
		return fmt.Sprintf(`### Dynadot DNS Setup

1. Log in to your [Dynadot account](https://www.dynadot.com/account/)
2. Go to **My Domains** > **Manage Domains**
3. Select your domain and click **DNS Settings**
4. Under **Domain Record (required)**, set type to **CNAME**
5. Set the value to `+"`%s`"+`
6. Click **Save DNS**`, targetCNAME)

	case strings.Contains(lower, "google"):
		return fmt.Sprintf(`### Google Domains DNS Setup

1. Log in to [Google Domains](https://domains.google.com/)
2. Select your domain
3. Go to **DNS** in the left sidebar
4. Under **Custom records**, click **Manage custom records**
5. Click **Create new record**
6. Set **Type** to **CNAME**
7. Set **Host name** to **@** (or your subdomain)
8. Set **Data** to `+"`%s`"+`
9. Click **Save**`, targetCNAME)

	case strings.Contains(lower, "name.com"):
		return fmt.Sprintf(`### Name.com DNS Setup

1. Log in to your [Name.com account](https://www.name.com/account/)
2. Select your domain
3. Go to **DNS Records**
4. Click **Add Record**
5. Set **Type** to **CNAME**
6. Set **Host** to your desired subdomain (or leave blank for root)
7. Set **Answer** to `+"`%s`"+`
8. Click **Add Record**`, targetCNAME)

	default:
		return fmt.Sprintf(`### DNS Setup Instructions

Add a **CNAME** record with your DNS provider:

| Setting | Value |
|---------|-------|
| **Type** | CNAME |
| **Name/Host** | @ (or your subdomain) |
| **Value/Target** | `+"`%s`"+` |
| **TTL** | 3600 (1 hour) |

After adding the record, return here and click **Verify DNS** to confirm the setup.`, targetCNAME)
	}
}
